//go:build linux

package tools

import (
	"context"
	"os/exec"
	"sync"
	"time"
)

// Linux native 隔离:用 bubblewrap(bwrap)。
// 根挂只读、workspace+临时+缓存挂可写、PID/UTS/IPC namespace 隔进程(看不到也杀不到 host 进程)。
// 网络保持开(不 --unshare-net)。bwrap 不在则退回裸 shell(由调用方决定是否套软黑名单)。
//
// 没装 bwrap 的退路:目前直接退回软策略;landlock(内核 ≥5.13)做纯 FS 限制是后续可选项。

var (
	bwrapProbeOnce sync.Once
	bwrapProbeOK   bool
)

// nativeIsolationAvailable 报告本机能否做 native OS 隔离。
// 不只查 bwrap 是否存在,而是**实跑一个极简沙箱确认它真能用**——很多发行版禁用了非特权
// user namespace,bwrap 装了也会在运行时报错。探测一次、整会话缓存。
// 探测失败 → 一致退回软策略(裸 shell + 黑名单,状态面板显示"软策略")。
func nativeIsolationAvailable() bool {
	bwrapProbeOnce.Do(func() {
		if _, err := exec.LookPath("bwrap"); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// 跑一个最小沙箱(含我们实际会用的 --unshare-pid):userns 被禁则此处失败
		err := exec.CommandContext(ctx, "bwrap",
			"--ro-bind", "/", "/", "--proc", "/proc", "--unshare-pid",
			"sh", "-c", ":").Run()
		bwrapProbeOK = err == nil
	})
	return bwrapProbeOK
}

// nativeShellCmd 构造在 bwrap 沙箱里跑命令的 *exec.Cmd;bwrap 不可用则退回裸 shell。
func nativeShellCmd(command, cwd string) *exec.Cmd {
	if !nativeIsolationAvailable() {
		return plainShellCmd(command, cwd)
	}
	args := []string{
		"--ro-bind", "/", "/", // 整个根只读
		"--dev", "/dev", // 干净的 /dev
		"--proc", "/proc", // 配合 PID namespace 的新 /proc
		"--unshare-pid", "--unshare-uts", "--unshare-ipc", // 进程隔离
		"--die-with-parent", // deepx 退出则沙箱进程一起死
	}
	// 可写目录:在只读根之上叠加可写绑定(workspace + 临时 + 缓存)
	for _, p := range nativeWritableRoots(cwd) {
		args = append(args, "--bind", p, p)
	}
	if cwd != "" {
		args = append(args, "--chdir", cwd)
	}
	args = append(args, "sh", "-c", command)
	// cwd 通过 --chdir 传入沙箱;不设 c.Dir(宿主侧),避免与 bind 语义冲突
	return exec.Command("bwrap", args...)
}
