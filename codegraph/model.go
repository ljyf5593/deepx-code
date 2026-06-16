// Package codegraph 给 deepx 提供"代码图谱":把 workspace 里的源码解析成符号(定义)与
// 引用,让 agent 能做符号级导航(找定义 / 找引用 / 看文件结构),而不是只能 Grep 文本。
//
// 设计分层:
//   - model.go   : Symbol / Ref 数据模型 + Graph 查询
//   - parser.go  : Parser 接口 + 按扩展名分发的注册表(多语言扩展点)
//   - parser_go.go: Go 语言解析器(stdlib go/parser,零外部依赖)
//   - index.go   : 遍历 workspace、调度 parser、构建并缓存 Graph
//
// 多语言:TS / Python 等只需新增实现 Parser 接口的文件并 Register,Graph / 工具层不动。
package codegraph

import (
	"sort"
	"strings"
)

// Kind 是符号种类。跨语言统一用这套,具体 parser 负责映射到它。
type Kind string

const (
	KindFunc   Kind = "func"   // 顶层函数
	KindMethod Kind = "method" // 带接收者 / 绑定到类型的方法
	KindType   Kind = "type"   // 类型 / 类 / 接口 / 结构体
	KindVar    Kind = "var"    // 变量
	KindConst  Kind = "const"  // 常量
	KindField  Kind = "field"  // 结构体 / 类字段
)

// Symbol 是一个定义点。File 是相对 workspace 根的路径,Line 1-based。
type Symbol struct {
	Name      string
	Kind      Kind
	File      string
	Line      int
	EndLine   int
	Container string // 所属类型(method/field 用),顶层符号为空
	Signature string // 单行签名预览,已折成一行并截断
	Lang      string
	Exported  bool
}

// QualifiedName 返回带容器前缀的限定名,如 "Manager.SessionID";无容器则就是 Name。
func (s Symbol) QualifiedName() string {
	if s.Container != "" {
		return s.Container + "." + s.Name
	}
	return s.Name
}

// Ref 是一次标识符引用(使用点)。比 Grep 强在于它只命中真正的标识符,不会撞到注释 / 字符串里的同名子串。
type Ref struct {
	Name string
	File string
	Line int
}

// EdgeKind 是边的类型。
type EdgeKind string

const (
	EdgeCall       EdgeKind = "call"       // From 调用了 To
	EdgeImport     EdgeKind = "import"     // From(文件)导入了 To(import path)
	EdgeExtends    EdgeKind = "extends"    // From 继承/嵌入 To(子→父):struct 嵌入、class extends、interface 嵌入
	EdgeImplements EdgeKind = "implements" // From(具体类型)实现了 To(接口)
)

// Edge 是图里的一条有向边。
//   - call:  From=调用方限定名(如 "Manager.Save" / "New"),To=被调用短名,File/Line=调用点
//   - import: From=文件路径,To=import path,File/Line=import 行
//
// ToQual 是被调用的"限定名":Go 经 go/types 精确解析后,方法调用会填成 "接收者类型.方法"
// (如 "User.Save"),用它查 callers 不会跟同名方法合并;未精确解析时 ToQual == To(短名)。
type Edge struct {
	From   string
	To     string
	ToQual string
	Kind   EdgeKind
	File   string
	Line   int
}

// ParseResult 是单文件解析产物:定义、引用、边。
type ParseResult struct {
	Symbols []Symbol
	Refs    []Ref
	Edges   []Edge
}

// Graph 是整个 workspace 的符号图谱。查询方法都只读,构建完后并发读安全。
type Graph struct {
	Symbols []Symbol
	// RawRefs / RawEdges 是原始引用 / 边列表(导出,供子进程建图后 gob 序列化回父进程;
	// 父进程重放 add* 重建下方索引)。索引 map 未导出、gob 不传,靠重放还原。
	RawRefs  []Ref
	RawEdges []Edge

	defByName  map[string][]int  // 符号名(非限定)→ Symbols 下标
	refsByName map[string][]Ref  // 标识符名 → 引用点
	calleeIdx  map[string][]Edge // 被调用短名 → 调用它的 call 边(查 callers)
	callerIdx  map[string][]Edge // 调用方名(限定+短名都建索引)→ 它发出的 call 边(查 callees)
	importsBy  map[string][]Edge // 文件路径 → import 边
	extFrom    map[string][]Edge // From → 它继承/嵌入的边(查 supertypes)
	extTo      map[string][]Edge // To → 继承/嵌入它的边(查 subtypes)
	implFrom   map[string][]Edge // From → 它实现的接口边
	implTo     map[string][]Edge // To(接口)→ 实现它的类型边(查 implementers)
}

