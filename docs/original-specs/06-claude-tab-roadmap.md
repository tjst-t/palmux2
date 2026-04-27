# 06. Claude タブ機能拡張ロードマップ

> 仕様書 05 (`Claude Agent タブ — 設計書`) の Phase 1 (stream-json + MCP 最小実装) 完了後の続編。
> 「Claude Code Desktop と同等以上の体験」をゴールに、機能ギャップを段階的に埋める計画。
> **コア部分の汎用機構を先に整備してから Claude タブ側で利用する**方針。

## 1. 背景

Phase 1 で以下が稼働:

- stream-json 双方向 IPC (initialize / mcp_message / set_permission_mode / interrupt 等)
- in-process MCP サーバー経由の `permission_prompt` ダイアログ
- 5 種ブロック描画 (text / thinking / tool_use / tool_result / todo / permission)
- Lazy spawn・`--resume` による会話継続
- `claude --help` から権限モード自動検出
- Claude Code Desktop ライクなビジュアル (アバター無し・ツール 1 行サマリ折りたたみ・コンパクト Composer)

足りないものは大きく分けて 3 種:

1. **入力系の補完 UI** (slash / @-mention / 画像)
2. **ツール出力のリッチ化** (diff / preview / ANSI)
3. **複数ブランチ並行運用の可視化** (Drawer pip / Activity Inbox 統合 / 履歴 popup)

(3) はタブ単独では完結せず、コアの汎用機構を先に整備する必要がある。

## 2. 設計原則

| 原則 | 内容 |
|---|---|
| **タブ間の対称性** | 通知・状態 pip 等の機構は全タブが乗れる汎用基盤として作る。Claude 専用 API を生やさない。 |
| **CLI が真実** | 会話 transcript・skill / agent / command 一覧・hooks 設定はすべて CLI 側で管理。Palmux はミラー描画と入力中継に徹する。 |
| **起動時負荷ゼロ志向** | CLI spawn は「最初のメッセージ送信」契機の lazy。起動だけでは API クォータを消費しない。 |
| **責務越境の最小化** | `~/.claude/`・`.claude/settings.json` を Palmux が書き換える機能は明示の許可フローを通す。 |
| **公式 SDK 互換性** | `mcp_message` / `mcp_set_servers` / `permission_prompt` 等の制御プロトコルは SDK 実装と完全互換にする (将来の CLI 更新で壊れにくくするため)。 |
| **Phase 区切り** | 各 Phase 完了後に main 上で動く。半完成状態を残さない。 |

## 3. 機能サーベイ (現状 + 目標)

凡例: ✅ 実装済 / ⚠️ 部分的 / ❌ 未実装 / 🚫 削除済

### 3.1 対話・描画

| 機能 | 状態 | 備考 |
|---|---|---|
| テキストストリーム (partial) | ✅ |  |
| Thinking 展開 | ⚠️ | summary 1 行 + クリック展開。全文表示 toggle 詳細は未対応 |
| Tool use 折りたたみ 1 行 | ✅ |  |
| Tool result 折りたたみ + プレビュー | ✅ | 改善余地: ANSI 色, 大量行の virtualization |
| Plan モード ExitPlanMode 連携 | ❌ | 専用ブロック描画 + 計画→実行ボタン |
| Adaptive thinking 予算表示 | ❌ | `system/status` に進捗あり |
| サブエージェント (Task) 入れ子 | ❌ | `parent_tool_use_id` で親子関係取得可 |

### 3.2 入力

| 機能 | 状態 | 備考 |
|---|---|---|
| テキスト送信 (IME) | ✅ |  |
| Slash command popup + 補完 | ❌ | `initialize` レスポンスに commands 配列 |
| @-mention ファイル参照 | ❌ | `/api/repos/.../files/search` 流用可 |
| 画像ペースト / D&D | ❌ | `/api/upload` 既存 |
| 添付チップ列 | ❌ |  |
| 音声入力 | ❌ | spec 8 章で skip 確認済 |
| ユーザー発話編集・巻戻し | ❌ | `rewind_to_user_message` 制御 (要 SDK 確認) |

