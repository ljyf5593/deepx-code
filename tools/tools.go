package tools

import "encoding/json"

// 模型角色常量,Tool.Roles 字段使用。空列表 = 任何角色都能用。
const (
	RoleFlash    = "flash"    // 默认起手模型,便宜快
	RolePro      = "pro"      // 升级后的强模型
	RoleSubAgent = "subagent" // plan/task 执行用的子 agent
)

// Tool 工具定义
type Tool struct {
	Name        string                               `json:"name"`
	Description string                               `json:"description"`
	Parameters  ToolParam                            `json:"parameters"`
	Executor    func(args map[string]any) ToolResult `json:"-"`
	ReadOnly    bool                                 `json:"-"`
	// Roles 限制本工具可见的模型角色。空 = 任何角色可见。
	// 例如 UpdatePlanStatus 只对 subagent 可见,主对话不需要。
	Roles []string `json:"-"`
}

// ToolParam 工具参数 schema（OpenAI function calling 格式）
type ToolParam struct {
	Type       string             `json:"type"`
	Properties map[string]PropDef `json:"properties"`
	Required   []string           `json:"required,omitempty"`
}

// PropDef 参数定义
type PropDef struct {
	Type        string         `json:"type"`
	Description string         `json:"description,omitempty"`
	Items       map[string]any `json:"items,omitempty"`
	Enum        []string       `json:"enum,omitempty"`
}

// ToolResult 调用结果
type ToolResult struct {
	Output  string
	Success bool
}

// OpenAIToolSpec 用于序列化为 OpenAI function calling 协议要求的 JSON。
type OpenAIToolSpec struct {
	Type     string             `json:"type"` // 总是 "function"
	Function OpenAIFunctionSpec `json:"function"`
}

type OpenAIFunctionSpec struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Parameters  ToolParam `json:"parameters"`
}

// ToOpenAISpec 转换成 OpenAI tools 协议。
func (t Tool) ToOpenAISpec() OpenAIToolSpec {
	return OpenAIToolSpec{
		Type: "function",
		Function: OpenAIFunctionSpec{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.Parameters,
		},
	}
}

// Find 按名查找工具，找不到返回 nil。
func Find(name string) *Tool {
	for i := range Tools {
		if Tools[i].Name == name {
			return &Tools[i]
		}
	}
	return nil
}

// ParseArgs 把 LLM 返回的 JSON 字符串参数解析成 map。
func ParseArgs(raw string) (map[string]any, error) {
	args := map[string]any{}
	if raw == "" || raw == "null" {
		return args, nil
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil, err
	}
	return args, nil
}

