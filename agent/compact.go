package agent

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"deepx/tools"

	"github.com/tiktoken-go/tokenizer"
)

// === 会话压缩 ===
//
// 本文件封装与 TUI 无关的压缩纯逻辑:token 估算 + RunCompression。
// bubbletea 胶水(触发时机、tea.Cmd 包装、消息处理、读 model/session)仍在 tui 包,调用这里的导出函数。

// keepRecentTurns 是压缩时至少保留的最近 user 轮数下限(与 20% 预算取较大者)。
// 5 轮通常能覆盖"当前任务 + 最近指代",又不过度占用预算。可按需调整。
const keepRecentTurns = 5

// compactionTimeout 是摘要 LLM 调用的硬超时。没有它,卡住的请求会让压缩锁永远占住、把所有压缩堵死。
// 给得宽松(容纳大摘要生成),只为兜住"永不返回",超时即失败、下轮重试。
const compactionTimeout = 3 * time.Minute

// compressionPrompt 是冷路径(无前缀快照)压缩历史时发给 LLM 的 system prompt。
const compressionPrompt = `你是会话「工作状态 checkpoint」生成器。把对话历史提炼成一份结构化的当前工作状态快照,用于丢弃旧历史后延续上下文。

## 摘要需保留
- 用户的任务目标和明确要求（尽量原文保留）
- 已修改的文件及改动目的
- 发现的错误和修复方案
- 架构设计决策
- 未完成的任务和下一步计划

## 可以丢弃
- 重复的调试尝试
- 工具调用的详细输出
- 已解决且不再相关的中间讨论

如果输入中有 [previous summary],将其与新对话合并为一个连贯摘要。

## 输出格式(严格按以下字段;无内容的字段写「无」)
## 任务目标
<用户想达成什么;明确要求尽量原文>

## 当前进度
<已完成 / 已验证的事项>

## 关键决策与取舍
<做了哪些决定及原因;试过并否决的方案 + 为什么>

## 关键事实与约束
<技术事实、项目约定、不能动的东西、踩过的坑>

## 相关文件
- <path> — <作用 / 改动>

## 未决项
<待用户确认 / 阻塞中的问题;无则写「无」>

## 下一步
<下一个具体动作>

最后模式: plan/auto`

// warmCompressInstruction 是缓存友好压缩时追加在历史末尾的指令(对应 compressionPrompt 的内容,
// 但作为尾部 user 消息而非独立 system,从而不破坏 [system][tools][history] 前缀的命中)。
const warmCompressInstruction = `请把以上完整对话(包括 system 提示词里已有的"当前工作状态"/会话摘要,若有)提炼成一份新的、连贯的结构化「当前工作状态 checkpoint」,用它覆盖旧的。

## 摘要需保留
- 用户的任务目标和明确要求(尽量原文保留)
- 已修改的文件及改动目的
- 发现的错误和修复方案
- 架构设计决策
- 未完成的任务和下一步计划

## 可以丢弃
- 重复的调试尝试
- 工具调用的详细输出
- 已解决且不再相关的中间讨论

## 输出格式(严格按以下字段;无内容的字段写「无」)
## 任务目标
<用户想达成什么;明确要求尽量原文>

## 当前进度
<已完成 / 已验证的事项>

## 关键决策与取舍
<做了哪些决定及原因;试过并否决的方案 + 为什么>

## 关键事实与约束
<技术事实、项目约定、不能动的东西、踩过的坑>

## 相关文件
- <path> — <作用 / 改动>

## 未决项
<待用户确认 / 阻塞中的问题;无则写「无」>

## 下一步
<下一个具体动作>

最后模式: plan/auto`

// === token 估算 ===

// tikCodec 惰性初始化 o200k_base 分词器(OpenAI 现代词表)。DeepSeek 未公开 Go 分词器,但 BPE
// token 密度相近,实测与真实 prompt_tokens 差 ~2.5%,对压缩阈值判断足够;且纯本地、零 API 依赖、
// 内容无关(中文/代码/JSON 都准)。词表编译进二进制,Get 仅首次有开销,故 sync.Once 缓存。
var (
	tikOnce  sync.Once
	tikCodec tokenizer.Codec
)

