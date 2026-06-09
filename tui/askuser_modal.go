package tui

import (
	"encoding/json"
	"fmt"
	"time"

	"charm.land/lipgloss/v2"
)

// askQuestionAnswered 判断某题是否已勾选(至少一个选项为 true)。
func askQuestionAnswered(sel []bool) bool {
	for _, on := range sel {
		if on {
			return true
		}
	}
	return false
}

// === AskUser 选择题弹窗 ===
//
// LLM 调用 AskUser 工具时,agent 循环发来 AskUserMsg 并阻塞等待;TUI 弹此框让用户勾选,
// 提交后把结果 JSON 写回 channel(buildAskAnswer),agent 拿到后作为工具结果回传给模型。
// 键盘:↑↓ 移光标,空格 勾选(单选互斥/多选可叠加),←→ 切题,Enter 下一题或最后一题提交,Esc 取消。

// buildAskAnswer 把当前各题勾选态组装成回传给 LLM 的 JSON:
//
//	{"answers":[{"question":"...","selected":["value", ...]}, ...]}
func (m model) buildAskAnswer() string {
	type ans struct {
		Question string   `json:"question"`
		Selected []string `json:"selected"`
	}
	out := struct {
		Answers []ans `json:"answers"`
	}{}
	for qi, q := range m.askQuestions {
		sel := []string{}
		for oi, on := range m.askSelected[qi] {
			if on {
				sel = append(sel, q.Options[oi].Value)
			}
		}
		out.Answers = append(out.Answers, ans{Question: q.Question, Selected: sel})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// askUserBlock 渲染 AskUser 选择题弹窗(当前题)。
func (m model) askUserBlock() string {
	if len(m.askQuestions) == 0 || m.askQIdx >= len(m.askQuestions) {
		return ""
	}
	q := m.askQuestions[m.askQIdx]

	dim := lipgloss.NewStyle().Foreground(subtleColor)
	on := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(highlightColor)

	// 标题:多题时带进度;标注单/多选
	head := "请选择"
	if len(m.askQuestions) > 1 {
		head = fmt.Sprintf("需求确认  %d/%d", m.askQIdx+1, len(m.askQuestions))
	}
	kind := "单选"
	if q.Multiple {
		kind = "多选"
	}
	title := titleStyle.Render(head) + dim.Render("   ("+kind+")")

	question := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("252")).Width(54).Render(q.Question)

	var rows []string
	for i, opt := range q.Options {
		selected := m.askSelected[m.askQIdx][i]
		var box string
		if q.Multiple {
			box = "☐"
			if selected {
				box = "☑"
			}
		} else {
			box = "○"
			if selected {
				box = "●"
			}
		}
		marker := "  "
		if i == m.askOptIdx {
			marker = "▸ "
		}
		seg := marker + box + " " + opt.Label
		switch {
		case selected:
			seg = on.Render(seg)
		case i == m.askOptIdx:
			seg = lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(seg)
		default:
			seg = dim.Render(seg)
		}
		rows = append(rows, seg)
	}

	// 操作提示:选中只认空格,Enter 仅用于前进/提交。
	nextLabel := "Enter 提交"
	if m.askQIdx < len(m.askQuestions)-1 {
		nextLabel = "Enter 下一题"
	}
	hint := "↑↓ 移动 · 空格 选择 · " + nextLabel
	if len(m.askQuestions) > 1 {
		hint += " · ←→ 切题"
	}
	footer := dim.Render(hint + " · Esc 取消")

	parts := []string{title, "", question, ""}
	parts = append(parts, rows...)
	// 没选就回车 → 红色闪烁警告(用空格选),提醒用户下次记住正确操作。
	if m.askWarn {
		blinkOn := (time.Now().UnixMilli()/350)%2 == 0
		c := lipgloss.Color("196") // 亮红
		if !blinkOn {
			c = lipgloss.Color("88") // 暗红,形成闪烁
		}
		warn := lipgloss.NewStyle().Bold(true).Foreground(c).Render("⚠ 请先用【空格】选中,再按回车")
		parts = append(parts, "", warn)
	}
	parts = append(parts, "", footer)
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(highlightColor).
		Padding(1, 2).
		Width(60).
		Render(content)
}
