// Package session 把当前 workspace 的对话内容持久化到 ~/.deepx/sessions/{sessionID}/。
//
// 设计要点:
//   - sessionID = sha1(abs(workspace))[:16],workspace 切换自然换 session。
//   - 每天一个 jsonl 文件,append-only,一行一条 Entry。
//   - 只存 user/assistant content 主对话(tool_call / tool_result 不入文件,
//     避免 jsonl 巨大化;LLM 当下能从 message 里看到工具序列,
//     重加历史时看不到也无所谓 —— 它只需要语义上的"上次聊了什么")。
//   - 读时按文件名(日期)倒序聚合,N 个 user→assistant 对截止。
//   - Search 用纯 Go strings.Contains 大小写不敏感扫描,不走外部 grep,跨平台稳。
package session

import (
	"bufio"
	"crypto/rand"
	"crypto/sha1"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// gobMagic 是二进制 history 文件的魔数头(4 字节),用于版本校验。
const gobMagic = "DXP1"

// Entry 是 jsonl 里每行的结构。简洁版只 ts/role/content。
type Entry struct {
	Ts      time.Time `json:"ts"`
	Role    string    `json:"role"`
	Content string    `json:"content"`
}

// SearchHit Memory 工具的命中条目。
type SearchHit struct {
	Date  string // 文件名日期 (YYYY-MM-DD)
	Entry Entry
}

// Manager 管单个 workspace 的 session。线程不安全,TUI 单 goroutine 调用即可。
type Manager struct {
	workspace string
	sessionID string
	rootDir   string // ~/.deepx/sessions/{sessionID}
	// convDir 是"当前对话"目录:对话相关文件(history.gob / summary / state.json / last_prompt /
	// last_tools)都在这里读写。默认对话 = rootDir 本身(零迁移,老数据原地不动);
	// /new 出来的新对话 = rootDir/conversations/{id}。jsonl 与 meta.json 始终在 rootDir(workspace 级)。
	convDir string
}

// metaFile 是 ~/.deepx/sessions/{sid}/meta.json 的结构。
type metaFile struct {
	Workspace  string    `json:"workspace"`
	CreatedAt  time.Time `json:"created_at"`
	LastSeenAt time.Time `json:"last_seen_at"`
	WebToken   string    `json:"web_token,omitempty"` // 该 session 固定的 web 面板访问令牌,生成一次后不变
}

// stateFile 是 ~/.deepx/sessions/{sid}/state.json 的结构。只放小而高频写的字段。
// 大块(摘要、上次 system 文本、上次 tool specs)各自存裸文件,避免高频写时整体重写大 JSON,
// 也避免 tool specs JSON 被二次转义。见 summaryFile / lastPromptFile / lastToolsFile。
type stateFile struct {
	LastUsage   *usageSnapshot `json:"last_usage,omitempty"`   // 上轮 API 调用 token 用量,启动时回填 Usage section
	PrefixSig   string         `json:"prefix_sig,omitempty"`   // hash(系统提示词+工具+mcp),重启检测前缀变化
	PrefixModel string         `json:"prefix_model,omitempty"` // 上次实际发送用的 model ID(缓存按模型分,压缩需同模型才命中)
	WorkingMode string         `json:"working_mode,omitempty"` // 工作模式 kp/openspec/sp(按会话保存,切会话时同步)。空 = 默认 kp
	ModelPin    string         `json:"model_pin,omitempty"`    // /model 锁定 auto/flash/pro(按子会话保存,切会话时同步)。空 = auto

	// ShadowCut 影子热压(shadow checkpoint)的切点 —— history[:ShadowCut] 已被压进 shadow 文件,
	// history[ShadowCut:] 是保留尾部。仅 >1h 冷重启时应用(见 tui 重启加载),0 = 无影子。
	ShadowCut int `json:"shadow_cut,omitempty"`

	// Summary 已迁出到独立裸文件;此字段仅用于读取旧版本遗留的 state.json(向后兼容)。
	Summary string `json:"summary,omitempty"`
}

// 大块裸文件名(无后缀,直接读写内容):
//
//	summaryFile     — 会话压缩摘要(纯文本)
//	lastPromptFile  — 上次实际发送的 system 文本(纯文本,压缩时复刻旧前缀)
//	lastToolsFile   — 上次实际发送的 tool specs(裸 JSON,不被二次转义)
const (
	summaryFile    = "summary"
	lastPromptFile = "last_prompt"
	lastToolsFile  = "last_tools"
	shadowFile     = "shadow" // 影子热压 checkpoint 文本(切点存 state.json 的 ShadowCut)
)

// writeRaw 原子写一个对话级裸文件(write-then-rename)到 convDir。失败静默。
func (m *Manager) writeRaw(name, content string) {
	path := filepath.Join(m.convDir, name)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// readRaw 读一个对话级裸文件(convDir),不存在返回空串。
func (m *Manager) readRaw(name string) string {
	data, err := os.ReadFile(filepath.Join(m.convDir, name))
	if err != nil {
		return ""
	}
	return string(data)
}

// usageSnapshot 是 stateFile 内嵌的 token 用量快照,字段名对齐 DeepSeek API。
// 单独定义而非引用 agent.UsageInfo,避免 session→agent 反向依赖。
type Usage = usageSnapshot
type usageSnapshot struct {
	PromptTokens          int `json:"prompt_tokens"`
	CompletionTokens      int `json:"completion_tokens"`
	PromptCacheHitTokens  int `json:"prompt_cache_hit_tokens"`
	PromptCacheMissTokens int `json:"prompt_cache_miss_tokens"`
}

// New 给指定 workspace 创建/打开 session。会自动建目录,刷新 meta.json 的 last_seen_at。
func New(workspace string) (*Manager, error) {
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("abs path: %w", err)
	}
	h := sha1.Sum([]byte(abs))
	sid := hex.EncodeToString(h[:])[:16]

	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("user home: %w", err)
	}
	root := filepath.Join(home, ".deepx", "sessions", sid)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir session: %w", err)
	}

	m := &Manager{workspace: abs, sessionID: sid, rootDir: root}
	m.convDir = root               // 安全默认:即"默认对话"=rootDir(老数据原地)
	m.convDir = m.resolveConvDir() // 按 current 指针定位当前对话(见 conversation.go)
	m.touchMeta()
	return m, nil
}

