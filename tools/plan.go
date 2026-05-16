package tools

// 注:CreatePlan 和 UpdateTaskStatus 的 Executor **不会被实际调用**——
// agent 主循环在派发工具调用前会先识别这两个名字并拦截 (产出对应 tea.Msg)。
// 这里的兜底 Executor 仅在拦截逻辑漏掉时返回明确文本,而不是空响应。

func CreatePlan(args map[string]any) ToolResult {
	return ToolResult{
		Output:  "已注册规划 (deepx 已接收, 等待执行)。",
		Success: true,
	}
}

func UpdateTaskStatus(args map[string]any) ToolResult {
	id, _ := args["id"].(string)
	status, _ := args["status"].(string)
	if id == "" {
		return ToolResult{Output: "UpdateTaskStatus: id 必填", Success: false}
	}
	if status == "" {
		return ToolResult{Output: "UpdateTaskStatus: status 必填", Success: false}
	}
	return ToolResult{Output: "状态已更新: " + id + " → " + status, Success: true}
}