### 3.3 ツール出力リッチ化

| 機能 | 状態 | 備考 |
|---|---|---|
| Edit / Write 差分表示 | ❌ | git タブの diff コンポーネントを共有化 |
| Read 先頭 N 行プレビュー | ❌ |  |
| Bash 出力 ANSI 色 + 折りたたみ | ❌ | `ansi-to-html` 等で十分 |
| Grep / Glob 結果クリッカブル | ❌ |  |
| "Open in Files tab" deep-link | ❌ | spec 13 章 |

### 3.4 権限・安全

| 機能 | 状態 | 備考 |
|---|---|---|
| MCP `permission_prompt` ダイアログ | ✅ |  |
| Allow / Allow for session / Deny | ✅ |  |
| Permission mode 6 種自動検出 | ✅ |  |
| `y` / `n` キーボード | ✅ |  |
| Edit ダイアログ (input 編集 → updatedInput) | ❌ | spec 8 章で MVP 後 |
| Always-allow 永続化 (`.claude/settings.json`) | ❌ | 責務越境につき UI 経由で明示同意 |

### 3.5 セッション運用 (複数ブランチ並行)

| 機能 | 状態 | 備考 |
|---|---|---|
| `--resume` 透過再開 | ✅ |  |
| sessions.json 永続化 | ✅ |  |
| ブラウザ閉じてもバックグラウンド継続 | ✅ |  |
| Drawer のブランチ status pip | ❌ | **Core-A 必要** |
| Activity Inbox 統合 (権限要求 / エラーの集約) | ❌ | **Core-B 必要** |
| Session history popup (⌘H) | ❌ (REST のみ) | 任意 session_id への resume も必要 |
| Fork session | ❌ | `--fork-session` フラグ |
| `/compact` (圧縮) | ❌ | control_request subtype 要調査 |
| ユーザー発話 edit / 巻戻し | ❌ |  |
| Resume by PR (`--from-pr`) | ❌ | 後回し |

### 3.6 ステータス / 可視化

| 機能 | 状態 | 備考 |
|---|---|---|
| StatusBar の status pip | ✅ |  |
| 累計コスト | ✅ |  |
| コンテキスト % | ❌ | `result.usage.contextWindow` |
| MCP サーバー接続状態表示 | ❌ | `system.init.mcp_servers` |
| Rate limit 警告 | ❌ | イベントは受信中 |
| Streaming インジケータ | ✅ |  |

### 3.7 チューニング

| 機能 | 状態 | 備考 |
|---|---|---|
| モデル選択 (sonnet/opus/haiku) | ✅ | (現状ハードコード — `init.models` から動的取得改善予定) |
| Effort レベル (low/medium/high/xhigh/max) | ❌ | `--effort` で渡す |
| Output style (default/Explanatory/Learning) | ❌ |  |
| Adaptive thinking on/off | ❌ |  |
| Fast mode 切替 | ❌ |  |

### 3.8 拡張系

| 機能 | 状態 | 備考 |
|---|---|---|
| Hook events (`--include-hook-events`) | ❌ |  |
| Custom agent (`--agents`) | ❌ |  |
| Skill 直接呼出し UI | ❌ (slash パススルーは可) |  |
| Plugin 対応 | ❌ |  |
| `--add-dir` | ❌ |  |
| `--file` 添付 | ❌ |  |
| `--json-schema` 構造化出力 | ❌ |  |
| MCP OAuth フロー | ❌ |  |
| Workspace trust | ❌ | CLI が `--print` モードで skip するので暫定 OK |

### 3.9 既存資産マップ

実装済の Palmux コア機構で再利用できるもの:

| 機構 | 場所 | 想定再利用先 |
|---|---|---|
| Notify Hub (notification 集約 + Inbox 配信) | `internal/notify/`, `frontend/src/components/inbox/` | **Core-B** (権限要求の Inbox 化) |
| EventHub (`/api/events` WS broadcast) | `internal/store/events.go`, `frontend/src/hooks/use-event-stream.ts` | **Core-A** (ブランチ別 status pip) |
| Files API (search / read / grep) | `internal/tab/files/`, `/api/repos/.../files/*` | @-mention / Read プレビュー |
| Image upload | `/api/upload` + `imageUploadDir` 設定 | 画像ペースト |
| ⌘K Command Palette | `frontend/src/components/command-palette/` | inline slash popup の参考実装 |
| Confirm / Prompt dialog | `frontend/src/components/context-menu/` | AskUserQuestion モーダル |
| Git diff renderer | `frontend/src/tabs/git/git-diff.tsx` | **Core-C** (Edit/Write 差分) |
| Sessions REST | `/api/sessions/*` | Session history popup |

## 4. コアレベル変更が必要な機能 (= "Core-x")

複数のタブ機能が依存する汎用基盤を**先に**整備する。順序は依存関係に従う。

### Core-A: ブランチ別エージェント状態の汎用バス

#### 課題

Drawer のブランチ pip や Activity Inbox は **アクティブなタブ以外のブランチの状態**を参照する必要がある。
現状の Claude タブは独自の `Agent.subs` (per-WS) でしかイベントを撒いていない。

#### 設計

- `claudeagent.Manager` が status 変化・permission 要求・エラー・コスト更新を、
  既存の `*store.EventHub` (`Publish(eventType, repoID, branchID, payload)`) にも publish する。
  - 新しい event type:
    - `claude.status` (idle / thinking / tool_running / awaiting_permission / error / starting)
    - `claude.permission_request` (toolName, permissionId)
    - `claude.error` (message, detail)
    - `claude.turn_end` (cost, durationMs)
- フロントの `usePalmuxStore` に `agents: Record<"{repoId}/{branchId}", AgentBranchState>` を追加。
  - `useEventStream` フックが `claude.*` イベントを reduce して store を更新。
- Drawer / TabBar / Inbox は `usePalmuxStore` から各ブランチの状態を読む。
- 既存の per-WS broadcast は維持 (アクティブタブはそちら経由でリアルタイム deltas を受ける)。

#### 影響範囲

| ファイル | 変更内容 |
|---|---|
| `internal/store/events.go` | event type 定数追加 |
| `internal/tab/claudeagent/manager.go` | broadcast 時に EventHub にも publish するヘルパ追加 |
| `cmd/palmux/main.go` | `claudeagent.NewManager` に EventHub publisher を渡す |
| `frontend/src/stores/palmux-store.ts` | `agents` slice 追加 |
| `frontend/src/hooks/use-event-stream.ts` | `claude.*` を reducer に流す |
| `frontend/src/components/drawer.tsx` | ブランチ pip 描画 |

### Core-B: Claude イベント → Notify Hub 統合

#### 課題

ユーザー目線で「他ブランチで権限要求が出てる」を見逃さないには、既存の Activity Inbox に流す必要がある。
現状の Notify Hub は外部 POST (`/api/notify`) を入口にしているので、内部からの publish 経路を追加する。

#### 設計

- `notify.Hub` に `IngestInternal(repoID, branchID, n Notification)` メソッド追加 (resolver 不要の直接登録版)。
- `claudeagent.Manager` に `notify.Hub` 参照を持たせる。
- 以下のイベントが Notification に化ける:
  - **権限要求**: `Type="urgent"`, `Title="Tool permission needed"`, `Message="Bash: ls /tmp/"`,
    `Actions=[{Label:"Allow (y)", Action:"allow"}, {Label:"Deny (n)", Action:"deny"}]`
  - **エラー (CLI exit 等)**: `Type="warning"`, `Message="Claude CLI exited"`
  - **長時間ターン完了** (>30s): `Type="info"`, `Message="Turn complete"` (オプション設定)
- Activity Inbox は既存のインライン y/n ボタン UI をそのまま使えるよう
  Notification の `Actions` 形式に合わせる。
