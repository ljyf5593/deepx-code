package codegraph

import (
	"bytes"
	"context"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"runtime/debug"
	"time"
)

// 代码图谱构建放进「re-exec 自身」的子进程跑(issue #115 的兜底)。理由:
//   - tree-sitter(纯 Go)在病态输入上会失控分配,goroutine 杀不掉、内存收不回;
//   - 精确 go/types pass 也可能把整棵依赖树 load 进内存。
//   两者都是「进程内拦不住、但换进程能干净杀掉」。子进程自带内存看门狗,超限自杀;
//   父进程拿不到结果就降级(无图谱 / 保留旧图),绝不被拖垮。

// memExitCode 是子进程内存看门狗判定超限后主动退出的码,父进程据此判定降级。
const memExitCode = 75

var (
	// buildMemLimitBytes:子进程建图的堆内存硬上限,看门狗超过即自杀。
	buildMemLimitBytes uint64 = 1500 << 20 // ~1.5 GiB
	// subprocBuildTimeout:父进程等子进程的墙钟上限(防卡死)。
	subprocBuildTimeout = 90 * time.Second
	// useSubprocessBuild:是否走子进程建图(生产 true)。测试里 re-exec 的是测试二进制、
	// 没有 __codegraph-build 子命令,会递归/失败,故 codegraph 测试在 init 里置 false 走进程内。
	useSubprocessBuild = true
)

// buildEnvelope 是子进程 gob 回传父进程的产物(只传原始数据,索引在父进程重放重建)。
type buildEnvelope struct {
	Symbols  []Symbol
	Refs     []Ref
	Edges    []Edge
	Degraded bool
}

// BuildAndEncode 是子进程入口(deepx __codegraph-build <root> <mode>):在本进程建图,
// gob 编码到 w。建图前起内存看门狗。mode: "quick"(语法近似)/ "precise"(go/types)。
func BuildAndEncode(root, mode string, w io.Writer) error {
	startMemWatchdog()

	ix := &Index{root: root} // 子进程只需 root;assemble/goPreciseCallEdges 不依赖其它字段
	var (
		g        *Graph
		degraded bool
		err      error
	)
	if mode == "precise" {
		if edges, ok := goPreciseCallEdges(root); ok {
			g, degraded, err = ix.assemble(edges, true)
		} else {
			g, degraded, err = ix.assemble(nil, false) // 精确失败 → 退回快图
		}
	} else {
		g, degraded, err = ix.assemble(nil, false)
	}
	if err != nil {
		return err
	}
	return gob.NewEncoder(w).Encode(buildEnvelope{
		Symbols: g.Symbols, Refs: g.RawRefs, Edges: g.RawEdges, Degraded: degraded,
	})
}

// startMemWatchdog 起一个 goroutine 轮询堆用量,超 buildMemLimitBytes 即 os.Exit(memExitCode)。
// 纯 Go 解析可被异步抢占,看门狗 goroutine 总能被调度到 —— 这正是「进程内杀不掉、子进程能杀」的关键。
func startMemWatchdog() {
	debug.SetMemoryLimit(int64(buildMemLimitBytes)) // 软上限:逼 GC 在临界点拼命回收(垃圾能压住,live 压不住)
	go func() {
		var m runtime.MemStats
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for range t.C {
			runtime.ReadMemStats(&m)
			if m.HeapAlloc >= buildMemLimitBytes { // live 堆越过硬线 → 失控,自杀
				os.Exit(memExitCode)
			}
		}
	}()
}

// buildViaSubprocess 父进程:exec 自己建图,gob 收结果。
//   - 子进程**启动失败**(环境禁 exec 等)→ 回退到进程内构建(保功能;OOM 概率低)。
//   - 子进程**跑起来后失败**(超内存自杀 / 超时被杀 / 出错)→ 降级:返回 (nil, true, err),不进程内重试(会重蹈 OOM)。
func buildViaSubprocess(root, mode string) (*Graph, bool, error) {
	if !useSubprocessBuild {
		return inProcessBuild(root, mode) // 测试 / 显式关闭 → 进程内
	}
	self, err := os.Executable()
	if err != nil {
		return inProcessBuild(root, mode) // 拿不到自身路径 → 回退
	}
	ctx, cancel := context.WithTimeout(context.Background(), subprocBuildTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, self, "__codegraph-build", root, mode)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard

	if err := cmd.Start(); err != nil {
		return inProcessBuild(root, mode) // 起不来(环境禁 exec)→ 回退进程内
	}
	if err := cmd.Wait(); err != nil {
		// 起来了但失败(看门狗自杀 / 超时 / panic)→ 降级,绝不进程内重试
		return nil, true, fmt.Errorf("codegraph 子进程建图失败(疑似超内存/超时,已降级): %w", err)
	}
	var env buildEnvelope
	if err := gob.NewDecoder(&out).Decode(&env); err != nil {
		return nil, true, fmt.Errorf("codegraph 子进程结果解码失败: %w", err)
	}
	return rebuildFromRaw(env.Symbols, env.Refs, env.Edges), env.Degraded, nil
}

// inProcessBuild 进程内构建(仅在子进程**无法启动**时回退用)。
func inProcessBuild(root, mode string) (*Graph, bool, error) {
	ix := &Index{root: root}
	if mode == "precise" {
		if edges, ok := goPreciseCallEdges(root); ok {
			return ix.assemble(edges, true)
		}
	}
	return ix.assemble(nil, false)
}

// rebuildFromRaw 从子进程传回的原始 Symbols/Refs/Edges 重放 add*,重建带索引的 Graph。
func rebuildFromRaw(symbols []Symbol, refs []Ref, edges []Edge) *Graph {
	g := newGraph()
	for _, s := range symbols {
		g.addSymbol(s)
	}
	for _, r := range refs {
		g.addRef(r)
	}
	for _, e := range edges {
		g.addEdge(e)
	}
	return g
}
