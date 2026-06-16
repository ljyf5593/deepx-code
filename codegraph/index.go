package codegraph

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Status 是索引的生命周期状态,给状态栏展示用。用 atomic 存,渲染线程可无锁读取。
type Status int32

const (
	StatusIdle     Status = iota // 未构建(还没查询过)
	StatusLoading                // 加载:正在遍历解析
	StatusReady                  // 就绪:已构建且最新
	StatusStale                  // 更新:文件已变、缓存失效,待下次查询重建
	StatusDisabled               // 禁用:工作区是危险根(home/FS 根/系统目录),拒绝建图
	StatusDegraded               // 降级:触及扫描预算上限,图谱可能不完整
)

// Token 返回稳定的状态标识串,供 i18n / 上层映射(不直接耦合渲染)。
func (s Status) Token() string {
	switch s {
	case StatusLoading:
		return "loading"
	case StatusReady:
		return "ready"
	case StatusStale:
		return "stale"
	case StatusDisabled:
		return "disabled"
	case StatusDegraded:
		return "degraded"
	default:
		return "idle"
	}
}

// 遍历时跳过的目录名(版本控制 / 依赖 / 构建产物 / 缓存 / deepx 自身数据)。
// 注意:1) 按目录"名"匹配,只列不易和源码目录重名的;以 "." 开头的目录统一跳过,不必在此重复列。
//  2. **键必须全小写** —— shouldSkipDir 用 strings.ToLower(name) 查找以做到大小写不敏感(issue #115)。
var skipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "vendor": true, ".deepx": true,
	"dist": true, "build": true, "target": true, ".next": true,
	// 各语言依赖 / 缓存 / 构建产物
	"bower_components": true, "pods": true, "__pycache__": true,
	"venv": true, "site-packages": true, ".nuxt": true,
	"out": true, "obj": true, "coverage": true, "__snapshots__": true,
}

// shouldSkipDir 判断遍历时是否跳过该目录:命中 skipDirs(**大小写不敏感**)或以 "." 开头。
// 大小写不敏感是关键(issue #115):Windows/macOS 文件系统大小写不敏感,目录可能叫 Vendor /
// Node_Modules / VENDOR 等;若按原大小写精确匹配,这些依赖目录会漏跳被索引,大仓直接 OOM。
func shouldSkipDir(name string) bool {
	return skipDirs[strings.ToLower(name)] || strings.HasPrefix(name, ".")
}

// 单文件大小上限:超过视为生成 / 压缩产物,跳过,避免拖慢索引。
const maxFileSize = 1 << 20 // 1 MiB

// 单行字节上限(issue #115):任一行超过即视为压缩 / 打包 / 生成 / 数据文件,解析前跳过。
// tree-sitter 的 GLR 在这类超长行上会内存爆炸,且其超时杀不掉已在 native 解析的 goroutine
// (遗弃的 runaway 会持续吃内存直到 OOM)→ 只能从源头不喂这类文件。用 var 便于测试调小。
var maxLineBytes = 40 << 10 // 40 KiB

// hasOverlongLine 报告 src 是否含超过 max 字节的行(O(n) 扫一遍,不分配)。
func hasOverlongLine(src []byte, max int) bool {
	start := 0
	for i, b := range src {
		if b == '\n' {
			if i-start > max {
				return true
			}
			start = i + 1
		}
	}
	return len(src)-start > max
}

// 整次构建的扫描预算:任一触顶即停,图谱标记为降级(StatusDegraded)。
// 这是与"根目录是什么"无关的硬兜底 —— 即使 gate 漏判、或撞上病态大仓,
// 也保证构建有界:不会再 CPU 焊死 / 内存无限上涨 / 永不结束。
// 阈值取得比正常大仓宽松,正常项目不会触发,只拦失控场景。用 var 便于测试覆盖。
var (
	maxIndexFiles    = 50000            // 被解析的源文件数上限
	maxIndexBytes    = int64(512 << 20) // 累计读取字节上限(512 MiB)
	maxIndexDuration = 60 * time.Second // 单次遍历耗时上限
)

