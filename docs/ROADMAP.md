# プロジェクトロードマップ: Palmux v2 — Phase 3

> Phase 1 (stream-json + MCP) と Phase 2 (Cores A〜E、入力補完、ツール出力リッチ化、セッション運用、安全機能) は完了済み。本ロードマップは **Phase 3 (拡張機能の本命)** をスプリント単位で管理する。Phase 1/2 の経緯は [`docs/original-specs/06-claude-tab-roadmap.md`](original-specs/06-claude-tab-roadmap.md)、Phase 4/5+ の予定も同ファイル参照。

## 進捗

- **直近完了: S006 — `--add-dir` / `--file` UI** (autopilot 完了 / autopilot/S006 ブランチ)
- 合計: 14 スプリント | 完了: 7 | 進行中: 0 | 残り: 7
- [██████████░░░░░░░░░░] 50%

## 実行順序

S001 ✅ → S002 ✅ → S003 ✅ → S007 ✅ → S004 ✅ → S005 ✅ → S006 ✅ → **S008** → **S009** → **S010** → **S011** → **S012** → **S013** → **S014**

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

> **追補 (S008 で再設計)**: ユーザレビューの結果、サーバ側のディレクトリ/ファイル参照は **`@` autocomplete に集約** する方針となり、本 sprint で追加した「Add directory」「Add file」ピッカー UI (`AttachMenu` の dir/file 項目および `PathPicker` コンポーネント) は **S008 で削除** する。代わりに「ローカルデバイスからのファイルアップロード」(画像以外も含む) を Upload Image 経路の汎化として実装する。`--add-dir` の BE プラミングは **アップロード先ディレクトリの自動登録** に流用される。

---

## スプリント S008: 任意ファイルのアップロード添付 (Upload Image 拡張) [ ]

ユーザのデバイス (PC/スマホ/タブレット) にあるローカルファイルをアップロードして Claude Code に読ませる。S006 のサーバ側ピッカー UI は削除し、代わりに既存の **Upload Image 経路を画像以外のファイル全般に汎用化** する。VISION の「シングルユーザ・自前ホスティング前提」を踏襲し、ファイルは Anthropic File API ではなく palmux2 サーバ自身に保存する (`--file` フラグは使わない、S006 D-1 と同じ判断)。

**設計の中核**:
- アップロード先: `<attachmentUploadDir>/<repoId>/<branchId>/<sanitized-name>` (per-branch 隔離)
- CLI 起動時に `--add-dir <attachmentUploadDir>/<repoId>/<branchId>` を **常に追加** → 添付ごとの respawn 不要
- 送信時の振り分け: 画像 → 既存 `[image: <abspath>]` (vision 入力)、それ以外 → `@<abspath>` (Read される)
- アップロード経路: GUI ボタン / drag-and-drop / paste の 3 経路すべて

### ストーリー S008-1: ローカルファイルを 3 経路でアップロードして添付できる [ ]

**ユーザーストーリー:**
Palmux のユーザとして、自分のデバイスにあるファイル (テキスト / PDF / ログ / 画像 / 任意のドキュメント) を Claude に読ませたい。なぜなら、worktree に含まれていない参照資料を会話に持ち込みたいケースが頻繁にあり、サーバに事前配置するのは煩雑で、Anthropic File API への外部アップロードは privacy / quota / 認証経路の点で受け入れがたいからだ。

**受け入れ条件:**
- [ ] 既存の Upload Image 経路を画像以外のファイル種別にも拡張する (画像も同じ経路で動作継続)
- [ ] Composer の `+` ボタンから「Attach file」を選択するとローカルのファイル選択ダイアログが開き、任意のファイルを選べる
- [ ] Composer 領域へのドラッグ＆ドロップでも同じくアップロードされる (ファイル種別を問わない)
- [ ] クリップボードからのペーストでもアップロードされる (画像クリップボードは既存挙動を維持しつつ、ファイルクリップボード一般もサポート)
- [ ] アップロード成功後、Composer に添付チップが表示される (画像はサムネイル、それ以外は 📄 アイコン + ファイル名)
- [ ] 添付チップの × ボタンで添付を取り消せる
- [ ] 送信時に画像は `[image: <abspath>]`、それ以外は `@<abspath>` として user message 本文末尾に注入される (実 CLI で `Read` が走ることを確認)
- [ ] S006 で追加したサーバ側ピッカー UI (`+` メニューの "Add directory" / "Add file"、`PathPicker` コンポーネント) は完全に削除される
- [ ] アップロードファイルは per-branch ディレクトリに隔離され、TTL でクリーンアップされる

**タスク:**
- [ ] **タスク S008-1-1**: グローバル設定 `imageUploadDir` を `attachmentUploadDir` に汎化 (デフォルト `/tmp/palmux-uploads/`)。後方互換のため `imageUploadDir` キーが残っていれば `attachmentUploadDir` として読み込む
- [ ] **タスク S008-1-2**: `POST /api/upload` の MIME 制限を解除し任意ファイルを受け付ける。保存先を `<attachmentUploadDir>/<repoId>/<branchId>/<sanitized-name>` (per-branch 隔離) に変更。レスポンスに絶対パス + MIME / 元ファイル名を含める
- [ ] **タスク S008-1-3**: CLI 起動時の argv に `--add-dir <attachmentUploadDir>/<repoId>/<branchId>` を **常に含める** よう `Manager.EnsureClient` を修正。起動時固定で添付ごとの respawn 不要にする
- [ ] **タスク S008-1-4**: Composer の `+` メニューを「Attach file」1 項目に簡素化。S006 の "Add directory" / "Add file" / "Upload image…" の 3 項目を統合
- [ ] **タスク S008-1-5**: Composer に drag-and-drop ハンドラを追加。drop 領域は composer ルート、ファイル種別を問わず受け付ける。multi-file drop もサポート
- [ ] **タスク S008-1-6**: 既存の paste ハンドラ (画像のみ対応) を画像以外のファイル Blob にも対応させる。`event.clipboardData.files` のすべてを処理
- [ ] **タスク S008-1-7**: 添付チップの表示を MIME / 拡張子で分岐 (画像 → サムネイル、それ以外 → 📄 + ファイル名)。アップロード進行中の状態 (アップロード中 / 完了 / エラー) も視覚化
- [ ] **タスク S008-1-8**: 送信時の振り分けロジックを実装: `kind === 'image'` → `[image: <abspath>]`、それ以外 → `@<abspath>` を user message 末尾に注入
- [ ] **タスク S008-1-9**: S006 の `AttachMenu` の dir/file 項目、`PathPicker` コンポーネント、関連 CSS、WS frame の `addDirs[]` 受信経路 (`SendUserMessageWithDirs` を経由した user-supplied dirs) を削除。`Agent.AddDirs` フィールドと `validateAddDirs` 自体は upload dir の自動登録に流用するため残す
- [ ] **タスク S008-1-10**: TTL クリーンアップを実装 — 起動時に `<attachmentUploadDir>/<repoId>/<branchId>/` 配下の N 日以上古いファイルを削除 (デフォルト 30 日、設定で変更可能)。ブランチ close 時の per-branch dir 削除も検討
- [ ] **タスク S008-1-11**: dev インスタンス + Playwright で実機検証。`tests/e2e/s008_*.py` で (a) ファイル選択ダイアログ経由、(b) drag-and-drop、(c) paste の 3 経路を検証。`ps -ef | grep claude` で `--add-dir <attachmentUploadDir>/...` が argv に含まれることと、送信後の user message に `@<abspath>` (テキストファイル) / `[image: <abspath>]` (画像) が含まれることを確認

---

## スプリント S009: 複数インスタンス可タブの統一管理 UI (Claude / Bash) [ ]

VISION の中核「複数 Claude Code を並行運用する」は現状ブランチをまたいだ並走 (1 ブランチ = 1 Claude タブ) しか提供していない。同じブランチ内で複数の Claude タブを立ち上げられるようにし、**併せて Bash タブも同じ操作感**(`+` ボタン位置、右クリックで Close、上限のパラメータ化、auto-naming) に統一する。DESIGN_PRINCIPLES 第 2 条「タブ間の対称性 > Claude 専用 API」に基づき、TabBar の管理 UI を **タブタイプ非依存** に作り直す。

