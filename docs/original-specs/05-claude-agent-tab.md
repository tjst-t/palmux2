# 05. Claude Agent タブ — 設計書

> 既存の tmux + xterm.js ベースの Claude タブを置き換える、Claude Agent SDK 相当の双方向ストリーミング UI。新しいタブタイプ `claude-agent` として実装し、既存 `claude` タブと並行運用する。

## 1. 背景と目的

現状の Claude タブは tmux window 上で `claude` CLI の TUI を動かし、xterm.js で描画している。これには以下の制約がある:

- 再接続時に scrollback 全体を再描画するため遅い
- ターミナルのリサイズが Claude TUI の再レンダリングを引き起こす
- ストリーミング応答中にユーザーが入力できない(TUI が入力欄を制御しているため)
- ツール呼び出しやファイル編集の構造化情報が ANSI 文字列としてしか得られない
- モバイルで TUI を扱うのは小画面・ソフトキーボード上でつらい

これらは tmux PTY と TUI レンダリングの構造的制約であり、xterm.js 側の改修では解決しない。

新タブ `claude-agent` では Claude Code CLI を **stream-json モード**(`--input-format stream-json --output-format stream-json`)で起動し、JSON-lines の双方向プロトコルでバックエンドが対話する。フロントは Web ネイティブのチャット UI として再構築する。

## 2. アーキテクチャ概要

```
┌─────────────────────────────────────────────────┐
│ Browser (React)                                 │
│   ClaudeAgent タブコンポーネント                │
│   ├── StatusBar / Conversation / Composer       │
│   └── ws: /api/repos/.../tabs/claude-agent/agent│
└────────────┬────────────────────────────────────┘
             │ WebSocket (Palmux 独自イベント JSON)
┌────────────┴────────────────────────────────────┐
│ Palmux Server (Go)                              │
│   internal/tab/claude-agent/                    │
│   ├── provider.go     Provider interface 実装   │
│   ├── handler.go      WS ハンドラ               │
│   ├── session.go      ブランチごとの Agent 管理 │
│   ├── client.go       claude プロセスの spawn    │
│   ├── protocol.go     stream-json 型定義        │
│   ├── control.go      control_request 多重化   │
│   └── events.go       Palmux ↔ Frontend イベント│
└────────────┬────────────────────────────────────┘
             │ stdin/stdout (JSON-lines)
┌────────────┴────────────────────────────────────┐
│ claude CLI (subprocess)                         │
│   --input-format stream-json                    │
│   --output-format stream-json                   │
│   --include-partial-messages                    │
│   --verbose                                     │
│   --resume <session_id>?                        │
│   --setting-sources project,user                │
└─────────────────────────────────────────────────┘
```

### 言語選択と依存

Go から `claude` バイナリを直接 spawn する。Node.js / Python ランタイム依存は導入しない。stream-json プロトコルは Go で自前実装する。Palmux のシングルバイナリ配布の思想を維持する。

### プロセスのライフサイクル

| イベント | 動作 |
|---|---|
| ブランチ Open | プロセスは起動しない(lazy 起動) |
| 初回メッセージ送信 | `claude` を spawn。`session_id` が永続化されていれば `--resume` 付き、なければ新規 |
| アシスタント応答中 | プロセスは生存。ストリーミングを WS 経由で配信 |
| ユーザー切断(ブラウザクローズ等) | プロセスは生かしたまま。応答も継続 |
| ブラウザ再接続 | 既存プロセスのインメモリキャッシュから過去メッセージを返し、以降のストリームを WS で受信 |
| Palmux サーバー再起動 | 全プロセス kill。次回起動時に `session_id` から `--resume` で復元 |
| ブランチ Close | プロセス kill |

「ジョブを流しっぱなしでブラウザ再起動」を許容するのが要件。tmux ベース現行版と同等以上の継続性を提供する。

## 3. データソースの単位

### Single Source of Truth

会話履歴の SoT は **Claude Code CLI が `~/.claude/projects/<cwd-hash>/<session_id>.jsonl` に保存する transcript**。Palmux は転写を二重に持たない。

