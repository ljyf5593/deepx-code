package codegraph

import (
	"os"
	"path/filepath"
	"testing"
)

// issue #115:goCorpusStats 只统计会被精确解析的模块 Go 源码,跳过 .git/vendor 等,
// 作为精确 pass 体量门控的输入。
func TestGoCorpusStats_SkipsHeavyDirs(t *testing.T) {
	root := t.TempDir()
	mk := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("a.go", "package x\n")              // 计入
	mk("sub/b.go", "package y\n")          // 计入
	mk(".git/c.go", "package z\n")         // 跳过(.git)
	mk("vendor/d.go", "package v\n")       // 跳过(vendor)
	mk("node_modules/e.go", "package n\n") // 跳过(node_modules)
	mk("readme.md", "hi")                  // 非 .go 不计

	ix := &Index{root: root}
	files, bytes := ix.goCorpusStats()
	if files != 2 {
		t.Fatalf("应只数 2 个模块内 .go(跳过 .git/vendor/node_modules),got %d", files)
	}
	if bytes <= 0 {
		t.Fatalf("bytes 应 > 0, got %d", bytes)
	}
}

func TestHasOverlongLine(t *testing.T) {
	short := []byte("package x\nfunc f() {}\n")
	if hasOverlongLine(short, 40<<10) {
		t.Error("正常源码不该判定为超长行")
	}
	// 一个 100KB 的单行(模拟压缩/生成文件)
	big := make([]byte, 100<<10)
	for i := range big {
		big[i] = 'a'
	}
	if !hasOverlongLine(big, 40<<10) {
		t.Error("100KB 单行应判定为超长行")
	}
	// 末行无换行也要检测
	if !hasOverlongLine(append([]byte("ok\n"), big...), 40<<10) {
		t.Error("末行超长(无结尾换行)应被检测")
	}
}

func TestShouldSkipDir_CaseInsensitive(t *testing.T) {
	// issue #115:Windows/macOS 大小写不敏感,Vendor/VENDOR/Node_Modules 也必须跳过。
	for _, n := range []string{"vendor", "Vendor", "VENDOR", "node_modules", "Node_Modules", ".git", ".Git", "Pods", "pods"} {
		if !shouldSkipDir(n) {
			t.Errorf("%q 应被跳过", n)
		}
	}
	// 正常源码目录不跳过。
	for _, n := range []string{"agent", "src", "internal", "vendors", "buildscripts"} {
		if shouldSkipDir(n) {
			t.Errorf("%q 不该被跳过", n)
		}
	}
}
