//go:build !darwin && !linux

package tools

import "os/exec"

// 其它平台(Windows 等):没有现成的轻量 OS 隔离机制,native 退回软黑名单。
// nativeIsolationAvailable 恒 false → SandboxCheck 走 nativePolicyCheck。

func nativeIsolationAvailable() bool { return false }

// nativeShellCmd 不做 OS 隔离,直接用平台 shell(与 off 模式同走 plainShellCmd)。
func nativeShellCmd(command, cwd string) *exec.Cmd {
	return plainShellCmd(command, cwd)
}
