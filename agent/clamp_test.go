package agent

import (
	"strings"
	"testing"
)

// clampMaxTokens:输入小不夹;输入大(但仍 < 窗口)时夹到 input+max 不溢出;maxTokens=0 / 窗口未知 不夹。
func TestClampMaxTokens(t *testing.T) {
	tests := []struct {
		name           string
		maxTokens, ctx int
		repeats        int // "token " 重复次数,造输入量
	}{
		{"输入小-不夹", 1000, 100000, 200},
		{"输入大-要夹", 80000, 100000, 30000}, // 输入 ~45k(<窗口),+80k max 会溢出 → 夹
		{"maxTokens=0-不夹", 0, 100000, 30000},
		{"窗口未知-不夹", 80000, 0, 30000},
	}
	for _, tc := range tests {
		convo := []ChatMessage{{Role: "user", Content: strings.Repeat("token ", tc.repeats)}}
		got := clampMaxTokens(tc.maxTokens, tc.ctx, convo)

		if tc.maxTokens <= 0 || tc.ctx <= 0 {
			if got != tc.maxTokens {
				t.Errorf("%s: 不该夹,got %d want %d", tc.name, got, tc.maxTokens)
			}
			continue
		}
		in := 0
		for _, m := range convo {
			in += MsgTokens(m)
		}
		if got > tc.maxTokens {
			t.Errorf("%s: 夹后超过配置 %d,got %d", tc.name, tc.maxTokens, got)
		}
		// 输入本身没超窗口时,夹后 input + max_tokens 必须落在窗口内(消除 400 溢出)。
		if in < tc.ctx && in+got > tc.ctx {
			t.Errorf("%s: 夹后仍溢出 input(%d)+max(%d)=%d > ctx(%d)", tc.name, in, got, in+got, tc.ctx)
		}
	}
}
