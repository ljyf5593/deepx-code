//go:build windows

package tools

import (
	"os/exec"
	"strconv"
)

// Windows 无进程组语义,启动时无需特殊设置。
func setPgid(cmd *exec.Cmd) {}

// killProc 用 taskkill /T 连子进程树一起杀。
func killProc(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	if err := exec.Command("taskkill", "/T", "/F", "/PID", pid).Run(); err != nil {
		return cmd.Process.Kill() // taskkill 不可用时退而只杀主进程
	}
	return nil
}
