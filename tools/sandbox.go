package tools

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync/atomic"
)

// 沙箱:约束工具对系统的影响。两种模式(无 off,始终在):
//   - native(默认):用 OS 机制做**文件写禁闭 + 进程隔离**——macOS 用 Seatbelt(sandbox-exec)、
//     Linux 用 bubblewrap(bwrap):命令只能写 workspace + 临时/缓存目录,host 其余只读;读和网络不限。
//     这是 OS 强制的真边界(Bash 也越不出去)。详见 sandbox_native*.go。
//     无可用 OS 机制的平台(Windows 等)退回软黑名单(nativePolicyCheck)兜底,仅防明显危险命令。
//   - docker:Bash 命令在容器里跑(完整隔离,跑不可信代码用)。
//
// 此外,不分模式:deepx 自己的写文件工具(Write/Edit)经 confineToWorkspace 强制只写 workspace 内
// (落盘由 Go 亲手做,任何平台都生效)。
//
// 切换:TUI 的 /sandbox 命令;Bash 执行前在 RunCommand 里过 SandboxCheck,执行时由 sandboxCmd 套隔离。

type SandboxMode string

const (
	SandboxOff    SandboxMode = "off"    // 完全关闭:命令不套隔离/不查黑名单,Write/Edit 也不限 workspace
	SandboxNative SandboxMode = "native" // 默认:OS 隔离(Seatbelt/bwrap),不可用平台退软黑名单
	SandboxDocker SandboxMode = "docker" // 容器隔离
)

// sandboxMode 当前模式(进程内,默认 native)。原子存取,TUI 改 / 执行读。
var sandboxMode atomic.Value // SandboxMode

func init() { sandboxMode.Store(SandboxNative) }

// SetSandboxMode 设置当前沙箱模式。
func SetSandboxMode(m SandboxMode) { sandboxMode.Store(m) }

// CurrentSandboxMode 返回当前沙箱模式(默认 native)。
func CurrentSandboxMode() SandboxMode {
	if v, ok := sandboxMode.Load().(SandboxMode); ok && v != "" {
		return v
	}
	return SandboxNative
}

// dangerousRule 是 native 黑名单的一条:命中 re 即拒,reason 给用户看。
type dangerousRule struct {
	re     *regexp.Regexp
	reason string
}

// dangerousRules 是 native 软策略的黑名单(放行其余,不影响正常 build/test/git/grep)。
// (?i) 大小写不敏感以兼容各 shell / Windows;best-effort,可被混淆绕过(那是 docker 的活)。
// 覆盖 unix + windows 的明显作死命令。
var dangerousRules = []dangerousRule{
	{regexp.MustCompile(`(?i)(^|[\s;&|(])sudo(\s|$)`), "提权(sudo)"},
	{regexp.MustCompile(`(?i)(^|[\s;&|(])su(\s|$)`), "切换用户(su)"},
	{regexp.MustCompile(`(?i)\brm\s+-[a-z]*[rf][a-z]*\s+(/|~|\$HOME)(\s|/|\*|$)`), "删除根/家目录(rm -rf /|~)"},
	{regexp.MustCompile(`(?i)\bmkfs\b`), "格式化文件系统(mkfs)"},
	{regexp.MustCompile(`(?i)\bdd\b[^\n]*\bof=/dev/`), "写裸设备(dd of=/dev/…)"},
	{regexp.MustCompile(`(?i)>\s*/dev/(sd|nvme|hd|disk)`), "重定向到磁盘设备"},
	{regexp.MustCompile(`(?i)(^|[\s;&|])(shutdown|reboot|halt|poweroff)(\s|$)`), "关机/重启"},
	{regexp.MustCompile(`(?i)(curl|wget)\b[^\n|]*\|\s*(sudo\s+)?(ba|z|da|c|k)?sh(\s|$)`), "远程管道进 shell(供应链风险:curl … | sh)"},
	{regexp.MustCompile(`:\s*\(\s*\)\s*\{[^}]*:\s*\|\s*:`), "fork 炸弹"},
	{regexp.MustCompile(`(?i)\bchmod\s+-[a-z]*r[a-z]*\s+777\s+/(\s|$)`), "递归 777 根目录"},
	// Windows
	{regexp.MustCompile(`(?i)\bformat\s+[a-z]:`), "格式化磁盘(format)"},
	{regexp.MustCompile(`(?i)\b(del|erase)\b[^\n]*\s/[sq][^\n]*\s+[a-z]:\\`), "递归强删盘根(del /s /q)"},
	{regexp.MustCompile(`(?i)\b(rd|rmdir)\b[^\n]*\s/s[^\n]*\s+[a-z]:\\`), "递归删目录(rd /s)"},
}

