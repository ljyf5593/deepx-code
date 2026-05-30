// Package config 负责 ~/.deepx/model.yaml 的读写。
//
// YAML 结构(每个 role 独立 base_url / model / api_key,允许用不同 provider):
//
//	flash:
//	  base_url: https://api.deepseek.com
//	  model: deepseek-v4-flash
//	  api_key: sk-...
//	pro:
//	  base_url: https://api.deepseek.com
//	  model: deepseek-v4-pro
//	  api_key: sk-...
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// ModelEntry 单个 role(flash / pro)的完整配置。
//
// 字段顺序需与 agent.ModelEntry 保持一致 —— tui 用 agent.ModelEntry(cfg.Flash) 整体类型转换。
type ModelEntry struct {
	BaseURL       string `yaml:"base_url"`
	Model         string `yaml:"model"`
	APIKey        string `yaml:"api_key"`
	ContextWindow int    `yaml:"context_window"` // 上下文窗口大小(tokens)
	MaxTokens     int    `yaml:"max_tokens"`     // 单次生成的 completion 上限(tokens)
	// 推理参数,跨供应商通用,空值绝对不发送 → 任何不支持的模型(MiMo、未来 OpenAI-兼容模型)
	// 都不会被多余字段炸 400。用户主动设了才往 chat/completions 请求体里塞。
	ReasoningEffort string `yaml:"reasoning_effort,omitempty"` // "low" / "medium" / "high"
	Thinking        string `yaml:"thinking,omitempty"`         // "enabled" / "disabled"
}

// WebConfig 本地 web dashboard 配置。
// Enabled 用指针:nil(yaml 里没写)= 采用默认值(开启);写了才按显式值。
type WebConfig struct {
	Enabled *bool `yaml:"enabled"` // nil => 默认开启
	Port    int   `yaml:"port"`    // 0 => 随机端口
}

// Config 整份 model.yaml 的反序列化目标。
type Config struct {
	Flash ModelEntry `yaml:"flash"`
	Pro   ModelEntry `yaml:"pro"`
	Web   WebConfig  `yaml:"web"`
}

// WebEnabled 解析 web dashboard 是否开启:env(DEEPX_WEB)优先于 config,config 没写默认开启。
// DEEPX_WEB 接受 off/0/false/no 关闭,on/1/true/yes 开启。
func (c *Config) WebEnabled() bool {
	if v, ok := os.LookupEnv("DEEPX_WEB"); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "off", "0", "false", "no":
			return false
		case "on", "1", "true", "yes":
			return true
		}
	}
	if c != nil && c.Web.Enabled != nil {
		return *c.Web.Enabled
	}
	return true
}

// WebPort 解析 web dashboard 端口:env(DEEPX_WEB_PORT)优先,其次 config,默认 0(随机)。
func (c *Config) WebPort() int {
	if v, ok := os.LookupEnv("DEEPX_WEB_PORT"); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
			return n
		}
	}
	if c != nil {
		return c.Web.Port
	}
	return 0
}

const (
	dirName  = ".deepx"
	fileName = "model.yaml"

	defaultProvider   = "deepseek"
	defaultBaseURL    = "https://api.deepseek.com"
	defaultFlashModel = "deepseek-v4-flash"
	defaultProModel   = "deepseek-v4-pro"
)

type modelT struct {
	URL           string
	FlashModel    string
	ProModel      string
	MaxTokens     int // 该供应商默认的单次 completion 上限
	ContextWindow int // 该供应商默认的上下文窗口大小
}

// ProviderOptions 是配置时可选的模型供应商,顺序即 UI 展示顺序(第一个为默认)。
var ProviderOptions = []string{"deepseek", "mimo"}

