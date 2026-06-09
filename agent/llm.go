package agent

import (
	"bufio"
	"bytes"
	"context"
	"deepx/tools"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"

	tea "charm.land/bubbletea/v2"
)

type AgentMode string

const (
	AgentMode_Plan   AgentMode = "plan"
	AgentMode_Auto   AgentMode = "auto"
	AgentMode_Review AgentMode = "review"

	// maxNoProgressRounds:连续这么多轮工具调用「全部失败、无任何成功」即判定卡死/空转,暂停。
	// 这是主 agent 唯一的"主动"熔断,拦反复改同一处失败、反复跑同一条报错命令这类失败循环;
	// 只要某轮有任一工具成功就重置计数,productive 的长任务不受影响。
	//
	// 刻意不设"绝对轮数上限"(对标 Claude Code 交互式主循环):长任务跑到模型自己停为止,
	// 不在第 N 轮硬性中断。终止由三道天然边界保证 —— ① convo 每轮只增,上下文单调增长,
	// 迟早撞模型上下文窗口让 streamOnce 优雅报错退出;② 本断路器拦失败循环;③ 用户 ESC 随时中断。
	// (见 issue #84:旧的固定 100 轮上限会把合法长任务在中途打断、需手动继续。)
	maxNoProgressRounds = 15

	// compactTriggerPct:单个 turn 内,上一轮真实 prompt tokens 占模型上下文窗口达到这个百分比,
	// 就在下次请求前自动压缩历史(对标 Claude Code 的 auto-compact:压缩+继续,而非熔断停)。
	// 取 80 留 ~20% 给本轮输出;压缩后历史缩到 ~20% 窗口,不会立刻反复触发。
	compactTriggerPct = 80
)

