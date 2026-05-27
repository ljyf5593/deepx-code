package tools

import "encoding/json"

// 模型角色常量:标识本轮用 flash 还是 pro。仅用于入口路由、SwitchModel、以及 UI 的活动模型
// 指示;不再用于工具可见性过滤(所有角色的工具表一致,保前缀缓存)。
const (
	RoleFlash = "flash" // 默认起手模型,便宜快
	RolePro   = "pro"   // 升级后的强模型
)

// Tool 工具定义
type Tool struct {
	Name        string                               `json:"name"`
	Description string                               `json:"description"`
	Parameters  ToolParam                            `json:"parameters"`
	Executor    func(args map[string]any) ToolResult `json:"-"`
	ReadOnly    bool                                 `json:"-"`
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

// Find 按名查找工具，找不到返回 nil。先查静态工具，再兜底查动态注入的 MCP 工具。
func Find(name string) *Tool {
	for i := range Tools {
		if Tools[i].Name == name {
			return &Tools[i]
		}
	}
	return findMCPTool(name)
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
		Name: "CodeGraph",
		Description: "代码图谱:对当前 workspace 做符号级导航,查代码符号(函数/类型/方法)的定义、调用关系、实现者、继承关系，影响范围,要优先于read,glob,grep工具调用。" +
			"\n\n**何时调用**(优先于 Grep):" +
			"\n- 某个函数/类型/方法定义在哪 → op=def" +
			"\n- 谁调用了某函数(改它的影响面)→ op=callers;某函数调用了哪些 → op=callees" +
			"\n- 谁实现了某接口(Go 隐式接口,grep 查不到)→ op=implementers;谁继承/嵌入某类型 → op=subtypes;某类型派生自什么 → op=supertypes" +
			"(注:继承/实现边覆盖 Go 及主流 OOP 语言,空结果会列出已覆盖范围、不代表无关系)" +
			"\n- 改某符号会牵连哪些下游(影响面/blast radius,传递闭包)→ op=impact(可给 depth)" +
			"\n- 谁引用/用到了某个名字 → op=refs" +
			"\n- 按名字找符号、或列某类符号 → op=symbols(可加 kind 过滤)" +
			"\n- 看一个文件有哪些符号 → op=outline;它 import 了什么 → op=imports(给 path)" +
			"\n\n**何时不要**:搜的是注释/字符串/任意文本(用 Grep);非代码文件。" +
			"\n\n结果是 `文件:行`(+签名/调用方),已截断分页。注:调用关系按名解析,同名会合并。" +
			"\n支持语言:Go(stdlib 精确)+ TS/JS/Python/Java/Rust/C/C++/C#/Ruby/PHP/Kotlin/Swift/Scala/Dart/Vue/Svelte。其它(shell/css/json/md 等)用 Grep。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"op":    {Type: "string", Enum: []string{"symbols", "def", "refs", "callers", "callees", "implementers", "subtypes", "supertypes", "impact", "imports", "outline", "reindex"}, Description: "操作类型"},
				"name":  {Type: "string", Description: "符号名;def/refs/callers/callees/implementers/subtypes/supertypes/impact 必填,支持 \"Type.Method\" 限定名;symbols 作模糊过滤"},
				"path":  {Type: "string", Description: "outline/imports 用:相对 workspace 的文件路径"},
				"kind":  {Type: "string", Enum: []string{"func", "method", "type", "var", "const", "field"}, Description: "可选,按符号种类过滤"},
				"depth": {Type: "integer", Description: "impact 用:传递闭包跳数,默认 3"},
				"max":   {Type: "integer", Description: "最多返回几条,默认 60"},
			},
			Required: []string{"op"},
		},
		Executor: CodeGraph,
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
		Name: "Update",
		Description: "编辑已有文件:用 old_string 精确匹配要替换的内容,new_string 为替换后的内容。\n\n" +
			"old_string 必须与文件内容逐字符一致(含缩进 Tab/空格、空行)。\n" +
			"从 Read 输出复制 old_string 时,只复制「│」之后的内容,不包含行号前缀。\n" +
			"选取 3-5 行上下文确保唯一性。若 old_string 出现多次且只想替换一处,加更多上下文让其唯一;\n" +
			"replace_all=true 可替换全部出现。\n\n" +
			"注意: old_string/new_string 中若有双引号必须转义为 \\\"。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"path":        {Type: "string", Description: "文件路径（绝对或相对路径）"},
				"old_string":  {Type: "string", Description: "要替换的精确文本,需逐字符匹配"},
				"new_string":  {Type: "string", Description: "替换后的文本"},
				"replace_all": {Type: "boolean", Description: "替换所有匹配项,默认 false"},
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
			"\n\n**常驻进程**(开发服务器 / watch / daemon,如 npm run dev、vite、python -m http.server、tail -f)" +
			"不会主动退出 —— 默认(前台)调用会一直阻塞到 timeout 才返回,并把子进程甩成孤儿。" +
			"启动这类进程时**必须传 `run_in_background: true`**:立即返回一个句柄 id(形如 bash_1),不阻塞。" +
			"随后用 `BashOutput(id)` 读输出/查是否就绪,用完用 `KillBash(id)` 结束。" +
			"\n⚠️ **不要用 shell 的 `&` 或 `nohup` 在前台模式里自己后台化**(如 `./server &`):那样救不了," +
			"Go 仍会等子进程继承的输出管道关闭而卡死到 timeout,还会留孤儿;要后台跑就用 `run_in_background: true`。" +
			"\n前台模式(默认)只用来跑会主动退出的命令:构建 / 单测 / lint / grep / git / ls / 安装依赖 / 一次性脚本。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"command":           {Type: "string", Description: "完整命令行（通过 sh -c 执行）"},
				"cwd":               {Type: "string", Description: "工作目录（可选）"},
				"timeout":           {Type: "integer", Description: "超时秒数，默认 60（仅前台模式生效）"},
				"run_in_background": {Type: "boolean", Description: "true 则后台启动常驻进程,立即返回句柄 id,不阻塞;配合 BashOutput / KillBash 使用"},
			},
			Required: []string{"command"},
		},
		Executor: RunCommand,
		ReadOnly: false,
	},
	{
		Name:        "BashOutput",
		Description: "读取某个后台进程(Bash run_in_background 启动)自上次读取以来的新输出,并报告其运行/退出状态。用于等待服务就绪、查看日志、确认是否报错。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"id": {Type: "string", Description: "后台进程句柄 id（Bash run_in_background 返回的，形如 bash_1）"},
			},
			Required: []string{"id"},
		},
		Executor: BashOutput,
		ReadOnly: true, // 只读输出,不动文件
	},
	{
		Name:        "KillBash",
		Description: "结束一个后台进程(连同其子进程树),释放端口/资源。常驻服务验证完务必调用,避免孤儿进程堆积。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"id": {Type: "string", Description: "后台进程句柄 id（形如 bash_1）"},
			},
			Required: []string{"id"},
		},
		Executor: KillBash,
		ReadOnly: false,
	},
	{
		Name: "Search",
		Description: "搜索公开互联网,返回标题/URL/摘要列表。" +
			"\n\n**何时调用**:用户问到时效性强的信息(新闻、版本号、近期事件)、需要最新文档/教程、" +
			"或代码里某个依赖/API 的官方说明你不确定。本工具只是检索摘要,要看完整内容仍需后续访问 URL。" +
			"\n\n**何时不要调用**:用户问的是当前 workspace 的代码细节(用 Read/Grep)、" +
			"通用编程常识、或你已经能给出可靠回答的问题 — 别为了凑 token 滥用搜索。" +
			"\n\n**后端**:Bing HTML 抓取,零配置、无需 API key,国内可用 (cn.bing.com 直连)。",
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
		Description: "读取一个 skill 的完整内容。" +
			"当 system prompt 的 Available Skills 列表中，有 skill 的 description 与当前任务匹配时调用。" +
			"同一回合内可连续调用多个相关 skill。" +
			"没有匹配的 skill 时不要调用。" +
			"同一 skill 在本会话内只调用一次。",
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
		// pro 调用时拦截层会 no-op。子 agent 不该用本工具,但不按角色隐藏(各角色工具表一致、保前缀缓存):
		// 靠子 agent 系统提示词禁止 + Executor 为 nil 的纵深防护兜底(真调了也只返回失败,不生效)。见 subagent.go。
	},
	{
		Name: "CreatePlan",
		Description: "把任务拆解成多个步骤的 DAG 执行。\n\n" +
			"**何时调用**:\n" +
			"- 用户明确要求规划\n" +
			"- 开发/重构/修复复杂业务时\n\n" +
			"**每个节点 model 字段**:\n" +
			"  • `flash` — 机械步骤:读文件 / grep / ls / git status / 统计行数\n" +
			"  • `pro` — 思考步骤:分析 / 代码评审 / 根因排查 / 最终汇总\n\n" +
			"**并发原则**: 默认不写 depends_on，只有某步骤确实必须等另一步完成才加。优先并发，而非串行。\n\n" +
			"**执行 & 返回值**: 按 DAG 依赖关系并发执行所有节点,返回每个节点的执行汇总(每行一个节点 + 状态 + 简短结果)。拿到汇总后只需给用户写一段简洁的最终总结,不要再做实际工作(状态由 deepx 自维护)。",
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
		// 入口由 keyword router 路由,模型自行判断要不要拆 plan;
		// flash 起手时也允许它把复杂任务拆成 DAG(其中可指定 pro 节点跑深度部分)。
		// 子 agent 不该用本工具(防递归),但不按角色隐藏(各角色工具表一致、保前缀缓存):靠系统提示词禁止 +
		// Executor 为 nil 兜底(同 Todo / SwitchModel)。见 subagent.go。
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
		// 主要给 CreatePlan 拆出的 DAG 子 agent 写中间状态(实际被吞,以 scheduler 为准)。
		// 主对话用 Todo 维护可见清单、不靠它;但不再按角色隐藏(各角色工具表一致,保前缀缓存),
		// 主 agent 真调了也只是发一条 TaskStatusMsg,apply 找不到 id 会静默忽略,无害。
	},
	{
		Name: "Todo",
		Description: "维护一个对用户可见的待办清单,展示并随进度勾选——这是给用户的进度透明度,不派子 agent、你自己一步步执行。\n\n" +
			"**何时用**:任务有 ≥3 个有先后顺序的步骤、值得让用户看到进度(从零搭应用、跨多文件改动、调试修复链路)。开工前先列出全部步骤,然后每开始/完成一步就重发整张清单更新状态。\n\n" +
			"**何时不用**:单步或简单任务(直接做);真正可并行、互相独立的扇出任务用 CreatePlan(那会派并发子 agent),搭一个连贯的应用不要用 CreatePlan。\n\n" +
			"**用法(全量快照)**:每次调用都传完整 todos 列表,每项含 content + status。新增 / 标记进行中 / 完成 / 调整,都重发整张表(不是增量)。同一时刻最多一项 in_progress。",
		Parameters: ToolParam{
			Type: "object",
			Properties: map[string]PropDef{
				"todos": {
					Type:        "array",
					Description: "完整的待办列表(全量快照,按执行顺序排列)。",
					Items: map[string]any{
						"type": "object",
						"properties": map[string]any{
							"content": map[string]any{"type": "string", "description": "这一步做什么(一句话)"},
							"status":  map[string]any{"type": "string", "enum": []string{"pending", "in_progress", "completed"}, "description": "pending=未开始 / in_progress=进行中 / completed=已完成"},
						},
						"required": []string{"content", "status"},
					},
				},
			},
			Required: []string{"todos"},
		},
		// Executor 为 nil:在 agent/llm.go 工具循环里被拦截,转成 PlanCreatedMsg 更新 UI,
		// 不走默认 Executor、不派子 agent。
		Executor: nil,
		ReadOnly: true,
		// Roles 留空 = 所有角色可见;子 agent 由其系统提示词禁止使用(同 CreatePlan)。
	},
}