func newGraph() *Graph {
	return &Graph{
		defByName:  map[string][]int{},
		refsByName: map[string][]Ref{},
		calleeIdx:  map[string][]Edge{},
		callerIdx:  map[string][]Edge{},
		importsBy:  map[string][]Edge{},
		extFrom:    map[string][]Edge{},
		extTo:      map[string][]Edge{},
		implFrom:   map[string][]Edge{},
		implTo:     map[string][]Edge{},
	}
}

func (g *Graph) addSymbol(s Symbol) {
	idx := len(g.Symbols)
	g.Symbols = append(g.Symbols, s)
	g.defByName[s.Name] = append(g.defByName[s.Name], idx)
}

func (g *Graph) addRef(r Ref) {
	g.RawRefs = append(g.RawRefs, r)
	g.refsByName[r.Name] = append(g.refsByName[r.Name], r)
}

func (g *Graph) addEdge(e Edge) {
	g.RawEdges = append(g.RawEdges, e) // 原始边留底,供序列化重放
	switch e.Kind {
	case EdgeCall:
		if e.ToQual == "" {
			e.ToQual = e.To
		}
		// 被调用方按短名 + 限定名各建一份索引:短名查得到合并视图,限定名查得到精确视图。
		g.calleeIdx[e.To] = append(g.calleeIdx[e.To], e)
		if e.ToQual != e.To {
			g.calleeIdx[e.ToQual] = append(g.calleeIdx[e.ToQual], e)
		}
		g.callerIdx[e.From] = append(g.callerIdx[e.From], e)
		if _, short := splitQualified(e.From); short != e.From {
			g.callerIdx[short] = append(g.callerIdx[short], e)
		}
	case EdgeImport:
		g.importsBy[e.File] = append(g.importsBy[e.File], e)
	case EdgeExtends:
		g.extFrom[e.From] = append(g.extFrom[e.From], e)
		g.extTo[e.To] = append(g.extTo[e.To], e)
	case EdgeImplements:
		g.implFrom[e.From] = append(g.implFrom[e.From], e)
		g.implTo[e.To] = append(g.implTo[e.To], e)
	}
}

// Implementers 返回实现了接口 name 的类型(Go 隐式接口的杀手查询;grep 查不到)。
func (g *Graph) Implementers(name string) []Edge {
	_, short := splitQualified(name)
	return sortEdgesByTarget(g.implTo[short])
}

// Subtypes 返回继承/嵌入了 name 的类型(谁 extends/embeds 它)。
func (g *Graph) Subtypes(name string) []Edge {
	_, short := splitQualified(name)
	return sortEdgesByTarget(g.extTo[short])
}

// Supertypes 返回 name 继承/嵌入的父类型 + 它实现的接口(它派生自什么)。
func (g *Graph) Supertypes(name string) []Edge {
	_, short := splitQualified(name)
	out := append([]Edge(nil), g.extFrom[short]...)
	out = append(out, g.implFrom[short]...)
	return sortEdgesByTarget(out)
}

// ImpactNode 是影响面里的一个受影响符号:Name 受影响符号,Hop 距起点几跳,Via 通过什么边波及。
type ImpactNode struct {
	Name string
	Hop  int
	Via  EdgeKind
	File string
	Line int
}

// reverseDeps 返回"依赖 name 的"反向边:调用它的(callers)+ 实现它的接口 + 继承/嵌入它的。
// 同时按限定名和短名各查一遍并集,保证影响面召回(宁可偏大不漏)。
func (g *Graph) reverseDeps(name string) []Edge {
	var out []Edge
	add := func(k string) {
		out = append(out, g.calleeIdx[k]...)
		out = append(out, g.implTo[k]...)
		out = append(out, g.extTo[k]...)
	}
	add(name)
	if _, short := splitQualified(name); short != name {
		add(short)
	}
	return out
}

// Impact 返回改动 name 会传递波及的下游符号(反向闭包 BFS,按跳数去重)。
// depth 限制跳数,max 限制返回条数。这是"blast radius / 影响面"——比手动逐层挖 callers 省事。
// 名字级解析,可能偏大(over-estimate),偏大比漏报安全。
func (g *Graph) Impact(name string, depth, max int) (nodes []ImpactNode, total int) {
	if depth <= 0 {
		depth = 3
	}
	seen := map[string]bool{name: true}
	if _, short := splitQualified(name); short != name {
		seen[short] = true
	}
	type qi struct {
		name string
		hop  int
	}
	queue := []qi{{name, 0}}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if cur.hop >= depth {
			continue
		}
		for _, e := range g.reverseDeps(cur.name) {
			if seen[e.From] {
				continue
			}
			seen[e.From] = true
			nodes = append(nodes, ImpactNode{Name: e.From, Hop: cur.hop + 1, Via: e.Kind, File: e.File, Line: e.Line})
			queue = append(queue, qi{e.From, cur.hop + 1})
		}
	}
	total = len(nodes)
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].Hop != nodes[j].Hop {
			return nodes[i].Hop < nodes[j].Hop
		}
		return nodes[i].Name < nodes[j].Name
	})
	if max > 0 && len(nodes) > max {
		nodes = nodes[:max]
	}
	return nodes, total
}

