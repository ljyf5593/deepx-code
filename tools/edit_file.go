package tools

import (
	"fmt"
	"os"
	"strings"
)

// EditFile 字符串模式替换:
//
//	old_string  (string) 要替换的内容（需逐字符精确匹配）
//	new_string  (string) 替换为
//	replace_all (bool)   是否替换所有匹配，默认 false
//
// 跟 Claude Code 的 Edit 一致。UI 预览侧用 locateLineInFile 反推出 old_string
// 在文件里的起始行号,patch -/+ 前面会带上行号列。
func EditFile(args map[string]any) ToolResult {
	path, _ := args["path"].(string)
	if path == "" {
		return ToolResult{Output: "错误: path 参数为空", Success: false}
	}
	oldStr, _ := args["old_string"].(string)
	newStr, _ := args["new_string"].(string)
	if oldStr == "" {
		return ToolResult{Output: "错误: old_string 不能为空", Success: false}
	}
	if oldStr == newStr {
		return ToolResult{Output: "错误: new_string 必须与 old_string 不同", Success: false}
	}
	replaceAll, _ := args["replace_all"].(bool)

	absPath, err := confineToWorkspace(path)
	if err != nil {
		return ToolResult{Output: err.Error(), Success: false}
	}
	data, err := os.ReadFile(absPath)
	if err != nil {
		return ToolResult{Output: fmt.Sprintf("读取失败: %v", err), Success: false}
	}

	content := string(data)
	count := strings.Count(content, oldStr)
	if count == 0 {
		return ToolResult{Output: "错误: 在文件中未找到 old_string", Success: false}
	}
	if count > 1 && !replaceAll {
		return ToolResult{
			Output:  fmt.Sprintf("错误: old_string 出现 %d 次,请提供更长上下文或设置 replace_all=true", count),
			Success: false,
		}
	}

	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(content, oldStr, newStr)
	} else {
		updated = strings.Replace(content, oldStr, newStr, 1)
	}
	if err := os.WriteFile(absPath, []byte(updated), 0o644); err != nil {
		return ToolResult{Output: fmt.Sprintf("写入失败: %v", err), Success: false}
	}
	CodeGraphInvalidate() // 文件变了,代码图谱缓存失效,下次查询重建
	return ToolResult{
		Output:  fmt.Sprintf("已替换 %d 处 -> %s", count, absPath),
		Success: true,
	}
}
