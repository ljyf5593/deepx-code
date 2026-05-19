package tui

import (
	"image/color"
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
// MouseMode 故意保持 None:bubbletea v2 只有 None/CellMotion/AllMotion 三档,
// 任何非 None 模式都会让终端进入 DEC 1000/1006 鼠标接管协议,native 拖拽选择/复制就失效。
// 我们选择保留终端原生选择(拖拽=选,Cmd+C=复制),代价是 chat 区不能滚轮翻滚,
// 改用 PgUp/PgDown/↑↓ 键盘滚动(model.go 已支持)。
func (m model) wrapView(content string) tea.View {
	v := tea.NewView(content)
	v.AltScreen = true
	v.MouseMode = tea.MouseModeNone
	return v
}

// layout 计算左侧 viewport 的宽度与高度。
// 总体结构:
//
//	外框(1) + 标题(1) + 横分隔(1) + viewport + 输入(1) + 外框(1) = m.height
//	所以 viewport 高度 = m.height - 5。
//
// 水平方向:innerW = chat + 滚动条(1) + 纵向分隔线(1) + right panel。
func (m model) layout() (leftW, vpH int) {
	innerW := m.width - 2
	rightW := rightPanelWidth
	if rightW > innerW/2 {
		rightW = innerW / 2
	}
	leftW = innerW - rightW - 1 - 1 // 1 = 纵向分隔线, 1 = chat 右边的滚动条列
	vpH = m.height - 5
	return
}

func (m model) View() tea.View {
	if m.width < 30 || m.height < 7 {
		return m.wrapView("Terminal too small.")
	}

	innerW := m.width - 2
	rightW := rightPanelWidth
	if rightW > innerW/2 {
		rightW = innerW / 2
	}
	leftW := innerW - rightW - 1 - 1 // 同 layout(): 含纵向分隔线 + 滚动条

	bodyH := m.height - 5
	if bodyH < 1 {
		bodyH = 1
	}

	header := m.renderHeader(innerW)
	headerSep := dividerStyle.Render(strings.Repeat("─", innerW))

	// chat + scrollbar 合成一个字符串,scrollbar 字符直接拼到每行末尾(不走 JoinHorizontal)。
	//
	// JoinHorizontal 做"按列对齐"会逐行测每列宽度,terminal 实际渲染宽度跟程序估算不一致时
	// (emoji / ZWJ / box drawing 等字体相关字符),scrollbar 列会左右波动。改成"chat 字符 +
	// scrollbar 字符"同行拼接后,scrollbar 永远紧贴 chat 字符流末尾,字符相对顺序由 ANSI 流
	// 决定,不再依赖列宽对齐。物理上 chat 行宽仍可能波动,但视觉上 scrollbar 始终贴 chat。
	chatPadded := padLinesToWidth(m.chatViewport.View(), leftW)
	chatLines := strings.Split(chatPadded, "\n")
	// 补 / 截到精确 bodyH 行
	for len(chatLines) < bodyH {
		chatLines = append(chatLines, strings.Repeat(" ", leftW))
	}
	if len(chatLines) > bodyH {
		chatLines = chatLines[:bodyH]
	}
	scrollbarLines := strings.Split(m.renderScrollbar(bodyH), "\n")
	for i := 0; i < bodyH && i < len(scrollbarLines); i++ {
		chatLines[i] += scrollbarLines[i]
	}
	chatWithBar := strings.Join(chatLines, "\n")

	// 输入框横跨 chat + 滚动条这两列,视觉上完整封底
	var inpInner string
	if m.inputAllSelected {
		// 全选态: 自己渲染 prompt + 反色 value,代替 textinput.View()
		// (textinput.View 会同时画光标/补 padding,不易精确套反色)
		inpInner = m.input.Prompt + lipgloss.NewStyle().Reverse(true).Render(m.input.Value())
	} else {
		inpInner = m.input.View()
	}
	inp := lipgloss.NewStyle().
		Width(leftW + 1).
		Height(1).
		MaxHeight(1).
		Foreground(lipgloss.Color("15")).
		Render(inpInner)
	left := lipgloss.JoinVertical(lipgloss.Left, chatWithBar, inp)

	totalH := bodyH + 1
	divider := dividerStyle.Render(strings.TrimRight(strings.Repeat("│\n", totalH), "\n"))

	right := lipgloss.NewStyle().
		Width(rightW).
		Height(totalH).
		Padding(0, 1).
		Render(m.rightPanelView())

	body := lipgloss.JoinHorizontal(lipgloss.Top, left, divider, right)
	inside := lipgloss.JoinVertical(lipgloss.Left, header, headerSep, body)

	mainUI := outerStyle.Render(inside)

	// 命令 palette:input value 以 "/" 起手时叠在输入框上方。
	// 不影响 chat viewport 高度,只是视觉遮挡 chat 区底部最后几行(用户能继续滚动看)。
	if matches := filterSlashCommands(m.input.Value()); len(matches) > 0 && !m.showSetup {
		idx := m.commandPaletteIdx
		if idx >= len(matches) {
			idx = len(matches) - 1
		}
		if idx < 0 {
			idx = 0
		}
		palette := renderCommandPalette(matches, idx, leftW)
		// 输入框 Y = 外框顶 1 + header 1 + headerSep 1 + chat bodyH 行 = 3+bodyH
		// palette 从 inputY - 行数 开始,左对齐到外框内 X=1
		inputY := 3 + bodyH
		startY := inputY - len(matches)
		if startY < 3 {
			startY = 3 // 不进 header / 分隔线
		}
		mainUI = overlayAt(mainUI, palette, 1, startY)
	}

	// modal 覆盖在主 UI 上居中显示,主 UI 仍可见(尤其首次启动让用户看到 deepx 的样子)
	if m.showSetup {
		mainUI = overlayCentered(mainUI, m.setupModalBlock(), m.width, m.height)
	}
	if m.reviewPending {
		mainUI = overlayCentered(mainUI, m.reviewBlock(), m.width, m.height)
	}
	// 锁到精确 m.width × m.height — Terminal.app 等终端对 ambiguous-width 字符 (◆/⏵ 等)
	// 的渲染跟 lipgloss 估算可能不一致,行宽不齐会让 bubbletea 的 line-diff 跳行,
	// 留下上一帧残影(右栏出现重复 section、滚动条断续)。强制 pad 到屏幕精确尺寸消除这一类。
	return m.wrapView(normalizeFrame(mainUI, m.width, m.height))
}

// normalizeFrame 把整帧锁到精确 width × height:
//   - 行数不够补空行(下方)
//   - 行数过多截掉(尾部)
//   - 每行宽度不够补空格,过宽用 ansi.Cut 切到精确 width
// 用同一套 ansi.StringWidth 测量,让 bubbletea 的 diff 看到稳定的帧形状。
func normalizeFrame(s string, width, height int) string {
	lines := strings.Split(s, "\n")
	// 截断或补足行数
	if len(lines) > height {
		lines = lines[:height]
	}
	for len(lines) < height {
		lines = append(lines, "")
	}
	// 每行 pad/truncate 到精确 width
	for i, ln := range lines {
		w := ansi.StringWidth(ln)
		switch {
		case w < width:
			lines[i] = ln + strings.Repeat(" ", width-w)
		case w > width:
			lines[i] = ansi.Cut(ln, 0, width)
		}
	}
	return strings.Join(lines, "\n")
}

// renderHeader 顶部标题:左侧产品名,右侧 Endpoint URL。
// 模型角色 / 模式 / 运行状态都移到右栏,header 留给静态信息(标题 + base_url)。
func (m model) renderHeader(width int) string {
	title := headerStyle.Render("deepx")

	// 右侧只放 endpoint;头部空间有限,超长 host 头部截断,保留 host 末尾(更具识别性)
	// 用 flash 的 base_url 当 header 默认显示;flash/pro 不同时,用户能在右栏看到差异。
	endpoint := m.models.Flash.BaseURL
	if endpoint == "" {
		endpoint = m.models.Pro.BaseURL
	}
	maxEndpoint := width - lipgloss.Width(title) - 2
	if maxEndpoint < 4 {
		maxEndpoint = 4
	}
	if len(endpoint) > maxEndpoint {
		endpoint = "…" + endpoint[len(endpoint)-(maxEndpoint-1):]
	}
	right := lipgloss.NewStyle().Foreground(subtleColor).Render(endpoint)

	titleW := lipgloss.Width(title)
	rightW := lipgloss.Width(right)

	gap := width - titleW - rightW
	if gap < 1 {
		gap = 1
	}
	line := title + strings.Repeat(" ", gap) + right
	return lipgloss.NewStyle().MaxWidth(width).Render(line)
}

// renderScrollbar 渲染 chat viewport 右侧的 1 列滚动条,高度等于 chat 区行数。
// 内容不超一屏时整列留空(保持列宽稳定,不让左侧 chat 因为没滚动条而变宽再变窄,造成跳动)。
//
// **关键**:thumb 和 track 都用"空格 + Background 色"渲染 — 同一种字符占同样的 cell 宽。
// 之前 track 曾用 `│` (U+2502) 字符,在 Terminal.app / 某些字体下被当 ambiguous-width 或
// 行间距不齐,跟空格的实际宽度不一致,导致滚动条不同行左右偏移、整列看起来歪歪扭扭。
// 用同一种字符(空格)就跟字形完全无关,任何字体 / 任何终端都是稳定 1 cell。
func (m model) renderScrollbar(height int) string {
	if height <= 0 {
		return ""
	}
	total := m.chatViewport.TotalLineCount()
	vis := m.chatViewport.VisibleLineCount()

	lines := make([]string, height)
	if total <= vis {
		for i := range lines {
			lines[i] = " "
		}
		return strings.Join(lines, "\n")
	}

	// 都是 Render(" "),lipgloss 把空格涂上 background 色,占满整 cell。
	thumbStyle := lipgloss.NewStyle().Background(highlightColor)
	trackStyle := lipgloss.NewStyle().Background(subtleColor)

	thumbH := vis * height / total
	if thumbH < 1 {
		thumbH = 1
	}
	if thumbH > height {
		thumbH = height
	}
	available := height - thumbH
	thumbStart := int(m.chatViewport.ScrollPercent() * float64(available))
	if thumbStart < 0 {
		thumbStart = 0
	}
	if thumbStart > available {
		thumbStart = available
	}

	for i := 0; i < height; i++ {
		if i >= thumbStart && i < thumbStart+thumbH {
			lines[i] = thumbStyle.Render(" ")
		} else {
			lines[i] = trackStyle.Render(" ")
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
	title := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("11")).Render("⏳  Review Required")
	desc := lipgloss.NewStyle().Foreground(lipgloss.Color("252")).Render(
		"Review " + m.reviewToolName + " " + truncateReviewArgs(m.reviewToolArgs, 40))

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
		yesStyle.Render("  [ YES ]"),
		noStyle.Render("  [ NO  ]"),
		"",
		lipgloss.NewStyle().Foreground(subtleColor).Render("↑/↓ 选择 · Enter 确认 · Esc 拒绝"),
	)
	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("11")).
		Padding(1, 2).
		Width(45).
		Render(content)
}