Palmux が独自に持つのはメタデータのみ:

- ブランチ ↔ アクティブ `session_id` のマッピング
- セッション一覧表示用のサマリ(タイトル、最終更新、ターン数、累計コスト等)

### 永続化ファイル

```
~/.config/palmux/sessions.json
```

```json
{
  "sessions": {
    "<session_id>": {
      "id": "<session_id>",
      "repoId": "tjst-t--palmux--a1b2",
      "branchId": "main--e5f6",
      "title": "Implement claude-agent tab",
      "model": "sonnet",
      "createdAt": "2026-04-27T10:00:00Z",
      "lastActivityAt": "2026-04-27T11:30:00Z",
      "turnCount": 12,
      "costUsd": 0.42,
      "parentSessionId": null
    }
  },
  "active": {
    "<repoId>/<branchId>": "<session_id>"
  }
}
```

`active` マップでブランチごとの「現在使う session_id」を管理。`/clear` 相当の操作はこのエントリを削除するだけ(transcript は CLI 側に残る)。

### インメモリキャッシュ

各 Agent プロセスに紐づくセッション状態をバックエンドのメモリ上に保持する:

- 受信済み Turn / Block 配列
- 進行中の partial message バッファ
- Pending の permission request
- 累計トークン・コスト

ブラウザ再接続時はこれを REST(`GET /api/sessions/:id`)で一括返却し、それ以降は WS で差分配信。

## 4. Stream-JSON プロトコル(CLI ↔ Palmux)

### 起動コマンド

```
claude \
  --input-format stream-json \
  --output-format stream-json \
  --include-partial-messages \
  --verbose \
  --setting-sources project,user \
  [--resume <session_id>] \
  [--model <model>] \
  [--permission-mode <mode>]
```

`cwd` はブランチの worktree パスに設定する(Palmux が spawn 時に指定)。

### 双方向 JSON-lines

stdin / stdout は改行区切りの JSON オブジェクト。各行は完全な JSON。

#### 送信(Palmux → CLI)

**ユーザーメッセージ:**

```json
{"type":"user","message":{"role":"user","content":"Hello"}}
```

**Control Request(同期 RPC、`request_id` で多重化):**

```json
{"type":"control_request","request_id":"req_1","request":{"subtype":"initialize"}}
{"type":"control_request","request_id":"req_2","request":{"subtype":"interrupt"}}
{"type":"control_request","request_id":"req_3","request":{"subtype":"set_model","model":"opus"}}
{"type":"control_request","request_id":"req_4","request":{"subtype":"set_permission_mode","mode":"plan"}}
```

**Permission レスポンス(CLI から `can_use_tool` 要求が来たときに返す):**

```json
{
  "type":"control_response",
  "request_id":"<CLI からのリクエスト ID>",
  "response":{
    "subtype":"can_use_tool",
    "behavior":"allow",
    "updatedInput": { "...": "編集後の input(任意)" }
  }
}
```

`behavior` は `allow` / `deny`。`deny` のときは `message` フィールドに理由文字列。

#### 受信(CLI → Palmux)

**Init(起動直後):**

```json
{"type":"system","subtype":"init","session_id":"<session_id>",...}
```

**Assistant メッセージ(完成):**

```json
{"type":"assistant","message":{"role":"assistant","content":[...]}}
```

**Partial(`--include-partial-messages` 有効時のストリーミング):**

```json
{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello"}}}
```

**Tool Result:**

```json
{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"...","content":"..."}]}}
```

**Result(ターン終了):**

```json
{"type":"result","subtype":"success","session_id":"...","total_cost_usd":0.0042,"usage":{...}}
```

**Control Request(CLI → Palmux、permission 要求等):**

```json
{
  "type":"control_request",
  "request_id":"<CLI 発行の ID>",
  "request":{
    "subtype":"can_use_tool",
    "tool_name":"Bash",
    "input":{"command":"rm -rf /tmp/foo"}
  }
}
```

これに Palmux は対応する `control_response` を返す。

### Go 側の型定義(`internal/tab/claude-agent/protocol.go`)