// modelConfig 各供应商的默认 base_url 与 flash/pro 模型 id。
// 注意:URL 只到域名(+可选 /v1),因为请求时 agent 会自行追加 "/chat/completions"
// (见 agent/llm.go);写成带 /chat/completions 的完整路径会被拼成
// ".../chat/completions/chat/completions" 而请求失败。
var modelConfig = map[string]modelT{
	"deepseek": {
		URL:           defaultBaseURL, // https://api.deepseek.com
		FlashModel:    defaultFlashModel,
		ProModel:      defaultProModel,
		MaxTokens:     393216,
		ContextWindow: 1_048_576, // 1M
	},
	"mimo": {
		URL:           "https://api.xiaomimimo.com/v1",
		FlashModel:    "mimo-v2.5",
		ProModel:      "mimo-v2.5-pro",
		MaxTokens:     131072,    // mimo 单次 completion 上限
		ContextWindow: 1_048_576, // 1M
	},
}

// Path 返回 ~/.deepx/model.yaml 绝对路径。
func Path() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("无法获取用户目录: %w", err)
	}
	return filepath.Join(home, dirName, fileName), nil
}

// Exists 配置文件是否已存在。出错或不存在均返回 false。
func Exists() bool {
	p, err := Path()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// DefaultFor 按指定供应商构造初始配置:flash/pro 共享该供应商的 base_url 和 key,
// 只是 model id 不同(flash 便宜起手 / pro 升级强模型)。未知供应商回退 deepseek。
// 用户之后仍可手动编辑 model.yaml 微调 base_url / model。
func DefaultFor(provider, apiKey string) *Config {
	mc, ok := modelConfig[provider]
	if !ok {
		mc = modelConfig[defaultProvider]
	}
	return &Config{
		Flash: ModelEntry{
			BaseURL:       mc.URL,
			Model:         mc.FlashModel,
			APIKey:        apiKey,
			ContextWindow: mc.ContextWindow,
			MaxTokens:     mc.MaxTokens,
		},
		Pro: ModelEntry{
			BaseURL:       mc.URL,
			Model:         mc.ProModel,
			APIKey:        apiKey,
			ContextWindow: mc.ContextWindow,
			MaxTokens:     mc.MaxTokens,
		},
	}
}

// Default 用默认供应商(deepseek)构造初始配置。保留以兼容现有调用。
func Default(apiKey string) *Config {
	return DefaultFor(defaultProvider, apiKey)
}

// defaultContextWindow 根据模型名推断上下文窗口,给旧 yaml(没写 context_window)兜底用。
// 已知供应商(deepseek / mimo)默认 1M tokens,其它未知模型保守取 64K。
func defaultContextWindow(model string) int {
	m := strings.ToLower(model)
	if strings.Contains(m, "deepseek") || strings.Contains(m, "mimo") {
		return 1_048_576
	}
	return 65_536
}

// defaultMaxTokens 根据模型名推断单次 completion 上限。含 deepseek 的模型沿用既有 384K;
// 其它模型保守取 131072(mimo 等的上限),避免超过模型实际允许值被拒。
// 给旧 model.yaml(没写 max_tokens)兜底用。
func defaultMaxTokens(model string) int {
	if strings.Contains(strings.ToLower(model), "deepseek") {
		return 393216
	}
	return 131072
}

// Load 从 ~/.deepx/model.yaml 读配置。文件缺失或解析失败返回 err。
func Load() (*Config, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("解析 %s: %w", p, err)
	}
	if c.Flash.ContextWindow <= 0 {
		c.Flash.ContextWindow = defaultContextWindow(c.Flash.Model)
	}
	if c.Pro.ContextWindow <= 0 {
		c.Pro.ContextWindow = defaultContextWindow(c.Pro.Model)
	}
	if c.Flash.MaxTokens <= 0 {
		c.Flash.MaxTokens = defaultMaxTokens(c.Flash.Model)
	}
	if c.Pro.MaxTokens <= 0 {
		c.Pro.MaxTokens = defaultMaxTokens(c.Pro.Model)
	}
	return &c, nil
}

// Save 写配置到 ~/.deepx/model.yaml,目录不存在会自动创建。
// 文件权限 0600(只有当前用户可读写,因为含 api key)。
func Save(c *Config) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		return err
	}
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return os.WriteFile(p, data, 0600)
}
