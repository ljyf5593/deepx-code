package tui

import (
	"deepx/agent"
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// planState 持有当前活跃的规划及其状态。
// 由 agent.PlanCreatedMsg 初始化,agent.TaskStatusMsg 增量更新。
// nil 表示当前无规划。
type planState struct {
	items []agent.PlanItem
}

// apply 处理 TaskStatusMsg,把 plan 状态写到对应项。
// 找不到 id 时静默忽略(LLM 偶尔会把 id 拼错)。
func (p *planState) apply(msg agent.TaskStatusMsg) {
	if p == nil {
		return
	}
	for i := range p.items {
		if p.items[i].ID == msg.ID {
			p.items[i].Status = msg.Status
			if msg.Summary != "" {
				p.items[i].Summary = msg.Summary
			}
			return
		}
	}
}

// planStatusBox plan 状态用复选框风格渲染,固定 3 ANSI cell 宽。
//   - pending: [ ] 待执行
//   - running: [⏵] 跑中(着色)
//   - done:    [✓] 完成(绿色)
//   - failed:  [✗] 失败(红色)
//   - blocked: [⏸] 跳过(暗色)
func planStatusBox(s agent.PlanStatus) string {
	switch s {
	case agent.PlanStatusRunning:
		return lipgloss.NewStyle().Foreground(highlightColor).Render("[⏵]")
	case agent.PlanStatusDone:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("[✓]")
	case agent.PlanStatusFailed:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("[✗]")
	case agent.PlanStatusBlocked:
		return lipgloss.NewStyle().Foreground(dimColor).Render("[⏸]")
	case agent.PlanStatusPending:
		return lipgloss.NewStyle().Foreground(dimColor).Render("[ ]")
	}
	return "[ ]"
}

// renderPlanSummary 右栏极简摘要:只显示完成进度 "X/Y done"。
func renderPlanSummary(p *planState, _ int) []string {
	if p == nil || len(p.items) == 0 {
		return nil
	}
	total, done := 0, 0
	for _, pl := range p.items {
		total++
		if pl.Status == agent.PlanStatusDone {
			done++
		}
	}
	return []string{fmt.Sprintf("%d/%d done", done, total)}
}

// planModelTag 渲染一个 plan 节点的 model 标签,显示在 title 后。
//   - "pro"   → 高亮色,提醒用户这一步用了贵模型
//   - "flash" → 暗色,弱化展示,信息完整但不抢眼
//   - 空 / 其他 → 不渲染(老数据 / 模型瞎填,降级到无 tag)
func planModelTag(model string) string {
	switch strings.ToLower(strings.TrimSpace(model)) {
	case "pro":
		return lipgloss.NewStyle().Foreground(accentColor).Render("[pro]")
	case "flash":
		return lipgloss.NewStyle().Foreground(dimColor).Render("[flash]")
	}
	return ""
}

// renderPlanForChat 把 plan 列表渲染成 chat 区使用的字符串(多行)。
// 每次都用当前 planState 的实际状态(checkbox 反映 done / running / pending),
// refreshViewport 每次 tick / token / TaskStatusMsg 都重新渲染一遍,实现 live overlay。
// 流结束时再固化一次到 chatContent,这样滚回历史也能看到最终结果。
func renderPlanForChat(p *planState) string {
	if p == nil || len(p.items) == 0 {
		return ""
	}
	var sb strings.Builder
	dim := lipgloss.NewStyle().Foreground(dimColor).Render

	sb.WriteString(lipgloss.NewStyle().Bold(true).Foreground(accentColor).Render("📋 Plan"))
	sb.WriteString("\n")
	for _, pl := range p.items {
		sb.WriteString("  ")
		sb.WriteString(planStatusBox(pl.Status))
		sb.WriteString(" ")
		sb.WriteString(pl.Title)
		if tag := planModelTag(pl.Model); tag != "" {
			sb.WriteString(" ")
			sb.WriteString(tag)
		}
		if len(pl.DependsOn) > 0 && pl.Status == agent.PlanStatusPending {
			sb.WriteString(dim("  (deps: " + strings.Join(pl.DependsOn, ",") + ")"))
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// truncate 用 … 截断超长字符串。按 rune 计宽 (中文每字 ~2 cell 暂不精确折算,差几格能接受)。
func truncate(s string, max int) string {
	if max <= 1 {
		return "…"
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}
