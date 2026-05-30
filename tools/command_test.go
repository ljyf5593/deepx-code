package tools

import (
	"runtime"
	"strings"
	"testing"
	"time"
)

// 测试用预算:把 autoBackgroundBudget 调到 100ms,这样"长命令"用 sleep 0.5/python sleep 即可触发,
// 不用真等 15s。每个 case 自己 setup/teardown,避免影响其他测试。
func withShortAutoBgBudget(t *testing.T, d time.Duration) {
	t.Helper()
	orig := autoBackgroundBudget
	autoBackgroundBudget = d
	t.Cleanup(func() { autoBackgroundBudget = orig })
}

// 短命令在预算内退出,走原前台路径,正常拿到 stdout。
func TestRunCommand_ForegroundQuickCompletes(t *testing.T) {
	withShortAutoBgBudget(t, 500*time.Millisecond)
	res := RunCommand(map[string]any{"command": "echo hello-deepx"})
	if !res.Success {
		t.Fatalf("应成功,实际 Success=false output=%q", res.Output)
	}
	if !strings.Contains(res.Output, "hello-deepx") {
		t.Errorf("应含 stdout 'hello-deepx',got %q", res.Output)
	}
}

// 命令超 budget 仍在跑 + 允许 auto-bg → 接管到 bg,返回句柄 id + 教育文案。
// 用 `sh -c "while true; do echo tick; sleep 1; done"` 类的长跑命令模拟(但要可控)。
// 这里用一个会跑 3s 的 while 循环,budget 设 100ms。
func TestRunCommand_AutoHandoffToBackground(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 sh 语法构造长命令,Windows 上跳过(语义一致即可)")
	}
	withShortAutoBgBudget(t, 100*time.Millisecond)

	res := RunCommand(map[string]any{
		"command": "for i in 1 2 3 4 5; do echo tick-$i; sleep 0.5; done",
	})
	if !res.Success {
		t.Fatalf("auto-bg 路径应返回 Success=true,实际 false: %q", res.Output)
	}
	if !strings.Contains(res.Output, "自动切到后台") {
		t.Errorf("应含'自动切到后台'文案,got %q", res.Output)
	}
	if !strings.Contains(res.Output, "句柄 id: bash_") {
		t.Errorf("应含分配的句柄 id,got %q", res.Output)
	}
	if !strings.Contains(res.Output, "run_in_background") {
		t.Errorf("应教育模型下次用 run_in_background,got %q", res.Output)
	}

	// 抓出分配的句柄 id,清理后台进程(避免残留拖慢 CI)
	if i := strings.Index(res.Output, "bash_"); i >= 0 {
		end := i
		for end < len(res.Output) && (res.Output[end] >= '0' && res.Output[end] <= '9' || res.Output[end] == '_' || res.Output[end] == 'b' || res.Output[end] == 'a' || res.Output[end] == 's' || res.Output[end] == 'h') {
			end++
		}
		id := res.Output[i:end]
		_ = KillBash(map[string]any{"id": id})
	}
}

// sleep 命令不允许 auto-bg —— budget 不触发切 bg,继续等到 sleep 自己完成。
// 用 sleep 0.3,budget 50ms。budget 触发但被 isAutoBackgroundAllowed 拒绝,
// 最终命令在 ~300ms 后正常完成。
func TestRunCommand_SleepNotAutoBackgrounded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sleep 在 Windows cmd 上语义不同,跳过")
	}
	withShortAutoBgBudget(t, 50*time.Millisecond)

	start := time.Now()
	res := RunCommand(map[string]any{
		"command": "sleep 0.3",
		"timeout": 5,
	})
	elapsed := time.Since(start)
	if !res.Success {
		t.Fatalf("sleep 应正常完成,实际 false: %q", res.Output)
	}
	if strings.Contains(res.Output, "自动切到后台") {
		t.Errorf("sleep 不应被 auto-bg,got %q", res.Output)
	}
	if elapsed < 250*time.Millisecond {
		t.Errorf("应等到 sleep 0.3 自己完成(~300ms),实际只用了 %v", elapsed)
	}
}

// run_in_background=true 明确传 → 走原 startBackground 路径,不受 auto-handoff 干扰。
func TestRunCommand_ExplicitBackgroundUnchanged(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("用 sh 语法,Windows 跳过")
	}
	withShortAutoBgBudget(t, 100*time.Millisecond)

	res := RunCommand(map[string]any{
		"command":           "echo explicit-bg",
		"run_in_background": true,
	})
	if !res.Success {
		t.Fatalf("explicit bg 应成功,got %q", res.Output)
	}
	if !strings.Contains(res.Output, "已在后台启动") {
		t.Errorf("应走原 startBackground 文案,got %q", res.Output)
	}
}

// issue #20 同形:命令尾带 `&` 想"自己后台化",前台路径下 Go 会卡到 shell 派生的
// 子进程仍持 stdout/stderr 管道,管道不关 → wait 不返回。
// 现在的处理:autoBackgroundBudget 到期 → 自动接管到 bg,**不杀进程**,返回句柄。
// 这个测试模拟"shell 后台化常驻进程"的精确语义,确认不会再 60s 卡死。
func TestRunCommand_Issue20_ShellBackgroundedDoesNotHang(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("issue #20 是 Linux/WSL,用 sh 语法构造,Windows 跳过")
	}
	withShortAutoBgBudget(t, 100*time.Millisecond)

	start := time.Now()
	// 后台跑 2s 的子 shell,父 shell 立刻退出但子 shell 继承管道 —— 完美复刻 issue #20 的卡死形态。
	res := RunCommand(map[string]any{
		"command": "(sleep 2; echo finished) &",
		"timeout": 30, // 老行为会卡到这里;新行为应在 ~100ms 切 bg 返回
	})
	elapsed := time.Since(start)

	if !res.Success {
		t.Fatalf("issue #20 形:auto-bg 应救场返回成功,实际 false: %q", res.Output)
	}
	if elapsed > 2*time.Second {
		t.Errorf("不该卡到 timeout,实际用了 %v(应在 ~100ms+ε 切 bg)", elapsed)
	}
	if !strings.Contains(res.Output, "自动切到后台") {
		t.Errorf("应走 auto-handoff 路径,got %q", res.Output)
	}

	// 清理 bg
	if i := strings.Index(res.Output, "bash_"); i >= 0 {
		end := i + len("bash_")
		for end < len(res.Output) && res.Output[end] >= '0' && res.Output[end] <= '9' {
			end++
		}
		_ = KillBash(map[string]any{"id": res.Output[i:end]})
	}
}

// isAutoBackgroundAllowed 单测:取 first token,sleep 拒绝,其他允许。
func TestIsAutoBackgroundAllowed(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"sleep 5", false},              // 显式 sleep
		{"sleep", false},                // 单 sleep
		{"  sleep 30  ", false},         // 前后空白
		{"sleep 5; echo done", false},   // sleep 起手
		{"sleep 5 && echo done", false}, // sleep + &&
		{"sleep 5 & echo done", false},  // sleep + 后台符
		{"python -m http.server 8080", true},
		{"python -m http.server 8080 &", true},
		{"npm run dev", true},
		{"go test ./...", true},
		{"tail -f log", true},
		{"echo sleep", true}, // first token 不是 sleep,允许
		{"", false},          // 空命令本身不允许
	}
	for _, tc := range cases {
		got := isAutoBackgroundAllowed(tc.in)
		if got != tc.want {
			t.Errorf("isAutoBackgroundAllowed(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
