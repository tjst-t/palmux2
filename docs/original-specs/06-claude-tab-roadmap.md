# 06. Claude タブ機能拡張ロードマップ

> 仕様書 05 (`Claude Agent タブ — 設計書`) の Phase 1 (stream-json + MCP 最小実装) 完了後の続編。
> 「Claude Code Desktop と同等以上の体験」をゴールに、機能ギャップを段階的に埋める計画。
> **コア部分の汎用機構を先に整備してから Claude タブ側で利用する**方針。
>
> **進行状況** (2026-04-29 時点): Phase 1 完了 → Phase 2 ほぼ完了（以下「§5 進捗サマリ」参照）→ 次は Phase 3 着手。

## 1. 背景

Phase 1 で以下が稼働:

- stream-json 双方向 IPC (initialize / mcp_message / set_permission_mode / interrupt 等)
- in-process MCP サーバー経由の `permission_prompt` ダイアログ
- 5 種ブロック描画 (text / thinking / tool_use / tool_result / todo / permission)
- Lazy spawn・`--resume` による会話継続
- `claude --help` から権限モード自動検出
- Claude Code Desktop ライクなビジュアル (アバター無し・ツール 1 行サマリ折りたたみ・コンパクト Composer)

Phase 2 で以下も追加:

- Core 5 機構（A〜E、§4）すべて実装済み
- 入力補完: slash / @-mention / 画像ペースト + チップ列
- ツール出力: Edit/Write 差分 / Bash ANSI / Grep clickable / Open in Files
- セッション運用: Drawer pip / Inbox 統合 / history popup / Context %
- チューニング: effort セレクタ / モデル動的取得
- 安全: always-allow + `.claude/settings.json` 書込 / Permission Edit / Fork

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
| Tool result 折りたたみ + プレビュー | ✅ | virtualization は **Phase 4** で対応予定 |
| Plan モード ExitPlanMode 連携 | ❌ | **Phase 3**: 専用ブロック描画 + 計画→実行ボタン |
| Adaptive thinking 予算表示 | ❌ | `system/status` に進捗あり |
| サブエージェント (Task) 入れ子 | ❌ | **Phase 3**: `parent_tool_use_id` で親子関係取得可 |

### 3.2 入力

| 機能 | 状態 | 備考 |
|---|---|---|
| テキスト送信 (IME) | ✅ |  |
| Slash command popup + 補完 | ✅ | InlineCompletion + initialize.commands |
| @-mention ファイル参照 | ✅ | Files API + InlineCompletion |
| 画像ペースト / D&D | ✅ | `/api/upload` + チップ列 + サムネイル |
| 添付チップ列 | ✅ | サムネイルプレビュー付き |
| 音声入力 | ❌ | spec 8 章で skip 確認済 |
| ユーザー発話編集・巻戻し | ❌ | `rewind_to_user_message` 仕様未確定。**保留**（CLI 仕様確定後） |
| AskUserQuestion モーダル | ❌ | **Phase 4**（CLI 応答経路の調査込み） |

### 3.3 ツール出力リッチ化

| 機能 | 状態 | 備考 |
|---|---|---|
| Edit / Write 差分表示 | ✅ | DiffView 共有化済み |
| Read 先頭 N 行プレビュー | ❌ | 現状は "Open in Files" のみ。**Phase 4** |
| Bash 出力 ANSI 色 + 折りたたみ | ✅ | `ansi-to-html` |
| Grep / Glob 結果クリッカブル | ✅ | pathList で行クリック → Files |
| "Open in Files tab" deep-link | ✅ | tab-nav.ts |

### 3.4 権限・安全

| 機能 | 状態 | 備考 |
|---|---|---|
| MCP `permission_prompt` ダイアログ | ✅ |  |
| Allow / Allow for session / Deny | ✅ |  |
| Permission mode 6 種自動検出 | ✅ |  |
| `y` / `n` キーボード | ✅ |  |
| Edit ダイアログ (input 編集 → updatedInput) | ✅ |  |
| Always-allow 永続化 (`.claude/settings.json`) | ✅ | 書込済み + 同意フロー |

### 3.5 セッション運用 (複数ブランチ並行)

