//go:build windows

package tools

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
)

// Windows 无进程组语义,启动时无需特殊设置。
func setPgid(cmd *exec.Cmd) {}

// killProc 用 taskkill /T 连子进程树一起杀。对"进程已退出"幂等(返回 nil)。
func killProc(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pid := strconv.Itoa(cmd.Process.Pid)
	if err := exec.Command("taskkill", "/T", "/F", "/PID", pid).Run(); err != nil {
		// taskkill 不可用(或进程已没)时退而只杀主进程;进程已退出则视为已达成。
		if kerr := cmd.Process.Kill(); kerr != nil && !errors.Is(kerr, os.ErrProcessDone) {
			return kerr
		}
	}
	return nil
}
