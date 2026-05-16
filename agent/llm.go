package agent

import (
	"bufio"
	"bytes"
	"deepx/tools"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type AgentMode string

const (
	AgentMode_Plan AgentMode = "plan"
	AgentMode_Auto AgentMode = "auto"

	// 主 agent 单轮对话内的工具调用上限。
	// 100 轮给复杂多步任务留足空间(典型场景:CreatePlan 之后还要做修改 + 测试 + 修复循环)。
	// 触顶通常意味着 LLM 在死循环,会返回错误中断。
	mainAgentMaxRounds = 100
)

// ModelEntry 单个 role 的完整连接配置 — base_url / model id / api_key 三件套。
// 设计目标:flash 和 pro 可以指向不同 provider(比如 flash 用本地 vllm,pro 用 DeepSeek 云端)。
type ModelEntry struct {
	BaseURL string
	Model   string
	APIKey  string
}

// ModelConfig 双模型配置。Flash 处理简单/查询型任务,Pro 处理复杂/规划型任务。
// 入口路由(keyword_router.go)决定本轮起手用哪个;每个 plan 节点也可以独立指定 model 字段。
// 两个 entry 可以共用同一个 BaseURL/APIKey,只 Model 不同(常见场景);也可以完全分离。
type ModelConfig struct {
	Flash ModelEntry
	Pro   ModelEntry
}

// === 给 TUI 的事件 ===

type TokenMsg string                  // 助手正式回复(content)的文本增量,会展示到 chat
type ReasoningTokenMsg string         // 模型思考过程(reasoning_content)增量,TUI 用它驱动 spinner,不展示文字
type StreamErrMsg struct{ Err error } // 错误
type StreamDoneMsg struct{}           // 整个会话回合结束
type ToolCallStartMsg struct {        // 即将调用工具
	Name string
	Args string
}
type ToolCallResultMsg struct { // 工具调用返回
	Name    string
	Output  string
	Success bool
}

// ModelSwitchMsg 通知 UI 本轮起手选择的模型。每轮仅在开头发一次,本轮不再变化。
type ModelSwitchMsg struct {
	Role    string // "flash" or "pro"
	ModelID string // 实际 model id
	Reason  string // 可选,描述路由依据(目前为空,B 方案静默路由)
}

// HistoryUpdateMsg 让 UI 用最新的 history 替换本地副本(包含 assistant tool_calls / tool 结果)
type HistoryUpdateMsg struct {
	History []ChatMessage
}

// === OpenAI 协议结构 ===

// ChatMessage 是历史记录与请求体共用的消息结构。
// 文本消息走 Content (string),包含图片的多模态消息走 ContentParts (array)。
// 两个字段都是内存表示, JSON 序列化由 MarshalJSON 统一处理。
type ChatMessage struct {
	Role             string        `json:"-"`
	Content          string        `json:"-"`
	ContentParts     []ContentPart `json:"-"`
	ReasoningContent string        `json:"-"`
	ToolCalls        []ToolCall    `json:"-"`
	ToolCallID       string        `json:"-"`
	Name             string        `json:"-"`
}

// ContentPart 是 OpenAI 多模态消息里 content 数组的一个元素。
// Type 取值: "text" | "image_url"。
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

type ImageURL struct {
	URL string `json:"url"`
}

// MarshalJSON 根据是否带图,把 content 序列化成 string 或 array。
// 同时保证 tool 消息 / 纯 assistant 工具调用消息 在 content 为空时不出现该字段。
func (m ChatMessage) MarshalJSON() ([]byte, error) {
	type wire struct {
		Role             string     `json:"role"`
		Content          any        `json:"content,omitempty"`
		ReasoningContent string     `json:"reasoning_content,omitempty"`
		ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID       string     `json:"tool_call_id,omitempty"`
		Name             string     `json:"name,omitempty"`
	}
	w := wire{
		Role:             m.Role,
		ReasoningContent: m.ReasoningContent,
		ToolCalls:        m.ToolCalls,
		ToolCallID:       m.ToolCallID,
		Name:             m.Name,
	}
	switch {
	case len(m.ContentParts) > 0:
		w.Content = m.ContentParts
	case m.Content != "":
		w.Content = m.Content
	case m.Role == "assistant" && len(m.ToolCalls) == 0:
		// DeepSeek (和部分严格的 OpenAI 兼容实现) 要求 assistant 消息至少含 content 或 tool_calls。
		// 当模型只输出 reasoning_content 时,两者都缺会导致下轮请求被 API 400 拒绝。
		// 这里兜底发个空字符串 content 满足契约;omitempty 对非 nil interface(空字符串包裹后)不生效。
		w.Content = ""
	}
	return json.Marshal(w)
}

type ToolCall struct {
	ID       string       `json:"id"`
	Type     string       `json:"type"`
	Index    int          `json:"index,omitempty"`
	Function ToolCallFunc `json:"function"`
}

type ToolCallFunc struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type chatRequest struct {
	Model     string                 `json:"model"`
	MaxTokens int                    `json:"max_tokens"`
	Stream    bool                   `json:"stream"`
	Messages  []ChatMessage          `json:"messages"`
	Tools     []tools.OpenAIToolSpec `json:"tools,omitempty"`
}

type sseChunk struct {
	Choices []struct {
		Delta struct {
			Content          string     `json:"content"`
			ReasoningContent string     `json:"reasoning_content"`
			ToolCalls        []ToolCall `json:"tool_calls"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
}

// === 入口 ===

// StartStream 启动一个对话回合。入口由 RouteByKeyword 决定起手模型(flash/pro),
// 本轮锁定该模型不再切换。复杂任务由模型主动调 CreatePlan 拆分,plan 节点的 model 字段
// 由 sub-agent 按需路由,实现细粒度的模型选择。
func StartStream(
	models ModelConfig,
	history []ChatMessage,
	maxTokens int,
	mode AgentMode,
	workspace string,
	skillCatalog string, // 见下方 system prompt 注入逻辑;空串表示当前没有 skill
) (tea.Cmd, <-chan tea.Msg) {
	ch := make(chan tea.Msg, 128)

	go func() {
		defer close(ch)

		convo := append([]ChatMessage(nil), history...)
		// 从 history 里找最后一条 user 消息,作为派给子 agent 的"任务背景"
		latestUserTask := ""
		for i := len(history) - 1; i >= 0; i-- {
			if history[i].Role == "user" {
				latestUserTask = history[i].Content
				break
			}
		}
		if workspace != "" {
			// 在首轮注入 system 提示:当前工作目录 + 任务拆解 + plan 节点的 model 选择指南。
			// 入口模型已经由 keyword router 决定(flash 或 pro);模型自行判断要不要 CreatePlan 拆任务。
			if len(convo) == 0 || convo[0].Role != "system" {
				// system prompt 只放跨工具的行为规约 — 触发条件 / 调用语法 / 拆分模式等
				// 每个工具自己的 description 已经覆盖,这里不重复,避免每轮浪费 token。
				sysBase := fmt.Sprintf(`你是 DeepX,一个自主编码 agent,跑在用户的本地开发环境里。

通过工具帮用户:理解代码 · 编辑文件 · 写代码 · 调试 · 执行 shell 命令 · 拆任务 · 推理架构。

# 核心原则
- 准确、简洁,行动优先于解释
- 增量解决问题
- 不假装做过没做的事,不编造文件内容 / 命令输出 / 工具结果
- 用工具拿事实,不要猜

# 工具使用
改代码前先 inspect 相关文件、理解上下文,改动最小化。编辑时保持现有风格,不顺手做不相关的重构,默认保持向后兼容(除非用户明确要求)。

# 任务处理
- 简单任务:直接做,不要过度规划
- 复杂任务:先短计划,分步执行,进度清晰
- 调试:找根因,不臆测;通过工具验证假设

# Shell 安全
- 不主动执行破坏性命令(rm -rf / drop / force push 等)
- 优先可逆操作,destructive 操作先确认

# 响应风格
- 简短、技术性,列表优于长段落
- 避免营销话术 / 重复显而易见的信息
- 只在必要时解释
- markdown 表格 cell 内容**禁用 emoji**(终端字体对 emoji cell 宽度渲染跟程序估算有偏差,会让列对齐错乱);emoji 可放表格外段落 / 列表项使用

# 失败处理
- 信息不足:继续 inspect 文件;必要时问一个聚焦问题
- 任务模糊:陈述假设,按最安全解读 proceed

# 运行时
- 当前工作目录:%s`,
					workspace,
				)
				// Skill 索引:tui 启动时扫 ~/.deepx/skills/ + session/skills/ 拼出来,
				// 这里只展示给 LLM "有哪些"和"是干啥的",正文按需通过 LoadSkill 工具拿。
				if skillCatalog != "" {
					sysBase += "\n\n**Available Skills**(用户预定义的指令包,description 跟当前任务对得上就调 `LoadSkill` 加载正文)\n" + skillCatalog
				}
				convo = append([]ChatMessage{{Role: "system", Content: sysBase}}, convo...)
			}
		}

		// 当前活跃角色,起手 flash。升级到 pro 后不回头。
		role := tools.RoleFlash
		currentEntry := models.Flash
		if currentEntry.Model == "" {
			currentEntry = models.Pro // 退化:flash 未设时,直接用 pro
			role = tools.RolePro
		}

		// 入口路由:纯本地关键词 + 长度判定,零延迟,无 LLM 调用。
		// 命中复杂关键词 / 消息 > 500 字 → pro;否则 flash。
		// 本轮锁定该模型,主循环不再切换 — plan 节点可独立指定 model 字段,
		// 由 sub-agent 按节点要求路由,跟"起手模型"解耦。
		if latestUserTask != "" && models.Pro.Model != "" {
			choice := RouteByKeyword(latestUserTask)
			if choice == "pro" {
				role = tools.RolePro
				currentEntry = models.Pro
			}
		}
		ch <- ModelSwitchMsg{Role: role, ModelID: currentEntry.Model}

		toolSpecs := buildToolSpecs(mode, role)

		// 100 轮上限给复杂多步任务留足空间(read → analyze → edit → test → fix 这种循环)。
		// 触顶通常说明 LLM 在死循环或反复试错,需要返回错误让用户介入。
		for round := 0; round < mainAgentMaxRounds; round++ {
			// 不再主动 strip reasoning_content:本轮不切换模型,thinking 模型仍按需回传,
			// 非 thinking 模型对 history 里的字段视而不见。若个别模型报错,
			// streamOnce 仍有 errReasoningRequired retry 兜底。
			assistantContent, reasoning, toolCalls, err := streamOnce(
				currentEntry.APIKey, currentEntry.BaseURL, currentEntry.Model,
				convo, maxTokens, toolSpecs, ch,
			)
			if err != nil {
				ch <- StreamErrMsg{err}
				return
			}

			// 把本轮 assistant 回复写入历史(含 reasoning_content,thinking 模型下轮需要)
			convo = append(convo, ChatMessage{
				Role:             "assistant",
				Content:          assistantContent,
				ReasoningContent: reasoning,
				ToolCalls:        toolCalls,
			})

			if len(toolCalls) == 0 {
				ch <- HistoryUpdateMsg{History: convo}
				ch <- StreamDoneMsg{}
				return
			}

			// 执行每个工具调用,把结果加进 convo。
			// 三个工具被 deepx 拦截 (不走 Executor):
			//   - CreatePlan         → 解析后产 PlanCreatedMsg,触发 DAG 调度
			//   - UpdateTaskStatus   → 解析后产 TaskStatusMsg,UI 更新单项状态
			//   - SwitchModel        → 改本轮 currentEntry / role,通过 ModelSwitchMsg 通知 UI
			// 拦截后仍要给 LLM 一个 fake tool result,让 OpenAI 工具循环能正常推进。
			for _, tc := range toolCalls {
				ch <- ToolCallStartMsg{Name: tc.Function.Name, Args: tc.Function.Arguments}

				var result tools.ToolResult
				switch tc.Function.Name {
				case "CreatePlan":
					plans, perr := parseCreatePlanArgs(tc.Function.Arguments)
					if perr != nil {
						result = tools.ToolResult{Output: perr.Error(), Success: false}
					} else {
						// 1. 通知 UI 渲染 plan 树
						ch <- PlanCreatedMsg{Plans: plans}
						// 2. 拍平成 DAG 节点并同步执行
						nodes := flattenPlans(plans)
						exec := func(n *schedulerNode, preds map[string]string) (string, error) {
							res := runSubAgent(subAgentInput{
								Models:       models,
								Entry:        resolveModelEntry(n.Model, models),
								NodeID:       n.ID,
								NodeTitle:    n.Title,
								UserTask:     latestUserTask,
								Predecessors: preds,
								Workspace:    workspace,
								MaxTokens:    maxTokens,
								Mode:         mode,
							})
							if res.Err != nil {
								return "", res.Err
							}
							return res.Summary, nil
						}
						final := runDAG(nodes, exec, ch)
						// 3. 拼汇总 ToolResult 给 pro,让它写最终给用户的总结
						var summary strings.Builder
						summary.WriteString(fmt.Sprintf("已执行完毕,共 %d 个节点。\n", len(final)))
						successCount := 0
						for _, n := range final {
							icon := "?"
							switch n.Status {
							case PlanStatusDone:
								icon = "✓"
								successCount++
							case PlanStatusFailed:
								icon = "✗"
							case PlanStatusBlocked:
								icon = "⏸"
							}
							summary.WriteString(fmt.Sprintf("  %s [%s] %s — %s\n", icon, n.ID, n.Title, n.Summary))
						}
						summary.WriteString(fmt.Sprintf("\n%d/%d 成功。请基于以上结果给用户写一段简洁的最终总结。", successCount, len(final)))
						result = tools.ToolResult{
							Output:  summary.String(),
							Success: successCount > 0,
						}
					}
				case "UpdateTaskStatus":
					id, st, summary, perr := parseUpdateTaskStatusArgs(tc.Function.Arguments)
					if perr != nil {
						result = tools.ToolResult{Output: perr.Error(), Success: false}
					} else {
						ch <- TaskStatusMsg{ID: id, Status: st, Summary: summary}
						result = tools.ToolResult{
							Output:  fmt.Sprintf("已记录: %s = %s", id, st),
							Success: true,
						}
					}
				case "SwitchModel":
					// 单向升级到 pro。已经在 pro 是 no-op,flash → pro 实际换 currentEntry。
					// 切换立即生效:本轮工具循环下一次 streamOnce 用新 entry。
					reason := parseSwitchModelReason(tc.Function.Arguments)
					if role == tools.RolePro {
						result = tools.ToolResult{
							Output:  "已经在 pro 模型,无需切换。继续完成任务即可。",
							Success: true,
						}
					} else if models.Pro.Model == "" {
						result = tools.ToolResult{
							Output:  "pro 模型未配置(model.yaml 里 pro.model 为空),无法升级。继续用 flash 处理。",
							Success: false,
						}
					} else {
						role = tools.RolePro
						currentEntry = models.Pro
						toolSpecs = buildToolSpecs(mode, role) // 角色切换后工具白名单可能变,重算
						ch <- ModelSwitchMsg{Role: role, ModelID: currentEntry.Model, Reason: reason}
						result = tools.ToolResult{
							Output:  fmt.Sprintf("已切到 pro 模型 (%s)。本轮剩余请求 + reasoning 用 pro 处理。", currentEntry.Model),
							Success: true,
						}
					}
				default:
					result = executeTool(tc, mode, role)
				}

				ch <- ToolCallResultMsg{
					Name:    tc.Function.Name,
					Output:  result.Output,
					Success: result.Success,
				}
				convo = append(convo, ChatMessage{
					Role:       "tool",
					ToolCallID: tc.ID,
					Name:       tc.Function.Name,
					Content:    result.Output,
				})
			}
			ch <- HistoryUpdateMsg{History: convo}
		}

		ch <- StreamErrMsg{fmt.Errorf("超过工具调用轮数上限")}
	}()

	return ListenToStream(ch), ch
}

// streamOnce 发起一次 chat/completions 请求,返回 (content, reasoning_content, tool_calls)。
func streamOnce(
	apiKey, baseURL, modelID string,
	convo []ChatMessage,
	maxTokens int,
	toolSpecs []tools.OpenAIToolSpec,
	ch chan<- tea.Msg,
) (string, string, []ToolCall, error) {

	body, err := json.Marshal(chatRequest{
		Model:     modelID,
		MaxTokens: maxTokens,
		Stream:    true,
		Messages:  convo,
		Tools:     toolSpecs,
	})
	if err != nil {
		return "", "", nil, err
	}

	req, err := http.NewRequest("POST", baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", "", nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}

	var (
		contentBuilder   strings.Builder
		reasoningBuilder strings.Builder
		inReasoning      bool
		toolBuf          = map[int]*ToolCall{}
	)

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var chunk sseChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta

		if delta.ReasoningContent != "" {
			// reasoning 走单独消息类型,TUI 只用它驱动 spinner,不写入对话区
			inReasoning = true
			reasoningBuilder.WriteString(delta.ReasoningContent)
			ch <- ReasoningTokenMsg(delta.ReasoningContent)
		}
		if delta.Content != "" {
			inReasoning = false
			contentBuilder.WriteString(delta.Content)
			ch <- TokenMsg(delta.Content)
		}
		_ = inReasoning // 仅用于 reasoning/content 切换语义,保留变量便于将来加 boundary 处理
		for _, tc := range delta.ToolCalls {
			cur, ok := toolBuf[tc.Index]
			if !ok {
				cur = &ToolCall{Index: tc.Index, Type: "function"}
				toolBuf[tc.Index] = cur
			}
			if tc.ID != "" {
				cur.ID = tc.ID
			}
			if tc.Type != "" {
				cur.Type = tc.Type
			}
			if tc.Function.Name != "" {
				cur.Function.Name = tc.Function.Name
			}
			if tc.Function.Arguments != "" {
				cur.Function.Arguments += tc.Function.Arguments
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return contentBuilder.String(), reasoningBuilder.String(), nil, err
	}

	// 按 index 升序拼装最终 tool_calls
	var toolCalls []ToolCall
	for i := 0; i < len(toolBuf); i++ {
		if tc, ok := toolBuf[i]; ok {
			toolCalls = append(toolCalls, *tc)
		}
	}
	return contentBuilder.String(), reasoningBuilder.String(), toolCalls, nil
}

// ListenToStream 把单条事件转给 bubbletea。
func ListenToStream(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// === 工具白名单 / 执行 ===

// buildToolSpecs 按权限模式 (plan/auto) + 当前模型角色 (flash/pro/subagent) 过滤工具列表。
func buildToolSpecs(mode AgentMode, role string) []tools.OpenAIToolSpec {
	var out []tools.OpenAIToolSpec
	for _, t := range tools.Tools {
		if !allowedInMode(t, mode) {
			continue
		}
		if !allowedForRole(t, role) {
			continue
		}
		out = append(out, t.ToOpenAISpec())
	}
	return out
}

func allowedInMode(t tools.Tool, mode AgentMode) bool {
	switch mode {
	case AgentMode_Plan:
		return t.ReadOnly // plan 模式只允许只读工具
	case AgentMode_Auto:
		return true
	default:
		return t.ReadOnly
	}
}

// allowedForRole 检查工具的 Roles 限制。Roles 为空表示对所有角色可见。
func allowedForRole(t tools.Tool, role string) bool {
	if len(t.Roles) == 0 {
		return true
	}
	for _, r := range t.Roles {
		if r == role {
			return true
		}
	}
	return false
}

func executeTool(tc ToolCall, mode AgentMode, role string) tools.ToolResult {
	t := tools.Find(tc.Function.Name)
	if t == nil {
		return tools.ToolResult{
			Output:  fmt.Sprintf("未注册的工具: %s", tc.Function.Name),
			Success: false,
		}
	}
	if !allowedInMode(*t, mode) {
		return tools.ToolResult{
			Output:  fmt.Sprintf("工具 %s 在当前模式 (%s) 不可用", t.Name, mode),
			Success: false,
		}
	}
	if !allowedForRole(*t, role) {
		return tools.ToolResult{
			Output:  fmt.Sprintf("工具 %s 对当前模型角色 (%s) 不可用", t.Name, role),
			Success: false,
		}
	}
	args, err := tools.ParseArgs(tc.Function.Arguments)
	if err != nil {
		return tools.ToolResult{
			Output:  fmt.Sprintf("参数解析失败: %v / raw=%s", err, tc.Function.Arguments),
			Success: false,
		}
	}
	return t.Executor(args)
}
