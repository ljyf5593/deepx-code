package tools

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// autoBackgroundBudget 是前台命令"还没退出就自动切后台"的预算时间。
// 对齐 claude-code 的 ASSISTANT_BLOCKING_BUDGET_MS(15s)设计 —— 主 agent 不该为单条命令
// 卡这么久;超 15s 仍在跑的命令大概率是 dev server / watch / 长构建,统一切到 bg 不杀进程,
// 模型继续推进,后续用 BashOutput / KillBash 接力。
//
// 设成 package-level var 而非 const,方便测试时调小(避免单测真等 15s)。
var autoBackgroundBudget = 15 * time.Second

// readerDrainGrace:进程退出后,最多再等输出管道收尾这么久,以抓全残余输出。
// 正常完成的命令几乎瞬间 EOF;若超过这点仍未 EOF,说明有后台子进程还占着输出
// (典型是命令里用 shell `&` 把 server / daemon 丢到了后台)→ 切到后台管理给句柄,
// 而不是干等。设成 var 便于测试调小。
var readerDrainGrace = 200 * time.Millisecond

// startWithPipe 启动 cmd,stdout/stderr 接到一个真实管道(*os.File),父进程随即关掉写端,
// 起 goroutine 把管道内容拷进 buf;管道读到 EOF(所有写端都关)时关闭返回的 readerDone。
//
// 关键:用 *os.File 而不是把 buf(io.Writer)直接挂到 cmd.Stdout —— 后者会让 Go 内部起一条
// io.Copy goroutine,而 cmd.Wait() 必须等这条 goroutine 读到 EOF 才返回。一旦命令用 `&` 把
// 长命子进程丢到后台,子进程继承并占着写端,EOF 永不到 → Wait() 永不返回(issue #20 卡死根因)。
// 改用 *os.File 后,Go 直接把 fd 交给子进程、不起内部 goroutine,cmd.Wait() 只等"进程退出",
// 不再被孤儿子进程占着的管道拖住。
func startWithPipe(cmd *exec.Cmd, buf *lockedBuffer) (readerDone chan struct{}, err error) {
	pr, pw, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	cmd.Stdout = pw
	cmd.Stderr = pw
	if err := cmd.Start(); err != nil {
		pw.Close()
		pr.Close()
		return nil, err
	}
	pw.Close() // 父进程不再持有写端,只剩子进程(组)持有;它们全退出后 pr 才 EOF
	readerDone = make(chan struct{})
	go func() {
		_, _ = io.Copy(buf, pr)
		pr.Close()
		close(readerDone)
	}()
	return readerDone, nil
}

// backgroundHandoffResult 是"前台命令被切到后台"统一的返回文案(15s 预算到期 / 进程退出但
// 仍有后台子进程占着输出两种路径共用)。
func backgroundHandoffResult(id, reason string) ToolResult {
	return ToolResult{
		Output: fmt.Sprintf(
			"%s,已**切到后台**(同一个进程继续运行,没杀重启)。\n"+
				"句柄 id: %s\n"+
				"- 后续用 BashOutput(id=%q) 读输出 / 查就绪;\n"+
				"- 用完用 KillBash(id=%q) 收尾;\n\n"+
				"提示:下次启动 dev server / watch / daemon 这类常驻进程,**直接传 `run_in_background: true`**\n"+
				"(无需在命令尾加 `&` / nohup)。",
			reason, id, id, id),
		Success: true,
	}
}

// RunCommand 执行 shell 命令并返回输出。
// 参数:
//
//	command            (string) 要执行的命令
//	cwd                (string, 可选) 工作目录
//	timeout            (int,    可选) 超时秒数,默认 60(仅前台模式的硬上限;auto-bg 优先)
//	run_in_background  (bool,   可选) true → 直接走后台路径,立即返回句柄
//
// 前台路径行为:
//  1. 在 autoBackgroundBudget(15s)内退出 → 正常返回 stdout/stderr
//  2. 超 15s 仍在跑 + 不是 sleep 类 → 自动接管到后台,返回句柄(进程不杀,继续跑)
//  3. sleep 等"用户明确要等"的命令 → 不自动 bg,跑到 timeout 才算超时
func RunCommand(args map[string]any) ToolResult {
	command, _ := args["command"].(string)
	if strings.TrimSpace(command) == "" {
		return ToolResult{Output: "错误: command 参数为空", Success: false}
	}
	// 沙箱预检(前台/后台同一入口,这里一处覆盖):当前模式策略不放行就拒掉,不执行。
	if err := SandboxCheck(command); err != nil {
		return ToolResult{Output: "🛡️ " + err.Error(), Success: false}
	}
	cwd, _ := args["cwd"].(string)

	// 模型显式 run_in_background=true:直接走后台路径。
	if toBool(args["run_in_background"]) {
		return startBackground(command, cwd)
	}

	timeout := toInt(args["timeout"], 60)
	if timeout <= 0 {
		timeout = 60
	}
	return runForegroundWithAutoHandoff(command, cwd, timeout)
}