// SessionID 返回 16 字符 hex,用作目录名与诊断显示。
func (m *Manager) SessionID() string { return m.sessionID }

// RootDir 返回 session 目录绝对路径。
func (m *Manager) RootDir() string { return m.rootDir }

// touchMeta 创建或更新 meta.json。失败静默,不影响主流程。
func (m *Manager) touchMeta() {
	path := filepath.Join(m.rootDir, "meta.json")
	var info metaFile
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &info)
	}
	if info.CreatedAt.IsZero() {
		info.CreatedAt = time.Now()
	}
	info.Workspace = m.workspace
	info.LastSeenAt = time.Now()
	data, _ := json.MarshalIndent(info, "", "  ")
	_ = os.WriteFile(path, data, 0o644)
}

// WebToken 返回该 session 固定的 web 面板访问令牌:读 meta.json,有则原样返回,
// 没有则生成一个并写回(之后永不变),保证同一 workspace 的访问 URL 跨重启稳定。
// 生成/写入失败时回退一个临时随机令牌(本次进程可用,但不持久)。
func (m *Manager) WebToken() string {
	path := filepath.Join(m.rootDir, "meta.json")
	var info metaFile
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &info)
	}
	if info.WebToken != "" {
		return info.WebToken
	}
	info.WebToken = randomToken()
	// 补齐其它字段(meta.json 可能还不存在),与 touchMeta 保持一致。
	if info.Workspace == "" {
		info.Workspace = m.workspace
	}
	if info.CreatedAt.IsZero() {
		info.CreatedAt = time.Now()
	}
	if info.LastSeenAt.IsZero() {
		info.LastSeenAt = time.Now()
	}
	if data, err := json.MarshalIndent(info, "", "  "); err == nil {
		_ = os.WriteFile(path, data, 0o644)
	}
	return info.WebToken
}

