package main

import (
	"deepx/agent"
	"deepx/config"
	"deepx/tui"
	"fmt"
	"io"
	"os"
	"strings"
)

// 由 goreleaser 在 build 时通过 -ldflags "-X main.version=..." 注入。
// dev 构建(直接 go build .)保留默认值,便于在本地区分。
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "--version" || os.Args[1] == "-v" || os.Args[1] == "version") {
		fmt.Printf("deepx %s (commit %s, built %s)\n", version, commit, date)
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "upgrade" {
		if err := tui.RunUpgrade(); err != nil {
			fmt.Fprintln(os.Stderr, "升级失败:", err)
			os.Exit(1)
		}
		return
	}
	// deepx exec [--mode plan|auto] "<任务>" —— 非交互一次性执行,结果打到 stdout,可脚本化 / 管道。
	// 也可从 stdin 接管道输入(cat x | deepx exec "分析这段")。默认 auto(全工具,不弹审批)。
	if len(os.Args) > 1 && os.Args[1] == "exec" {
		runExecAndExit(os.Args[2:])
	}
	cfg, needsSetup, err := loadOrEmptyConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
	if err := tui.Run(toAgentConfig(cfg), needsSetup, version, cfg.WebEnabled(), cfg.WebPort()); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

// loadOrEmptyConfig:
//   - 如果 ~/.deepx/model.yaml 存在 → 读取并返回 (cfg, false, nil)
//   - 不存在 → 返回 (空 cfg, true, nil),TUI 起来后会弹 modal 引导
//
// 读取出错(yaml 格式坏)→ 直接返回 err 让程序退出,不静默吞。
func loadOrEmptyConfig() (*config.Config, bool, error) {
	if !config.Exists() {
		return &config.Config{}, true, nil
	}
	c, err := config.Load()
	if err != nil {
		return nil, false, err
	}
	return c, false, nil
}

// toAgentConfig 把 config.Config 转成 agent.ModelConfig。
// 空 cfg(首次启动)转出来也是空,TUI 在 modal 关闭前不会发起任何 LLM 调用。
func toAgentConfig(c *config.Config) agent.ModelConfig {
	return agent.ModelConfig{
		Flash: agent.ModelEntry(c.Flash),
		Pro:   agent.ModelEntry(c.Pro),
	}
}

// runExecAndExit 解析 `deepx exec` 的参数(args = os.Args[2:]),跑非交互执行,然后退出进程。
// 用法:deepx exec "<任务>";也可从 stdin 管道喂输入。固定 auto 模式、只输出结果,无任何参数。
// 退出码:成功 0;用法错误 2;执行/配置错误 1。
func runExecAndExit(args []string) {
	prompt := strings.TrimSpace(strings.Join(args, " "))

	// stdin 若是管道(非终端),读进来拼到 prompt 后面:prompt 当指令,管道内容当数据。
	if fi, _ := os.Stdin.Stat(); fi != nil && (fi.Mode()&os.ModeCharDevice) == 0 {
		if data, err := io.ReadAll(os.Stdin); err == nil {
			if piped := strings.TrimSpace(string(data)); piped != "" {
				if prompt == "" {
					prompt = piped
				} else {
					prompt = prompt + "\n\n" + piped
				}
			}
		}
	}
	if prompt == "" {
		fmt.Fprintln(os.Stderr, `用法: deepx exec "<任务>"   (也可通过管道喂输入)`)
		os.Exit(2)
	}

	cfg, needsSetup, err := loadOrEmptyConfig()
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}
	ac := toAgentConfig(cfg)
	if needsSetup || (ac.Flash.Model == "" && ac.Pro.Model == "") {
		fmt.Fprintln(os.Stderr, "错误: 尚未配置模型 / API key。先运行 `deepx`(交互式)完成配置,再用 exec。")
		os.Exit(1)
	}

	if err := tui.RunExec(ac, prompt); err != nil {
		fmt.Fprintln(os.Stderr, "\nexec 失败:", err)
		os.Exit(1)
	}
	os.Exit(0)
}
