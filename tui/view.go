package tui

import (
	"image/color"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// wrapView 把字符串 mainUI 包成 v2 tea.View,并设置 alt-screen / mouse mode 等终端能力。
// v2 的 View() 不再返回 string,而是带元数据的结构体;终端选项也从 NewProgram 的
// option 迁到 View 字段(声明式)。
//
// MouseMode = CellMotion:启用 DEC 1002 + SGR 1006,鼠标事件(wheel / click / drag)
// 全部送进程序。代价是 native 终端拖拽选择被接管 — 用户要在 chat 区拖拽选择得用
// Option+拖拽(macOS Terminal.app / iTerm2 / Kitty 都支持 Option/Alt 旁路鼠标追踪)。
// 收益是触控板/滚轮直接滚 chat(MouseWheelMsg → chatViewport.Update),
// 程序内拖拽选区会高亮并在松开时写入系统剪贴板(model.go 的 selection 流程)。
func (m model) wrapView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeCellMotion
	// 开 Kitty keyboard 协议的"alternate keys"上报 — 让 Ctrl+Enter / Shift+Enter
	// 等组合键以独立 escape 序列发到程序,而不是被终端合并成普通 Enter。
	// 支持的终端:Kitty / Wezterm / Foot / iTerm2(实验性);macOS Terminal.app 不支持
	// (这俩组合在 Terminal.app 下仍跟 Enter 等价,只能用 Alt/Option+Enter 换行)。
	v.KeyboardEnhancements.ReportAlternateKeys = true
	return v
}

// inputAreaHeight 是底部输入框占用的固定行数。textarea 3 行 + 1 行顶部留白让输入区跟
// 上方 chat 视觉分开。改这个值要同步 palette overlay 起点。
const inputAreaHeight = 4

// inputGutterWidth 是输入区左侧固定 gutter 列宽:首行画 "> ",其余行 "  "。
// textarea 实际宽度 = m.width - inputGutterWidth。
const inputGutterWidth = 2

// inputPromptStyle 是 gutter 里 "> " 的样式(粉紫加粗,同 banner 主色)。
var inputPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true)

// layout 计算 chat viewport 的宽度与高度。
// 总体结构:
//
//	body(vpH) + input(inputAreaHeight) = m.height
//	body 内部:chat(leftW) + divider(1) + rightPanel(rightW),banner 摆右栏顶部。
//	vpH = m.height - inputAreaHeight
//	leftW = m.width - rightPanelWidth - 1
func (m model) layout() (leftW, vpH int) {
	rightW := rightPanelWidth
	if rightW > m.width/2 {
		rightW = m.width / 2
	}
	leftW = m.width - rightW - 1
	if leftW < 1 {
		leftW = 1
	}
	vpH = m.height - inputAreaHeight
	if vpH < 1 {
		vpH = 1
	}
	return
}