```go
type StreamMessage struct {
    Type    string          `json:"type"`
    Subtype string          `json:"subtype,omitempty"`
    Message json.RawMessage `json:"message,omitempty"`
    Event   json.RawMessage `json:"event,omitempty"`
    SessionID string        `json:"session_id,omitempty"`
    // control_request / control_response 用
    RequestID string          `json:"request_id,omitempty"`
    Request   json.RawMessage `json:"request,omitempty"`
    Response  json.RawMessage `json:"response,omitempty"`
    // result 用
    TotalCostUSD float64 `json:"total_cost_usd,omitempty"`
    Usage        json.RawMessage `json:"usage,omitempty"`
}
```

詳細な型は SDK のリリースに追従して拡張する。プロトコルバージョン互換性のため、未知のフィールドは保持して透過的に転送(panic しない)。

### Control Protocol の多重化

`request_id` ベースの request/response パターン。Go 側は `map[string]chan StreamMessage` で in-flight リクエストを管理する:

```go
type controlMultiplexer struct {
    mu       sync.Mutex
    pending  map[string]chan StreamMessage
    nextID   atomic.Uint64
}

func (m *controlMultiplexer) call(ctx context.Context, req any) (StreamMessage, error)
func (m *controlMultiplexer) handleResponse(msg StreamMessage)
```

CLI から来た control_request(permission 等)はバックエンドで処理 → control_response を返す。Palmux 発の control_request(interrupt 等)はレスポンスを待つ。

## 5. WebSocket プロトコル(Palmux ↔ Frontend)

### エンドポイント

```
WS /api/repos/{repoId}/branches/{branchId}/tabs/claude-agent/agent
```

既存ターミナル WS(`/attach`、バイナリ pty I/O)とは別。プロトコルが完全に違うので明示的に分ける。

### イベントの基本形

```json
{ "type": "<event_type>", "ts": "2026-04-27T...", ... }
```

### サーバー → クライアントイベント

| type | 説明 |
|---|---|
| `session.init` | 接続直後に1回。session_id, model, status, 既存 turn 配列のスナップショット |
| `turn.start` | 新しい assistant ターン開始 |
| `block.start` | ブロック開始(text / thinking / tool_use / todo) |
| `block.delta` | partial 更新(text_delta, thinking_delta, tool input delta 等) |
| `block.end` | ブロック完了 |
| `tool.result` | ツール実行結果 |
| `permission.request` | ツール承認要求(対応する `permission_id` を含む) |
| `turn.end` | ターン終了(usage, cost を含む) |
| `status.change` | idle / thinking / tool_running / awaiting_permission / error |
| `session.replaced` | `/clear` 等で session_id が変わった通知 |
| `error` | エラー通知(CLI クラッシュ、API エラー等) |

### クライアント → サーバーイベント

| type | 説明 |
|---|---|
| `user.message` | テキスト入力送信。content + attachments |
| `interrupt` | 進行中の応答を中断 |
| `permission.respond` | permission 承認/拒否(permission_id, decision, updated_input?) |
| `model.set` | モデル切替 |
| `permission_mode.set` | acceptEdits / default / plan / bypassPermissions |
| `session.clear` | 現在のセッションを離脱(新規 session_id でリセット) |

### バックエンドの責務

stream-json のメッセージは Palmux バックエンドで Palmux 独自イベントに正規化してからフロントへ流す。フロントが SDK 仕様変更の影響を受けないよう、変換層をサーバー側に置く。

## 6. パッケージ構成と Provider 実装

### ディレクトリ構成

```
internal/tab/claude-agent/
├── provider.go      Provider interface 実装
├── handler.go       WS ハンドラ
├── manager.go       ブランチごとの Agent ライフサイクル管理
├── session.go       1セッションの状態(turns, status, cache)
├── client.go        claude プロセスの spawn / stdin・stdout 管理
├── protocol.go      stream-json 型定義
├── control.go       control_request の request/response 多重化
├── events.go        Palmux WS イベント型定義
├── store.go         sessions.json の読み書き
└── normalize.go     stream-json → Palmux イベント変換
```

### Provider interface への組み込み

