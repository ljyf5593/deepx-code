//go:build darwin

package tui

import (
	"os"
	"os/exec"
	"strings"
)

// writeClipboardText 用 macOS 的 pbcopy 把文本写到系统剪贴板。
// pbcopy 是系统自带,无外部依赖。失败时返回 err,调用方静默忽略即可
// (复制失败不是关键路径,用户可以重新选)。
//
// 注意:pbcopy 按环境里的 locale 解释 stdin 的字符编码。被 GUI(如 VS Code)拉起时
// LANG/LC_* 常缺失或非 UTF-8,pbcopy 会把我们喂的 UTF-8 文本当 MacRoman/Latin-1 解码,
// 粘贴出来就是乱码。所以强制给子进程一个 UTF-8 locale。
func writeClipboardText(text string) error {
	cmd := exec.Command("pbcopy")
	cmd.Stdin = strings.NewReader(text)
	cmd.Env = utf8LocaleEnv()
	return cmd.Run()
}

// utf8LocaleEnv 复制当前环境,但丢弃已有的 locale 变量并统一覆盖为 UTF-8。
// 必须先丢弃:环境里出现重复 key 时,getenv 取的是靠前那个,直接 append 覆盖不了。
func utf8LocaleEnv() []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+2)
	for _, kv := range base {
		if strings.HasPrefix(kv, "LC_ALL=") ||
			strings.HasPrefix(kv, "LC_CTYPE=") ||
			strings.HasPrefix(kv, "LANG=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "LANG=en_US.UTF-8", "LC_CTYPE=UTF-8")
}
