package tools

import (
	"fmt"
	"strings"
	"sync/atomic"

	"deepx/codegraph"
)

// cgIndex 是当前 workspace 的代码图谱,由 SetCodeGraphRoot 在 tui 启动时注入。
// 跟 Memory/Skill 一样用包级变量做最小侵入注入(运行时单 workspace,无竞态)。
var cgIndex *codegraph.Index

// cgCalls 统计 CodeGraph 工具被调用的次数,供状态栏展示(渲染线程无锁读)。
var cgCalls atomic.Int64

// CodeGraphCalls 返回 CodeGraph 工具累计调用次数。
func CodeGraphCalls() int { return int(cgCalls.Load()) }

// SetCodeGraphRoot 绑定 workspace 根目录并新建索引,随即尝试后台预热。
// 预热是否真正发生由 root 的安全等级决定(见 codegraph.classifyRoot):
// 危险根(home / 文件系统根 / 系统目录)直接禁用,绝不遍历;非项目散目录不自动预热,
// 留到模型显式查询时惰性构建。只有识别为项目根才像以前那样开机就建。
func SetCodeGraphRoot(root string) {
	cgIndex = codegraph.NewIndex(root)
	cgIndex.Prewarm()
}

// CodeGraphInvalidate 让图谱缓存失效,下次查询重建。Write/Update 改了文件后调用。
func CodeGraphInvalidate() {
	if cgIndex != nil {
		cgIndex.Invalidate()
	}
}

// CodeGraphStatus 返回图谱状态标识串(idle/loading/ready/stale),供状态栏渲染。
func CodeGraphStatus() string {
	if cgIndex == nil {
		return "idle"
	}
	return cgIndex.Status().Token()
}

const cgDefaultMax = 60

// CodeGraph 是工具入口:基于符号图谱做代码导航。op 见 cgOps / 工具描述。
//
//	symbols def refs outline imports | callers callees | implementers subtypes supertypes | impact | reindex
func CodeGraph(args map[string]any) ToolResult {
	cgCalls.Add(1)
	if cgIndex == nil {
		return ToolResult{Output: "代码图谱未启用(workspace 未初始化)", Success: false}
	}
	if cgIndex.Disabled() {
		return ToolResult{Output: fmt.Sprintf("代码图谱已禁用:%s。请在具体的项目目录(含 .git/go.mod/package.json 等)中启动,而非主目录或系统根目录。", cgIndex.Reason()), Success: false}
	}
	op, _ := args["op"].(string)
	op = strings.TrimSpace(strings.ToLower(op))
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	path, _ := args["path"].(string)
	path = strings.TrimSpace(path)
	kind := codegraph.Kind(strings.TrimSpace(toStr(args["kind"])))
	max := toInt(args["max"], cgDefaultMax)
	if max <= 0 {
		max = cgDefaultMax
	}

	switch op {
	case "reindex":
		n, err := cgIndex.Reindex()
		if err != nil {
			return ToolResult{Output: fmt.Sprintf("重建索引失败: %v", err), Success: false}
		}
		return ToolResult{Output: fmt.Sprintf("已重建索引,共 %d 个符号。", n), Success: true}
	}

	g, err := cgIndex.Graph()
	if err != nil {
		return ToolResult{Output: fmt.Sprintf("构建索引失败: %v", err), Success: false}
	}

	switch op {
	case "symbols":
		hits, total := g.FindSymbols(name, kind, max)
		return ToolResult{Output: formatSymbols(fmt.Sprintf("符号匹配 %q", name), hits, total), Success: true}

	case "def":
		if name == "" {
			return ToolResult{Output: "def 需要 name 参数", Success: false}
		}
		defs := g.Def(name)
		return ToolResult{Output: formatSymbols(fmt.Sprintf("%q 的定义", name), defs, len(defs)), Success: true}

	case "refs":
		if name == "" {
			return ToolResult{Output: "refs 需要 name 参数", Success: false}
		}
		refs, total := g.Refs(name, max)
		return ToolResult{Output: formatRefs(name, refs, total), Success: true}

	case "outline":
		if path == "" {
			return ToolResult{Output: "outline 需要 path 参数(相对 workspace 的文件路径)", Success: false}
		}
		syms := g.Outline(path)
		return ToolResult{Output: formatSymbols("文件结构 "+path, syms, len(syms)), Success: true}

	case "callers":
		if name == "" {
			return ToolResult{Output: "callers 需要 name 参数", Success: false}
		}
		edges, total := g.Callers(name, max)
		return ToolResult{Output: formatCallers(name, edges, total), Success: true}

	case "callees":
		if name == "" {
			return ToolResult{Output: "callees 需要 name 参数", Success: false}
		}
		edges, total := g.Callees(name, max)
		return ToolResult{Output: formatCallees(name, edges, total), Success: true}

	case "imports":
		if path == "" {
			return ToolResult{Output: "imports 需要 path 参数(相对 workspace 的文件路径)", Success: false}
		}
		return ToolResult{Output: formatImports(path, g.Imports(path)), Success: true}

	case "implementers":
		if name == "" {
			return ToolResult{Output: "implementers 需要 name 参数(接口名)", Success: false}
		}
		impl := g.Implementers(name)
		if len(impl) == 0 {
			// 兜底:C#/Kotlin/Swift/Scala 把实现记为 extends,implementers 查不到 → 自动回退看 subtypes。
			if subs := g.Subtypes(name); len(subs) > 0 {
				return ToolResult{Output: formatHierarchy(fmt.Sprintf("派生自 %q 的类型(注:无 implements 数据,以下来自 extends——部分语言继承/实现不分)", name), "←", subs, true), Success: true}
			}
		}
		return ToolResult{Output: formatHierarchy(fmt.Sprintf("实现接口 %q 的类型", name), "←", impl, true), Success: true}

	case "subtypes":
		if name == "" {
			return ToolResult{Output: "subtypes 需要 name 参数(类型名)", Success: false}
		}
		return ToolResult{Output: formatHierarchy(fmt.Sprintf("继承/嵌入 %q 的类型", name), "←", g.Subtypes(name), true), Success: true}

	case "supertypes":
		if name == "" {
			return ToolResult{Output: "supertypes 需要 name 参数(类型名)", Success: false}
		}
		return ToolResult{Output: formatHierarchy(fmt.Sprintf("%q 派生自(继承/实现)", name), "→", g.Supertypes(name), false), Success: true}

	case "impact":
		if name == "" {
			return ToolResult{Output: "impact 需要 name 参数(要改动的符号)", Success: false}
		}
		depth := toInt(args["depth"], 3)
		nodes, total := g.Impact(name, depth, max)
		return ToolResult{Output: formatImpact(name, depth, nodes, total), Success: true}

	default:
		return ToolResult{Output: "未知 op,可选: symbols | def | refs | callers | callees | implementers | subtypes | supertypes | impact | imports | outline | reindex", Success: false}
	}
}