// randomToken 生成 16 字节随机 hex 令牌。
func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "deepx" // crypto/rand 实际不会失败,极端兜底
	}
	return hex.EncodeToString(b)
}

// SaveSummary 保存压缩摘要到裸文件。
func (m *Manager) SaveSummary(text string) error {
	m.writeRaw(summaryFile, text)
	return nil
}

// SaveShadow 保存影子热压结果:checkpoint 文本进裸文件,切点进 state.json。
// 影子是"预先算好、推迟到 >1h 冷重启才应用的压缩",不影响实时会话。
func (m *Manager) SaveShadow(checkpoint string, cut int) {
	m.writeRaw(shadowFile, checkpoint)
	m.setShadowCut(cut)
}

// ClearShadow 清掉影子(实时压缩落地后调用 —— 实时状态已是更新的 checkpoint+tail,影子作废)。
func (m *Manager) ClearShadow() {
	m.writeRaw(shadowFile, "")
	m.setShadowCut(0)
}

// LoadShadow 读影子热压结果;无影子返回 ("", 0)。
func (m *Manager) LoadShadow() (checkpoint string, cut int) {
	checkpoint = m.readRaw(shadowFile)
	if data, err := os.ReadFile(filepath.Join(m.convDir, "state.json")); err == nil {
		var s stateFile
		if json.Unmarshal(data, &s) == nil {
			cut = s.ShadowCut
		}
	}
	return checkpoint, cut
}

// setShadowCut 读改写 state.json 的 ShadowCut(只在主 goroutine 的 Update 里调,无并发)。
func (m *Manager) setShadowCut(cut int) {
	m.updateState(func(s *stateFile) { s.ShadowCut = cut })
}

// updateState 对 state.json 做读-改-写:读当前内容 → fn 改自己那个字段 → 写回。这些小而高频的
// 字段共用一个文件;只在主 goroutine(Update)里调,无并发。失败静默,不影响主流程。
func (m *Manager) updateState(fn func(*stateFile)) {
	path := filepath.Join(m.convDir, "state.json")
	var s stateFile
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &s)
	}
	fn(&s)
	data, _ := json.MarshalIndent(&s, "", "  ")
	_ = os.WriteFile(path, data, 0o644)
}

// SavePrefixSnapshot 记录"上次实际发送"的前缀快照:签名进 state.json,system 文本和 tool specs
// 各进裸文件(避免高频整体重写大 JSON + tool specs 二次转义)。失败静默,不影响主流程。
func (m *Manager) SavePrefixSnapshot(sig, model, systemPrompt, toolSpecsJSON string) {
	m.writeRaw(lastPromptFile, systemPrompt)
	m.writeRaw(lastToolsFile, toolSpecsJSON)
	m.updateState(func(s *stateFile) { s.PrefixSig = sig; s.PrefixModel = model })
}

