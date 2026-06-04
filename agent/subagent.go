package agent

import (
	"context"
	"deepx/tools"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// buildSubAgentToolSpecs 子 agent 工具集。已不再按角色过滤工具,所以跟主 agent 的工具表
// 逐字节一致 —— 刻意如此,保前缀缓存(别为了"藏工具"去按角色裁,裁了工具表分叉就 cache miss)。
//
// CreatePlan / Todo / SwitchModel 子 agent 不该用,靠两层兜底(没有 subAgentToolDenylist 那种硬过滤,别去找):
//  1. runSubAgent 尾部系统提示词明确禁止 CreatePlan / Todo / SwitchModel;
//  2. 它们 Executor 为 nil,子 agent 走 executeTool 时纵深防护返回失败(不 panic、不生效)。
func buildSubAgentToolSpecs(mode AgentMode) []tools.OpenAIToolSpec {
	return buildToolSpecs(mode)
}

// subAgentInput 是一次子 agent 调用的全部依赖。
// 由 runDAG 的 exec 回调按节点上下文构造,主 agent 不直接调用。
type subAgentInput struct {
	Models       ModelConfig // 整套配置,留作扩展用(目前不直接消费)
	Entry        ModelEntry  // 本节点选定的连接参数 (BaseURL/Model/APIKey)
	NodeID       string
	NodeTitle    string
	UserTask     string            // 用户原始消息,作为背景给子 agent
	Predecessors map[string]string // 已完成上游节点的 summary
	Workspace    string
	SkillCatalog string // 与主 agent 同一份 skill 目录,使子 agent 也能用 LoadSkill
	Mode         AgentMode
}

// subAgentResult 子 agent 完成后的产物。
type subAgentResult struct {
	Summary string
	Err     error
}

// 子 agent 的轮数上限。比主 agent 紧一点(主 100, 子 50),
// 因为子 agent 任务粒度更小;做不完直接 fail,scheduler 会把该节点标 failed 而不影响其他节点。
const subAgentMaxRounds = 50

// subAgentCtxBudgetPct 是子 agent convo 占模型上下文窗口的上限百分比;超过即中止本节点。
// 子 agent 不压缩,留 20% 余量给本轮输入+输出,避免撑爆窗口导致 API 脏失败。
const subAgentCtxBudgetPct = 80

// estimateConvoTokens 粗估一段 convo 的 token 数(沿用项目 ~3 字符/token 的口径)。
// 只算文本主体(Content + ReasoningContent + 工具调用参数),够做预算判断。
func estimateConvoTokens(convo []ChatMessage) int {
	chars := 0
	for _, m := range convo {
		chars += len([]rune(m.Content)) + len([]rune(m.ReasoningContent))
		for _, tc := range m.ToolCalls {
			chars += len([]rune(tc.Function.Arguments))
		}
	}
	return chars / 3
}

// runSubAgent 执行单个 plan/task 节点。
//
// 行为:
//   - 独立 history,只含 system prompt + 用户原始任务 + 节点 title
//   - 工具表与主 agent 一致;不该用的 CreatePlan/Todo/SwitchModel 靠系统提示词禁止 + nil-Executor 兜底(见 buildSubAgentToolSpecs)
//   - UpdatePlanStatus 调用被吞掉,scheduler 才是状态真实来源
//   - 不向 TUI 发 TokenMsg / ToolCallStartMsg 等可见事件,子 agent 中间过程完全隐藏
//   - 最终 assistant content 作为 Summary 返回;失败 → Err
func runSubAgent(ctx context.Context, in subAgentInput) subAgentResult {
	// 系统提示 = 与主 agent 共用的核心(身份+规则+workspace+skill)+ 子 agent 专属尾部。
	// 共用核心逐字节一致 → 与主 agent / 同模型兄弟节点共享缓存前缀;同时子 agent 也拿到了
	// 安全/模式规则和 skill 目录(LoadSkill 因此可用)。专属部分放尾部,只有它是 miss。
	var sb strings.Builder
	sb.WriteString(coreSystemPrompt(in.Workspace, in.SkillCatalog))
	sb.WriteString("\n\n# 子 agent 任务\n你是 deepx 的子 agent,只负责完成下面这一项,禁止 CreatePlan / Todo / SwitchModel(只做被分派的事,不要再拆分、维护待办或换模型)。")
	sb.WriteString("\n- 用户的原始任务背景: ")
	sb.WriteString(in.UserTask)
	sb.WriteString("\n- 你这一项的具体目标: ")
	sb.WriteString(in.NodeTitle)
	if len(in.Predecessors) > 0 {
		sb.WriteString("\n\n上游已完成节点的产出 (作为上下文使用):")
		for id, sum := range in.Predecessors {
			sb.WriteString("\n- [")
			sb.WriteString(id)
			sb.WriteString("] ")
			sb.WriteString(sum)
		}
	}
	sb.WriteString("\n\n完成后只输出一段简短(<200 字)的结果总结。不要写多余的客套。")

	convo := []ChatMessage{
		{Role: "system", Content: sb.String()},
		{Role: "user", Content: in.NodeTitle},
	}

	toolSpecs := buildSubAgentToolSpecs(in.Mode)

	// 静默 channel:streamOnce 的 TokenMsg 不进 UI,内部 drain 掉
	silent := make(chan tea.Msg, 64)
	drained := make(chan struct{})
	go func() {
		for range silent {
		}
		close(drained)
	}()

	// 上下文预算熔断:子 agent 不做压缩,convo 只增不减,读几个大文件就可能撑爆窗口。
	// 每轮前估算,超过窗口的 ctxBudgetPct 就主动中止(干净失败),而不是等 API 报错或耗满轮数。
	ctxWin := in.Entry.ContextWindow
	if ctxWin <= 0 {
		ctxWin = 65536
	}
	ctxBudget := ctxWin * subAgentCtxBudgetPct / 100

	for round := 0; round < subAgentMaxRounds; round++ {
		// 检查 context 是否取消(ESC/退出)
		if ctx.Err() != nil {
			close(silent)
			<-drained
			return subAgentResult{Err: ctx.Err()}
		}
		// 上下文预算熔断:超预算就停,避免脏失败(API 超长报错)和卡死时的 token 浪费。
		if est := estimateConvoTokens(convo); est >= ctxBudget {
			close(silent)
			<-drained
			return subAgentResult{Err: fmt.Errorf("子 agent [%s] 上下文超预算(~%d/%d tokens),中止", in.NodeID, est, ctxWin)}
		}
		// 不主动 strip reasoning:本轮锁定模型,thinking 模型仍正常回传,
		// 非 thinking 模型忽略 history 里的 reasoning_content 字段(omitempty 已处理空值)。
		content, reasoning, toolCalls, _, _, err := streamOnce(
			ctx,
			in.Entry.APIKey, in.Entry.BaseURL, in.Entry.Model,
			convo, in.Entry.MaxTokens, toolSpecs,
			in.Entry.ReasoningEffort, in.Entry.Thinking,
			silent,
		)
		if err != nil {
			close(silent)
			<-drained
			return subAgentResult{Err: err}
		}

		// 必须把 reasoning_content 存进 history,thinking 模型下一轮要求原样回传。
		// 之前丢这个字段是 sub-agent 400 "reasoning_content must be passed back" 的根因。
		convo = append(convo, ChatMessage{
			Role:             "assistant",
			Content:          content,
			ReasoningContent: reasoning,
			ToolCalls:        toolCalls,
		})

		if len(toolCalls) == 0 {
			// 子 agent 完成,返回最后一段 assistant 文本作为 summary
			close(silent)
			<-drained
			summary := strings.TrimSpace(content)
			if summary == "" {
				summary = "(子 agent 未给出明确结论)"
			}
			return subAgentResult{Summary: summary}
		}

		for _, tc := range toolCalls {
			var result tools.ToolResult
			switch tc.Function.Name {
			case "UpdatePlanStatus":
				// 子 agent 想自报状态,吞掉给 OK。scheduler 才是状态来源。
				// (只用 Output 拼 tool 消息,Success 不读,故不设)
				result = tools.ToolResult{Output: "已记录"}
			default:
				result = executeTool(tc, in.Mode)
			}
			convo = append(convo, ChatMessage{
				Role:       "tool",
				ToolCallID: tc.ID,
				Name:       tc.Function.Name,
				Content:    result.Output,
			})
		}
	}

	close(silent)
	<-drained
	return subAgentResult{Err: fmt.Errorf("子 agent [%s] 超过 %d 轮工具调用上限", in.NodeID, subAgentMaxRounds)}
}

// resolveModelEntry 把 plan/task 里 "flash" / "pro" 字符串映射到 ModelConfig 里的完整 entry。
// roleHint 解析:
//   - "pro" / "Pro" → 返回 cfg.Pro(若有 model id)
//   - "flash" / "" / 其他 → 返回 cfg.Flash(若有 model id),否则退到 cfg.Pro
//
// 兜底逻辑保证不会返回空 entry,即使节点的 model 字段误填也能跑。
func resolveModelEntry(roleHint string, cfg ModelConfig) ModelEntry {
	switch strings.ToLower(strings.TrimSpace(roleHint)) {
	case "pro":
		if cfg.Pro.Model != "" {
			return cfg.Pro
		}
	case "flash", "":
		// 走默认
	}
	if cfg.Flash.Model != "" {
		return cfg.Flash
	}
	return cfg.Pro
}