```go
type provider struct {
    manager *Manager
}

func (p *provider) Type() string             { return "claude-agent" }
func (p *provider) DisplayName() string      { return "Claude" }
func (p *provider) Protected() bool          { return true }
func (p *provider) Multiple() bool           { return false }
func (p *provider) NeedsTmuxWindow() bool    { return false }
func (p *provider) OnBranchOpen(ctx, repo, branch) error  { /* lazy: 何もしない */ }
func (p *provider) OnBranchClose(ctx, repo, branch) error { return p.manager.KillBranch(...) }
func (p *provider) RegisterRoutes(mux *http.ServeMux)     { /* WS と REST 登録 */ }
```

`NeedsTmuxWindow() = false` で tmux window を作らない第3カテゴリ(Files/Git と同じ)。

### 既存 `claude` タブとの並行運用

`cmd/palmux/main.go` で両方を Register:

```go
tabRegistry.Register(claude.New())          // 旧: tmux + TUI
tabRegistry.Register(claudeAgent.New())     // 新: stream-json + Web UI
```

設定 `settings.json` の `claudeTabImplementation: "tmux" | "agent"` でどちらをデフォルトにするか切替。デフォルトはしばらく `tmux`、安定後 `agent` に切替、最終的に旧版を deprecate。

URL スキームは旧 `/claude`、新 `/claude-agent` で衝突しない。

## 7. 認証

Palmux は Claude Code CLI の認証管理に **関与しない**。

### 起動時チェック

サーバー起動時に CLI の認証状態を確認:

1. `CLAUDE_CODE_OAUTH_TOKEN` 環境変数が存在するか
2. `ANTHROPIC_API_KEY` 環境変数が存在するか
3. `~/.claude/.credentials.json` が存在するか

いずれもなければフロントの Claude タブに認証セットアップガイドを表示:

```
Claude Code が認証されていません。サーバーマシンで以下のいずれかを実行してください:

  ローカル開発:    claude  (ブラウザでログイン)
  リモートサーバー: claude setup-token  → CLAUDE_CODE_OAUTH_TOKEN を環境変数に設定
  従量課金:        ANTHROPIC_API_KEY を環境変数に設定

設定後、Palmux を再起動してください。
```

セットアップ完了後の動作確認は、最初のメッセージ送信時に CLI の `init` イベントが正常に返るかで判定。失敗時はエラーメッセージを表示。

### なぜこの方針か

- OAuth フローを Web UI で代行するのは技術的に可能だが Anthropic 規約・セキュリティ両面で重い
- Palmux のターゲットユーザーは開発者であり、CLI 認証は障壁にならない
- Anthropic 側の OAuth 仕様変更に振り回されない

## 8. Permission モデル

### 採用するモード

Claude CLI の `--setting-sources project,user` で `.claude/settings.json` を読み込ませる。Palmux は CLI の設定に委譲する。

### Permission Decision

`canUseTool` 要求に対するフロントの選択肢:

| UI ラベル | 動作 |
|---|---|
| Allow | このリクエストのみ allow。CLI に `behavior: allow` を返す |
| Allow for this session | Palmux バックエンドで覚えて、同セッション内の同 tool + 同 input パターンを自動 allow |
| Edit | input を編集可能ダイアログで開く → 編集後の input で allow(`updatedInput` 経由) |
| Deny | `behavior: deny` を返す。理由を Composer 風の小欄で添える |

「Always allow(永続化)」は MVP には含めない。理由: Palmux が `.claude/settings.json` を勝手に書き換える責務越境を避けるため。ユーザーが恒久的な許可を望むなら、Claude Code TUI ないしエディタで `.claude/settings.json` を編集してもらう。

### Permission Mode セレクタ

StatusBar 寄りに **モードセレクタ**(セグメントコントロール)を置く:

| モード | 動作 |
|---|---|
| `default` | 個別承認モード。各ツール呼び出しに承認 UI |
| `acceptEdits` | ファイル編集系を自動承認(デフォルト) |
| `plan` | ツール実行を保留して計画のみ |
| `bypassPermissions` | 全自動(警告色で表示) |