// 精确 go/types pass 的体量门控(issue #115):go/packages.Load("./...") 带 NeedSyntax|
// NeedTypesInfo 会把整个 module + 依赖树的语法树/类型信息一次性全 load 进内存,无内置上限,
// 大项目下可吃几十 GB 直接 OOM。快图有 512MiB 预算约束,精确 pass 也必须有对应的体量上限:
// 模块自身 Go 源码超过任一阈值,就**跳过精确 pass**、保留已建好的有界快图(Go 调用边退化为
// 语法近似,功能可用)。用 var 便于测试调小。
var (
	maxPreciseGoFiles = 2000            // 模块内被精确解析的 .go 文件数上限
	maxPreciseGoBytes = int64(32 << 20) // 模块内 .go 源码总字节上限(32 MiB)
)

// projectMarkers:出现在根目录顶层即认为"这是一个项目根",可放心自动预热。
// 正向识别比黑名单健壮 —— home 顶层通常没有这些,真实项目几乎都有。
var projectMarkers = []string{
	".git", ".hg", ".svn",
	"go.mod", "package.json", "Cargo.toml", "pyproject.toml", "setup.py",
	"pom.xml", "build.gradle", "composer.json", "Gemfile", "CMakeLists.txt",
}

// classifyRoot 判定一个目录作为图谱根的安全等级。纯函数(只读文件系统),便于测试。
//
//	forbidden=true  : 危险根(FS 根 / home 本身 / home 的祖先 / 系统目录),一律不建图。
//	forbidden=false :
//	    isProject=true  → 像项目根,开机自动预热;
//	    isProject=false → 安全但不像项目(如随手打开的散目录),不自动预热,
//	                      仅在显式/惰性查询时构建,且一律受扫描预算约束。
func classifyRoot(root string) (forbidden, isProject bool, reason string) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return true, false, "无法解析工作目录的绝对路径"
	}
	clean := filepath.Clean(abs)

	// 文件系统根 / 盘符根:Clean 后其父级等于自身。
	if clean == filepath.Dir(clean) {
		return true, false, "工作目录是文件系统根目录"
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		hc := filepath.Clean(home)
		if clean == hc {
			return true, false, "工作目录是用户主目录(范围过大)"
		}
		if isAncestor(clean, hc) { // home 落在 clean 之下,如 /Users、/home
			return true, false, "工作目录是主目录的上级(范围过大)"
		}
	}
	if systemDirs[clean] {
		return true, false, "工作目录是系统目录(范围过大)"
	}

	for _, m := range projectMarkers {
		if _, err := os.Stat(filepath.Join(clean, m)); err == nil {
			return false, true, ""
		}
	}
	return false, false, "未检测到项目标志(.git/go.mod/package.json 等),不自动预建图谱"
}

// systemDirs:常见系统 / 平台目录,作为图谱根均无意义且代价高。按绝对路径精确匹配,
// 不影响项目内部出现的同名子目录(那由 skipDirs 另行处理)。
var systemDirs = func() map[string]bool {
	m := map[string]bool{}
	for _, p := range []string{
		"/", "/usr", "/etc", "/var", "/tmp", "/opt", "/bin", "/sbin",
		"/System", "/Library", "/Applications", "/Volumes", "/private",
		"/Users", "/home", "/root",
	} {
		m[filepath.Clean(p)] = true
	}
	return m
}()

// isAncestor 报告 dir 是否为 target 的祖先目录(或相等由调用方另判)。
func isAncestor(dir, target string) bool {
	rel, err := filepath.Rel(dir, target)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && rel != "."
}

// Index 持有某个 workspace 根的图谱,懒构建 + 进程内缓存。Reindex 可强制重建。
//
// 两段式:Graph() 同步出"快图"(语法层,瞬间可用,Go 调用边是近似版);随后异步在后台跑
// go/types 精确解析(慢,~秒级,要类型检查依赖树),跑完原子换上"精确图"(Go 同名方法不再合并)。
// 精确结果按 Go 文件签名缓存:只在 .go 真变了才重算,非 Go 编辑直接复用,避免反复付那份开销。
// 内存态、单进程;落盘增量索引留作后续优化。
type Index struct {
	root string

	// 由 NewIndex 在创建时算定的根目录安全等级(见 classifyRoot),全程只读。
	forbidden bool   // 危险根:一切构建入口直接拒绝
	isProject bool   // 像项目根:允许开机自动预热
	reason    string // forbidden / 非项目根时的人类可读原因,供状态栏与工具提示

	mu sync.Mutex
	g  *Graph
	st atomic.Int32 // Status,无锁读供状态栏用

	gPrecise       bool   // 当前 ix.g 是否已是精确图
	goSig          string // ix.g 精确图对应的 Go 文件签名
	preciseRunning bool   // 后台精确解析是否在跑(避免并发重复)

	rebuildMu    sync.Mutex  // 保护 rebuildTimer
	rebuildTimer *time.Timer // Invalidate 后的防抖重建定时器(连续编辑只在静默后重建一次)
}