func sortEdgesByTarget(in []Edge) []Edge {
	out := append([]Edge(nil), in...)
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out
}

// Callers 返回调用了 name 的所有调用点。name 给限定名("User.Save")则精确匹配、不与同名合并;
// 给短名("Save")则返回全部同名调用(合并视图)。限定名无精确数据时回退到短名(合并)。
func (g *Graph) Callers(name string, max int) (edges []Edge, total int) {
	hits := g.calleeIdx[name]
	if len(hits) == 0 {
		if _, short := splitQualified(name); short != name {
			hits = g.calleeIdx[short]
		}
	}
	return capEdges(hits, max)
}

// Callees 返回 name(调用方,限定名或短名)发出的所有调用,最多 max 条。
func (g *Graph) Callees(name string, max int) (edges []Edge, total int) {
	return capEdges(g.callerIdx[name], max)
}

// Imports 返回某个文件的全部 import 边。
func (g *Graph) Imports(file string) []Edge {
	out := append([]Edge(nil), g.importsBy[file]...)
	sort.Slice(out, func(i, j int) bool { return out[i].To < out[j].To })
	return out
}

func capEdges(all []Edge, max int) (edges []Edge, total int) {
	total = len(all)
	edges = append([]Edge(nil), all...)
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].File != edges[j].File {
			return edges[i].File < edges[j].File
		}
		return edges[i].Line < edges[j].Line
	})
	if max > 0 && len(edges) > max {
		edges = edges[:max]
	}
	return edges, total
}

// Def 返回名为 name 的定义。name 可以是非限定名("SessionID")或限定名("Manager.SessionID")。
func (g *Graph) Def(name string) []Symbol {
	container, short := splitQualified(name)
	var out []Symbol
	for _, idx := range g.defByName[short] {
		s := g.Symbols[idx]
		if container != "" && !strings.EqualFold(s.Container, container) {
			continue
		}
		out = append(out, s)
	}
	sortSymbols(out)
	return out
}

// FindSymbols 按子串(大小写不敏感)模糊匹配符号名,可选按 kind 过滤,最多返回 max 条。
// query 为空则列出全部(配合 kind/max 当目录用)。
func (g *Graph) FindSymbols(query string, kind Kind, max int) (hits []Symbol, total int) {
	q := strings.ToLower(query)
	for _, s := range g.Symbols {
		if kind != "" && s.Kind != kind {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(s.Name), q) {
			continue
		}
		total++
		hits = append(hits, s)
	}
	sortSymbols(hits)
	if max > 0 && len(hits) > max {
		hits = hits[:max]
	}
	return hits, total
}

// Refs 返回名为 name 的所有引用点,最多 max 条。
func (g *Graph) Refs(name string, max int) (refs []Ref, total int) {
	_, short := splitQualified(name)
	all := g.refsByName[short]
	total = len(all)
	refs = all
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].File != refs[j].File {
			return refs[i].File < refs[j].File
		}
		return refs[i].Line < refs[j].Line
	})
	if max > 0 && len(refs) > max {
		refs = refs[:max]
	}
	return refs, total
}

// Outline 返回某个文件里的全部符号,按行号排序 —— 给"看这个文件的结构"用。
func (g *Graph) Outline(file string) []Symbol {
	var out []Symbol
	for _, s := range g.Symbols {
		if s.File == file {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Line < out[j].Line })
	return out
}

// splitQualified 拆 "Container.Name";无点则 container 为空。
func splitQualified(name string) (container, short string) {
	if i := strings.LastIndex(name, "."); i >= 0 {
		return name[:i], name[i+1:]
	}
	return "", name
}

// sortSymbols 统一排序:先文件名,再行号,稳定可预测。
func sortSymbols(ss []Symbol) {
	sort.Slice(ss, func(i, j int) bool {
		if ss[i].File != ss[j].File {
			return ss[i].File < ss[j].File
		}
		return ss[i].Line < ss[j].Line
	})
}
