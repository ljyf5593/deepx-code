package web

import (
	"strconv"
	"sync"

	"deepx/agent"
)

// Message 是聊天窗口里的一条消息(只在左栏渲染)。role: "user" | "assistant" | "tool"。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	// 以下仅 role=="tool" 有意义:把工具调用内联到对话流里渲染(对齐 TUI)。
	ID     string `json:"id,omitempty"`
	Name   string `json:"name,omitempty"`
	Args   string `json:"args,omitempty"`
	Status string `json:"status,omitempty"` // running | done | failed
	Output string `json:"output,omitempty"`
}

// ToolCallView 是右栏实时工具调用列表的一项。
type ToolCallView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Args   string `json:"args"`
	Status string `json:"status"` // running | done | failed
	Output string `json:"output,omitempty"`
}

// ModelsInfo 右栏顶部展示的模型信息。
type ModelsInfo struct {
	Flash      string `json:"flash"`
	Pro        string `json:"pro"`
	ActiveRole string `json:"activeRole"` // "flash" | "pro"
}

// ReviewInfo 待人工确认的工具调用(review 模式)。nil = 当前没有待确认。
type ReviewInfo struct {
	Name string `json:"name"`
	Args string `json:"args"`
}

// SessionInfo 是会话列表里的一项(对齐 TUI 的 /sessions),供前端点击切换。
type SessionInfo struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Active bool   `json:"active"`
}

// Snapshot 是 web dashboard 的完整状态快照,新连接的浏览器先收到它,再收实时增量。
type Snapshot struct {
	Messages      []Message      `json:"messages"`
	Plan          []PlanNode     `json:"plan"` // 计划(Todo)
	Step          []PlanNode     `json:"step"` // 步骤(CreatePlan DAG)
	ToolCalls     []ToolCallView `json:"toolCalls"`
	Usage         *Usage         `json:"usage"`
	Streaming     bool           `json:"streaming"`
	Models        ModelsInfo     `json:"models"`
	Workspace     string         `json:"workspace"`
	Lang          string         `json:"lang"` // "zh" | "en",跟 TUI 同步
	ReviewPending *ReviewInfo    `json:"reviewPending"`
	AskQuestions  []agent.AskQuestion `json:"askQuestions"` // 非空 = 有待答选择题

	// 控制态,与 TUI 对齐:路由 / 权限模式 / 沙箱 / 工作模式 / 代码图谱状态 + 会话列表。
	Vendor      string        `json:"vendor"`      // 模型厂商(api host)
	Routing     string        `json:"routing"`     // auto | flash | pro(模型路由 pin)
	Mode        string        `json:"mode"`        // plan | auto | review
	Sandbox     string        `json:"sandbox"`     // off | native | docker
	WorkingMode string        `json:"workingMode"` // karpathy | openspec | superpowers
	CodeGraph   string        `json:"codegraph"`   // 代码图谱状态 token
	Sessions    []SessionInfo `json:"sessions"`
}

const clientBufferSize = 512

// Hub 是 SSE 广播中心:维护一份服务端会话快照(供新连接初始化),
// 并把实时增量事件 fan-out 给所有已连接的浏览器。
//
// 所有有状态逻辑(开/合 assistant 气泡、工具调用配对、plan 更新)集中在 apply 里,
// 浏览器端用同样的 reducer 处理增量,保证两边一致。
type Hub struct {
	mu      sync.Mutex
	clients map[chan Event]struct{}
	snap    Snapshot

	openAssistant int // 当前正在流式的 assistant 消息下标;-1 表示没有
	toolSeq       int // 工具调用 ID 自增
}

// NewHub 创建 Hub。flashModel/proModel/workspace/lang 是初始展示信息(lang 后续可由 lang 事件更新)。
func NewHub(flashModel, proModel, workspace, lang string) *Hub {
	return &Hub{
		clients: make(map[chan Event]struct{}),
		snap: Snapshot{
			Messages:  []Message{},
			Plan:      []PlanNode{},
			Step:      []PlanNode{},
			ToolCalls: []ToolCallView{},
			Models:    ModelsInfo{Flash: flashModel, Pro: proModel, ActiveRole: "flash"},
			Workspace: workspace,
			Lang:      lang,
			Sessions:  []SessionInfo{},
			// 占位默认值;TUI 启动时会广播权威控制态覆盖它们。
			Routing:     "auto",
			Mode:        "review",
			Sandbox:     "native",
			WorkingMode: "karpathy",
		},
		openAssistant: -1,
	}
}