セッション中に `set_permission_mode` control_request で動的に切替可能。

### `allowed_tools` / `disallowed_tools`

`.claude/settings.json` に書かれていれば CLI が自動で尊重。Palmux 側で UI を持たない。

## 9. UI レイアウトと挙動

ベースは「Palmux Claude タブ 構造仕様」の 4 ブロック構成 + Toolbar 非表示。本節では実装上重要な点と、構造仕様からの逸脱・補足のみ記述する。

### 全体構成

```
┌─────────────────────────────────────────┐
│ [1] StatusBar                           │
├─────────────────────────────────────────┤
│ [2] Conversation (flex:1)               │
├─────────────────────────────────────────┤
│ [3] StreamingIndicator (条件表示)       │
├─────────────────────────────────────────┤
│ [4] Composer                            │
└─────────────────────────────────────────┘
[overlay] SessionHistoryPopup
```

Claude Agent タブが active のときは v2 の下部 Toolbar を非表示(Composer が代替)。

### StatusBar

左から右へ:

- ステータス pip(idle / thinking / tool_running / awaiting_permission / error)
- モデルセレクタ
- Permission Mode セレクタ
- 状態テキスト("thinking…" 等)
- (spacer)
- コンテキスト使用率 + パーセント
- 累計コスト
- 履歴ボタン
- ⋯ メニュー(rename, export, delete, settings, `/clear`)

Fork ボタンは MVP には含めない。

### Conversation

- 自動スクロール追従。ユーザーが手動で上にスクロールしたら追従停止 → 「下に新規メッセージ」フローティングボタン表示
- Turn を時系列縦並び、user は右寄せバブル、assistant は左にアバター
- 長文 text は markdown レンダリング
- Block 種別は構造仕様の通り(Text / Thinking / ToolUse / Todo / Permission)

### ToolUseBlock

構造仕様の通り。具体的な展開時表示:

- `Edit` / `Write` → 差分表示。"Open in Files tab" ボタンで Files タブにジャンプ
- `Read` → 先頭 N 行プレビュー
- `Bash` → stdout/stderr を擬似ターミナル風(ANSI 色対応)、長い出力は折りたたみ
- `Grep` / `Glob` → 結果リスト、クリックで Files タブへ
- `Task` (subagent) → サブエージェントのメッセージを入れ子表示

実行中は spinner、完了で結果に置換。デフォルト折りたたみ、実行中は自動展開。

### TodoBlock

CLI の `TodoWrite` ツールに対応。同一ターン内で複数回更新される場合、同じブロックを **置換**(append しない)。

### PermissionBlock

会話末尾に表示。pending 中はストリーム停止状態。`y` / `n` でキーボード操作可(Composer フォーカス外)。

### Composer

- 上部: 添付チップ列(@mention 結果)
- 中央: textarea(IME 対応、Enter 送信、Shift+Enter 改行、⌘+Enter 送信)
- 下部ツールバー: 添付、`/` slash command、`@` mention、モデルセレクタ、送信ボタン

ストリーミング中は送信ボタンが中断ボタン(Esc)に切替。ユーザーは次ターンの先取り入力可能(完了後に手動送信、自動キューイングはしない)。

### Slash Commands

CLI の TUI スラッシュコマンドを Palmux 側で実装し直す。stdin に文字列として流すのではなく、対応する control_request か Palmux 内部処理に変換:

| コマンド | 実装 |
|---|---|
| `/clear` | Palmux 内部処理。`active` から session_id を削除、新規プロセス起動 |
| `/model` | `set_model` control_request |
| `/cost` | キャッシュ済み usage 情報を Conversation に表示 |
| `/status` | session_id, model, mode 等を表示 |
| `/help` | Palmux 内部処理。コマンド一覧表示 |

`/compact` `/init` `/resume` はパワーユーザー向けで MVP 後に検討。

### SessionHistoryPopup

構造仕様の通り。検索、ブランチフィルタ、resume、新規セッション。⌘H でトグル。

### モバイル UX

