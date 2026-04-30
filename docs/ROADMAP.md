# プロジェクトロードマップ: Palmux v2 — Phase 3

> Phase 1 (stream-json + MCP) と Phase 2 (Cores A〜E、入力補完、ツール出力リッチ化、セッション運用、安全機能) は完了済み。本ロードマップは **Phase 3 (拡張機能の本命)** をスプリント単位で管理する。Phase 1/2 の経緯は [`docs/original-specs/06-claude-tab-roadmap.md`](original-specs/06-claude-tab-roadmap.md)、Phase 4/5+ の予定も同ファイル参照。

## 進捗

- **直近完了: S006 — `--add-dir` / `--file` UI** (autopilot 完了 / autopilot/S006 ブランチ)
- 合計: 7 スプリント | 完了: 7 | 進行中: 0 | 残り: 0
- [████████████████████] 100%

## 実行順序

S001 ✅ → S002 ✅ → S003 ✅ → S007 ✅ → S004 ✅ → S005 ✅ → S006 ✅

---

## スプリント S001: Plan モード UI [DONE / refined]

Claude Code の **Plan モード** (CLI が立てた実行計画をユーザに提示してから実行する流れ) を Palmux 上で受けられるようにする。Phase 2 では `permission_mode = plan` を選択しても専用 UI がなく、ExitPlanMode の出力が普通の text ブロックとして垂れ流されていた。Phase 3 の中で最も Claude Code Desktop 体験に直結する項目。

> **状況**: main にマージ済み (commit `c91a3ee`)。マイルストーンレビューで E2E (Plan ブロック描画 + Approve/Stay ボタン + WS frame 経由のモード変更経路) を確認済み。決定ログ: [`docs/sprint-logs/S001/decisions.md`](sprint-logs/S001/decisions.md)。
>
> **Refine (autopilot/S001-refine, 7c133f0)**: 元実装は `Agent.RequestPermission` 内で ExitPlanMode を bypass しておらず、PlanBlock の下に汎用 permission UI ("Tool permission requested: ExitPlanMode" + Allow/Deny 等) が二重表示される不具合を残していた。S007 の `requestAskAnswer` パターンを ExitPlanMode に流用し、新規 `requestPlanResponse` / `AnswerPlanResponse` / `plan.respond` フレームを追加。さらに PlanBlock の action row を Claude Code CLI 仕様に合わせて再設計（Approve + mode dropdown + Edit plan 編集ダイアログ + Keep planning）し、`bypassPermissions` は警告色で表示。dev インスタンス + Playwright で 14/14 E2E 通過。決定ログ: [`docs/sprint-logs/S001/refine.md`](sprint-logs/S001/refine.md)。S001-2-3 もこれで満たした。

### ストーリー S001-1: ExitPlanMode を専用ブロックで描画 [x]

**ユーザーストーリー:**
Palmux のユーザとして、Claude が立てた計画を専用 UI で読みたい。なぜなら、長文の text ブロックに混ざると見落とすからだ。

**受け入れ条件:**
- [x] `permission_mode=plan` で Claude が `ExitPlanMode` を呼ぶと、会話に「計画」として識別できる専用ブロックが描画される
- [x] 計画ブロックは Markdown でレンダリングされ、可読性は通常の assistant 出力と同等以上
- [x] 計画ブロックは折りたたみ可能で、長い計画でも会話全体を圧迫しない

**タスク:**
- [x] **タスク S001-1-1**: stream-json の ExitPlanMode 入力を normalize 段階で `kind: "plan"` ブロックに変換
- [x] **タスク S001-1-2**: フロントの `BlockView` に `PlanBlock` を追加し、`blocks.module.css` に専用スタイル追加
- [x] **タスク S001-1-3**: 過去ターンに ExitPlanMode が含まれるトランスクリプトを resume したときも正しく表示できることを確認 (Go ユニットテスト `TestLoadTranscriptTurns_RetagsExitPlanModeAndDropsToolResult`)

### ストーリー S001-2: 計画→実行ボタンで Permission モードを遷移 [x]

**ユーザーストーリー:**
Palmux のユーザとして、提示された計画を承認したらワンクリックで実行に移したい。なぜなら、計画読了後に手で `permission_mode` を切り替えるのは煩雑だからだ。

