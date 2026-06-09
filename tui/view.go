package tui

import (
	"image/color"
	"strconv"
	"strings"
	"time"

	"deepx/tools"

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
	// 文本输入类弹窗(填 API key / MCP / skill / web 配置)打开时关掉鼠标捕获,
	// 让终端恢复原生右键粘贴(WSL2 / Windows Terminal 等)。否则鼠标模式开着时,
	// 右键被当成鼠标事件吞掉,用户只能 Ctrl+V(见 issue #40)。这些弹窗内不需要拖拽/滚动。
	if m.showSetup || m.showMcpAdd || m.showSkillAdd || m.showWebConfig {
		v.MouseMode = tea.MouseModeNone
	}
	// 开 Kitty keyboard 协议的"alternate keys"上报 — 让 Ctrl+Enter / Shift+Enter
	// 等组合键以独立 escape 序列发到程序,而不是被终端合并成普通 Enter。
	// 支持的终端:Kitty / Wezterm / Foot / iTerm2(实验性);macOS Terminal.app 不支持
	// (这俩组合在 Terminal.app 下仍跟 Enter 等价,只能用 Alt/Option+Enter 换行)。
	v.KeyboardEnhancements.ReportAlternateKeys = true
	return v
}

// inputTopPad / inputBotPad 是输入区 textarea 上下的留白行数。顶部留 2 行把输入框跟上方
// chat 拉开,底部不留白。改这俩值光标 / palette 起点会跟着 inputTopPad 自动对齐。
const inputTopPad = 2
const inputBotPad = 0

// inputAreaHeight 是底部输入框占用的固定行数:textarea 3 行 + 上下留白。
// textarea 高 = inputAreaHeight - inputTopPad - inputBotPad。
const inputAreaHeight = 3 + inputTopPad + inputBotPad

// inputGutterWidth 是输入区左侧固定 gutter 列宽:首行画 "❱ ",其余行 "  "。
// textarea 实际宽度 = m.width - inputGutterWidth。
const inputGutterWidth = 2

// inputPromptStyle 是 gutter 里 "❱ " 的样式(粉紫加粗,同 banner 主色)。
var inputPromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("99")).Bold(true)

var (
	scrollbarDividerStyle = lipgloss.NewStyle().Foreground(bannerDecoColor)     // 轨道:暗色粗 ┃
	scrollbarThumbStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("255")) // 滑块:亮白粗 ┃
)

// scrollbarWidth 是右侧分隔线/滚动条区占的列数:1 列。
const scrollbarWidth = 1

// scrollbarDividers 生成 body 每行右侧的分隔线/滚动条字形:轨道行用暗色 `┃`,滑块行用亮白 `┃`
// (同一字符、同列、同宽、上下连续,只靠加亮区分;与左侧引用条 ┃ 一致)。
func (m model) scrollbarDividers(height int) []string {
	track := scrollbarDividerStyle.Render("┃")
	thumbStr := scrollbarThumbStyle.Render("┃")
	out := make([]string, height)
	for i := range out {
		out[i] = track
	}
	if height <= 0 {
		return out
	}
	total := m.chatViewport.TotalLineCount()
	visible := m.chatViewport.Height()
	if total <= visible || visible <= 0 {
		return out
	}
	thumb := height * visible / total
	if thumb < 1 {
		thumb = 1
	}
	if thumb > height {
		thumb = height
	}
	maxOff := total - visible
	yoff := m.chatViewport.YOffset()
	if yoff < 0 {
		yoff = 0
	}
	if yoff > maxOff {
		yoff = maxOff
	}
	pos := 0
	if maxOff > 0 {
		pos = yoff * (height - thumb) / maxOff
	}
	if pos > height-thumb {
		pos = height - thumb
	}
	for i := pos; i < pos+thumb && i < height; i++ {
		out[i] = thumbStr
	}
	return out
}

// scrollChatToTrackRow 把滚动条轨道上的第 row 行(光标 Y - chatTop)映射成 chat 的滚动偏移并应用。
// trackH 是轨道高度(= 可视行数)。滑块中心贴着光标;越界自动钳到 [0, maxOff]。内容不溢出则什么都不做。
func (m *model) scrollChatToTrackRow(row, trackH int) {
	total := m.chatViewport.TotalLineCount()
	visible := m.chatViewport.Height()
	if total <= visible || visible <= 0 || trackH <= 0 {
		return
	}
	thumb := trackH * visible / total
	if thumb < 1 {
		thumb = 1
	}
	if thumb > trackH {
		thumb = trackH
	}
	maxOff := total - visible
	target := maxOff
	if denom := trackH - thumb; denom > 0 {
		target = (row - thumb/2) * maxOff / denom // 滑块中心对齐光标
	}
	if target < 0 {
		target = 0
	}
	if target > maxOff {
		target = maxOff
	}
	m.chatViewport.SetYOffset(target)
}

