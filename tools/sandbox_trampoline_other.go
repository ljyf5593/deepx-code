//go:build !linux

package tools

// 仅 Linux 用 Landlock re-exec 跳板;其它平台无此机制,空实现,main() 调用它即立即返回。
func RunSandboxTrampolineIfRequested() {}