// PrefixSnapshotTime 返回前缀快照(last_prompt)最后写入的时间,即"上次请求实际发送"的时刻 ——
// 用于判断 DeepSeek 缓存是否还可能热。文件不存在返回 (零值, false)。
func (m *Manager) PrefixSnapshotTime() (time.Time, bool) {
	fi, err := os.Stat(filepath.Join(m.convDir, lastPromptFile))
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// LoadPrefixSnapshot 读取上次的前缀快照(签名/model 来自 state.json,system/tools 来自裸文件)。
func (m *Manager) LoadPrefixSnapshot() (sig, model, systemPrompt, toolSpecsJSON string) {
	path := filepath.Join(m.convDir, "state.json")
	if data, err := os.ReadFile(path); err == nil {
		var s stateFile
		if json.Unmarshal(data, &s) == nil {
			sig = s.PrefixSig
			model = s.PrefixModel
		}
	}
	return sig, model, m.readRaw(lastPromptFile), m.readRaw(lastToolsFile)
}

// SaveUsage 写入 last_usage 字段。失败静默,不影响主流程。
// 复用 state.json,避免再多一个文件。
func (m *Manager) SaveUsage(promptTokens, completionTokens, cacheHit, cacheMiss int) {
	m.updateState(func(s *stateFile) {
		s.LastUsage = &usageSnapshot{
			PromptTokens:          promptTokens,
			CompletionTokens:      completionTokens,
			PromptCacheHitTokens:  cacheHit,
			PromptCacheMissTokens: cacheMiss,
		}
	})
}

// LoadUsage 读 last_usage 字段。文件/字段缺失返回 nil。
func (m *Manager) LoadUsage() *Usage {
	path := filepath.Join(m.convDir, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var s stateFile
	if err := json.Unmarshal(data, &s); err != nil {
		return nil
	}
	return s.LastUsage
}

// SaveWorkingMode 写入工作模式到 state.json(按会话持久化)。失败静默。
func (m *Manager) SaveWorkingMode(mode string) {
	m.updateState(func(s *stateFile) { s.WorkingMode = mode })
}

// LoadWorkingMode 读工作模式;缺失返回空串(调用方归一为默认 kp)。
func (m *Manager) LoadWorkingMode() string {
	path := filepath.Join(m.convDir, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var s stateFile
	if err := json.Unmarshal(data, &s); err != nil {
		return ""
	}
	return s.WorkingMode
}

// SaveModelPin 写入 /model 锁定到当前子会话的 state.json。失败静默。
func (m *Manager) SaveModelPin(pin string) {
	m.updateState(func(s *stateFile) { s.ModelPin = pin })
}

// LoadModelPin 读 /model 锁定;缺失返回空串(调用方归一为默认 auto)。
func (m *Manager) LoadModelPin() string {
	path := filepath.Join(m.convDir, "state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var s stateFile
	if err := json.Unmarshal(data, &s); err != nil {
		return ""
	}
	return s.ModelPin
}

// LoadSummary 读取压缩摘要:优先裸文件,为空时回退到旧版本遗留在 state.json 的 summary 字段。
func (m *Manager) LoadSummary() string {
	if s := m.readRaw(summaryFile); s != "" {
		return s
	}
	var st stateFile // 旧格式回退
	if data, err := os.ReadFile(filepath.Join(m.convDir, "state.json")); err == nil {
		_ = json.Unmarshal(data, &st)
	}
	return st.Summary
}

// SaveGob 以 gob 格式将 v 编码到 filename,写入 session 目录。原子写(write-then-rename)。
// 文件头带 4 字节魔数 DXP1,用于版本校验。
func (m *Manager) SaveGob(filename string, v any) error {
	path := filepath.Join(m.convDir, filename)
	tmpPath := path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return err
	}
	closeOK := false
	defer func() {
		if !closeOK {
			f.Close()
			os.Remove(tmpPath)
		}
	}()
	if _, err := f.Write([]byte(gobMagic)); err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(v); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmpPath)
		return err
	}
	closeOK = true
	return os.Rename(tmpPath, path)
}

// LoadGob 从 session 目录的 filename 读 gob 编码,解码到 v。
// 魔数不匹配或文件不存在均返回 error。
func (m *Manager) LoadGob(filename string, v any) error {
	path := filepath.Join(m.convDir, filename)
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	magic := make([]byte, 4)
	if _, err := io.ReadFull(f, magic); err != nil {
		return err
	}
	if string(magic) != gobMagic {
		return fmt.Errorf("invalid history magic: %x", magic)
	}
	return gob.NewDecoder(f).Decode(v)
}

// todayPath 当天文件路径。按本地时区命名,方便人类浏览。
func (m *Manager) todayPath() string {
	return filepath.Join(m.rootDir, time.Now().Format("2006-01-02")+".jsonl")
}

// Append 写一条记录到今天的 jsonl。
// 只接受 role = "user" / "assistant",其他 role(system/tool)静默丢弃 —— 主对话才入会话文件。
// 空 content 也跳过(流式中途的占位等)。
func (m *Manager) Append(role, content string) error {
	if role != "user" && role != "assistant" {
		return nil
	}
	if strings.TrimSpace(content) == "" {
		return nil
	}
	f, err := os.OpenFile(m.todayPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	return enc.Encode(Entry{Ts: time.Now(), Role: role, Content: content})
}

// LoadRecentTurns 从最新日期文件倒着读,凑足 n 个 user→assistant 对停。
// 返回按时间正序的 entries,可直接喂给 LLM history。
//
// "1 个 turn" 的定义:一条 user message 加它后续的 assistant 回复(可能多条 assistant
// 段或被工具调用插开,但本会话文件只记 content,所以约等于 1 个 user + 1 个 assistant)。
//
// 实现:文件按日期降序遍历,每个文件读完后从尾部反向扫,边扫边累积。
// 一旦凑够 n 个 user 立即返回,避免把 180 天历史全读进内存。
// 热路径(N=20 且当天就够)只读 1 个文件;稀疏使用时回退到读多个文件。
func (m *Manager) LoadRecentTurns(n int) []Entry {
	if n <= 0 {
		return nil
	}
	files, _ := filepath.Glob(filepath.Join(m.rootDir, "*.jsonl"))
	sort.Sort(sort.Reverse(sort.StringSlice(files))) // 新 → 旧

	// collected 按"新到旧"顺序累积(反向序),最后整体翻转一次成时间正序。
	var collected []Entry
	userCount := 0
	for _, fp := range files {
		entries := readJSONL(fp)
		for i := len(entries) - 1; i >= 0; i-- {
			collected = append(collected, entries[i])
			if entries[i].Role == "user" {
				userCount++
				if userCount >= n {
					reverseEntries(collected)
					return collected
				}
			}
		}
	}
	// 历史不够 n turn,把已有的全返回
	reverseEntries(collected)
	return collected
}

// reverseEntries 就地翻转 slice,O(n) 无分配。
func reverseEntries(s []Entry) {
	for i, j := 0, len(s)-1; i < j; i, j = i+1, j-1 {
		s[i], s[j] = s[j], s[i]
	}
}

// Search 在当前 session 下扫描所有 jsonl,按关键词命中。
// mode = "and"(默认,全部关键词都在) / "or"(任一命中)
// 按日期降序遍历(新的优先),max 上限后立即返回。
func (m *Manager) Search(keywords []string, mode string, max int) []SearchHit {
	if max <= 0 {
		max = 20
	}
	if len(keywords) == 0 {
		return nil
	}
	if mode == "" {
		mode = "and"
	}
	files, _ := filepath.Glob(filepath.Join(m.rootDir, "*.jsonl"))
	sort.Sort(sort.Reverse(sort.StringSlice(files))) // 新 → 旧
	var hits []SearchHit
	for _, fp := range files {
		date := strings.TrimSuffix(filepath.Base(fp), ".jsonl")
		for _, e := range readJSONL(fp) {
			if matchKeywords(e.Content, keywords, mode) {
				hits = append(hits, SearchHit{Date: date, Entry: e})
				if len(hits) >= max {
					return hits
				}
			}
		}
	}
	return hits
}

// readJSONL 读一个 jsonl 文件,容错跳过解析失败的行。
// 单行容量 1MB(防写入超长内容时 scanner 默认 64KB 撑爆)。
func readJSONL(path string) []Entry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var out []Entry
	for sc.Scan() {
		var e Entry
		if err := json.Unmarshal(sc.Bytes(), &e); err == nil {
			out = append(out, e)
		}
	}
	return out
}

// matchKeywords 大小写不敏感地匹配。mode="and" 全部命中, "or" 任一命中。
func matchKeywords(text string, kws []string, mode string) bool {
	lt := strings.ToLower(text)
	if mode == "or" {
		for _, k := range kws {
			if strings.Contains(lt, strings.ToLower(k)) {
				return true
			}
		}
		return false
	}
	for _, k := range kws {
		if !strings.Contains(lt, strings.ToLower(k)) {
			return false
		}
	}
	return true
}