// layout 计算 chat viewport 的宽度与高度。
// 总体结构:
//
//	body(vpH) + input(inputAreaHeight) = m.height
//	body 内部:chat(leftW) + divider(1) + rightPanel(rightW),banner 摆右栏顶部。
//	vpH = m.height - inputAreaHeight
//	leftW = m.width - rightPanelWidth - 1
func (m model) layout() (leftW, vpH int) {
	rightW := 0 // 隐藏状态栏 → rightW=0 → chat 整宽(仅留最右列给滚动条)
	if !m.hideStatusPanel {
		rightW = rightPanelWidth
		if rightW > m.width/2 {
			rightW = m.width / 2
		}
	}
	leftW = m.width - rightW - scrollbarWidth
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

	rightW := 0 // 隐藏状态栏时 rightW=0 → chat 铺满整宽(仅留最右一列给滚动条/分隔线)
	if !m.hideStatusPanel {
		rightW = rightPanelWidth
		if rightW > m.width/2 {
			rightW = m.width / 2
		}
	}
	leftW := m.width - rightW - scrollbarWidth // 右侧留 scrollbarWidth 列给分隔线/滚动条
	if leftW < 1 {
		leftW = 1
	}
	// 排队区(流式中暂存的待发送消息)挂在输入框上方,占 queuedH 行,从 body 高度里扣。
	// 别让它把对话挤没:至少给 chat 留 1 行。
	queuedLines := m.queuedDisplayLines(m.width)
	if maxQ := m.height - inputAreaHeight - 1; len(queuedLines) > maxQ {
		if maxQ < 0 {
			maxQ = 0
		}
		queuedLines = queuedLines[:maxQ]
	}
	queuedH := len(queuedLines)

	bodyH := m.height - inputAreaHeight - queuedH
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
		// 仅当排队区压缩了 body 时才会溢出(viewport 自身高度=无排队时的 bodyH)。
		// 留"尾部"而非头部:流式时 viewport 贴底,最新输出在末尾,要保证它不被排队区盖掉。
		chatLines = chatLines[len(chatLines)-bodyH:]
	}

	// 右栏:status section 区,固定 rightW × bodyH。隐藏时全空行(不渲染状态栏)。
	rightLines := make([]string, bodyH)
	if !m.hideStatusPanel {
		right := lipgloss.NewStyle().
			Width(rightW).
			Height(bodyH).
			Padding(0, 1).
			Render(m.rightPanelView())
		rightLines = strings.Split(right, "\n")
		for len(rightLines) < bodyH {
			rightLines = append(rightLines, strings.Repeat(" ", rightW))
		}
		if len(rightLines) > bodyH {
			rightLines = rightLines[:bodyH]
		}
	}

	// 手动逐行拼接:chat_line + 分隔线/滚动条(scrollbarWidth 列)+ right_line。
	divs := m.scrollbarDividers(bodyH)
	bodyLines := make([]string, bodyH)
	for i := 0; i < bodyH; i++ {
		bodyLines[i] = chatLines[i] + divs[i] + rightLines[i]
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
			gutter = inputPromptStyle.Render("❱ ")
		}
		inputRows[i] = gutter + tl
	}
	// 输入区不画竖分隔线 —— 分隔线只到 body 底(对话+右栏区),输入区整宽。
	// 顶部 / 底部按 inputTopPad / inputBotPad 留白,normalizeFrame 会把空行补成整宽。
	inputLines := make([]string, 0, queuedH+len(inputRows)+inputTopPad+inputBotPad)
	for i := 0; i < inputTopPad; i++ {
		// 顶部留白的第一行用来挂活动状态行(运行中 spinner+耗时 / 空闲"就绪"),
		// 其余仍是空行。inputTopPad 不变,光标 Y(bodyH+inputTopPad)也不变。
		if i == 0 && inputTopPad > 0 {
			inputLines = append(inputLines, m.statusFooterLine(m.width))
			continue
		}
		inputLines = append(inputLines, "") // 顶部留白行
	}
	// 排队区放在活动状态行之后、输入框之前(紧贴输入框),让"待发送"和你正在打的字成组。
	inputLines = append(inputLines, queuedLines...)
	inputLines = append(inputLines, inputRows...)
	for i := 0; i < inputBotPad; i++ {
		inputLines = append(inputLines, "") // 底部留白行
	}
	inputBlock := strings.Join(inputLines, "\n")

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
		// 输入框首行 Y = body(bodyH) + 排队区行数 + 顶部留白行数
		inputY := bodyH + queuedH + inputTopPad
		startY := inputY - len(matches)
		if startY < 0 {
			startY = 0
		}
		mainUI = overlayAt(mainUI, palette, 0, startY)
	} else if _, _, query, active := fileMentionContext(m.input.Value(), m.input.Line(), m.input.Column()); active && len(m.fileMentionCache) > 0 && !m.showSetup {
		// @ 文件提及选择器:叠在输入框上方(与 / palette 同位,二者互斥)。
		if matches := filterWorkspaceFiles(query, m.fileMentionCache, fileMentionMaxRows); len(matches) > 0 {
			idx := m.fileMentionIdx
			if idx >= len(matches) {
				idx = len(matches) - 1
			}
			if idx < 0 {
				idx = 0
			}
			palette := renderFileMentionPalette(matches, idx, leftW)
			inputY := bodyH + queuedH + inputTopPad
			startY := inputY - len(matches)
			if startY < 0 {
				startY = 0
			}
			mainUI = overlayAt(mainUI, palette, 0, startY)
		}
	}

	// modal 覆盖在主 UI 上居中显示。
	if m.showSetup {
		mainUI = overlayCentered(mainUI, m.setupModalBlock(), m.width, m.height)
	}
	if m.reviewPending {
		mainUI = overlayCentered(mainUI, m.reviewBlock(), m.width, m.height)
	}
	if m.askPending {
		mainUI = overlayCentered(mainUI, m.askUserBlock(), m.width, m.height)
	}
	if m.showLangModal {
		mainUI = overlayCentered(mainUI, m.langModalBlock(), m.width, m.height)
	}
	if m.showWorkingModeModal {
		mainUI = overlayCentered(mainUI, m.workingModeModalBlock(), m.width, m.height)
	}
	if m.showModelModal {
		mainUI = overlayCentered(mainUI, m.modelModalBlock(), m.width, m.height)
	}
	if m.showSandboxModal {
		mainUI = overlayCentered(mainUI, m.sandboxModalBlock(), m.width, m.height)
	}
	if m.showReasoningModal {
		mainUI = overlayCentered(mainUI, m.reasoningModalBlock(), m.width, m.height)
	}
	if m.showMcpAdd {
		mainUI = overlayCentered(mainUI, m.mcpAddModalBlock(), m.width, m.height)
	}
	if m.showWebConfig {
		mainUI = overlayCentered(mainUI, m.webConfigModalBlock(), m.width, m.height)
	}
	if m.showMcpDelete {
		mainUI = overlayCentered(mainUI, m.mcpDeleteModalBlock(), m.width, m.height)
	}
	if m.showSkillAdd {
		mainUI = overlayCentered(mainUI, m.skillAddModalBlock(), m.width, m.height)
	}
	if m.showSkillDelete {
		mainUI = overlayCentered(mainUI, m.skillDeleteModalBlock(), m.width, m.height)
	}
	if m.showSessionList {
		mainUI = overlayCentered(mainUI, m.sessionListModalBlock(), m.width, m.height)
	}
	v := m.wrapView(normalizeFrame(mainUI, m.width, m.height))
	// 真实终端光标定位到 input 内的 cursor 位置。
	// textarea.Cursor() 给的 X/Y 是相对 textarea 自身的局部坐标;input 起始 Y =
	// body 占的行数 + 顶部留白行数;X 要加上左侧 gutter 宽(textarea 现在从 gutter 右边起)。
	// modal 打开时不显示真实光标 —— 避免光标卡在 modal 背后。
	// cursorBlinkOff 由 cursorBlinkTickMsg 600ms 切一次:亮时塞 Cursor,灭时不塞 —
	// 不依赖终端的 DECSCUSR blink 支持,VS Code 终端等也能闪。
	if !m.showSetup && !m.showLangModal && !m.showWorkingModeModal && !m.showSandboxModal && !m.showMcpAdd && !m.showWebConfig && !m.showMcpDelete && !m.showSkillAdd && !m.showSkillDelete && !m.showSessionList && !m.reviewPending && !m.askPending && !m.cursorBlinkOff {
		if c := m.input.Cursor(); c != nil {
			c.Position.X += inputGutterWidth
			c.Position.Y += bodyH + queuedH + inputTopPad
			v.Cursor = c
		}
	}
	return v
}