func tokCodec() tokenizer.Codec {
	tikOnce.Do(func() {
		tikCodec, _ = tokenizer.Get(tokenizer.O200kBase) // 失败留 nil,EstTokens 自动退回 字符/2.5
	})
	return tikCodec
}

// fallbackCharsPerTok:tiktoken 不可用时的兜底"字符/token"比。取 2.5(略保守,估得偏高一点 →
// 宁可早压不晚压),而非 3 —— 实测混合内容真实比例在 2.3~3.2,2.5 居中且不易低估历史。
const fallbackCharsPerTok = 2.5

// EstTokens 估算文本 token 数:优先用 tiktoken(o200k)精确分词;分词器不可用时退回 字符/2.5。
func EstTokens(s string) int {
	if s == "" {
		return 0
	}
	if c := tokCodec(); c != nil {
		if ids, _, err := c.Encode(s); err == nil {
			return len(ids)
		}
	}
	return int(float64(len([]rune(s))) / fallbackCharsPerTok)
}

// MsgTokens 估算单条消息在请求体里占的 token(全字段:Content + ReasoningContent + ContentParts +
// ToolCalls 的 Name/Arguments)—— 漏算 ToolCalls.Arguments(agentic 会话里占比可观)会系统性低估历史。
func MsgTokens(m ChatMessage) int {
	t := EstTokens(m.Content) + EstTokens(m.ReasoningContent)
	for _, p := range m.ContentParts {
		t += EstTokens(p.Text)
	}
	for _, tc := range m.ToolCalls {
		t += EstTokens(tc.Function.Name) + EstTokens(tc.Function.Arguments)
	}
	return t
}

// EstimateHistoryTokens 估算会话历史的 token 数(全字段),不含 system / tools / summary 固定底座 ——
// 与压缩"保留量"口径一致(RunCompression 也只按历史算):历史是唯一可被压缩的部分,底座压不掉,
// 故"值不值得压"只看历史。
func EstimateHistoryTokens(history []ChatMessage) int {
	t := 0
	for _, msg := range history {
		t += MsgTokens(msg)
	}
	return t
}

// EstimatePromptTokens 本地估算整个 prompt 的 token 数 = 系统提示词 + 工具定义 JSON + 历史。
// 仅在 API 没返回 usage 时作兜底(调用方优先用真实 prompt_tokens)。
func EstimatePromptTokens(workspace, skillCatalog, summary string, history []ChatMessage) int {
	t := EstTokens(BuildSystemPrompt(workspace, skillCatalog, summary))

	specs := make([]tools.OpenAIToolSpec, 0, len(tools.Tools))
	for _, tl := range tools.Tools {
		specs = append(specs, tl.ToOpenAISpec())
	}
	for _, tl := range tools.MCPTools() {
		specs = append(specs, tl.ToOpenAISpec())
	}
	t += EstTokens(MarshalToolSpecs(specs))

	return t + EstimateHistoryTokens(history)
}

// === 压缩执行 ===