// rebuildDebounce 是文件变动后到后台重建之间的静默窗口:一次工具循环里连续改多个文件时,
// 每次 Invalidate 都重置它,只在最后一次编辑静默后才真正重建一次,避免重建风暴。
const rebuildDebounce = 600 * time.Millisecond

// Status 返回当前索引状态(无锁,供 TUI 每帧读取)。
func (ix *Index) Status() Status { return Status(ix.st.Load()) }

func (ix *Index) setStatus(s Status) { ix.st.Store(int32(s)) }

// setStatusBuilt 在一次构建成功后置状态:触顶预算则降级,否则就绪。
func (ix *Index) setStatusBuilt(degraded bool) {
	if degraded {
		ix.setStatus(StatusDegraded)
	} else {
		ix.setStatus(StatusReady)
	}
}

// NewIndex 创建绑定到 root(workspace 绝对路径)的索引,此时还未构建。
// 创建时即算定根目录的安全等级:危险根直接置为禁用,后续一切构建入口都会拒绝。
func NewIndex(root string) *Index {
	forbidden, isProject, reason := classifyRoot(root)
	ix := &Index{root: root, forbidden: forbidden, isProject: isProject, reason: reason}
	if forbidden {
		ix.setStatus(StatusDisabled)
	}
	return ix
}

// Disabled 报告图谱是否因危险根被禁用;Reason 返回禁用 / 不自动预热的原因。
func (ix *Index) Disabled() bool { return ix.forbidden }
func (ix *Index) Reason() string { return ix.reason }

// Prewarm 后台预热:开机只建"快图"(语法层,便宜),不阻塞调用方。
// 刻意不在此跑精确解析(go/packages 较重、可能联网)—— 那留到模型真正调用 CodeGraph 时
// 才后台升级,避免"用户根本没用代码图谱却在每次启动后台跑 go list"的浪费。
func (ix *Index) Prewarm() {
	// 危险根:直接禁用,绝不在开机时遍历(本类故障的根因)。
	if ix.forbidden {
		ix.setStatus(StatusDisabled)
		return
	}
	// 安全但不像项目根(散目录):不自动预热,留到模型显式查询时惰性构建(受预算约束)。
	if !ix.isProject {
		ix.setStatus(StatusIdle)
		return
	}
	ix.setStatus(StatusLoading)
	go func() {
		ix.mu.Lock()
		if ix.g == nil {
			g, degraded, err := buildViaSubprocess(ix.root, "quick")
			if err != nil {
				ix.mu.Unlock()
				ix.setStatus(StatusIdle)
				return
			}
			ix.g = g
			ix.gPrecise = false
			ix.mu.Unlock()
			ix.setStatusBuilt(degraded)
			return
		}
		ix.mu.Unlock()
		ix.setStatus(StatusReady)
	}()
}

// Graph 返回图谱(快图,首次调用时构建并缓存),并在后台异步把 Go 调用边升级为精确版。
// 危险根:返回空图,绝不遍历。
func (ix *Index) Graph() (*Graph, error) {
	if ix.forbidden {
		return newGraph(), nil
	}
	ix.mu.Lock()
	if ix.g != nil {
		g := ix.g
		ix.mu.Unlock()
		ix.maybePrecise()
		return g, nil
	}
	g, degraded, err := buildViaSubprocess(ix.root, "quick") // 快图:语法近似
	if err != nil {
		ix.mu.Unlock()
		ix.setStatus(StatusIdle)
		return nil, err
	}
	ix.g = g
	ix.gPrecise = false
	ix.mu.Unlock()
	ix.setStatusBuilt(degraded)
	if !degraded {
		ix.maybePrecise() // 已降级则不再追加跑更重的精确解析
	}
	return g, nil
}

