package web

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"deepx/agent"
)

// TestHubApply 验证 hub reducer:user→token 开 assistant 气泡,tool_call/result 配对,plan/usage 更新。
func TestHubApply(t *testing.T) {
	h := NewHub("flash-x", "pro-y", "/tmp/ws", "zh")

	h.Broadcast(Event{Kind: "user_message", Text: "hi"})
	h.Broadcast(Event{Kind: "token", Text: "hel"})
	h.Broadcast(Event{Kind: "token", Text: "lo"})
	h.Broadcast(Event{Kind: "tool_call", Name: "Bash", Args: "ls"})
	if got := h.SnapshotCopy(); len(got.ToolCalls) != 1 || got.ToolCalls[0].ID == "" {
		t.Fatalf("tool_call should append a running tool with ID, got %+v", got.ToolCalls)
	}
	yes := true
	h.Broadcast(Event{Kind: "tool_result", Name: "Bash", Output: "a\nb", Success: &yes})
	h.Broadcast(Event{Kind: "usage", Usage: &Usage{PromptTokens: 100, CompletionTokens: 20, CacheHit: 80, CacheMiss: 20}})
	h.Broadcast(Event{Kind: "done"})

	s := h.SnapshotCopy()
	// 工具调用现在也内联进对话流:user / assistant / tool 三条。
	if len(s.Messages) != 3 || s.Messages[0].Role != "user" || s.Messages[1].Role != "assistant" || s.Messages[2].Role != "tool" {
		t.Fatalf("messages wrong: %+v", s.Messages)
	}
	if s.Messages[1].Content != "hello" {
		t.Fatalf("assistant content = %q, want hello", s.Messages[1].Content)
	}
	if s.Messages[2].Name != "Bash" || s.Messages[2].Status != "done" || s.Messages[2].Output != "a\nb" {
		t.Fatalf("inline tool message wrong: %+v", s.Messages[2])
	}
	if len(s.ToolCalls) != 1 || s.ToolCalls[0].Status != "done" || s.ToolCalls[0].Output != "a\nb" {
		t.Fatalf("toolcall wrong: %+v", s.ToolCalls)
	}
	if s.Usage == nil || s.Usage.PromptTokens != 100 {
		t.Fatalf("usage wrong: %+v", s.Usage)
	}
	if s.Streaming {
		t.Fatalf("streaming should be false after done")
	}
}

// TestHubAskQuestions 验证 AskUser 选择题在快照里的维护:ask_request 写入、ask_resolved 清空、
// 新一轮 user_message 也清空(避免上一轮残留的待答问题串到下一轮)。
func TestHubAskQuestions(t *testing.T) {
	h := NewHub("flash", "pro", "/tmp/ws", "zh")
	qs := []agent.AskQuestion{{
		Question: "用哪个数据库?",
		Options:  []agent.AskOption{{Label: "PostgreSQL", Value: "pg"}, {Label: "MySQL", Value: "mysql"}},
	}}

	h.Broadcast(Event{Kind: "ask_request", Questions: qs})
	if s := h.SnapshotCopy(); len(s.AskQuestions) != 1 || s.AskQuestions[0].Options[1].Value != "mysql" {
		t.Fatalf("ask_request should populate snapshot, got %+v", s.AskQuestions)
	}

	h.Broadcast(Event{Kind: "ask_resolved"})
	if s := h.SnapshotCopy(); len(s.AskQuestions) != 0 {
		t.Fatalf("ask_resolved should clear snapshot, got %+v", s.AskQuestions)
	}

	// 新一轮 user_message 应清空残留待答问题
	h.Broadcast(Event{Kind: "ask_request", Questions: qs})
	h.Broadcast(Event{Kind: "user_message", Text: "hi"})
	if s := h.SnapshotCopy(); len(s.AskQuestions) != 0 {
		t.Fatalf("new turn should clear pending questions, got %+v", s.AskQuestions)
	}

	// interrupted 应停掉 streaming 并清空待答问题
	h.Broadcast(Event{Kind: "ask_request", Questions: qs})
	h.Broadcast(Event{Kind: "interrupted"})
	if s := h.SnapshotCopy(); s.Streaming || len(s.AskQuestions) != 0 {
		t.Fatalf("interrupted should stop streaming + clear questions, got streaming=%v q=%+v", s.Streaming, s.AskQuestions)
	}
}

