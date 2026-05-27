package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// looksLikeBackgrounding 判断命令里是否含一个用作"后台运行"的 `&`(控制操作符),
// 同时排除三类含 `&` 但不是后台化的语法:
//   - `&&` 逻辑与
//   - 重定向中的 `&`:`2>&1`、`>&2`、`&>file`(与 `>` 相邻)
//
// 命中的典型形态:`./server &`、`./server & sleep 2`、`nohup x &`。
func looksLikeBackgrounding(command string) bool {
	b := []byte(command)
	for i := 0; i < len(b); i++ {
		if b[i] != '&' {
			continue
		}
		if i+1 < len(b) && b[i+1] == '&' { // `&&` 逻辑与:跳过这一对
			i++
			continue
		}
		if i > 0 && b[i-1] == '&' { // `&&` 的第二个 &
			continue
		}
		if (i > 0 && b[i-1] == '>') || (i+1 < len(b) && b[i+1] == '>') { // 重定向 >& / &>
			continue
		}
		return true // 余下的 & = 后台控制操作符
	}
	return false
}

// RunCommand 执行 shell 命令并返回输出。
// 参数:
//
//	command (string) 要执行的命令
//	cwd     (string, 可选) 工作目录
//	timeout (int,    可选) 超时秒数，默认 60
func RunCommand(args map[string]any) ToolResult {
	command, _ := args["command"].(string)
	if strings.TrimSpace(command) == "" {
		return ToolResult{Output: "错误: command 参数为空", Success: false}
	}
	cwd, _ := args["cwd"].(string)

	// 常驻进程(dev server / watch / daemon)走后台:立即返回句柄,不阻塞 agent。
	if toBool(args["run_in_background"]) {
		return startBackground(command, cwd)
	}

	// 护栏:模型用 shell 的 `&` / nohup 自行后台化常驻进程,在前台路径下不但救不了,反而会
	// 卡死到超时(Go 要等子进程继承的 stdout/stderr 管道关闭,而后台进程一直攥着它)。
	// 直接拦下,引导改用 run_in_background。
	if looksLikeBackgrounding(command) {
		return ToolResult{
			Output: "检测到命令里用 shell 的 `&`(或 nohup)做后台化。这在本工具的前台模式下行不通:" +
				"Go 会一直等子进程继承的 stdout/stderr 关闭,后台进程不退出就卡死到超时,还会留下孤儿进程。\n" +
				"如果你要启动常驻进程(dev server / watch / daemon),改调本工具并传 `run_in_background: true`," +
				"会立即返回一个句柄 id,再用 BashOutput(id) 读输出/探活、KillBash(id) 收尾。\n" +
				"如果只是想并行几条会很快结束的命令,去掉 `&` 改成顺序执行即可。",
			Success: false,
		}
	}

	timeout := toInt(args["timeout"], 60)
	if timeout <= 0 {
		timeout = 60
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeout)*time.Second)
	defer cancel()

	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "cmd", "/C", command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", command)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	out := stdout.String()
	if errOut := stderr.String(); errOut != "" {
		if out != "" {
			out += "\n"
		}
		out += "[stderr]\n" + errOut
	}

	if ctx.Err() == context.DeadlineExceeded {
		return ToolResult{Output: out + fmt.Sprintf("\n超时（%ds）", timeout), Success: false}
	}
	if err != nil {
		return ToolResult{Output: out + fmt.Sprintf("\n[exit] %v", err), Success: false}
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
