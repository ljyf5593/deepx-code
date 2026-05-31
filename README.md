<div align="center">

# deepx-code

**DeepSeek 原生、兼容 OpenAI 接口的终端编程 Agent（已支持小米 MiMo）—— 单二进制、缓存友好、内置代码图谱与本地 OCR**

[![Go](https://img.shields.io/badge/built%20with-Go-00ADD8?logo=go&logoColor=white)](https://go.dev) [![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE) [![Release](https://img.shields.io/github/v/release/itmisx/deepx-code?color=success)](https://github.com/itmisx/deepx-code/releases) [![Stars](https://img.shields.io/github/stars/itmisx/deepx-code?style=flat)](https://github.com/itmisx/deepx-code/stargazers) ![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)

简体中文 · [English](README.en.md) · [日本語](README.ja.md) · [한국어](README.ko.md)

![deepx-code demo](assets/demo.gif)

</div>

> [!TIP]
> **⚡ 长会话实测 prompt-cache 命中 ~99%**（真实 session：41,591 tokens 中 41,472 命中）。DeepSeek 对命中缓存的输入按未命中价的几十分之一计费（[官方定价](https://api-docs.deepseek.com/quick_start/pricing)），长跑几乎不为重复的上下文重复付费。

---

## ✨ 核心特性

- **🦫 单一 Go 二进制** —— 无 Node / Python 运行时，`curl` 一行装，macOS / Linux / Windows 全覆盖。
- **💰 缓存友好，长会话省钱** —— 围绕 DeepSeek 前缀缓存设计，实测 ~99% 命中；本地关键词路由零延迟、零 token 起手。
- **🧭 内置代码图谱（codegraph）** —— 符号级跳定义 / 找调用 / 接口实现 / 影响面分析，Go 经 `go/types` 精确解析，替代满仓库 grep。
- **👀 本地图片 OCR（PaddleOCR）** —— 离线读图，丢一张截图就能识别其中文字，不依赖多模态 API。
- **🧠 双模型自动路由** —— flash 起手省钱，复杂任务自动升 pro；也可用 `/model flash|pro` 锁定模型、`/auto` `/plan` `/review` 切模式。
- **🗂️ 顺序 Todo + 并发 Plan DAG** —— 多步任务用可见待办清单逐步勾选；可并行的独立子任务拆成 DAG 派并发子 agent。
- **💾 无损会话持久化** —— gob 完整保留 `tool_calls` / `tool results` / `reasoning_content`，重启无缝续接；超窗自动分层压缩。
- **🔌 MCP + Skill 生态** —— 原生 MCP；兼容 Claude 的 skill 目录，已有 skill 直接复用。
- **🛡️ 审核模式** —— 写文件 / 执行 Shell 默认需人工确认，安全可控。

## 📊 对比 Claude Code

|                  | **deepx-code**                       | Claude Code            |
| :--------------- | :----------------------------------- | :--------------------- |
| 分发             | Go 单二进制，`curl` 一行装           | Node（npm）            |
| 开源             | ✅ MIT                               | ❌ 闭源                |
| 模型             | DeepSeek / 小米 MiMo（OpenAI 兼容，配置时选供应商，flash/pro 自动路由） | Anthropic Claude       |
| 成本             | 长会话 ~99% 缓存命中，几乎不为重复上下文付费 | 订阅 / 按 Claude API 用量 |
| 内置代码图谱     | ✅ codegraph（Go 走 `go/types` 精确） | ❌（靠 grep / 搜索）   |
| 本地 · 离线 OCR  | ✅ PaddleOCR                         | ❌（图片走云端多模态） |
| MCP              | ✅                                   | ✅                     |
| Skill 生态       | ✅（兼容 Claude skill 目录）          | ✅                     |

> [!NOTE]
> 这张表不比模型质量本身；deepx-code 的取舍是 **成本、开源、单二进制、内置代码图谱与离线 OCR**。

## 🚀 快速开始

**1. 安装**

```bash
# macOS / Linux(尾部 `&& exec $SHELL` 让当前 shell 立即拿到新 PATH,不用再 source 或重开 terminal)
curl -fsSL https://raw.githubusercontent.com/itmisx/deepx-code/main/scripts/install.sh | bash && exec $SHELL

# Windows (PowerShell)
irm https://raw.githubusercontent.com/itmisx/deepx-code/main/scripts/install.ps1 | iex
```

安装到 `~/.local/bin/deepx`，随时用 `deepx upgrade` 升级。

**2. 进入项目并启动**

```bash
cd <你的项目目录>
deepx
```

**3. 配置**

| 项目        | 怎么做                                                        |
| :---------- | :----------------------------------------------------------- |
| 供应商 & Key | 首次启动弹出向导：**用 ←/→ 选模型供应商（DeepSeek / 小米 MiMo），再填对应 API Key**，持久化到 `~/.deepx/model.yaml`。各供应商已预置 flash/pro 默认模型与 1M 上下文（DeepSeek `deepseek-v4-flash` / `-pro`，MiMo `mimo-v2.5` / `-pro`）。也可 `/config` 重配。 |
| 手动覆盖    | 可直接编辑 `~/.deepx/model.yaml`，按 role（flash/pro）覆盖 `base_url` / `model` / `api_key` / `max_tokens` / `context_window`；flash 与 pro 也可指向不同供应商。 |
| Skill       | 放到 `<工作区>/.deepx/skills/`，或复用 `~/.claude/skills/` 等已有目录。 |
| MCP         | TUI 内 `/mcp-add` 添加，`/mcp-list` 查看。                    |

## 🧠 核心机制

<details>
<summary><b>模型路由（本地，零延迟，零 token）</b></summary>

用户消息发来时，deepx 在本地做关键词匹配 + 长度判定，瞬间决定起手模型，不额外消耗任何 LLM token：

```
消息含 "重构 / refactor / architecture / 调试 …" → 直接升 pro
消息长度 < 100 字符                              → flash
消息长度 > 500 字符                              → pro
```

覆盖中（简 / 繁）/ 英 / 日 / 韩五种语言。本轮中如遇复杂推理，模型还可主动 `SwitchModel` 升到 pro。

</details>

<details>
<summary><b>会话持久化（gob 二进制，无损续接）</b></summary>

```
~/.deepx/sessions/<sha1(workspace)[:16]>/
├── meta.json          # 工作区元信息
├── state.json         # 压缩状态 + 用量快照
├── YYYY-MM-DD.jsonl   # 文本日志（Memory 搜索用）
└── history.gob        # 二进制完整历史
```

| 格式               | 存储内容                                                                | 用途                       |
| :----------------- | :---------------------------------------------------------------------- | :------------------------- |
| `history.gob`      | system + user + assistant（含 `tool_calls`、`tool results`、`reasoning_content`） | **重启恢复，LLM 无缝续接** |
| `YYYY-MM-DD.jsonl` | user / assistant 纯文本                                                  | Memory 工具搜索            |

重启优先加载 gob，失败回退 JSONL。system prompt 因升级 / skill 变化而变动时，gob 恢复时自动原地替换为当前版本（保持缓存前缀一致）。

</details>

<details>
<summary><b>会话压缩（分层 + 摘要合并）</b></summary>

长对话超出上下文窗口 70% 时自动触发：尾部分层保留约 20K token，旧内容由 LLM 压成连贯摘要并合并已有摘要。压缩后同步更新 gob，重启一致。

</details>

<details>
<summary><b>任务规划：Todo（顺序）vs Plan DAG（并发）</b></summary>

- **Todo** —— 多步、强顺序、强上下文的任务（如从零搭一个应用）：模型用可见待办清单列出步骤、逐项勾选，自己一步步执行，给你实时进度。
- **CreatePlan（Plan DAG）** —— 真正可并行、彼此独立的扇出任务：拆成 DAG，按依赖关系派并发子 agent，每个节点独立选 flash / pro，最后汇总。

```
CreatePlan
  ├─ plan-1: Read  (flash) ─────┐
  ├─ plan-2: Read  (flash) ─────┤
  ├─ plan-3: Grep  (flash) ─────┤
  └─ plan-4: Write (pro)   ─────┘ depends_on: [1,2,3]
```

</details>

<details>
<summary><b>本地 OCR（补齐读图能力）</b></summary>

粘贴图片或给出图片路径 → LLM 通过 `OCR` 工具（PaddleOCR PP-OCRv5）识别其中文字。首次自动下载 OCR 模型（~37MB）与 ONNX runtime，之后**离线、秒级**响应。让你不依赖多模态 API 也能让 agent「看懂」报错截图 / UI 稿。

</details>

### 🧭 代码图谱（codegraph）

内置符号图谱引擎，模型直接做符号级导航 + 调用关系查询，代替满仓库 grep + 一个个翻文件。

<details>
<summary><b>操作速查表（12 个 op）</b></summary>

| op             | 用途                 | 必填参数                   | 说明                                            |
| :------------- | :------------------- | :------------------------- | :---------------------------------------------- |
| `def`          | 符号定义在哪         | `name`                     | 函数 / 类型 / 方法 / 变量的定义位置             |
| `refs`         | 谁用到了某符号       | `name`                     | 全部引用（定义 + 调用 + 取值）                  |
| `symbols`      | 按名模糊搜索符号     | `name`(可选), `kind`(可选) | `kind`: func/method/type/var/const/field        |
| `outline`      | 一个文件有哪些符号   | `path`                     | 文件大纲                                        |
| `imports`      | 文件 import 了哪些包 | `path`                     | 依赖概览                                        |
| `callers`      | 谁调用了某函数       | `name`                     | **改函数时查影响面**，Go 隐式接口也覆盖         |
| `callees`      | 某函数调用了哪些     | `name`                     | 理解函数内部实现流程                            |
| `implementers` | 谁实现了某接口       | `name`                     | 对 Go 隐式接口**精确到符号级**，grep 查不出     |
| `subtypes`     | 谁继承 / 嵌入某类型  | `name`                     | 子类型追踪                                      |
| `supertypes`   | 某类型派生自什么     | `name`                     | 父类型 / 嵌入接口                               |
| `impact`       | 改某符号牵连哪些下游 | `name`, `depth`(默认 3)    | 传递闭包，blast radius 分析                     |
| `reindex`      | 强制重建索引         | —                          | 缓存异常时手动触发                              |

</details>

**覆盖语言**：Go（stdlib 精确解析）+ TypeScript / JavaScript / Python / Java / Rust / C / C++ / C# / Ruby / PHP / Kotlin / Swift / Scala / Dart / Vue / Svelte。

**工作机制**：启动后台 `Prewarm` 建索引（状态栏 `loading → ready`）；文件被 Write/Update 后标 `stale`，下次查询增量重建；结果按 `文件:行`（含签名 / 调用方）展示并自动分页。

## 🧰 工具集

| 类型     | 工具                               |         plan | auto | review |
| :------- | :--------------------------------- | -----------: | :--: | :----: |
| 文件只读 | `Read` `List` `Tree` `Glob` `Grep` |            ✓ |  ✓   |   ✓    |
| 代码图谱 | `CodeGraph`                        |            ✓ |  ✓   |   ✓    |
| 文件写入 | `Write` `Update`                   |            ✗ |  ✓   |   ⏳   |
| Shell    | `Bash`                             |            ✗ |  ✓   |   ⏳   |
| 联网     | `Search` `Fetch`                   |            ✓ |  ✓   |   ✓    |
| 记忆     | `Memory`                           |            ✓ |  ✓   |   ✓    |
| 技能     | `LoadSkill`                        |            ✓ |  ✓   |   ✓    |
| 图片     | `OCR`                              |            ✓ |  ✓   |   ✓    |
| 规划     | `Todo` `CreatePlan`                | LLM 自主调用 |      |        |
| 升级     | `SwitchModel`                      | LLM 自主调用 |      |        |

> ⏳ = 自动执行，但需人工确认。

## ⌨️ Slash 命令

| 命令                       | 作用                              |
| :------------------------- | :-------------------------------- |
| `/plan` `/auto` `/review`  | 切换模式（只读 / 全自动 / 审核）  |
| `/model`                   | 弹窗选择模型（auto 按任务路由 / flash / pro 定死）；也可 `/model flash` 直接指定 |
| `/compact`                 | 手动压缩会话以节省上下文          |
| `/lang`                    | 切换界面语言（中 / 英）           |
| `/mcp-list` `/mcp-add` `/mcp-delete` | 管理 MCP server         |
| `/skills` `/config` `/mode`| 列出 skill / 重配 key / 查看模式  |
| `/help`                    | 帮助                              |

## 🛡️ 审核模式

| 模式             | Write / Update / Bash | 其余工具 | 切换命令  |
| :--------------- | :-------------------- | :------- | :-------- |
| `review`（默认） | 人工 YES/NO 确认      | 自动执行 | `/review` |
| `auto`           | 自动执行              | 自动执行 | `/auto`   |
| `plan`           | 禁用                  | 自动执行 | `/plan`   |

## 📦 Skills 生态

```
workspace 级  <wd>/.deepx/skills/
global 级     ~/.agents/skills/ → ~/.claude/skills/ → ~/.deepx/skills/
```

- workspace 级可 `git add` 共享给团队
- global 兼容 Claude Code 生态，已有 skill 直接复用

## 🏗️ 架构

<details>
<summary><b>展开数据流</b></summary>

```
单轮对话:
  用户输入
    ↓
  RouteByKeyword (本地) ─► flash 或 pro
    ↓
  StartStream (主循环)
    ├─ 直接答
    ├─ 调工具 → review 拦截写/Shell → 执行 → 结果回灌 → 继续
    ├─ Todo → 可见待办清单(主 agent 自己逐步执行)
    ├─ SwitchModel → 升 pro
    └─ CreatePlan → DAG scheduler → 子 agent 并发 → 汇总

会话持久化:
  HistoryUpdateMsg → SaveGob (history.gob, 完整 fidelity)
  StreamDoneMsg    → Append JSONL (纯文本, Memory 搜索)
  重启             → LoadGob (优先) / JSONL (回退)

会话压缩:
  tokens ≥ ctxWindow × 70% → runCompression (异步)
    → 尾部分层保留 ~20K token → LLM 合并新旧摘要 → 更新 gob + state.json
```

</details>

**目录结构**

```
deepx/
├── main.go
├── agent/      StartStream 工具循环 + 路由 + DAG 调度 + 子 agent
├── config/     ~/.deepx/model.yaml 读写
├── session/    gob 持久化 + JSONL 日志 + 会话压缩状态
├── tools/      全部工具实现（读写 / 搜索 / OCR / Memory / Skill / Plan / CodeGraph）
├── codegraph/  代码图谱：跳定义 / 找调用 / 继承实现 / 影响面
├── skill/      多路径 skill 发现与加载
├── ocr/        PaddleOCR 包装（ONNX Runtime）
├── tui/        bubbletea TUI（输入 / 渲染 / 剪贴板 / 选中 / 仪表盘）
└── scripts/    安装脚本
```

## 💰 Token 经济

- **路由零 token**：纯本地关键词，不发 LLM 调用
- **工具不预注入**：`Memory` / `LoadSkill` 只在调用时才进 context
- **system prompt 极简**：仅跨工具规约 + workspace，工具触发条件在各自 description 里
- **DeepSeek KV cache 友好**：tools 数组不随模式 / 角色变化；system prompt gob 恢复时版本感知
- **代码图谱替代盲搜**：从根上减少 read / glob / grep 的 token 浪费

## 🩹 卸载

```bash
# macOS / Linux
rm -f ~/.local/bin/deepx && rm -rf ~/.deepx

# Windows：删除 %LOCALAPPDATA%\Programs\deepx 和 %USERPROFILE%\.deepx
```

## 📄 License

[MIT](LICENSE) © 2026 itmisx
