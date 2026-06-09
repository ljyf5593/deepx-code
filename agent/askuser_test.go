package agent

import "testing"

func TestParseAskUserArgs(t *testing.T) {
	raw := `{"questions":[
		{"question":"用哪个数据库?","options":[{"label":"PostgreSQL"},{"label":"MySQL","value":"mysql"}]},
		{"question":"要哪些功能?","multiple":true,"options":[{"label":"登录"},{"label":"支付"}]},
		{"question":"","options":[{"label":"x"}]},
		{"question":"没选项","options":[]}
	]}`
	qs, err := parseAskUserArgs(raw)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(qs) != 2 {
		t.Fatalf("expected 2 valid questions (空题/无选项应剔除), got %d", len(qs))
	}
	// value 缺省回填为 label
	if qs[0].Options[0].Value != "PostgreSQL" {
		t.Errorf("value 缺省应回填为 label,got %q", qs[0].Options[0].Value)
	}
	if qs[0].Options[1].Value != "mysql" {
		t.Errorf("显式 value 应保留,got %q", qs[0].Options[1].Value)
	}
	if !qs[1].Multiple {
		t.Errorf("第二题应为多选")
	}
}

func TestParseAskUserArgsInvalid(t *testing.T) {
	if _, err := parseAskUserArgs(`not json`); err == nil {
		t.Errorf("坏 JSON 应报错")
	}
	if _, err := parseAskUserArgs(`{"questions":[]}`); err == nil {
		t.Errorf("空问题列表应报错")
	}
}
