package tools

import (
	"bufio"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Docker 沙箱后端:命令在常驻容器里跑(workspace 绑定挂载到 /workspace),真隔离。
// 容器按 workspace 命名复用;首次用时拉镜像 + 起容器;deepx 退出时删容器。
// 跨三平台靠 docker CLI(mac/win 用 Docker Desktop,linux 原生)。

const dockerMount = "/workspace" // 容器内挂载点

var (
	sbWorkspace atomic_string // 宿主 workspace 绝对路径(SetSandboxWorkspace 注入)
	sbImage     atomic_string // 容器镜像(默认 ubuntu:24.04,/sandbox docker <image> 可换)
	dockerMu    sync.Mutex    // 串行化容器生命周期操作
)

// atomic_string 是个极简的并发安全字符串(避免引入 atomic.Value 的类型断言样板)。
type atomic_string struct {
	mu sync.RWMutex
	v  string
}

func (a *atomic_string) Store(s string) { a.mu.Lock(); a.v = s; a.mu.Unlock() }
func (a *atomic_string) Load() string   { a.mu.RLock(); defer a.mu.RUnlock(); return a.v }

// SetSandboxWorkspace 注入 workspace 绝对路径(docker 挂载 + cwd 换算用)。启动时调,同 SetCodeGraphRoot。
func SetSandboxWorkspace(dir string) {
	if abs, err := filepath.Abs(dir); err == nil {
		sbWorkspace.Store(abs)
	} else {
		sbWorkspace.Store(dir)
	}
}

// SandboxDockerImage 返回当前容器镜像(空则默认 ubuntu:24.04)。
func SandboxDockerImage() string {
	if v := sbImage.Load(); v != "" {
		return v
	}
	return "ubuntu:24.04"
}

// SetSandboxDockerImage 设置容器镜像。换镜像意味着下次用容器要重建(由调用方决定时机)。
func SetSandboxDockerImage(image string) { sbImage.Store(strings.TrimSpace(image)) }

// DockerAvailable 探测 docker 是否可用(已装且 daemon 在跑)。3s 超时,不挂死。
func DockerAvailable() error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("未找到 docker 命令(请安装 Docker)")
	}
	if out, err := exec.CommandContext(ctx, "docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		return fmt.Errorf("Docker daemon 未运行或不可达(请启动 Docker):%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// sandboxContainerName 按 workspace 路径派生稳定容器名,便于复用。
func sandboxContainerName() string {
	h := sha1.Sum([]byte(sbWorkspace.Load()))
	return "deepx-sbx-" + hex.EncodeToString(h[:])[:12]
}

// EnsureDockerContainer 保证容器在跑(没有则建、停了则启),返回容器名。串行化,避免并发重复建。
func EnsureDockerContainer() (string, error) {
	dockerMu.Lock()
	defer dockerMu.Unlock()

	ws := sbWorkspace.Load()
	if ws == "" {
		return "", fmt.Errorf("workspace 未设置,无法挂载")
	}
	name := sandboxContainerName()

	// 已在跑 → 直接用
	if running, _ := containerRunning(name); running {
		return name, nil
	}
	// 存在但停了 → 启动;否则新建
	if exists, _ := containerExists(name); exists {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if out, err := exec.CommandContext(ctx, "docker", "start", name).CombinedOutput(); err != nil {
			return "", fmt.Errorf("启动已有容器失败:%s", strings.TrimSpace(string(out)))
		}
		return name, nil
	}

	// 新建:挂载 workspace、保活(sleep infinity)、网络默认开(bridge)。首次拉镜像可能较慢。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	args := []string{
		"run", "-d", "--name", name,
		"--label", "deepx-sandbox=1",
		"-v", ws + ":" + dockerMount,
		"-w", dockerMount,
		SandboxDockerImage(),
		"sleep", "infinity",
	}
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return "", fmt.Errorf("创建容器失败(镜像 %s):%s", SandboxDockerImage(), strings.TrimSpace(string(out)))
	}
	return name, nil
}