// RunCompression 执行一次会话压缩:按 context_window × 20% 保留尾部上下文。
// 通常在 goroutine 中调用。history 仅含会话消息(不含 system / 旧摘要消息)。
//
// 缓存友好:传入 lastSystemPrompt(上次实际发送的 system 文本)时,摘要请求构造成
// [lastSystemPrompt] + history[:keepStart] + [尾部压缩指令],并带上 lastToolSpecsJSON 还原的
// 工具集 —— 这串前缀正是上次缓存下来的,几乎全命中,只有尾部指令是 miss。
// lastSystemPrompt 为空(无快照)时退回冷路径:compressionPrompt 当 system + 拍平历史。
func RunCompression(lastSystemPrompt, lastToolSpecsJSON string, history []ChatMessage, entry ModelEntry, ctxWin int) (
	summary string, cutIdx int, compressedTurns int, err error) {

	totalUsers := 0
	for _, msg := range history {
		if msg.Role == "user" {
			totalUsers++
		}
	}
	if totalUsers <= 2 {
		return "", 0, 0, fmt.Errorf("user 轮数不足,无需压缩")
	}

	// 保留量 = max(20% 预算, 最近 keepRecentTurns 轮),取保留更多者(切点更靠前 = 下标更小)。
	// 关键:budgetStart 初值 = 0 = "默认保留全部"。从尾部累加 token 攒够 20% 窗口,才把切点后移
	// (留得更少)。整段历史都不到 20% → budgetStart 停在 0 → keepStart=0 → 守卫拒绝,不压。
	keepTarget := ctxWin * 20 / 100

	budgetStart := 0 // 默认保留全部;攒够 20% 才后移切点
	cc := 0
	for i := len(history) - 1; i >= 0; i-- {
		// 用 MsgTokens(tiktoken 精确分词)按 token 累加全字段,含 ReasoningContent + ToolCalls 参数。
		cc += MsgTokens(history[i])
		if history[i].Role == "user" && cc >= keepTarget {
			budgetStart = i
			break
		}
	}
	turnStart := len(history) // 最近 keepRecentTurns 个 user 轮;不足 5 轮则保持 len(不参与取 min)
	uc := 0
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == "user" {
			uc++
			if uc >= keepRecentTurns {
				turnStart = i
				break
			}
		}
	}
	keepStart := budgetStart // 取保留更多者 = 更靠前的切点
	if turnStart < keepStart {
		keepStart = turnStart
	}
	if keepStart <= 0 {
		// 整段都要留:历史不足 20% 窗口,或 20% 边界就在最前一条 —— 没有可压缩前缀。
		return "", 0, 0, fmt.Errorf("历史不足 20%% 窗口,无需压缩")
	}
	cutIdx = keepStart

	lastMode := "auto"
	compressedUserCount := 0
	for _, msg := range history[:keepStart] {
		if msg.Role == "user" {
			compressedUserCount++
		}
		if msg.Role == "assistant" && strings.Contains(msg.Content, "当前模式: plan") {
			lastMode = "plan"
		}
		if msg.Role == "assistant" && strings.Contains(msg.Content, "当前模式: auto") {
			lastMode = "auto"
		}
	}
	compressedTurns = compressedUserCount

	summaryMax := ctxWin * 3 / 100
	if summaryMax < 256 {
		summaryMax = 256 // 最小 256 tok,避免太小失去摘要意义
	}

	// 硬超时:卡住的摘要请求不会永久占住压缩锁(否则压缩全堵死)。
	ctx, cancel := context.WithTimeout(context.Background(), compactionTimeout)
	defer cancel()

	if lastSystemPrompt != "" {
		// 缓存友好路径:复刻 [system][tools][history[:keepStart]] + 尾部指令。
		convo := make([]ChatMessage, 0, keepStart+2)
		convo = append(convo, ChatMessage{Role: "system", Content: lastSystemPrompt})
		convo = append(convo, history[:keepStart]...)
		convo = append(convo, ChatMessage{Role: "user", Content: warmCompressInstruction})
		toolSpecs := UnmarshalToolSpecs(lastToolSpecsJSON)
		summary, err = CallWithTools(ctx, entry.APIKey, entry.BaseURL, entry.Model, convo, toolSpecs, summaryMax)
	} else {
		// 冷路径:无快照,拍平历史走独立 system(必 miss,但正确)。
		var inputBuf strings.Builder
		for _, msg := range history[:keepStart] {
			inputBuf.WriteString("[" + msg.Role + "]\n" + msg.Content + "\n\n")
		}
		convo := []ChatMessage{
			{Role: "system", Content: compressionPrompt},
			{Role: "user", Content: inputBuf.String()},
		}
		summary, err = CallOnce(ctx, entry.APIKey, entry.BaseURL, entry.Model, convo, summaryMax)
	}
	if err != nil {
		return "", 0, 0, err
	}
	if !strings.Contains(summary, "最后模式:") {
		summary += "\n最后模式: " + lastMode
	}
	return summary, cutIdx, compressedTurns, nil
}