func (m model) View() tea.View {
	if m.width < 30 || m.height < 7 {
		return m.wrapView(T("misc.terminal_too_small"))
	}

	rightW := rightPanelWidth
	if rightW > m.width/2 {
		rightW = m.width / 2
	}
	leftW := m.width - rightW - 1 // 1 = 竖分隔线
	if leftW < 1 {
		leftW = 1
	}
	bodyH := m.height - inputAreaHeight
	if bodyH < 1 {
		bodyH = 1
	}

	// chat 区:pad/截到精确 leftW × bodyH。
	chatPadded := padLinesToWidth(m.chatViewport.View(), leftW)
	chatLines := strings.Split(chatPadded, "\n")
	for len(chatLines) < bodyH {
		chatLines = append(chatLines, strings.Repeat(" ", leftW))
	}
	if len(chatLines) > bodyH {
		chatLines = chatLines[:bodyH]
	}

	// 右栏:status section 区,固定 rightW × bodyH。
	right := lipgloss.NewStyle().
		Width(rightW).
		Height(bodyH).
		Padding(0, 1).
		Render(m.rightPanelView())
	rightLines := strings.Split(right, "\n")
	for len(rightLines) < bodyH {
		rightLines = append(rightLines, strings.Repeat(" ", rightW))
	}
	if len(rightLines) > bodyH {
		rightLines = rightLines[:bodyH]
	}

	// 手动逐行拼接:chat_line + │ + right_line。
	// 不走 lipgloss.JoinHorizontal — JoinHorizontal 按 lipgloss 测出的列宽对齐,而
	// chat 行里的 emoji / 特殊字符(`🐚` `⛅` `°C` 等)在 ansi.StringWidth vs
	// terminal 实际渲染之间可能差 1 cell,逐行差异导致 `│` 在某些行偏移、视觉上断开。
	// 字符串拼接让 `│` 永远紧跟在 chat 最后一个字符之后,跟 chat 行内容流式衔接,
	// 即使 chat 行实际宽度跟预期不一致,`│` 也连贯不断。
	dividerStyleP := lipgloss.NewStyle().Foreground(bannerDecoColor)
	dividerChar := dividerStyleP.Render("│")
	bodyLines := make([]string, bodyH)
	for i := 0; i < bodyH; i++ {
		bodyLines[i] = chatLines[i] + dividerChar + rightLines[i]
	}
	body := strings.Join(bodyLines, "\n")

	// 输入区 = 左侧固定 gutter + 右侧 textarea,逐行拼接。
	// gutter 首行 "> "(粉紫),其余行 "  ";textarea 宽度已是 m.width-gutter。
	// 这样多行粘贴 / 滚动时 "> " 始终钉在左上角,不会跟内容滚走。
	taView := m.input.View()
	taLines := strings.Split(taView, "\n")
	if m.inputAllSelected {
		// 逐行 strip 成纯文本再套反色:textarea 输出里的内部 reset(\x1b[0m)会取消整段反色,
		// 导致全选看不到高亮。只反色每行实际文字,空行 / 尾部空白不动。
		for i, ln := range taLines {
			plain := strings.TrimRight(ansi.Strip(ln), " ")
			if plain == "" {
				continue
			}
			taLines[i] = ansiReverseOn + plain + ansiReverseOff
		}
	}
	inputRows := make([]string, len(taLines))
	for i, tl := range taLines {
		gutter := strings.Repeat(" ", inputGutterWidth)
		if i == 0 {
			gutter = inputPromptStyle.Render("> ")
		}
		inputRows[i] = gutter + tl
	}
	inpInner := strings.Join(inputRows, "\n")
	inputBlock := strings.Repeat(" ", m.width) + "\n" + inpInner

	mainUI := lipgloss.JoinVertical(lipgloss.Left, body, inputBlock)

	// 复制成功提示:在鼠标松开的位置叠一个绿色"✓ 已复制"小标(copyHintClearMsg 到点清空)。
	if m.copyHint != "" {
		hint := lipgloss.NewStyle().Foreground(lipgloss.Color("0")).Background(lipgloss.Color("10")).Bold(true).
			Render(" " + m.copyHint + " ")
		hw := ansi.StringWidth(hint)
		x := m.copyHintX + 1 // 紧贴松开点右边一点,不盖住光标
		if x+hw > m.width {
			x = m.width - hw // 贴右边界,别出屏
		}
		if x < 0 {
			x = 0
		}
		y := m.copyHintY
		if y < 0 {
			y = 0
		}
		if y >= m.height {
			y = m.height - 1
		}
		mainUI = overlayAt(mainUI, hint, x, y)
	}

	// 命令 palette:input value 以 "/" 起手时叠在输入框上方。
	if matches := filterSlashCommands(m.input.Value()); len(matches) > 0 && !m.showSetup {
		idx := m.commandPaletteIdx
		if idx >= len(matches) {
			idx = len(matches) - 1
		}
		if idx < 0 {
			idx = 0
		}
		palette := renderCommandPalette(matches, idx, leftW)
		// 输入框首行 Y = body(bodyH) + 1 行顶部空白
		inputY := bodyH + 1
		startY := inputY - len(matches)
		if startY < 0 {
			startY = 0
		}
		mainUI = overlayAt(mainUI, palette, 0, startY)
	}

	// modal 覆盖在主 UI 上居中显示。
	if m.showSetup {
		mainUI = overlayCentered(mainUI, m.setupModalBlock(), m.width, m.height)
	}
	if m.reviewPending {
		mainUI = overlayCentered(mainUI, m.reviewBlock(), m.width, m.height)
	}
	if m.showLangModal {
		mainUI = overlayCentered(mainUI, m.langModalBlock(), m.width, m.height)
	}
	v := m.wrapView(normalizeFrame(mainUI, m.width, m.height))
	// 真实终端光标定位到 input 内的 cursor 位置。
	// textarea.Cursor() 给的 X/Y 是相对 textarea 自身的局部坐标;input 起始 Y =
	// body 占的行数 + 1(上方空白行);X 要加上左侧 gutter 宽(textarea 现在从 gutter 右边起)。
	// modal 打开时不显示真实光标 —— 避免光标卡在 modal 背后。
	// cursorBlinkOff 由 cursorBlinkTickMsg 600ms 切一次:亮时塞 Cursor,灭时不塞 —
	// 不依赖终端的 DECSCUSR blink 支持,VS Code 终端等也能闪。
	if !m.showSetup && !m.showLangModal && !m.reviewPending && !m.cursorBlinkOff {
		if c := m.input.Cursor(); c != nil {
			c.Position.X += inputGutterWidth
			c.Position.Y += bodyH + 1
			v.Cursor = c
		}
	}
	return v
}