**実装上の前提 (確認済み)**:
- Claude タブ (`internal/tab/claudeagent/provider.go`): `Multiple() = false`、`NeedsTmuxWindow() = false`。tmux 不使用、`claude` CLI を subprocess として spawn し stdin/stdout の stream-json で通信、MCP は palmux2 in-process サーバ。本 sprint で `Multiple() = true` に切り替え、Manager の agent ownership を per-tab に refactor
- Bash タブ (`internal/tab/bash/provider.go`): 既に `Multiple() = true`、`NeedsTmuxWindow() = true`。TabBar には既に `+` ボタン (`frontend/src/components/tab-bar.tsx:127` の `onAddBash`) と削除 (confirm dialog 経由) が存在するが、上限なし、`+` 位置は TabBar 末尾、削除は context menu 経由ではない。本 sprint で UX を S009 共通基盤に統一
- 共通: `frontend/src/components/context-menu/` に `ContextMenu` コンポーネントが既に存在 (流用)

**設計の中核**:

| 観点 | 共通の設計 |
|---|---|
| `+` ボタン | tab provider の `Multiple() = true` のタブ群に対し、その種別の **一番右のタブの右側** に表示。Claude グループ末尾と Bash グループ末尾にそれぞれ独立した `+` |
| 右クリック / 長押し | 共通 `ContextMenu` で「Close」を表示。`MinInstances()` で宣言された下限に達しているグループでは Close 項目を出さない |
| min / max 設定 | `Provider` interface に `MinInstances() int / MaxInstances(settings) int` を追加。Claude=1/3、Bash=1/5 |
| Tab ID 体系 | 既存 Bash の `{type}:{name}` 規約 — Claude も同形式に統一 (1 番目 `claude:claude`、2 番目以降 `claude:claude-2`...) |
| URL 後方互換 | 既存 `/<repo>/<branch>/claude` は `claude:claude` の永続的 alias |
| ラベル生成 | tabId の suffix から index を読み「Claude」「Claude 2」「Claude 3」/「Bash」「Bash 2」「Bash 3」を auto-naming |
| Bash 命名 prompt 廃止 | 既存の `prompt: "New tab name"` フローは削除、auto-naming のみ (rename はバックログ) |
| Close 時の確認 | 進行中の assistant turn (Claude) / 下書きありの composer (Claude) / 実行中のシェルプロセス (Bash) のときのみ confirm dialog |

### ストーリー S009-1: 1 ブランチ内で複数 Claude / Bash タブを統一 UI で管理できる [ ]

**ユーザーストーリー:**
Palmux のユーザとして、Claude タブと Bash タブを同じ操作 (`+` で追加、右クリックで Close) で並列に立ち上げたい。なぜなら、リファクタしながら別 Claude で設計 Q&A をしたり、ビルド・サーバ・watcher を別 Bash で並走させたい場面が頻繁にあり、タブ種別ごとに違う UX を覚えずに並列タスクをこなしたいからだ。

**受け入れ条件:**

**共通 (Claude / Bash 両方に適用):**
- [ ] `Multiple() = true` のタブ種別ごとに、その種別の **一番右のタブの右側** に `+` ボタンが表示される (Claude グループ末尾と Bash グループ末尾にそれぞれ独立した `+`)
- [ ] `+` クリックで同種別の新タブが作成され自動でフォーカスする
- [ ] 上限到達時は `+` ボタンが disabled、tooltip で「上限 (N) に達しています」と表示
- [ ] 各タブで右クリック (モバイル: 長押し 500ms) するとコンテキストメニューが開く
- [ ] 同種別タブが下限 (デフォルト 1) に達しているときは Close 項目が出ない
- [ ] Close 時、進行中状態 (Claude: assistant turn / composer 下書き、Bash: 実行中プロセス) があれば confirm dialog が出る
- [ ] WS イベント `tab.added` / `tab.removed` で全クライアントが TabBar を同期
- [ ] モバイル (< 600px) で `+` ボタンと長押しメニューが破綻しない (TabBar overflow 時のスクロールも確認)

**Claude タブ固有:**
- [ ] Claude タブは最低 1 個必ず存在し、最大 `maxClaudeTabsPerBranch` (デフォルト 3) 個まで立ち上がる
- [ ] 各タブが独立した claude CLI subprocess を spawn する (`pgrep -f claude` で複数 PID を観測可能)
- [ ] 各タブが独立した stream-json 接続 / Session / MCP request 経路を持ち、片方の会話がもう片方に混入しない
- [ ] 2 番目以降の Claude タブは `--resume` なしで新セッションとして起動する (新しい session_id が発行される)
- [ ] タブラベルは「Claude」「Claude 2」「Claude 3」
- [ ] 閉じた Claude タブの session_id は **既存のセッション履歴 popup から resume 可能** (削除されず orphan で残る)
- [ ] URL ルーティング `/<repoId>/<branchId>/claude:claude-2` がブラウザの戻る/進むで遷移できる
- [ ] 既存 URL `/<repoId>/<branchId>/claude` は引き続き 1 番目の Claude タブを開く (後方互換)
- [ ] 通知 (Activity Inbox) には発火元の Claude タブ名 (例: 「Claude 2」) が含まれる

**Bash タブ固有:**
- [ ] Bash タブは最低 1 個必ず存在し、最大 `maxBashTabsPerBranch` (デフォルト 5) 個まで立ち上がる
- [ ] 既存の Bash 削除 UI (TabBar 末尾の confirm dialog 直開き経路) は完全に context menu 経由に置き換わる
- [ ] 既存の Bash 追加時の名前 prompt (`prompt: "New tab name"`) は廃止され、auto-naming「Bash」「Bash 2」「Bash 3」になる (rename はバックログ「Claude / Bash タブ rename」で対応)
- [ ] 各 Bash タブが独立した tmux window (`palmux:bash:bash`, `palmux:bash:bash-2`, ...) を持つ — 既存挙動の維持
- [ ] グローバル設定 `maxBashTabsPerBranch` を変更すると次回起動から反映される

**タスク:**

**共通基盤 (TabBar 汎用化 + Provider interface 拡張):**
- [ ] **タスク S009-1-1**: `internal/tab/provider.go` の `Provider` interface に `MinInstances() int` / `MaxInstances(settings *Settings) int` を追加。既存 5 Provider (claude, bash, files, git, + 将来追加分) すべてに実装。Files/Git は `Min=1, Max=1`、Bash は `Min=1, Max=settings.MaxBashTabsPerBranch`、Claude は `Min=1, Max=settings.MaxClaudeTabsPerBranch`
- [ ] **タスク S009-1-2**: グローバル設定に `maxClaudeTabsPerBranch` (デフォルト 3) と `maxBashTabsPerBranch` (デフォルト 5) を追加 — `internal/config/settings.go` (or 該当箇所)。`GET/PATCH /api/settings` で読み書き可能にする
- [ ] **タスク S009-1-3**: 既存 `POST /api/repos/{repoId}/branches/{branchId}/tabs` ハンドラに上限チェックを追加 (上限超過で 409 Conflict)。`DELETE /api/.../tabs/{tabId}` ハンドラに最低 1 個保護を追加 (1 個目を消そうとすると 409)
- [ ] **タスク S009-1-4**: フロントの `TabBar` (`frontend/src/components/tab-bar.tsx`) を **タブ種別グループ単位で描画** するよう refactor。各 `Multiple() = true` 種別の最後のタブの右側に `+` を配置 (現状の TabBar 末尾固定 `+` を廃止)。上限到達で disabled
- [ ] **タスク S009-1-5**: フロントの `ContextMenu` (`frontend/src/components/context-menu/`) を流用して、各タブで右クリック / 長押し (500ms) で「Close」項目を表示。下限到達のタブグループでは Close 項目を出さない。進行中状態あれば confirm dialog 経由
- [ ] **タスク S009-1-6**: タブラベル auto-naming ロジックを `frontend/src/lib/tab-registry.ts` (or 該当箇所) に追加。tabId が `{type}:{type}` のとき DisplayName そのまま、`{type}:{type}-N` のとき `${DisplayName} N` を生成
- [ ] **タスク S009-1-7**: WS イベント `tab.added` / `tab.removed` を全 `Multiple() = true` 種別で emit。FE が event を受けて TabBar を再描画

