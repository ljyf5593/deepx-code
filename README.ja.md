<div align="center">

# deepx-code

**DeepSeek ネイティブ・OpenAI 互換のターミナル向けコーディングエージェント —— 単一バイナリ・キャッシュフレンドリー・コードグラフとローカル OCR を内蔵**

**DeepSeek・Xiaomi MiMo・Kimi・Qwen をプリセット、任意の OpenAI 互換モデルにも対応**

[![Go](https://img.shields.io/badge/built%20with-Go-00ADD8?logo=go&logoColor=white)](https://go.dev) [![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE) [![Release](https://img.shields.io/github/v/release/itmisx/deepx-code?color=success)](https://github.com/itmisx/deepx-code/releases) [![Stars](https://img.shields.io/github/stars/itmisx/deepx-code?style=flat)](https://github.com/itmisx/deepx-code/stargazers) ![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux%20%7C%20Windows-lightgrey)

[简体中文](README.md) · [English](README.en.md) · **日本語** · [한국어](README.ko.md)

![deepx-code demo](assets/demo.gif)

</div>

> [!TIP]
> **⚡ 長いセッションで実測 ~99% の prompt-cache ヒット**（実際のセッション：41,591 トークン中 41,472 がキャッシュ）。DeepSeek はキャッシュヒットの入力をキャッシュミスの数十分の一で課金するため（[公式料金](https://api-docs.deepseek.com/quick_start/pricing)）、長時間の実行でも繰り返しのコンテキストにほとんど課金されません。

---

## ✨ 特長

- **🦫 単一の Go バイナリ** —— Node / Python ランタイム不要、`curl` 一行でインストール、macOS / Linux / Windows 対応。
- **💰 キャッシュフレンドリーで長時間でも安い** —— DeepSeek のプレフィックスキャッシュを軸に設計（実測 ~99% ヒット）。ローカルキーワードルーティングは毎ターン、遅延ゼロ・トークンゼロで起動。
- **🧭 コードグラフ内蔵（codegraph）** —— シンボル単位の定義ジャンプ / 呼び出し元 / インターフェース実装 / 影響範囲。Go は `go/types` で正確に解析し、リポジトリ全体の grep を置き換えます。
- **👀 ローカル画像 OCR（PaddleOCR）** —— スクリーンショットの文字をオフラインで読み取り、マルチモーダル API は不要。
- **📎 `@` ファイル / ディレクトリ参照** —— 入力欄で `@` を打つとローカルのファジー検索パスピッカーが開く。選択すると `@パス` がメッセージに挿入され、モデルは必要に応じて Read（ファイル）/ List（ディレクトリ）を呼ぶ。コンテキストを精密に渡せて、全部詰め込まなくて済む。
- **🧠 デュアルモデル自動ルーティング** —— 軽い処理は flash、複雑なタスクは自動で pro に昇格。`/model flash|pro` でモデル固定、`/auto` `/plan` `/review` でモード切替も可能。
- **🗂️ 逐次 Todo + 並列 Plan DAG** —— 多段階タスクは可視チェックリストで一歩ずつ。独立した並列タスクは DAG に分解してサブエージェントを並列実行。
- **💾 ロスレスなセッション永続化** —— gob が `tool_calls` / ツール結果 / `reasoning_content` を完全保持し、再起動後もシームレスに継続。ウィンドウが埋まると自動で階層圧縮。
- **🔌 MCP + Skill エコシステム** —— MCP ネイティブ対応。Claude の skill ディレクトリと互換で、既存の skill をそのまま再利用。
- **🛡️ レビューモード** —— ファイル書き込み / Shell 実行はデフォルトで人間の確認を要求。
- **🧱 ネイティブ OS レベルサンドボックス** —— 既定の `native` は OS 隔離（macOS は Seatbelt、Linux は bubblewrap — 書き込みを workspace に限定 + プロセス隔離;OS 機構が無い環境はソフトポリシーのブラックリストにフォールバック）。`docker` コンテナ隔離や `off` も選択可。コンテナ無しでも agent に安全境界を引ける。
- **🎛️ 作業モード（working mode）** —— 1 コマンドで方法論を固定：`karpathy`（実用重視）/ `openspec`（仕様駆動）/ `superpowers`（全工程を厳格に）。3 モードは排他的で、1 つ選ぶと他 2 つの skill を無効化し方法論の混在を防ぐ。セッションに保存され、毎ターン履歴を汚さずプロンプトを注入。
- **⚡ 非対話 `exec` モード** —— `deepx exec "タスク"` は一度だけ実行して結果を stdout に直接出力。パイプで入力、出力をリダイレクト、スクリプト / CI / cron に組み込み可能で、**TUI に入る必要なし**(下記参照)。

## 📊 Claude Code との比較

|                     | **deepx-code**                          | Claude Code              |
| :------------------ | :-------------------------------------- | :----------------------- |
| 配布                | 単一 Go バイナリ、`curl` 一行            | Node（npm）              |
| オープンソース      | ✅ MIT                                  | ❌ クローズド            |
| モデル              | DeepSeek / Xiaomi MiMo（OpenAI 互換、設定時にプロバイダを選択、flash/pro 自動ルーティング） | Anthropic Claude   |
| コスト              | 長いセッションで ~99% キャッシュヒット   | サブスク / Claude API 従量 |
| コードグラフ内蔵    | ✅ codegraph（Go は `go/types` で正確）  | ❌（grep / 検索）        |
| ローカル・オフライン OCR | ✅ PaddleOCR                        | ❌（画像はクラウドのマルチモーダル） |
| MCP                 | ✅                                      | ✅                       |
| Skill エコシステム  | ✅（Claude の skill ディレクトリを再利用） | ✅                     |

> [!NOTE]
> これはモデルの品質そのものを比べるものではありません。deepx-code のトレードオフは **コスト・オープンソース・単一バイナリ・内蔵コードグラフ・オフライン OCR** です。

## 🚀 クイックスタート

**1. インストール**

macOS / Linux(末尾の `&& exec $SHELL` は現在のシェルを再起動し、PATH に deepx をすぐ反映させます。rc の source や新しいターミナルを開く必要はありません):

```bash
curl -fsSL https://raw.githubusercontent.com/itmisx/deepx-code/main/scripts/install.sh | bash && exec $SHELL
```

Windows(PowerShell):

```powershell
irm https://raw.githubusercontent.com/itmisx/deepx-code/main/scripts/install.ps1 | iex
```

🇨🇳 中国本土のユーザーは **Gitee ミラー** で高速にインストールできます(ソース・バイナリとも Gitee から取得。以降の `deepx upgrade` も Gitee 経由):

macOS / Linux:

```bash
curl -fsSL https://gitee.com/itmisx/deepx-code/raw/main/scripts/install.sh | SOURCE=gitee bash && exec $SHELL
```

Windows PowerShell

```powershell
$env:SOURCE='gitee'; irm https://gitee.com/itmisx/deepx-code/raw/main/scripts/install.ps1 | iex
```

`~/.local/bin/deepx` にインストールされます。`deepx upgrade` でいつでも更新可能。

**2. ターミナルでプロジェクトに入って起動**

deepx は**ターミナルプログラム**です。ターミナルを開き、プロジェクトに `cd` して `deepx` を実行すると対話 UI に入ります。

- どのターミナルでも OK:macOS の Terminal / iTerm2、Linux のターミナル、Windows Terminal / PowerShell。
- **VS Code 内蔵ターミナル**もおすすめ(`Terminal → New Terminal`、または `` Ctrl+` ``):開いているプロジェクトのディレクトリにいるので、`deepx` がそのプロジェクトに対して動き、編集はエディタに即座に反映されます。

```bash
cd <あなたのプロジェクト>   # VS Code の内蔵ターミナルなら通常すでにプロジェクト直下
deepx                       # 対話型 TUI に入る
```

**3. 設定**

| 項目          | 方法                                                         |
| :------------ | :----------------------------------------------------------- |
| プロバイダ & Key | 初回起動時のウィザードで：**←/→ でプロバイダ（DeepSeek / Xiaomi MiMo）を選び、対応する API Key を入力**し `~/.deepx/model.yaml` に保存。各プロバイダに flash/pro の既定モデルと 1M コンテキストを用意（DeepSeek `deepseek-v4-flash` / `-pro`、MiMo `mimo-v2.5` / `-pro`）。`/config` で再設定。 |
| 手動上書き    | `~/.deepx/model.yaml` を直接編集し、role（flash/pro）ごとに `base_url` / `model` / `api_key` / `max_tokens` / `context_window` を上書き可能。flash と pro で別プロバイダも指定できる。 |
| Skill         | `<ワークスペース>/.deepx/skills/` に配置、または `~/.claude/skills/` などを再利用。 |
| MCP           | TUI 内で `/mcp-add` で追加、`/mcp-list` で一覧。              |

## ⚡ 非対話実行（`deepx exec`）

フル TUI に入らず deepx をスクリプトに組み込みたいときは `deepx exec "<タスク>"` を使います。タスクを実行し、結果をそのままターミナル(stdout)に出力して終了します。結果のみ、途中の出力はありません。

```bash
deepx exec "README の機能リストを英語に翻訳して README.en.md に書き込む"
```

パイプ入力にも対応(`cat error.log | deepx exec "このエラーを分析して"`)。先に対話型 `deepx` で API キーを設定しておいてください。

## 🧠 仕組み

<details>
<summary><b>モデルルーティング（ローカル・遅延ゼロ・トークンゼロ）</b></summary>

メッセージが届くと、deepx はローカルでキーワード照合 + 長さ判定を行い、追加の LLM トークンを使わずに起動モデルを即座に決定します：

```
"リファクタ / refactor / architecture / デバッグ …" を含む → すぐ pro
長さ < 100 文字                                          → flash
長さ > 500 文字                                          → pro
```

中国語（簡体 / 繁体）/ 英語 / 日本語 / 韓国語に対応。ターンの途中でも、モデルは難しい推論のために `SwitchModel` で pro に昇格できます。

</details>

<details>
<summary><b>セッション永続化（gob バイナリ、ロスレス再開）</b></summary>

```
~/.deepx/sessions/<sha1(workspace)[:16]>/
├── meta.json          # ワークスペースのメタ情報
├── state.json         # 圧縮状態 + 使用量スナップショット
├── YYYY-MM-DD.jsonl   # テキストログ（Memory 検索用）
└── history.gob        # 完全なバイナリ履歴
```

| 形式               | 保存内容                                                                | 用途                          |
| :----------------- | :---------------------------------------------------------------------- | :---------------------------- |
| `history.gob`      | system + user + assistant（`tool_calls`・ツール結果・`reasoning_content` を含む） | **再起動からの復元、シームレス継続** |
| `YYYY-MM-DD.jsonl` | user / assistant のプレーンテキスト                                     | Memory ツールの検索           |

再起動時はまず gob を読み込み、失敗時は JSONL にフォールバック。アップグレードや skill 変更で system prompt が変わった場合、gob 復元時に現行版へ透過的に置換し、キャッシュのプレフィックスを安定させます。

</details>

<details>
<summary><b>セッション圧縮（階層 + サマリ統合）</b></summary>

コンテキストウィンドウの 70% を超えると自動で発動：末尾に約 20K トークンを階層的に残し、古い内容は LLM が一貫したサマリに圧縮して既存サマリと統合します。gob も更新されるため、再起動後も整合します。

</details>

<details>
<summary><b>タスク計画：Todo（逐次）vs Plan DAG（並列）</b></summary>

- **Todo** —— 多段階・逐次・文脈依存の作業（ゼロからのアプリ構築など）：モデルが可視チェックリストに手順を並べ、一つずつチェックしながら自分で実行し、進捗をリアルタイムに見せます。
- **CreatePlan（Plan DAG）** —— 本当に並列で独立したファンアウト：DAG に分解し、依存順に並列サブエージェントを起動。各ノードが flash / pro を選び、最後に集約します。

```
CreatePlan
  ├─ plan-1: Read  (flash) ─────┐
  ├─ plan-2: Read  (flash) ─────┤
  ├─ plan-3: Grep  (flash) ─────┤
  └─ plan-4: Write (pro)   ─────┘ depends_on: [1,2,3]
```

</details>

<details>
<summary><b>ローカル OCR（画像読み取りを補完）</b></summary>

画像を貼り付ける／パスを渡すと、LLM が `OCR` ツール（PaddleOCR PP-OCRv5）で文字を読み取ります。初回に OCR モデル（~37MB）と ONNX runtime をダウンロードし、以降は **オフラインで数秒** で応答。マルチモーダル API なしでも、エラーのスクリーンショットや UI モックをエージェントに「見せる」ことができます。

</details>

### 🧭 コードグラフ（codegraph）

シンボルグラフエンジンを内蔵し、リポジトリ全体の grep やファイルを一つずつ開く代わりに、モデルがシンボル単位のナビゲーション + 呼び出し関係クエリを直接実行できます。

<details>
<summary><b>op 早見表（12 個）</b></summary>

| op             | 用途                       | 必須                       | 説明                                            |
| :------------- | :------------------------- | :------------------------- | :---------------------------------------------- |
| `def`          | シンボルの定義位置          | `name`                    | 関数 / 型 / メソッド / 変数の定義箇所           |
| `refs`         | シンボルの使用箇所          | `name`                    | すべての参照（定義 + 呼び出し + 取得）          |
| `symbols`      | 名前で曖昧検索              | `name`(任意), `kind`(任意) | `kind`: func/method/type/var/const/field        |
| `outline`      | ファイル内のシンボル一覧    | `path`                    | ファイルアウトライン                            |
| `imports`      | ファイルの import 一覧      | `path`                    | 依存の概観                                      |
| `callers`      | 関数の呼び出し元            | `name`                    | **変更時の影響範囲**、Go の暗黙インターフェースも網羅 |
| `callees`      | 関数が呼び出すもの          | `name`                    | 内部フローの理解                                |
| `implementers` | インターフェースの実装者    | `name`                    | Go の暗黙インターフェースを **シンボル精度** で。grep では不可 |
| `subtypes`     | 型を継承 / 埋め込むもの     | `name`                    | サブタイプ追跡                                  |
| `supertypes`   | 型の派生元                  | `name`                    | スーパータイプ / 埋め込みインターフェース       |
| `impact`       | 変更が及ぼす下流            | `name`, `depth`(既定 3)   | 推移閉包、影響範囲分析                          |
| `reindex`      | インデックス強制再構築      | —                          | キャッシュ異常時の手動トリガー                  |

</details>

**対応言語**：Go（stdlib の正確な解析）+ TypeScript / JavaScript / Python / Java / Rust / C / C++ / C# / Ruby / PHP / Kotlin / Swift / Scala / Dart / Vue / Svelte。

**仕組み**：起動時にバックグラウンドの `Prewarm` がインデックスを構築（`loading → ready`）。Write/Update で変更されたファイルは `stale` となり次回クエリで増分再構築。結果は `ファイル:行`（シグネチャ / 呼び出し元付き）で表示しページングされます。

## 🧰 ツール

| 種類        | ツール                             |       plan | auto | review |
| :---------- | :--------------------------------- | ---------: | :--: | :----: |
| 読み取り専用 | `Read` `List` `Tree` `Glob` `Grep` |          ✓ |  ✓   |   ✓    |
| コードグラフ | `CodeGraph`                        |          ✓ |  ✓   |   ✓    |
| ファイル書込 | `Write` `Update`                   |          ✗ |  ✓   |   ⏳   |
| Shell       | `Bash`                             |          ✗ |  ✓   |   ⏳   |
| Web         | `Search` `Fetch`                   |          ✓ |  ✓   |   ✓    |
| メモリ      | `Memory`                           |          ✓ |  ✓   |   ✓    |
| Skill       | `LoadSkill`                        |          ✓ |  ✓   |   ✓    |
| 画像        | `OCR`                              |          ✓ |  ✓   |   ✓    |
| 計画        | `Todo` `CreatePlan`                | LLM が呼び出し |   |        |
| 昇格        | `SwitchModel`                      | LLM が呼び出し |   |        |

> ⏳ = 自動実行されるが人間の確認が必要。

## ⌨️ スラッシュコマンド

| コマンド                             | 動作                                |
| :----------------------------------- | :---------------------------------- |
| `/plan` `/auto` `/review`            | モード切替（読み取り専用 / 自動 / レビュー） |
| `/model`                             | モデル選択ポップアップ（auto=タスク振り分け / flash / pro 固定）；`/model flash` で直接指定も可 |
| `/reasoning`                         | `thinking` / `reasoning_effort` をロール毎（flash/pro）に設定するポップアップ；空 = 該当フィールドを送信しない（MiMo など非対応モデルに無影響） |
| `/compact`                           | セッションを手動圧縮                |
| `/new` `/sessions`                   | 新しい会話を開始 / 履歴一覧（↑↓ 選択、Enter で切替） |
| `/status`                            | 右側ステータス欄の表示/非表示（`Ctrl+B` でも可） |
| `/web-config`                        | web パネルのバインド IP とポートをポップアップで設定（「IP [ポート]」を空白区切りで入力;IP 空欄/`127.0.0.1`=ローカルのみ、`0.0.0.0`=LAN でスマホ/タブレットからアクセス可、ポート省略=ランダム）。保存すると再起動なしで即時反映され新しい URL を表示;設定はセッションの `meta.json` に保存、アクセストークンはセッションごとに固定で再起動後も不変。⚠️ このパネルはセッションを操作しコマンドを実行でき、かつ平文 HTTP — 外部公開は信頼できる LAN のみ |
| `/sandbox`                           | サンドボックス：`off`（無効）/ `native`（既定、OS 隔離：macOS は Seatbelt、Linux は bubblewrap — 書き込みを workspace に限定 + プロセス隔離;OS 機構が無い環境ではソフトポリシーのブラックリストにフォールバック)/ `docker`（コンテナ隔離、`/sandbox docker <image>`） |
| `/working-mode`                      | 作業モード（方法論）：`karpathy`（既定、実用重視）/ `openspec`（仕様駆動）/ `superpowers`（全工程を厳格に）；ポップアップで選択、または `/working-mode kp\|spec\|sp` で直接切替。3 モードは排他的で、1 つ選ぶと他 2 つの skill を無効化し方法論の混在を防ぐ。セッションに保存され、毎ターン履歴を汚さずプロンプトを注入 |
| `/lang`                              | UI 言語切替（中 / 英）              |
| `/mcp-list` `/mcp-add` `/mcp-delete` | MCP サーバー管理                    |
| `/skills` `/config` `/mode`          | skill 一覧 / key 再設定 / モード表示 |
| `/help`                              | ヘルプ                              |
| `/exit`                              | deepx を終了                        |

## 🛡️ レビューモード

| モード             | Write / Update / Bash | その他のツール | コマンド  |
| :----------------- | :-------------------- | :------------- | :-------- |
| `review`（既定）   | 人間が YES/NO 確認    | 自動実行       | `/review` |
| `auto`             | 自動実行              | 自動実行       | `/auto`   |
| `plan`             | 無効                  | 自動実行       | `/plan`   |

## 📦 Skill

```
ワークスペース  <wd>/.deepx/skills/
グローバル      ~/.agents/skills/ → ~/.claude/skills/ → ~/.deepx/skills/
```

- ワークスペース単位は `git add` でチームに共有可能
- グローバルは Claude Code 互換 —— 既存の skill をそのまま再利用

## 🏗️ アーキテクチャ

<details>
<summary><b>データフローを展開</b></summary>

```
1 ターン:
  ユーザー入力
    ↓
  RouteByKeyword (ローカル) ─► flash または pro
    ↓
  StartStream (メインループ)
    ├─ 直接回答
    ├─ ツール呼び出し → review が 書込/Shell をゲート → 実行 → 結果を戻す → 継続
    ├─ Todo → 可視チェックリスト(メインエージェントが一歩ずつ実行)
    ├─ SwitchModel → pro に昇格
    └─ CreatePlan → DAG scheduler → 並列サブエージェント → 集約

永続化:
  HistoryUpdateMsg → SaveGob (history.gob, 完全忠実)
  StreamDoneMsg    → Append JSONL (プレーンテキスト, Memory 検索)
  再起動           → LoadGob (優先) / JSONL (フォールバック)

圧縮:
  tokens ≥ ctxWindow × 70% → runCompression (非同期)
    → 末尾に ~20K トークンを保持 → LLM が新旧サマリを統合 → gob + state.json を更新
```

</details>

**ディレクトリ構成**

```
deepx/
├── main.go
├── agent/      StartStream ツールループ + ルーティング + DAG スケジューラ + サブエージェント
├── config/     ~/.deepx/model.yaml の読み書き
├── session/    gob 永続化 + JSONL ログ + 圧縮状態
├── tools/      全ツール実装（読み書き / 検索 / OCR / Memory / Skill / Plan / CodeGraph）
├── codegraph/  コードグラフ：定義 / 呼び出し / 継承実装 / 影響範囲
├── skill/      複数パスの skill 探索と読み込み
├── ocr/        PaddleOCR ラッパー（ONNX Runtime）
├── tui/        bubbletea TUI（入力 / 描画 / クリップボード / 選択 / ダッシュボード）
└── scripts/    インストールスクリプト
```

## 💰 トークン経済

- **ルーティングはトークンゼロ**：純粋にローカルキーワード、LLM 呼び出しなし
- **ツールを事前注入しない**：`Memory` / `LoadSkill` は呼び出し時のみ context に入る
- **system prompt は最小限**：ツール横断の規約 + workspace のみ。トリガー条件は各ツールの description に
- **DeepSeek の KV キャッシュに優しい**：tools 配列はモード / ロールで変わらず、system prompt は gob 復元時にバージョン認識
- **盲目的検索よりコードグラフ**：read / glob / grep のトークン浪費を根本から削減

## 🩹 アンインストール

```bash
# macOS / Linux
rm -f ~/.local/bin/deepx && rm -rf ~/.deepx

# Windows: %LOCALAPPDATA%\Programs\deepx と %USERPROFILE%\.deepx を削除
```

## ⭐ Star 推移

[![Star History Chart](https://api.star-history.com/svg?repos=itmisx/deepx-code&type=Date)](https://star-history.com/#itmisx/deepx-code&Date)

## 📄 License

[MIT](LICENSE) © 2026 itmisx
