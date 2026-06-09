package agent

import (
	"encoding/json"
	"fmt"
	"strings"
)

// AskUser:LLM 主动发起选择题,TUI 弹窗让用户勾选,选完把结果(JSON)写回 ResponseCh。
// 复用 review 模式的"暂停 agent 循环 + channel 回传 + reviewResultMsg 恢复监听"骨架。

// AskOption 一个候选项。Value 省略时由 parseAskUserArgs 回填为 Label。
type AskOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// AskQuestion 一道选择题。Multiple=true 为多选,否则单选。
type AskQuestion struct {
	Question string      `json:"question"`
	Multiple bool        `json:"multiple"`
	Options  []AskOption `json:"options"`
}

// AskUserMsg:发给 TUI 的"请弹选择框"事件。用户提交后把结果 JSON 写回 ResponseCh,
// 取消则写入空串。agent 循环在 <-ResponseCh 处阻塞,直到拿到结果再继续工具循环。
type AskUserMsg struct {
	Questions  []AskQuestion
	ResponseCh chan string
}

// parseAskUserArgs 解析 AskUser 工具参数。剔除空题/无选项题,并把缺省 value 回填为 label。
func parseAskUserArgs(raw string) ([]AskQuestion, error) {
	var w struct {
		Questions []AskQuestion `json:"questions"`
	}
	if err := json.Unmarshal([]byte(raw), &w); err != nil {
		return nil, fmt.Errorf("AskUser 参数解析失败: %w", err)
	}
	out := make([]AskQuestion, 0, len(w.Questions))
	for _, q := range w.Questions {
		if strings.TrimSpace(q.Question) == "" || len(q.Options) == 0 {
			continue
		}
		for i := range q.Options {
			if strings.TrimSpace(q.Options[i].Value) == "" {
				q.Options[i].Value = q.Options[i].Label
			}
		}
		out = append(out, q)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("AskUser 未提供有效问题(每题需要 question 和至少一个 option)")
	}
	return out, nil
}