// ModelEntry 单个 role 的完整连接配置 — base_url / model id / api_key 三件套。
// 设计目标:flash 和 pro 可以指向不同 provider(比如 flash 用本地 vllm,pro 用 DeepSeek 云端)。
type ModelEntry struct {
	BaseURL       string
	Model         string
	APIKey        string
	ContextWindow int // 上下文窗口大小(tokens)
	MaxTokens     int // 单次生成的 completion 上限(tokens);字段顺序需与 config.ModelEntry 一致
	// 推理参数(跨供应商通用,空值不发送)。详见 config.ModelEntry 同名字段注释。
	ReasoningEffort string
	Thinking        string
	// Vision 表示该模型是否支持图片输入(由启动探测的缓存填入,见 tui)。决定带图消息发请求时
	// 渲染成 base64 image_url(true)还是路径文本走 OCR(false)。
	Vision bool
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
	Name     string
	Args     string
	ReviewCh chan bool // review 模式下的审核通道,nil = 无需审核
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

// CompactedMsg 在「单个长 turn 内」自动压缩后发给 UI:摘要存在 session(每轮注入 system 尾部),
// 而 system 不入 history(HistoryUpdateMsg 会剥掉)——必须用这条把新摘要带出来,否则下轮上下文丢失。
// history 截断由随后的 HistoryUpdateMsg 同步。Turns = 本次被压掉的 user 轮数,仅供 UI 提示。
type CompactedMsg struct {
	Summary string
	Turns   int
}

// VisionUnsupportedMsg:本以为支持视觉的模型,实际发图被端点拒(如 404 "no image input")。
// agent 已自动改用 OCR 重发,这里通知 TUI 把该模型标记为无视觉、纠正缓存,后续不再发 base64。
type VisionUnsupportedMsg struct {
	Model   string
	BaseURL string
}

// PrefixSnapshotMsg 携带本轮"实际发送"的前缀(system 文本 + tool specs JSON)。
// TUI 持久化它,用于重启变化检测与缓存友好压缩复刻旧前缀。每轮发一次。
type PrefixSnapshotMsg struct {
	Model         string // 本轮实际使用的 model ID(缓存按模型分,压缩需同模型才命中)
	SystemPrompt  string
	ToolSpecsJSON string
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
	// ImagePaths 是这条消息附带的图片绝对路径(粘贴落盘的图)。**规范形态只存路径、不存 base64**
	// (历史小、缓存友好)。发请求前由 renderConvoImages 按"当轮模型支不支持视觉"即时渲染:
	// 支持 → 读成 base64 image_url;不支持 → 路径替回文本走 OCR。gob 持久化(导出字段)。
	ImagePaths []string `json:"-"`
	// WorkingMode 记录这条 user 消息**提交当轮所处的工作模式**(只对 user 消息有意义)。
	// 钉死不变:发请求前由 renderWorkingMode 按**每条消息自己的** mode 确定性渲染后缀,
	// 切换当前模式不会改写历史消息的后缀 → 历史逐字节稳定、前缀缓存不 miss。空值兜底为默认 kp。
	// 同 ImagePaths 走"规范形态只存标签、发送那刻才渲染"的思路。gob 持久化(导出字段)。
	WorkingMode WorkingMode `json:"-"`
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
		ReasoningContent any        `json:"reasoning_content,omitempty"`
		ToolCalls        []ToolCall `json:"tool_calls,omitempty"`
		ToolCallID       string     `json:"tool_call_id,omitempty"`
		Name             string     `json:"name,omitempty"`
	}
	w := wire{
		Role:       m.Role,
		ToolCalls:  m.ToolCalls,
		ToolCallID: m.ToolCallID,
		Name:       m.Name,
	}
	switch {
	case m.ReasoningContent != "":
		w.ReasoningContent = m.ReasoningContent
	case m.Role == "assistant":
		w.ReasoningContent = ""
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
	Model string `json:"model"`
	// omitempty:max_tokens=0 时不发这个字段,让模型走自己的默认输出上限
	// (不同模型默认上限差很多,config 里某些供应商故意留 0 用默认,见 config.modelConfig)。
	MaxTokens     int                    `json:"max_tokens,omitempty"`
	Stream        bool                   `json:"stream"`
	StreamOptions *streamOptions         `json:"stream_options,omitempty"`
	Messages      []ChatMessage          `json:"messages"`
	Tools         []tools.OpenAIToolSpec `json:"tools,omitempty"`
	// 推理参数 —— **两个并列的顶层字段**(对照 DeepSeek 官方文档):
	//
	//   {"thinking": {"type": "enabled"}, "reasoning_effort": "high"}
	//
	// 不要写成嵌套(reasoning_effort 不是 thinking 的子字段)。
	// 空值严格 omitempty —— 用户不设就完全没有对应 JSON 键,任何不支持的模型
	// (MiMo / 未来 OpenAI-兼容新模型)都不会被多余字段炸 400。
	Thinking        *thinkingOption `json:"thinking,omitempty"`
	ReasoningEffort string          `json:"reasoning_effort,omitempty"`
}

// thinkingOption 是 DeepSeek 思考开关的请求体格式:`{"type": "enabled"}` 或 `{"type": "disabled"}`。
// DeepSeek 默认 enabled,MiMo 默认 disabled。
type thinkingOption struct {
	Type string `json:"type"`
}

// buildThinkingOption 把 ModelEntry.Thinking 字符串转成请求体 thinking 对象。
// 空 / 未识别值返回 nil → omitempty 整个键消失。
func buildThinkingOption(v string) *thinkingOption {
	switch v {
	case "enabled", "disabled":
		return &thinkingOption{Type: v}
	}
	return nil
}

// validateReasoningEffort 把 ModelEntry.ReasoningEffort 过一遍白名单,未识别值
// (yaml 笔误、未来废弃档等)返回 "" → omitempty 不发,防止脏值送到服务端导致 400。
//
// 取值(DeepSeek 文档):
//   - canonical: high (默认) | max
//   - 兼容别名:  low / medium → high;xhigh → max
//
// 白名单纳入全部 5 个,既覆盖 DeepSeek canonical,也覆盖 OpenAI o1/o3 风格(low/medium/high)
// —— 后者拼到 DeepSeek 自动映射,拼到 OpenAI-兼容端就是合法标准取值。
func validateReasoningEffort(v string) string {
	switch v {
	case "low", "medium", "high", "max", "xhigh":
		return v
	}
	return ""
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// UsageInfo 单次 API 调用的 token 用量,含缓存命中信息。
//
// 缓存命中字段各供应商口径不同:DeepSeek 直接给 prompt_cache_hit_tokens;
// OpenAI 标准(mimo 等)放在嵌套的 prompt_tokens_details.cached_tokens。
// normalize() 把后者回填到 PromptCacheHitTokens,使下游显示逻辑只认一套字段。
type UsageInfo struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	TotalTokens           int `json:"total_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`  // DeepSeek 专有
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"` // DeepSeek 专有
	PromptTokensDetails   struct {
		CachedTokens int `json:"cached_tokens"` // OpenAI 标准(mimo 等)
	} `json:"prompt_tokens_details"`
}

// normalize 统一缓存命中口径:DeepSeek 字段缺失而 OpenAI 标准字段存在时,
// 用 cached_tokens 回填 hit,并据 prompt_tokens 推出 miss。
func (u *UsageInfo) normalize() {
	if u == nil {
		return
	}
	if u.PromptCacheHitTokens == 0 && u.PromptTokensDetails.CachedTokens > 0 {
		u.PromptCacheHitTokens = u.PromptTokensDetails.CachedTokens
		if miss := u.PromptTokens - u.PromptCacheHitTokens; miss > 0 {
			u.PromptCacheMissTokens = miss
		}
	}
}

// UsageMsg 从 agent goroutine 发给 TUI 的单次 API 用量。
type UsageMsg struct {
	Usage UsageInfo
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
	Usage *UsageInfo `json:"usage,omitempty"`
}

// chatResponse 非流式响应的完整结构。
type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

// CallOnce 发起一次非流式 chat completion 调用,直接返回 content 文本。
// 不带 tools 参数,适用于摘要生成等一次性文本生成场景。
func CallOnce(ctx context.Context, apiKey, baseURL, modelID string, convo []ChatMessage, maxTokens int) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:     modelID,
		MaxTokens: maxTokens,
		Stream:    false,
		Messages:  convo,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}

	var result chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return result.Choices[0].Message.Content, nil
}