- Inbox からの allow / deny クリック → Inbox UI が `permission.respond` 相当の WS frame を該当ブランチの WS 経由で送る。
  - **そのためには Inbox から Claude タブ非アクティブのまま WS を開ける必要がある** → 各ブランチに常時 1 本の "control WS" を張るか、軽量な REST `POST /api/repos/{repoId}/branches/{branchId}/tabs/claude/permission/{permissionId}` を新設するかの 2 案。
  - **採用案: REST**。WS を増やさず、Manager の `Agent.AnswerPermission` を REST で叩けるようにする。

#### 影響範囲

| ファイル | 変更内容 |
|---|---|
| `internal/notify/hub.go` | `IngestInternal` 追加 + `Notification.Actions` フィールド |
| `internal/tab/claudeagent/manager.go` | NotifyHub 参照 + イベント時に Ingest |
| `internal/tab/claudeagent/handler.go` | `POST .../permission/{permissionId}` REST 追加 |
| `internal/domain/entities.go` | `NotificationAction` 既存型を再利用 |
| `frontend/src/components/inbox/activity-inbox.tsx` | y/n ボタンが Claude permission 用 REST を叩く分岐 |
| `frontend/src/stores/palmux-store.ts` | (Core-A と一部重複) Notification reducer |

### Core-C: 差分レンダラの共通化

#### 課題

Edit / Write ツールの結果に diff を描画したいが、git タブにある `GitDiff` は API fetch を内包しており再利用しにくい。

#### 設計

- 純粋描画コンポーネント `frontend/src/components/diff/diff-view.tsx` を新設。
  - 入力: `{ filePath, hunks: DiffHunk[] }` (もしくは `oldText`/`newText` から自前計算)。
- `tabs/git/git-diff.tsx` をリファクタして `DiffView` を使うように。
- Claude タブは `tool_use_result` から `oldString`/`newString` (Edit) または `content` (Write) を取り出して `DiffView` に渡す。
  - Edit は SDK のツール仕様で `old_string`/`new_string` が input にある (input から再構成)。
  - Write は input の `content` をそのまま新規ファイル diff として描画。

#### 影響範囲

| ファイル | 変更内容 |
|---|---|
| `frontend/src/components/diff/diff-view.tsx` | 新規 (純粋関数コンポーネント) |
| `frontend/src/components/diff/types.ts` | DiffHunk 型を git タブから移設 |
| `frontend/src/tabs/git/git-diff.tsx` | DiffView を import するよう移行 |
| `frontend/src/tabs/git/types.ts` | DiffHunk re-export |
| `frontend/src/tabs/claude-agent/blocks.tsx` | tool_use の Edit/Write 描画分岐 |

### Core-D: 共有インライン popup プリミティブ

#### 課題

Slash command 補完と @-mention 補完は同じ「textarea からトリガ → 候補リスト → 選択挿入」パターン。
⌘K Command Palette は floating だが内部ロジック (検索フィルタ・キーボード操作) は流用可能。

#### 設計

- `frontend/src/components/inline-completion/` を新設。
  - `useInlineCompletion({ text, cursor, triggers, fetchOptions })` フックで:
    - トリガ文字 (`/`, `@`) を検出 → クエリ抽出 → fetchOptions 実行 → 候補表示
    - ↑↓ で選択、Enter で挿入、Esc で閉じる
  - `<InlineCompletionPopup>` で位置決め + 描画。
- Composer はこのプリミティブに `triggers={[{ char: '/', source: slashCommands }, { char: '@', source: fileSearch }]}` を渡す。

#### 影響範囲

| ファイル | 変更内容 |
|---|---|
| `frontend/src/components/inline-completion/` (新規) | 共通フック + popup |
| `frontend/src/tabs/claude-agent/claude-agent-view.tsx` | Composer に組み込み |

### Core-E: タブ間ナビゲーションヘルパ

#### 課題

「Open in Files tab」のような cross-tab 遷移を毎回 URL 手組みするのを避けたい。

#### 設計

軽量。`frontend/src/lib/tab-nav.ts` に:

```ts
export function urlForFiles(repoId: string, branchId: string, path: string): string
export function urlForGit(repoId: string, branchId: string, view: 'diff'|'log'|...): string
```

を生やすだけ。

#### 影響範囲

最小。

### Core まとめ

