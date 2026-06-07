//go:build linux

package tools

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/landlock-lsm/go-landlock/landlock"
	llsys "github.com/landlock-lsm/go-landlock/landlock/syscall"
)

// Linux native 隔离,三级择优:
//   1. bubblewrap(bwrap):根只读 + 可写目录叠加 + PID/UTS/IPC namespace 隔进程。最强。
//   2. Landlock(内核 ≥5.13):纯文件写禁闭(无进程隔离),按路径授权、不改文件标签。无需装任何东西。
//   3. 都没有 → 退软黑名单(由 SandboxCheck 调 nativePolicyCheck)。
// 网络始终开(否则 go mod / npm / git fetch 全断)。读不限,只禁"写到 workspace 外"。
//
// Landlock 的限制一旦施加于某进程便不可逆,所以不能加在长驻的 deepx 上,只能加在"将要执行命令的那个
// 进程"里。做法是 re-exec 跳板:nativeShellCmd 让命令以「deepx 自身 + 一组 env 标记」启动,启动后的
// deepx 在 main() 最早处(RunSandboxTrampolineIfRequested)识别标记 → 施加 Landlock → exec 真正的
// sh -c <命令>。Landlock 限制随 execve 保留,从而约束到命令本身及其子进程。

const (
	sbxTrampolineEnv = "DEEPX_SBX_LANDLOCK" // =1 标记本进程是 Landlock 跳板
	sbxWritableEnv   = "DEEPX_SBX_WRITABLE" // 可写根列表(PathListSeparator 分隔)
	sbxCmdEnv        = "DEEPX_SBX_CMD"       // 要执行的 shell 命令
	sbxCwdEnv        = "DEEPX_SBX_CWD"       // 工作目录
)

var (
	bwrapProbeOnce sync.Once
	bwrapProbeOK   bool
	llProbeOnce    sync.Once
	llProbeOK      bool
)

// bwrapAvailable 实跑一个极简 bwrap 沙箱确认真能用(很多发行版禁用非特权 userns,装了也运行时报错)。
func bwrapAvailable() bool {
	bwrapProbeOnce.Do(func() {
		if _, err := exec.LookPath("bwrap"); err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := exec.CommandContext(ctx, "bwrap",
			"--ro-bind", "/", "/", "--proc", "/proc", "--unshare-pid",
			"sh", "-c", ":").Run()
		bwrapProbeOK = err == nil
	})
	return bwrapProbeOK
}

// landlockAvailable 仅查询内核 Landlock ABI 版本(纯探测,不施加任何限制)。≥1 即支持。
func landlockAvailable() bool {
	llProbeOnce.Do(func() {
		if v, err := llsys.LandlockGetABIVersion(); err == nil && v >= 1 {
			llProbeOK = true
		}
	})
	return llProbeOK
}

// nativeIsolationAvailable 报告本机能否做 native OS 隔离(bwrap 或 Landlock 任一)。
// 都没有 → false,SandboxCheck 退软黑名单。探测各缓存一次。
func nativeIsolationAvailable() bool {
	return bwrapAvailable() || landlockAvailable()
}

// nativeShellCmd 按优先级构造隔离命令:bwrap > Landlock > 裸 shell。
func nativeShellCmd(command, cwd string) *exec.Cmd {
	if bwrapAvailable() {
		return bwrapShellCmd(command, cwd)
	}
	if landlockAvailable() {
		if c := landlockShellCmd(command, cwd); c != nil {
			return c
		}
	}
	return plainShellCmd(command, cwd)
}

// bwrapShellCmd 构造在 bwrap 沙箱里跑命令的 *exec.Cmd。
func bwrapShellCmd(command, cwd string) *exec.Cmd {
	args := []string{
		"--ro-bind", "/", "/", // 整个根只读
		"--dev", "/dev", // 干净的 /dev
		"--proc", "/proc", // 配合 PID namespace 的新 /proc
		"--unshare-pid", "--unshare-uts", "--unshare-ipc", // 进程隔离
		"--die-with-parent", // deepx 退出则沙箱进程一起死
	}
	// 可写目录:在只读根之上叠加可写绑定。用 --bind-try 而非 --bind:候选含 macOS 专属路径
	// (/private/tmp、~/Library/Caches),Linux 上不存在,普通 --bind 绑不存在的 source 会致命报错。
	for _, p := range nativeWritableRoots(cwd) {
		args = append(args, "--bind-try", p, p)
	}
	if cwd != "" {
		args = append(args, "--chdir", cwd)
	}
	args = append(args, "sh", "-c", command)
	return exec.Command("bwrap", args...)
}

// landlockShellCmd 以 deepx 自身作 re-exec 跳板,带 env 标记启动;跳板进程负责施加 Landlock 再 exec 真命令。
func landlockShellCmd(command, cwd string) *exec.Cmd {
	exe, err := os.Executable()
	if err != nil {
		return nil // 拿不到自身路径就放弃 Landlock,退裸 shell
	}
	c := exec.Command(exe)
	c.Env = append(os.Environ(),
		sbxTrampolineEnv+"=1",
		sbxWritableEnv+"="+strings.Join(nativeWritableRoots(cwd), string(os.PathListSeparator)),
		sbxCmdEnv+"="+command,
		sbxCwdEnv+"="+cwd,
	)
	return c
}

// RunSandboxTrampolineIfRequested 必须在 main() 最早处调用。
// 若本进程带 Landlock 跳板标记:施加"读全局 / 只写可写根"的 Landlock 写禁闭,然后 exec sh -c <命令>,
// 永不返回。否则立即返回,deepx 正常启动。
func RunSandboxTrampolineIfRequested() {
	if os.Getenv(sbxTrampolineEnv) != "1" {
		return
	}
	cwd := os.Getenv(sbxCwdEnv)
	command := os.Getenv(sbxCmdEnv)
	var roots []string
	if w := os.Getenv(sbxWritableEnv); w != "" {
		roots = filepath.SplitList(w)
	}
	if cwd != "" {
		_ = os.Chdir(cwd)
	}

	// 读全局(RODirs 含执行权限,能跑二进制),只写可写根。缓存目录可能不存在 → IgnoreIfMissing。
	rules := []landlock.Rule{landlock.RODirs("/")}
	for _, r := range roots {
		if r != "" {
			rules = append(rules, landlock.RWDirs(r).IgnoreIfMissing())
		}
	}
	// BestEffort:内核版本不够则尽力而为,绝不因 Landlock 失败而拒跑命令(最坏退化为不隔离)。
	_ = landlock.V5.BestEffort().RestrictPaths(rules...)

	// 清掉跳板自用的 env,避免泄漏给子命令(否则子命令里再起 deepx 会被误判为跳板)。
	env := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, sbxTrampolineEnv+"=") || strings.HasPrefix(kv, sbxWritableEnv+"=") ||
			strings.HasPrefix(kv, sbxCmdEnv+"=") || strings.HasPrefix(kv, sbxCwdEnv+"=") {
			continue
		}
		env = append(env, kv)
	}

	sh, err := exec.LookPath("sh")
	if err != nil {
		sh = "/bin/sh"
	}
	// exec 替换当前进程映像;Landlock 域随 execve 保留 → 真正约束 sh 及其后代。
	_ = syscall.Exec(sh, []string{"sh", "-c", command}, env)
	os.Exit(127) // 只有 exec 失败才会走到这
}