// CallWithTools 与 CallOnce 类似(非流式、返回 content),但额外带上 tools 参数。
// 用于缓存友好的压缩:摘要请求复刻会话的 [system][tools][history] 前缀,只在末尾追加压缩指令,
// 从而命中已缓存的前缀(tools 必须和被缓存的那次逐字节一致才命中,故由调用方传入旧 specs)。
func CallWithTools(ctx context.Context, apiKey, baseURL, modelID string, convo []ChatMessage, toolSpecs []tools.OpenAIToolSpec, maxTokens int) (string, error) {
	body, err := json.Marshal(chatRequest{
		Model:     modelID,
		MaxTokens: maxTokens,
		Stream:    false,
		Messages:  convo,
		Tools:     toolSpecs,
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}
	var result chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", err
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("no choices in response")
	}
	return result.Choices[0].Message.Content, nil
}

// MarshalToolSpecs 把工具 specs 序列化成 JSON 字符串,供快照持久化(逐字节)。
func MarshalToolSpecs(toolSpecs []tools.OpenAIToolSpec) string {
	b, err := json.Marshal(toolSpecs)
	if err != nil {
		return ""
	}
	return string(b)
}

// UnmarshalToolSpecs 从快照 JSON 还原工具 specs,供压缩复刻旧前缀。空串/失败返回 nil。
func UnmarshalToolSpecs(s string) []tools.OpenAIToolSpec {
	if s == "" {
		return nil
	}
	var out []tools.OpenAIToolSpec
	if err := json.Unmarshal([]byte(s), &out); err != nil {
		return nil
	}
	return out
}

// === 入口 ===

// StartStream 启动一个对话回合。入口由 RouteByKeyword 决定起手模型(flash/pro),
// 本轮锁定该模型不再切换。复杂任务由模型主动调 CreatePlan 拆分,plan 节点的 model 字段
// 由 sub-agent 按需路由,实现细粒度的模型选择。
// coreSystemPrompt 是主 agent 与子 agent **共用**的稳定头部:身份 + 行为规则 + workspace + skill 目录。
// 主/子在同一 workspace、同一 skill 目录下逐字节一致 —— 这是缓存前缀共享的基础。
// 主 agent 在其后接「会话摘要」,子 agent 在其后接「节点目标」等专属尾部(见各自构造处)。
func coreSystemPrompt(workspace, skillCatalog string) string {
	base := fmt.Sprintf(`你是 DeepX,一个自主编码 agent,跑在用户的本地开发环境里。

通过工具帮用户:理解代码 · 编辑文件 · 写代码 · 调试 · 执行 shell 命令 · 拆任务 · 推理架构。1

# 核心原则
- 准确、简洁,行动优先于解释
- 增量解决问题
- 不假装做过没做的事,不编造文件内容 / 命令输出 / 工具结果
- 用工具拿事实,不要猜

# 工具使用
- 改代码前先 inspect 相关文件、理解上下文,改动最小化。编辑时保持现有风格,不顺手做不相关的重构,默认保持向后兼容(除非用户明确要求)。
- 查代码符号(函数/类型/方法)的定义、调用关系、实现者、继承请优先用 CodeGraph工具(更准、不误命中注释/字符串)。
- 需要用户在**有限、明确的选项**里做选择或拍板时(需求确认、技术选型、A/B 方案、是否包含某功能等),**必须调用 AskUser 工具弹窗让用户勾选**,可一次问多道;不要把选项写成文字列表让用户敲字回复。开放性、需要自由表达的问题才用文字提问。

# 技能skill使用
- 实现功能、修复 bug、重构或 review 代码时,遵循本轮用户消息尾部「工作模式」指明的方法论 skill(加载其正文并执行),不要使用未指明的其它工作模式 skill。

# 任务规划
- 简单/单步任务:直接做,不要过度规划。
- 多步顺序任务(≥3 步且有先后,如从零搭应用 / 跨多文件改动 / 调试修复链路):动手前先用 Todo 列出全部步骤,之后每开始或完成一步就重发整张 todos 更新状态,让用户看到进度。你自己逐步执行,不派子 agent。
- **别提前收尾**:任务没真正完成前(尤其 todo 里还有未完成项),不要只回一段总结就停下——继续调用工具推进到底。只在全部做完、或确实卡住需要用户提供信息时才结束。像"分析/梳理 XX 流程"这类调研任务,要把相关文件都查透、给出完整结论,不能查两三个文件就收。
- 真正可并行、彼此独立的扇出任务才用 CreatePlan 拆 DAG(会派并发子 agent 各自跑);搭一个连贯的应用别用 CreatePlan。

# Shell 安全
- 不主动执行破坏性命令(rm -rf / drop / force push 等)
- 优先可逆操作,destructive 操作先确认
- Write/Update 因目标在 workspace 外被拒时,由用户确认或自行处理,不要自作主张绕过。
- docker 沙箱模式下(命令在 Linux 容器里跑、~ 解析为 /root、宿主路径如 /Users/… 不存在):只有项目 workspace 挂载在 /workspace 且持久化,写到容器其它位置(含 ~ 与宿主绝对路径)是临时文件、容器销毁即丢。此时要在 workspace 外建/改宿主文件,别在容器里写一份就声称成功——直接告诉用户该路径在 docker 沙箱不可达、只有项目目录可用,需要的话切到 native/off。

# 模式限制
- plan 模式:禁止 Write / Update / Bash,其余工具均可使用。
- auto 模式:全部工具均可使用,无需人工审核。
- review 模式:所有工具均可使用,但 Write / Update / Bash 需要人工审核确认后才执行,其余工具自动执行。
- 每次模式切换时会有一条系统通知明确告诉你当前处于什么模式,严格遵守。
- 如果当前模式禁止了你需要的工具,告诉用户"当前是 plan 模式,该操作不允许,请用 /auto 切换到 auto 模式"。不要试图绕过限制。

# 响应风格
- 简短、技术性,列表优于长段落
- 避免营销话术/重复显而易见的信息
- 只在必要时解释

# 失败处理
- 信息不足: 继续inspect文件,必要时问一个聚焦问题
- 任务模糊: 陈述假设,按最安全解读 proceed

# 运行时
- 当前工作目录:%s`,
		workspace,
	)
	if skillCatalog != "" {
		base += "\n\n**Available Skills**(用户预定义的指令包,description 跟当前任务对得上就调 `LoadSkill` 加载正文)\n" + skillCatalog
	}
	return base
}