| ID | 名前 | 規模 | 依存される機能 |
|---|---|---|---|
| Core-A | エージェント状態バス | M | Drawer pip, Inbox 統合, 履歴 popup の "そのブランチ走ってる?" 判定 |
| Core-B | Notify Hub 統合 | S-M | Inbox 経由の権限応答, 通知サマリ |
| Core-C | DiffView 共通化 | S | Edit/Write ツール結果描画 |
| Core-D | InlineCompletion 共通化 | S-M | Slash, @-mention |
| Core-E | tab-nav ヘルパ | XS | Open in Files |

## 5. Phase 別実装計画

### Phase 2.0 — コア基盤整備 (先行)

| # | タスク | 依存 |
|---|---|---|
| 2.0.1 | Core-C: DiffView 抽出 | なし |
| 2.0.2 | Core-D: InlineCompletion 抽出 | なし |
| 2.0.3 | Core-E: tab-nav ヘルパ | なし |
| 2.0.4 | Core-A: エージェント状態バス | なし |
| 2.0.5 | Core-B: Notify Hub 統合 | Core-A |

**完了条件**: コア機能としては動くが、UI 側でまだ使っていない状態でも OK (= 既存挙動を壊さない)。

### Phase 2.1 — 入力系の補完

| # | タスク | 依存 |
|---|---|---|
| 2.1.1 | Slash command popup (commands 配列を init から保持 → InlineCompletion で表示) | Core-D |
| 2.1.2 | @-mention ファイル補完 (Files API + InlineCompletion) | Core-D |
| 2.1.3 | 画像ペースト / D&D (`/api/upload` 経由) | なし |
| 2.1.4 | AskUserQuestion モーダル | なし (既存 dialog 流用) |

### Phase 2.2 — ツール出力リッチ化

| # | タスク | 依存 |
|---|---|---|
| 2.2.1 | Edit / Write 差分描画 | Core-C |
| 2.2.2 | Read 先頭 N 行プレビュー | なし |
| 2.2.3 | Bash 出力 ANSI 色 + 折りたたみ | なし (`ansi-to-html` 追加) |
| 2.2.4 | Grep / Glob 結果クリッカブル | Core-E |
| 2.2.5 | "Open in Files tab" deep-link | Core-E |

### Phase 2.3 — セッション運用

| # | タスク | 依存 |
|---|---|---|
| 2.3.1 | Drawer のブランチ status pip | Core-A |
| 2.3.2 | Activity Inbox 統合 (権限要求 inline allow/deny) | Core-A + Core-B |
| 2.3.3 | Session history popup (⌘H) + 任意 session_id resume | (REST 拡張) |
| 2.3.4 | コンテキスト % 表示 | なし |
| 2.3.5 | MCP サーバー一覧表示 | なし |

### Phase 2.4 — チューニング

| # | タスク | 依存 |
|---|---|---|
| 2.4.1 | Effort セレクタ (`--effort` で respawn) | なし |
| 2.4.2 | Output style セレクタ | なし |
| 2.4.3 | `/compact` | (control_request 仕様確認) |
| 2.4.4 | `/cost` `/usage` `/status` 等の slash 内蔵 | なし |
| 2.4.5 | モデル一覧を init.models から動的取得 | なし |

### Phase 2.5 — 安全寄り機能

| # | タスク | 依存 |
|---|---|---|
| 2.5.1 | 永続 always-allow (`.claude/settings.json` 書込) + 同意トースト | (settings IO) |
| 2.5.2 | Permission Edit ダイアログ (input 編集 → updatedInput) | なし |
| 2.5.3 | ユーザー発話 edit / 巻戻し | (CLI subtype 要調査) |
| 2.5.4 | Fork session (`--fork-session`) | なし |
| 2.5.5 | Plan モード UI (ExitPlanMode 連携) | なし |

### Phase 3 — 拡張系 (任意)

| # | タスク |
|---|---|
| 3.1 | サブエージェント (Task) 入れ子ツリー |
| 3.2 | Hook events 表示 |
| 3.3 | Custom agent / skill 呼出し UI |
| 3.4 | `--add-dir` / `--file` 添付 UI |
| 3.5 | Plugin 対応 |
| 3.6 | MCP OAuth フロー |
| 3.7 | Settings (`.claude/settings.json`) editor |
| 3.8 | `--json-schema` 構造化出力 |

