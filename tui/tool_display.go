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

// formatToolCallLine 把一次工具调用渲染成单行紧凑显示。
// 格式: "<icon> <ToolName> (<主要参数>)"
// 主要参数按工具类型选取:文件路径优先, 否则 pattern/command 摘要。
// 截断阈值 80 字符,避免长 prompt/正则把整行撑爆。
func formatToolCallLine(name, argsJSON string) string {
	icon, ok := toolIcons[name]
	if !ok {
		icon = defaultToolIcon
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
