package tui

import (
	"fmt"
	"strings"

	"deepx/tools"

	tea "charm.land/bubbletea/v2"
)

// docker 镜像拉取的 TUI 侧:异步拉取的进度经 dockerPullMsg 流回 Update,驱动对话区进度条。

type dockerPullMsg struct {
	p  tools.PullProgress
	ch <-chan tools.PullProgress
}

// listenDockerPull 读一条拉取进度;channel 关闭则视为结束。每收到一条非结束进度,Update 会再调一次它续听。
func listenDockerPull(ch <-chan tools.PullProgress) tea.Cmd {
	return func() tea.Msg {
		p, ok := <-ch
		if !ok {
			return dockerPullMsg{p: tools.PullProgress{Finished: true}}
		}
		return dockerPullMsg{p: p, ch: ch}
	}
}

// dockerPullBar 渲染一行拉取进度条:🐳 拉取镜像 ubuntu:24.04 [████░░] 50% · 4/8 层。
func dockerPullBar(image string, done, total int) string {
	const width = 16
	pct := 0
	if total > 0 {
		pct = done * 100 / total
	}
	filled := pct * width / 100
	if filled > width {
		filled = width
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	return fmt.Sprintf("🐳 "+T("sandbox.pulling")+" %s  [%s] %d%%  · %d/%d", image, bar, pct, done, total)
}
