<div align="center">

# deepx-code

**A DeepSeek-native, OpenAI-compatible coding agent for your terminal (Xiaomi MiMo now supported) — single binary, cache-friendly, with a built-in code graph and local OCR**

[![Go](https://img.shields.io/badge/built%20with-Go-00ADD8?logo=go&logoColor=white)](https://go.dev) [![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE) [![Release](https://img.shields.io/github/v/release/itmisx/deepx-code?color=success)](https://github.com/itmisx/deepx-code/releases) [![Stars](https://img.shields.io/github/stars/itmisx/deepx-code?style=flat)](https://github.com/itmisx/deepx-code/stargazers) ![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)

[简体中文](README.md) · **English** · [日本語](README.ja.md) · [한국어](README.ko.md)

![deepx-code demo](assets/demo.gif)

</div>

> [!TIP]
> **⚡ ~99% prompt-cache hit on long sessions (measured)** — a real session: 41,472 of 41,591 tokens cached. DeepSeek bills cache-hit input at a fraction of cache-miss ([official pricing](https://api-docs.deepseek.com/quick_start/pricing)), so long runs barely pay to re-process context.

---

## ✨ Highlights

- **🦫 Single Go binary** — no Node / Python runtime, one-line `curl` install, macOS / Linux / Windows.
- **💰 Cache-friendly, cheap long sessions** — engineered around DeepSeek's prefix cache (~99% hit measured); local keyword routing starts every turn with zero latency and zero tokens.
- **🧭 Built-in code graph (codegraph)** — symbol-level go-to-def / callers / interface impls / blast-radius, precise on Go via `go/types`. Replaces whole-repo grep.
- **👀 Local image OCR (PaddleOCR)** — read text from a screenshot offline, no multimodal API needed.
- **🧠 Dual-model auto-routing** — flash for cheap iteration, auto-escalates to pro for hard work; pin a model with `/model flash|pro` or switch mode with `/auto` `/plan` `/review`.
- **🗂️ Sequential Todo + concurrent Plan DAG** — step through a visible checklist for multi-step work; fan out independent subtasks to concurrent sub-agents.
- **💾 Lossless session persistence** — gob preserves `tool_calls` / tool results / `reasoning_content`, so restarts resume seamlessly; auto layered compaction when the window fills.
- **🔌 MCP + skill ecosystem** — native MCP; compatible with Claude's skill directories, reuse what you have.
- **🛡️ Review mode** — file writes / shell run behind human confirmation by default.

## 📊 vs Claude Code

|                   | **deepx-code**                          | Claude Code              |
| :---------------- | :-------------------------------------- | :----------------------- |
| Distribution      | Single Go binary, one-line `curl`       | Node (npm)               |
| Open source       | ✅ MIT                                  | ❌ Closed                |
| Model             | DeepSeek / Xiaomi MiMo (OpenAI-compatible, pick provider at setup, flash/pro auto-routing) | Anthropic Claude       |
| Cost              | ~99% cache hit on long sessions         | Subscription / Claude API usage |
| Built-in code graph | ✅ codegraph (precise on Go via `go/types`) | ❌ (grep / search)   |
| Local · offline OCR | ✅ PaddleOCR                          | ❌ (images via cloud multimodal) |
| MCP               | ✅                                      | ✅                       |
| Skill ecosystem   | ✅ (reuses Claude skill dirs)            | ✅                       |

> [!NOTE]
> This isn't about model quality itself; deepx-code's trade-off is **cost, open source, a single binary, a built-in code graph, and offline OCR**.

## 🚀 Quick Start

**1. Install**

```bash
# macOS / Linux (the trailing `&& exec $SHELL` refreshes your current shell so `deepx` is on PATH immediately — no need to source rc or open a new terminal)
curl -fsSL https://raw.githubusercontent.com/itmisx/deepx-code/main/scripts/install.sh | bash && exec $SHELL

# Windows (PowerShell)
irm https://raw.githubusercontent.com/itmisx/deepx-code/main/scripts/install.ps1 | iex
```

Installs to `~/.local/bin/deepx`; upgrade any time with `deepx upgrade`.

**2. Enter a project and launch**

```bash
cd <your-project>
deepx
```

**3. Configure**

| Item         | How                                                          |
| :----------- | :----------------------------------------------------------- |
| Provider & key | A wizard prompts on first run: **use ←/→ to pick a provider (DeepSeek / Xiaomi MiMo), then enter its API key**, persisted to `~/.deepx/model.yaml`. Each provider ships default flash/pro models and 1M context (DeepSeek `deepseek-v4-flash` / `-pro`, MiMo `mimo-v2.5` / `-pro`). Reconfigure with `/config`. |
| Manual override | Edit `~/.deepx/model.yaml` directly to override `base_url` / `model` / `api_key` / `max_tokens` / `context_window` per role (flash/pro); flash and pro may even point at different providers. |
| Skills       | Drop into `<workspace>/.deepx/skills/`, or reuse `~/.claude/skills/` etc. |
| MCP          | Add via `/mcp-add` inside the TUI; list with `/mcp-list`.    |

## 🧠 How It Works

<details>
<summary><b>Model routing (local, zero latency, zero tokens)</b></summary>

When your message arrives, deepx does local keyword matching + a length check and picks the starting model instantly, with no extra LLM tokens:

```
contains "refactor / architecture / debug …"  → straight to pro
length < 100 chars                             → flash
length > 500 chars                             → pro
```

Covers Chinese (Simplified / Traditional) / English / Japanese / Korean. Mid-turn, the model can also `SwitchModel` up to pro for hard reasoning.

</details>

<details>
<summary><b>Session persistence (gob binary, lossless resume)</b></summary>

```
~/.deepx/sessions/<sha1(workspace)[:16]>/
├── meta.json          # workspace metadata
├── state.json         # compaction state + usage snapshot
├── YYYY-MM-DD.jsonl   # text log (for Memory search)
└── history.gob        # full binary history
```

| Format             | Stores                                                                 | Purpose                         |
| :----------------- | :--------------------------------------------------------------------- | :------------------------------ |
| `history.gob`      | system + user + assistant (incl. `tool_calls`, tool results, `reasoning_content`) | **restart resume, seamless** |
| `YYYY-MM-DD.jsonl` | user / assistant plain text                                            | Memory tool search              |

Restart loads gob first, falling back to JSONL. If the system prompt changes (upgrade / skill change), it's transparently replaced on gob restore to keep the cache prefix stable.

</details>

<details>
<summary><b>Session compaction (layered + summary merge)</b></summary>

Triggers automatically past 70% of the context window: keeps ~20K tokens at the tail in layers, and the LLM compresses older content into a coherent summary merged with the existing one. The gob is updated too, so restarts stay consistent.

</details>

<details>
<summary><b>Planning: Todo (sequential) vs Plan DAG (concurrent)</b></summary>

- **Todo** — for multi-step, sequential, context-heavy work (e.g. building an app from scratch): the model lists the steps in a visible checklist, ticks them off, and executes them itself, giving you live progress.
- **CreatePlan (Plan DAG)** — for genuinely parallel, independent fan-out: split into a DAG, run concurrent sub-agents by dependency order, each node picking flash / pro, then summarize.

```
CreatePlan
  ├─ plan-1: Read  (flash) ─────┐
  ├─ plan-2: Read  (flash) ─────┤
  ├─ plan-3: Grep  (flash) ─────┤
  └─ plan-4: Write (pro)   ─────┘ depends_on: [1,2,3]
```

</details>

<details>
<summary><b>Local OCR (fills the image-reading gap)</b></summary>

Paste an image or give a path → the LLM reads its text via the `OCR` tool (PaddleOCR PP-OCRv5). The first call downloads the OCR model (~37MB) and the ONNX runtime; after that it's **offline and responds in seconds**. Lets the agent "see" an error screenshot or UI mockup without a multimodal API.

</details>

### 🧭 Code graph (codegraph)

A built-in symbol-graph engine lets the model do symbol-level navigation + call-relationship queries instead of grepping the whole repo and opening files one by one.

<details>
<summary><b>Op cheat sheet (12 ops)</b></summary>

| op             | Purpose                  | Required                   | Notes                                           |
| :------------- | :----------------------- | :------------------------- | :---------------------------------------------- |
| `def`          | Where is a symbol defined | `name`                    | def site of func / type / method / var          |
| `refs`         | Who uses a symbol         | `name`                    | all references (def + call + read)              |
| `symbols`      | Fuzzy search symbols      | `name`(opt), `kind`(opt)  | `kind`: func/method/type/var/const/field        |
| `outline`      | Symbols in a file         | `path`                    | file outline                                    |
| `imports`      | What a file imports        | `path`                    | dependency overview                             |
| `callers`      | Who calls a function       | `name`                    | **blast radius when changing it**; covers Go implicit interfaces |
| `callees`      | What a function calls       | `name`                    | understand internal flow                        |
| `implementers` | Who implements an interface | `name`                  | **symbol-precise** for Go implicit interfaces; grep can't |
| `subtypes`     | Who inherits / embeds a type | `name`                  | subtype tracking                                |
| `supertypes`   | What a type derives from   | `name`                    | super types / embedded interfaces               |
| `impact`       | Downstream of changing a symbol | `name`, `depth`(def 3) | transitive closure, blast-radius analysis    |
| `reindex`      | Force a rebuild            | —                          | manual trigger if the cache misbehaves          |

</details>

**Languages**: Go (precise stdlib parsing) + TypeScript / JavaScript / Python / Java / Rust / C / C++ / C# / Ruby / PHP / Kotlin / Swift / Scala / Dart / Vue / Svelte.

**Mechanics**: a background `Prewarm` builds the index at startup (`loading → ready`); files edited via Write/Update are marked `stale` and incrementally rebuilt on next query; results show as `file:line` (with signatures / callers) and paginate.

## 🧰 Tools

| Type        | Tools                              |       plan | auto | review |
| :---------- | :--------------------------------- | ---------: | :--: | :----: |
| Read-only   | `Read` `List` `Tree` `Glob` `Grep` |          ✓ |  ✓   |   ✓    |
| Code graph  | `CodeGraph`                        |          ✓ |  ✓   |   ✓    |
| File write  | `Write` `Update`                   |          ✗ |  ✓   |   ⏳   |
| Shell       | `Bash`                             |          ✗ |  ✓   |   ⏳   |
| Web         | `Search` `Fetch`                   |          ✓ |  ✓   |   ✓    |
| Memory      | `Memory`                           |          ✓ |  ✓   |   ✓    |
| Skill       | `LoadSkill`                        |          ✓ |  ✓   |   ✓    |
| Image       | `OCR`                              |          ✓ |  ✓   |   ✓    |
| Planning    | `Todo` `CreatePlan`                | LLM-invoked |     |        |
| Upgrade     | `SwitchModel`                      | LLM-invoked |     |        |

> ⏳ = runs automatically but needs human confirmation.

## ⌨️ Slash Commands

| Command                              | Action                              |
| :----------------------------------- | :---------------------------------- |
| `/plan` `/auto` `/review`            | switch mode (read-only / auto / review) |
| `/model`                             | popup to pick the model (auto routes by task / flash / pro lock); `/model flash` also works directly |
| `/compact`                           | manually compact the session        |
| `/lang`                              | switch UI language (zh / en)        |
| `/mcp-list` `/mcp-add` `/mcp-delete` | manage MCP servers                  |
| `/skills` `/config` `/mode`          | list skills / reconfigure key / show mode |
| `/help`                              | help                                |

## 🛡️ Review Modes

| Mode               | Write / Update / Bash | Other tools | Command   |
| :----------------- | :-------------------- | :---------- | :-------- |
| `review` (default) | human YES/NO          | automatic   | `/review` |
| `auto`             | automatic             | automatic   | `/auto`   |
| `plan`             | disabled              | automatic   | `/plan`   |

## 📦 Skills

```
workspace  <wd>/.deepx/skills/
global     ~/.agents/skills/ → ~/.claude/skills/ → ~/.deepx/skills/
```

- workspace-level can be `git add`-ed and shared with your team
- global is Claude Code-compatible — reuse existing skills directly

## 🏗️ Architecture

<details>
<summary><b>Expand data flow</b></summary>

```
Single turn:
  user input
    ↓
  RouteByKeyword (local) ─► flash or pro
    ↓
  StartStream (main loop)
    ├─ answer directly
    ├─ call tool → review gates write/shell → run → feed result back → continue
    ├─ Todo → visible checklist (main agent executes it step by step)
    ├─ SwitchModel → upgrade to pro
    └─ CreatePlan → DAG scheduler → concurrent sub-agents → summarize

Persistence:
  HistoryUpdateMsg → SaveGob (history.gob, full fidelity)
  StreamDoneMsg    → Append JSONL (plain text, Memory search)
  restart          → LoadGob (preferred) / JSONL (fallback)

Compaction:
  tokens ≥ ctxWindow × 70% → runCompression (async)
    → keep ~20K tokens at the tail → LLM merges old + new summary → update gob + state.json
```

</details>

**Layout**

```
deepx/
├── main.go
├── agent/      StartStream tool loop + routing + DAG scheduler + sub-agents
├── config/     ~/.deepx/model.yaml read/write
├── session/    gob persistence + JSONL log + compaction state
├── tools/      all tool implementations (read/write / search / OCR / Memory / Skill / Plan / CodeGraph)
├── codegraph/  code graph: def / callers / inheritance / impact
├── skill/      multi-path skill discovery & loading
├── ocr/        PaddleOCR wrapper (ONNX Runtime)
├── tui/        bubbletea TUI (input / render / clipboard / selection / dashboard)
└── scripts/    install scripts
```

## 💰 Token Economy

- **Zero-token routing**: pure local keywords, no LLM call
- **No tool pre-injection**: `Memory` / `LoadSkill` enter context only when called
- **Minimal system prompt**: only cross-tool rules + workspace; trigger conditions live in each tool's description
- **DeepSeek KV-cache friendly**: the tools array doesn't change with mode / role; the system prompt is version-aware on gob restore
- **Code graph over blind search**: cuts read / glob / grep token waste at the root

## 🩹 Uninstall

```bash
# macOS / Linux
rm -f ~/.local/bin/deepx && rm -rf ~/.deepx

# Windows: delete %LOCALAPPDATA%\Programs\deepx and %USERPROFILE%\.deepx
```

## 📄 License

[MIT](LICENSE) © 2026 itmisx
