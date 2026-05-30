package tui

import (
	"deepx/agent"
	"deepx/config"
	"strings"

	"charm.land/lipgloss/v2"
)

// === /reasoning 居中弹窗 ===
//
// 目标:让用户一处把 flash 和 pro 两个 role 的推理参数(thinking、effort)都设好。
// 跨供应商兼容:m.models 里的字段为空字符串 → chatRequest 不发送对应 JSON 字段(omitempty),
// 任何不支持的模型(MiMo / 未来 OpenAI-兼容新模型)都不会被多余字段炸 400。
//
// 4 行(rows),每行一组单选:
//
//	row 0: flash · thinking  → [enabled, disabled, default(="")]
//	row 1: flash · effort    → [low, medium, high, max, default(="")]
//	row 2: pro · thinking    → [enabled, disabled, default(="")]
//	row 3: pro · effort      → [low, medium, high, max, default(="")]
//
// effort 取值兼顾两个生态:
//   - DeepSeek canonical 是 high / max;low / medium 是兼容别名(服务端自动映射到 high)
//   - OpenAI o1/o3 标准是 low / medium / high
//
// 把 low/medium/high/max 全列上,DeepSeek 用户和 OpenAI-兼容用户都能选到合适档,
// 拼到对方接口也都是"合法或自动映射"的取值。MiMo 文档没列 effort —— 留 default 最稳。
//
// 键盘:↑/↓ / j/k 切行,←/→ / h/l 在当前行内切值并**立刻应用 + 落盘**,
// Enter / Esc 关闭(没有 cancel 概念 —— 改了的都已经入盘了)。

var (
	reasoningThinkingOpts = []string{"enabled", "disabled", ""}
	reasoningEffortOpts   = []string{"low", "medium", "high", "max", ""}
)

// reasoningRowMeta 一行对应 (role, field, 该行可选值)。
func reasoningRowMeta(row int) (role, field string, opts []string) {
	switch row {
	case 0:
		return "flash", "thinking", reasoningThinkingOpts
	case 1:
		return "flash", "effort", reasoningEffortOpts
	case 2:
		return "pro", "thinking", reasoningThinkingOpts
	case 3:
		return "pro", "effort", reasoningEffortOpts
	}
	return "", "", nil
}

// reasoningCurrentValue 取当前 m.models 里 (role, field) 的实时值。
func reasoningCurrentValue(m model, role, field string) string {
	e := m.models.Flash
	if role == "pro" {
		e = m.models.Pro
	}
	if field == "thinking" {
		return e.Thinking
	}
	return e.ReasoningEffort
}

// applyReasoningCell 把 (role, field, value) 写到 m.models,并落盘到 ~/.deepx/model.yaml。
// value="" 表示"不发该字段"(走 API 默认)。落盘失败时往 chat 写 System 提示但不回滚内存。
func (m *model) applyReasoningCell(role, field, value string) {
	apply := func(e *agent.ModelEntry) {
		switch field {
		case "thinking":
			e.Thinking = value
		case "effort":
			e.ReasoningEffort = value
		}
	}
	if role == "pro" {
		apply(&m.models.Pro)
	} else {
		apply(&m.models.Flash)
	}
	// 先 Load 现有 cfg → 覆盖 Flash/Pro 两个 entry → Save。先 Load 是为了保留 web 配置等其他段。
	loaded, err := config.Load()
	if err != nil {
		m.appendChat("System", "读取配置失败,本次推理参数仅在内存生效:"+err.Error())
		return
	}
	loaded.Flash = config.ModelEntry(m.models.Flash)
	loaded.Pro = config.ModelEntry(m.models.Pro)
	if err := config.Save(loaded); err != nil {
		m.appendChat("System", "保存配置失败:"+err.Error())
	}
}

// reasoningStepRow 在第 row 行的可选值里前进 step 步(±1),环绕,立即应用并落盘。
func (m *model) reasoningStepRow(row, step int) {
	role, field, opts := reasoningRowMeta(row)
	if len(opts) == 0 {
		return
	}
	cur := reasoningCurrentValue(*m, role, field)
	idx := 0
	for i, v := range opts {
		if v == cur {
			idx = i
			break
		}
	}
	idx = ((idx+step)%len(opts) + len(opts)) % len(opts) // 处理负数
	m.applyReasoningCell(role, field, opts[idx])
}

// openReasoningModal 打开 /reasoning 弹窗,光标停在 row 0。
func (m *model) openReasoningModal() {
	m.reasoningModalRow = 0
	m.showReasoningModal = true
}

// reasoningModalBlock 渲染 /reasoning 弹窗内容。
func (m model) reasoningModalBlock() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(highlightColor).Render(T("reasoning.modal.title"))

	dim := lipgloss.NewStyle().Foreground(subtleColor)
	on := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	roleStyle := lipgloss.NewStyle().Foreground(bannerDecoColor).Bold(true)

	var rows []string
	for row := 0; row < 4; row++ {
		role, field, opts := reasoningRowMeta(row)
		cur := reasoningCurrentValue(m, role, field)

		// 行头:`▸ flash · thinking` 选中态,`  flash · thinking` 非选中态
		marker := "  "
		if row == m.reasoningModalRow {
			marker = "▸ "
		}
		header := marker + roleStyle.Render(role) + dim.Render(" · "+field)

		// 选项行:每个选项前面 ● / ○,选中 row 时选中值用绿色加粗,其他值正常,非选中 row 整行 dim
		var optParts []string
		for _, opt := range opts {
			label := opt
			if label == "" {
				label = "default"
			}
			marker := "○"
			if opt == cur {
				marker = "●"
			}
			seg := marker + " " + label
			switch {
			case row == m.reasoningModalRow && opt == cur:
				seg = on.Render(seg)
			case row == m.reasoningModalRow:
				seg = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(seg)
			default:
				seg = dim.Render(seg)
			}
			optParts = append(optParts, seg)
		}
		optLine := "    " + strings.Join(optParts, "   ")

		rows = append(rows, header, optLine, "")
	}

	footer := dim.Render(T("reasoning.modal.footer"))
	parts := []string{title, ""}
	parts = append(parts, rows...)
	parts = append(parts, footer)
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(highlightColor).
		Padding(1, 2).
		Width(56).
		Render(content)
}
