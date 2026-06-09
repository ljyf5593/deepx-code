package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"deepx/mcp"
	"deepx/skill"
)

// Server 是本地 web dashboard 的 HTTP 服务。默认绑 127.0.0.1(仅本机),带随机 token 防未授权访问。
// 可由调用方指定绑定地址(如 0.0.0.0)对外暴露 —— 见 Listen 的安全说明。
// 输入回注通过 OnInput/OnReview 回调解耦,由 tui/run.go 注入(内部调 program.Send)。
type Server struct {
	hub   *Hub
	token string
	ln    net.Listener
	srv   *http.Server

	// 回调:浏览器提交输入 / review 确认时触发。由调用方注入。
	OnInput     func(text string)
	OnReview    func(approve bool)
	OnAskAnswer func(answer string) // AskUser 选择题:浏览器回传的答案 JSON
	OnInterrupt func()              // 浏览器点"停止":中断当前执行(等价终端 Esc)

	// OnListFiles 返回工作区文件相对路径列表,供前端 @ 文件选择器使用。由调用方注入
	// (web 包不能依赖 tui —— tui 已依赖 web,会成环;遍历逻辑在 tui 侧,经回调注入)。
	OnListFiles func() []string

	// 控制类回调:浏览器点按钮 → 注入回 TUI(同样经 program.Send,走相同 Update 逻辑)。
	OnNewSession     func()             // 新建会话(/new)
	OnSwitchSession  func(id string)    // 切换会话(/sessions 点击)
	OnRenameSession  func(id, title string) // 重命名会话
	OnDeleteSession  func(id string)    // 删除会话
	OnSetModel       func(role string) // 路由 auto/flash/pro
	OnSetMode        func(mode string) // 权限模式 plan/auto/review
	OnSetSandbox     func(mode string) // 沙箱 off/native/docker
	OnSetWorkingMode func(mode string) // 工作模式 karpathy/openspec/superpowers
	OnSetLang        func(lang string) // 界面语言 zh/en

	// 左栏操作:压缩会话 / MCP 增删(均需动 live agent 状态,经回调注入)。
	OnCompact   func()                 // 手动压缩当前会话(等价 /compact)
	OnMcpAdd    func(cfg mcp.ServerConfig) // 添加 MCP server 并连接
	OnMcpDelete func(name string)          // 删除 MCP server 并断连
}

// NewServer 创建 Server,token 先随机兜底(可被 SetToken 覆盖成 session 固定令牌)。
func NewServer(hub *Hub) *Server {
	return &Server{hub: hub, token: randomToken()}
}

// SetToken 用一个固定令牌覆盖随机令牌(空串忽略)。须在 Listen 前调用,返回的 URL 才带该令牌。
// 给 tui/run.go 注入 session 级固定 token 用,保证同一 workspace 的访问 URL 跨重启稳定。
func (s *Server) SetToken(t string) {
	if t != "" {
		s.token = t
	}
}

// Listen 在 host:port 上监听(host 空 => 127.0.0.1,仅本机;port=0 => 随机空闲端口),
// 返回带 token 的访问 URL。
//
// 安全:该面板可向 agent 注入输入、批准工具调用、关沙箱 —— 等价于在本机执行代码,且为明文 HTTP、
// 令牌在 URL 里。host 设成 0.0.0.0 或某网卡 IP 会把它暴露到局域网/公网,仅应在可信网络这么做。
// 默认 127.0.0.1 不对外。
func (s *Server) Listen(host string, port int) (string, error) {
	if host == "" {
		host = "127.0.0.1"
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return "", err
	}
	s.ln = ln
	actual := ln.Addr().(*net.TCPAddr).Port
	return fmt.Sprintf("http://%s:%d/?t=%s", displayHost(host), actual, s.token), nil
}

// displayHost 把绑定地址转成给用户点击的 URL host:通配地址(0.0.0.0 / ::)解析成本机第一个
// 非回环 IPv4(便于局域网设备直接访问),其余原样返回。
func displayHost(bind string) string {
	if bind == "0.0.0.0" || bind == "::" {
		return localIP()
	}
	return bind
}

// localIP 返回本机第一个非回环 IPv4;找不到则回退 127.0.0.1。
// best-effort:多网卡(docker 网桥 / VPN / 多 NIC)时取到的不一定是用户期望的那张,仅用于 URL 回显,
// 不影响实际监听(已绑在所有接口)。
func localIP() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, a := range addrs {
		if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() && ipnet.IP.To4() != nil {
			return ipnet.IP.String()
		}
	}
	return "127.0.0.1"
}