**Claude 固有:**
- [ ] **タスク S009-1-8**: `internal/tab/claudeagent/provider.go` の `Multiple()` を `true` に変更。`MinInstances() = 1` / `MaxInstances() = settings.MaxClaudeTabsPerBranch` を実装
- [ ] **タスク S009-1-9**: `internal/tab/claudeagent/manager.go` の `Manager.agents` map のキーを `(repoId, branchId)` から `(repoId, branchId, tabId)` に refactor。`Agent` のライフサイクルを per-tab に紐づける
- [ ] **タスク S009-1-10**: `internal/tab/claudeagent/store.go` (`SessionMeta` の永続化、`tmp/sessions.json`) を per-tab キー化。既存の `(repoId, branchId)` キーで保存されたデータは「1 番目タブ」(`claude:claude`) として読み込めるよう migration 互換層を入れる
- [ ] **タスク S009-1-11**: `internal/tab/claudeagent/mcp.go` の MCP server で複数 Agent / 複数タブを多重に扱えるか確認。`tool_use_id` ベースで一意性が取れていれば変更不要、足りなければ tab 識別子を request payload に含める拡張を行う
- [ ] **タスク S009-1-12**: `Agent.EnsureClient` で 2 番目以降のタブは `resumeID` を空にして spawn (新 session_id 発行)。1 番目タブは既存挙動 (`--resume` で復元) を維持
- [ ] **タスク S009-1-13**: URL ルーティング: React Router の path で `claude:claude-2` のようなコロン含む tabId を受け付ける。既存 URL `/<repo>/<branch>/claude` を `claude:claude` の alias として扱う後方互換層を `frontend/src/lib/tab-nav.ts` に追加
- [ ] **タスク S009-1-14**: Activity Inbox の notification metadata にタブ名 (例: "Claude 2") を含めるよう拡張。BE のイベント payload + FE 表示

**Bash 固有:**
- [ ] **タスク S009-1-15**: `internal/tab/bash/provider.go` に `MinInstances() = 1` / `MaxInstances() = settings.MaxBashTabsPerBranch` を実装
- [ ] **タスク S009-1-16**: 既存 `frontend/src/components/tab-bar.tsx:45-48` の `onAddBash` から `prompt: "New tab name"` 経路を削除し、サーバ側で auto-naming させる (現状クライアントが name を渡しているなら BE 側で `bash:bash-N` を採番)。`POST /api/.../tabs` の name パラメータを optional or 削除
- [ ] **タスク S009-1-17**: 既存の Bash 削除 UI (`tab-bar.tsx:220` の `confirmDialog → removeTab`) を共通 `ContextMenu` 経路に統合し、Bash 専用の削除ジェスチャを削除

**モバイル / E2E:**
- [ ] **タスク S009-1-18**: モバイル (< 600px) で `+` ボタンと長押しメニューが破綻しないことをデザイン調整 + 検証。タブが上限 + `+` ボタンで TabBar が overflow する場合のスクロール確認
- [ ] **タスク S009-1-19**: dev インスタンス + Playwright で実機検証。`tests/e2e/s009_*.py` で:
  - **Claude**: (a) `+` ボタンで 2nd タブ作成 → `pgrep -f claude` で 2 PID、(b) 各タブが独立した会話、(c) 右クリック → Close、(d) 上限到達で `+` disabled、(e) 1 個のときは Close 不可、(f) `maxClaudeTabsPerBranch=2` で上限変更、(g) 既存 URL `/<repo>/<branch>/claude` が 1 番目タブを開く
  - **Bash**: (h) `+` ボタンが Bash グループ末尾にある (TabBar 末尾固定ではない) こと、(i) `maxBashTabsPerBranch=3` で上限到達時 `+` disabled、(j) 右クリック → Close、(k) 1 個のときは Close 不可、(l) auto-naming「Bash 2」「Bash 3」が生成される
  - **共通**: (m) モバイル幅で長押しメニュー、(n) WS event `tab.added` / `tab.removed` で別クライアント間の同期

---

## スプリント S010: Files preview 拡張 (source code / 画像 / Draw.io) [ ]

現状の Files タブのプレビューは Markdown のみ対応。ソースコード / 画像 / Draw.io のプレビューを追加し、エディタや別ツールに切り替えずに Palmux 内で内容を確認できるようにする。本 sprint は **read-only**、編集機能は S011 で別途扱う。

**設計の中核**:
- **テキスト・ソースコード**: Monaco Editor を **read-only mode** で組み込み (S011 でも同じ Monaco を edit-mode で使うので、ライブラリは S010 で導入し S011 で再利用)
- **対応言語**: Monaco builtin を活用 (Go / TS / JS / Python / Rust / Java / C / C++ / shell / yaml / json / toml / md / sql / dockerfile 等の主要言語が拡張子から自動判定)
- **画像**: png / jpg / gif / webp / svg を `<img>` でインライン表示。**SVG のみ DOMPurify で `<script>` `<foreignObject>` 等を除去してから描画** (or `<iframe sandbox>` でも可。実装時に bundle size との trade-off で確定)
- **Draw.io**: drawio webapp を **palmux2 サーバ自身にバンドル** (`internal/static/drawio/` に webapp 一式 + `embed.FS` 配信、サイズ +10MB 程度) — VISION「シングルユーザ・自前ホスティング前提」整合、オフライン動作対応。`<iframe src="/static/drawio/?embed=1&...">` で読み込み、postMessage で read-mode 起動
- **既存 MD preview は維持** (markdown-it 経路はそのまま)。ファイル拡張子で **viewer 自動分岐**: `.md` → 既存 MD preview、`.drawio` / `.drawio.svg` → DrawioViewer、画像拡張子 → ImageView、それ以外 → MonacoView (read-only)、未知の拡張子 → raw text fallback

### ストーリー S010-1: Files タブで主要ファイル形式をプレビューできる [ ]

**ユーザーストーリー:**
Palmux のユーザとして、Files タブで開いたソースコード・画像・Draw.io 図をその場でプレビューしたい。なぜなら、内容を確認するたびにエディタや別アプリに切り替えるのは煩雑で、ブラウザだけで完結できれば PC・モバイル両方で同じ体験ができるからだ。

**受け入れ条件:**
- [ ] Files タブで主要言語のソースコード (Go / TS/JS / Python / Rust / Java / C/C++ / shell / yaml / json / toml / sql / dockerfile 等) を開くとシンタックスハイライト付きで表示される
- [ ] 行番号、word wrap、find (`Ctrl+F`)、code folding が動作する (Monaco builtin 機能)
- [ ] 画像ファイル (png / jpg / gif / webp / svg) を開くとインラインで表示される
- [ ] SVG 画像は `<script>` 等を除去した上で表示される (XSS 防御)
- [ ] `.drawio` / `.drawio.svg` ファイルを開くと Draw.io ビューア (read-only mode) で図が表示される
- [ ] Draw.io ビューアは palmux2 サーバ自身が提供する (外部 CDN 不要、オフライン動作)
- [ ] 既存の MD preview は引き続き同じ見た目で表示される
- [ ] 未知の拡張子のファイルは raw text として Monaco に表示される (fallback)
- [ ] ファイルサイズが大きい (例: > 10MB) ときは「ファイルが大きすぎます」のメッセージを出して preview を抑制する
- [ ] モバイル (< 600px) で全 viewer が表示崩れなく動作する
- [ ] **VISION スコープ外機能 (autocomplete / LSP / 言語サーバ連携 / Refactor / Debugger) は明示的に OFF** (Monaco の対応 option を無効化)