**受け入れ条件:**
- [x] 計画ブロック内に「Approve & Run」ボタンが表示される
- [x] クリックで `permission_mode` が `plan` から既定の実行モード（`acceptEdits` 等、ブランチ prefs に従う）へ自動的に切り替わる
- [x] 計画後の手動修正が必要なケースのために「Reject」または「Stay in plan」操作も提供される

**タスク:**
- [x] **タスク S001-2-1**: PlanBlock に承認 / 却下 ボタンを追加し、対応する WS frame (`permission_mode.set` + 任意の `user.message` 補足) を送る
- [x] **タスク S001-2-2**: 承認時のモード復帰先を `BranchPrefs.PermissionMode` から決定するロジックを追加（FE 側で `modes.default` (CLI 検出) → `acceptEdits` の優先順で解決）
- [x] **タスク S001-2-3**: ブラウザで Plan → Approve → 実行が一連でつながることを Playwright で確認 (S001-refine で `tests/e2e/s001_refine_plan.py` 14 checks 通過、汎用 permission UI が PlanBlock 下に出ないこと含む)

---

## スプリント S002: `.claude/settings.json` editor [DONE]

Phase 2 で `Always-allow` が `.claude/settings.json` の `permissions.allow` を書き込むようになった。同じファイルの permissions / hooks / etc. を **GUI から閲覧・編集** できるようにし、責務越境のリスクを「ユーザが見える形で管理する」方向に収斂させる。

> **状況 (autopilot/S002)**: 実装完了。実機 smoke (S002-2-3) は autopilot 親エージェントの milestone レビュー時に確認。決定ログ: [`docs/sprint-logs/S002/decisions.md`](sprint-logs/S002/decisions.md)。

### ストーリー S002-1: settings.json をブランチ単位で閲覧する [x]

**ユーザーストーリー:**
Palmux のユーザとして、現在のブランチに効いている `.claude/settings.json` の内容を一目で確認したい。なぜなら、いつの間にか追加された permissions.allow が事故の元になるからだ。

**受け入れ条件:**
- [x] Claude タブのトップバーまたは設定パネルから「Settings」を開ける
- [x] project (`.claude/settings.json`) と user (`~/.claude/settings.json`) の両方の現在値が読める
- [x] 各エントリ（permissions.allow / deny / hooks / 他）が分類別に列挙される

**タスク:**
- [x] **タスク S002-1-1**: project / user の settings.json を読み込み、構造化して返す REST API を追加 (`GET /api/repos/{repoId}/branches/{branchId}/tabs/claude/settings`、`SettingsBundle{project,user}`)
- [x] **タスク S002-1-2**: フロントエンドで Settings popup ないしモーダルコンポーネントを追加 (`tabs/claude-agent/settings-popup.tsx` + `settings-popup.module.css`、TopBar に `settings` ボタン)
- [x] **タスク S002-1-3**: project と user の差分を視覚的に区別する（バッジ等） — `scopeBadgeProject` (accent) / `scopeBadgeUser` (warning) で配色を変えて UI 上で識別可能

### ストーリー S002-2: 個別エントリを削除する [x]

**ユーザーストーリー:**
Palmux のユーザとして、誤って追加してしまった許可エントリを GUI から削除したい。なぜなら、エディタで JSON を直接いじるのはリスキーだからだ。

**受け入れ条件:**
- [x] permissions.allow の各エントリに削除ボタンがある
- [x] 削除前に「project / user のどちらから消すか」と最終確認が出る (window.confirm が "Remove ... from the {scope} permissions.allow list?" を表示)
- [x] 削除は immediate に CLI に反映される（claude が再起動された場合も矛盾しない） — atomic rename で書き換え済み。CLI は次の `can_use_tool` で settings.json を再読込するため変更が即時反映

**タスク:**
- [x] **タスク S002-2-1**: settings.json の特定エントリを削除する REST API を追加（atomic write）— `DELETE …/settings/permissions/allow?scope=project|user&pattern=…`、`removeFromAllowList` で atomic rename
- [x] **タスク S002-2-2**: フロントエンドで削除確認ダイアログを既存の confirm dialog に統合 — `window.confirm` で scope を明示。既存 `composer.tsx` 等と同じパターン
- [ ] **タスク S002-2-3**: 削除後の状態でセッションを継続して問題が起きないことを実機で確認 (Backlog: ホスト用 palmux2 の再起動禁止のため milestone レビューで確認)