// Broadcast 把一个事件应用到快照并 fan-out 给所有客户端。
// apply 可能会丰富事件(如给工具调用分配 ID),fan-out 的是丰富后的版本,确保前后端一致。
func (h *Hub) Broadcast(ev Event) {
	if h == nil {
		return
	}
	h.mu.Lock()
	enriched := h.apply(ev)
	for ch := range h.clients {
		select {
		case ch <- enriched:
		default:
			// 客户端缓冲满(慢消费者)→ 关闭它,浏览器 EventSource 会自动重连并重新拉快照。
			close(ch)
			delete(h.clients, ch)
		}
	}
	h.mu.Unlock()
}

// Subscribe 注册一个新客户端,返回其事件 channel、当前快照、以及注销函数。
// 调用方应先把 snapshot 发给浏览器,再从 channel 持续读增量。
func (h *Hub) Subscribe() (<-chan Event, Snapshot, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan Event, clientBufferSize)
	h.clients[ch] = struct{}{}
	snap := h.copySnapshotLocked()
	unsub := func() {
		h.mu.Lock()
		if _, ok := h.clients[ch]; ok {
			delete(h.clients, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
	return ch, snap, unsub
}

// SnapshotCopy 返回当前快照的拷贝(给 GET /api/state)。
func (h *Hub) SnapshotCopy() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.copySnapshotLocked()
}

func (h *Hub) copySnapshotLocked() Snapshot {
	s := h.snap
	s.Messages = append([]Message(nil), h.snap.Messages...)
	s.Plan = append([]PlanNode(nil), h.snap.Plan...)
	s.Step = append([]PlanNode(nil), h.snap.Step...)
	s.ToolCalls = append([]ToolCallView(nil), h.snap.ToolCalls...)
	s.Sessions = append([]SessionInfo(nil), h.snap.Sessions...)
	if h.snap.Usage != nil {
		u := *h.snap.Usage
		s.Usage = &u
	}
	s.AskQuestions = append([]agent.AskQuestion(nil), h.snap.AskQuestions...)
	if h.snap.ReviewPending != nil {
		r := *h.snap.ReviewPending
		s.ReviewPending = &r
	}
	return s
}

// apply 把事件更新到快照,返回(可能被丰富过的)事件用于 fan-out。必须在持锁状态下调用。
func (h *Hub) apply(ev Event) Event {
	switch ev.Kind {
	case "user_message":
		// 新回合开始:落一条 user 消息,清空上一轮的 plan / 工具列表,重置 assistant 气泡。
		h.snap.Messages = append(h.snap.Messages, Message{Role: "user", Content: ev.Text})
		h.snap.Plan = []PlanNode{}
		h.snap.Step = []PlanNode{}
		h.snap.ToolCalls = []ToolCallView{}
		h.snap.Usage = nil
		h.snap.ReviewPending = nil
		h.snap.AskQuestions = nil
		h.snap.Streaming = true
		h.openAssistant = -1

	case "token":
		if h.openAssistant < 0 {
			h.snap.Messages = append(h.snap.Messages, Message{Role: "assistant"})
			h.openAssistant = len(h.snap.Messages) - 1
		}
		h.snap.Messages[h.openAssistant].Content += ev.Text

	case "reasoning_token":
		// 思考过程不进聊天气泡;前端可用它显示 "thinking…"。快照不存。

	case "tool_call":
		h.toolSeq++
		id := strconv.Itoa(h.toolSeq)
		ev.ID = id
		h.snap.ToolCalls = append(h.snap.ToolCalls, ToolCallView{
			ID: id, Name: ev.Name, Args: ev.Args, Status: "running",
		})
		// 同时内联到对话流;工具后另起 assistant 气泡(下一条 token 新开)。
		h.snap.Messages = append(h.snap.Messages, Message{
			Role: "tool", ID: id, Name: ev.Name, Args: ev.Args, Status: "running",
		})
		h.openAssistant = -1

	case "tool_result":
		// 配对到最近一个同名 running 工具。
		for i := len(h.snap.ToolCalls) - 1; i >= 0; i-- {
			tc := &h.snap.ToolCalls[i]
			if tc.Name == ev.Name && tc.Status == "running" {
				if ev.Success != nil && *ev.Success {
					tc.Status = "done"
				} else {
					tc.Status = "failed"
				}
				tc.Output = ev.Output
				ev.ID = tc.ID
				break
			}
		}
		// 同步对话流里的工具条。
		for i := len(h.snap.Messages) - 1; i >= 0; i-- {
			m := &h.snap.Messages[i]
			if m.Role == "tool" && m.Status == "running" && m.Name == ev.Name {
				if ev.Success != nil && *ev.Success {
					m.Status = "done"
				} else {
					m.Status = "failed"
				}
				m.Output = ev.Output
				break
			}
		}

	case "model_switch":
		if ev.Role != "" {
			h.snap.Models.ActiveRole = ev.Role
		}

	case "plan":
		// createplan → 步骤;todo / 其它 → 计划。对齐 TUI 的 计划/步骤 两套。
		if ev.PlanKind == "createplan" {
			h.snap.Step = append([]PlanNode(nil), ev.Plan...)
		} else {
			h.snap.Plan = append([]PlanNode(nil), ev.Plan...)
		}

	case "plan_status":
		// 节点 id 可能在 计划 或 步骤 任一份里,两边都找。
		for _, list := range [][]PlanNode{h.snap.Plan, h.snap.Step} {
			for i := range list {
				if list[i].ID == ev.ID {
					if ev.Status != "" {
						list[i].Status = ev.Status
					}
					if ev.Summary != "" {
						list[i].Summary = ev.Summary
					}
				}
			}
		}

	case "usage":
		h.snap.Usage = ev.Usage

	case "done":
		h.snap.Streaming = false
		h.openAssistant = -1

	case "error":
		h.snap.Streaming = false
		h.openAssistant = -1

	case "ask_request":
		h.snap.AskQuestions = ev.Questions
	case "ask_resolved":
		h.snap.AskQuestions = nil
	case "interrupted":
		// 用户中断:停掉 streaming,清掉任何待答的 review/ask 弹层。
		h.snap.Streaming = false
		h.snap.ReviewPending = nil
		h.snap.AskQuestions = nil
	case "review_request":
		h.snap.ReviewPending = &ReviewInfo{Name: ev.Name, Args: ev.Args}

	case "review_resolved":
		h.snap.ReviewPending = nil

	case "lang":
		if ev.Text != "" {
			h.snap.Lang = ev.Text
		}

	case "vendor":
		if ev.Text != "" {
			h.snap.Vendor = ev.Text
		}

	case "routing":
		if ev.Text != "" {
			h.snap.Routing = ev.Text
		}

	case "mode":
		if ev.Text != "" {
			h.snap.Mode = ev.Text
		}

	case "sandbox":
		if ev.Text != "" {
			h.snap.Sandbox = ev.Text
		}

	case "working_mode":
		if ev.Text != "" {
			h.snap.WorkingMode = ev.Text
		}

	case "codegraph":
		if ev.Text != "" {
			h.snap.CodeGraph = ev.Text
		}

	case "sessions":
		h.snap.Sessions = append([]SessionInfo(nil), ev.Sessions...)

	case "session_loaded":
		// 切换 / 新建会话:重置聊天与本轮派生状态,载入新会话的消息。
		h.snap.Messages = append([]Message(nil), ev.Messages...)
		h.snap.Plan = []PlanNode{}
		h.snap.Step = []PlanNode{}
		h.snap.ToolCalls = []ToolCallView{}
		h.snap.Usage = nil
		h.snap.ReviewPending = nil
		h.snap.Streaming = false
		h.openAssistant = -1
	}
	return ev
}