// Reindex 丢弃缓存并立即重建快图,返回符号数(精确升级仍走后台)。
func (ix *Index) Reindex() (int, error) {
	if ix.forbidden {
		return 0, fmt.Errorf("代码图谱已禁用:%s", ix.reason)
	}
	ix.mu.Lock()
	g, degraded, err := buildViaSubprocess(ix.root, "quick")
	if err != nil {
		ix.mu.Unlock()
		return 0, err
	}
	ix.g = g
	ix.gPrecise = false
	n := len(g.Symbols)
	ix.mu.Unlock()
	ix.setStatusBuilt(degraded)
	if !degraded {
		ix.maybePrecise()
	}
	return n, nil
}

// Invalidate 标记缓存失效并安排后台重建。供文件被编辑后调用(增量刷新的简化版)。
// 只有"已构建过"(就绪/降级)的图谱被改动才降级成"待更新"并排队重建;从没构建过(idle)的
// 维持 idle —— 否则模型还没用过图谱、只是改了个文件,状态就会错误地跳成"更新"。
//
// 重建走防抖后台任务:不再等下次 Graph() 查询(那次查询可能永远不来,状态就会一直卡在"更新")。
func (ix *Index) Invalidate() {
	ix.mu.Lock()
	ix.g = nil
	ix.gPrecise = false
	ix.mu.Unlock()

	switch Status(ix.st.Load()) {
	case StatusReady, StatusDegraded:
		ix.setStatus(StatusStale)
		ix.scheduleRebuild()
	}
}

// scheduleRebuild 在静默窗口后于后台重建快图,把状态从"更新"刷回"就绪"。
// 连续编辑会不断重置定时器,因此只在最后一次编辑静默 rebuildDebounce 后重建一次。
func (ix *Index) scheduleRebuild() {
	if ix.forbidden {
		return
	}
	ix.rebuildMu.Lock()
	defer ix.rebuildMu.Unlock()
	if ix.rebuildTimer != nil {
		ix.rebuildTimer.Stop()
	}
	ix.rebuildTimer = time.AfterFunc(rebuildDebounce, func() {
		ix.mu.Lock()
		if ix.g != nil { // 期间已有查询重建过,直接置就绪即可
			ix.mu.Unlock()
			ix.setStatus(StatusReady)
			return
		}
		g, degraded, err := buildViaSubprocess(ix.root, "quick")
		if err != nil {
			ix.mu.Unlock()
			return // 建失败:保持"更新",留待下次编辑或查询再试
		}
		ix.g = g
		ix.gPrecise = false
		ix.mu.Unlock()
		ix.setStatusBuilt(degraded)
		if !degraded {
			ix.maybePrecise()
		}
	})
}

// maybePrecise 在后台把当前快图升级成精确图(若有 Go 代码且尚未精确)。
// 命中 Go 签名缓存就复用,否则跑 go/types(慢);全程不阻塞调用方,跑完原子换图。
func (ix *Index) maybePrecise() {
	if ix.forbidden {
		return // 危险根:绝不跑 go/types(会扫整棵依赖树)
	}
	// 体量门控(issue #115):Go 源码过大就跳过精确 pass,避免 go/packages 把整棵依赖树
	// load 进内存导致 OOM。保留已建好的有界快图即可。
	if files, bytes := ix.goCorpusStats(); files == 0 || files > maxPreciseGoFiles || bytes > maxPreciseGoBytes {
		return
	}
	sig := ix.goSignature()
	if sig == "" {
		return // 没有 Go 文件,免跑
	}
	ix.mu.Lock()
	if ix.preciseRunning || (ix.gPrecise && ix.goSig == sig) {
		ix.mu.Unlock()
		return
	}
	ix.preciseRunning = true
	ix.mu.Unlock()

	go func() {
		// 精确解析(goPreciseCallEdges + 重新 assemble)整体在受内存限额的子进程里跑,
		// 成功才原子换上精确图;失败 / 降级则保持当前快图。
		g2, _, err := buildViaSubprocess(ix.root, "precise")
		ix.mu.Lock()
		if err == nil && g2 != nil {
			ix.g = g2
			ix.gPrecise = true
			ix.goSig = sig
		}
		ix.preciseRunning = false
		ix.mu.Unlock()
	}()
}