## 6. 推奨着手順序

ユーザー価値の累積効果を最大化する順:

1. **Phase 2.0 全部** (コア基盤を先に整備) — UI 変化なしだが土台
2. **Phase 2.3.1 + 2.3.2** (Drawer pip + Inbox 統合) — 複数ブランチ並行運用が見えるようになる
3. **Phase 2.1.1 + 2.1.2 + 2.1.3** (slash / @-mention / 画像) — 入力体験が実用ラインに
4. **Phase 2.2.1 + 2.2.5** (diff + Open in Files) — エージェント出力が読みやすくなる
5. **Phase 2.5.2 + 2.5.1** (Permission Edit + always-allow) — 権限フローを実運用ラインに
6. **Phase 2.3.3** (Session history) + **2.5.4** (Fork) — セッション運用が完結
7. 残り (Phase 2.4 / Phase 2.5 残り / Phase 3) は需要次第

## 7. 主要リスク・未確定点

| リスク | 内容 | 対策 |
|---|---|---|
| **CLI 制御リクエスト仕様の不安定さ** | `/compact` `/fork` `rewind_to_user_message` の subtype と payload が SDK バイナリ string 解析でしか確認できない | 実機プロービング + バージョンピン (`claude --version` を起動時にログ) |
| **`mcp_set_servers` 重複登録** | `--mcp-config` と `initialize.sdkMcpServers` で同名サーバーを二重登録すると CLI が異常終了するケースあり (実装中に発見) | `sdkMcpServers` のみで登録する方針を維持 |
| **Always-allow の責務越境** | `.claude/settings.json` を Palmux が編集すると、CLI / 他ツールとの整合性が崩れる可能性 | UI で書込先 (project / user) を明示 + 確認ダイアログ |
| **大量ツール出力のレンダリング負荷** | `tool_result` が数千行になることがある (find / grep) | virtualization (react-window) または truncate + "show more" |
| **AskUserQuestion の応答経路** | 応答を tool_result として返すか user.message として返すか SDK 実装で要確認 | 実機テスト先行 |
| **Drawer pip の状態欠落** | Palmux サーバー再起動時、tab を一度も開かないと Agent が spawn されず status が "未起動" のまま | EventHub に "agent.shutdown" 含めて整合 |
| **Inbox からの権限応答** | アクティブなタブ以外で「Allow (y)」されたら → REST で answer → tab 開いた時に session.init で復元される (Core-B 設計通り) | 既存の `findPendingPermissionInTurns` が動く前提で OK |

## 8. 既存仕様書との関係

- **05-claude-agent-tab.md** が前提。本書はその MVP (Phase 1) を完了したあとの拡張計画。
- **04-ui-requirements.md** v2.1 の Activity Inbox / Drawer pip / ⌘K パレット との整合は本書 Core-A / Core-B / Phase 2.1.1 で取る。
- **02-CLAUDE-rules.md** の "spec precedence" に従い、本書と 04 で食い違いがあれば 04 を優先 (UI 詳細)。本書は実装順序の指針として上書きする。

## 9. 完了基準 (= "Phase 2 done")

以下が満たされた時点で Phase 2 完了とみなす:

- [ ] Drawer の各ブランチアイコンに idle / thinking / awaiting_permission の pip が表示される
- [ ] Activity Inbox に Claude の権限要求が並び、そこから allow/deny できる
- [ ] Composer で `/` `@` 補完が動く
- [ ] 画像をペーストして送信できる
- [ ] Edit / Write の結果が diff として表示される
- [ ] Read / Bash / Grep のツール結果がリッチに表示される
- [ ] Tool ブロックから Files タブへジャンプできる
- [ ] Session history popup から過去セッションを resume できる
- [ ] always-allow / Permission Edit ダイアログが動く
- [ ] Phase 1 で動いていたものが全て引き続き動く (回帰なし)