// cgEmptyHint 在查询无结果时附带:提醒可能符号不存在或文件类型未被解析,引导用 Grep 复核。
// 防止模型把"图谱没收录"误判成"绝对不存在"。
func cgEmptyHint() string {
	return "\n\n提示:无结果可能是符号确实不存在、或该文件类型不在图谱覆盖内(仅 Go 及主流编程语言;shell/css/json/md 等走 Grep)。可改用 Grep 复核。"
}

func formatSymbols(title string, syms []codegraph.Symbol, total int) string {
	if len(syms) == 0 {
		return title + ":无结果" + cgEmptyHint()
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s,共 %d 条:\n\n", title, total)
	for _, s := range syms {
		fmt.Fprintf(&sb, "%s:%d  [%s] %s\n", s.File, s.Line, s.Kind, s.QualifiedName())
		if s.Signature != "" {
			fmt.Fprintf(&sb, "    %s\n", s.Signature)
		}
	}
	if total > len(syms) {
		fmt.Fprintf(&sb, "\n…还有 %d 条未显示,缩小 name / 指定 kind / 调大 max。\n", total-len(syms))
	}
	return sb.String()
}

// cgCallEmptyHint:调用查询无结果时的提示。调用边按已解析代码得出(Go 精确,其它语法级),
// "未发现" ≠ "一定没有"——接口/动态分发、反射、函数值调用、非 Go 部分语言可能漏。
func cgCallEmptyHint() string {
	return "\n\n注:未发现 ≠ 一定没有。调用边对接口/动态分发、反射、函数值调用抓不到;非 Go 部分语言调用边也不全。可用 refs / Grep 复核。"
}

// cgRefsEmptyHint:refs 无结果提示。refs 覆盖度按语言不同。
func cgRefsEmptyHint() string {
	return "\n\n注:refs 覆盖——Go 是全部标识符出现点;其它语言只覆盖 tags 捕获的引用(主要是调用处),不穷尽。未发现可用 Grep 复核。"
}

func formatRefs(name string, refs []codegraph.Ref, total int) string {
	if len(refs) == 0 {
		return fmt.Sprintf("未发现 %q 的引用", name) + cgRefsEmptyHint()
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%q 的引用,共 %d 处:\n\n", name, total)
	for _, r := range refs {
		fmt.Fprintf(&sb, "%s:%d\n", r.File, r.Line)
	}
	if total > len(refs) {
		fmt.Fprintf(&sb, "\n…还有 %d 处未显示,调大 max。\n", total-len(refs))
	}
	return sb.String()
}

func formatCallers(name string, edges []codegraph.Edge, total int) string {
	if len(edges) == 0 {
		return fmt.Sprintf("未发现调用 %q 的地方", name) + cgCallEmptyHint()
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "调用 %q 的地方,共 %d 处:\n\n", name, total)
	for _, e := range edges {
		fmt.Fprintf(&sb, "%s:%d  ← %s\n", e.File, e.Line, e.From)
	}
	if total > len(edges) {
		fmt.Fprintf(&sb, "\n…还有 %d 处未显示,调大 max。\n", total-len(edges))
	}
	return sb.String()
}

func formatCallees(name string, edges []codegraph.Edge, total int) string {
	if len(edges) == 0 {
		return fmt.Sprintf("未发现 %q 调用的函数", name) + cgCallEmptyHint()
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%q 调用的函数,共 %d 处:\n\n", name, total)
	for _, e := range edges {
		fmt.Fprintf(&sb, "%s:%d  → %s\n", e.File, e.Line, e.To)
	}
	if total > len(edges) {
		fmt.Fprintf(&sb, "\n…还有 %d 处未显示,调大 max。\n", total-len(edges))
	}
	return sb.String()
}

// formatHierarchy 渲染继承/实现类查询。showFrom=true 时主体是 From(谁实现/继承,带定位),
// 否则主体是 To(派生自什么)。arrow 仅作视觉指示。
// cgHeritageEmptyHint:继承/实现查询无结果时的提示。继承边只对部分语言抽取,空结果
// 不代表没有关系——必须讲清楚,否则模型会对未覆盖语言误判"无子类/无实现"。
func cgHeritageEmptyHint() string {
	return "\n\n提示:继承/实现边覆盖这些语言:" + strings.Join(codegraph.HeritageLangs(), " ") +
		"。不在内的语言无此数据,空结果≠无关系,可用 Grep 复核。" +
		"另注:C#/Kotlin/Swift/Scala 语法不分继承与实现,统一记为 extends —— 查这些语言某接口的实现者请改用 subtypes(而非 implementers)。"
}

func formatHierarchy(title, arrow string, edges []codegraph.Edge, showFrom bool) string {
	if len(edges) == 0 {
		return title + ":无结果" + cgHeritageEmptyHint()
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s,共 %d 个:\n\n", title, len(edges))
	for _, e := range edges {
		if showFrom {
			fmt.Fprintf(&sb, "%s:%d  %s %s\n", e.File, e.Line, arrow, e.From)
		} else {
			fmt.Fprintf(&sb, "%s %s [%s]\n", arrow, e.To, e.Kind)
		}
	}
	return sb.String()
}

// cgImpactCaveat 按比例声明:impact 对静态调用/继承(Go 还精确)已较全,只在特定盲点需补查,
// 不要因此把整个结果当无用而全程改用 Grep。
const cgImpactCaveat = "\n注:多跳反向闭包,基于已解析的边(Go 类型精确)。对直接+传递的静态调用、Go 接口实现已较全。" +
	"盲点(这几类才需用 refs/Grep 补查):接口/动态分发的具体实现、反射、函数值调用、非 Go 语言的继承传播。" +
	"也可能略偏大(同名合并)。"

func formatImpact(name string, depth int, nodes []codegraph.ImpactNode, total int) string {
	if len(nodes) == 0 {
		return fmt.Sprintf("改动 %q 的影响面(深度 %d):在已覆盖的边内未发现下游依赖。", name, depth) + cgImpactCaveat
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "改动 %q 的影响面(深度 %d),已知波及 %d 处:\n\n", name, depth, total)
	for _, n := range nodes {
		fmt.Fprintf(&sb, "  [%d跳·%s] %s  %s:%d\n", n.Hop, n.Via, n.Name, n.File, n.Line)
	}
	if total > len(nodes) {
		fmt.Fprintf(&sb, "\n…还有 %d 处未显示,调大 max。\n", total-len(nodes))
	}
	sb.WriteString(cgImpactCaveat)
	return sb.String()
}

func formatImports(path string, edges []codegraph.Edge) string {
	if len(edges) == 0 {
		return fmt.Sprintf("%s 无 import(或未索引)", path) + cgEmptyHint()
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s 的 import,共 %d 个:\n\n", path, len(edges))
	for _, e := range edges {
		fmt.Fprintf(&sb, "%s\n", e.To)
	}
	return sb.String()
}

func toStr(v any) string {
	s, _ := v.(string)
	return s
}