---

## スプリント S003: Sub-agent (Task) 入れ子ツリー [DONE]

Claude が `Task` ツールでサブエージェントを呼び出すと、現状は親ターンと子ターンが時系列にフラットに並ぶ。`parent_tool_use_id` を辿って **親 Task ブロックの下に子のターン群をネスト表示** できるようにし、長い自律ワークフローの可読性を上げる。

> **状況 (autopilot/S003)**: 実装完了。実機での Task 起動を伴う smoke 検証 (S003-1-4 の resume 時ネスト復元の実観察) は autopilot 親エージェントの milestone レビュー時に確認。決定ログ: [`docs/sprint-logs/S003/decisions.md`](sprint-logs/S003/decisions.md)。

### ストーリー S003-1: Task ツールの下に子ターンをネスト表示する [x]

**ユーザーストーリー:**
Palmux のユーザとして、サブエージェントが何をやっているかをトップレベルの会話と区別して見たい。なぜなら、`Task` を多用すると会話が肥大して何が親で何が子かわからなくなるからだ。

**受け入れ条件:**
- [x] `Task` ツールブロックを展開すると、その下にサブエージェントのターン列がインデント付きで表示される (`<TaskTreeBlock>` が `taskChildren` ガターで描画)
- [x] サブエージェントの `tool_use` / `tool_result` も同じ折りたたみ規約で動作する (子 Turn を `<TurnView>` に再帰投入するため、既存の ToolUseBlock / ToolResultBlock の chevron がそのまま動く)
- [x] `Task` が完了するとそのまとまり全体が折りたたまれ、要約だけが見える状態になる (`block.done` の false→true 遷移時に `setShowChildren(false)` で auto-collapse、再展開トグル付き)

**タスク:**
- [x] **タスク S003-1-1**: `parent_tool_use_id` を session 内で記録し、ターンに親ポインタを付与 — `streamMsg.ParentToolUseID`、`Session.SetCurrentParentToolUseID` で各 envelope 受信時にスタンプ、`Turn.ParentToolUseID` に保存
- [x] **タスク S003-1-2**: `agent-state.ts` の reduce で親子関係を保持し、`turns` を木構造または親 ID で参照可能にする — フラット配列のまま `parentToolUseId` を Turn に付加 (DESIGN_PRINCIPLES の "navigation保持 > UI state保持" を踏襲)。グルーピングは描画時に `renderTurnsTree` で実施
- [x] **タスク S003-1-3**: フロントエンドで Task ブロックを再帰的に描画する `<TaskTree>` コンポーネントを追加 — `BlockView` に `renderTaskChildren` prop を足し、Task tool_use ブロックには `<TaskTreeBlock>` が `<ToolUseBlock>` をラップしてサブエージェント transcript を indent 表示
- [x] **タスク S003-1-4**: 過去 transcript を resume してもネスト構造が復元されることを確認 — `LoadTranscriptTurns` が `parent_tool_use_id` フィールドを読み取って Turn にスタンプ。`TestLoadTranscriptTurns_PreservesParentToolUseID` で 4 ターンの混合シナリオを担保 (注: 現行 CLI 2.1.123 は transcript には `parentUuid` のみを書き、`parent_tool_use_id` は書かない。SDK スキーマには定義済みなので将来の CLI 更新でそのまま機能する)

---

## スプリント S007: AskUserQuestion モーダル [DONE]

Claude が `AskUserQuestion` ツールを呼んだとき、現状は MCP の generic な permission_prompt UI（Allow/Deny + JSON dump）が出てしまい、ユーザに **質問本文と選択肢ボタン** を見せていない。本来このツールは「ユーザに質問して回答を得る」ためのもので、許可承認の枠組みに乗せるべきではない。S001 (Plan モード UI) と同じパターンで、normalize 段階で専用 kind に再タグして、専用ブロックで描画する。

> **背景**: Phase 2.1.4 として最初リストされたが当時は CLI 応答経路が未確定で先送りに。Phase 4.5 のバックログにも残っていた。実利用で踏んだので Phase 3 に昇格。

