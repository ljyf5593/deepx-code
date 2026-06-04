package tools

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteFile 写入（覆盖）文本文件。
// 参数:
//
//	path    (string) 文件路径
//	content (string) 写入的内容
func WriteFile(args map[string]any) ToolResult {
	path, _ := args["path"].(string)
	if path == "" {
		return ToolResult{Output: "错误: path 参数为空", Success: false}
	}
	content, _ := args["content"].(string)

	absPath, err := confineToWorkspace(path)
	if err != nil {
		return ToolResult{Output: err.Error(), Success: false}
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return ToolResult{Output: fmt.Sprintf("创建父目录失败: %v", err), Success: false}
	}
	if err := os.WriteFile(absPath, []byte(content), 0o644); err != nil {
		return ToolResult{Output: fmt.Sprintf("写入失败: %v", err), Success: false}
	}
	CodeGraphInvalidate() // 文件变了,代码图谱缓存失效,下次查询重建
	return ToolResult{
		Output:  fmt.Sprintf("已写入 %s (%d bytes)", absPath, len(content)),
		Success: true,
	}
}
