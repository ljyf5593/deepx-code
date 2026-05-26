package tui

import (
	"encoding/json"
	"os"
	"strings"

	"charm.land/lipgloss/v2"
)

// toolIcons 给每个工具一个 emoji 图标,统一 2 cell 显示宽。
//
// **规范**:
//   - 只用 *默认 emoji presentation* 字符(U+1F300+ 段大多数都是),不用 VS16
//   - VS16 (U+FE0F) 强制 emoji 形态在 macOS 系字体里没问题,但 Linux 终端 / tmux / SSH
//     远程经常忽略 VS16,把 ✏ ⚙ 👁 ⬆ 🗂 等 text-presentation 默认字符渲染成 1 cell 文字
//     字形,跟 lineDisplayWidth 强制 +1 的 2 cell 估算对不齐 → 行实宽 -1 → 滚动条左移抖动
//   - 不加 trailing space — 拼接由 formatToolCallLine 在 icon 跟 name 之间显式加空格
//
// 表里没有的工具走 defaultToolIcon 兜底。
var toolIcons = map[string]string{
	"Read":             "📄",
	"Write":            "📝",
	"Update":           "📝",
	"List":             "📂",
	"Tree":             "🌲",
	"Glob":             "🔎",
	"Grep":             "🔍",
	"Bash":             "🐚",
	"OCR":              "👀",
	"Search":           "🌐",
	"Fetch":            "📡",
	"Memory":           "🧠",
	"LoadSkill":        "📜",
	"CreatePlan":       "📋",
	"Todo":             "📌",
	"UpdatePlanStatus": "✅",
	"SwitchModel":      "🚀",
}

const defaultToolIcon = "🔧"

// formatToolCallLine 把一次工具调用渲染成紧凑展示。
// 常规工具:单行 "<icon> <ToolName> (<主要参数>)"。
// Update 工具:首行带路径,后接 ~~~diff 代码块,old_string 标 `-`、new_string 标 `+`,
// 形成类似 git diff 的 patch 预览。
// 单行参数截断阈值 80 字符,避免长 prompt/正则把整行撑爆。
func formatToolCallLine(name, argsJSON string) string {
	icon, ok := toolIcons[name]
	if !ok {
		icon = defaultToolIcon
	}
	if name == "Update" {
		return formatUpdatePreview(icon, argsJSON)
	}
	arg := extractMainArg(name, argsJSON)
	// icon 是 emoji(2 cell),显式加 1 空格分隔 ToolName,避免 emoji 紧贴字母在
	// 某些终端字距异常。后跟 "(arg)" 时同样空格分隔。
	if arg == "" {
		return icon + " " + name
	}
	if len(arg) > 80 {
		arg = arg[:77] + "..."
	}
	return icon + " " + name + " (" + arg + ")"
}

// formatUpdatePreview 把 Update 工具的 path / old_string / new_string 渲染成 patch 预览。
//
// 输出形如:
//
//	📝 Update (path/to/file)
//
//	~~~diff
//	- old line 1
//	- old line 2
//	+ new line 1
//	+ new line 2
//	~~~
//
// 用 ~~~ 而不是 ``` 包裹,避免 old/new 内容里含 ``` 撑爆 fence。
// 长 / 多行的 old/new 各自截断到 updatePreviewMaxLines 行,行宽超过 updatePreviewMaxWidth
// 时尾部截断 + "...";剩余行数追加 "... (N more lines)" 提示。
// args 解析失败时退化为 "📝 Update" 单行,不抛错。
func formatUpdatePreview(icon, argsJSON string) string {
	header := icon + " Update"
	if argsJSON == "" || argsJSON == "null" {
		return header
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return header
	}
	if path := strVal(args["path"]); path != "" {
		header += " (" + path + ")"
	}
	oldS, _ := args["old_string"].(string)
	newS, _ := args["new_string"].(string)
	if oldS == "" && newS == "" {
		return header
	}
	// 字符串模式没有显式行号,grep 文件定位 old_string 的起始行,这样 -/+ 行前面
	// 也能渲染行号列。ToolCallStartMsg 在 Executor 之前 fire,文件还是 pre-edit 状态,
	// 此时 old_string 仍能在文件里精确匹配到。读不到 / 没匹配 → startLine=0,退化成无行号。
	startLine := 0
	if oldS != "" {
		startLine = locateLineInFile(strVal(args["path"]), oldS)
	}

	var sb strings.Builder
	sb.WriteString(header)
	sb.WriteString("\n\n~~~diff\n")
	writeDiffBlock(&sb, oldS, "-", startLine)
	writeDiffBlock(&sb, newS, "+", startLine)
	sb.WriteString("~~~")
	return sb.String()
}

