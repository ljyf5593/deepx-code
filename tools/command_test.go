package tools

import (
	"strings"
	"testing"
)

func TestLooksLikeBackgrounding(t *testing.T) {
	bg := []string{
		"./server &",
		"./server & sleep 2",            // & 在中间(本次故障形态)
		"nohup ./app &",
		"npm run dev & echo started",
		"  ./server  &  ",
	}
	for _, c := range bg {
		if !looksLikeBackgrounding(c) {
			t.Errorf("应判为后台化: %q", c)
		}
	}

	notBg := []string{
		"go build ./... && go test ./...", // 逻辑与
		"make && ./run-once",
		"echo hi 2>&1",                    // 重定向
		"./tool >out.log 2>&1",
		"cmd &>combined.log",              // bash 合并重定向
		"git log --oneline",
		"grep -r foo .",
	}
	for _, c := range notBg {
		if looksLikeBackgrounding(c) {
			t.Errorf("不应判为后台化: %q", c)
		}
	}
}

// 前台路径遇到后台化命令应被拦下并引导用 run_in_background。
func TestRunCommandBlocksBackgrounding(t *testing.T) {
	res := RunCommand(map[string]any{"command": "./server & sleep 2; echo done"})
	if res.Success {
		t.Fatal("后台化命令在前台路径应被拦下")
	}
	if !strings.Contains(res.Output, "run_in_background") {
		t.Fatalf("拦截提示应引导 run_in_background,got: %q", res.Output)
	}
}
