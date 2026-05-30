package tools

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"sync"
	"time"
)

// 后台进程支持:RunCommand(run_in_background=true) 启动的常驻进程(dev server / watch / daemon)
// 登记在此,不阻塞 agent 主循环;之后用 BashOutput 读增量输出/查状态,KillBash 结束(连同子进程树)。
// 包级单例,跟 cgIndex / skillLoader 一样的注入风格;运行时单 workspace。

// 单个后台进程的输出缓冲上限:常驻进程输出通常不大(启动 banner + 偶发日志),
// 超出则丢弃最旧的,只保留尾部,避免重蹈"内存无限上涨"。
const maxBgOutput = 256 * 1024

// lockedBuffer 是并发安全、容量有上限的输出缓冲:子进程 goroutine 写,工具调用读后清空。
type lockedBuffer struct {
	mu sync.Mutex
	b  []byte
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.b = append(l.b, p...)
	if len(l.b) > maxBgOutput {
		l.b = append([]byte(nil), l.b[len(l.b)-maxBgOutput:]...) // 只留尾部,旧的丢弃
	}
	return len(p), nil
}

// drain 取走当前全部输出并清空(BashOutput 的增量读语义)。
func (l *lockedBuffer) drain() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	s := string(l.b)
	l.b = l.b[:0]
	return s
}

type bgProc struct {
	id        string
	cmd       *exec.Cmd
	buf       *lockedBuffer
	startedAt time.Time

	mu      sync.Mutex
	done    bool
	exitErr error
}

var (
	bgMu    sync.Mutex
	bgProcs = map[string]*bgProc{}
	bgSeq   int
)

// startBackground 启动一个常驻进程,立即返回句柄 id,不等它结束。
func startBackground(command, cwd string) ToolResult {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	setPgid(cmd) // 单独进程组,便于 KillBash 连子进程一起杀(否则 node/vite 之类会成孤儿)

	buf := &lockedBuffer{}
	cmd.Stdout = buf
	cmd.Stderr = buf

	if err := cmd.Start(); err != nil {
		return ToolResult{Output: fmt.Sprintf("后台启动失败: %v", err), Success: false}
	}

	bgMu.Lock()
	bgSeq++
	id := fmt.Sprintf("bash_%d", bgSeq)
	p := &bgProc{id: id, cmd: cmd, buf: buf, startedAt: time.Now()}
	bgProcs[id] = p
	bgMu.Unlock()

	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.done = true
		p.exitErr = err
		p.mu.Unlock()
	}()

	return ToolResult{
		Output: fmt.Sprintf(
			"已在后台启动 (id: %s)。\n"+
				"- 用 BashOutput(id=%q) 读输出并查看是否就绪;\n"+
				"- 服务通常不会立刻就绪,稍等再读,或在命令里探活(如 curl);\n"+
				"- 用完务必 KillBash(id=%q) 结束,否则会一直占用端口/资源。",
			id, id, id),
		Success: true,
	}
}

// adoptBackground 把一个已经 cmd.Start() 起来、stdout/stderr 都指向 buf 的前台命令
// 接管到后台子系统:不杀进程,只换管理模式。返回分配的句柄 id。
//
// 用在 RunCommand 的 auto-handoff 路径:命令前台跑超过预算仍未退出,直接交给 bg,
// 这样 `python -m http.server &` 这类命令(包括正确不带 `&` 的)能继续提供服务,
// 模型用 BashOutput / KillBash 接力,验证步骤照旧能跑(对照 issue #20)。
//
// waitErrCh 是调用方那边读 cmd.Wait() 结果的 channel —— 命令真正退出时会有一个值,
// 这里起 goroutine 接收并更新 bgProc.done / exitErr,跟 startBackground 的语义对齐。
func adoptBackground(cmd *exec.Cmd, buf *lockedBuffer, startedAt time.Time, waitErrCh <-chan error) string {
	bgMu.Lock()
	bgSeq++
	id := fmt.Sprintf("bash_%d", bgSeq)
	p := &bgProc{id: id, cmd: cmd, buf: buf, startedAt: startedAt}
	bgProcs[id] = p
	bgMu.Unlock()

	go func() {
		err := <-waitErrCh
		p.mu.Lock()
		p.done = true
		p.exitErr = err
		p.mu.Unlock()
	}()
	return id
}

func lookupBg(args map[string]any) (*bgProc, string) {
	id, _ := args["id"].(string)
	if id == "" {
		return nil, "id 参数为空,请传 RunCommand 后台启动时返回的 id(形如 bash_1)"
	}
	bgMu.Lock()
	p := bgProcs[id]
	bgMu.Unlock()
	if p == nil {
		return nil, fmt.Sprintf("没有 id 为 %q 的后台进程(可能已结束并被清理)", id)
	}
	return p, ""
}

// statusLine 描述后台进程当前状态(运行中 / 已退出+退出码)。
func (p *bgProc) statusLine() string {
	p.mu.Lock()
	done, exitErr := p.done, p.exitErr
	p.mu.Unlock()
	if !done {
		return fmt.Sprintf("[运行中] %s,已运行 %s", p.id, time.Since(p.startedAt).Round(time.Second))
	}
	if exitErr == nil {
		return fmt.Sprintf("[已退出] %s,退出码 0", p.id)
	}
	var ee *exec.ExitError
	if errors.As(exitErr, &ee) {
		return fmt.Sprintf("[已退出] %s,退出码 %d", p.id, ee.ExitCode())
	}
	return fmt.Sprintf("[已退出] %s,错误: %v", p.id, exitErr)
}

// BashOutput 读取后台进程自上次读取以来的新输出,并报告其状态。
func BashOutput(args map[string]any) ToolResult {
	p, errMsg := lookupBg(args)
	if p == nil {
		return ToolResult{Output: errMsg, Success: false}
	}
	out := p.buf.drain()
	if out == "" {
		out = "(暂无新输出)"
	}
	return ToolResult{Output: p.statusLine() + "\n" + out, Success: true}
}

// KillBash 结束后台进程(连同其子进程树),返回剩余输出,并从注册表移除。
// 进程若已自行退出,视为干净的 no-op(成功),不报"process already finished"。
func KillBash(args map[string]any) ToolResult {
	p, errMsg := lookupBg(args)
	if p == nil {
		return ToolResult{Output: errMsg, Success: false}
	}
	p.mu.Lock()
	alreadyDone := p.done
	p.mu.Unlock()

	var killErr error
	if !alreadyDone { // 已退出就不必再杀(killProc 仍对竞态下的"已退出"容错)
		killErr = killProc(p.cmd)
	}

	bgMu.Lock()
	delete(bgProcs, p.id)
	bgMu.Unlock()

	tail := p.buf.drain()
	var msg string
	switch {
	case killErr != nil:
		msg = fmt.Sprintf("结束 %s 时出错: %v", p.id, killErr)
	case alreadyDone:
		msg = fmt.Sprintf("%s 已自行退出,已从列表移除。", p.id)
	default:
		msg = fmt.Sprintf("已结束 %s。", p.id)
	}
	if tail != "" {
		msg += "\n剩余输出:\n" + tail
	}
	return ToolResult{Output: msg, Success: killErr == nil}
}

// toBool 把 LLM 传来的布尔参数归一(可能是 bool 或字符串)。
func toBool(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || x == "yes"
	}
	return false
}