// normalizeFrame 把整帧锁到精确 width × height:
//   - 行数不够补空行(下方)
//   - 行数过多截掉(尾部)
//   - 每行宽度不够补空格,过宽用 ansi.Cut 切到精确 width
//
// 测量统一走 lineDisplayWidth(启动时按终端选 WcWidth / GraphemeWidth),跟 chat 行 pad
// 用同一套口径,divider 才不会被推偏。
func normalizeFrame(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	for i, ln := range lines {
		w := lineDisplayWidth(ln)
		switch {
		case w < width:
			lines[i] = ln + strings.Repeat(" ", width-w)
		case w > width:
			lines[i] = ansi.Cut(ln, 0, width)
		}
	}
	return strings.Join(lines, "\n")
}

func statusColor(s string) color.Color {
	switch s {
	case "idle":
		return lipgloss.Color("10")
	case "thinking":
		return lipgloss.Color("11")
	case "streaming":
		return lipgloss.Color("14")
	case "tool":
		return highlightColor
	case "error":
		return lipgloss.Color("9")
	}
	return lipgloss.Color("7")
}

func statusIcon(s string) string {
	switch s {
	case "idle":
		return "●"
	case "thinking":
		return "◌"
	case "streaming":
		return "▶"
	case "tool":
		return "⚙"
	case "error":
		return "✗"
	}
	return "·"
}

// reviewBlock 渲染审核确认弹窗。
func (m model) reviewBlock() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11")).Render(T("review.title"))
	desc := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(
		T("review.desc_prefix") + m.reviewToolName + " " + truncateReviewArgs(m.reviewToolArgs, 40))

	yesStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	noStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	if m.reviewYesNo {
		yesStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")).Background(lipgloss.Color("236"))
	} else {
		noStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9")).Background(lipgloss.Color("236"))
	}
	content := lipgloss.JoinVertical(lipgloss.Left,
		title,
		"",
		desc,
		"",
		yesStyle.Render(T("review.yes")),
		noStyle.Render(T("review.no")),
		"",
		lipgloss.NewStyle().Foreground(subtleColor).Render(T("review.footer")),
	)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("11")).
		Padding(1, 2).
		Width(45).
		Render(content)
}

