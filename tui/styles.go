package tui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
)

const (
	// rightPanelWidth 右栏 section 区固定列宽。窄到能塞下两条 ◆ TITLE 行,宽到 model id
	// 不被 truncate 成 "deeps…"。32 跟 HEAD 一致,主流 IDE/终端默认宽度 (>= 100 列) 下
	// 至少给 chat 留 60+ 列。
	rightPanelWidth = 32
)

var (
	// 通用色:dim 给次要文本(label / 占位),subtle 给更暗的辅助信息,
	// accent / highlight 给右栏 section 标题和强调元素。
	dimColor       = lipgloss.Color("240")
	subtleColor    = lipgloss.Color("8")
	accentColor    = lipgloss.Color("12")
	highlightColor = lipgloss.Color("13")

	// outerStyle 不再画外框 — 整个主 UI 直接贴终端边缘,跟 Crush 风格一致。
	// 保留变量以兼容历史调用,但不带 border。
	outerStyle = lipgloss.NewStyle()

	dividerStyle = lipgloss.NewStyle().Foreground(subtleColor)

	// === 色条(quote bar)样式 ===
	//
	// 每条消息左侧画一根色条:首行 ╭、中间行 │、末行 ╰。
	// 视觉上像把消息"包"在一对括号里,身份靠色条颜色区分,不再用 inline emoji 前缀。
	//
	// 颜色挑选避开终端默认 0-15 色,用 256-color 调色板里饱和度低、暗背景下不刺眼的中间色:
	//   - user      cyan          消息从用户来,凉色
	//   - assistant violet/purple deepx 回复,跟终端常见绿色错开
	//   - tools     amber         工具组,暖色,跟 assistant 错开
	//   - system    gray          系统提示,最弱视觉权重
	userBarColor      = lipgloss.Color("39")  // 亮天蓝
	assistantBarColor = lipgloss.Color("141") // 淡紫
	toolsBarColor     = lipgloss.Color("215") // 琥珀
	systemBarColor    = lipgloss.Color("242") // 中灰

	// 用户回合做成"气泡":钢蓝底 + 白字 + 亮青块条,延续"用户=蓝"的语义,跟左对齐的 assistant/tools 错开。
	userBubbleBg  = lipgloss.Color("24")  // 深钢蓝底
	userBubbleFg  = lipgloss.Color("231") // 白字
	userBubbleBar = lipgloss.Color("51")  // 亮青块条

	// 色条总列宽 = 缩进 + 竖线 + 1 空格。
	// 一级(user/assistant/system):2 列 = ┃ + 空格;不缩进。
	// 二级(tools):4 列 = 2 列缩进 + │ + 空格;视觉上嵌入到一级段内部。
	barColWidthLevel1 = 2
	barColWidthLevel2 = 4
)

// barColorFor 根据 segment kind 返回对应色条颜色。
// 未知 kind 兜底用 system 灰色 — 不致命,只是视觉上跟系统提示同列。
func barColorFor(kind string) color.Color {
	switch kind {
	case kindUser:
		return userBarColor
	case kindAssistant:
		return assistantBarColor
	case kindTools:
		return toolsBarColor
	case kindSystem:
		return systemBarColor
	}
	return systemBarColor
}

// applyQuoteBar 在已渲染 ANSI 文本左侧画一根色条,做两级引用。
//
//	一级(user / assistant / system):每行前缀 "┃ " (U+2503 粗竖线 + 空格),不缩进
//	二级(tools):每行前缀 "  │ " (2 空格缩进 + U+2502 细竖线 + 空格)
//
// 所有行统一前缀,不再用 ╭/╰ 端点 — 单行段也不会出现"半截括号"错位的视觉。
// 色条字符按 kind 着色,正文 1 cell 间距。
//
// 输入 content 应该已经按 barInnerWidth(width, kind) 软换行 — 这里只加前缀,不再 wrap。
func applyQuoteBar(content, kind string) string {
	if content == "" {
		return ""
	}
	var (
		barChar string
		indent  string
	)
	switch kind {
	case kindTools:
		barChar = "│" // 细线,作为二级 quote
		indent = "  " // 2 列缩进,视觉上嵌入到上方 assistant 段
	default:
		barChar = "┃" // 粗线,user/assistant/system 一级 quote
		indent = ""
	}
	bar := lipgloss.NewStyle().Foreground(barColorFor(kind))
	prefix := indent + bar.Render(barChar) + " "
	lines := strings.Split(content, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}

// renderUserBubble 把用户回合渲染成气泡:左侧 1 列 ┃ 色条(与 LLM 一级 quote 同字符,多行连续)
// + 整段浅色底,按视口宽度铺满。不走 glamour —— 用户输入是纯文本(路径 / 代码片段居多),
// 气泡里按字面显示反而更清晰,也省掉 markdown 转义带来的意外(见 backslashSentinel)。
// lipgloss 的 Width 负责按宽换行 + 背景铺满。
func renderUserBubble(text string, viewportW int) string {
	if viewportW <= 0 || text == "" {
		return text
	}
	boxW := viewportW - 1 // 左侧色条占 1 列
	if boxW < 1 {
		boxW = 1
	}
	// 暂时去掉钢蓝色底(userBubbleBg),只保留左色条 + 白字 + 按宽换行,避免整片底色显乱。
	box := lipgloss.NewStyle().
		Foreground(userBubbleFg).
		Width(boxW).
		Padding(0, 1)
	bar := lipgloss.NewStyle().Foreground(userBubbleBar).Render("┃")
	lines := strings.Split(box.Render(text), "\n")
	for i, ln := range lines {
		lines[i] = bar + ln
	}
	return strings.Join(lines, "\n")
}

// barInnerWidth 计算正文区可用宽度(扣掉色条占用的列)。
// tools 段比一级段额外扣 2 列缩进。
func barInnerWidth(viewportW int, kind string) int {
	bar := barColWidthLevel1
	if kind == kindTools {
		bar = barColWidthLevel2
	}
	w := viewportW - bar
	if w < 1 {
		w = 1
	}
	return w
}

// indentBlock 给多行内容的每一行前面加 prefix(典型用法:缩进 2 个空格)。
// 用于 plan / spinner 等临时 overlay 内容,跟上方色条段视觉对齐。
func indentBlock(content, prefix string) string {
	if content == "" {
		return content
	}
	lines := strings.Split(content, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}
