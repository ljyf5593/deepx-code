package skill

import (
	"strings"
	"testing"
)

// 内置 superpowers 工作流 skill 必须能被解压、发现,且 frontmatter 完整。
func TestBuiltinSuperpowersSkills(t *testing.T) {
	home := t.TempDir()
	dest, err := ExtractBuiltins(home)
	if err != nil {
		t.Fatalf("ExtractBuiltins: %v", err)
	}

	loader := New(nil, []string{dest})
	got := map[string]Metadata{}
	for _, m := range loader.List() {
		got[m.Name] = m
	}

	// 保留的行为类子集(其余 superpowers skill 已按需裁掉)。
	want := []string{
		"brainstorming",
		"verification-before-completion",
	}
	for _, name := range want {
		m, ok := got[name]
		if !ok {
			t.Errorf("内置 skill %q 未被发现", name)
			continue
		}
		if strings.TrimSpace(m.Description) == "" {
			t.Errorf("skill %q 的 description 为空", name)
		}
		// 正文应能完整加载且非空
		s, err := loader.Load(name)
		if err != nil {
			t.Errorf("加载 skill %q: %v", name, err)
			continue
		}
		if strings.TrimSpace(s.Body) == "" {
			t.Errorf("skill %q 正文为空", name)
		}
	}
}