// 工具定义列表
var Tools = []Tool{
	{
		Name:        "List",
		Description: "列出指定目录下的文件和子目录。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"path": {Type: "string", Description: "目录路径（绝对或相对路径）"},
			},
			Required: []string{"path"},
		},
		Executor: ListDir,
		ReadOnly: true,
	},
	{
		Name:        "Read",
		Description: "读取文本文件内容，可选 offset/limit 指定行范围。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"path":   {Type: "string", Description: "文件路径"},
				"offset": {Type: "integer", Description: "起始行号(从1开始)"},
				"limit":  {Type: "integer", Description: "最多读取多少行,默认2000"},
			},
			Required: []string{"path"},
		},
		Executor: ReadFile,
		ReadOnly: true,
	},
	{
		Name:        "Write",
		Description: "写入（覆盖）文本文件。父目录会自动创建。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"path":    {Type: "string", Description: "文件路径"},
				"content": {Type: "string", Description: "要写入的全部内容"},
			},
			Required: []string{"path", "content"},
		},
		Executor: WriteFile,
		ReadOnly: false,
	},
	{
		Name:        "Update",
		Description: "对已有文件做精确字符串替换。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"path":        {Type: "string", Description: "文件路径"},
				"old_string":  {Type: "string", Description: "原始字符串（要替换的内容，需精确匹配）"},
				"new_string":  {Type: "string", Description: "替换后的字符串"},
				"replace_all": {Type: "boolean", Description: "是否替换全部出现，默认 false"},
			},
			Required: []string{"path", "old_string", "new_string"},
		},
		Executor: EditFile,
		ReadOnly: false,
	},
	{
		Name:        "Glob",
		Description: "按 glob 模式查找文件，支持 ** 递归通配，结果按修改时间倒序返回。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"pattern": {Type: "string", Description: "glob 模式，如 **/*.go"},
				"path":    {Type: "string", Description: "搜索根目录（可选）"},
			},
			Required: []string{"pattern"},
		},
		Executor: GlobFile,
		ReadOnly: true,
	},
	{
		Name:        "Grep",
		Description: "在文件中按正则查找匹配行，结果为 path:line:content 格式。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"pattern": {Type: "string", Description: "正则表达式"},
				"path":    {Type: "string", Description: "搜索路径（文件或目录）"},
				"glob":    {Type: "string", Description: "文件名模式过滤，如 *.go"},
			},
			Required: []string{"pattern"},
		},
		Executor: GrepFile,
		ReadOnly: true,
	},
	{
		Name:        "Tree",
		Description: "以树状结构显示目录。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"path":  {Type: "string", Description: "根目录路径"},
				"depth": {Type: "integer", Description: "最大深度，默认 3"},
			},
		},
		Executor: FileTree,
		ReadOnly: true,
	},
	{
		Name: "Bash",
		Description: "在 shell 中执行命令并返回 stdout/stderr。可指定 cwd 与超时秒数(默认 60)。" +
			"\n\n**不要用本工具启动长时间运行的进程**(开发服务器 / 守护进程 / 无限监视器),例如:" +
			"\n  - npm run dev / vite / pnpm dev / yarn start" +
			"\n  - python -m http.server" +
			"\n  - tail -f / watch -n" +
			"\n  - 任何 daemon" +
			"\n这类命令不会主动退出,deepx 必须等子进程 stdout/stderr 关闭才能拿到结果," +
			"会导致整个 agent 卡死到 timeout 触发(默认 60s)。" +
			"加 `&` / `nohup` 也不能解决 — Go 仍会等继承的文件描述符关闭。" +
			"\n本工具只用来跑会主动退出的命令:构建 / 单测 / lint / grep / git / ls / 安装依赖 / 一次性脚本。" +
			"\n如果用户要求启动服务,直接告诉用户在自己终端里手动跑,不要调本工具。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"command": {Type: "string", Description: "完整命令行（通过 sh -c 执行）"},
				"cwd":     {Type: "string", Description: "工作目录（可选）"},
				"timeout": {Type: "integer", Description: "超时秒数，默认 60"},
			},
			Required: []string{"command"},
		},
		Executor: RunCommand,
		ReadOnly: false,
	},
	{
		Name: "Search",
		Description: "搜索公开互联网,返回标题/URL/摘要列表。" +
			"\n\n**何时调用**:用户问到时效性强的信息(新闻、版本号、近期事件)、需要最新文档/教程、" +
			"或代码里某个依赖/API 的官方说明你不确定。本工具只是检索摘要,要看完整内容仍需后续访问 URL。" +
			"\n\n**何时不要调用**:用户问的是当前 workspace 的代码细节(用 Read/Grep)、" +
			"通用编程常识、或你已经能给出可靠回答的问题 — 别为了凑 token 滥用搜索。" +
			"\n\n**Provider 选择**(由 env 决定,工具自身不需要参数):" +
			"\n  - 默认 Bing HTML 抓取,零配置,国内可用 (cn.bing.com 直连)" +
			"\n  - DEEPX_SEARCH_API_KEY=bocha:<key>  → 博查 AI (国内厂商, 质量更好)" +
			"\n  - DEEPX_SEARCH_API_KEY=tavily:<key> → Tavily (海外, LLM 友好)",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"query":       {Type: "string", Description: "搜索关键词,直接用自然语言或英文都行"},
				"max_results": {Type: "integer", Description: "最多返回几条 (1-15, 默认 5)"},
			},
			Required: []string{"query"},
		},
		Executor: WebSearch,
		ReadOnly: true, // 仅查询,不动本地文件
	},
	{
		Name: "Fetch",
		Description: "抓取单个 URL 的内容,HTML 自动转纯文本(去 script/style/nav,优先抽 main/article)。" +
			"\n\n**何时调用**:" +
			"\n- 用户直接给定 URL 让你读 / 总结 / 评审" +
			"\n- Search 找到 URL 后需要看正文(snippet 不够)" +
			"\n- 跟踪官方文档 / GitHub issue / 技术博客等具体页面" +
			"\n\n**何时别调**:" +
			"\n- 用户问的是 workspace 本地内容 (用 Read)" +
			"\n- 你已经能凭训练数据答好(避免无谓 fetch)" +
			"\n\n输出含 URL/Content-Type/长度元数据 + 正文。超过 max_chars 自动截断。" +
			"\n非 HTML(JSON/Markdown/纯文本)直接返回原文不做处理。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"url":       {Type: "string", Description: "完整 URL,必须以 http:// 或 https:// 开头"},
				"max_chars": {Type: "integer", Description: "输出字符上限 (1-30000, 默认 10000)"},
			},
			Required: []string{"url"},
		},
		Executor: WebFetch,
		ReadOnly: true,
	},
	{
		Name: "Memory",
		Description: "检索当前 workspace 历史对话(跨日期,仅当前 workspace 的 session)。" +
			"\n\n**何时调用**:" +
			"\n- 用户说\"上次\"、\"之前\"、\"以前我们聊过\"等指向过往对话的表达" +
			"\n- 你需要回忆之前讨论过的设计决策、做过的修改、报错的根因" +
			"\n- 任务延续:用户接着之前未完成的事情往下做" +
			"\n\n**怎么用**:你给一组关键词(建议 2-4 个,关键名词/动词,避免停用词)。" +
			"mode=and(默认)要求全部命中、最精准;mode=or 任一命中、召回更广,关键词少且独立时用。" +
			"\n\n命中后返回 [日期 | 时刻 | 角色 | 内容前 400 字]。如果未命中,换近义词或拆短再试。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"keywords": {
					Type:        "array",
					Description: "检索关键词列表,1-6 个",
					Items:       map[string]any{"type": "string"},
				},
				"mode": {
					Type:        "string",
					Description: "and=全部命中(默认),or=任一命中",
					Enum:        []string{"and", "or"},
				},
				"max_results": {Type: "integer", Description: "返回上限 (1-50, 默认 10)"},
			},
			Required: []string{"keywords"},
		},
		Executor: Memory,
		ReadOnly: true,
	},
	{
		Name: "LoadSkill",
		Description: "加载一个 skill (用户预定义的指令包) 的完整正文塞进上下文。" +
			"\n\n**何时调用**:" +
			"\n- 你看 system prompt 里的 \"Available Skills\" 列表,判断哪些 skill 的 description 跟当前任务对得上" +
			"\n- 比如用户让你写 Go 代码,而列表里有 `go-style: 写 Go 时遵循的命名/错误处理规约`,就调一次 LoadSkill(name=\"go-style\")" +
			"\n- 一个回合内可以连续调多个,加载多个相关 skill" +
			"\n\n**何时别调**:" +
			"\n- 没有任何 skill 跟当前任务相关 —— 不要硬调" +
			"\n- 同一 skill 本会话已加载过(history 里能看到 tool result)—— 不要重复",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"name": {Type: "string", Description: "skill 名,从 system prompt 的 Available Skills 列表中选"},
			},
			Required: []string{"name"},
		},
		Executor: LoadSkill,
		ReadOnly: true,
	},
	{
		Name:        "OCR",
		Description: "对本地图片做 OCR 识别,返回图片中的全部文字 (基于 PaddleOCR PP-OCRv5,支持中英文)。当对话里出现图片路径 (常见 [Image #N] 占位符已被替换为路径),需要了解图片内容时调用本工具。首次调用会自动下载 ~37MB 模型,后续调用秒级响应。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"path": {Type: "string", Description: "图片的本地文件路径,支持 PNG/JPEG/GIF"},
			},
			Required: []string{"path"},
		},
		Executor: ImgOCR,
		ReadOnly: true, // 仅读取本地图片,不修改任何东西
	},
	{
		Name: "SwitchModel",
		Description: "把当前对话切到 pro 模型(更强但更贵)。单向升级,升完本轮剩余都用 pro;下一轮 user 输入重新走 keyword router 决定起手。\n\n" +
			"**何时调用 SwitchModel(满足任一条件)**:\n" +
			"1. 需要复杂推理 / 长链路因果分析\n" +
			"2. 需要长链路规划(多步分解、跨阶段决策)\n" +
			"3. 需要生成大型代码块(>100 行 / 跨多文件)\n" +
			"4. 需要高精度代码修改(关键算法 / 性能敏感路径 / 并发同步)\n" +
			"5. 需要分析大型项目结构\n" +
			"6. 需要多文件重构(命名 / 抽象 / 跨包改动)\n" +
			"7. 用户明确要求 \"深度思考\" / \"仔细分析\" / \"认真想想\"\n" +
			"8. 当前模型连续 2+ 轮处理同一问题失败 / 反复试错\n" +
			"9. 需要高质量创作(长文档 / 设计稿 / 详细方案)\n" +
			"10. 上下文接近窗口限制(history 占比 > 70%),需要更大窗口模型\n\n" +
			"**何时别调**:简单单步任务(读单文件、一行替换、直接答事实、跑 ls/grep)用 flash 足够,不要无脑升级。\n\n" +
			"已经在 pro 时调用是 no-op(deepx 会返回提示但不报错)。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"reason": {
					Type:        "string",
					Description: "简述切换理由(一句话),会显示给用户,让用户知道为何升级",
				},
			},
			Required: []string{"reason"},
		},
		// Executor 为 nil:本工具在 agent/llm.go 工具循环里被拦截,不走默认 Executor。
		// 拦截后直接修改 agent 内部 currentEntry/role,通过 ModelSwitchMsg 通知 UI。
		Executor: nil,
		ReadOnly: true,
		// Roles 留空 = 所有角色可见。pro 调用时拦截层会 no-op。
	},
	{
		Name: "CreatePlan",
		Description: "把复杂任务拆解成可并发执行的 DAG。\n\n" +
			"**何时调用**:\n" +
			"- **用户明确要求『规划 / 列出步骤 / 列待办 / 先 plan / 必须先规划』等意图时,第一步必须调用本工具**\n" +
			"- 任务含 2+ 个独立步骤 / 跨多个文件 / 需要先调研再修改\n" +
			"- 简单任务(读单文件 / 一行替换 / 直接答事实)不要硬拆,直接答更高效\n\n" +
			"**每个节点 model 字段选择**:\n" +
			"  • `flash` — 机械步骤:读单文件 / grep / ls / git status / 统计行数 / 列目录\n" +
			"  • `pro` — 思考步骤:综合分析 / 跨文件关联 / 代码评审 / 根因排查 / 最终汇总\n\n" +
			"**典型拆分模式**:\n" +
			"```\n" +
			"plan1: 读文件 A    (flash)  ┐\n" +
			"plan2: 读文件 B    (flash)  ├─ 并发\n" +
			"plan3: 读文件 C    (flash)  ┘\n" +
			"plan4: 综合分析     (pro)    ← depends_on=[plan1,plan2,plan3]\n" +
			"```\n" +
			"无依赖时**不要写 depends_on**(允许 deepx 并发跑节点)。\n\n" +
			"**执行 & 返回值**:deepx 按 DAG 依赖关系并发执行所有节点,返回每个节点的执行汇总(每行一个节点 + 状态 + 简短结果)。拿到汇总后只需给用户写一段简洁的最终总结,不要再做实际工作(状态由 deepx 自维护)。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"plans": {
					Type:        "array",
					Description: "顶层规划节点列表。每项含 id(plan1, plan2 ...)、title、model、可选 depends_on。",
					Items: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"id":         map[string]any{"type": "string", "description": "plan1, plan2 ..."},
							"title":      map[string]any{"type": "string", "description": "一句话说明这一步做什么"},
							"model":      map[string]any{"type": "string", "enum": []string{"flash", "pro"}, "description": "执行该 plan 使用哪个模型"},
							"depends_on": map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "依赖的其他 plan id;无依赖留空(允许并发)"},
						},
						"required": []string{"id", "title", "model"},
					},
				},
			},
			Required: []string{"plans"},
		},
		Executor: CreatePlan,
		ReadOnly: true, // 仅做规划登记,不动文件
		// Roles 留空 = 所有角色可见。
		// 入口由 keyword router 路由,模型自行判断要不要拆 plan;
		// flash 起手时也允许它把复杂任务拆成 DAG(其中可指定 pro 节点跑深度部分)。
	},
	{
		Name: "UpdatePlanStatus",
		Description: "更新某个 plan 节点的执行状态。每开始一个 plan 前调用一次(status=running),完成或失败时再调一次(status=done/failed)。summary 是可选的一段简短结论。" +
			"deepx 用这些状态实时驱动右栏 Current Plan 区的显示。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"id":      {Type: "string", Description: "plan 的 id,如 plan1"},
				"status":  {Type: "string", Enum: []string{"pending", "running", "done", "failed", "blocked"}, Description: "新状态"},
				"summary": {Type: "string", Description: "可选,完成/失败时一段简短的结论或错误描述"},
			},
			Required: []string{"id", "status"},
		},
		Executor: UpdatePlanStatus,
		ReadOnly: true,
		// 只对子 agent 暴露。主对话里的 pro 在调用 CreatePlan 后 DAG 自动驱动状态,
		// 不需要 pro 显式更新;子 agent 偶尔需要写中间状态(实际被吞,以 scheduler 为准)。
		Roles: []string{RoleSubAgent},
	},
}