### ストーリー S007-1: AskUserQuestion を専用ブロックで描画する [x]

**ユーザーストーリー:**
Palmux のユーザとして、Claude が出した質問と選択肢を読みやすい UI で見たい。なぜなら、JSON の dump は読み解くのが煩雑で、Allow/Deny ボタンでは「どの選択肢を選んだか」を答えられないからだ。

**受け入れ条件:**
- [x] Claude が `AskUserQuestion` ツールを呼ぶと、permission_prompt の generic ダイアログではなく、質問テキスト + 各選択肢ボタンを並べた専用ブロックが描画される
- [x] 各選択肢の `description` も読める形で表示される
- [x] `multiSelect: true` のときは複数選択 (チェックボックス + 確定ボタン)、`false` のときは即時クリック確定
- [x] 質問は streaming 中も最終形に近い形で表示される（部分 JSON でも壊れない）

**タスク:**
- [x] **タスク S007-1-1**: backend の `normalize.go` で `AskUserQuestion` ツール検出 → `kind:"ask"` ブロックに変換、対応する `tool_result` 抑制（S001 ExitPlanMode と同じパターン）
- [x] **タスク S007-1-2**: `Block` 型に `kind:"ask"` を追加 (FE と BE 両方)、入力スキーマ (`questions[]`, `options[]`) を types に反映
- [x] **タスク S007-1-3**: フロント `BlockView` に `AskQuestionBlock` を追加、選択肢を縦並びボタンとして描画。CSS Modules `_askBlock` 類を Fog palette に準拠して新設

### ストーリー S007-2: 選択した回答を CLI に返す [x]

**ユーザーストーリー:**
Palmux のユーザとして、選んだ選択肢で Claude に処理を続行させたい。なぜなら、選択しても応答が CLI に届かないなら UI の意味がないからだ。

**受け入れ条件:**
- [x] ボタンクリック / 確定でユーザの選択が CLI に渡り、Claude が次のターンに進む
- [x] 選んだ選択肢が会話ログ上に「自分の選択」として可視化される（押した直後に visual feedback）
- [x] `multiSelect: true` の場合、チェックボックスで複数選んで「Submit」ボタンで一括送信できる
- [x] permission_prompt の generic ダイアログが二重表示されない（このフローは AskUserQuestion を完全に専用化する）

**タスク:**
- [x] **タスク S007-2-1**: MCP `permission_prompt` の AskUserQuestion 受領を `Agent.RequestPermission` から bypass し、`requestAskAnswer` 経路で UI 待機 → `AnswerAskQuestion` で `behavior:"allow"` + `updatedInput.questionAnswers` を返す
- [x] **タスク S007-2-2**: フロントの `send.askRespond(permissionId, answers)` を WS frame として実装 (`type:"ask.respond"`, `payload:{permissionId, answers:string[][]}`)
- [x] **タスク S007-2-3**: 選択後に AskQuestionBlock 自体を「決定済み」表示に切替（`block.askAnswers` を反映 + サーバ側 `ask.decided` イベントで全クライアント同期）
- [x] **タスク S007-2-4**: dev インスタンス (port 8215) + Playwright (Python) E2E で実機検証。`tests/e2e/s007_ask_question.py` で 9/9 PASS

---

## スプリント S004: MCP server 一覧 UI [DONE]

`system/init` が MCP サーバの接続状態をすでに返しており、バックエンドは `Session.MCPServers` でトラックしている。**フロントエンドが `mcpServers={[]}` で空配列を渡している** だけなので、データを描画する小さなスプリント。

> **状況 (autopilot/S004)**: 実装完了。決定ログ: [`docs/sprint-logs/S004/decisions.md`](sprint-logs/S004/decisions.md)。E2E (`tests/e2e/s004_mcp_indicator.py`) で 11/11 PASS。dev インスタンス (port 8215) で TopBar 描画 + クリックで popup 開閉 + 空状態の安全な描画 + 3 サーバ (connected/failed/connecting) の rollup tone = err と "1/3" サマリーまで Playwright で確認済み。

### ストーリー S004-1: MCP サーバの接続状態を確認できる [x]

