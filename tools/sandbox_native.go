package tools

import (
	"os"
	"path/filepath"
)

// native OS 级隔离的跨平台共享部分。
//
// 思路:native 不再只靠黑名单字符串过滤,而是用各平台的 OS 机制做**文件写禁闭 + 进程隔离**:
//   - macOS: sandbox-exec(Seatbelt)——见 sandbox_native_darwin.go
//   - Linux: bubblewrap(bwrap)——见 sandbox_native_linux.go
//   - 其它(Windows 等):无 OS 隔离,退回软黑名单——见 sandbox_native_other.go
//
// 网络保持开(否则 go mod / npm / git fetch 全断)。读基本不限,只禁"写到 workspace 外"。
// 各平台的 nativeShellCmd / nativeIsolationAvailable 由对应 build-tag 文件实现。

// nativeWritableRoots 返回 native OS 隔离下"允许写"的目录:
// workspace + cwd + 临时目录 + 常见工具链缓存。其余 host 路径一律只读。
//
// 关键:全部 realpath 解析(跟符号链接)。macOS 的 /tmp→/private/tmp、Seatbelt 按真实路径匹配;
// 不解析就会漏放行 → 正常命令(如 go build 写 $TMPDIR)被误杀。缓存目录也尽量从环境变量取(最准)。
func nativeWritableRoots(cwd string) []string {
	cand := []string{sbWorkspace.Load(), cwd}

	// 临时目录(go/cc 等大量用)
	cand = append(cand, os.TempDir(), "/tmp", "/private/tmp", "/var/tmp")
	if t := os.Getenv("TMPDIR"); t != "" {
		cand = append(cand, t)
	}

	// 常见工具链缓存:写不进这些,go/npm/pip/cargo/maven/gradle 等会构建失败。
	if h, err := os.UserHomeDir(); err == nil {
		cand = append(cand,
			filepath.Join(h, ".cache"),            // XDG 通用 + pip
			filepath.Join(h, "Library", "Caches"), // macOS 通用(含 go-build 默认)
			filepath.Join(h, "go"),                // GOPATH 默认(pkg/mod)
			filepath.Join(h, ".cargo"), filepath.Join(h, ".rustup"),
			filepath.Join(h, ".npm"), filepath.Join(h, ".gradle"),
			filepath.Join(h, ".m2"), filepath.Join(h, ".gem"),
		)
	}
	// 显式环境变量覆盖默认位置(非默认 GOCACHE/GOPATH 等)
	for _, e := range []string{"GOCACHE", "GOPATH", "GOMODCACHE", "npm_config_cache", "PIP_CACHE_DIR", "XDG_CACHE_HOME"} {
		if v := os.Getenv(e); v != "" {
			cand = append(cand, v)
		}
	}

	// realpath 解析 + 去空 + 去重
	seen := map[string]bool{}
	out := make([]string, 0, len(cand))
	for _, p := range cand {
		rp := realpathOrAbs(p)
		if rp == "" || seen[rp] {
			continue
		}
		seen[rp] = true
		out = append(out, rp)
	}
	return out
}

// realpathOrAbs 解析符号链接得到真实路径;路径不存在则退回 Abs+Clean(放行一个不存在的目录无害)。
func realpathOrAbs(p string) string {
	if p == "" {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return filepath.Clean(p)
}
