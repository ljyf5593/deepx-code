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

// killProc 杀掉子进程所在的整个进程组。对"进程已退出"幂等(返回 nil),
// 这样 KillBash 在进程刚好自行退出的竞态下也不会报错。
func killProc(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	pgid, err := syscall.Getpgid(cmd.Process.Pid)
	if err == syscall.ESRCH { // 进程已不存在(已退出/被 reap)→ 目的已达成
		return nil
	}
	if err != nil {
		return cmd.Process.Kill() // 其它异常,退而只杀主进程
	}
	if kerr := syscall.Kill(-pgid, syscall.SIGKILL); kerr != nil && kerr != syscall.ESRCH {
		return kerr // 负号 = 整组;组已消失(ESRCH)同样视为已达成
	}
	return nil
}
