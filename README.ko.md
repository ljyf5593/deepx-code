<div align="center">

# deepx-code

**DeepSeek 네이티브, OpenAI 호환 터미널 코딩 에이전트(Xiaomi MiMo 지원) —— 단일 바이너리, 캐시 친화적, 코드 그래프와 로컬 OCR 내장**

[![Go](https://img.shields.io/badge/built%20with-Go-00ADD8?logo=go&logoColor=white)](https://go.dev) [![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE) [![Release](https://img.shields.io/github/v/release/itmisx/deepx-code?color=success)](https://github.com/itmisx/deepx-code/releases) [![Stars](https://img.shields.io/github/stars/itmisx/deepx-code?style=flat)](https://github.com/itmisx/deepx-code/stargazers) ![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)

[简体中文](README.md) · [English](README.en.md) · [日本語](README.ja.md) · **한국어**

![deepx-code demo](assets/demo.gif)

</div>

> [!TIP]
> **⚡ 긴 세션에서 prompt-cache 적중률 ~99% 실측**（실제 세션: 41,591 토큰 중 41,472 적중）. DeepSeek은 캐시 적중 입력을 캐시 미스의 수십분의 1로 과금하므로（[공식 가격](https://api-docs.deepseek.com/quick_start/pricing)）, 긴 실행에서도 반복되는 컨텍스트에 거의 과금되지 않습니다.

---

## ✨ 핵심 기능

- **🦫 단일 Go 바이너리** —— Node / Python 런타임 불필요, `curl` 한 줄 설치, macOS / Linux / Windows 지원.
- **💰 캐시 친화적, 긴 세션도 저렴** —— DeepSeek 프리픽스 캐시를 중심으로 설계(실측 ~99% 적중). 로컬 키워드 라우팅은 매 턴 지연 0·토큰 0으로 시작.
- **🧭 코드 그래프 내장(codegraph)** —— 심볼 단위 정의 이동 / 호출자 / 인터페이스 구현 / 영향 범위. Go는 `go/types`로 정확히 해석하여 저장소 전체 grep을 대체.
- **👀 로컬 이미지 OCR(PaddleOCR)** —— 스크린샷의 텍스트를 오프라인으로 읽고, 멀티모달 API 불필요.
- **🧠 듀얼 모델 자동 라우팅** —— 가벼운 작업은 flash, 복잡한 작업은 자동으로 pro 승격. `/model flash|pro`로 모델 고정, `/auto` `/plan` `/review`로 모드 전환도 가능.
- **🗂️ 순차 Todo + 병렬 Plan DAG** —— 다단계 작업은 보이는 체크리스트로 한 단계씩. 독립적인 병렬 작업은 DAG로 분해해 서브 에이전트를 병렬 실행.
- **💾 무손실 세션 영속화** —— gob가 `tool_calls` / 도구 결과 / `reasoning_content`를 완전 보존해 재시작 후에도 매끄럽게 이어감. 윈도가 차면 자동 계층 압축.
- **🔌 MCP + Skill 생태계** —— MCP 네이티브 지원. Claude의 skill 디렉터리와 호환되어 기존 skill을 그대로 재사용.
- **🛡️ 검토 모드** —— 파일 쓰기 / Shell 실행은 기본적으로 사람의 확인을 요구.

## 📊 Claude Code 비교

|                     | **deepx-code**                          | Claude Code              |
| :------------------ | :-------------------------------------- | :----------------------- |
| 배포                | 단일 Go 바이너리, `curl` 한 줄           | Node(npm)                |
| 오픈소스            | ✅ MIT                                  | ❌ 비공개                |
| 모델                | DeepSeek / Xiaomi MiMo(OpenAI 호환, 설정 시 공급자 선택, flash/pro 자동 라우팅) | Anthropic Claude        |
| 비용                | 긴 세션 ~99% 캐시 적중                   | 구독 / Claude API 사용량  |
| 코드 그래프 내장    | ✅ codegraph(Go는 `go/types`로 정확)     | ❌(grep / 검색)          |
| 로컬·오프라인 OCR   | ✅ PaddleOCR                            | ❌(이미지는 클라우드 멀티모달) |
| MCP                 | ✅                                      | ✅                       |
| Skill 생태계        | ✅(Claude skill 디렉터리 재사용)         | ✅                       |

> [!NOTE]
> 이 표는 모델 품질 자체를 비교하지 않습니다. deepx-code의 선택은 **비용·오픈소스·단일 바이너리·내장 코드 그래프·오프라인 OCR** 입니다.

## 🚀 빠른 시작

**1. 설치**

```bash
# macOS / Linux(끝의 `&& exec $SHELL`이 현재 셸을 새로 시작해 PATH에 deepx를 즉시 반영합니다. rc 소스나 새 터미널을 열 필요가 없습니다)
curl -fsSL https://raw.githubusercontent.com/itmisx/deepx-code/main/scripts/install.sh | bash && exec $SHELL

# Windows (PowerShell)
irm https://raw.githubusercontent.com/itmisx/deepx-code/main/scripts/install.ps1 | iex
```

`~/.local/bin/deepx`에 설치됩니다. `deepx upgrade`로 언제든 업데이트.

**2. 프로젝트로 이동해 실행**

```bash
cd <당신의 프로젝트>
deepx
```

**3. 설정**

| 항목          | 방법                                                         |
| :------------ | :----------------------------------------------------------- |
| 공급자 & Key | 첫 실행 마법사에서 **←/→로 공급자(DeepSeek / Xiaomi MiMo)를 선택하고 해당 API Key를 입력**해 `~/.deepx/model.yaml`에 저장. 각 공급자에 flash/pro 기본 모델과 1M 컨텍스트 제공(DeepSeek `deepseek-v4-flash` / `-pro`, MiMo `mimo-v2.5` / `-pro`). `/config`로 재설정. |
| 수동 재정의   | `~/.deepx/model.yaml`을 직접 편집해 role(flash/pro)별로 `base_url` / `model` / `api_key` / `max_tokens` / `context_window`를 재정의 가능. flash와 pro가 서로 다른 공급자를 가리킬 수도 있음. |
| Skill         | `<워크스페이스>/.deepx/skills/`에 두거나 `~/.claude/skills/` 등 재사용. |
| MCP           | TUI에서 `/mcp-add`로 추가, `/mcp-list`로 목록 확인.          |

## 🧠 동작 원리

<details>
<summary><b>모델 라우팅(로컬, 지연 0, 토큰 0)</b></summary>

메시지가 도착하면 deepx는 로컬에서 키워드 매칭 + 길이 판정을 수행하여, 추가 LLM 토큰 없이 시작 모델을 즉시 결정합니다:

```
"리팩터링 / refactor / architecture / 디버깅 …" 포함 → 바로 pro
길이 < 100자                                         → flash
길이 > 500자                                         → pro
```

중국어(간체 / 번체) / 영어 / 일본어 / 한국어를 지원. 턴 도중에도 모델은 어려운 추론을 위해 `SwitchModel`로 pro 승격이 가능합니다.

</details>

<details>
<summary><b>세션 영속화(gob 바이너리, 무손실 복원)</b></summary>

```
~/.deepx/sessions/<sha1(workspace)[:16]>/
├── meta.json          # 워크스페이스 메타 정보
├── state.json         # 압축 상태 + 사용량 스냅샷
├── YYYY-MM-DD.jsonl   # 텍스트 로그(Memory 검색용)
└── history.gob        # 완전한 바이너리 히스토리
```

| 형식               | 저장 내용                                                               | 용도                          |
| :----------------- | :--------------------------------------------------------------------- | :---------------------------- |
| `history.gob`      | system + user + assistant(`tool_calls`·도구 결과·`reasoning_content` 포함) | **재시작 복원, 매끄러운 이어가기** |
| `YYYY-MM-DD.jsonl` | user / assistant 일반 텍스트                                            | Memory 도구 검색              |

재시작 시 gob를 우선 로드하고 실패하면 JSONL로 폴백. 업그레이드나 skill 변경으로 system prompt가 바뀌면 gob 복원 시 현재 버전으로 투명하게 교체하여 캐시 프리픽스를 안정적으로 유지합니다.

</details>

<details>
<summary><b>세션 압축(계층 + 요약 병합)</b></summary>

컨텍스트 윈도의 70%를 넘으면 자동 발동: 꼬리 부분에 약 20K 토큰을 계층적으로 남기고, 오래된 내용은 LLM이 일관된 요약으로 압축해 기존 요약과 병합합니다. gob도 갱신되어 재시작 후에도 일관됩니다.

</details>

<details>
<summary><b>작업 계획: Todo(순차) vs Plan DAG(병렬)</b></summary>

- **Todo** —— 다단계·순차·컨텍스트 의존 작업(예: 앱을 처음부터 구축): 모델이 보이는 체크리스트에 단계를 나열하고 하나씩 체크하며 스스로 실행해, 실시간 진행 상황을 보여줍니다.
- **CreatePlan(Plan DAG)** —— 진짜로 병렬·독립적인 팬아웃: DAG로 분해해 의존 순서대로 병렬 서브 에이전트를 실행하고, 각 노드가 flash / pro를 선택한 뒤 집계합니다.

```
CreatePlan
  ├─ plan-1: Read  (flash) ─────┐
  ├─ plan-2: Read  (flash) ─────┤
  ├─ plan-3: Grep  (flash) ─────┤
  └─ plan-4: Write (pro)   ─────┘ depends_on: [1,2,3]
```

</details>

<details>
<summary><b>로컬 OCR(이미지 읽기 보완)</b></summary>

이미지를 붙여넣거나 경로를 주면 LLM이 `OCR` 도구(PaddleOCR PP-OCRv5)로 텍스트를 읽습니다. 첫 호출 시 OCR 모델(~37MB)과 ONNX runtime을 내려받고, 이후에는 **오프라인으로 수 초 내** 응답. 멀티모달 API 없이도 에러 스크린샷이나 UI 목업을 에이전트가 "읽을" 수 있습니다.

</details>

### 🧭 코드 그래프(codegraph)

심볼 그래프 엔진을 내장하여, 저장소 전체 grep과 파일을 하나씩 여는 대신 모델이 심볼 단위 내비게이션 + 호출 관계 쿼리를 직접 수행합니다.

<details>
<summary><b>op 치트시트(12개)</b></summary>

| op             | 용도                       | 필수                       | 설명                                            |
| :------------- | :------------------------- | :------------------------- | :---------------------------------------------- |
| `def`          | 심볼 정의 위치              | `name`                    | 함수 / 타입 / 메서드 / 변수의 정의 위치         |
| `refs`         | 심볼 사용처                 | `name`                    | 모든 참조(정의 + 호출 + 읽기)                   |
| `symbols`      | 이름으로 퍼지 검색          | `name`(선택), `kind`(선택) | `kind`: func/method/type/var/const/field        |
| `outline`      | 파일 내 심볼 목록           | `path`                    | 파일 아웃라인                                   |
| `imports`      | 파일의 import 목록          | `path`                    | 의존성 개요                                     |
| `callers`      | 함수의 호출자               | `name`                    | **변경 시 영향 범위**, Go 암시적 인터페이스도 포함 |
| `callees`      | 함수가 호출하는 것          | `name`                    | 내부 흐름 이해                                  |
| `implementers` | 인터페이스 구현자           | `name`                    | Go 암시적 인터페이스를 **심볼 정밀도**로. grep 불가 |
| `subtypes`     | 타입을 상속 / 임베드하는 것 | `name`                    | 서브타입 추적                                   |
| `supertypes`   | 타입의 파생 원본            | `name`                    | 슈퍼타입 / 임베드 인터페이스                    |
| `impact`       | 변경의 하위 영향            | `name`, `depth`(기본 3)   | 전이 폐포, 영향 범위 분석                       |
| `reindex`      | 인덱스 강제 재구축          | —                          | 캐시 이상 시 수동 트리거                        |

</details>

**지원 언어**: Go(stdlib 정밀 파싱) + TypeScript / JavaScript / Python / Java / Rust / C / C++ / C# / Ruby / PHP / Kotlin / Swift / Scala / Dart / Vue / Svelte.

**동작**: 시작 시 백그라운드 `Prewarm`이 인덱스를 구축(`loading → ready`). Write/Update로 변경된 파일은 `stale`로 표시되어 다음 쿼리에서 증분 재구축. 결과는 `파일:행`(시그니처 / 호출자 포함)으로 표시되고 페이지네이션됩니다.

## 🧰 도구

| 종류        | 도구                               |       plan | auto | review |
| :---------- | :--------------------------------- | ---------: | :--: | :----: |
| 읽기 전용   | `Read` `List` `Tree` `Glob` `Grep` |          ✓ |  ✓   |   ✓    |
| 코드 그래프 | `CodeGraph`                        |          ✓ |  ✓   |   ✓    |
| 파일 쓰기   | `Write` `Update`                   |          ✗ |  ✓   |   ⏳   |
| Shell       | `Bash`                             |          ✗ |  ✓   |   ⏳   |
| 웹          | `Search` `Fetch`                   |          ✓ |  ✓   |   ✓    |
| 메모리      | `Memory`                           |          ✓ |  ✓   |   ✓    |
| Skill       | `LoadSkill`                        |          ✓ |  ✓   |   ✓    |
| 이미지      | `OCR`                              |          ✓ |  ✓   |   ✓    |
| 계획        | `Todo` `CreatePlan`                | LLM이 호출 |     |        |
| 승격        | `SwitchModel`                      | LLM이 호출 |     |        |

> ⏳ = 자동 실행되지만 사람의 확인이 필요.

## ⌨️ 슬래시 명령

| 명령                                 | 동작                                |
| :----------------------------------- | :---------------------------------- |
| `/plan` `/auto` `/review`            | 모드 전환(읽기 전용 / 자동 / 검토)  |
| `/model`                             | 모델 선택 팝업(auto=작업별 라우팅 / flash / pro 고정); `/model flash` 직접 지정도 가능 |
| `/compact`                           | 세션 수동 압축                      |
| `/lang`                              | UI 언어 전환(중 / 영)               |
| `/mcp-list` `/mcp-add` `/mcp-delete` | MCP 서버 관리                       |
| `/skills` `/config` `/mode`          | skill 목록 / key 재설정 / 모드 표시 |
| `/help`                              | 도움말                              |

## 🛡️ 검토 모드

| 모드               | Write / Update / Bash | 기타 도구 | 명령      |
| :----------------- | :-------------------- | :-------- | :-------- |
| `review`(기본)     | 사람이 YES/NO 확인    | 자동 실행 | `/review` |
| `auto`             | 자동 실행             | 자동 실행 | `/auto`   |
| `plan`             | 비활성                | 자동 실행 | `/plan`   |

## 📦 Skill

```
워크스페이스  <wd>/.deepx/skills/
글로벌        ~/.agents/skills/ → ~/.claude/skills/ → ~/.deepx/skills/
```

- 워크스페이스 단위는 `git add`로 팀과 공유 가능
- 글로벌은 Claude Code 호환 —— 기존 skill을 그대로 재사용

## 🏗️ 아키텍처

<details>
<summary><b>데이터 흐름 펼치기</b></summary>

```
1 턴:
  사용자 입력
    ↓
  RouteByKeyword (로컬) ─► flash 또는 pro
    ↓
  StartStream (메인 루프)
    ├─ 직접 답변
    ├─ 도구 호출 → review가 쓰기/Shell 게이트 → 실행 → 결과 회신 → 계속
    ├─ Todo → 보이는 체크리스트(메인 에이전트가 단계별 실행)
    ├─ SwitchModel → pro 승격
    └─ CreatePlan → DAG scheduler → 병렬 서브 에이전트 → 집계

영속화:
  HistoryUpdateMsg → SaveGob (history.gob, 완전 충실)
  StreamDoneMsg    → Append JSONL (일반 텍스트, Memory 검색)
  재시작           → LoadGob (우선) / JSONL (폴백)

압축:
  tokens ≥ ctxWindow × 70% → runCompression (비동기)
    → 꼬리에 ~20K 토큰 유지 → LLM이 신·구 요약 병합 → gob + state.json 갱신
```

</details>

**디렉터리 구조**

```
deepx/
├── main.go
├── agent/      StartStream 도구 루프 + 라우팅 + DAG 스케줄러 + 서브 에이전트
├── config/     ~/.deepx/model.yaml 읽기/쓰기
├── session/    gob 영속화 + JSONL 로그 + 압축 상태
├── tools/      모든 도구 구현(읽기/쓰기 / 검색 / OCR / Memory / Skill / Plan / CodeGraph)
├── codegraph/  코드 그래프: 정의 / 호출 / 상속 구현 / 영향 범위
├── skill/      다중 경로 skill 탐색 및 로드
├── ocr/        PaddleOCR 래퍼(ONNX Runtime)
├── tui/        bubbletea TUI(입력 / 렌더 / 클립보드 / 선택 / 대시보드)
└── scripts/    설치 스크립트
```

## 💰 토큰 경제

- **라우팅 토큰 0**: 순수 로컬 키워드, LLM 호출 없음
- **도구 사전 주입 없음**: `Memory` / `LoadSkill`은 호출 시에만 context에 진입
- **system prompt 최소화**: 도구 공통 규약 + workspace만, 트리거 조건은 각 도구 description에
- **DeepSeek KV 캐시 친화**: tools 배열은 모드 / 역할로 바뀌지 않고, system prompt는 gob 복원 시 버전 인식
- **맹목적 검색보다 코드 그래프**: read / glob / grep 토큰 낭비를 근본부터 절감

## 🩹 제거

```bash
# macOS / Linux
rm -f ~/.local/bin/deepx && rm -rf ~/.deepx

# Windows: %LOCALAPPDATA%\Programs\deepx 와 %USERPROFILE%\.deepx 삭제
```

## 📄 License

[MIT](LICENSE) © 2026 itmisx