// locateLineInFile 读 path,在文件内容里精确定位 needle,返回它的首行行号(1-indexed)。
// 读不到 / 没匹配返回 0。多次匹配时取第一个 —— 跟 EditFile 字符串模式"必须唯一"约束一致。
// 用于字符串模式的 patch 预览:有了行号就能在 -/+ 前面渲染行号列。
func locateLineInFile(path, needle string) int {
	if path == "" || needle == "" {
		return 0
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	idx := strings.Index(string(data), needle)
	if idx < 0 {
		return 0
	}
	return strings.Count(string(data[:idx]), "\n") + 1
}

const (
	updatePreviewMaxLines = 5
	updatePreviewMaxWidth = 100
)

// colorizeDiffBlock 扫描 tools 段文本,找出 ~~~diff ... ~~~ 块,把里面的 `-` 行染红、
// `+` 行染绿、"... (N more lines)" 行染暗,fence 行(```/~~~)整行删掉避免污染视觉。
// 块外的内容(工具调用首行、其他工具的 raw 一行式)原样保留。
// 没遇到 fence 时整段原样返回,常规工具调用不受影响。
func colorizeDiffBlock(s string) string {
	if !strings.Contains(s, "~~~") && !strings.Contains(s, "```") {
		return s
	}
	addStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("2")).Render  // green
	delStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("1")).Render  // red
	hintStyle := lipgloss.NewStyle().Foreground(dimColor).Render
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	inDiff := false
	for _, l := range lines {
		trim := strings.TrimSpace(l)
		// fence open / close —— diff 块用 ~~~diff 开 ~~~ 闭,常规 ``` 也兼容。
		if strings.HasPrefix(trim, "~~~") || strings.HasPrefix(trim, "```") {
			if inDiff {
				inDiff = false
			} else if strings.Contains(trim, "diff") {
				inDiff = true
			}
			continue // fence 不显示
		}
		if !inDiff {
			out = append(out, l)
			continue
		}
		// 在 diff 块内 —— 行可能是:
		//   "<sign> <content>"           (无行号 / 字符串模式拿不到)
		//   "  42 <sign> <content>"      (有行号,左侧 padding 对齐)
		//   "... (N more lines)"         (截断提示)
		// 检测 sign 时跳过前导空白和数字。
		sign := diffSignByte(l)
		switch sign {
		case '+':
			out = append(out, addStyle(l))
		case '-':
			out = append(out, delStyle(l))
		case 0:
			if strings.HasPrefix(strings.TrimLeft(l, " "), "... (") {
				out = append(out, hintStyle(l))
			} else {
				out = append(out, l)
			}
		}
	}
	return strings.Join(out, "\n")
}