// SandboxCheck 在执行命令前按当前沙箱模式做预检。返回非 nil 即拒绝执行。
func SandboxCheck(command string) error {
	switch CurrentSandboxMode() {
	case SandboxOff, SandboxDocker:
		return nil // off:不设防;docker:容器即边界
	default: // native
		if nativeIsolationAvailable() {
			return nil // OS 级隔离(Seatbelt/bwrap)即边界,不再套黑名单
		}
		return nativePolicyCheck(command) // 无 OS 隔离的平台,退回软黑名单兜底
	}
}

// NativeIsolationActive 报告 native 模式下是否启用了 OS 级隔离(供 UI 显示保护级别)。
func NativeIsolationActive() bool { return nativeIsolationAvailable() }

// sandboxCmd 按当前沙箱模式构造可执行的 *exec.Cmd:
// off→裸 shell(不隔离);native→OS 隔离(Seatbelt/bwrap,不可用则裸 shell);docker→容器内 exec。
// 前台(command.go)与后台(bg.go)都用它。
func sandboxCmd(command, cwd string) (*exec.Cmd, error) {
	switch CurrentSandboxMode() {
	case SandboxDocker:
		return dockerExecCmd(command, cwd)
	case SandboxOff:
		return plainShellCmd(command, cwd), nil
	default: // native
		return nativeShellCmd(command, cwd), nil
	}
}

// plainShellCmd 不套任何隔离,只按平台选 shell。off 模式 + 各平台 native 的"无 OS 隔离"退化路径共用。
func plainShellCmd(command, cwd string) *exec.Cmd {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd", "/C", command)
	} else {
		cmd = exec.Command("sh", "-c", command)
	}
	if cwd != "" {
		cmd.Dir = cwd
	}
	return cmd
}

// confineToWorkspace 把 path 解析为绝对路径,并要求它落在 workspace 内,否则拒绝。
// 这是 deepx 写文件工具(Write/Edit)能"真正强制"的边界:落盘由 deepx 自己在 Go 里做,
// 不像 Bash 命令只能看字符串。两种沙箱模式下都生效(文件工具始终写宿主,理应锁在 workspace)。
// workspace 未注入时(启动时必注入,极少触发)退化为放行,不阻断正常使用。
func confineToWorkspace(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("路径错误: %v", err)
	}
	if CurrentSandboxMode() == SandboxOff {
		return abs, nil // off:沙箱完全关闭,Write/Edit 不限制 workspace
	}
	ws := sbWorkspace.Load()
	if ws == "" {
		return abs, nil
	}
	rel, err := filepath.Rel(ws, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("拒绝写入 workspace 外的文件:%s(沙箱只允许修改 workspace 内的文件)", abs)
	}
	return abs, nil
}

// nativePolicyCheck 跑 native 黑名单。命中返回原因;否则放行。
func nativePolicyCheck(command string) error {
	for _, r := range dangerousRules {
		if r.re.MatchString(command) {
			return fmt.Errorf("native 沙箱拒绝执行 —— %s。如确需运行不可信命令,请切到 docker 沙箱(/sandbox docker)", r.reason)
		}
	}
	return nil
}