// runForegroundWithAutoHandoff 前台启动命令,三路 select 等结果:
//
//	(A) 命令在 autoBackgroundBudget 内退出 → 返回输出
//	(B) 超 autoBackgroundBudget,命令仍在跑 + 允许 auto-bg → 接管到 bg(同一进程,不杀),返回句柄
//	(C) 超 timeout(硬上限)→ 杀进程,返回超时错误(老行为)
//
// 关键设计:auto-bg 路径**不杀进程**,只换管理模式;模型拿到 id 继续推进。
func runForegroundWithAutoHandoff(command, cwd string, timeoutSec int) ToolResult {
	cmd, err := sandboxCmd(command, cwd) // native=本地 shell;docker=容器内 exec
	if err != nil {
		return ToolResult{Output: "🛡️ 沙箱启动失败: " + err.Error(), Success: false}
	}
	setPgid(cmd) // 进程组化:auto-bg 后 KillBash 能整族杀;timeout 路径也能整族杀。

	buf := &lockedBuffer{}
	startedAt := time.Now()
	readerDone, err := startWithPipe(cmd, buf) // *os.File 管道:Wait 只等进程退出,不被孤儿管道卡住
	if err != nil {
		return ToolResult{Output: fmt.Sprintf("启动失败: %v", err), Success: false}
	}

	// Wait goroutine:命令进程退出时往 waitErrCh 写一个值。
	// 完成路径由 select 消费;走 auto-bg 接管时由 adoptBackground 接管消费。
	waitErrCh := make(chan error, 1)
	go func() {
		waitErrCh <- cmd.Wait()
	}()

	autoBgTimer := time.NewTimer(autoBackgroundBudget)
	defer autoBgTimer.Stop()
	timeoutTimer := time.NewTimer(time.Duration(timeoutSec) * time.Second)
	defer timeoutTimer.Stop()

	// autoBgCh:首次触发后置 nil,select 上 nil channel 永远阻塞 = 该分支禁用。
	// 用来处理"sleep 等不允许 auto-bg 的命令":自然 fallback 到等 waitErrCh 或 timeout。
	autoBgCh := autoBgTimer.C

	// finishOrAdopt:进程已退出(werr 是其退出结果)后的统一收尾 —— 等输出 goroutine 收尾抓全
	// 残余输出;若 readerDrainGrace 内管道仍未 EOF(有后台子进程占着,典型是命令用 `&` 把 server
	// 丢后台、shell 自己秒退),就切到后台给句柄,而不是当"已完成"返回把那个子进程漏成无主孤儿。
	finishOrAdopt := func(werr error) ToolResult {
		select {
		case <-readerDone:
			return formatForegroundResult(buf.drain(), werr)
		case <-time.After(readerDrainGrace):
			id := adoptBackground(cmd, buf, startedAt, nil, readerDone)
			return backgroundHandoffResult(id, "命令已返回,但仍有子进程在后台占用输出")
		}
	}

	for {
		select {
		case werr := <-waitErrCh:
			// 进程已退出 → 收尾或(有后台子进程时)转后台。这正是 issue #20 的形态。
			return finishOrAdopt(werr)

		case <-autoBgCh:
			// 微观竞态防御:autoBg 跟 wait 完成可能同时 ready,select 随机选一个。
			// 若 wait 已就绪就走完成收尾(同样会在仍有后台子进程时转后台),不当"前台超时"处理。
			select {
			case werr := <-waitErrCh:
				return finishOrAdopt(werr)
			default:
			}
			if !isAutoBackgroundAllowed(command) {
				// 不允许 auto-bg(sleep 等)→ 禁用这一路,继续等 wait 或 timeout
				autoBgCh = nil
				continue
			}
			// 路径 B:接管到 bg,同一个进程继续跑(典型:前台 dev server 不带 `&`,进程一直活着)。
			id := adoptBackground(cmd, buf, startedAt, waitErrCh, readerDone)
			return backgroundHandoffResult(id, fmt.Sprintf("命令前台跑了 %s 仍未退出", autoBackgroundBudget))

		case <-timeoutTimer.C:
			// 路径 C:跑到了硬超时(此时仅可能是 sleep 等不允许 auto-bg 的命令在等)。
			// 杀进程组,等 wait 收尾(避免 goroutine 泄漏),返回当前已有输出 + 超时标记。
			_ = killProc(cmd)
			<-waitErrCh
			out := buf.drain()
			return ToolResult{
				Output:  out + fmt.Sprintf("\n超时(%ds)", timeoutSec),
				Success: false,
			}
		}
	}
}

// formatForegroundResult 把完成态的 cmd 输出 + exit 错误整理成 ToolResult。
// 保持跟历史行为一致:无输出兜底"(无输出)",超 16KB 截断尾部。
func formatForegroundResult(out string, err error) ToolResult {
	if err != nil {
		if out != "" {
			out += "\n"
		}
		return ToolResult{Output: out + fmt.Sprintf("[exit] %v", err), Success: false}
	}
	if out == "" {
		out = "(无输出)"
	}
	const maxOut = 16 * 1024
	if len(out) > maxOut {
		out = out[:maxOut] + "\n... (输出被截断)"
	}
	return ToolResult{Output: out, Success: true}
}

// isAutoBackgroundAllowed 判断命令是否允许在超 autoBackgroundBudget 时自动切后台。
// 当前只排除 `sleep`(对齐 claude-code 的 DISALLOWED_AUTO_BACKGROUND_COMMANDS=['sleep']):
// `sleep N` 本身的语义就是"等 N 秒",切到 bg 反而破坏意图;让它跑到 timeout 才算超时即可。
//
// 取命令首个 token —— 简单可靠,不做 shell parse(那是误伤温床)。
func isAutoBackgroundAllowed(command string) bool {
	s := strings.TrimSpace(command)
	if s == "" {
		return false
	}
	end := len(s)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c == ' ' || c == '\t' || c == ';' || c == '&' || c == '|' {
			end = i
			break
		}
	}
	return s[:end] != "sleep"
}