// BuildSystemPrompt 主 agent 的 system prompt = 共用核心 + 会话摘要尾部。
// 摘要垫在最后:核心 + skill 那段会话内字节不变,即使摘要每次压缩都变,前缀仍命中,
// 失效点只从摘要开始(详见前缀缓存优化设计)。
func BuildSystemPrompt(workspace, skillCatalog, summary string) string {
	base := coreSystemPrompt(workspace, skillCatalog)
	if summary != "" {
		base += "\n\n# 会话摘要(此前对话的压缩,延续上下文)\n" + summary
	}
	return base
}

func StartStream(
	ctx context.Context,
	models ModelConfig,
	history []ChatMessage,
	mode AgentMode,
	workspace string,
	skillCatalog string, // 见下方 system prompt 注入逻辑;空串表示当前没有 skill
	summary string, // 会话压缩摘要,垫在 system prompt 末尾;空串表示尚未压缩
	forceRole string, // 用户锁定的模型角色("flash"/"pro");空串或 "auto" 表示走关键词路由
	workingMode WorkingMode, // 工作模式:每轮把对应 skill 引导追加到最后一条 user 消息(renderWorkingMode)
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
				sysBase := BuildSystemPrompt(workspace, skillCatalog, summary)
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

		// 起手模型选择:
		//   - forceRole=flash/pro:用户用 /model 锁定,直接定死,绕过关键词路由;
		//   - 否则(""/auto):入口关键词路由(纯本地、零延迟、无 LLM)——命中复杂关键词 /
		//     消息 > 500 字 → pro,否则 flash。
		// 无论哪种,本轮锁定该模型,主循环不再自动切换。
		switch forceRole {
		case tools.RoleFlash:
			if models.Flash.Model != "" {
				role, currentEntry = tools.RoleFlash, models.Flash
			}
		case tools.RolePro:
			if models.Pro.Model != "" {
				role, currentEntry = tools.RolePro, models.Pro
			}
		default:
			if latestUserTask != "" && models.Pro.Model != "" {
				if RouteByKeyword(latestUserTask) == "pro" {
					role, currentEntry = tools.RolePro, models.Pro
				}
			}
		}
		ch <- ModelSwitchMsg{Role: role, ModelID: currentEntry.Model}

		toolSpecs := buildToolSpecs(mode)

		// 发出本轮"实际发送"的前缀快照(system 文本 + tool specs JSON),供 TUI 持久化:
		// 重启变化检测 + 缓存友好压缩复刻旧前缀。tool specs 随 mode/role 变,故必须存实际值。
		{
			sysContent := ""
			if len(convo) > 0 && convo[0].Role == "system" {
				sysContent = convo[0].Content
			}
			ch <- PrefixSnapshotMsg{Model: currentEntry.Model, SystemPrompt: sysContent, ToolSpecsJSON: MarshalToolSpecs(toolSpecs)}
		}

		// 完成度门禁状态:lastTodo = 最近一次 Todo 快照(判断是否还有未完成项);
		// gateNudges = 连续被门禁挡回的次数(死循环保护,见 completionGate)。
		var lastTodo []PlanItem
		gateNudges := 0

		// lastFile = 本轮最近操作的文件路径,给 Update 漏 path 时兜底回填(issue #81)。
		var lastFile string

		// noProgressRounds = 连续「全部工具调用失败」的轮数;到 maxNoProgressRounds 判定卡死中止。
		// 任一轮有工具成功就归零。无绝对轮数上限(见 maxNoProgressRounds 注释),跑到模型自己停为止。
		noProgressRounds := 0

		// 循环内 auto-compact 状态:lastPromptTokens = 上一轮真实 prompt tokens(判是否该压缩);
		// inLoopCompactOff = 本轮压缩失败后置位,退回"撞窗口优雅报错",避免反复压缩刷请求。
		lastPromptTokens := 0
		inLoopCompactOff := false

		for {
			// 检查 context 是否取消(ESC/退出),提前退出不卡后台
			if ctx.Err() != nil {
				return
			}

			// 循环内 auto-compact:上一轮 prompt 接近上下文窗口就先压缩历史腾空间再继续(不熔断停)。
			// 对标 Claude Code:压缩 convo[1:] 成摘要、重建 [system(新摘要)]+尾部,新摘要经 CompactedMsg
			// 回传 TUI 存 session(否则被剥的 system 摘要会丢失),history 截断经 HistoryUpdateMsg 同步。
			if ctxWin := currentEntry.ContextWindow; !inLoopCompactOff && ctxWin > 0 &&
				lastPromptTokens >= ctxWin*compactTriggerPct/100 &&
				len(convo) > 0 && convo[0].Role == "system" {
				hist := convo[1:]
				sum, cutIdx, turns, cerr := RunCompression(convo[0].Content, MarshalToolSpecs(toolSpecs), hist, currentEntry, ctxWin)
				if cerr != nil {
					inLoopCompactOff = true // 压不动(历史太短/摘要失败)→ 本轮不再尝试,退回撞窗口报错
				} else {
					summary = sum
					kept := append([]ChatMessage(nil), hist[cutIdx:]...)
					convo = append([]ChatMessage{{Role: "system", Content: BuildSystemPrompt(workspace, skillCatalog, summary)}}, kept...)
					lastPromptTokens = 0 // 压完归零,等下一轮真实 usage 再判
					// 防死循环:若压缩后历史仍超阈值(最近 5 轮本身就超窗口,RunCompression 切点缩不动),
					// 再压也无效 → 本轮关掉循环内压缩,退回撞窗口优雅报错,避免反复压缩刷请求。
					if EstimatePromptTokens(workspace, skillCatalog, summary, kept) >= ctxWin*compactTriggerPct/100 {
						inLoopCompactOff = true
					}
					ch <- CompactedMsg{Summary: summary, Turns: turns}
					ch <- HistoryUpdateMsg{History: convo}
				}
			}
			// 按本轮模型支不支持视觉,即时把带图消息渲染成 base64 或 路径+OCR(见 renderConvoImages)。
			// 只渲染发出去的副本,convo 规范形态(只存路径)不变。
			assistantContent, reasoning, toolCalls, finishReason, usage, err := streamOnce(
				ctx,
				currentEntry.APIKey, currentEntry.BaseURL, currentEntry.Model,
				renderConvoImages(renderWorkingMode(convo, workingMode), currentEntry.Vision), currentEntry.MaxTokens, toolSpecs,
				currentEntry.ReasoningEffort, currentEntry.Thinking,
				ch,
			)
			// 自愈兜底:被端点以"不支持图片输入"拒掉(无论 base64 是探测误判发的、还是历史里混进来的)→
			// 把该模型降级为无视觉(本轮后续也生效),用"剥图"渲染重发一次,并通知 TUI 纠正缓存。
			// 不限定 currentEntry.Vision —— base64 可能从别处混入,撞到就无条件回退。用户看不到这个 404。
			if err != nil && isImageInputUnsupported(err) {
				currentEntry.Vision = false
				ch <- VisionUnsupportedMsg{Model: currentEntry.Model, BaseURL: currentEntry.BaseURL}
				assistantContent, reasoning, toolCalls, finishReason, usage, err = streamOnce(
					ctx,
					currentEntry.APIKey, currentEntry.BaseURL, currentEntry.Model,
					renderConvoImages(renderWorkingMode(convo, workingMode), false), currentEntry.MaxTokens, toolSpecs,
					currentEntry.ReasoningEffort, currentEntry.Thinking,
					ch,
				)
			}
			if err != nil {
				// context 取消是主动中断,不报 Error 给 UI。
				if errors.Is(err, context.Canceled) {
					return
				}
				ch <- StreamErrMsg{err}
				return
			}
			// 主 agent 的 token 用量发给 TUI 显示。
			if usage != nil {
				ch <- UsageMsg{Usage: *usage}
				lastPromptTokens = usage.PromptTokens // 供下一轮判断是否触发循环内 auto-compact
			}

			// 把本轮 assistant 回复写入历史(含 reasoning_content,thinking 模型下轮需要)
			convo = append(convo, ChatMessage{
				Role:             "assistant",
				Content:          assistantContent,
				ReasoningContent: reasoning,
				ToolCalls:        toolCalls,
			})

			if len(toolCalls) == 0 {
				// 完成度门禁:别把"这轮没工具调用"直接当成"任务完成"。
				// 截断判定双信号(后端可能不给 finish_reason,尤其代理/自建池子):
				// finish_reason==length 或 生成 token 撞上 max_tokens 上限。
				truncated := finishReason == "length" ||
					(usage != nil && currentEntry.MaxTokens > 0 && usage.CompletionTokens >= currentEntry.MaxTokens)
				// 被截断、或还有未完成 todo 时,注入一条提示再跑一轮,催它继续。
				if nudge := completionGate(truncated, lastTodo, &gateNudges); nudge != "" {
					convo = append(convo, ChatMessage{Role: "user", Content: nudge})
					continue
				}
				ch <- HistoryUpdateMsg{History: convo}
				ch <- StreamDoneMsg{}
				return
			}
			gateNudges = 0 // 有工具调用 = 有进展,重置门禁计数

			// roundProgress = 本轮是否有任一工具调用成功;决定 noProgressRounds 是归零还是累加。
			roundProgress := false

			// 执行每个工具调用,把结果加进 convo。
			// 这些工具被 deepx 拦截 (不走 Executor):
			//   - CreatePlan         → 解析后产 PlanCreatedMsg,触发 DAG 调度(派并发子 agent)
			//   - Todo               → 解析后产 PlanCreatedMsg 刷新可见清单,主 agent 自己执行,不派子 agent
			//   - UpdatePlanStatus   → 解析后产 TaskStatusMsg,UI 更新单项状态
			//   - SwitchModel        → 改本轮 currentEntry / role,通过 ModelSwitchMsg 通知 UI
			// 拦截后仍要给 LLM 一个 fake tool result,让 OpenAI 工具循环能正常推进。
			for _, tc := range toolCalls {
				// review 模式:对 Write/Update/Bash 发起审核
				var reviewCh chan bool
				if mode == AgentMode_Review && isReviewable(tc.Function.Name) {
					reviewCh = make(chan bool, 1)
				}
				ch <- ToolCallStartMsg{Name: tc.Function.Name, Args: tc.Function.Arguments, ReviewCh: reviewCh}
				if reviewCh != nil {
					select {
					case approved := <-reviewCh:
						if !approved {
							ch <- ToolCallResultMsg{Name: tc.Function.Name, Output: "操作已被用户拒绝 (review 模式)", Success: false}
							convo = append(convo, ChatMessage{
								Role:       "tool",
								ToolCallID: tc.ID,
								Name:       tc.Function.Name,
								Content:    "操作已被用户拒绝 (review 模式)",
							})
							continue
						}
					case <-ctx.Done():
						return
					}
				}

				var result tools.ToolResult
				switch tc.Function.Name {
				case "AskUser":
					// 弹 TUI 选择框,阻塞等用户选完(同 review 的 channel 骨架)。
					questions, perr := parseAskUserArgs(tc.Function.Arguments)
					if perr != nil {
						result = tools.ToolResult{Output: perr.Error(), Success: false}
					} else {
						respCh := make(chan string, 1)
						ch <- AskUserMsg{Questions: questions, ResponseCh: respCh}
						select {
						case answer := <-respCh:
							if answer == "" {
								result = tools.ToolResult{Output: "用户取消了选择(或希望改用文字回答)。请改用普通对话继续询问,不要重复弹窗。", Success: false}
							} else {
								result = tools.ToolResult{Output: answer, Success: true}
							}
						case <-ctx.Done():
							return
						}
					}
				case "CreatePlan":
					plans, perr := parseCreatePlanArgs(tc.Function.Arguments)
					if perr != nil {
						result = tools.ToolResult{Output: perr.Error(), Success: false}
					} else {
						// 1. 通知 UI 渲染 plan 树
						ch <- PlanCreatedMsg{Plans: plans, Kind: "createplan"}
						// 2. 拍平成 DAG 节点并同步执行
						nodes := flattenPlans(plans)
						exec := func(n *schedulerNode, preds map[string]string) (string, error) {
							res := runSubAgent(ctx, subAgentInput{
								Models:       models,
								Entry:        resolveModelEntry(n.Model, models),
								NodeID:       n.ID,
								NodeTitle:    n.Title,
								UserTask:     latestUserTask,
								Predecessors: preds,
								Workspace:    workspace,
								SkillCatalog: skillCatalog,
								Mode:         mode,
							})
							if res.Err != nil {
								return "", res.Err
							}
							return res.Summary, nil
						}
						final := runDAG(ctx, nodes, exec, ch)
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
				case "Todo":
					// 主 agent 自驱动的可见待办清单:全量快照覆盖当前 planState,不派子 agent。
					// 复用 PlanCreatedMsg 让 UI 直接按各项 status 渲染 checkbox。
					items, perr := parseTodoArgs(tc.Function.Arguments)
					if perr != nil {
						result = tools.ToolResult{Output: perr.Error(), Success: false}
					} else {
						lastTodo = items // 记录最新快照,供完成度门禁判断是否还有未完成项
						ch <- PlanCreatedMsg{Plans: items, Kind: "todo"}
						done := 0
						for _, it := range items {
							if it.Status == PlanStatusDone {
								done++
							}
						}
						result = tools.ToolResult{
							Output:  fmt.Sprintf("待办已更新:%d/%d 完成。继续按清单执行,每开始/完成一步就重发整张 todos 更新状态。", done, len(items)),
							Success: true,
						}
					}
				case "UpdatePlanStatus":
					id, st, summary, perr := parseUpdatePlanStatusArgs(tc.Function.Arguments)
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
					if forceRole == tools.RoleFlash {
						// 用户用 /model flash 锁定,模型无权越权升级。
						result = tools.ToolResult{
							Output:  "用户已锁定 flash 模型(/model flash),忽略本次升级,继续用 flash 完成任务。",
							Success: true,
						}
					} else if role == tools.RolePro {
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
						// 工具表不随角色变(各角色一致),无需重算 toolSpecs。
						ch <- ModelSwitchMsg{Role: role, ModelID: currentEntry.Model, Reason: reason}
						result = tools.ToolResult{
							Output:  fmt.Sprintf("已切到 pro 模型 (%s)。本轮剩余请求 + reasoning 用 pro 处理。", currentEntry.Model),
							Success: true,
						}
					}
				case "OCR":
					// 视觉模型本就能看图。它对"已经内联给它的那张图"还调 OCR(mimo 甚至会先 ls 缓存目录
					// 再 OCR),纯属冗余绕路 —— base64 都喂到嘴边了还去翻文件。软提醒(消息备注/工具描述)
					// 压不住这个模型,这里在执行层硬拦:不真跑 OCR,把它怼回去直接看图。不改工具表,缓存安全。
					// 只拦"对已内联图的 OCR";OCR 一个没内联的文件路径(视觉模型确实看不到的)照常放行。
					if currentEntry.Vision && ocrTargetsInlinedImage(tc.Function.Arguments, convo) {
						result = tools.ToolResult{
							Output:  "你是视觉模型,这张图已经以图片形式内联在当前对话里了,请直接查看图片作答 —— 不要调用 OCR,也不要用 ls/find 去文件系统查找图片文件。",
							Success: false,
						}
					} else {
						result = executeTool(tc, mode, &lastFile)
					}
				default:
					result = executeTool(tc, mode, &lastFile)
				}

				if result.Success {
					roundProgress = true
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

			// 无进展断路器:本轮工具全失败则累加,任一成功则归零;连续卡死到上限就暂停。
			if roundProgress {
				noProgressRounds = 0
			} else {
				noProgressRounds++
				if noProgressRounds >= maxNoProgressRounds {
					ch <- StreamErrMsg{fmt.Errorf("连续 %d 轮工具调用均未成功,疑似卡死或反复失败,已暂停。可输入「继续」让它接着尝试,或换个说法。", maxNoProgressRounds)}
					return
				}
			}
		}
	}()

	return ListenToStream(ch), ch
}

// streamOnce 发起一次 chat/completions 请求,返回 (content, reasoning_content, tool_calls, usage, error)。
//
// reasoningEffort / thinking 是跨供应商通用的推理参数,**空字符串严格不发送**(走各家 API 默认),
// 这是兼容 MiMo 等不支持这俩字段的模型的关键 —— 任何不主动启用的模型都不会被多余字段炸 400。
// maxGateNudges 是完成度门禁连续催继续的上限:催够这么多次模型仍不动工具,就放行结束,防死循环/空转。
const maxGateNudges = 3

// completionGate 在"这轮没有工具调用"时决定是否还要继续:
//   - 返回非空 = 应继续,内容是注入给模型的提示(催它接着干);
//   - 返回 "" = 真的结束。
//
// 触发继续:① 上轮被截断(truncated,话没说完);② 还有未完成的 todo。
// 死循环保护:连续催 maxGateNudges 次仍无进展就放行。纯对话/单步任务(没建 todo、未截断)照常一轮结束。
func completionGate(truncated bool, todo []PlanItem, nudges *int) string {
	if *nudges >= maxGateNudges {
		return ""
	}
	if truncated {
		*nudges++
		return "(你上一条回复似乎被长度上限截断,没有输出完。请接着把没做完的部分继续做完——该调用工具就调用,不要停在这里总结。)"
	}
	if pending := countPendingTodos(todo); pending > 0 {
		*nudges++
		return fmt.Sprintf("(待办还有 %d 项未完成,任务尚未结束。请继续执行下一步并调用相应工具,不要提前收尾;若确实卡住无法继续,再说明原因。)", pending)
	}
	return ""
}

// countPendingTodos 统计 todo 里仍待办的项(pending/running);done/failed/blocked 不计入。
func countPendingTodos(todo []PlanItem) int {
	n := 0
	for _, it := range todo {
		if it.Status == PlanStatusPending || it.Status == PlanStatusRunning {
			n++
		}
	}
	return n
}

func streamOnce(
	ctx context.Context,
	apiKey, baseURL, modelID string,
	convo []ChatMessage,
	maxTokens int,
	toolSpecs []tools.OpenAIToolSpec,
	reasoningEffort string,
	thinking string,
	ch chan<- tea.Msg,
) (string, string, []ToolCall, string, *UsageInfo, error) {

	body, err := json.Marshal(chatRequest{
		Model:     modelID,
		MaxTokens: maxTokens,
		Stream:    true,
		StreamOptions: &streamOptions{
			IncludeUsage: true,
		},
		Messages: convo,
		Tools:    toolSpecs,
		// thinking 和 reasoning_effort 是两个独立顶层字段。各自 omitempty,
		// 用户设了就发、没设就不发,白名单内的值才透传(防 yaml 笔误)。
		Thinking:        buildThinkingOption(thinking),
		ReasoningEffort: validateReasoningEffort(reasoningEffort),
	})
	if err != nil {
		return "", "", nil, "", nil, err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", "", nil, "", nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", nil, "", nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return "", "", nil, "", nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(b))
	}

	var (
		contentBuilder   strings.Builder
		reasoningBuilder strings.Builder
		inReasoning      bool
		toolBuf          = map[int]*ToolCall{}
		lastUsage        *UsageInfo // stream_options.include_usage 会在最后 chunk 返回 usage
		finishReason     string     // 最后一个非空 finish_reason("stop"/"length"/"tool_calls"…),供主循环判断截断
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
		// stream_options.include_usage: 最后 chunk 有 usage、choices 为空
		if chunk.Usage != nil {
			chunk.Usage.normalize() // 统一各供应商的缓存命中口径(mimo 等走 prompt_tokens_details)
			lastUsage = chunk.Usage
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		if fr := chunk.Choices[0].FinishReason; fr != nil && *fr != "" {
			finishReason = *fr
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
		return contentBuilder.String(), reasoningBuilder.String(), nil, finishReason, lastUsage, err
	}

	// 按 index 升序拼装最终 tool_calls。
	// 注意:toolBuf 的 key 不保证从 0 开始、也不保证连续——DeepSeek 官方 index 从 0 起,
	// 但部分第三方/自建 base_url 池子从 1 起(见 issue #59)。若按 0..len-1 遍历会漏掉
	// 非零起始或跳号的 key,导致工具调用被整个丢弃、会话被误判为结束而提前中断。
	idxs := make([]int, 0, len(toolBuf))
	for idx := range toolBuf {
		idxs = append(idxs, idx)
	}
	sort.Ints(idxs)
	toolCalls := make([]ToolCall, 0, len(idxs))
	for _, idx := range idxs {
		toolCalls = append(toolCalls, *toolBuf[idx])
	}
	return contentBuilder.String(), reasoningBuilder.String(), toolCalls, finishReason, lastUsage, nil
}

// ListenToStream 把单条事件转给 bubbletea。
func ListenToStream(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// === 工具白名单 / 执行 ===

// buildToolSpecs 组装本轮工具列表。当前所有模式 / 角色拿到的工具表一致(模式与角色限制都靠
// system prompt + executeTool 兜底,不在这里裁剪),这样前缀缓存稳定。
func buildToolSpecs(mode AgentMode) []tools.OpenAIToolSpec {
	var out []tools.OpenAIToolSpec
	for _, t := range tools.Tools {
		if !allowedInMode(t, mode) {
			continue
		}
		out = append(out, t.ToOpenAISpec())
	}
	// 动态注入的 MCP 工具:对所有角色可见(子 agent 也能用)。放在内置工具之后,
	// 保持内置工具的前缀稳定(MCP 工具变动不影响内置部分的 KV cache)。
	for _, t := range tools.MCPTools() {
		out = append(out, t.ToOpenAISpec())
	}
	return out
}

func allowedInMode(_ tools.Tool, _ AgentMode) bool {
	// tools 数组不再按模式裁剪:所有模式下暴露全部工具,保持 prefix cache 稳定。
	// 模式限制通过 system prompt + 切换时注入的模式通知消息传达,LLM 自行遵守。
	// executeTool 里仍保留硬拦截作为兜底。
	return true
}

// isReviewable 判断工具在 review 模式下是否需要人工审核。
func isReviewable(name string) bool {
	return name == "Write" || name == "Update" || name == "Bash"
}

// fileToolNames 是用于维护 lastFile(模型当前正在编辑的文件)的工具,给 Update 漏 path 时兜底。
// 只取真正"在读写编辑目标文件"的:Read（打开待编辑文件)/ Write / Update。刻意排除:
//   - Grep:path 常是目录或"查别的文件",会把 lastFile 污染成非编辑目标
//   - OCR:path 是图片,而图片永远不会是 Update 的目标
//   - List/Tree:path 是目录
var fileToolNames = map[string]bool{"Read": true, "Write": true, "Update": true}

// executeTool 执行单个工具调用。lastFile(可空)跟踪"最近操作的文件路径":
// 部分模型把 Update 的 path 排在参数最后、偶尔整段漏掉(issue #81),导致第一次 Update 因
// "path 参数为空"失败、再重试才成。此时用 lastFile 兜底回填 path —— 只对 Update 生效
// (它带 old_string,补错文件会因匹配不到而安全失败;Write 是创建/覆盖,绝不猜路径)。
func executeTool(tc ToolCall, mode AgentMode, lastFile *string) tools.ToolResult {
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
	args, err := tools.ParseArgs(tc.Function.Arguments)
	if err != nil {
		return tools.ToolResult{
			Output:  fmt.Sprintf("参数解析失败: %v / raw=%s", err, tc.Function.Arguments),
			Success: false,
		}
	}
	// 最近文件兜底(issue #81):Update 的 path 为空时,用 lastFile 回填;否则记录本次的 path。
	if lastFile != nil {
		p, _ := args["path"].(string)
		switch {
		case strings.TrimSpace(p) == "" && t.Name == "Update" && *lastFile != "":
			args["path"] = *lastFile
		case strings.TrimSpace(p) != "" && fileToolNames[t.Name]:
			*lastFile = strings.TrimSpace(p)
		}
	}
	// 纵深防御:Executor 为 nil 的工具(SwitchModel / CreatePlan 等)预期在主/子 agent
	// 工具循环里被拦截,不应该走到这里。一旦走到,直接调 nil 会段错误整个进程崩。
	// 退而返回失败给 LLM,让它自纠或交给上层重试,而不是 panic。
	if t.Executor == nil {
		return tools.ToolResult{
			Output:  fmt.Sprintf("工具 %s 不能直接执行(应在 agent 循环内被拦截);请用别的工具完成此步骤", t.Name),
			Success: false,
		}
	}
	return t.Executor(args)
}