// versionLine 渲染右栏 banner 下方的版本行。
// 默认显示 `v<current>`(暗色),如果检测到 GitHub 有更高版本则改成
// `v<current> ↑ <latest>` 并把 ↑ 跟新版本染高亮色 — 视觉上是温和提示而非弹窗。
// width 用来居中:右栏内宽 = rightPanelWidth-2,banner 居中,这里也居中对齐。
func (m model) versionLine(width int) string {
	cur := m.version
	if cur == "" {
		cur = "dev"
	}
	dim := lipgloss.NewStyle().Foreground(subtleColor).Render
	hi := lipgloss.NewStyle().Foreground(highlightColor).Bold(true).Render

	left := dim("v" + cur)
	if m.upgradeAvailable && m.latestVersion != "" {
		left = left + " " + hi("↑ "+m.latestVersion)
	}
	// 居中 pad,跟 banner 同一对齐
	cellWidth := ansi.StringWidth(ansi.Strip(left))
	if cellWidth >= width {
		return left
	}
	pad := (width - cellWidth) / 2
	return strings.Repeat(" ", pad) + left
}

// langModalBlock 渲染 /lang 语言选择弹窗。两项:中文 / 英文,langModalIdx 是当前光标。
func (m model) langModalBlock() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(highlightColor).Render(T("lang.title"))

	options := []string{T("lang.option.zh"), T("lang.option.en")}
	rows := make([]string, 0, len(options))
	for i, opt := range options {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		if i == m.langModalIdx {
			marker = "▸ "
			style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")).Background(lipgloss.Color("236"))
		}
		rows = append(rows, style.Render(marker+opt))
	}

	footer := lipgloss.NewStyle().Foreground(subtleColor).Render(T("lang.footer"))
	parts := []string{title, ""}
	parts = append(parts, rows...)
	parts = append(parts, "", footer)
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(highlightColor).
		Padding(1, 2).
		Width(42).
		Render(content)
}

// truncateReviewArgs 截断审核时显示的参数。
func truncateReviewArgs(args string, max int) string {
	if len(args) <= max {
		return args
	}
	return args[:max] + "…"
}