| 機能 | 状態 | 備考 |
|---|---|---|
| `--resume` 透過再開 | ✅ |  |
| sessions.json 永続化 | ✅ |  |
| ブラウザ閉じてもバックグラウンド継続 | ✅ |  |
| Drawer のブランチ status pip | ✅ | Core-A 経由 |
| Activity Inbox 統合 (権限要求 / エラーの集約) | ✅ | Core-B 経由 |
| Session history popup (⌘H) | ✅ | 任意 session_id resume 込み |
| Fork session | ✅ | `--fork-session` |
| `/compact` (圧縮) | ❌ | **保留**（control_request subtype 仕様未確定） |
| ユーザー発話 edit / 巻戻し | ❌ | **保留**（同上） |
| Resume by PR (`--from-pr`) | ❌ | 後回し |

### 3.6 ステータス / 可視化

| 機能 | 状態 | 備考 |
|---|---|---|
| StatusBar の status pip | ✅ |  |
| 累計コスト | ✅ |  |
| コンテキスト % | ✅ | `result.usage.contextWindow` |
| MCP サーバー接続状態表示 | ⚠️ | バックエンドはデータあり、フロント `mcpServers={[]}` 空。**Phase 3** |
| Rate limit 警告 | ❌ | イベントは受信中 |
| Streaming インジケータ | ✅ |  |

### 3.7 チューニング

| 機能 | 状態 | 備考 |
|---|---|---|
| モデル選択 (sonnet/opus/haiku) | ✅ | `init.models` から動的取得 |
| Effort レベル (low/medium/high/xhigh/max) | ✅ | `--effort` (respawn 経由) |
| Output style (default/Explanatory/Learning) | ❌ | バックエンドはトラック済み、UI 未。**Phase 4 候補**（CLI 応答経路と合わせて） |
| Adaptive thinking on/off | ❌ |  |
| Fast mode 切替 | ❌ |  |

### 3.8 拡張系

| 機能 | 状態 | 備考 |
|---|---|---|
| Hook events (`--include-hook-events`) | ❌ | **Phase 3** |
| Custom agent (`--agents`) | ❌ | **保留**（slash パススルーで実用上問題なし） |
| Skill 直接呼出し UI | ❌ (slash パススルーは可) |  |
| Plugin 対応 | ❌ | **保留**（Claude Code 側の plugin システム待ち） |
| `--add-dir` | ❌ | **Phase 3** |
| `--file` 添付 | ❌ | **Phase 3** |
| `--json-schema` 構造化出力 | ❌ | **保留**（niche） |
| MCP OAuth フロー | ❌ | **保留**（需要待ち） |
| `.claude/settings.json` editor | ❌ | **Phase 3**（always-allow と整合） |
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

> **進捗**: Core-A〜Core-E すべて Phase 2 で実装完了。以下は当時の設計メモを残してある（参照用）。

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

### Phase 2 — 進捗サマリ (ほぼ完了)

| Phase | 内容 | 状態 |
|---|---|---|
| 2.0 | Core 基盤 (A〜E) | ✅ 全項目完了 |
| 2.1 | 入力補完 | ✅ slash / @-mention / 画像ペースト・チップ列 完了。AskUserQuestion は **Phase 4 へ延期**（CLI 応答経路の調査待ち） |
| 2.2 | ツール出力リッチ化 | ✅ Edit/Write diff / Bash ANSI / Grep clickable / Open in Files 完了。Read プレビュー（先頭 N 行）は **Phase 4 へ** |
| 2.3 | セッション運用 | ✅ Drawer pip / Inbox 統合 / history popup / Context % 完了。MCP サーバ一覧 UI は **Phase 3 へ**（バックエンド済み・描画のみ残） |
| 2.4 | チューニング | ✅ effort / models 動的取得 完了。Output style セレクタ / `/compact` は **Phase 4 候補**（CLI 仕様確認込み） |
| 2.5 | 安全寄り | ✅ always-allow / Permission Edit / Fork 完了。Plan モード UI は **Phase 3 へ**。ユーザ発話 edit / rewind は **保留**（CLI subtype 未確定） |

> Phase 2.0〜2.5 のうち UI 完成度が確認できたものは ✅。残課題は Phase 3 / Phase 4 へ振り直す。

### Phase 3 — 拡張機能の本命

Phase 2 取りこぼし + ロードマップ §3.8 の高価値項目を統合した中核 Phase。
**ユーザ価値が大きく、実装も妥当な範囲のもの** に絞る。