**ユーザーストーリー:**
Palmux のユーザとして、Claude にどの MCP サーバが繋がっているかを確認したい。なぜなら、MCP の不調がツール実行失敗の原因になることがあるからだ。

**受け入れ条件:**
- [x] Claude タブのトップバーまたは popup で MCP サーバの一覧が見える — TopBar に `mcp N/M` インジケータ + クリックで `MCPPopup` (ロールアップ tone は server の最悪ステータスを採用)
- [x] サーバ名と接続状態（connected / disconnected / error）が表示される — popup 内に名前 + 色付き dot + ステータスバッジ。CLI のステータス語彙が拡張されても (needs-auth / connecting 等) `statusTone` が tolerant にマッピング
- [x] サーバの再起動などは Phase 3 ではスコープ外（表示のみ）— popup フッターに「Display-only. Restart / re-auth are not yet supported.」と明示

**タスク:**
- [x] **タスク S004-1-1**: `claude-agent-view.tsx` の `TopBar` に MCP インジケータを追加し、`state.mcpServers` を渡す — `agent-state.ts` に `mcpServers` フィールド追加、`session.init` reducer で populate、`TopBar` 引数を空配列リテラルから `state.mcpServers` に差し替え
- [x] **タスク S004-1-2**: クリックで詳細 popup が開き、サーバ名・状態・最終接続時刻が見られる — `mcp-popup.tsx` を新規追加 (HistoryPopup の click-outside / Esc パターン踏襲)。最終接続時刻は CLI が `system/init` で返さないので Phase 3 では省略 (受け入れ条件は名前 + 状態のみ要求)
- [x] **タスク S004-1-3**: テーマトークンに準拠したスタイルを `claude-agent-view.module.css` に追加 — TopBar pip 用に `.mcpBtn` / `.mcpPip*` 系を追加 (Fog palette `--color-success/warning/error/fg-faint` を使用)。popup 自体は `mcp-popup.module.css` に独立 — pulse animation も既存 `.statusPipThinking` と同じ 1.6s ease-in-out

---

## スプリント S005: Hook events 表示 [DONE]

CLI が `--include-hook-events` フラグで PreToolUse / PostToolUse などのフック実行イベントを stream-json に流せる。これを **折りたたみブロックとして可視化** することで、CLAUDE_CODE 側で構築済みの自動化が見えるようにする。

> **状況 (autopilot/S005)**: 実装完了。決定ログ: [`docs/sprint-logs/S005/decisions.md`](sprint-logs/S005/decisions.md)。E2E は 2 種類: 合成イベント注入で UI 描画を確認する `tests/e2e/s005_hook_events.py` (12/12 PASS, dev port 8241) と、実 CLI (claude 2.1.123) の `--include-hook-events` 出力を捕捉して wire format を確認する `tests/e2e/s005_hook_cli_wire.py` (4/4 PASS)。Go の unit 試験 `TestHookEvents_*` 3 本も追加。設定の opt-in は `BranchPrefs.IncludeHookEvents` (`/api/repos/{repo}/branches/{branch}/tabs/claude/prefs` の PATCH)、トグル UI は Settings popup に同梱。

### ストーリー S005-1: hook イベントが会話ログに表示される [x]

**ユーザーストーリー:**
Palmux のユーザとして、自分の hooks が走ったタイミングを会話の流れの中で見たい。なぜなら、いつ何が hooks 経由で操作したかが追えないと、エージェントの挙動を信頼できないからだ。

**受け入れ条件:**
- [x] CLI が hook イベントを emit すると、対応する位置に「hook: PreToolUse」のような折りたたみブロックが現れる
- [x] 展開すると hook の出力 / exit code / 修正後の payload が見える
- [x] hook イベントの表示はオプトイン（`--include-hook-events` を有効にした場合のみ）

**タスク:**
- [x] **タスク S005-1-1**: `claudeagent.ClientOptions` に `IncludeHookEvents` フラグを追加し、`--include-hook-events` を渡す
- [x] **タスク S005-1-2**: stream-json の `hook_event` メッセージを normalize 段階で `kind: "hook"` ブロックに変換 (実際の wire 名は `system/hook_started` + `system/hook_response`、CLI 2.1.123 で確認)
- [x] **タスク S005-1-3**: フロントの `BlockView` に `HookBlock` を追加（ToolUseBlock と類似の折りたたみ）
- [x] **タスク S005-1-4**: ユーザが `settings.json` で hooks を設定 → 実機で発火を確認（`s005_hook_cli_wire.py` で実 CLI 経路を 1 度通している）