// truncateReviewArgs 截断审核时显示的参数。
func truncateReviewArgs(args string, max int) string {
	if len(args) <= max {
		return args
	}
	return args[:max] + "…"
}

func (m model) rightPanelView() string {
	label := lipgloss.NewStyle().Foreground(dimColor).Render
	subtle := lipgloss.NewStyle().Foreground(subtleColor).Render

	// 模型 id 太长时截断,避免把右栏挤变形
	flashID := truncate(m.models.Flash.Model, rightPanelWidth-8)
	proID := truncate(m.models.Pro.Model, rightPanelWidth-8)

	// 路径压缩 ~/.../tail
	cwd := abbreviatePath(m.workspace, rightPanelWidth-2)

	// 上下文用量(全部 history 字符数粗估 token)
	ctxChars := sumHistoryChars(m.history) + m.currentReply.Len()
	ctxTokens := estimateTokens(ctxChars)

	// 本轮 elapsed:streaming 时实时 time.Since,idle 时用 stream done 时冻结的 turnElapsed
	var elapsed time.Duration
	if m.streaming && !m.turnStartedAt.IsZero() {
		elapsed = time.Since(m.turnStartedAt)
	} else {
		elapsed = m.turnElapsed
	}

	// 本轮 I/O token 估算
	turnIn := estimateTokens(m.turnInputChars)
	turnOut := estimateTokens(m.turnOutputChars)

	// 活跃模型用 ▶ 直接标在 MODELS 行的左侧,不占额外行。
	// 占位用两空格保持非活跃行的对齐。
	arrowStyle := lipgloss.NewStyle().Foreground(highlightColor).Render("▶ ")
	flashIndicator := "  "
	proIndicator := "  "
	switch m.activeModelRole {
	case "flash":
		flashIndicator = arrowStyle
	case "pro":
		proIndicator = arrowStyle
	}

	// STATUS section: 给 idle/thinking/streaming 加 label,跟 mode 行对齐
	statusLine := label("status") + " " +
		lipgloss.NewStyle().Foreground(statusColor(m.status)).Render(m.status)
	modeLine := label("mode  ") + " " +
		lipgloss.NewStyle().Foreground(highlightColor).Render(string(m.mode))

	// section 是分组渲染 helper:标题用 ◆ 前缀 + 强调色加粗大写,内容缩进 2 空格,
	// 结尾留一行空白当分组分隔。沿用 lipgloss 的 "section header + indented body" 模式。
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
	// Workspace section:标题行尾接 session 哈希 (sha1(abs(wd))[:16],对应
	// ~/.deepx/sessions/<hash>/ 目录)。不走 section() 助手是为了让哈希用
	// subtle 暗色,而不是被 strings.ToUpper 大写化跟标题挤一起抢视觉权重。
	workspaceTitle := lipgloss.NewStyle().Foreground(highlightColor).Render("◆ ") +
		lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render("WORKSPACE")
	if m.session != nil {
		workspaceTitle += " " + subtle("("+m.session.SessionID()+")")
	}
	rows = append(rows, workspaceTitle, "  "+subtle(cwd), "")
	rows = append(rows, section("Models", []string{
		flashIndicator + label("flash ") + " " + flashID,
		proIndicator + label("pro   ") + " " + proID,
	})...)
	rows = append(rows, section("Status", []string{
		statusLine,
		modeLine,
	})...)
	rows = append(rows, section("Usage", []string{
		label("context") + " ~" + formatTokenCount(ctxTokens) + " tok",
		label("tokens ") + " ↑" + formatTokenCount(turnIn) +
			" ↓" + formatTokenCount(turnOut) +
			" " + formatElapsed(elapsed),
	})...)
	rows = append(rows, section("Commands", []string{
		label("/plan ") + " read-only",
		label("/auto ") + " all tools",
		label("/review ") + " review mode",
		label("/help ") + " help",
	})...)

	// 完整 plan 树在 chat 区显示,右栏只放进度摘要(X/Y done + 当前运行节点)。
	if planLines := renderPlanSummary(m.plan, rightPanelWidth-4); len(planLines) > 0 {
		rows = append(rows, section("Plan", planLines)...)
	}

	// 删掉最后那行多余的空行(每个 section 结尾都留了)
	if len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}

	return strings.Join(rows, "\n")
}