// normalizeFrame 把整帧锁到精确 width × height:行数不足补空行/过多截尾,
// 每行宽度不足补空格/过宽用 ansi.Cut 切到精确 width。
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

// codegraphColor 给代码图谱状态上色:加载=高亮、就绪=绿、更新/降级=黄、未构建/已禁用=暗。
func codegraphColor(s string) color.Color {
	switch s {
	case "loading":
		return highlightColor
	case "ready":
		return lipgloss.Color("10")
	case "stale", "degraded":
		return lipgloss.Color("11")
	}
	return subtleColor
}

// queuedMaxRows 排队区最多显示几条原文,超出折叠成 "… +N"。
const queuedMaxRows = 5

// queuedDisplayLines 把排队消息(流式中按 Enter 暂存、本轮结束自动发)渲染成输入框上方的
// "待发送"区——仿 Claude Code:让用户直接看到排队的原文,而不是一个计数。
// 每条折成一行(换行压成空格)按宽度截断;超过 queuedMaxRows 条时末行折叠成 "… +N"。
// 返回的行已含左侧 gutter 缩进,直接拼进 inputBlock 顶部即可。
func (m model) queuedDisplayLines(width int) []string {
	if len(m.queuedInput) == 0 {
		return nil
	}
	dim := lipgloss.NewStyle().Foreground(subtleColor).Render
	gutter := strings.Repeat(" ", inputGutterWidth)
	avail := width - inputGutterWidth - 2 // 2 = "↳ " 标记宽
	if avail < 8 {
		avail = 8
	}
	shown := m.queuedInput
	overflow := 0
	if len(shown) > queuedMaxRows {
		overflow = len(shown) - (queuedMaxRows - 1)
		shown = shown[:queuedMaxRows-1]
	}
	lines := make([]string, 0, len(shown)+1)
	for _, q := range shown {
		oneLine := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ").Replace(q)
		oneLine = ansi.Truncate(oneLine, avail, "…")
		lines = append(lines, gutter+dim("↳ "+oneLine))
	}
	if overflow > 0 {
		lines = append(lines, gutter+dim("… +"+strconv.Itoa(overflow)))
	}
	return lines
}