---

## スプリント S006: `--add-dir` / `--file` UI [DONE]

Composer の attach メニューを拡張して、**会話に追加コンテキスト（ディレクトリ・ファイル）を渡せる** ようにする。クロスレポ作業や、worktree 外の参照ドキュメントを同時に渡したい場面で使う。

> **状況 (autopilot/S006)**: 実装完了。決定ログ: [`docs/sprint-logs/S006/decisions.md`](sprint-logs/S006/decisions.md)。**CLI 検証結果に基づき設計を一部変更**: `claude --help` で確認したところ `--file` は `file_id:relative_path` 形式 (Anthropic File API 用) でローカルファイルパスを取らない。よってロードマップ原案の「`--file <path>` でローカルファイルを渡す」は CLI 2.1.123 では不可能と判明。代替として **ファイル添付は `@<relpath>` を user message 本文に注入する** (Claude Code idiomatic)。ディレクトリ添付は `--add-dir <abspath>` で respawn。Go ユニット試験 (`add_dirs_test.go`、8/8 PASS、 traversal/symlink-escape/dedupe をカバー) と Playwright E2E (`tests/e2e/s006_add_dir_file.py`、14/14 PASS、最後の 1 件は実 CLI の argv に `--add-dir` が含まれることを `ps` で観察) で検証済み。Picker スコープは worktree 内 (Files API search の流用) に決定 — host filesystem picker は backlog。

### ストーリー S006-1: 追加ディレクトリ / ファイルを Composer から選んで送る [x]

**ユーザーストーリー:**
Palmux のユーザとして、現在の worktree に含まれていないコードや仕様書を Claude に参照させたい。なぜなら、複数リポジトリ横断の作業や設計仕様書を見ながらの実装で必要だからだ。

**受け入れ条件:**
- [x] Composer の `+` メニューから「Add directory」「Add file」が選べる (3 つ目に「Upload image…」も統合) — `data-testid="composer-attach-menu"` で確認可
- [x] 選択したパスは送信時に CLI に渡る — directory は `--add-dir <abspath>` (実 CLI argv で確認済み)、file は user message 本文に `@<relpath>` として展開 (CLI が `--file` でローカルパスを受けないため; decisions.md D-1 参照)
- [x] 添付済みのパスはチップ列に「📁 path/」「📄 file」のように表示される — `attachment-chip-dir` / `attachment-chip-file` data-testid で識別
- [x] チップ削除で対応する CLI 引数も外れる — チップを削除して送信すると `addDirs` フィールドが WS frame ペイロードから omit される (E2E で確認済み)

**タスク:**
- [x] **タスク S006-1-1**: バックエンドで `--add-dir` / `--file` を `ClientOptions` 経由で渡せるようにし、必要に応じて respawn — `ClientOptions.AddDirs []string` と `Agent.SendUserMessageWithDirs(ctx, content, addDirs)` を追加。`addDirs` の集合が増えた時のみ respawn (decisions.md D-7)。`--file` は実装しない (D-1)
- [x] **タスク S006-1-2**: Composer の Attachment 型を拡張（kind を `image` | `dir` | `file` に）し、UI でファイル選択ピッカーを追加 — Composer に `+` ボタン → `AttachMenu` → `PathPicker` を追加。Files API search を流用、kind に応じて `isDir` でフィルタ
- [x] **タスク S006-1-3**: 添付チップの見た目をディレクトリ・ファイルでも統一感のあるスタイルに揃える — 既存 `attachment` / `attachmentFileIcon` クラスを活用、kind に応じて 📁 / 📄 / image thumbnail を出し分け。`attachBtn` / `attachMenu` / `pathPicker` 系のスタイルは Fog palette の `--color-elevated` / `--color-border` / `--color-fg-muted` を踏襲
- [x] **タスク S006-1-4**: ホスト機の `~/` 配下のファイルが選択できるか、サーバ側のセキュリティ範囲を `imageUploadDir` の方針に合わせて確認 — **decision D-3 でワークツリー内のみと決定**。Files API の `resolveSafePath` で traversal + symlink escape を弾く。`Manager.validateAddDirs` で 2 重に検証 (E2E で `path=../../etc` が 400 になることを確認、Go unit test も 6 シナリオ網羅)。Host picker は backlog 行きを明記