// Relisten 关掉当前监听,在新的 host:port 重新监听并起服务,返回新访问 URL。
// token / hub 不变(已打开的浏览器刷新即可继续用)。供 /web-config 改完配置后热生效,免重启。
func (s *Server) Relisten(host string, port int) (string, error) {
	if s.srv != nil {
		_ = s.srv.Close() // 关旧 http.Server,旧 Serve goroutine 随之返回
		s.srv = nil
	} else if s.ln != nil {
		_ = s.ln.Close()
	}
	url, err := s.Listen(host, port)
	if err != nil {
		return "", err
	}
	go func() { _ = s.Serve() }()
	return url, nil
}

// Serve 启动 HTTP 服务(阻塞)。通常放进 goroutine。Close 后返回。
func (s *Server) Serve() error {
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/events", s.handleEvents)
	mux.HandleFunc("/api/input", s.handleInput)
	mux.HandleFunc("/api/review", s.handleReview)
	mux.HandleFunc("/api/ask-answer", s.handleAskAnswer)
	mux.HandleFunc("/api/interrupt", s.handleInterrupt)
	mux.HandleFunc("/api/state", s.handleState)
	mux.HandleFunc("/api/files", s.handleFiles)
	mux.HandleFunc("/api/new", s.handleNew)
	mux.HandleFunc("/api/switch", s.handleSwitch)
	mux.HandleFunc("/api/session-rename", s.handleSessionRename)
	mux.HandleFunc("/api/session-delete", s.handleSessionDelete)
	mux.HandleFunc("/api/model", s.handleModel)
	mux.HandleFunc("/api/mode", s.handleMode)
	mux.HandleFunc("/api/sandbox", s.handleSandbox)
	mux.HandleFunc("/api/workingmode", s.handleWorkingMode)
	mux.HandleFunc("/api/lang", s.handleLang)
	// 左栏操作:压缩会话 / MCP 管理 / Skill 管理
	mux.HandleFunc("/api/compact", s.handleCompact)
	mux.HandleFunc("/api/mcp-list", s.handleMcpList)
	mux.HandleFunc("/api/mcp-add", s.handleMcpAdd)
	mux.HandleFunc("/api/mcp-delete", s.handleMcpDelete)
	mux.HandleFunc("/api/skill-list", s.handleSkillList)
	mux.HandleFunc("/api/skill-search", s.handleSkillSearch)
	mux.HandleFunc("/api/skill-install", s.handleSkillInstall)
	mux.HandleFunc("/api/skill-install-source", s.handleSkillInstallSource)
	mux.HandleFunc("/api/skill-delete", s.handleSkillDelete)
	s.srv = &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	err := s.srv.Serve(s.ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// Close 关闭服务。
func (s *Server) Close() {
	if s.srv != nil {
		_ = s.srv.Close()
	} else if s.ln != nil {
		_ = s.ln.Close()
	}
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "deepx" // 极端兜底,实际 crypto/rand 不会失败
	}
	return hex.EncodeToString(b)
}

// authed 校验请求是否带正确 token(query ?t= 或 cookie)。
func (s *Server) authed(r *http.Request) bool {
	if r.URL.Query().Get("t") == s.token {
		return true
	}
	if c, err := r.Cookie("deepx_token"); err == nil && c.Value == s.token {
		return true
	}
	return false
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	// 首次带 token 访问 → 种 cookie,后续 API 请求免带 ?t=。
	if r.URL.Query().Get("t") == s.token {
		http.SetCookie(w, &http.Cookie{
			Name: "deepx_token", Value: s.token, Path: "/", HttpOnly: true,
		})
	}
	// 禁止浏览器缓存内嵌静态资源 —— go:embed 文件 modtime 为零值,否则浏览器会一直用旧的
	// app.js/css,deepx 升级后前端不更新(用户曾遇到改了算法但页面还是旧逻辑)。
	w.Header().Set("Cache-Control", "no-store")
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		http.Error(w, "embed error", http.StatusInternalServerError)
		return
	}
	http.FileServer(http.FS(sub)).ServeHTTP(w, r)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, snap, unsub := s.hub.Subscribe()
	defer unsub()

	writeSSE(w, "snapshot", snap)
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-ch:
			if !ok {
				return // hub 关闭了该客户端(慢消费者),浏览器会自动重连
			}
			writeSSE(w, "delta", ev)
			flusher.Flush()
		}
	}
}

