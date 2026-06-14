package tui

import (
	"testing"
)

func TestDetectGraphemeMode(t *testing.T) {
	// 清掉可能干扰判定的终端 env,逐个用例显式设置。
	envKeys := []string{"TERM_PROGRAM", "KITTY_WINDOW_ID", "ALACRITTY_LOG", "ALACRITTY_WINDOW_ID", "WT_SESSION", "VTE_VERSION", "KONSOLE_VERSION"}
	for _, k := range envKeys {
		t.Setenv(k, "")
	}

	cases := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{"vscode → grapheme", map[string]string{"TERM_PROGRAM": "vscode"}, true},
		{"AppleTerminal → grapheme", map[string]string{"TERM_PROGRAM": "Apple_Terminal"}, true},
		{"WezTerm → grapheme", map[string]string{"TERM_PROGRAM": "WezTerm"}, true},
		{"WindowsTerminal → grapheme", map[string]string{"WT_SESSION": "some-guid"}, true},
		{"GNOME/VTE → grapheme", map[string]string{"VTE_VERSION": "7200"}, true},
		{"Konsole → grapheme", map[string]string{"KONSOLE_VERSION": "220400"}, true},
		// conhost / 传统 PowerShell:无任何特征 env → 默认 wcwidth(issue #113)。
		{"plain(conhost/PowerShell) → wcwidth", map[string]string{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, k := range envKeys {
				t.Setenv(k, tc.env[k])
			}
			if got := detectGraphemeMode(); got != tc.want {
				t.Fatalf("detectGraphemeMode() = %v, want %v", got, tc.want)
			}
		})
	}
}
