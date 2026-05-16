package tui

import (
	"encoding/json"
	"strings"
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
	"UpdateTaskStatus": "✅",
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
	var sb strings.Builder
	sb.WriteString(header)
	// 空行 + ~~~diff:markdown 块边界,glamour 会按 diff 语法上色(- 红 / + 绿)
	sb.WriteString("\n\n~~~diff\n")
	writeDiffBlock(&sb, oldS, "- ")
	writeDiffBlock(&sb, newS, "+ ")
	sb.WriteString("~~~")
	return sb.String()
}

const (
	updatePreviewMaxLines = 5
	updatePreviewMaxWidth = 100
)

// writeDiffBlock 把单段文本(old_string 或 new_string)按行拆分,每行加 prefix 后写入 sb。
// 超过 updatePreviewMaxLines 行只保留头部 N 行,末尾追加 "... (M more lines)";
// 单行长度超过 updatePreviewMaxWidth 字节时尾部截断为 "..."。
// 空串直接返回(调用方已在 oldS && newS 全空时短路)。
func writeDiffBlock(sb *strings.Builder, s, prefix string) {
	if s == "" {
		return
	}
	lines := strings.Split(s, "\n")
	total := len(lines)
	if total > updatePreviewMaxLines {
		lines = lines[:updatePreviewMaxLines]
	}
	for _, l := range lines {
		if len(l) > updatePreviewMaxWidth {
			l = l[:updatePreviewMaxWidth-3] + "..."
		}
		sb.WriteString(prefix)
		sb.WriteString(l)
		sb.WriteByte('\n')
	}
	if total > updatePreviewMaxLines {
		sb.WriteString("... (")
		sb.WriteString(itoa(total - updatePreviewMaxLines))
		sb.WriteString(" more lines)\n")
	}
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
	case "UpdateTaskStatus":
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