func writeSSE(w http.ResponseWriter, event string, payload any) {
	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
}

func (s *Server) handleInput(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.OnInput != nil && body.Text != "" {
		s.OnInput(body.Text)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleReview(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Approve bool `json:"approve"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.OnReview != nil {
		s.OnReview(body.Approve)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAskAnswer 接收浏览器对 AskUser 选择题的作答(answer 为答案 JSON 字符串),回注 TUI。
func (s *Server) handleAskAnswer(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.OnAskAnswer != nil {
		s.OnAskAnswer(body.Answer)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleInterrupt 接收浏览器的"停止"请求,中断当前执行(无 body)。
func (s *Server) handleInterrupt(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.OnInterrupt != nil {
		s.OnInterrupt()
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleCompact 触发手动压缩当前会话(无 body),经回调回注 TUI。
func (s *Server) handleCompact(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	if s.OnCompact != nil {
		s.OnCompact()
	}
	w.WriteHeader(http.StatusNoContent)
}

// mcpItem 是给前端的 MCP server 列表项(从 ~/.deepx/mcp.json 读)。
type mcpItem struct {
	Name      string `json:"name"`
	Transport string `json:"transport"` // stdio | http
	Detail    string `json:"detail"`    // command [args] 或 url
}

// handleMcpList 返回已配置的 MCP server 列表(GET)。
func (s *Server) handleMcpList(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	cfgs, _ := mcp.LoadConfig()
	out := make([]mcpItem, 0, len(cfgs))
	for _, c := range cfgs {
		it := mcpItem{Name: c.Name, Transport: "stdio", Detail: strings.TrimSpace(c.Command + " " + strings.Join(c.Args, " "))}
		if c.URL != "" {
			it.Transport = "http"
			it.Detail = c.URL
		}
		out = append(out, it)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleMcpAdd 添加 MCP server。body: {name, command, args(空格分隔), url}。stdio 与 http 二选一。
func (s *Server) handleMcpAdd(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Name    string `json:"name"`
		Command string `json:"command"`
		Args    string `json:"args"`
		URL     string `json:"url"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" || (strings.TrimSpace(body.Command) == "" && strings.TrimSpace(body.URL) == "") {
		http.Error(w, "需要 name 且 command 或 url 至少其一", http.StatusBadRequest)
		return
	}
	cfg := mcp.ServerConfig{Name: name}
	if u := strings.TrimSpace(body.URL); u != "" {
		cfg.URL = u
	} else {
		cfg.Command = strings.TrimSpace(body.Command)
		if a := strings.Fields(body.Args); len(a) > 0 {
			cfg.Args = a
		}
	}
	if s.OnMcpAdd != nil {
		s.OnMcpAdd(cfg)
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMcpDelete 删除 MCP server。body: {name}。
func (s *Server) handleMcpDelete(w http.ResponseWriter, r *http.Request) {
	s.postField(w, r, "name", func(name string) {
		if s.OnMcpDelete != nil && name != "" {
			s.OnMcpDelete(name)
		}
	})
}

// skillItem 是给前端的已装 skill 列表项。Builtin=true 的内置 skill 不可删。
type skillItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Builtin     bool   `json:"builtin"`
}

// handleSkillList 返回已安装的 skill 列表(GET);标注哪些是内置(不可删)。
func (s *Server) handleSkillList(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	metas, _ := skill.InstalledList()
	builtin := skill.BuiltinNames()
	out := make([]skillItem, 0, len(metas))
	for _, m := range metas {
		out = append(out, skillItem{Name: m.Name, Description: m.Description, Builtin: builtin[m.Name]})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleSkillInstall 从 GitHub URL / 本地路径安装 skill(同步,可能耗时几秒)。
// catalog 每轮重建,装完下一轮自动可见,无需回注 TUI。
func (s *Server) handleSkillInstall(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Src string `json:"src"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	src := strings.TrimSpace(body.Src)
	resp := map[string]any{"ok": true}
	if src == "" {
		resp = map[string]any{"ok": false, "error": "src 为空"}
	} else if name, err := skill.Install(src); err != nil {
		resp = map[string]any{"ok": false, "error": err.Error()}
	} else {
		resp = map[string]any{"ok": true, "name": name}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// skillSearchItem 是 Clawhub 搜索结果项。
type skillSearchItem struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
	Stars       int    `json:"stars"`
	Downloads   int    `json:"downloads"`
	SourceID    string `json:"sourceId"`
	RemoteRef   string `json:"remoteRef"`
}

// handleSkillSearch 从 Clawhub 搜索 skill。body: {query}。
func (s *Server) handleSkillSearch(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		Query string `json:"query"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	infos, err := skill.SearchSkills(ctx, strings.TrimSpace(body.Query), "")
	if err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error(), "results": []skillSearchItem{}})
		return
	}
	out := make([]skillSearchItem, 0, len(infos))
	for _, i := range infos {
		out = append(out, skillSearchItem{
			Name: i.Name, Description: i.Description, Author: i.Author,
			Stars: i.Stars, Downloads: i.Downloads, SourceID: i.SourceID, RemoteRef: i.RemoteRef,
		})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"results": out})
}