- ソフトキーボード表示時は Conversation の高さを自動調整(`visualViewport` API)
- ストリーミング中の自動スクロールは慣性スクロール中は抑制
- PermissionBlock の `y` / `n` ショートカットはモバイルでは大きな承認/拒否ボタンに置換
- Composer の slash / mention ポップアップはモバイルでフルハイト表示

## 10. キーボードショートカット

| キー | 動作 |
|---|---|
| ⌘↵ / Enter | 送信 |
| Shift+Enter | 改行 |
| Esc | 中断 / Permission Deny / 履歴閉じる |
| y / n | Permission Allow / Deny(Composer 非フォーカス時) |
| ⌘K | コマンドパレット |
| ⌘/ | Slash command |
| ⌘H | 履歴ポップアップ |
| ⌘N | 新規セッション(`/clear` 相当) |
| ↑↓ (履歴内) | 選択移動 |

⌘⇧F の Fork は MVP では未実装。

## 11. REST API

WS が会話のメインチャネル。REST は補助:

```
GET    /api/sessions?branch={branchId}&q=          履歴一覧
GET    /api/sessions/:sessionId                    セッション復元(snapshot)
DELETE /api/sessions/:sessionId                    削除(active から外す。transcript は CLI 側に残る)
PATCH  /api/sessions/:sessionId                    タイトル変更等のメタ更新
GET    /api/claude-agent/auth-status               認証チェック結果
```

メッセージ送信、interrupt、permission レスポンス等はすべて WS 経由。

## 12. Branch 切替時の挙動

- 別ブランチに切り替えてもプロセスは生かしたまま
- Drawer のブランチアイコンに状態 pip:
  - 緑: idle(完了済み)
  - 黄パルス: thinking / tool_running
  - 赤パルス: awaiting_permission(ユーザー操作待ち)
  - 灰: 未起動 / セッションなし
- ブランチに戻ると Conversation を再描画(キャッシュから即座に)、以降の差分は WS で受信

## 13. 既存タブとの連携

### Files タブ

- ToolUseBlock(`Edit` / `Write` / `Read`)に "Open in Files tab" ボタン
- クリックで Files タブを active 化、該当パスを開く。Edit の場合は diff ビューで開く

### Git タブ

- assistant が変更したファイルは Files タブの通常メカニズムで自動的に Working changes に現れる(Palmux 側で何もしない、git の自然な状態管理)
- Edit/Write は **stage しない**。stage はユーザーが Git タブで明示

### Bash タブ

統合しない。

理由: Claude Code の `Bash` ツールは CLI 内部で完結する非対話的 exec で、Palmux の Bash タブ(tmux 上の対話シェル)とは別世界。同一 PTY 共有は技術的に不可能。

将来的に「Claude が Bash ツールで実行したコマンド履歴を Bash タブの履歴ペインに参照表示する」程度の連携は可能だが MVP には含めない。

## 14. 設定

`settings.json` に `claudeAgent` セクションを追加:

```json
{
  "claudeAgent": {
    "defaultModel": "sonnet",
    "defaultPermissionMode": "acceptEdits",
    "showThinking": false,
    "thinkingCollapsedByDefault": true,
    "includePartialMessages": true,
    "claudeBinary": "claude",
    "extraArgs": []
  }
}
```

`allowedTools` / `disallowedTools` / プロジェクト固有の permission ルールは Palmux では持たず `.claude/settings.json` に委譲。

`claudeTabImplementation` トップレベル設定で `"tmux"` / `"agent"` を切替。

## 15. エラーハンドリング

### CLI プロセスのクラッシュ

- stdout EOF / 非ゼロ終了コードを検知 → status を `error` に
- stderr の最終 N 行をエラーメッセージとしてフロントへ
- 自動再起動はしない(無限ループ回避)。ユーザーが「再接続」ボタンで手動再起動

### Control Request タイムアウト

