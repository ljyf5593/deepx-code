package skill

import (
	"embed"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed skills/*
var builtinFS embed.FS

// builtinVersion 决定是否需要把内嵌 skill 重新解压到 ~/.deepx/skills。通过 -ldflags -X 注入:
//   - goreleaser 发布版(.goreleaser.yaml):= {{.Version}},随发布版本走,升级到新版本才刷新;
//   - install.* 的 --from-source 路径:= 构建时间戳,每次重装刷新;
//   - 未注入(本地 go build / go run):保持 "dev",每次启动都刷新,改完内嵌 skill 即生效。
//
// 这样省去了过去手动 +1 维护版本号的负担。
var builtinVersion = "dev"

// manifestFile 记录"上一次解压出去的内置 skill 名字"(每行一个)。它是把废弃内置删干净的依据:
// 升级后,旧清单里有、当前 embed 里没有的,就是被移除的内置 → 精确删掉。用户自己装的 / 自定义的
// skill 从不写进清单,所以永远不会被这套清理逻辑碰到。
const manifestFile = ".builtin_manifest"

// BuiltinNames 返回当前随二进制 embed 的内置 skill 名字集合。
// 供 UI 判断某 skill 是否内置(内置不可删,只能随升级更新)。
func BuiltinNames() map[string]bool {
	out := map[string]bool{}
	entries, err := builtinFS.ReadDir("skills")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			out[e.Name()] = true
		}
	}
	return out
}

// ExtractBuiltins 将内嵌 skill 解压到 ~/.deepx/skills/。
// 通过版本文件判断是否需要更新，避免每次启动都写盘。
// 用户自定义 skill 不受影响（只覆盖同名内置 skill）；被移除的内置 skill 会按清单差集删除。
func ExtractBuiltins(home string) (string, error) {
	dest := filepath.Join(home, ".deepx", "skills")
	verFile := filepath.Join(dest, ".builtin_version")

	// dev 构建(版本未注入)总是重新解压,确保改完内嵌 skill 立刻生效;
	// 发布构建按时间戳版本比对,命中就跳过写盘。命中即说明内置集没变,无需清理。
	if builtinVersion != "dev" {
		if data, err := os.ReadFile(verFile); err == nil && string(data) == builtinVersion {
			return dest, nil
		}
	}

	entries, err := builtinFS.ReadDir("skills")
	if err != nil {
		return dest, err
	}

	// 当前内置 skill 名字集。
	current := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			current = append(current, e.Name())
		}
	}

	// 先清理:旧清单里有、当前已不存在的内置 → 删除其解压目录。
	pruneRemovedBuiltins(dest, current)

	for _, name := range current {
		if err := copyBuiltinDir("skills/"+name, filepath.Join(dest, name)); err != nil {
			return dest, err
		}
	}

	os.MkdirAll(dest, 0o755)
	os.WriteFile(verFile, []byte(builtinVersion), 0o644)
	writeBuiltinManifest(dest, current)
	return dest, nil
}

// pruneRemovedBuiltins 删除"上次是内置、这次 embed 里已没有"的 skill 目录。
// 只针对清单里记过的名字,所以用户自定义 / 第三方装的 skill 不会被误删。
func pruneRemovedBuiltins(dest string, current []string) {
	old := readBuiltinManifest(dest)
	if len(old) == 0 {
		return
	}
	cur := make(map[string]struct{}, len(current))
	for _, n := range current {
		cur[n] = struct{}{}
	}
	for _, name := range old {
		if name == "" {
			continue
		}
		if _, stillBuiltin := cur[name]; !stillBuiltin {
			os.RemoveAll(filepath.Join(dest, name))
		}
	}
}

// readBuiltinManifest 读取上次写下的内置 skill 名单(不存在则返回 nil)。
func readBuiltinManifest(dest string) []string {
	data, err := os.ReadFile(filepath.Join(dest, manifestFile))
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(string(data), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			names = append(names, s)
		}
	}
	return names
}

// writeBuiltinManifest 写下本次解压的内置 skill 名单(排序后每行一个),作为下次清理的依据。
func writeBuiltinManifest(dest string, names []string) {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	os.WriteFile(filepath.Join(dest, manifestFile), []byte(strings.Join(sorted, "\n")+"\n"), 0o644)
}

func copyBuiltinDir(src, dst string) error {
	entries, err := builtinFS.ReadDir(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		srcPath := src + "/" + e.Name()
		dstPath := filepath.Join(dst, e.Name())
		if e.IsDir() {
			if err := copyBuiltinDir(srcPath, dstPath); err != nil {
				return err
			}
		} else {
			data, err := builtinFS.ReadFile(srcPath)
			if err != nil {
				return err
			}
			if err := os.WriteFile(dstPath, data, 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}