// writeDiffBlock 把单段文本(old_string 或 new_string)按行拆分,每行格式化成
// "<lineNo> <sign> <content>",sign 是 "-" / "+",lineNo 从 startLine 起 1 行 1。
// startLine == 0 表示没有行号信息,这时省略行号列、退化成 "<sign> <content>"。
// 超过 updatePreviewMaxLines 行只保留头部 N 行,末尾追加 "... (M more lines)";
// 单行长度超过 updatePreviewMaxWidth 字节时尾部截断为 "..."。
// 空串直接返回。
func writeDiffBlock(sb *strings.Builder, s, sign string, startLine int) {
	if s == "" {
		return
	}
	lines := strings.Split(s, "\n")
	total := len(lines)
	if total > updatePreviewMaxLines {
		lines = lines[:updatePreviewMaxLines]
	}
	// 行号列宽按"最大可能行号"算,右对齐,这样多行 diff 视觉对齐。
	lineNoWidth := 0
	if startLine > 0 {
		lineNoWidth = numDigits(startLine + total - 1)
	}
	for i, l := range lines {
		if len(l) > updatePreviewMaxWidth {
			l = l[:updatePreviewMaxWidth-3] + "..."
		}
		if startLine > 0 {
			ln := itoa(startLine + i)
			sb.WriteString(strings.Repeat(" ", lineNoWidth-len(ln)))
			sb.WriteString(ln)
			sb.WriteByte(' ')
		}
		sb.WriteString(sign)
		sb.WriteByte(' ')
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	if total > updatePreviewMaxLines {
		// "... (N more lines)" 行不带行号 / sign,colorizeDiffBlock 单独识别此前缀染暗。
		sb.WriteString("... (")
		sb.WriteString(itoa(total - updatePreviewMaxLines))
		sb.WriteString(" more lines)\n")
	}
}

// diffSignByte 返回行内 diff 符号字节('+' / '-'),跳过前导空白和数字行号。
// 行不属于 diff 行(eg. "... more lines"、注释)返回 0。
//
// 识别:
//
//	"+ foo"        → '+'
//	"  42 + foo"   → '+'
//	"-bar"         → '-'(防御性,正常会有空格,但也接受)
//	"  abc"        → 0
//	""             → 0
func diffSignByte(l string) byte {
	i := 0
	// 跳过前导空白
	for i < len(l) && l[i] == ' ' {
		i++
	}
	// 跳过可选的数字行号
	if i < len(l) && l[i] >= '0' && l[i] <= '9' {
		for i < len(l) && l[i] >= '0' && l[i] <= '9' {
			i++
		}
		// 行号后必须跟空格,否则不算行号(防止把 "5xxx" 之类当成 5 + "xxx")
		if i >= len(l) || l[i] != ' ' {
			return 0
		}
		i++
	}
	if i < len(l) && (l[i] == '+' || l[i] == '-') {
		return l[i]
	}
	return 0
}

// numDigits 返回正整数 n 的十进制位数。0 / 负数视作 1 位。
func numDigits(n int) int {
	if n < 10 {
		return 1
	}
	d := 0
	for n > 0 {
		d++
		n /= 10
	}
	return d
}

// extractMainArg 从 LLM 给的 args JSON 里抽取一个最具代表性的字段值,显示到行尾括号里。
// 解析失败 / 字段缺失返回空串。
func extractMainArg(name, argsJSON string) string {
	if argsJSON == "" || argsJSON == "null" {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return ""
	}

	// 按工具类型选优先字段。多数工具的"主参数"就是 path,其他工具单独处理。
	switch name {
	case "Read", "Write", "Update", "List", "Tree", "OCR":
		return strVal(args["path"])
	case "Glob":
		p := strVal(args["pattern"])
		if path := strVal(args["path"]); path != "" && path != "." {
			return p + " in " + path
		}
		return p
	case "Grep":
		pat := strVal(args["pattern"])
		if path := strVal(args["path"]); path != "" {
			return pat + " in " + path
		}
		return pat
	case "Search":
		return strVal(args["query"])
	case "Fetch":
		return strVal(args["url"])
	case "LoadSkill":
		return strVal(args["name"])
	case "Memory":
		// keywords 是 []string,join 成空格分隔显示
		if kws, ok := args["keywords"].([]any); ok {
			parts := make([]string, 0, len(kws))
			for _, k := range kws {
				if s, ok := k.(string); ok {
					parts = append(parts, s)
				}
			}
			return strings.Join(parts, " ")
		}
		return ""
	case "Bash":
		return strVal(args["command"])
	case "SwitchModel":
		return strVal(args["reason"])
	case "UpdatePlanStatus":
		id, st := strVal(args["id"]), strVal(args["status"])
		if id != "" && st != "" {
			return id + " → " + st
		}
		return id
	case "CreatePlan":
		// plans 是数组,显示节点数
		if p, ok := args["plans"].([]any); ok {
			return countLabel(len(p), "node")
		}
		return ""
	}
	// 兜底:任何带 path 的工具都用 path
	return strVal(args["path"])
}

func strVal(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func countLabel(n int, unit string) string {
	if n == 1 {
		return "1 " + unit
	}
	return itoa(n) + " " + unit + "s"
}

func itoa(n int) string {
	// 避免引入 strconv 只为一个整数转字符串,实际本文件几乎不调用
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
