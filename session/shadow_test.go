package session

import (
	"os"
	"path/filepath"
	"testing"
)

// 影子热压存取:Save→Load 往返;Clear 归零;与 summary/prefix 等其它 state 字段互不踩。
func TestShadowSaveLoadClear(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	t.Setenv("USERPROFILE", tmp)

	ws := filepath.Join(tmp, "proj")
	if err := os.MkdirAll(ws, 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := New(ws)
	if err != nil {
		t.Fatal(err)
	}

	// 缺省:无影子
	if cp, cut := m.LoadShadow(); cp != "" || cut != 0 {
		t.Fatalf("初始应无影子,得到 cp=%q cut=%d", cp, cut)
	}

	// 同时写一份 summary,验证影子不踩它(都读改写 state.json,且 summary 在裸文件)。
	_ = m.SaveSummary("旧摘要")
	m.SaveShadow("## 任务目标\n实现影子热压", 42)

	if cp, cut := m.LoadShadow(); cp != "## 任务目标\n实现影子热压" || cut != 42 {
		t.Fatalf("Save→Load 往返失败:cp=%q cut=%d", cp, cut)
	}
	if got := m.LoadSummary(); got != "旧摘要" {
		t.Errorf("影子写入不应覆盖 summary,得到 %q", got)
	}

	// 新覆盖旧
	m.SaveShadow("## 任务目标\n新内容", 99)
	if cp, cut := m.LoadShadow(); cp != "## 任务目标\n新内容" || cut != 99 {
		t.Fatalf("覆盖失败:cp=%q cut=%d", cp, cut)
	}

	// Clear 归零,但 summary 仍在
	m.ClearShadow()
	if cp, cut := m.LoadShadow(); cp != "" || cut != 0 {
		t.Fatalf("Clear 后应无影子,得到 cp=%q cut=%d", cp, cut)
	}
	if got := m.LoadSummary(); got != "旧摘要" {
		t.Errorf("Clear 影子不应动 summary,得到 %q", got)
	}

	// 重新打开同一会话,影子(已清)仍为空,summary 仍持久
	m2, err := New(ws)
	if err != nil {
		t.Fatal(err)
	}
	if cp, cut := m2.LoadShadow(); cp != "" || cut != 0 {
		t.Errorf("重开后影子应为空,得到 cp=%q cut=%d", cp, cut)
	}
	if got := m2.LoadSummary(); got != "旧摘要" {
		t.Errorf("重开后 summary 应持久,得到 %q", got)
	}
}