// rightPanelView 渲染右侧状态栏:Workspace / Models / Status / Usage / Commands / Plan。
// 每段标题用 ◆ 前缀 + 大写,内容缩进 2 空格。section 之间留 1 行空行做视觉分隔。
func (m model) rightPanelView() string {
	label := lipgloss.NewStyle().Foreground(dimColor).Render
	subtle := lipgloss.NewStyle().Foreground(subtleColor).Render

	// 模型 id 太长就截断,避免把右栏挤变形
	flashID := truncate(m.models.Flash.Model, rightPanelWidth-8)
	proID := truncate(m.models.Pro.Model, rightPanelWidth-8)

	// 路径压缩 ~/.../tail
	cwd := abbreviatePath(m.workspace, rightPanelWidth-2)

	// 本轮 elapsed:streaming 时实时算,idle 时用 stream done 时冻结的 turnElapsed
	var elapsed time.Duration
	if m.streaming && !m.turnStartedAt.IsZero() {
		elapsed = time.Since(m.turnStartedAt)
	} else {
		elapsed = m.turnElapsed
	}

	// 活跃模型用 ▶ 标记。占位用两空格保持对齐。
	arrowStyle := lipgloss.NewStyle().Foreground(highlightColor).Render("▶ ")
	flashIndicator := "  "
	proIndicator := "  "
	switch m.activeModelRole {
	case "flash":
		flashIndicator = arrowStyle
	case "pro":
		proIndicator = arrowStyle
	}

	statusLine := label(T("panel.label.status")) + " " +
		lipgloss.NewStyle().Foreground(statusColor(m.status)).Render(T("status."+m.status))
	modeLine := label(T("panel.label.mode")) + " " +
		lipgloss.NewStyle().Foreground(highlightColor).Render(string(m.mode))

	// section 渲染助手:标题 + 缩进内容 + 末尾空行。
	section := func(title string, body []string) []string {
		out := make([]string, 0, len(body)+2)
		out = append(out,
			lipgloss.NewStyle().Foreground(highlightColor).Render("◆ ")+
				lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(strings.ToUpper(title)),
		)
		for _, line := range body {
			out = append(out, "  "+line)
		}
		out = append(out, "")
		return out
	}

	rows := []string{}
	// 5 行文字 logo:上 `/` 修饰条 + 3 行 deepx ascii art + 下 `/` 修饰条。
	// 右栏宽 rightPanelWidth,Padding(0,1) 后内宽 rightPanelWidth-2,banner 渲到这个宽度。
	bannerInnerW := rightPanelWidth - 2
	if banner := renderBanner(bannerInnerW); banner != "" {
		rows = append(rows, strings.Split(banner, "\n")...)
	}

	// 版本行紧贴 banner 下:`v0.1.0` 或 `v0.1.0 ↑ 0.2.0`(有新版本时高亮)。
	rows = append(rows, m.versionLine(bannerInnerW), "")

	// Endpoint section:api host(去 scheme / path)
	endpoint := m.models.Flash.BaseURL
	if endpoint == "" {
		endpoint = m.models.Pro.BaseURL
	}
	if endpoint == "" {
		endpoint = "(not set)"
	}
	host := endpoint
	if idx := strings.Index(host, "://"); idx >= 0 {
		host = host[idx+3:]
	}
	if idx := strings.IndexAny(host, "/?"); idx >= 0 {
		host = host[:idx]
	}
	rows = append(rows, section(T("panel.endpoint"), []string{subtle(host)})...)

	// 注:web dashboard 地址不在右栏显示 —— 启动时已在 chat 区给出可点击 / 已复制的提示,
	// 这里再放一份既重复又因面板窄被迫折行,反而没法点。

	// Workspace section:标题尾接 session 哈希。不走 section() 是为了让哈希用 subtle 暗色。
	workspaceTitle := lipgloss.NewStyle().Foreground(highlightColor).Render("◆ ") +
		lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(strings.ToUpper(T("panel.workspace")))
	if m.session != nil {
		workspaceTitle += " " + subtle("("+m.session.SessionID()[:8]+")")
	}
	rows = append(rows, workspaceTitle, "  "+subtle(cwd), "")

	rows = append(rows, section(T("panel.models"), []string{
		flashIndicator + label(T("panel.label.flash")) + " " + flashID,
		proIndicator + label(T("panel.label.pro")) + " " + proID,
	})...)
	rows = append(rows, section(T("panel.status"), []string{
		statusLine,
		modeLine,
	})...)
	// 首轮 API 调用结束才能拿到 lastUsage,没拿到前用 "—" 占位,保持布局一致。
	// 分母用 PromptTokens 而非 hit+miss:DeepSeek API 保证 prompt_tokens = hit + miss,
	// 但 hit/miss 是 DeepSeek 私有字段,兼容 OpenAI 的模型可能不返回,用 PromptTokens 更稳。
	promptStr, outputStr, cacheStr := "—", "—", "—"
	if u := m.lastUsage; u != nil {
		promptStr = formatTokenCount(u.PromptTokens) + " tok"
		outputStr = formatTokenCount(u.CompletionTokens) + " tok"
		if u.PromptTokens > 0 {
			cacheStr = strconv.Itoa(u.PromptCacheHitTokens*100/u.PromptTokens) + "% hit"
		} else {
			cacheStr = "0% hit"
		}
	}
	rows = append(rows, section(T("panel.usage"), []string{
		label(T("panel.label.prompt")) + " " + promptStr,
		label(T("panel.label.output")) + " " + outputStr,
		label(T("panel.label.cache")) + " " + cacheStr,
		label(T("panel.label.time")) + " " + formatElapsed(elapsed),
	})...)
	rows = append(rows, section(T("panel.commands"), []string{
		label("/plan   ") + "Write/Bash off",
		label("/auto   ") + "Write/Bash on",
		label("/review ") + "Write/Bash ask",
		label("/lang   ") + "zh / en",
		label("/help   ") + "all cmds",
	})...)

	// 完整 plan 树在 chat 区显示,右栏只放进度摘要。
	if planLines := renderPlanSummary(m.plan, rightPanelWidth-4); len(planLines) > 0 {
		rows = append(rows, section(T("panel.plan"), planLines)...)
	}

	// 删掉最后那行多余空行
	if len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}

	return strings.Join(rows, "\n")
}