// handleSkillInstallSource 安装一个搜索结果(Clawhub)。body: {sourceId, remoteRef}。
func (s *Server) handleSkillInstallSource(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		SourceID  string `json:"sourceId"`
		RemoteRef string `json:"remoteRef"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	sourceID := strings.TrimSpace(body.SourceID)
	if sourceID == "" {
		sourceID = skill.SourceIDClawhub
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	w.Header().Set("Content-Type", "application/json")
	if name, err := skill.InstallFromSource(ctx, sourceID, strings.TrimSpace(body.RemoteRef)); err != nil {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": err.Error()})
	} else {
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "name": name})
	}
}

// handleSkillDelete 删除已安装 skill(直接改磁盘,下一轮 catalog 重建生效)。
// 内置 skill 不允许删除(后端兜底,即使前端被绕过)。
func (s *Server) handleSkillDelete(w http.ResponseWriter, r *http.Request) {
	s.postField(w, r, "name", func(name string) {
		if name != "" && !skill.BuiltinNames()[name] {
			_ = skill.Delete(name)
		}
	})
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.hub.SnapshotCopy())
}

// handleFiles 返回工作区文件相对路径列表(JSON 数组),供前端 @ 文件选择器过滤。
func (s *Server) handleFiles(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var files []string
	if s.OnListFiles != nil {
		files = s.OnListFiles()
	}
	if files == nil {
		files = []string{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(files)
}

// postField 是控制类 POST 端点的公共骨架:校验 auth + method,解析 body 里的 field 字段,
// 调回调。field 为空字符串时表示无 body(如新建会话),cb 直接调。
func (s *Server) postField(w http.ResponseWriter, r *http.Request, field string, cb func(string)) {
	if !s.authed(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	val := ""
	if field != "" {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		val = body[field]
	}
	if cb != nil {
		cb(val)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleNew(w http.ResponseWriter, r *http.Request) {
	s.postField(w, r, "", func(string) {
		if s.OnNewSession != nil {
			s.OnNewSession()
		}
	})
}

func (s *Server) handleSwitch(w http.ResponseWriter, r *http.Request) {
	s.postField(w, r, "id", func(id string) {
		if s.OnSwitchSession != nil && id != "" {
			s.OnSwitchSession(id)
		}
	})
}

func (s *Server) handleSessionRename(w http.ResponseWriter, r *http.Request) {
	if !s.authed(r) || r.Method != http.MethodPost {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var body struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if s.OnRenameSession != nil && body.ID != "" {
		s.OnRenameSession(body.ID, body.Title)
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleSessionDelete(w http.ResponseWriter, r *http.Request) {
	s.postField(w, r, "id", func(id string) {
		if s.OnDeleteSession != nil && id != "" {
			s.OnDeleteSession(id)
		}
	})
}

func (s *Server) handleLang(w http.ResponseWriter, r *http.Request) {
	s.postField(w, r, "lang", func(lang string) {
		if s.OnSetLang != nil && lang != "" {
			s.OnSetLang(lang)
		}
	})
}

func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	s.postField(w, r, "role", func(role string) {
		if s.OnSetModel != nil && role != "" {
			s.OnSetModel(role)
		}
	})
}

func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	s.postField(w, r, "mode", func(m string) {
		if s.OnSetMode != nil && m != "" {
			s.OnSetMode(m)
		}
	})
}

func (s *Server) handleSandbox(w http.ResponseWriter, r *http.Request) {
	s.postField(w, r, "mode", func(m string) {
		if s.OnSetSandbox != nil && m != "" {
			s.OnSetSandbox(m)
		}
	})
}

func (s *Server) handleWorkingMode(w http.ResponseWriter, r *http.Request) {
	s.postField(w, r, "mode", func(m string) {
		if s.OnSetWorkingMode != nil && m != "" {
			s.OnSetWorkingMode(m)
		}
	})
}
