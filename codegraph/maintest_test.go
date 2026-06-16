package codegraph

// 测试里禁用「子进程建图」:re-exec 的是测试二进制(无 __codegraph-build 子命令),
// 会递归/失败。测试要验证的是建图逻辑本身,走进程内即可;子进程的 gob 往返另有专测。
func init() { useSubprocessBuild = false }