func containerRunning(name string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Running}}", name).Output()
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(string(out)) == "true", nil
}

func containerExists(name string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := exec.CommandContext(ctx, "docker", "inspect", name).Run()
	return err == nil, err
}

// StopSandboxContainer 强删沙箱容器(deepx 退出时调)。best-effort,失败静默。
func StopSandboxContainer() {
	ws := sbWorkspace.Load()
	if ws == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_ = exec.CommandContext(ctx, "docker", "rm", "-f", sandboxContainerName()).Run()
}

// containerWorkdir 把宿主 cwd 换算成容器内路径(挂载点下)。cwd 为空或不在 workspace 内 → /workspace。
func containerWorkdir(cwd string) string {
	ws := sbWorkspace.Load()
	if cwd == "" || ws == "" {
		return dockerMount
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return dockerMount
	}
	rel, err := filepath.Rel(ws, abs)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return dockerMount
	}
	return dockerMount + "/" + filepath.ToSlash(rel)
}

// PullProgress 是一次镜像拉取的进度。Layers=已发现层数,Done=已完成层数(Pull complete/Already exists)。
// Finished=拉取结束(成功或失败),Err 非空表示失败。
type PullProgress struct {
	Layers   int
	Done     int
	Finished bool
	Err      error
}

// ImagePresent 判断镜像是否本地已有(有则无需拉取)。
func ImagePresent(image string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "docker", "image", "inspect", image).Run() == nil
}

// PullImage 异步 `docker pull <image>`,把按层进度流式写入返回的 channel(结束后关闭)。
// 进度按"完成层数/已发现层数"算(层级粒度,跨平台、不依赖 docker API)。镜像已存在则直接发 Finished。
func PullImage(ctx context.Context, image string) <-chan PullProgress {
	ch := make(chan PullProgress, 32)
	go func() {
		defer close(ch)
		if ImagePresent(image) {
			ch <- PullProgress{Finished: true}
			return
		}
		cmd := exec.CommandContext(ctx, "docker", "pull", image)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			ch <- PullProgress{Finished: true, Err: err}
			return
		}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		if err := cmd.Start(); err != nil {
			ch <- PullProgress{Finished: true, Err: err}
			return
		}
		total := map[string]bool{}
		done := map[string]bool{}
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for sc.Scan() {
			id, status, ok := parseLayerLine(sc.Text())
			if !ok {
				continue
			}
			total[id] = true
			if status == "Pull complete" || status == "Already exists" {
				done[id] = true
			}
			ch <- PullProgress{Layers: len(total), Done: len(done)}
		}
		werr := cmd.Wait()
		if werr != nil && stderr.Len() > 0 {
			werr = fmt.Errorf("%s", strings.TrimSpace(stderr.String()))
		}
		ch <- PullProgress{Layers: len(total), Done: len(done), Finished: true, Err: werr}
	}()
	return ch
}

// parseLayerLine 解析 docker pull 的一行 "<id>: <status>…",返回层 id 和状态短语。
// 只认层级状态行(过滤 "Pulling from …" / "Digest:" / "Status:" 等非层行)。
func parseLayerLine(line string) (id, status string, ok bool) {
	i := strings.Index(line, ": ")
	if i <= 0 {
		return "", "", false
	}
	id = strings.TrimSpace(line[:i])
	rest := strings.TrimSpace(line[i+2:])
	for _, s := range []string{
		"Pull complete", "Already exists", "Pulling fs layer", "Waiting",
		"Downloading", "Verifying Checksum", "Download complete", "Extracting",
	} {
		if strings.HasPrefix(rest, s) {
			return id, s, true
		}
	}
	return "", "", false
}

// dockerExecCmd 构造"在容器里跑命令"的 exec.Cmd:确保容器在跑,再 docker exec。
func dockerExecCmd(command, cwd string) (*exec.Cmd, error) {
	name, err := EnsureDockerContainer()
	if err != nil {
		return nil, err
	}
	return exec.Command("docker", "exec", "-w", containerWorkdir(cwd), name, "sh", "-c", command), nil
}