// TestServerAuthAndCallbacks 验证 token 鉴权 + input/review 回调 + SSE 快照。
func TestServerAuthAndCallbacks(t *testing.T) {
	h := NewHub("flash", "pro", "/tmp/ws", "en")
	srv := NewServer(h)
	gotInput := make(chan string, 1)
	gotReview := make(chan bool, 1)
	gotAsk := make(chan string, 1)
	gotInterrupt := make(chan struct{}, 1)
	srv.OnInput = func(s string) { gotInput <- s }
	srv.OnReview = func(b bool) { gotReview <- b }
	srv.OnAskAnswer = func(s string) { gotAsk <- s }
	srv.OnInterrupt = func() { gotInterrupt <- struct{}{} }

	rawURL, err := srv.Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve() }()
	defer srv.Close()

	u, _ := url.Parse(rawURL)
	base := "http://" + u.Host
	token := u.Query().Get("t")
	if token == "" {
		t.Fatalf("URL missing token: %s", rawURL)
	}

	// 无 token → 403
	resp, err := http.Get(base + "/api/state")
	if err != nil {
		t.Fatalf("get state no-token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no-token state want 403, got %d", resp.StatusCode)
	}

	// 带 token → 200
	resp, err = http.Get(base + "/api/state?t=" + token)
	if err != nil {
		t.Fatalf("get state: %v", err)
	}
	var snap Snapshot
	_ = json.NewDecoder(resp.Body).Decode(&snap)
	resp.Body.Close()
	if resp.StatusCode != 200 || snap.Models.Flash != "flash" {
		t.Fatalf("state want 200+flash, got %d %+v", resp.StatusCode, snap.Models)
	}

	// POST /api/input → 回调拿到文本
	postJSON(t, base+"/api/input?t="+token, map[string]any{"text": "你好"})
	select {
	case got := <-gotInput:
		if got != "你好" {
			t.Fatalf("OnInput got %q", got)
		}
	case <-time.After(time.Second):
		t.Fatal("OnInput not called")
	}

	// POST /api/review → 回调拿到 approve
	postJSON(t, base+"/api/review?t="+token, map[string]any{"approve": true})
	select {
	case got := <-gotReview:
		if !got {
			t.Fatal("OnReview got false, want true")
		}
	case <-time.After(time.Second):
		t.Fatal("OnReview not called")
	}

	// POST /api/ask-answer → 回调拿到答案 JSON
	answer := `{"answers":[{"question":"用哪个库?","selected":["mysql"]}]}`
	postJSON(t, base+"/api/ask-answer?t="+token, map[string]any{"answer": answer})
	select {
	case got := <-gotAsk:
		if got != answer {
			t.Fatalf("OnAskAnswer got %q, want %q", got, answer)
		}
	case <-time.After(time.Second):
		t.Fatal("OnAskAnswer not called")
	}

	// POST /api/interrupt → 回调被触发(无 body)
	postJSON(t, base+"/api/interrupt?t="+token, map[string]any{})
	select {
	case <-gotInterrupt:
	case <-time.After(time.Second):
		t.Fatal("OnInterrupt not called")
	}

	// GET / 应返回内嵌的 index.html(验证 go:embed 链路)
	resp, err = http.Get(base + "/?t=" + token)
	if err != nil {
		t.Fatalf("get index: %v", err)
	}
	idxBody, _ := readAllString(resp)
	resp.Body.Close()
	if resp.StatusCode != 200 || !strings.Contains(idxBody, "deepx-code") {
		t.Fatalf("index want 200 + embedded html, got %d (len=%d)", resp.StatusCode, len(idxBody))
	}

	// SSE /api/events 首帧应为 snapshot
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+"/api/events?t="+token, nil)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sse: %v", err)
	}
	defer resp.Body.Close()
	br := bufio.NewReader(resp.Body)
	line, _ := br.ReadString('\n')
	if !strings.HasPrefix(line, "event: snapshot") {
		t.Fatalf("first SSE line = %q, want 'event: snapshot'", line)
	}
}

