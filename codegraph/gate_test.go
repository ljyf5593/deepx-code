package codegraph

import (
	"os"
	"path/filepath"
	"testing"
)

// 危险根(文件系统根 / home / home 的祖先 / 系统目录)必须被判为 forbidden,绝不建图。
func TestClassifyRootForbidden(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("无法获取 home 目录")
	}
	cases := []string{
		string(filepath.Separator), // 文件系统根
		home,                        // home 本身
		filepath.Dir(home),         // home 的上级(如 /Users、/home)
	}
	for _, root := range cases {
		if forbidden, _, reason := classifyRoot(root); !forbidden {
			t.Errorf("classifyRoot(%q) 应为 forbidden,got forbidden=false reason=%q", root, reason)
		}
	}
}

// 含项目标志的目录:非 forbidden 且 isProject=true(可自动预热)。
func TestClassifyRootProject(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	forbidden, isProject, _ := classifyRoot(dir)
	if forbidden || !isProject {
		t.Fatalf("含 go.mod 的目录应 forbidden=false isProject=true,got %v/%v", forbidden, isProject)
	}
}

// 安全但无项目标志的散目录:非 forbidden,但 isProject=false(不自动预热)。
func TestClassifyRootBareDir(t *testing.T) {
	dir := t.TempDir()
	forbidden, isProject, reason := classifyRoot(dir)
	if forbidden {
		t.Fatalf("普通临时目录不应 forbidden,reason=%q", reason)
	}
	if isProject {
		t.Fatalf("无项目标志的目录不应判为 isProject")
	}
}

// 危险根上建的 Index:状态为 Disabled,Graph 返回空图且从不遍历,Reindex 报错。
func TestForbiddenIndexNeverWalks(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("无法获取 home 目录")
	}
	ix := NewIndex(home)
	if !ix.Disabled() || ix.Status() != StatusDisabled {
		t.Fatalf("home 上的 Index 应禁用,got disabled=%v status=%v", ix.Disabled(), ix.Status())
	}
	g, err := ix.Graph()
	if err != nil {
		t.Fatalf("禁用图谱 Graph 不应报错,got %v", err)
	}
	if len(g.Symbols) != 0 {
		t.Fatalf("禁用图谱应为空,got %d 符号", len(g.Symbols))
	}
	if _, err := ix.Reindex(); err == nil {
		t.Fatalf("禁用图谱 Reindex 应报错")
	}
	// 预热也不应改变禁用状态
	ix.Prewarm()
	if ix.Status() != StatusDisabled {
		t.Fatalf("禁用图谱 Prewarm 后状态应仍为 Disabled,got %v", ix.Status())
	}
}

// 散目录:Prewarm 不自动构建(保持 Idle),但显式 Graph 仍可惰性构建。
func TestBareDirLazyBuild(t *testing.T) {
	dir := t.TempDir()
	ix := NewIndex(dir)
	ix.Prewarm()
	if ix.Status() != StatusIdle {
		t.Fatalf("散目录 Prewarm 后应为 Idle(不自动建),got %v", ix.Status())
	}
	if _, err := ix.Graph(); err != nil { // 显式查询惰性构建
		t.Fatalf("散目录 Graph 应能惰性构建,got %v", err)
	}
}

// 触及文件数预算时,构建应被截断并标记降级(StatusDegraded),而非无限扫描。
func TestBudgetDegrades(t *testing.T) {
	orig := maxIndexFiles
	maxIndexFiles = 3
	defer func() { maxIndexFiles = orig }()

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for i := range 10 {
		src := "package x\nfunc F" + string(rune('a'+i)) + "() {}\n"
		if err := os.WriteFile(filepath.Join(dir, "f"+string(rune('a'+i))+".go"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	ix := NewIndex(dir)
	if _, err := ix.Graph(); err != nil {
		t.Fatal(err)
	}
	if ix.Status() != StatusDegraded {
		t.Fatalf("超预算应标记 Degraded,got %v", ix.Status())
	}
}