// assemble 遍历 workspace 构图。usePrecise=true 时 Go 调用边用传入的精确边,否则用语法近似边。
// 第二个返回值 degraded 表示构建因触及扫描预算(文件数/字节数/耗时)被提前截断,图谱可能不完整。
func (ix *Index) assemble(preciseGoCalls []Edge, usePrecise bool) (_ *Graph, degraded bool, _ error) {
	g := newGraph()
	var goApproxCalls []Edge // 语法近似的 Go 调用边(快图用)
	var (
		files    int
		bytes    int64
		deadline = time.Now().Add(maxIndexDuration)
	)
	err := filepath.WalkDir(ix.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // 单个条目出错就跳过,别中断整次遍历
		}
		// 预算兜底:与根目录是什么无关,任一触顶即停,保证构建有界。
		if files >= maxIndexFiles || bytes >= maxIndexBytes || time.Now().After(deadline) {
			degraded = true
			return filepath.SkipAll
		}
		name := d.Name()
		if d.IsDir() {
			if path != ix.root && shouldSkipDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		p := parserFor(path)
		if p == nil {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.Size() > maxFileSize {
			return nil
		}
		src, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		files++
		bytes += int64(len(src))
		// 超长行 = 压缩 / 打包 / 生成 / 数据文件:tree-sitter 的 GLR 解析对这类输入会内存爆炸
		// (issue #115),且其超时杀不掉已在 native 解析的 goroutine,遗弃的 runaway 会持续吃内存。
		// 这类文件对代码图谱也几乎无价值 —— 解析前直接跳过,从源头不喂炸弹。
		if hasOverlongLine(src, maxLineBytes) {
			return nil
		}
		rel, relErr := filepath.Rel(ix.root, path)
		if relErr != nil {
			rel = path
		}
		rel = filepath.ToSlash(rel)
		res, perr := p.Parse(rel, src)
		if perr != nil {
			return nil // 坏文件跳过,不污染整体
		}
		for _, s := range res.Symbols {
			g.addSymbol(s)
		}
		for _, r := range res.Refs {
			g.addRef(r)
		}
		for _, e := range res.Edges {
			if usePrecise && e.Kind == EdgeCall && strings.HasSuffix(e.File, ".go") {
				continue // 精确模式下丢弃 Go 语法近似边,改用传入的精确边
			}
			if !usePrecise && e.Kind == EdgeCall && strings.HasSuffix(e.File, ".go") {
				goApproxCalls = append(goApproxCalls, e)
				continue
			}
			g.addEdge(e)
		}
		return nil
	})
	if err != nil {
		return nil, degraded, err
	}
	if usePrecise {
		for _, e := range preciseGoCalls {
			g.addEdge(e)
		}
	} else {
		for _, e := range goApproxCalls {
			g.addEdge(e)
		}
	}
	return g, degraded, nil
}

// goCorpusStats 统计 workspace 内(跳过 skipDirs/隐藏目录)的 .go 文件数与总字节,只 stat 不读。
// 用于精确 pass 的体量门控(issue #115)。skip 规则与 assemble/goSignature 一致,
// 因此不数 vendor/依赖/.git,约等于「module 自身会被 ./... 精确解析的 Go 源码量」。
func (ix *Index) goCorpusStats() (files int, bytes int64) {
	_ = filepath.WalkDir(ix.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != ix.root && shouldSkipDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			if info, e := d.Info(); e == nil {
				files++
				bytes += info.Size()
			}
		}
		return nil
	})
	return files, bytes
}

// goSignature 扫 workspace 里所有 .go 文件的大小+修改时间,拼成签名;Go 没变签名就不变。
// 只 stat 不读内容,便宜。
func (ix *Index) goSignature() string {
	var b strings.Builder
	_ = filepath.WalkDir(ix.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if path != ix.root && shouldSkipDir(name) {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") {
			if info, e := d.Info(); e == nil {
				fmt.Fprintf(&b, "%s:%d:%d;", path, info.Size(), info.ModTime().UnixNano())
			}
		}
		return nil
	})
	return b.String()
}