- 60秒以内にレスポンスが返らない場合は失敗扱い
- ゾンビプロセスを避けるため、初期化失敗時は確実に kill する(Issue #18666 対策)

### Stream JSON パースエラー

- 1行のパース失敗ではプロセスを kill しない(警告ログ)
- 連続 N 回失敗したらプロセス異常とみなして kill

### 認証エラー

- 起動直後の `init` エラーで認証関連メッセージを検知 → フロントに認証セットアップガイドを表示

### 無効な session_id での resume

- `--resume <invalid_id>` 時、CLI は stderr に "No conversation found with session ID" を出して終了する(Issue #387)
- これを検知したら `active` から該当エントリを削除、新規セッションとして再起動

## 16. テスト方針

### Go

- `internal/tab/claude-agent/` のユニットテスト
- `client.go` は CLI を実プロセスとして起動するテストと、stub プロセス(echo bot 的なもの)を使うテストの両方
- `protocol.go` / `normalize.go` はテーブル駆動テスト(JSON 入力 → 期待されるイベント出力)
- `control.go` は並行リクエストの多重化が壊れないことを確認

### TS

- Vitest で stores / lib のユニットテスト
- WS イベント受信 → UI 状態更新のテスト

### E2E

- 実 Claude API を叩くと金がかかるので、CI では stub CLI を用いる
- 手動確認: 認証フロー、permission 各種、interrupt、resume、ブラウザ再接続、ブランチ切替時のバックグラウンド継続

## 17. 実装フェーズ

### MVP(Phase 1)

これまでの議論の決定事項に対応する範囲:

- [ ] パッケージ骨格(`internal/tab/claude-agent/`)
- [ ] Provider 登録(既存 `claude` と並行)
- [ ] 認証チェック(起動時 + フロント表示)
- [ ] Stream-json プロトコル(基本: user / assistant / tool_use / tool_result / result)
- [ ] Control protocol(initialize / interrupt / set_model / set_permission_mode / can_use_tool)
- [ ] Lazy 起動 + resume + Palmux 再起動透過
- [ ] WS 双方向プロトコル + イベント正規化
- [ ] sessions.json の永続化
- [ ] StatusBar(model / mode / status / cost / context / history / menu)
- [ ] Conversation(text / thinking / tool_use / todo / permission の 5 種ブロック描画)
- [ ] Composer(IME / 添付 / slash / mention / 中断ボタン)
- [ ] StreamingIndicator
- [ ] PermissionBlock(allow_once / allow_session / edit / deny)
- [ ] partial messages のストリーミング表示
- [ ] SessionHistoryPopup(検索 / フィルタ / resume)
- [ ] `/clear` `/model` `/cost` `/status` `/help`
- [ ] Plan モード
- [ ] Branch 切替時のバックグラウンド継続 + Drawer 状態 pip
- [ ] Files タブ連携("Open in Files tab")
- [ ] モバイル UX 基本対応

### Phase 2(MVP 安定後)

CLI のドキュメント化されていない挙動の検証が必要なもの、責務越境の懸念があるもの:

- [ ] Fork セッション
- [ ] メッセージ書き換え(file checkpointing + rewind_to_user_message)
- [ ] `always` permission(`.claude/settings.json` 自動編集 or 設定 UI)
- [ ] `/compact`
- [ ] `/init`
- [ ] Bash タブとの履歴参照連携
- [ ] Thinking 表示の細かい設定 UI
- [ ] サブエージェント(`Task` ツール)の入れ子表示の最適化
- [ ] エクスポート(transcript を markdown / json で保存)

### Phase 3(運用フェーズ)

- [ ] 旧 `claude`(tmux 版)タブの deprecate アナウンス
- [ ] `claudeTabImplementation` のデフォルトを `agent` に
- [ ] 旧タブの削除

## 18. 注意事項

- stream-json プロトコルは公開されているが「公式安定 API」ではない。CLI のバージョンアップで変わる可能性がある。CI に CLI バージョン互換テストを組み込む
- Claude Code CLI のブランディング規約: Palmux は独自の UI / 名称を使う。Claude Code そっくりの UI は規約違反
- Anthropic 規約上、Palmux サブスクで動かす場合は他人への配布は推奨されない(自分用ローカルツールとしての利用が前提)
- transcript を Single Source of Truth とする以上、`~/.claude/projects/` のディスク使用量はユーザー側の管理事項。Palmux からの削除 UI は提供しない(誤削除のリスク)