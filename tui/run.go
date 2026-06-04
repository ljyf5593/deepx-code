package tui

import (
	"deepx/agent"
	"deepx/tools"
	"deepx/web"
	"os"

	tea "charm.land/bubbletea/v2"
)

// Run 启动 TUI 主循环,直到用户退出或发生错误。
// needsSetup=true 时(磁盘上没 model.yaml),TUI 起来后立即弹配置 modal,
// 用户在 modal 里填 api key,deepx 落盘后无缝转入正常聊天。
// version 是 build 时通过 ldflags 注入的版本号,显示在右栏并用作升级检查的当前版本。
// webEnabled/webPort 控制本地 web dashboard:开启时起一个 127.0.0.1 服务,把同一会话镜像到浏览器。
func Run(models agent.ModelConfig, needsSetup bool, version string, webEnabled bool, webPort int) error {
	// 启动前清空当前终端的 scrollback (\x1b[3J),避免 deepx 启动时 Terminal.app
	// 把之前 shell 的输出留在 alt-screen 的 scrollback 缓冲里,用户拖滚动条会看到。
	// 注意:bubbletea 默认不发这个 escape (它只发 \x1b[2J 清可见区)。
	// 运行中产生的帧 Terminal.app 仍会缓存到自己的 scrollback,无解(同 vim/less)。
	os.Stderr.Write([]byte("\x1b[3J"))

	// web dashboard:能起就起,起不来(端口占用等)静默降级 —— 不影响终端 TUI。
	var hub *web.Hub
	var srv *web.Server
	var webURL string
	if webEnabled {
		wd, _ := os.Getwd()
		hub = web.NewHub(models.Flash.Model, models.Pro.Model, wd, string(CurrentLang()))
		srv = web.NewServer(hub)
		if url, err := srv.Listen(webPort); err == nil {
			webURL = url
		} else {
			// 监听失败 → 关掉 web,TUI 照常跑。
			hub = nil
			srv = nil
		}
	}

	// v2 把 alt-screen / mouse mode 从 Program option 移到了 View 结构体字段。
	// 见 tui/view.go 的 wrapView(): 每帧返回的 tea.View 里设 AltScreen=true + MouseMode。
	p := tea.NewProgram(initialModel(models, needsSetup, version, hub, webURL))

	if srv != nil {
		// 浏览器输入 / review 确认 → program.Send 注入,走和终端完全相同的 Update 逻辑。
		srv.OnInput = func(text string) { p.Send(webInputMsg{text: text}) }
		srv.OnReview = func(approve bool) { p.Send(webReviewMsg{approve: approve}) }
		srv.OnListFiles = func() []string {
			wd, _ := os.Getwd()
			return listWorkspaceFiles(wd)
		}
		go func() { _ = srv.Serve() }()
		defer srv.Close()
	}

	// 退出时清理 docker 沙箱容器(若起过)。native 模式无副作用,这步是 no-op。
	defer tools.StopSandboxContainer()

	_, err := p.Run()
	return err
}
