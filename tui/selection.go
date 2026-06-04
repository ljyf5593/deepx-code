package tui

import (
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// cellPos 表示一个 chat 内部坐标:
//   - col  = 显示列 (0..viewport.Width-1)
//   - line = 经 ansi.Wrap 后的行号(在 chatContent 的总行集合内)
//
// 用"已 wrap 行号"而非"内容字节偏移"的好处:
//   - 用户在屏幕看到的就是 wrapped lines,鼠标拖拽方向跟它一一对应
//   - 内容只增不减(append-only),老的 line 号永远稳定
//   - 终端尺寸不变时,wrap 结果稳定;变了再用 WindowSizeMsg 清掉选区即可
type cellPos struct {
	col  int
	line int
}

// orderSel 把 anchor / end 按"流向"排好:先 line,后 col。
// 流式选择 = 文本编辑器式连续选区:从 start 一直到 end,跨多行时中间行整行入选。
// 不同于矩形/块选择 (那种是 col ∈ [min,max] × line ∈ [min,max])。
func orderSel(a, b cellPos) (start, end cellPos) {
	if a.line < b.line || (a.line == b.line && a.col <= b.col) {
		return a, b
	}
	return b, a
}

const (
	ansiReverseOn  = "\x1b[7m"
	ansiReverseOff = "\x1b[27m"
	ansiResetAll   = "\x1b[0m" // 进反色段前先全 reset,避免 pre 段未闭合的颜色渗入
)

// selRange 计算第 i 行的高亮 / 抠字列区间 [left, right):
//   - 单行选择:[start.col, end.col+1)
//   - 首行:[start.col, width)
//   - 末行:[0, end.col+1)
//   - 中间行:[0, width)
func selRange(i int, start, end cellPos, width int) (left, right int) {
	switch {
	case i == start.line && i == end.line:
		left, right = start.col, end.col+1
	case i == start.line:
		left, right = start.col, width
	case i == end.line:
		left, right = 0, end.col+1
	default:
		left, right = 0, width
	}
	if right > width {
		right = width
	}
	if left < 0 {
		left = 0
	}
	return
}

// applySelectionHighlight 在已渲染的 chat 内容上画流式选区反色。
// width 必须等于 viewport.Width,否则 col 坐标对不上。
func applySelectionHighlight(wrapped string, a, b cellPos, width int) string {
	if width <= 0 {
		return wrapped
	}
	start, end := orderSel(a, b)

	lines := strings.Split(wrapped, "\n")
	for i := start.line; i <= end.line && i < len(lines); i++ {
		if i < 0 {
			continue
		}
		left, right := selRange(i, start, end, width)
		if left >= right {
			continue
		}
		line := lines[i]
		// pad 短行,让矩形高亮在空白处也可见(整行连续段尤其需要)
		if cur := ansi.StringWidth(line); cur < width {
			line += strings.Repeat(" ", width-cur)
		}
		pre := ansi.Cut(line, 0, left)
		// 选中段必须 ansi.Strip 成纯文本再套反色:mid 里(markdown / URL 渲染)常带
		// `\x1b[0m` 全 reset,它会把前面的 `\x1b[7m` 反色一起取消 → 选中文字看起来没高亮。
		// 去掉内部 SGR 后整段都是反色实心块(编辑器式选区),前面补一个 reset 防 pre 的颜色渗进来。
		mid := ansi.Strip(ansi.Cut(line, left, right))
		post := ansi.Cut(line, right, width)
		lines[i] = pre + ansiResetAll + ansiReverseOn + mid + ansiReverseOff + post
	}
	return strings.Join(lines, "\n")
}

// extractSelectionText 按流式选区抠纯文本(去 ANSI、去左侧色条前缀、trim 行尾空格)。
// 选区 left==0 时,说明这一行从首列入选,等价于"选了整行",剥掉左侧的 "┃ " / "  │ " 色条前缀
// 让剪贴板里得到纯净对话文本而不是带引用前缀的乱码。
func extractSelectionText(wrapped string, a, b cellPos, width int) string {
	if width <= 0 {
		return ""
	}
	start, end := orderSel(a, b)
	if start == end {
		return ""
	}

	lines := strings.Split(wrapped, "\n")
	var out []string
	for i := start.line; i <= end.line && i < len(lines); i++ {
		if i < 0 {
			out = append(out, "")
			continue
		}
		left, right := selRange(i, start, end, width)
		if left >= right {
			out = append(out, "")
			continue
		}
		seg := ansi.Cut(lines[i], left, right)
		seg = ansi.Strip(seg)
		if left == 0 {
			seg = stripQuoteBarPrefix(seg)
		}
		seg = strings.TrimRight(seg, " ")
		out = append(out, seg)
	}
	return strings.Join(out, "\n")
}

// copySelection 把当前选区文本写进剪贴板,并弹一个"已复制"临时提示。选区为空返回 nil。
//
// 本地优先用原生剪贴板(pbcopy/xclip/clip.exe),**不再叠加 OSC52**:
// OSC52 的 payload 是 base64 字节,部分终端(如 VS Code 的 xterm.js)把它按 Latin-1 解码,
// 中文会变乱码;若 pbcopy 之后再发 OSC52,反把干净结果覆盖成乱码。
// OSC52 只在两种情况下用:① 远程会话(SSH,本地剪贴板工具写的是远端,没用,只能靠 OSC52 转发);
// ② 原生写入失败(没装 xclip / 无 DISPLAY 等)兜底。
func (m *model) copySelection() tea.Cmd {
	text := m.collectSelectionText()
	if text == "" {
		return nil
	}
	m.copyHint = T("copy.done")
	return tea.Batch(clipboardWriteCmd(text), clearCopyHintCmd())
}

// clipboardWriteCmd 把 text 写进剪贴板,并返回 Update 里待执行的 tea.Cmd:
//   - 本地会话:用原生剪贴板(pbcopy/xclip/clip.exe),成功则返回 nil(不发 OSC52);
//   - 远程(SSH)或原生写入失败:退回 OSC52(tea.SetClipboard)。
//
// 之所以本地不发 OSC52:它的 payload 是 base64 字节,部分终端(如 VS Code 的 xterm.js)
// 按 Latin-1 解码,中文会变乱码;叠在 pbcopy 之后还会把干净结果覆盖掉。
func clipboardWriteCmd(text string) tea.Cmd {
	if isRemoteSession() {
		return tea.SetClipboard(text)
	}
	if err := writeClipboardText(text); err != nil {
		return tea.SetClipboard(text)
	}
	return nil
}

// isRemoteSession 判断是否在 SSH 远程会话里(此时本地剪贴板工具写的是远端剪贴板,需靠 OSC52 转发)。
func isRemoteSession() bool {
	return os.Getenv("SSH_CONNECTION") != "" || os.Getenv("SSH_TTY") != ""
}

// copyHintClearMsg 到达时清掉"已复制"提示。
type copyHintClearMsg struct{}

// clearCopyHintCmd 1.5s 后发 copyHintClearMsg。
func clearCopyHintCmd() tea.Cmd {
	return tea.Tick(1500*time.Millisecond, func(time.Time) tea.Msg { return copyHintClearMsg{} })
}

// stripQuoteBarPrefix 移除 applyQuoteBar 加的左侧色条前缀。
// 一级 (user/assistant/system): "┃ ";二级 (tools):"  │ "。
// 顺序很重要 — 先匹配长前缀("  │ "),再匹配 "┃ ",避免缩进二级被一级吃掉。
func stripQuoteBarPrefix(s string) string {
	for _, prefix := range []string{"  │ ", "┃ "} {
		if strings.HasPrefix(s, prefix) {
			return s[len(prefix):]
		}
	}
	return s
}