**タスク:**
- [ ] **タスク S010-1-1**: Monaco Editor を `npm i monaco-editor` で導入。`@monaco-editor/react` ラッパも同時導入。bundle 分割を検討 (lazy import で Files タブ初回開時のみロード)
- [ ] **タスク S010-1-2**: フロントに viewer dispatcher (`frontend/src/tabs/files/viewers/index.ts` など) を新設。ファイル拡張子 → component の routing table を持つ
- [ ] **タスク S010-1-3**: `MonacoView` (read-only) コンポーネントを実装。VISION スコープ外機能を OFF にする (`quickSuggestions: false`, `parameterHints: false`, `suggestOnTriggerCharacters: false`, `wordBasedSuggestions: false`, `acceptSuggestionOnEnter: 'off'`)
- [ ] **タスク S010-1-4**: `ImageView` コンポーネントを実装。MIME / 拡張子別に処理: png/jpg/gif/webp は `<img>` 直接、SVG は **DOMPurify で sanitize** してから `<img>` の `data:` URL or インライン描画
- [ ] **タスク S010-1-5**: drawio webapp (https://github.com/jgraph/drawio の `/src/main/webapp/`) をリポジトリの `internal/static/drawio/` に配置。LICENSE 表記もリポジトリに追加 (Apache-2.0)
- [ ] **タスク S010-1-6**: バックエンドで `internal/static/` を `embed.FS` 化、`/static/drawio/*` ルートで配信。auth 不要 (静的アセット)
- [ ] **タスク S010-1-7**: `DrawioViewer` コンポーネントを実装。`<iframe src="/static/drawio/?embed=1&modified=unsavedChanges&proto=json&spin=1">` で読み込み、`window.postMessage` で `{action: 'load', xml: <fileContent>}` を送信。read-only mode は drawio embed のオプションで指定
- [ ] **タスク S010-1-8**: ファイルサイズ制限 — preview 前にサイズチェックして 10MB (設定可能) を超える場合は viewer 表示せず警告メッセージ
- [ ] **タスク S010-1-9**: 既存 MD preview を新 viewer dispatcher に組み込む (既存挙動を変えずに refactor)
- [ ] **タスク S010-1-10**: モバイルで各 viewer が破綻しないことをデザイン調整。Files タブの "preview-only" モード (既存) と整合
- [ ] **タスク S010-1-11**: dev インスタンス + Playwright で実機検証。`tests/e2e/s010_*.py` で (a) Go / TS / Python ファイルがハイライト、(b) PNG / SVG が表示、(c) SVG の `<script>` が除去される、(d) `.drawio` ファイルが Draw.io iframe で読み込まれる、(e) MD は既存挙動、(f) 未知拡張子で raw text fallback、(g) 大ファイル抑制、(h) モバイル表示

---

## スプリント S011: Files テキスト + Draw.io 編集 [ ]

S010 で導入した Monaco / DrawioViewer を **編集モード** にし、変更内容を `PUT /api/.../files/{path}` で保存できるようにする。S010 から書き込み経路を持つ破壊的変更を含むため、独立 sprint として競合検出 / 未保存離脱 confirm / dirty state 管理を慎重に組み込む。

**設計の中核**:
- **テキスト編集**: S010 の `MonacoView` を edit-mode に切替可能にする。VISION スコープ外機能 (autocomplete / LSP / Refactor) は **edit mode でも引き続き OFF**。Monaco の機能で「編集環境の基礎」のみ提供 (シンタックスハイライト / find/replace / undo/redo / multi-cursor / line numbers / word wrap / auto-indent / bracket matching / code folding)
- **Draw.io 編集**: S010 の `DrawioViewer` を edit-mode で起動 (drawio embed の通常モード)。drawio から `event: 'save'` の postMessage を受信して `PUT` 発行
- **保存トリガ**: 明示的な Save (`Ctrl+S` / `Cmd+S` / Save ボタン)。オートセーブはなし
- **Dirty state 管理**: 未保存変更があれば Files ツリーのファイル名にバッジ表示、タブ切替・ブラウザ離脱時に confirm dialog
- **競合検出**: `PUT` リクエストに `If-Match: <etag>` ヘッダ。サーバが mtime + size hash で ETag を発行し、不一致なら 412 Precondition Failed で拒否。クライアントは「サーバ側で変更がありました — 再読込 / 上書き / キャンセル」 dialog を出す
- **書き込み API**: `PUT /api/repos/{repoId}/branches/{branchId}/files/{path}` (worktree-only、symlink/traversal 検証は既存 Files API のヘルパー流用、レスポンスに新 ETag)

### ストーリー S011-1: ソースコード / テキストファイルを編集して保存できる [ ]

**ユーザーストーリー:**
Palmux のユーザとして、Files タブで開いたソースコード・テキストファイルを編集して保存したい。なぜなら、簡単な修正のたびに別エディタに切り替えるのは煩雑で、Claude エージェントの提案を流し読みしながら手動で微修正することが多いからだ。

**受け入れ条件:**
- [ ] Files タブで開いた MonacoView に「Edit」ボタンがあり、クリックすると編集モードに切り替わる
- [ ] 編集モードで以下が動作する: シンタックスハイライト、Find/Replace (`Ctrl+F` / `Ctrl+H`)、Undo/Redo (`Ctrl+Z` / `Ctrl+Shift+Z`)、Multi-cursor (`Ctrl+Click` / `Ctrl+Alt+Up/Down`)、Line numbers、Word wrap、Auto-indent、Bracket matching、Code folding
- [ ] **VISION スコープ外機能 (autocomplete / LSP / 言語サーバ連携 / Refactor / Debugger) は edit mode でも明示的に OFF** (Monaco option で無効化)
- [ ] `Ctrl+S` (or `Cmd+S` on Mac) または「Save」ボタンクリックで保存。成功時に dirty バッジが消える
- [ ] 未保存変更があれば Files ツリーのファイル名に dirty バッジ (●) が表示される
- [ ] 未保存のままタブ / ブラウザを離れようとすると confirm dialog が出る
- [ ] サーバ側で同ファイルが他クライアント / git 操作で変更された後に保存しようとすると、412 Conflict 後「サーバ側で変更がありました」 dialog が出る (再読込 / 上書き / キャンセル)
- [ ] 編集モードを抜けて view mode に戻れる (未保存変更があれば離脱 confirm)
- [ ] モバイル (< 600px) で編集が動作する (タッチでのテキスト選択 / IME 入力 / Save ボタンが使える)

**タスク:**
- [ ] **タスク S011-1-1**: バックエンド `PUT /api/repos/{repoId}/branches/{branchId}/files/{path}` を実装。worktree-only、symlink/traversal 検証 (既存 Files API ヘルパー流用)、ETag は mtime + size の base64 hash、`If-Match` header 必須 (なければ 428 Precondition Required)、不一致で 412
- [ ] **タスク S011-1-2**: 既存 `GET /api/.../files/{path}` レスポンスに `ETag` ヘッダを追加 (PUT の `If-Match` で使う基準値になる)
- [ ] **タスク S011-1-3**: `MonacoView` の prop に `mode: 'view' | 'edit'` を追加。view mode は S010 のまま、edit mode で Monaco を `readOnly: false` に切替
- [ ] **タスク S011-1-4**: VISION スコープ外機能の Monaco option 無効化を **edit mode でも明示**: `quickSuggestions: false`、`parameterHints.enabled: false`、`suggestOnTriggerCharacters: false`、`wordBasedSuggestions: 'off'`、`acceptSuggestionOnEnter: 'off'`、`hover.enabled: false`、`occurrencesHighlight: 'off'` 等
- [ ] **タスク S011-1-5**: 「Edit」ボタンと「Save」ボタンを Files プレビューペインのヘッダに追加 (Fog palette 準拠)
- [ ] **タスク S011-1-6**: dirty state を Zustand store に追加 (`{repoId, branchId, path}` keyed)。Files ツリーで該当ファイルにバッジ表示
- [ ] **タスク S011-1-7**: `Ctrl+S` / `Cmd+S` のキーバインドを Monaco editor 内で処理 (デフォルトの browser save dialog を抑制)
- [ ] **タスク S011-1-8**: 未保存離脱 confirm: タブ切替時 / `beforeunload` event でブラウザ離脱時 / 別ファイル open 時に dirty なら confirm dialog
- [ ] **タスク S011-1-9**: 競合検出 UI: 412 受信時に dialog で「サーバ側のコンテンツを再読込 / 自分の変更で上書き / キャンセル」を選択。「再読込」では Monaco を最新コンテンツで置き換え、「上書き」では `If-Match` を新 ETag に変えて再 PUT
- [ ] **タスク S011-1-10**: dev インスタンス + Playwright で実機検証。`tests/e2e/s011_text_edit_*.py` で (a) Edit → 文字を入力 → Save → ファイルが書き換わる、(b) `Ctrl+S` でも保存、(c) 未保存タブ切替で confirm、(d) 別ターミナルで同ファイルを書き換えてから Palmux で Save → 412 → 競合 dialog、(e) VISION スコープ外機能 (autocomplete) が出ないこと、(f) Find/Replace / multi-cursor / undo が動く、(g) モバイルで保存できる

### ストーリー S011-2: Draw.io 図を編集して保存できる [ ]

**ユーザーストーリー:**
Palmux のユーザとして、Files タブで開いた Draw.io 図をブラウザ上で直接編集して保存したい。なぜなら、設計図を別アプリで編集してからコミットするより、ブラウザ上で完結したほうが早く、モバイル端末からでも図を直せると便利だからだ。

**受け入れ条件:**
- [ ] `.drawio` / `.drawio.svg` ファイルを開いて DrawioViewer の「Edit」ボタンをクリックすると編集モードに切り替わる
- [ ] Draw.io の標準 UI (図形パレット、ツールバー、プロパティパネル) で編集できる
- [ ] `Ctrl+S` (or `Cmd+S`) または「Save」ボタンで保存。drawio iframe の `event: 'save'` postMessage を palmux2 が受信して PUT 発行
- [ ] 未保存変更があれば dirty バッジが表示される (S011-1 と同じ store + 表示)
- [ ] 未保存離脱で confirm dialog (S011-1 と共通)
- [ ] 競合検出 dialog (S011-1 と共通、`If-Match` で 412)
- [ ] モバイル (< 600px) で編集が動作する (Draw.io のタッチサポートに依存、絶望的に使いづらい場合は edit ボタンを desktop only にする選択肢も検討)

**タスク:**
- [ ] **タスク S011-2-1**: `DrawioViewer` の prop に `mode: 'view' | 'edit'` を追加。edit mode では iframe の URL から `&chrome=0` を外して通常の drawio UI を出す
- [ ] **タスク S011-2-2**: drawio から飛んでくる postMessage (`event: 'save'`、`xml` payload) を listen し、`PUT /api/.../files/{path}` に `If-Match: <etag>` 付きで送信
- [ ] **タスク S011-2-3**: drawio iframe にも `Ctrl+S` を inject (drawio 自体が拾う場合あり、検証して必要なら parent から forward)
- [ ] **タスク S011-2-4**: dirty state は drawio の `event: 'autosave'` (or `event: 'editor-init'` 後の dirty signal) を S011-1 と共通の store に反映
- [ ] **タスク S011-2-5**: 競合検出 dialog (S011-1 と同じ component を再利用)
- [ ] **タスク S011-2-6**: モバイル: drawio のタッチ操作が破綻する場合は edit ボタンを `< 900px` で disabled + 「PC で編集してください」 tooltip を出す対応も視野
- [ ] **タスク S011-2-7**: dev インスタンス + Playwright で実機検証。`tests/e2e/s011_drawio_edit_*.py` で (a) `.drawio` ファイルを開いて編集モード → 矩形を 1 個追加 → Save → ファイル内容が変わる、(b) 競合 dialog のフロー、(c) `Ctrl+S` でも保存、(d) 未保存離脱 confirm

---

## スプリント S012: Git Core (review-and-commit flow) [ ]

現状の Git タブには `status` / `log` / `diff` / `branches` / `stage` / `unstage` / `discard` / `stageHunk` の REST が揃っているが、**commit が存在せず日常的な「変更確認 → コミット → push」が完結しない** (=「中途半端」の正体)。本 sprint で commit / push / pull / fetch を含む日常フローを揃え、AI commit message・モバイルスワイプ・filewatch 連動・Magit 風キーボードショートカットなど Palmux2 ならではの差別化要素も導入する。世の中の Git GUI (Sublime Merge / Fork / Magit / GitLens / GitHub Desktop) のサーベイから、**速度・発見性・安全性・AI ネイティブ・モバイル完結・キーボード優先** を Palmux2 Git の設計哲学とする。

**設計の中核**:
- Diff viewer は Monaco の diff mode を使用 (S010 で導入する Monaco を再利用、シンタックスハイライト + side-by-side / unified 切替)
- Status view は **filewatch (`fsnotify`)** で自動更新、WS event `git.statusChanged` で全クライアントに同期 (debounce 1000ms)
- Hunk-level に加え **行範囲レベルの staging** を実装 (Monaco の選択範囲を stage)
- Commit form は Monaco を message editor として再利用、amend / signoff (`-s`) / `--no-verify` オプション
- **AI commit message**: Claude タブが立っているときのみ有効化。staged diff + branch context を Claude タブの composer に prefill (新 frame `composer.prefill`)、ユーザは Claude タブに移動して送信 / 編集する
- 認証: `GIT_ASKPASS` / SSH agent / HTTPS credential helper を CLI 経由でそのままパススルー
- 危険操作: force-push は 2 段階 confirm + `--force-with-lease` をデフォルト推奨。force-delete branch は dialog
- モバイル: status view の各ファイル行で **左スワイプで stage、右スワイプで discard**、コンパクト diff モード (unified)
- キーボード: status view にフォーカス中、Magit 風の単一キー: `s`=stage、`u`=unstage、`c`=commit、`d`=discard、`p`=push、`f`=fetch

### ストーリー S012-1: Git タブから日常的なレビュー&コミットフローを完結できる [ ]

**ユーザーストーリー:**
Palmux のユーザとして、Git タブだけで「変更を確認 → 段階 → コミット → push」の日常フローを完結させたい。なぜなら、現状は commit や push のたびに別ターミナルに切り替えており、複数ブランチで Claude を並走させるとき、コミット作業のたびにフローが断絶するからだ。

**受け入れ条件:**

**Status & Diff:**
- [ ] Status view は filewatch で自動更新され、ファイル変更後 1 秒以内に反映される
- [ ] Status view は staged / unstaged / untracked / conflicts の 4 セクションに分かれる
- [ ] 各ファイルクリックで右ペインに Monaco diff mode で diff が表示される (側面比較 / unified 切替)
- [ ] Hunk 単位 + 行範囲 (Monaco の選択 → 選択範囲のみ stage) で staging できる

**Commit:**
- [ ] Commit form: message を Monaco editor で書ける (markdown highlight、wrap、複数行対応)
- [ ] amend オプション (前 commit を修正)、signoff (`-s`)、`--no-verify` のチェックボックス
- [ ] **AI commit message** ボタン: Claude タブが立っているときのみ enabled、クリックで staged diff + branch context を Claude タブの composer に prefill。ユーザは送信前に編集可能
- [ ] commit 成功後 status view が即更新される

**Sync (push/pull/fetch):**
- [ ] Push / Pull / Fetch ボタン、進行状況を Toast で表示
- [ ] Pull のオプション: merge / rebase / fast-forward only
- [ ] Force-push は 2 段階 confirm + `--force-with-lease` がデフォルト
- [ ] HTTPS credential / SSH agent / askpass プロンプトに対応 (CLI 経由パススルー)

**ブランチ操作:**
- [ ] ブランチ作成 / 切替 / 削除 (force-delete は confirm)、tracking branch の設定 (`--set-upstream`)

**モバイル & キーボード:**
- [ ] モバイル (< 600px) で各ファイル行を **左スワイプで stage、右スワイプで discard** 可能
- [ ] モバイルで diff は unified mode 固定
- [ ] Status view フォーカス中、Magit 風の単一キーが動作 (`s`/`u`/`c`/`d`/`p`/`f`)。input field 中は無効化

**タスク:**

**バックエンド (Git ops):**
- [ ] **タスク S012-1-1**: `internal/tab/git/handler.go` に `POST /api/.../git/commit` を追加 (`{message, amend, signoff, no_verify}` JSON body)
- [ ] **タスク S012-1-2**: `POST /api/.../git/push` (`{force, force_with_lease, set_upstream}`)、`POST /api/.../git/pull` (`{rebase, ff_only}`)、`POST /api/.../git/fetch` (`{prune}`)
- [ ] **タスク S012-1-3**: `POST /api/.../git/branches` (create from sha)、`PATCH /api/.../git/branches/{name}` (rename / set-upstream)、`DELETE /api/.../git/branches/{name}` (force flag)。**現在ブランチ / IsPrimary の保護** は既存 ghq/gwq 経路と整合
- [ ] **タスク S012-1-4**: 既存 `stageHunk` を拡張し、行範囲 staging (`stageLines`) に対応 (`{path, line_ranges: [{start, end}]}` を受けて該当範囲のみ staged)
- [ ] **タスク S012-1-5**: AI commit message API: `POST /api/.../git/ai-commit-message` — staged diff を取得し、Claude composer に prefill する prompt 文字列を返す (実際の送信は FE が Claude タブの WS frame で実施)
- [ ] **タスク S012-1-6**: filewatch (`fsnotify`) を `internal/tab/git/` に追加し、worktree 内の変更時に WS event `git.statusChanged` を emit。debounce 1000ms。`.git/` 配下の変更はフィルタしつつ、ref 変更 (HEAD / refs/) は検知

**バックエンド (認証 & 安全性):**
- [ ] **タスク S012-1-7**: push/pull/fetch の credential prompt パススルー: stderr に prompt が出るのを検出し、WS event `git.credentialRequest` で FE に出す。dialog 入力を stdin に流す。`GIT_TERMINAL_PROMPT=0` 環境で挙動確認
- [ ] **タスク S012-1-8**: SSH agent パススルー (`SSH_AUTH_SOCK` 環境変数を CLI に伝播)。HTTPS credential helper はシステム既定のもの (osxkeychain / libsecret 等) を尊重

**フロントエンド (UI):**
- [ ] **タスク S012-1-9**: `git-status.tsx` を refactor: 4 セクション (staged / unstaged / untracked / conflicts) 構造、filewatch event subscribe で auto-refresh
- [ ] **タスク S012-1-10**: `git-diff.tsx` を Monaco diff mode に置き換え (S010 の Monaco 流用)、シンタックスハイライト + side-by-side / unified 切替
- [ ] **タスク S012-1-11**: 行範囲 staging UI: Monaco の選択範囲をフックして「Stage selected lines」ボタン → backend `stageLines` 経由
- [ ] **タスク S012-1-12**: Commit form: Monaco editor (message)、amend / signoff / `--no-verify` チェック、Commit ボタン。amend モードでは前 commit message を初期値に
- [ ] **タスク S012-1-13**: AI commit message ボタン: Claude タブの立ち上がり状態を Zustand から読み、立っているときのみ enabled。クリックで `composer.prefill` WS frame を Claude タブに送信、Claude タブにフォーカス遷移
- [ ] **タスク S012-1-14**: Push/Pull/Fetch ボタン群、進行状況 Toast、credential dialog (パスワード入力 + remember 1 hour オプション)
- [ ] **タスク S012-1-15**: Force-push の 2 段階 confirm dialog (1 段目: `--force-with-lease` 推奨説明、2 段目: 最終確認 + 影響を受けるブランチ表示)
- [ ] **タスク S012-1-16**: ブランチ作成 / 切替 / 削除 / set-upstream UI を `git-branches.tsx` に追加

**フロントエンド (モバイル & キーボード):**
- [ ] **タスク S012-1-17**: Status view の各ファイル行に touch swipe handler。左 → stage、右 → discard。デスクトップでは無効
- [ ] **タスク S012-1-18**: モバイル `< 900px` で diff を unified mode 強制
- [ ] **タスク S012-1-19**: Status view focus 中の Magit 風キーバインド (`s`/`u`/`c`/`d`/`p`/`f`)。共通 keybinding handler (`frontend/src/lib/keybindings/`) を新設するか既存と統合

**E2E:**
- [ ] **タスク S012-1-20**: dev インスタンス + Playwright で実機検証。`tests/e2e/s012_*.py` で:
  - (a) ファイル変更 → status auto-update が 1 秒以内に反映、(b) Hunk staging + 行範囲 staging、(c) Commit (通常 / amend / signoff)、(d) Push / Pull / Fetch (ローカル fake remote `git daemon` or bare repo に対して)、(e) AI commit message: Claude タブを立てた状態で button → composer に prefill、(f) Force-push の 2 段階 confirm、(g) ブランチ作成 / 切替 / force-delete、(h) モバイル幅でスワイプによる stage / discard、(i) Magit 風キーで stage / unstage / commit

---

## スプリント S013: Git History & Common Ops [ ]

S012 で日常フローを揃えた後、週次〜不定期で必要になる **履歴を遡る・作業を付け替える系の操作** を揃える。Sourcetree / Fork の comprehensive log + GitLens の inline blame + lazygit の stash 操作を参考に、palmux2 ならではの **⌘K palette 統合** で全 Git op を発見可能にする。

**設計の中核**:
- Rich log view: linear timeline + filter (author / date range / grep) + paginated
- 簡素な SVG branch graph (commit dots + parent edges)。1 ブランチに 10〜100 コミット規模で読みやすい layout
- Stash 完全ライフサイクル (save with message / list / apply / pop / drop / show diff)
- Cherry-pick / Revert / Reset の preview-first UX (操作前に「何が起こるか」を表示)
- Reset の安全装置: `--hard` は **二段階 confirm + reflog 保証の説明** を必ず表示
- Tag 管理 (annotated / lightweight、push tag、delete remote tag)
- File history: Files タブから「このファイルの履歴」リンクで遷移
- Blame: Monaco の gutter にコミット情報を注釈、hover で commit 詳細
- ⌘K palette: 全 Git op を `git: ...` プレフィクスで登録 (`git: stash this`, `git: cherry-pick from...`, `git: blame current file`, `git: reset to...` 等)

### ストーリー S013-1: 履歴を遡って作業を付け替える操作を Git タブで完結できる [ ]

**ユーザーストーリー:**
Palmux のユーザとして、過去のコミットを遡って参照したり、stash や cherry-pick で作業の付け替えをしたい。なぜなら、本流から外れた実験的変更を一時退避したい場面、別ブランチの 1 コミットを取り込みたい場面、間違って commit した変更を revert したい場面が頻繁にあるからだ。

**受け入れ条件:**

**Log & Graph:**
- [ ] Rich log view: author / date range / grep でフィルタ、paginated (50 件ずつ)、無限スクロール
- [ ] 各 commit クリックで右ペインに commit 詳細 + diff (Monaco diff mode)
- [ ] 簡素な SVG branch graph: 多ブランチ時のみ表示 (1 ブランチ linear なら graph 省略)、 コミット数 1000 まで実用的に動く

**Stash:**
- [ ] Stash 一覧、message + 作成時刻、各 stash クリックで diff プレビュー
- [ ] Save (message 入力) / Apply / Pop / Drop / Show diff のアクション

**Cherry-pick / Revert / Reset:**
- [ ] Cherry-pick: log view から commit を右クリック → 「Cherry-pick onto current branch」、preview ダイアログで影響範囲 + 競合可能性を表示
- [ ] Revert: log view から commit を右クリック → 「Revert this commit」、自動生成される revert commit message のプレビュー
- [ ] Reset: log view から commit を右クリック → 「Reset to here」、mode 選択 (soft / mixed / hard)、`hard` は 2 段階 confirm + reflog 保証の説明

**Tag:**
- [ ] Tag 一覧 (annotated / lightweight 区別)、 commit ごとに紐づくタグも表示
- [ ] Tag 作成 (annotated message、 commit 指定)、 削除 (local + remote)、 push tag

**File history & Blame:**
- [ ] Files タブから「Show history」アクション → S013 の file history view に遷移、そのファイルを変更した commit 列を時系列表示
- [ ] Files preview の Monaco に「Blame」トグル、ON で gutter に commit hash + author + date 表示、 hover で commit 詳細

**⌘K palette:**
- [ ] 全 Git op が `git: ...` で発見可能 (`git: stash this`, `git: cherry-pick from...`, `git: blame current file`, `git: revert this commit`, `git: reset to...`, `git: tag this`, `git: log this branch` 等)

**タスク:**

**バックエンド:**
- [ ] **タスク S013-1-1**: `GET /api/.../git/log` を拡張 — author / date / grep / since / until / paginate 対応
- [ ] **タスク S013-1-2**: `GET /api/.../git/branch-graph` — commit list with parent edges (SVG layout 用の隣接情報)
- [ ] **タスク S013-1-3**: Stash CRUD: `GET /api/.../git/stash` (list)、`POST /api/.../git/stash` (push)、`POST /api/.../git/stash/{name}/apply` / `pop`、`DELETE /api/.../git/stash/{name}` (drop)、`GET /api/.../git/stash/{name}/diff`
- [ ] **タスク S013-1-4**: `POST /api/.../git/cherry-pick` (`{commit_sha, no_commit}`)、競合発生時は WS event で通知
- [ ] **タスク S013-1-5**: `POST /api/.../git/revert` (`{commit_sha}`)
- [ ] **タスク S013-1-6**: `POST /api/.../git/reset` (`{commit_sha, mode: soft|mixed|hard}`)
- [ ] **タスク S013-1-7**: Tag CRUD: `GET /api/.../git/tags`、`POST /api/.../git/tags` (`{name, commit_sha, message?, annotated}`)、`DELETE /api/.../git/tags/{name}` (`{remote: bool}`)
- [ ] **タスク S013-1-8**: `GET /api/.../git/file-history?path={path}` — 指定パスを変更した commit 列
- [ ] **タスク S013-1-9**: `GET /api/.../git/blame?path={path}&revision={sha}` — `git blame --porcelain` 出力をパースして JSON で返す

**フロントエンド:**
- [ ] **タスク S013-1-10**: `git-log.tsx` を rich log view に置き換え — filter UI、grep search、無限スクロール、commit 選択で右ペイン詳細
- [ ] **タスク S013-1-11**: 簡素 SVG branch graph component (commit dots + parent edges)、log view の左カラムに描画
- [ ] **タスク S013-1-12**: Stash manager UI (list + actions)、`git-view.tsx` に新セクション追加
- [ ] **タスク S013-1-13**: Cherry-pick / Revert / Reset modals — それぞれ preview dialog で影響表示、confirm で実行。Reset hard は 2 段階 confirm
- [ ] **タスク S013-1-14**: Tag manager UI (list + create + delete + push)
- [ ] **タスク S013-1-15**: File history view (Files タブからリンク遷移)、各 commit 選択で diff
- [ ] **タスク S013-1-16**: Blame view: Monaco の gutter に注釈描画、 hover で commit 詳細 popover
- [ ] **タスク S013-1-17**: ⌘K palette に Git op を全部登録 (`frontend/src/components/command-palette/`) — `git:` プレフィクスでフィルタ可能

**E2E:**
- [ ] **タスク S013-1-18**: dev インスタンス + Playwright で実機検証。`tests/e2e/s013_*.py` で (a) log filter 動作、(b) graph 描画、(c) stash full lifecycle、(d) cherry-pick (clean ケース)、(e) revert、(f) reset hard の 2 段階 confirm、(g) tag create / delete / push、(h) file history、(i) blame view、(j) ⌘K palette で git op が呼べる

---

## スプリント S014: Conflict & Interactive Rebase [ ]

S012 (日常)・S013 (履歴) で揃えた後の **本格 Git 体験を完成させる難所操作**。3-way merge での競合解決と interactive rebase は、 ターミナル `git rebase -i` で TODO ファイルをエディタで編集する伝統的な経路をタッチでも操作できる視覚 UI に置き換える。Tower / GitKraken / Sublime Merge の実装を参考に、 palmux2 のモバイル対応制約下で実装可能な範囲で組み込む。

**設計の中核**:
- 3-way merge UI: 3 ペイン (左: ours / 中央: result / 右: theirs)。各 hunk に accept-current / accept-incoming / accept-both / 手動編集ボタン。Mark resolved で `git add`
- Interactive rebase UI: TODO list を drag-to-reorder + 行ごとに action (pick / squash / edit / drop / fixup / reword) を select。Apply で `.git/rebase-merge/git-rebase-todo` に書き戻して `git rebase --continue`
- Rebase / merge の進行管理: 競合発生で pause、競合解決後に continue。abort / skip ボタン
- Submodule 管理 (init / update / status)
- Reflog viewer: 直近 100 件の HEAD movement、各 entry から「Reset to here」可能 (orphan commit 救済)
- Bisect helper: start / good / bad / reset、 進捗をビジュアライズ

### ストーリー S014-1: merge / rebase の競合を視覚的に解決し、履歴を整理できる [ ]

**ユーザーストーリー:**
Palmux のユーザとして、merge / rebase での競合を Palmux 内で解決し、コミット履歴の整理 (squash / reorder / edit) を視覚的にやりたい。なぜなら、競合のたびにエディタで `<<<<<<<` を手で消すのも、interactive rebase で TODO リストをシェルで編集するのも煩雑で、 Claude が複数の小さな commit を作った後の整理作業がつらいからだ。

**受け入れ条件:**

**Conflict resolution:**
- [ ] 競合発生時、Status view の conflicts セクションに該当ファイルが並ぶ
- [ ] 各ファイルクリックで 3-way merge UI (3 ペイン) が開く
- [ ] 各 conflict hunk に accept-current / accept-incoming / accept-both ボタン
- [ ] 手動編集も可能 (中央ペインを直接編集)
- [ ] 全 hunk 解決後 「Mark as resolved」ボタンが enabled、クリックで `git add`
- [ ] 全競合ファイルを resolve 後 「Continue merge / rebase」ボタンが現れる

**Interactive rebase:**
- [ ] log view から「Rebase from here」 → interactive rebase UI が開く
- [ ] TODO list を drag-and-drop で並び替え可能
- [ ] 各行に action select (pick / squash / edit / drop / fixup / reword)
- [ ] 「Apply」 で実際に rebase 開始、競合発生時は conflict UI に遷移
- [ ] Abort / Skip ボタンで途中中断可能

**Submodule:**
- [ ] Submodule の一覧 (path / commit / status) を表示
- [ ] Init / Update / Status のアクション

**Reflog & Bisect:**
- [ ] Reflog viewer: 直近 100 件の HEAD movement、 各 entry から「Reset to here」可能
- [ ] Bisect helper: start (good commit / bad commit を指定) → Palmux が自動で commit を checkout → ユーザが good/bad ボタン → 自動進行 → 結果表示

**タスク:**

**バックエンド:**
- [ ] **タスク S014-1-1**: `GET /api/.../git/conflicts` — 競合中ファイル + 各 hunk の `<<<<<<<` `=======` `>>>>>>>` 領域をパースして返す
- [ ] **タスク S014-1-2**: `GET /api/.../git/conflict/{path}` — 該当ファイルの ours / base / theirs を `git show :1:path :2:path :3:path` で取得
- [ ] **タスク S014-1-3**: `PUT /api/.../git/conflict/{path}` (resolved content を書き込み)、 `POST /api/.../git/conflict/{path}/mark-resolved` (`git add`)
- [ ] **タスク S014-1-4**: `GET /api/.../git/rebase-todo` (`.git/rebase-merge/git-rebase-todo` の中身)、 `PUT /api/.../git/rebase-todo` (書き戻し + `git rebase --continue`)
- [ ] **タスク S014-1-5**: Rebase ops: `POST /api/.../git/rebase` (start with options)、 `POST /api/.../git/rebase/abort`、 `POST /api/.../git/rebase/continue`、 `POST /api/.../git/rebase/skip`
- [ ] **タスク S014-1-6**: Merge ops: `POST /api/.../git/merge` (`{branch, no_ff, squash, message?}`)、 `POST /api/.../git/merge/abort`
- [ ] **タスク S014-1-7**: Submodule API: `GET /api/.../git/submodules`、 `POST /api/.../git/submodules/{path}/init`、 `POST /api/.../git/submodules/{path}/update`
- [ ] **タスク S014-1-8**: `GET /api/.../git/reflog?limit=100`
- [ ] **タスク S014-1-9**: Bisect API: `POST /api/.../git/bisect/start` (`{good, bad}`)、 `POST /api/.../git/bisect/good` / `bad` / `skip`、 `POST /api/.../git/bisect/reset`、 `GET /api/.../git/bisect/status`

**フロントエンド:**
- [ ] **タスク S014-1-10**: 3-way merge component: 3 ペイン (Monaco diff)、 hunk ごとの accept ボタン、手動編集対応、 mark-resolved ボタン
- [ ] **タスク S014-1-11**: Interactive rebase modal: TODO list (drag-and-drop)、 action select、 Apply
- [ ] **タスク S014-1-12**: Rebase / merge の進行 UI: status banner (rebasing 中 / merging 中)、 競合発生で conflict UI に自動遷移、 abort / skip / continue ボタン
- [ ] **タスク S014-1-13**: Submodule panel
- [ ] **タスク S014-1-14**: Reflog viewer
- [ ] **タスク S014-1-15**: Bisect panel: start dialog → 自動 checkout → good/bad ボタン → 結果表示

**E2E:**
- [ ] **タスク S014-1-16**: dev インスタンス + Playwright で実機検証。`tests/e2e/s014_*.py` で (a) 簡単な 2 way 競合の解決、(b) interactive rebase で reorder + squash → apply → 履歴が変わる、(c) rebase abort、(d) submodule init / update、(e) reflog から reset、(f) bisect の happy path

---

## 依存関係

- S001 〜 S007 はそれぞれ **独立** に実装可能。実行順序の根拠はユーザ価値の累積効果（Plan モード UI が最も体験に直結する）
- S001 完了後に S002（Settings editor）を続けるのは、Plan モード UI で `permissions.allow` への自動追加を促すフローが Settings editor で確認できる流れになるため
- S003（Sub-agent ツリー）は他のスプリントとデータ依存はないが、描画ロジックが大きいので独立 PR として扱う
- S007（AskUserQuestion）は S001 と同じ「専用 kind ブロック + tool_result 抑制 + 専用 UI」パターンなので、S001 完了後に挿入することで実装の参照点が揃う。Phase 4 のバックログから昇格
- S008（任意ファイルのアップロード添付）は **S006 のサーバ側ピッカー UI を削除する破壊的変更を含む**。S006 の `--add-dir` BE プラミングは upload dir の自動登録として残すが、`AttachMenu` の dir/file 項目と `PathPicker` は削除されるため、 S006 の後にしか実装できない
- S009（複数インスタンス可タブの統一管理 UI）は S008 と独立 (Composer surface と TabBar surface の改修で merge conflict は限定的)。並列実装も可能だが、autopilot 実行は逐次で十分。S009 は `Provider` interface 拡張 + `Manager.agents` map キー refactor + 既存 Bash UI 移行を伴うため、並列実装するなら片方が先にマージされてからもう片方が rebase する運用になる
- S010（Files preview 拡張）は S008 / S009 と独立。Files タブのみが改修対象。Monaco / drawio webapp の bundle 増加 (~10MB) を伴う
- S011（Files テキスト + Draw.io 編集）は **S010 必須前提**。S010 で導入する `MonacoView` / `DrawioViewer` の view mode を edit mode に拡張する形で実装するため、 S010 の後にしか実装できない。書き込み API (`PUT`) と競合検出を持つので S010 とは別 sprint として安全に分離
- S012（Git Core）は **S010 必須前提** (Monaco diff mode を再利用)。S008 / S009 / S011 とは独立 (Files / Composer / TabBar surface との競合なし、Git surface のみ改修)
- S013（Git History & Common Ops）は **S012 必須前提** (S012 の commit / push 経路と filewatch を前提に履歴・付け替え系を載せる)
- S014（Conflict & Interactive Rebase）は **S013 必須前提**ではないが、 S012 + S013 完了後の方が「日常 / 履歴」フローが揃った状態で残る難所として実装する流れが自然。 S014 単独でも論理的には実装可能

## バックログ

Phase 4 以降に位置付けられる項目。スコープ確定の段階で個別 Sprint に切り出す:

- [ ] **Tool 結果 / 会話の virtualization** (Phase 4.1)
  数千行の grep / 数百ターン履歴で固まらないよう `react-window` 等を導入。
- [x] ~~**Markdown / コードブロック syntax highlighting** (Phase 4.2)~~
  S010 (Files preview 拡張) で Monaco Editor を導入する際、Markdown 内コードブロックも Monaco レンダラに乗せられるなら同時対応。優先度低め (既存 MD preview の見た目に合わせる)。バックログとしては S010 完了後に再評価。
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
- [x] ~~**Host-filesystem picker** (S006 由来)~~
  ユーザレビューで方針転換: サーバ側ファイル参照は `@` autocomplete に集約、ローカルデバイス上のファイルは S008 のアップロード経路を使う方針に決定。Host-filesystem picker は実装しない。
- [ ] **Per-branch persisted attachments** (S006 由来)
  現状 dir/file チップは UI state のみで、ページリロード / ブランチ切替で消える。`@CLAUDE.md` のような毎回付けたい参照を「このブランチには常にこの dir/file を含める」設定として永続化する。サイズ S/M。
- [ ] **Composer Enter キー submit の inline-completion 挙動調査** (S006 E2E 由来)
  S006 の E2E で `textarea.press("Enter")` が稀に submit を発火しないケースを観測 (回避策として Send ボタン click を使った)。inline-completion handler が Enter を吸ったまま completion が cancel されない経路がある可能性。サイズ S。
- [ ] **Claude / Bash タブの drag reorder** (S009 由来)
  S009 では「最低 1、最大 N、`+` で末尾追加、右クリックで Close」という最小ライフサイクルしか提供しない。タブの並び替え (drag-and-drop で順序変更) は `Multiple() = true` のタブ種別共通の機能として別 sprint に切り出す。サイズ S/M。
- [ ] **Claude / Bash タブの rename** (S009 由来)
  S009 では auto-naming のみ実装し、既存の Bash 命名 prompt は廃止される。ユーザが「draft refactor」「dev server」のように意味のある名前を付けたい場面に対応するため、`Multiple() = true` のタブ種別共通の rename 機構を別 sprint で実装する。タブの context menu に「Rename」項目を追加。サイズ S。
- [ ] **画像プレビューの zoom / pan / 100% トグル** (S010 由来)
  S010 では画像は表示のみ。大きい画像のディテール確認や、UI スクリーンショット比較などのユースケースに備えて zoom / pan / fit-to-window / 100% トグルを別 sprint で追加。サイズ S。
- [ ] **編集者間のリアルタイム協調 (CRDT / OT)** (S011 由来)
  S011 では競合検出は ETag ベースの楽観ロック (412 Conflict)。複数クライアント・複数デバイスで同ファイルを同時編集するユースケースが頻発する場合は yjs 等の CRDT 導入を検討。VISION「シングルユーザ」前提で頻度は低いはずだが、複数デバイスからの併用で衝突する可能性はある。サイズ L。
- [ ] **AI conflict 解決提案** (S014 由来 / Phase 5 候補)
  3-way merge の各 conflict hunk に対し「Claude にこの conflict をどう解決すべきか相談」ボタンを追加。Claude タブの composer に conflict context (ours / base / theirs + 周辺コード) を prefill。単一の hunk について Claude に提案させ、ユーザが受け入れるか手動編集する。サイズ M。
- [ ] **AI changelog 草稿生成** (S013 / S014 由来 / Phase 5 候補)
  log view で commit range を選択 → 「AI changelog ボタン」で Claude タブに「このコミット範囲を release notes 形式で要約して」のプロンプトを prefill。出力をリリースタグ作成時の annotated message に流用。サイズ S/M。
- [ ] **Stack-based workflow (Sapling 風)** (S013 / S014 由来 / Phase 5 候補)
  branch ではなく commit stack で作業を管理する Sapling / git-branchless 風のワークフロー。各 commit が独立して push/PR 化できる前提のチーム向け機能だが、 Palmux2 はシングルユーザ前提なので「複数 in-flight な変更を 1 ブランチで同時管理」用途で意味がある可能性。spike が必要。サイズ L+。
- [ ] **Bash タブも Magit 風キーバインド対象に** (S012 由来 / 対称性)
  S012 で Git タブにキーボードショートカットを入れたが、 Bash タブの focus 中に Magit 風キーは無効化されるべき (シェル入力との衝突回避)。汎用 keybinding handler を Bash でも統合する設計確認。サイズ S。