// statusFooterLine 渲染输入框正上方那一行活动状态——把"在跑还是结束了"放到用户注意力
// 所在处(主列底部),而不是只藏在右侧状态栏。
//   - 运行中:spinner(thinking/tool 时转)/状态图标 + 状态词 + 实时耗时 + 当前工具,右侧贴 "Esc 中断"。
//   - 空闲:一个暗色 "● 就绪",和运行态形成明显对比。
func (m model) statusFooterLine(width int) string {
	dim := lipgloss.NewStyle().Foreground(subtleColor).Render
	if !m.streaming {
		// 上一轮出错:红色 ✗ 出错(chat 里已有具体错误信息)。
		if m.status == "error" {
			return lipgloss.NewStyle().Foreground(statusColor("error")).Render("✗ " + T("footer.error"))
		}
		// 跑过至少一轮:绿色 ✓ 完成 + 用时 + 工具数(取代之前打进 chat 的 done 行)。
		if m.turnElapsed > 0 {
			done := lipgloss.NewStyle().Foreground(statusColor("idle")).Render("✓ " + T("done.done"))
			s := done + dim(" · "+formatElapsed(m.turnElapsed))
			if m.turnToolCalls > 0 {
				s += dim(" · " + strconv.Itoa(m.turnToolCalls) + " " + T("done.tools"))
			}
			return s
		}
		// 还没跑过任何一轮:留空。活动行的价值在"运行↔完成"的对比,启动时常挂个"就绪"只是噪音。
		return ""
	}
	head := statusIcon(m.status)
	if m.thinking {
		head = m.spinner.View() // thinking / 工具执行期间动起来
	}
	statusWord := lipgloss.NewStyle().Foreground(statusColor(m.status)).Bold(true).Render(T("footer." + m.status))
	left := head + " " + statusWord + dim(" · "+formatElapsed(time.Since(m.turnStartedAt)))
	if m.activeTool != "" {
		left += dim(" · " + m.activeTool)
	}
	hint := dim(T("footer.interrupt"))
	gap := width - ansi.StringWidth(ansi.Strip(left)) - ansi.StringWidth(ansi.Strip(hint))
	if gap < 1 {
		return left // 太窄,中断提示让位,左侧由 normalizeFrame 截断
	}
	return left + strings.Repeat(" ", gap) + hint
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

// workingModeModalBlock 渲染 /working-mode 选择弹窗。三项 karpathy/openspec/superpowers,
// workingModeModalIdx 是当前光标。
func (m model) workingModeModalBlock() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(highlightColor).Render(T("workingmode.title"))

	options := []string{
		T("workingmode.opt.karpathy"),
		T("workingmode.opt.openspec"),
		T("workingmode.opt.superpowers"),
	}
	rows := make([]string, 0, len(options))
	for i, opt := range options {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		if i == m.workingModeModalIdx {
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

// sandboxModalBlock 渲染 /sandbox 选择弹窗。三项 native/off/docker(见 sandboxModeOrder),
// sandboxModalIdx 是当前光标。
func (m model) sandboxModalBlock() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(highlightColor).Render(T("sandbox.title"))

	options := []string{
		T("sandbox.opt.native"),
		T("sandbox.opt.off"),
		T("sandbox.opt.docker"),
	}
	rows := make([]string, 0, len(options))
	for i, opt := range options {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		if i == m.sandboxModalIdx {
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

// modelModalBlock 渲染 /model 选择弹窗。三项:auto / flash / pro,modelModalIdx 是当前光标。
func (m model) modelModalBlock() string {
	title := lipgloss.NewStyle().Bold(true).Foreground(highlightColor).Render(T("model.modal.title"))

	options := []string{T("model.opt.auto"), T("model.opt.flash"), T("model.opt.pro")}
	rows := make([]string, 0, len(options))
	for i, opt := range options {
		marker := "  "
		style := lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
		if i == m.modelModalIdx {
			marker = "▸ "
			style = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10")).Background(lipgloss.Color("236"))
		}
		rows = append(rows, style.Render(marker+opt))
	}

	footer := lipgloss.NewStyle().Foreground(subtleColor).Render(T("model.footer"))
	parts := []string{title, ""}
	parts = append(parts, rows...)
	parts = append(parts, "", footer)
	content := lipgloss.JoinVertical(lipgloss.Left, parts...)

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(highlightColor).
		Padding(1, 2).
		Width(52).
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

	// 路径压缩 ~/.../tail
	cwd := abbreviatePath(m.workspace, rightPanelWidth-2)


	// 权限模式(plan/auto/review)单独成段。任务执行状态(idle/streaming)已在输入框上方的
	// statusFooterLine 实时显示,右栏不再重复。
	modeStr := lipgloss.NewStyle().Foreground(highlightColor).Render(string(m.mode))

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

	// inlineRow:标签和值放在同一行(◆ 标题  值),用于权限模式 / 计划 / 步骤这类单值项,不换行。
	inlineRow := func(title, value string) string {
		return lipgloss.NewStyle().Foreground(highlightColor).Render("◆ ") +
			lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(strings.ToUpper(title)) +
			"  " + value
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

	// Workspace section:标题尾接 session 哈希。不走 section() 是为了让哈希用 subtle 暗色。
	workspaceTitle := lipgloss.NewStyle().Foreground(highlightColor).Render("◆ ") +
		lipgloss.NewStyle().Foreground(accentColor).Bold(true).Render(strings.ToUpper(T("panel.workspace")))
	if m.session != nil {
		workspaceTitle += " " + subtle("("+m.session.SessionID()[:8]+")")
	}
	rows = append(rows, workspaceTitle, "  "+subtle(cwd), "")

	// 模型厂商 section:api host(去 scheme / path),host 即可标识厂商。
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
	rows = append(rows, section(T("panel.vendor"), []string{subtle(host)})...)

	// 注:web dashboard 地址不在右栏显示 —— 启动时已在 chat 区给出可点击 / 已复制的提示,
	// 这里再放一份既重复又因面板窄被迫折行,反而没法点。

	// 模型路由:只显示 auto / flash / pro。
	routing := m.modelPin
	if routing == "" {
		routing = "auto"
	}
	rows = append(rows, inlineRow(T("panel.routing"), lipgloss.NewStyle().Foreground(highlightColor).Render(routing)), "")
	// 当前模型:只显示模型名(去掉 org/ 前缀,截断防溢出)。
	curModel := m.activeModelID
	if i := strings.LastIndexByte(curModel, '/'); i >= 0 {
		curModel = curModel[i+1:]
	}
	if curModel == "" {
		curModel = "—"
	}
	rows = append(rows, section(T("panel.curmodel"), []string{
		truncate(curModel, rightPanelWidth-4),
	})...)
	// 用量紧跟模型。首轮 API 调用结束才能拿到 lastUsage,没拿到前用 "—" 占位,保持布局一致。
	// 上下文占用:本轮发出的 prompt tokens / 当前模型窗口。窗口从当前模型配置动态取(非硬编码);
	// 未配置(=0)则只显示已用 token,不编造分母/百分比。
	ctxWin := m.models.Flash.ContextWindow
	if m.activeModelRole == "pro" {
		ctxWin = m.models.Pro.ContextWindow
	}
	usedStr, outputStr, cacheStr := "—", "—", "—"
	if u := m.lastUsage; u != nil {
		if ctxWin > 0 && u.PromptTokens > 0 {
			usedStr = formatTokenCount(u.PromptTokens) + " / " + formatTokenCount(ctxWin) +
				" · " + strconv.Itoa(u.PromptTokens*100/ctxWin) + "%"
		} else {
			usedStr = formatTokenCount(u.PromptTokens) + " tok"
		}
		outputStr = formatTokenCount(u.CompletionTokens) + " tok"
		// 命中率:分母用 PromptTokens(DeepSeek 保证 = hit + miss;hit/miss 是其私有字段)。
		if u.PromptTokens > 0 {
			cacheStr = strconv.Itoa(u.PromptCacheHitTokens*100/u.PromptTokens) + "% hit"
		} else {
			cacheStr = "0% hit"
		}
	}
	rows = append(rows, section(T("panel.context"), []string{
		label(T("panel.label.used")) + " " + usedStr,
		label(T("panel.label.cache")) + " " + cacheStr,
		label(T("panel.label.output")) + " " + outputStr,
	})...)
	rows = append(rows, inlineRow(T("panel.permmode"), modeStr), "")
	// 代码图谱:单行只显示状态。
	cgState := tools.CodeGraphStatus()
	rows = append(rows, inlineRow(T("panel.codegraph"),
		lipgloss.NewStyle().Foreground(codegraphColor(cgState)).Render(T("codegraph."+cgState))), "")
	// 沙箱:显示当前模式 + native 的保护级别(OS 隔离 / 软策略),让用户清楚当前的边界强度。
	sbDesc := string(tools.CurrentSandboxMode())
	switch tools.CurrentSandboxMode() {
	case tools.SandboxOff:
		sbDesc += " (无防护)"
	case tools.SandboxNative:
		if tools.NativeIsolationActive() {
			sbDesc += " (OS隔离)"
		} else {
			sbDesc += " (软策略)"
		}
	}
	rows = append(rows, section("🛡 "+T("panel.sandbox"), []string{
		label(T("panel.label.sbmode")) + " " + sbDesc,
	})...)
	// 工作模式:kp / openspec / sp。
	rows = append(rows, section("🧭 "+T("panel.workmode"), []string{
		label(T("panel.label.wmode")) + " " + string(m.workingMode),
	})...)
	// 规划进度:始终显示(无规划时 0/0)。完整 plan 树在 chat 区展示,右栏只放摘要。
	// 待办(Todo)= Todo 工具(主 agent 顺序清单);计划(Plan)= CreatePlan(并发子 agent DAG)。
	// 二者共用 m.plan(同一时刻只一种活跃),按 planKind 分到两段显示,另一段为 0/0。
	var todoState, cpState *planState
	switch m.planKind {
	case "todo":
		todoState = m.plan
	case "createplan":
		cpState = m.plan
	}
	rows = append(rows,
		inlineRow(T("panel.todo"), renderPlanSummary(todoState, 0)[0]),
		"",
		inlineRow(T("panel.plan"), renderPlanSummary(cpState, 0)[0]),
		"")
	rows = append(rows, inlineRow(T("panel.help"), lipgloss.NewStyle().Foreground(highlightColor).Render("/help")))

	// 删掉最后那行多余空行
	if len(rows) > 0 && rows[len(rows)-1] == "" {
		rows = rows[:len(rows)-1]
	}

	return strings.Join(rows, "\n")
}