| # | タスク | 規模 | 由来 |
|---|---|---|---|
| 3.1 | **Plan モード UI** (ExitPlanMode ブロック描画 + 計画→実行ボタン) | M | 旧 2.5.5 |
| 3.2 | **`.claude/settings.json` editor** (permissions.allow / hooks の GUI 編集。既存 always-allow 書込と整合) | M | 旧 §3.8 |
| 3.3 | **サブエージェント (Task) 入れ子ツリー** (`parent_tool_use_id` で親子描画) | M | 旧 §3.1 |
| 3.4 | **MCP サーバ一覧 UI** (バックエンドのデータをステータスバーまたは popup で描画) | XS | 旧 2.3.5 |
| 3.5 | **Hook events 表示** (`--include-hook-events` + イベントタイプ追加 + 折りたたみブロック) | S-M | 旧 §3.8 |
| 3.6 | **`--add-dir` / `--file` UI** (Composer の attach メニュー拡張、クロスレポ作業向け) | M | 旧 §3.8 |

**着手順序の推奨**: 3.1 → 3.2 → 3.3 → 3.4 → 3.5 → 3.6 (上から、価値と独立性が高い順)。

### Phase 4 — 磨き込み

Phase 3 までで機能が揃った後の「長く使っても重くならない・読みやすい・探しやすい」磨き込み Phase。

| # | タスク | 規模 | 価値 |
|---|---|---|---|
| 4.1 | Tool 結果 / 会話の virtualization (`react-window` 等) | M | 数千行 grep / 数百ターン履歴で固まらない |
| 4.2 | Markdown / コードブロック syntax highlighting (shiki 等) | S-M | 読みやすさ。Files プレビューと共通化 |
| 4.3 | 会話内検索 (Cmd+F でターン横断) | S | 長セッションで「あの一行」を見つける |
| 4.4 | 会話エクスポート (Markdown / JSON) | S | レビュー・共有・バックアップ |
| 4.5 | AskUserQuestion モーダル (旧 2.1.4) | S | CLI 応答経路の確認込み |
| 4.6 | `/compact` (旧 2.4.3) | S | 仕様確定後すぐ |
| 4.7 | Read 先頭 N 行プレビュー (旧 2.2.2) | XS | tool_use_result から軽く描画 |
| 4.8 | バンドル分割 (dynamic import で diff / xterm 切出し) | S | 初回ロード時間 |
| 4.9 | モバイル UX 総点検 (bottom sheet 化したセレクタ / タップ領域) | S | タッチ操作の決定打 |

### Phase 5+ — 需要次第

以下は **明示の需要が出てから** 着手する。スコープ確定はその時点で別ドキュメントに分離する。

- **共有 / 運用系**: OIDC / OAuth ログイン、マルチユーザ namespacing、観戦モード（read-only spectator）、操作監査ログ、ユーザ発話 edit / rewind、update notifier
- **Claude 以外への拡張**: `agent.Provider` 抽象化、Cursor agent / Aider などの取り込み、Custom agent UI（旧 §3.3）、Plugin 対応（旧 §3.5）、MCP OAuth（旧 §3.6）
- **ニッチ・実験**: `--json-schema` 構造化出力（旧 §3.8）、Files → Claude reverse direction、セッション差分（複数モデルの結果比較）、palmux v1 → v2 migration、i18n インフラ、PWA / ネイティブラッパ

> Phase 5+ はロードマップに残しておくが、実装計画には含めない。需要が出た時点で個別に Phase を切る。

## 6. 推奨着手順序

Phase 2 が完了している現状からの実行プラン:

1. **Phase 3.1** Plan モード UI — Claude Code Desktop の目玉機能。単独完結で価値が見えやすい
2. **Phase 3.2** `.claude/settings.json` editor — 既存 always-allow と整合。責務越境リスクの軽減策にもなる
3. **Phase 3.3** Sub-agent ツリー — 描画ロジックが大きいので独立コミット
4. **Phase 3.4** MCP server UI — 小タスクで仕上げ感
5. **Phase 3.5** Hook events — オプトインで低リスク
6. **Phase 3.6** `--add-dir` / `--file` — 最後
7. **Phase 4.1〜4.9** — Phase 3 完了後、運用してみて重さや読みづらさを感じた箇所から
8. **Phase 5+** — 需要観測後

## 7. 主要リスク・未確定点

Phase 2 の実装で多くは決着がついた。残るのは Phase 3 / Phase 4 で再度向き合う必要があるもの:

| リスク | 状態 | 対策 |
|---|---|---|
| **CLI 制御リクエスト仕様の不安定さ** (`/compact` / `rewind_to_user_message`) | 未確定 — Phase 4.6 / Phase 5+ 着手前に実機プロービング | バージョンピン (`claude --version` を起動時ログ)、subtype 確認できた段階で実装 |
| **AskUserQuestion の応答経路** | 未確定 — Phase 4.5 着手前に確認 | 実機テスト先行 → モーダル経由で応答するか tool_result で返すか決める |
| **大量ツール出力のレンダリング負荷** | Phase 2 では truncate / 折りたたみで凌いでいるが本格対応は Phase 4.1 | virtualization (`react-window`) で対応予定 |
| **Always-allow の責務越境** (`.claude/settings.json` 編集) | Phase 2 で書込済み・project スコープのみ | Phase 3.2 の Settings editor で UI 経由の明示同意フローに昇格 |
| **`mcp_set_servers` 重複登録** | Phase 1/2 で対処済み | `sdkMcpServers` のみで登録する方針を維持 |
| **Drawer pip の状態欠落** (palmux 再起動直後) | Phase 2 で部分対応 (initialise 後の status 整合) | サーバ再起動後、未起動 Agent は idle 表示になる挙動で許容 |
| **Inbox からの権限応答** | Phase 2 で動作確認済み | 既存の `findPendingPermissionInTurns` で session.init 復元が機能 |

## 8. 既存仕様書との関係

- **05-claude-agent-tab.md** が前提。本書はその MVP (Phase 1) を完了したあとの拡張計画。
- **04-ui-requirements.md** v2.1 の Activity Inbox / Drawer pip / ⌘K パレット との整合は本書 Core-A / Core-B / Phase 2.1.1 で取る。
- **02-CLAUDE-rules.md** の "spec precedence" に従い、本書と 04 で食い違いがあれば 04 を優先 (UI 詳細)。本書は実装順序の指針として上書きする。

## 9. 完了基準

### Phase 2 done (達成済み)

以下はすべて満たされている:

- [x] Drawer の各ブランチアイコンに idle / thinking / awaiting_permission の pip が表示される
- [x] Activity Inbox に Claude の権限要求が並び、そこから allow/deny できる
- [x] Composer で `/` `@` 補完が動く
- [x] 画像をペーストして送信できる（添付チップ + サムネイル）
- [x] Edit / Write の結果が diff として表示される
- [x] Bash (ANSI) / Grep のツール結果がリッチに表示される
- [x] Tool ブロックから Files タブへジャンプできる（worktree 相対化済み）
- [x] Session history popup から過去セッションを resume / fork できる
- [x] always-allow / Permission Edit ダイアログが動く
- [x] Phase 1 で動いていたものが全て引き続き動く

### Phase 3 done

以下が満たされた時点で Phase 3 完了:

- [ ] Plan モードに入ると ExitPlanMode を専用ブロックで描画し、ユーザの「Approve & Run」操作で実行に移れる
- [ ] `.claude/settings.json` の permissions.allow / hooks を GUI から編集 / 削除でき、書込先 (project / user) を明示できる
- [ ] Task ツール経由のサブエージェントが親ターンの下にネストして表示される
- [ ] MCP サーバ一覧と接続状態がステータスバーまたは popup で確認できる
- [ ] `--include-hook-events` を有効化し、PreToolUse / PostToolUse などが折りたたみブロックで可視化される
- [ ] Composer の attach メニューから `--add-dir` / `--file` を指定して送信できる

### Phase 4 done

以下が満たされた時点で Phase 4 完了:

- [ ] 数千行のツール出力 / 数百ターンの履歴でもスクロール / 描画が固まらない (virtualization)
- [ ] コードブロックが言語シンタックスでハイライトされる
- [ ] 会話内 Cmd+F 検索でターン横断ヒットが出る
- [ ] 会話を Markdown / JSON でエクスポートできる
- [ ] AskUserQuestion 応答経路が確定し、モーダル経由で応答できる
- [ ] `/compact` が動く（CLI 仕様確定後）
- [ ] Read ツールが先頭 N 行をインラインプレビューする
- [ ] 初回ロードのバンドルサイズが体感で改善（dynamic import）
- [ ] モバイルでセレクタが bottom sheet 化、主要タップ領域が 44px 以上
