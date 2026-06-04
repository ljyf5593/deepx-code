//go:build darwin

package tools

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// macOS native 隔离:用系统自带的 sandbox-exec(Seatbelt)。
// profile = 默认放行 → 禁止一切文件写 → 再放行 workspace + 临时 + 工具缓存。读和网络不限。
//
// 进程隔离:macOS 没有 PID namespace,给不出独立进程视图;能加的 deny signal/process-exec 一旦开
// 容易误杀正常子进程管理,得不偿失。所以 **mac 上只做文件写禁闭(实打实),进程隔离不强行做**。
// 要真正的进程隔离用 Linux(bwrap PID ns)或 docker。
//
// 注:sandbox-exec 被 Apple 标记 deprecated,但至今可用、也是系统级沙箱的同一套机制。

var (
	sbxProbeOnce sync.Once
	sbxProbeOK   bool
)

// nativeIsolationAvailable 报告本机能否做 native OS 隔离。
// 不只查 sandbox-exec 是否存在,而是**实跑一个极简 profile 确认它真能用**——
// 应对 Apple 移除/禁用、环境受限等"二进制在但跑不起来"的情况。探测一次、整会话缓存。
// 探测失败 → 一致退回软策略(nativeShellCmd 走裸 shell,SandboxCheck 走黑名单,状态面板显示"软策略")。
func nativeIsolationAvailable() bool {
	sbxProbeOnce.Do(func() {
		if _, err := exec.LookPath("sandbox-exec"); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		// 极简 profile + /usr/bin/true:成功即说明 Seatbelt 基础设施可用
		err := exec.CommandContext(ctx, "sandbox-exec", "-p", "(version 1)(allow default)", "/usr/bin/true").Run()
		sbxProbeOK = err == nil
	})
	return sbxProbeOK
}

// nativeShellCmd 构造在 Seatbelt 沙箱里跑命令的 *exec.Cmd;sandbox-exec 不可用则退回裸 shell。
func nativeShellCmd(command, cwd string) *exec.Cmd {
	if !nativeIsolationAvailable() {
		return plainShellCmd(command, cwd)
	}
	profile := seatbeltProfile(nativeWritableRoots(cwd))
	c := exec.Command("sandbox-exec", "-p", profile, "sh", "-c", command)
	if cwd != "" {
		c.Dir = cwd
	}
	return c
}

// seatbeltProfile 生成 SBPL:允许默认 → 禁所有写 → 放行给定可写目录 + 标准流/设备。
func seatbeltProfile(writable []string) string {
	var b strings.Builder
	b.WriteString("(version 1)\n(allow default)\n(deny file-write*)\n(allow file-write*\n")
	for _, p := range writable {
		b.WriteString("  (subpath ")
		b.WriteString(sbplQuote(p))
		b.WriteString(")\n")
	}
	for _, lit := range []string{"/dev/null", "/dev/stdout", "/dev/stderr", "/dev/tty", "/dev/dtracehelper", "/dev/random", "/dev/urandom"} {
		b.WriteString("  (literal ")
		b.WriteString(sbplQuote(lit))
		b.WriteString(")\n")
	}
	b.WriteString("  (subpath ")
	b.WriteString(sbplQuote("/dev/fd"))
	b.WriteString("))\n")
	return b.String()
}

// sbplQuote 把字符串安全放进 SBPL 字面量(转义反斜杠与双引号)。
func sbplQuote(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}
