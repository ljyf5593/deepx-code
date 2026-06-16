package codegraph

import (
	"bytes"
	"encoding/gob"
	"os"
	"path/filepath"
	"testing"
)

// 子进程方案的核心风险:Graph 索引是未导出 map,gob 不传,靠父进程重放 add* 重建。
// 这里验证:建图 → 取原始 Symbols/Refs/Edges → gob 往返 → rebuildFromRaw → 查询结果一致。
func TestGraphGobRoundTrip(t *testing.T) {
	root := t.TempDir()
	mk := func(name, content string) {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mk("go.mod", "module demo\n\ngo 1.22\n")
	mk("a.go", "package demo\n\nfunc Alpha() { Beta() }\nfunc Beta() {}\n")

	ix := &Index{root: root}
	g, _, err := ix.assemble(nil, false)
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	if len(g.Symbols) == 0 {
		t.Fatal("原图应有符号")
	}

	// gob 往返
	var buf bytes.Buffer
	if err := gob.NewEncoder(&buf).Encode(buildEnvelope{
		Symbols: g.Symbols, Refs: g.RawRefs, Edges: g.RawEdges,
	}); err != nil {
		t.Fatalf("encode: %v", err)
	}
	var env buildEnvelope
	if err := gob.NewDecoder(&buf).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	rebuilt := rebuildFromRaw(env.Symbols, env.Refs, env.Edges)

	// 符号数一致
	if len(rebuilt.Symbols) != len(g.Symbols) {
		t.Fatalf("符号数不一致: %d vs %d", len(rebuilt.Symbols), len(g.Symbols))
	}
	// 索引查询一致:Alpha 应能查到定义;且重建图的 Def 与原图一致
	if len(rebuilt.Def("Alpha")) != len(g.Def("Alpha")) {
		t.Fatalf("Def(Alpha) 重建后不一致")
	}
	if len(g.Def("Alpha")) == 0 {
		t.Fatal("原图 Def(Alpha) 不该为空(索引重放才有意义)")
	}
}

// 子进程入口冒烟:BuildAndEncode 应产出可解码的 envelope(顺带覆盖看门狗启动不报错)。
func TestBuildAndEncodeSmoke(t *testing.T) {
	root := t.TempDir()
	_ = os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\ngo 1.22\n"), 0o644)
	_ = os.WriteFile(filepath.Join(root, "x.go"), []byte("package x\nfunc F(){}\n"), 0o644)

	var buf bytes.Buffer
	if err := BuildAndEncode(root, "quick", &buf); err != nil {
		t.Fatalf("BuildAndEncode: %v", err)
	}
	var env buildEnvelope
	if err := gob.NewDecoder(&buf).Decode(&env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(env.Symbols) == 0 {
		t.Fatal("应解码出符号")
	}
}
