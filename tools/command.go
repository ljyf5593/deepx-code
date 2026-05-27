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
	// 是否走后台由模型按 Bash 工具描述自行判断(描述里已说明常驻进程必须传此参数,
	// 以及为何不能用 shell 的 & / nohup 在前台后台化)。
	if toBool(args["run_in_background"]) {
		return startBackground(command, cwd)
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
