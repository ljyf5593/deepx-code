//go:build !windows

package tools

import (
	"os/exec"
	"syscall"
)

// setPgid 让子进程自成一个进程组,这样 killProc 能用负 PID 把整棵进程树一起杀掉
// —— 否则 `sh -c "npx vite"` 派生出的 node 子进程会成孤儿继续占着端口。
func setPgid(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killProc 杀掉子进程所在的整个进程组。
func killProc(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == nil {
		return syscall.Kill(-pgid, syscall.SIGKILL) // 负号 = 整组
	}
	return cmd.Process.Kill() // 拿不到进程组就退而求其次只杀主进程
}