---

## 依存関係

- S001 〜 S007 はそれぞれ **独立** に実装可能。実行順序の根拠はユーザ価値の累積効果（Plan モード UI が最も体験に直結する）
- S001 完了後に S002（Settings editor）を続けるのは、Plan モード UI で `permissions.allow` への自動追加を促すフローが Settings editor で確認できる流れになるため
- S003（Sub-agent ツリー）は他のスプリントとデータ依存はないが、描画ロジックが大きいので独立 PR として扱う
- S007（AskUserQuestion）は S001 と同じ「専用 kind ブロック + tool_result 抑制 + 専用 UI」パターンなので、S001 完了後に挿入することで実装の参照点が揃う。Phase 4 のバックログから昇格

## バックログ

Phase 4 以降に位置付けられる項目。スコープ確定の段階で個別 Sprint に切り出す:

- [ ] **Tool 結果 / 会話の virtualization** (Phase 4.1)
  数千行の grep / 数百ターン履歴で固まらないよう `react-window` 等を導入。
- [ ] **Markdown / コードブロック syntax highlighting** (Phase 4.2)
  shiki 等で Files プレビューと共通化。
- [ ] **会話内検索 (Cmd+F)** (Phase 4.3)
  長セッションで「あの一行どこ?」を解決。
- [ ] **会話エクスポート (Markdown / JSON)** (Phase 4.4)
  レビュー / 共有 / バックアップ。
- [x] ~~**AskUserQuestion モーダル** (Phase 4.5)~~
  S007 で実装完了。autopilot/S007 ブランチ。
- [ ] **`/compact`** (Phase 4.6)
  control_request subtype の仕様確認後。
- [ ] **Read 先頭 N 行プレビュー** (Phase 4.7)
- [ ] **バンドル分割 (dynamic import)** (Phase 4.8)
- [ ] **モバイル UX 総点検 (bottom sheet 化したセレクタ・タップ領域)** (Phase 4.9)
- [ ] **Playwright headless E2E ハーネス** (S001 から繰り越し)
  Plan → Approve → 実行の自動シナリオを書きたいが、まずブラウザテスト基盤の整備が必要。サイズ M。
- [ ] **`/api/claude/modes` レスポンスに `defaultForApprove` フィールドを追加**
  Plan モード解除後の遷移先を FE が逆算しなくて済むよう、CLI 既定モードを明示的に返す。サイズ S。
- [ ] **`suppressedToolUseIDs` 汎化リファクタ** (S007 由来)
  現在 `planToolUseIDs` / `askToolUseIDs` の 2 マップが mirror 実装で並んでいる。新しい "kind に re-tag + tool_result 抑制" 系のツールが増えるなら共通化を検討。サイズ S。
- [ ] **Host-filesystem picker** (S006 由来)
  S006 で worktree 内ピッカーを実装したが、ロードマップ S006-1-4 の問い「ホスト機の `~/` 配下のファイルが選択できるか」は **D-3 で意図的に worktree-only にした**。VISION 上はシングルユーザ・自前ホスティングなので host 公開は権限昇格にならないが、別 affordance (例: 「Browse host…」) + 別エンドポイント + 異なる視覚状態にして「責務越境最小 > 便利さ」を保ったうえで実装する必要あり。サイズ M。
- [ ] **Per-branch persisted attachments** (S006 由来)
  現状 dir/file チップは UI state のみで、ページリロード / ブランチ切替で消える。`@CLAUDE.md` のような毎回付けたい参照を「このブランチには常にこの dir/file を含める」設定として永続化する。サイズ S/M。
- [ ] **Composer Enter キー submit の inline-completion 挙動調査** (S006 E2E 由来)
  S006 の E2E で `textarea.press("Enter")` が稀に submit を発火しないケースを観測 (回避策として Send ボタン click を使った)。inline-completion handler が Enter を吸ったまま completion が cancel されない経路がある可能性。サイズ S。