// TestHandleFiles 验证 /api/files 鉴权 + OnListFiles 回调结果以 JSON 数组返回。
func TestHandleFiles(t *testing.T) {
	h := NewHub("flash", "pro", "/tmp/ws", "en")
	srv := NewServer(h)
	srv.OnListFiles = func() []string { return []string{"tui/model.go", "README.md"} }

	rawURL, err := srv.Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve() }()
	defer srv.Close()

	u, _ := url.Parse(rawURL)
	base := "http://" + u.Host
	token := u.Query().Get("t")

	// 无 token → 403
	resp, err := http.Get(base + "/api/files")
	if err != nil {
		t.Fatalf("get files no-token: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no-token files want 403, got %d", resp.StatusCode)
	}

	// 带 token → 回调列表
	resp, err = http.Get(base + "/api/files?t=" + token)
	if err != nil {
		t.Fatalf("get files: %v", err)
	}
	var files []string
	_ = json.NewDecoder(resp.Body).Decode(&files)
	resp.Body.Close()
	if resp.StatusCode != 200 || len(files) != 2 || files[0] != "tui/model.go" {
		t.Fatalf("files want 200 + 2 entries, got %d %v", resp.StatusCode, files)
	}
}

// TestControlEndpoints 验证新增的控制类端点(new/switch/mode/sandbox/workingmode)鉴权 + 回调,
// 以及 hub 对控制态事件 / session_loaded 的快照更新。
func TestControlEndpoints(t *testing.T) {
	h := NewHub("flash", "pro", "/tmp/ws", "zh")
	srv := NewServer(h)
	gotNew := make(chan struct{}, 1)
	gotSwitch := make(chan string, 1)
	gotRename := make(chan [2]string, 1)
	gotDelete := make(chan string, 1)
	gotModel := make(chan string, 1)
	gotMode := make(chan string, 1)
	gotSandbox := make(chan string, 1)
	gotWM := make(chan string, 1)
	gotLang := make(chan string, 1)
	srv.OnSetLang = func(l string) { gotLang <- l }
	srv.OnNewSession = func() { gotNew <- struct{}{} }
	srv.OnSwitchSession = func(id string) { gotSwitch <- id }
	srv.OnRenameSession = func(id, title string) { gotRename <- [2]string{id, title} }
	srv.OnDeleteSession = func(id string) { gotDelete <- id }
	srv.OnSetModel = func(r string) { gotModel <- r }
	srv.OnSetMode = func(m string) { gotMode <- m }
	srv.OnSetSandbox = func(m string) { gotSandbox <- m }
	srv.OnSetWorkingMode = func(m string) { gotWM <- m }

	rawURL, err := srv.Listen("127.0.0.1", 0)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve() }()
	defer srv.Close()
	u, _ := url.Parse(rawURL)
	base := "http://" + u.Host
	token := u.Query().Get("t")

	// 无 token → 403(以 /api/mode 为代表)
	resp, _ := http.Post(base+"/api/mode", "application/json", strings.NewReader(`{"mode":"plan"}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("no-token mode want 403, got %d", resp.StatusCode)
	}

	postJSON(t, base+"/api/new?t="+token, map[string]any{})
	select {
	case <-gotNew:
	case <-time.After(time.Second):
		t.Fatal("OnNewSession not called")
	}

	postJSON(t, base+"/api/switch?t="+token, map[string]any{"id": "conv-123"})
	if got := <-gotSwitch; got != "conv-123" {
		t.Fatalf("OnSwitchSession got %q", got)
	}
	postJSON(t, base+"/api/session-rename?t="+token, map[string]any{"id": "c1", "title": "新名字"})
	if got := <-gotRename; got[0] != "c1" || got[1] != "新名字" {
		t.Fatalf("OnRenameSession got %v", got)
	}
	postJSON(t, base+"/api/session-delete?t="+token, map[string]any{"id": "c1"})
	if got := <-gotDelete; got != "c1" {
		t.Fatalf("OnDeleteSession got %q", got)
	}
	postJSON(t, base+"/api/model?t="+token, map[string]any{"role": "pro"})
	if got := <-gotModel; got != "pro" {
		t.Fatalf("OnSetModel got %q", got)
	}
	postJSON(t, base+"/api/mode?t="+token, map[string]any{"mode": "plan"})
	if got := <-gotMode; got != "plan" {
		t.Fatalf("OnSetMode got %q", got)
	}
	postJSON(t, base+"/api/sandbox?t="+token, map[string]any{"mode": "docker"})
	if got := <-gotSandbox; got != "docker" {
		t.Fatalf("OnSetSandbox got %q", got)
	}
	postJSON(t, base+"/api/workingmode?t="+token, map[string]any{"mode": "openspec"})
	if got := <-gotWM; got != "openspec" {
		t.Fatalf("OnSetWorkingMode got %q", got)
	}
	postJSON(t, base+"/api/lang?t="+token, map[string]any{"lang": "en"})
	if got := <-gotLang; got != "en" {
		t.Fatalf("OnSetLang got %q", got)
	}

	// 控制态事件应进快照
	h.Broadcast(Event{Kind: "vendor", Text: "api.deepseek.com"})
	h.Broadcast(Event{Kind: "routing", Text: "flash"})
	h.Broadcast(Event{Kind: "mode", Text: "plan"})
	h.Broadcast(Event{Kind: "sandbox", Text: "off"})
	h.Broadcast(Event{Kind: "working_mode", Text: "superpowers"})
	h.Broadcast(Event{Kind: "sessions", Sessions: []SessionInfo{{ID: "a", Title: "T", Active: true}}})
	s := h.SnapshotCopy()
	if s.Vendor != "api.deepseek.com" || s.Routing != "flash" || s.Mode != "plan" || s.Sandbox != "off" || s.WorkingMode != "superpowers" {
		t.Fatalf("control state not in snapshot: %+v", s)
	}

	// 计划 / 步骤分流:createplan → Step,todo → Plan
	h.Broadcast(Event{Kind: "plan", PlanKind: "createplan", Plan: []PlanNode{{ID: "s1", Title: "step1"}}})
	h.Broadcast(Event{Kind: "plan", PlanKind: "todo", Plan: []PlanNode{{ID: "t1", Title: "todo1"}}})
	h.Broadcast(Event{Kind: "plan_status", ID: "s1", Status: "done"})
	s = h.SnapshotCopy()
	if len(s.Step) != 1 || s.Step[0].Status != "done" {
		t.Fatalf("step not routed/updated: %+v", s.Step)
	}
	if len(s.Plan) != 1 || s.Plan[0].ID != "t1" {
		t.Fatalf("plan(todo) not routed: %+v", s.Plan)
	}
	if len(s.Sessions) != 1 || !s.Sessions[0].Active {
		t.Fatalf("sessions not in snapshot: %+v", s.Sessions)
	}

	// session_loaded 应替换消息并清空派生态
	h.Broadcast(Event{Kind: "user_message", Text: "old"})
	h.Broadcast(Event{Kind: "session_loaded", Messages: []Message{{Role: "user", Content: "新会话第一条"}}})
	s = h.SnapshotCopy()
	if len(s.Messages) != 1 || s.Messages[0].Content != "新会话第一条" || s.Streaming {
		t.Fatalf("session_loaded did not reset messages: %+v streaming=%v", s.Messages, s.Streaming)
	}
}

func readAllString(resp *http.Response) (string, error) {
	var b bytes.Buffer
	_, err := b.ReadFrom(resp.Body)
	return b.String(), err
}

func postJSON(t *testing.T, url string, body map[string]any) {
	t.Helper()
	data, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("post %s: %v", url, err)
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		t.Fatalf("post %s status %d", url, resp.StatusCode)
	}
}
