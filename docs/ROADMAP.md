# プロジェクトロードマップ: Palmux v2 — Phase 3

> Phase 1 (stream-json + MCP) と Phase 2 (Cores A〜E、入力補完、ツール出力リッチ化、セッション運用、安全機能) は完了済み。本ロードマップは **Phase 3 (拡張機能の本命)** をスプリント単位で管理する。Phase 1/2 の経緯は [`docs/original-specs/06-claude-tab-roadmap.md`](original-specs/06-claude-tab-roadmap.md)、Phase 4/5+ の予定も同ファイル参照。

## 進捗

- **直近完了: S022 — Mobile UX 総点検 + Playwright モバイル E2E ハーネス** (autopilot 完了 / `autopilot/main/S022` ブランチ) — **22 スプリント全完了**
- 合計: 23 スプリント | 完了: 23 | 進行中: 0 | 残り: 0
- [████████████████████] 100%

## 実行順序

S001 ✅ → S002 ✅ → S003 ✅ → S007 ✅ → S004 ✅ → S005 ✅ → S006 ✅ → S008 ✅ → S009 ✅ → S010 ✅ → S011 ✅ → S012 ✅ → S013 ✅ → S014 ✅ → S015 ✅ → S016 ✅ → S017 ✅ → S018 ✅ → S019 ✅ → S020 ✅ → S021 ✅ → S022 ✅ → S023 ✅

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

## スプリント S008: 任意ファイルのアップロード添付 (Upload Image 拡張) [DONE]

ユーザのデバイス (PC/スマホ/タブレット) にあるローカルファイルをアップロードして Claude Code に読ませる。S006 のサーバ側ピッカー UI は削除し、代わりに既存の **Upload Image 経路を画像以外のファイル全般に汎用化** する。VISION の「シングルユーザ・自前ホスティング前提」を踏襲し、ファイルは Anthropic File API ではなく palmux2 サーバ自身に保存する (`--file` フラグは使わない、S006 D-1 と同じ判断)。

**設計の中核**:
- アップロード先: `<attachmentUploadDir>/<repoId>/<branchId>/<sanitized-name>` (per-branch 隔離)
- CLI 起動時に `--add-dir <attachmentUploadDir>/<repoId>/<branchId>` を **常に追加** → 添付ごとの respawn 不要
- 送信時の振り分け: 画像 → 既存 `[image: <abspath>]` (vision 入力)、それ以外 → `@<abspath>` (Read される)
- アップロード経路: GUI ボタン / drag-and-drop / paste の 3 経路すべて

### ストーリー S008-1: ローカルファイルを 3 経路でアップロードして添付できる [x]

**ユーザーストーリー:**
Palmux のユーザとして、自分のデバイスにあるファイル (テキスト / PDF / ログ / 画像 / 任意のドキュメント) を Claude に読ませたい。なぜなら、worktree に含まれていない参照資料を会話に持ち込みたいケースが頻繁にあり、サーバに事前配置するのは煩雑で、Anthropic File API への外部アップロードは privacy / quota / 認証経路の点で受け入れがたいからだ。

**受け入れ条件:**
- [x] 既存の Upload Image 経路を画像以外のファイル種別にも拡張する (画像も同じ経路で動作継続)
- [x] Composer の `+` ボタンから「Attach file」を選択するとローカルのファイル選択ダイアログが開き、任意のファイルを選べる
- [x] Composer 領域へのドラッグ＆ドロップでも同じくアップロードされる (ファイル種別を問わない)
- [x] クリップボードからのペーストでもアップロードされる (画像クリップボードは既存挙動を維持しつつ、ファイルクリップボード一般もサポート)
- [x] アップロード成功後、Composer に添付チップが表示される (画像はサムネイル、それ以外は 📄 アイコン + ファイル名)
- [x] 添付チップの × ボタンで添付を取り消せる
- [x] 送信時に画像は `[image: <abspath>]`、それ以外は `@<abspath>` として user message 本文末尾に注入される (実 CLI で `Read` が走ることを確認)
- [x] S006 で追加したサーバ側ピッカー UI (`+` メニューの "Add directory" / "Add file"、`PathPicker` コンポーネント) は完全に削除される
- [x] アップロードファイルは per-branch ディレクトリに隔離され、TTL でクリーンアップされる

**タスク:**
- [x] **タスク S008-1-1**: グローバル設定 `imageUploadDir` を `attachmentUploadDir` に汎化 (デフォルト `/tmp/palmux-uploads/`)。後方互換のため `imageUploadDir` キーが残っていれば `attachmentUploadDir` として読み込む
- [x] **タスク S008-1-2**: `POST /api/upload` の MIME 制限を解除し任意ファイルを受け付ける。保存先を `<attachmentUploadDir>/<repoId>/<branchId>/<sanitized-name>` (per-branch 隔離) に変更。レスポンスに絶対パス + MIME / 元ファイル名を含める
- [x] **タスク S008-1-3**: CLI 起動時の argv に `--add-dir <attachmentUploadDir>/<repoId>/<branchId>` を **常に含める** よう `Manager.EnsureClient` を修正。起動時固定で添付ごとの respawn 不要にする
- [x] **タスク S008-1-4**: Composer の `+` メニューを「Attach file」1 項目に簡素化。S006 の "Add directory" / "Add file" / "Upload image…" の 3 項目を統合
- [x] **タスク S008-1-5**: Composer に drag-and-drop ハンドラを追加。drop 領域は composer ルート、ファイル種別を問わず受け付ける。multi-file drop もサポート
- [x] **タスク S008-1-6**: 既存の paste ハンドラ (画像のみ対応) を画像以外のファイル Blob にも対応させる。`event.clipboardData.files` のすべてを処理
- [x] **タスク S008-1-7**: 添付チップの表示を MIME / 拡張子で分岐 (画像 → サムネイル、それ以外 → 📄 + ファイル名)。アップロード進行中の状態 (アップロード中 / 完了 / エラー) も視覚化
- [x] **タスク S008-1-8**: 送信時の振り分けロジックを実装: `kind === 'image'` → `[image: <abspath>]`、それ以外 → `@<abspath>` を user message 末尾に注入
- [x] **タスク S008-1-9**: S006 の `AttachMenu` の dir/file 項目、`PathPicker` コンポーネント、関連 CSS、WS frame の `addDirs[]` 受信経路 (`SendUserMessageWithDirs` を経由した user-supplied dirs) を削除。`Agent.AddDirs` フィールドと `validateAddDirs` 自体は upload dir の自動登録に流用するため残す
- [x] **タスク S008-1-10**: TTL クリーンアップを実装 — 起動時に `<attachmentUploadDir>/<repoId>/<branchId>/` 配下の N 日以上古いファイルを削除 (デフォルト 30 日、設定で変更可能)。ブランチ close 時の per-branch dir 削除も検討
- [x] **タスク S008-1-11**: dev インスタンス + Playwright で実機検証。`tests/e2e/s008_*.py` で (a) ファイル選択ダイアログ経由、(b) drag-and-drop、(c) paste の 3 経路を検証。`ps -ef | grep claude` で `--add-dir <attachmentUploadDir>/...` が argv に含まれることと、送信後の user message に `@<abspath>` (テキストファイル) / `[image: <abspath>]` (画像) が含まれることを確認

> 完了ログ: [docs/sprint-logs/S008/decisions.md](sprint-logs/S008/decisions.md). E2E: `tests/e2e/s008_upload_routes.py` (file picker / drag-and-drop / paste の 3 経路 + ps argv 監視で `--add-dir <attachmentUploadDir>/<repoId>/<branchId>` を確認). 実 CLI が観測可能なケースで PASS。

---

## スプリント S009: 複数インスタンス可タブの統一管理 UI (Claude / Bash) [x]

> **追補 (S009-fix-1, 2026-05-01)**: post-S015 refine で 4 件の lifecycle / WS reconnect バグが報告され、緊急 fix sprint で対応した。
> - Bug 1+2: Claude タブを `+` で増やすと Bash タブが消える、 Claude タブを消しても Bash が戻らない → `recomputeTabs` が `tmux ListWindows` 失敗を「ウィンドウ 0 個」と誤解釈していた。 `internal/store/store.go` で transient 失敗時に既存タブリストを保持するよう修正
> - Bug 3: Bash タブが 3 秒ごとに reconnecting → Bug 1+2 の症状 (FE が Bash タブを失う → terminal-view unmount → WS close → reconnect)。 上記修正で副次的に解消
> - Bug 4: Bash タブを増やしてもタブが増えない/つながらない → `pickNextWindowName` が tmux session の GC race で失敗していた。 `internal/store/tab.go::AddTab` で `ensureBranchSession` を先に呼ぶよう修正
> - 関連 FE 修正: legacy URL `/{repo}/{branch}/claude` (canonical 化前のブックマーク等) を `claude:claude` へ自動リダイレクト (`frontend/src/components/panel.tsx`)
> - E2E: `tests/e2e/s009_fix_lifecycle.py` で 7 cases (a-g) を新規カバー。 既存の S008-S015 すべて pass を確認
> - 詳細は `docs/sprint-logs/S009-fix-1/decisions.md`
>
> **追補 (S009-fix-2, 2026-05-01)**: S009-fix-1 後の手動検証で **3 件の Bash 専用バグ** が再発。「Bash WS event propagation を修正」の表題で対応。
> - Bug h: Bash 端末 WS が約 3 秒間隔で reconnect → `attachTab` が `NewGroupSession` 直後に `tmux attach-session` を呼ぶが、 base session に user-added Bash window が無い瞬間に当たると `1011 failed to attach` で WS が即 close、 ReconnectingWebSocket が ~3s でリトライ。 `Store.EnsureTabWindow` を attach 直前に呼んで base に target window を確実に存在させる
> - Bug i: Bash タブを `+` で追加しても Reconnecting のまま → 同じ EnsureTabWindow 不足。 同じ修正で解消
> - Bug j: Bash タブ削除が Claude タブ操作まで反映されない → `RemoveTab` が `KillWindowByName` 失敗で 500 を返し、 FE の `removeTab` action は throw で `reloadRepos()` をスキップ。 `isWindowGoneErr` で「window already gone」を success 扱いに
> - 副次的に判明した bug: `enrichRecoverySpecs` が stale clone を使って削除済みウィンドウを resurrect していた → live `s.repos` を再読する形に修正
> - Cross-instance 安全策: `Store.knownConnIDs` を導入し、 sync_tmux の zombie group-session kill を「自分が発行した conn ID」 限定に。 host + dev palmux2 が同じ tmux server を共有するブートストラップシナリオで互いのクライアントを巻き込まなくなる
> - E2E: `tests/e2e/s009_fix_lifecycle_v2.py` で 4 cases (h-k) を新規カバー。 S008-S015 + S009-fix-1 既存 E2E は pass のまま
> - 詳細は `docs/sprint-logs/S009-fix-2/decisions.md`
>
> **追補 (S009-fix-3, 2026-05-01)**: S009-fix-2 直後にユーザから「Bash 端末が数秒使える ↔ 数秒 Reconnecting を周期的に繰り返す」 と再報告。 fix-2 までの E2E は短い窓 + drop budget で検証していたためサイクル自体を見逃していた。
> - 改良 E2E `tests/e2e/s009_fix_periodic_check.py` を新設: 3 分連続で WS continuity / 2 秒毎 marker round-trip / server log scrape を並行監視し、 ws_close 0 / marker_fail 0 / zombie_kill 0 を合格条件とする。 close→close 間隔も最後にレポート (定期パターンを即座に可視化)
> - 修正前再現: 60 s で 12 回 ws_close、 close→close 間隔は `[4.98, 5.02, 4.98, 5.03, 4.95, …]` — `SyncTmuxInterval = 5s` と完全に同期。 シェアド tmux server 上で host palmux2 (旧バイナリ) が dev palmux2 のセッションを 5 秒毎に zombie として kill していた
> - 根本原因: `_palmux_` 接頭辞が **プロセス・グローバル**だったため、 host と dev の `sync_tmux` ループがお互いのセッション空間を共有し、 互いに「未追跡 → zombie」 と判断して kill しあっていた。 fix-2 の `knownConnIDs` は group session だけを保護しており base session race は残っていた
> - 修正アプローチ 1 (採用): tmux session 接頭辞を CLI フラグ化 (`--tmux-prefix`)。 `domain.PalmuxSessionPrefix` を mutable var + `domain.Configure` 経由で設定。 `Makefile` の `make serve INSTANCE=<name>` が `--tmux-prefix=_pmx_<name>_` を自動付与。 デフォルト未指定時は従来通り `_palmux_`、 既存インストールに影響なし
> - **`_palmux_` ではなく `_pmx_` を選んだ理由**: 旧バイナリの host (pre-fix-3) は単純な `HasPrefix(name, "_palmux_")` で peer instance を見ているため、 `_palmux_dev_*` も自分のセッションとして claim してしまう。 `_pmx_*` なら `HasPrefix` が false を返すので host を rebuild する前から isolation が効く
> - 防御策として `ParseSessionName` を厳格化: prefix 後の repoID 部分が `--` を含む場合のみ Palmux のセッションと認める (本物の repoID は slug+hash 形式 `<owner>--<repo>--<hash4>` で必ず `--` を含む)。 fix-3 host が将来 `_palmux_<word>_*` 形式の peer を見ても誤って claim しない
> - E2E: 修正後 180 s 連続監視で ws_closes=0 / marker_fails=0 / zombie_kills=0 を達成。 S009-fix-1 / fix-2 / S008 / S009 multi-tab / S015 既存 E2E すべて pass
> - 詳細は `docs/sprint-logs/S009-fix-3/decisions.md`、 `investigation.md`
>
> **追補 (S009-fix-4, 2026-05-01)**: fix-3 後もユーザは UI 上で「Reconnecting」 オーバーレイを観測。 server-side metrics は clean だったが、 直接 Playwright で DOM をポーリングしてサイクルを再確認した結果、 **shared `_palmux_` prefix を持つ peer palmux2 (空 repos.json) が base session を 5 秒毎に kill していた**ことが判明。 fix-3 の prefix isolation は dev / host を救うが、 ユーザ環境にレガシーバイナリ・テスト用 instance 等が同居している間は別 instance 経由で同じ問題が再発し得る
> - 差分調査: pre-S009 (`04bfa9b`) と S009 (`3c63887`) で `internal/store/sync_tmux.go` は **完全同一**。 `tracked` に乗らない `_palmux_*` を全 kill する logic は S009 以前から存在。 ただし fix-2 で **group session** には `knownConnIDs` 保護が入った一方、 **base session** はノーガードのまま残っていた
> - 修正: `Store.knownBaseSessions` を追加。 `ensureSession` が触ったセッション名だけを kill 対象にする。 `CloseBranch` でエントリを削除。 fix-2 の group session 保護と完全対称
> - 「保守的にした」 セマンティクス: 自プロセスが create / recover していない `_palmux_*` セッションは存在ごと無視 (read のみ)。 自分のセッションが close されたが notify が消えたケースだけ kill する
> - UI 検証 (Playwright 3 分)`tests/e2e/s009_fix4_ui_monitor.py` で DOM ポーリング 250 ms 毎、 「Reconnecting」 出現 0 回を pass 条件とする。 fix-4 適用 + peer-killer 排除後 = 0 件。 fix 前 = 36 件 (5 秒周期)
> - 既存 fix lifecycle E2E (`s009_fix_lifecycle.py` / `_v2.py` / `_periodic_check.py`) すべて pass、 unit test (`go test ./...`) clean、 `make build` clean
> - 詳細は `docs/sprint-logs/S009-fix-4/decisions.md`、 `investigation.md`

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

### ストーリー S009-1: 1 ブランチ内で複数 Claude / Bash タブを統一 UI で管理できる [x]

**ユーザーストーリー:**
Palmux のユーザとして、Claude タブと Bash タブを同じ操作 (`+` で追加、右クリックで Close) で並列に立ち上げたい。なぜなら、リファクタしながら別 Claude で設計 Q&A をしたり、ビルド・サーバ・watcher を別 Bash で並走させたい場面が頻繁にあり、タブ種別ごとに違う UX を覚えずに並列タスクをこなしたいからだ。

**受け入れ条件:**

**共通 (Claude / Bash 両方に適用):**
- [x] `Multiple() = true` のタブ種別ごとに、その種別の **一番右のタブの右側** に `+` ボタンが表示される (Claude グループ末尾と Bash グループ末尾にそれぞれ独立した `+`)
- [x] `+` クリックで同種別の新タブが作成され自動でフォーカスする
- [x] 上限到達時は `+` ボタンが disabled、tooltip で「上限 (N) に達しています」と表示
- [x] 各タブで右クリック (モバイル: 長押し 500ms) するとコンテキストメニューが開く
- [x] 同種別タブが下限 (デフォルト 1) に達しているときは Close 項目が出ない
- [x] Close 時、進行中状態 (Claude: assistant turn / composer 下書き、Bash: 実行中プロセス) があれば confirm dialog が出る
- [x] WS イベント `tab.added` / `tab.removed` で全クライアントが TabBar を同期
- [x] モバイル (< 600px) で `+` ボタンと長押しメニューが破綻しない (TabBar overflow 時のスクロールも確認)

**Claude タブ固有:**
- [x] Claude タブは最低 1 個必ず存在し、最大 `maxClaudeTabsPerBranch` (デフォルト 3) 個まで立ち上がる
- [x] 各タブが独立した claude CLI subprocess を spawn する (`pgrep -f claude` で複数 PID を観測可能)
- [x] 各タブが独立した stream-json 接続 / Session / MCP request 経路を持ち、片方の会話がもう片方に混入しない
- [x] 2 番目以降の Claude タブは `--resume` なしで新セッションとして起動する (新しい session_id が発行される)
- [x] タブラベルは「Claude」「Claude 2」「Claude 3」
- [x] 閉じた Claude タブの session_id は **既存のセッション履歴 popup から resume 可能** (削除されず orphan で残る)
- [x] URL ルーティング `/<repoId>/<branchId>/claude:claude-2` がブラウザの戻る/進むで遷移できる
- [x] 既存 URL `/<repoId>/<branchId>/claude` は引き続き 1 番目の Claude タブを開く (後方互換)
- [x] 通知 (Activity Inbox) には発火元の Claude タブ名 (例: 「Claude 2」) が含まれる

**Bash タブ固有:**
- [x] Bash タブは最低 1 個必ず存在し、最大 `maxBashTabsPerBranch` (デフォルト 5) 個まで立ち上がる
- [x] 既存の Bash 削除 UI (TabBar 末尾の confirm dialog 直開き経路) は完全に context menu 経由に置き換わる
- [x] 既存の Bash 追加時の名前 prompt (`prompt: "New tab name"`) は廃止され、auto-naming「Bash」「Bash 2」「Bash 3」になる (rename はバックログ「Claude / Bash タブ rename」で対応)
- [x] 各 Bash タブが独立した tmux window (`palmux:bash:bash`, `palmux:bash:bash-2`, ...) を持つ — 既存挙動の維持
- [x] グローバル設定 `maxBashTabsPerBranch` を変更すると次回起動から反映される

**タスク:**

**共通基盤 (TabBar 汎用化 + Provider interface 拡張):**
- [x] **タスク S009-1-1**: `internal/tab/provider.go` の `Provider` interface に `Limits(SettingsView) InstanceLimits` を追加 (Min=Max=1 for Files/Git、Min=1/Max=settings driven for Claude/Bash)
- [x] **タスク S009-1-2**: グローバル設定に `maxClaudeTabsPerBranch` (デフォルト 3) と `maxBashTabsPerBranch` (デフォルト 5) を追加 — `internal/config/settings.go` で `tab.SettingsView` を実装し、 `GET/PATCH /api/settings` で読み書き可能
- [x] **タスク S009-1-3**: `POST /tabs` に上限チェック追加 (超過で 409 Conflict、 `ErrTabLimit`)、 `DELETE /tabs/{tabId}` に Min=1 floor 保護
- [x] **タスク S009-1-4**: `frontend/src/components/tab-bar.tsx` を per-type group 描画にリファクタ。 `Multiple()=true` 種別の各グループ末尾に `+` ボタン (`tab-add-{type}` testid)。 上限到達で disabled
- [x] **タスク S009-1-5**: ContextMenu 経由の Close。 Min floor 到達タブの Close 項目は disabled (ラベルに "(last Claude — protected)" / "(protected)") 、 進行中状態は既存 `confirmDialog`
- [x] **タスク S009-1-6**: auto-naming ロジック (`claudeagent.DisplayNameForTab` + `displayNameFor` for bash) で `{type}:{type}` → `Type`、 `{type}:{type}-N` → `Type N`
- [x] **タスク S009-1-7**: store の AddTab/RemoveTab が既に `EventTabAdded` / `EventTabRemoved` を publish しているのを確認、 非 tmux multi タブも同経路でフロー (recomputeTabs 経由)

**Claude 固有:**
- [x] **タスク S009-1-8**: `claudeagent.Provider.Multiple()` を `true` に切り替え、 `Limits()` で settings 駆動の Max=3 を実装
- [x] **タスク S009-1-9**: `Manager.agents` map のキーを `(repoId, branchId, tabId)` に refactor。 `EnsureAgent`/`Get` に tabID 引数を追加、 `KillBranch` は branch prefix scan で全 tab Agent を shutdown
- [x] **タスク S009-1-10**: `tmp/sessions.json` の `Active` / `BranchPrefs` を per-tab キー化 (`{repoId}/{branchId}/{tabId}`)、 `BranchTabs` map で per-branch tab list を永続化、 `migrateLegacyTabKeys()` で legacy 2-segment key を `claude:claude` に移行
- [x] **タスク S009-1-11**: MCP server は per-Agent (PermissionRequester=Agent) なので multi-tab で混線しない。 `tool_use_id` dedupe も Agent 内 map で完結。 変更不要 (decisions.md に記録)
- [x] **タスク S009-1-12**: `EnsureAgent` は per-tab `ActiveFor(...)` を読み、 secondary タブは empty resume → 新 session_id 発行。 canonical タブは legacy migration で resume を継承
- [x] **タスク S009-1-13**: `useAgent(repoId, branchId, tabId?)` で WS URL を `/tabs/{tabId}/agent` に切替、 canonical は legacy `/tabs/claude/agent` route に維持。 server は `/tabs/{tabId}/agent` も同 handler に bind し `tabIDFromRequest()` で extract
- [x] **タスク S009-1-14**: `notify.Notification.TabID` / `TabName` フィールド追加、 `Agent.publishNotification` で stamp、 FE が pendingItem.tabName を render

**Bash 固有:**
- [x] **タスク S009-1-15**: `bash.Provider.Limits()` で settings 駆動の Max=5 (default fallback) 実装
- [x] **タスク S009-1-16**: TabBar の `prompt: "New tab name"` を削除、 `addTab(repoId, branchId, type)` で server 側 `pickNextWindowName` が `bash:bash-N` を auto-pick
- [x] **タスク S009-1-17**: Bash 用の confirmDialog 直開き経路を削除、 共通 ContextMenu の Close 項目に統合 (Claude / Bash で同一 UX)

**モバイル / E2E:**
- [x] **タスク S009-1-18**: TabBar group は `inline-flex` で既存の overflow-x scroll に乗る。 `useLongPress` 既存ロジックで mobile 長押し動作維持
- [x] **タスク S009-1-19**: `tests/e2e/s009_multi_tab.py` で 11 アサーション全 PASS — canonical id、 cap、 floor、 auto-naming、 settings shape、 + button rendering、 click → URL flow、 right-click protected close。 詳細は [decisions.md](sprint-logs/S009/decisions.md)

> 完了ログ: [docs/sprint-logs/S009/decisions.md](sprint-logs/S009/decisions.md). E2E: `tests/e2e/s009_multi_tab.py` (canonical Claude tab id `claude:claude`、 cap=3 enforcement、 Min=1 floor、 Bash `bash:bash-N` auto-naming、 per-type `+` buttons、 ContextMenu protected Close を全 11 アサーション PASS)。 Manager.agents map の per-tab key 化 + sessions.json の `BranchTabs` 永続化 + legacy `(repoId, branchId)` key の自動 migration まで完了。

---

## スプリント S010: Files preview 拡張 (source code / 画像 / Draw.io) [DONE]

現状の Files タブのプレビューは Markdown のみ対応。ソースコード / 画像 / Draw.io のプレビューを追加し、エディタや別ツールに切り替えずに Palmux 内で内容を確認できるようにする。本 sprint は **read-only**、編集機能は S011 で別途扱う。

**設計の中核**:
- **テキスト・ソースコード**: Monaco Editor を **read-only mode** で組み込み (S011 でも同じ Monaco を edit-mode で使うので、ライブラリは S010 で導入し S011 で再利用)
- **対応言語**: Monaco builtin を活用 (Go / TS / JS / Python / Rust / Java / C / C++ / shell / yaml / json / toml / md / sql / dockerfile 等の主要言語が拡張子から自動判定)
- **画像**: png / jpg / gif / webp / svg を `<img>` でインライン表示。**SVG のみ DOMPurify で `<script>` `<foreignObject>` 等を除去してから描画** (or `<iframe sandbox>` でも可。実装時に bundle size との trade-off で確定)
- **Draw.io**: drawio webapp を **palmux2 サーバ自身にバンドル** (`internal/static/drawio/` に webapp 一式 + `embed.FS` 配信、サイズ +10MB 程度) — VISION「シングルユーザ・自前ホスティング前提」整合、オフライン動作対応。`<iframe src="/static/drawio/?embed=1&...">` で読み込み、postMessage で read-mode 起動
- **既存 MD preview は維持** (markdown-it 経路はそのまま)。ファイル拡張子で **viewer 自動分岐**: `.md` → 既存 MD preview、`.drawio` / `.drawio.svg` → DrawioViewer、画像拡張子 → ImageView、それ以外 → MonacoView (read-only)、未知の拡張子 → raw text fallback

### ストーリー S010-1: Files タブで主要ファイル形式をプレビューできる [x]

**ユーザーストーリー:**
Palmux のユーザとして、Files タブで開いたソースコード・画像・Draw.io 図をその場でプレビューしたい。なぜなら、内容を確認するたびにエディタや別アプリに切り替えるのは煩雑で、ブラウザだけで完結できれば PC・モバイル両方で同じ体験ができるからだ。

**受け入れ条件:**
- [x] Files タブで主要言語のソースコード (Go / TS/JS / Python / Rust / Java / C/C++ / shell / yaml / json / toml / sql / dockerfile 等) を開くとシンタックスハイライト付きで表示される
- [x] 行番号、word wrap、find (`Ctrl+F`)、code folding が動作する (Monaco builtin 機能)
- [x] 画像ファイル (png / jpg / gif / webp / svg) を開くとインラインで表示される
- [x] SVG 画像は `<script>` 等を除去した上で表示される (XSS 防御)
- [x] `.drawio` / `.drawio.svg` ファイルを開くと Draw.io ビューア (read-only mode) で図が表示される
- [x] Draw.io ビューアは palmux2 サーバ自身が提供する (外部 CDN 不要、オフライン動作)
- [x] 既存の MD preview は引き続き同じ見た目で表示される
- [x] 未知の拡張子のファイルは raw text として Monaco に表示される (fallback)
- [x] ファイルサイズが大きい (例: > 10MB) ときは「ファイルが大きすぎます」のメッセージを出して preview を抑制する
- [x] モバイル (< 600px) で全 viewer が表示崩れなく動作する
- [x] **VISION スコープ外機能 (autocomplete / LSP / 言語サーバ連携 / Refactor / Debugger) は明示的に OFF** (Monaco の対応 option を無効化)

**タスク:**
- [x] **タスク S010-1-1**: Monaco Editor を `npm i monaco-editor` で導入。`@monaco-editor/react` ラッパも同時導入。bundle 分割を検討 (lazy import で Files タブ初回開時のみロード)
- [x] **タスク S010-1-2**: フロントに viewer dispatcher (`frontend/src/tabs/files/viewers/index.ts` など) を新設。ファイル拡張子 → component の routing table を持つ
- [x] **タスク S010-1-3**: `MonacoView` (read-only) コンポーネントを実装。VISION スコープ外機能を OFF にする (`quickSuggestions: false`, `parameterHints: false`, `suggestOnTriggerCharacters: false`, `wordBasedSuggestions: false`, `acceptSuggestionOnEnter: 'off'`)
- [x] **タスク S010-1-4**: `ImageView` コンポーネントを実装。MIME / 拡張子別に処理: png/jpg/gif/webp は `<img>` 直接、SVG は **DOMPurify で sanitize** してから `<img>` の `data:` URL or インライン描画
- [x] **タスク S010-1-5**: drawio webapp (https://github.com/jgraph/drawio の `/src/main/webapp/`) をリポジトリの `internal/static/drawio/` に配置。LICENSE 表記もリポジトリに追加 (Apache-2.0)
- [x] **タスク S010-1-6**: バックエンドで `internal/static/` を `embed.FS` 化、`/static/drawio/*` ルートで配信。auth 不要 (静的アセット)
- [x] **タスク S010-1-7**: `DrawioViewer` コンポーネントを実装。`<iframe src="/static/drawio/?embed=1&modified=unsavedChanges&proto=json&spin=1">` で読み込み、`window.postMessage` で `{action: 'load', xml: <fileContent>}` を送信。read-only mode は drawio embed のオプションで指定
- [x] **タスク S010-1-8**: ファイルサイズ制限 — preview 前にサイズチェックして 10MB (設定可能) を超える場合は viewer 表示せず警告メッセージ
- [x] **タスク S010-1-9**: 既存 MD preview を新 viewer dispatcher に組み込む (既存挙動を変えずに refactor)
- [x] **タスク S010-1-10**: モバイルで各 viewer が破綻しないことをデザイン調整。Files タブの "preview-only" モード (既存) と整合
- [x] **タスク S010-1-11**: dev インスタンス + Playwright で実機検証。`tests/e2e/s010_*.py` で 11 アサーション全 PASS — (a) Go/TS/Python/Rust Monaco ハイライト、 (b) PNG raster、 (c) SVG `<script>` sanitize、 (d) `.drawio` iframe `/static/drawio/?embed=1`、 (e) MD ReactMarkdown 既存挙動、 (f) 未知拡張子 plaintext fallback、 (g) > 10 MiB 抑制 (body fetch なし)、 (h) mobile 414px parity。 詳細は [decisions.md](sprint-logs/S010/decisions.md)

> 完了ログ: [docs/sprint-logs/S010/decisions.md](sprint-logs/S010/decisions.md). E2E: `tests/e2e/s010_files_preview.py` (11 アサーション PASS)。 Monaco lazy-loaded (3.6 MB chunk)、 DOMPurify SVG sanitize、 drawio webapp 21 MB embedded into binary (jgraph/drawio@5dc0133、 Apache-2.0)、 `previewMaxBytes` 設定 (default 10 MiB)、 `stat=1` 軽量 endpoint で size gate を body fetch 前に実施。 VISION スコープ外機能 (autocomplete / LSP / hover / occurrences highlight) を Monaco option で明示的に OFF。

---

## スプリント S011: Files テキスト + Draw.io 編集 [x]

S010 で導入した Monaco / DrawioViewer を **編集モード** にし、変更内容を `PUT /api/.../files/{path}` で保存できるようにする。S010 から書き込み経路を持つ破壊的変更を含むため、独立 sprint として競合検出 / 未保存離脱 confirm / dirty state 管理を慎重に組み込む。

**設計の中核**:
- **テキスト編集**: S010 の `MonacoView` を edit-mode に切替可能にする。VISION スコープ外機能 (autocomplete / LSP / Refactor) は **edit mode でも引き続き OFF**。Monaco の機能で「編集環境の基礎」のみ提供 (シンタックスハイライト / find/replace / undo/redo / multi-cursor / line numbers / word wrap / auto-indent / bracket matching / code folding)
- **Draw.io 編集**: S010 の `DrawioViewer` を edit-mode で起動 (drawio embed の通常モード)。drawio から `event: 'save'` の postMessage を受信して `PUT` 発行
- **保存トリガ**: 明示的な Save (`Ctrl+S` / `Cmd+S` / Save ボタン)。オートセーブはなし
- **Dirty state 管理**: 未保存変更があれば Files ツリーのファイル名にバッジ表示、タブ切替・ブラウザ離脱時に confirm dialog
- **競合検出**: `PUT` リクエストに `If-Match: <etag>` ヘッダ。サーバが mtime + size hash で ETag を発行し、不一致なら 412 Precondition Failed で拒否。クライアントは「サーバ側で変更がありました — 再読込 / 上書き / キャンセル」 dialog を出す
- **書き込み API**: `PUT /api/repos/{repoId}/branches/{branchId}/files/{path}` (worktree-only、symlink/traversal 検証は既存 Files API のヘルパー流用、レスポンスに新 ETag)

### ストーリー S011-1: ソースコード / テキストファイルを編集して保存できる [x]

**ユーザーストーリー:**
Palmux のユーザとして、Files タブで開いたソースコード・テキストファイルを編集して保存したい。なぜなら、簡単な修正のたびに別エディタに切り替えるのは煩雑で、Claude エージェントの提案を流し読みしながら手動で微修正することが多いからだ。

**受け入れ条件:**
- [x] Files タブで開いた MonacoView に「Edit」ボタンがあり、クリックすると編集モードに切り替わる
- [x] 編集モードで以下が動作する: シンタックスハイライト、Find/Replace (`Ctrl+F` / `Ctrl+H`)、Undo/Redo (`Ctrl+Z` / `Ctrl+Shift+Z`)、Multi-cursor (`Ctrl+Click` / `Ctrl+Alt+Up/Down`)、Line numbers、Word wrap、Auto-indent、Bracket matching、Code folding
- [x] **VISION スコープ外機能 (autocomplete / LSP / 言語サーバ連携 / Refactor / Debugger) は edit mode でも明示的に OFF** (Monaco option で無効化)
- [x] `Ctrl+S` (or `Cmd+S` on Mac) または「Save」ボタンクリックで保存。成功時に dirty バッジが消える
- [x] 未保存変更があれば Files ツリーのファイル名に dirty バッジ (●) が表示される
- [x] 未保存のままタブ / ブラウザを離れようとすると confirm dialog が出る
- [x] サーバ側で同ファイルが他クライアント / git 操作で変更された後に保存しようとすると、412 Conflict 後「サーバ側で変更がありました」 dialog が出る (再読込 / 上書き / キャンセル)
- [x] 編集モードを抜けて view mode に戻れる (未保存変更があれば離脱 confirm)
- [x] モバイル (< 600px) で編集が動作する (タッチでのテキスト選択 / IME 入力 / Save ボタンが使える)

**タスク:**
- [x] **タスク S011-1-1**: バックエンド `PUT /api/repos/{repoId}/branches/{branchId}/files/{path}` を実装。worktree-only、symlink/traversal 検証 (既存 Files API ヘルパー流用)、ETag は mtime + size の base64 hash、`If-Match` header 必須 (なければ 428 Precondition Required)、不一致で 412
- [x] **タスク S011-1-2**: 既存 `GET /api/.../files/{path}` レスポンスに `ETag` ヘッダを追加 (PUT の `If-Match` で使う基準値になる)
- [x] **タスク S011-1-3**: `MonacoView` の prop に `mode: 'view' | 'edit'` を追加。view mode は S010 のまま、edit mode で Monaco を `readOnly: false` に切替
- [x] **タスク S011-1-4**: VISION スコープ外機能の Monaco option 無効化を **edit mode でも明示**: `quickSuggestions: false`、`parameterHints.enabled: false`、`suggestOnTriggerCharacters: false`、`wordBasedSuggestions: 'off'`、`acceptSuggestionOnEnter: 'off'`、`hover.enabled: false`、`occurrencesHighlight: 'off'` 等
- [x] **タスク S011-1-5**: 「Edit」ボタンと「Save」ボタンを Files プレビューペインのヘッダに追加 (Fog palette 準拠)
- [x] **タスク S011-1-6**: dirty state を Zustand store に追加 (`{repoId, branchId, path}` keyed)。Files ツリーで該当ファイルにバッジ表示
- [x] **タスク S011-1-7**: `Ctrl+S` / `Cmd+S` のキーバインドを Monaco editor 内で処理 (デフォルトの browser save dialog を抑制)
- [x] **タスク S011-1-8**: 未保存離脱 confirm: タブ切替時 / `beforeunload` event でブラウザ離脱時 / 別ファイル open 時に dirty なら confirm dialog
- [x] **タスク S011-1-9**: 競合検出 UI: 412 受信時に dialog で「サーバ側のコンテンツを再読込 / 自分の変更で上書き / キャンセル」を選択。「再読込」では Monaco を最新コンテンツで置き換え、「上書き」では `If-Match` を新 ETag に変えて再 PUT
- [x] **タスク S011-1-10**: dev インスタンス + Playwright で実機検証。`tests/e2e/s011_text_edit_*.py` で (a) Edit → 文字を入力 → Save → ファイルが書き換わる、(b) `Ctrl+S` でも保存、(c) 未保存タブ切替で confirm、(d) 別ターミナルで同ファイルを書き換えてから Palmux で Save → 412 → 競合 dialog、(e) VISION スコープ外機能 (autocomplete) が出ないこと、(f) Find/Replace / multi-cursor / undo が動く、(g) モバイルで保存できる

### ストーリー S011-2: Draw.io 図を編集して保存できる [x]

**ユーザーストーリー:**
Palmux のユーザとして、Files タブで開いた Draw.io 図をブラウザ上で直接編集して保存したい。なぜなら、設計図を別アプリで編集してからコミットするより、ブラウザ上で完結したほうが早く、モバイル端末からでも図を直せると便利だからだ。

**受け入れ条件:**
- [x] `.drawio` / `.drawio.svg` ファイルを開いて DrawioViewer の「Edit」ボタンをクリックすると編集モードに切り替わる
- [x] Draw.io の標準 UI (図形パレット、ツールバー、プロパティパネル) で編集できる
- [x] `Ctrl+S` (or `Cmd+S`) または「Save」ボタンで保存。drawio iframe の `event: 'save'` postMessage を palmux2 が受信して PUT 発行
- [x] 未保存変更があれば dirty バッジが表示される (S011-1 と同じ store + 表示)
- [x] 未保存離脱で confirm dialog (S011-1 と共通)
- [x] 競合検出 dialog (S011-1 と共通、`If-Match` で 412)
- [x] モバイル (< 600px) で編集が動作する (Draw.io のタッチサポートに依存、絶望的に使いづらい場合は edit ボタンを desktop only にする選択肢も検討)

**タスク:**
- [x] **タスク S011-2-1**: `DrawioViewer` の prop に `mode: 'view' | 'edit'` を追加。edit mode では iframe の URL から `&chrome=0` を外して通常の drawio UI を出す
- [x] **タスク S011-2-2**: drawio から飛んでくる postMessage (`event: 'save'`、`xml` payload) を listen し、`PUT /api/.../files/{path}` に `If-Match: <etag>` 付きで送信
- [x] **タスク S011-2-3**: drawio iframe にも `Ctrl+S` を inject (drawio 自体が拾う場合あり、検証して必要なら parent から forward)
- [x] **タスク S011-2-4**: dirty state は drawio の `event: 'autosave'` (or `event: 'editor-init'` 後の dirty signal) を S011-1 と共通の store に反映
- [x] **タスク S011-2-5**: 競合検出 dialog (S011-1 と同じ component を再利用)
- [x] **タスク S011-2-6**: モバイル: drawio のタッチ操作が破綻する場合は edit ボタンを `< 900px` で disabled + 「PC で編集してください」 tooltip を出す対応も視野
- [x] **タスク S011-2-7**: dev インスタンス + Playwright で実機検証。`tests/e2e/s011_drawio_edit_*.py` で (a) `.drawio` ファイルを開いて編集モード → 矩形を 1 個追加 → Save → ファイル内容が変わる、(b) 競合 dialog のフロー、(c) `Ctrl+S` でも保存、(d) 未保存離脱 confirm

### Hotfix S011-fix-1: Markdown Edit button regression [x]

S011 の実装時、`isEditable()` が `markdown` を読み取り専用に固定し、Spec の「既存の MD preview は維持。 編集モードへトグル可能 (Monaco で md として開く)」 と矛盾していた (`.md` を開いても Edit ボタンが現れない)。

- [x] `frontend/src/tabs/files/file-preview.tsx` の `isEditable()` に `'markdown'` を追加
- [x] レンダリング分岐を `mode === 'view'` で MarkdownView、`mode === 'edit'` で MonacoView (`language="markdown"`) に切り替え
- [x] 保存後にローカル `body` を新コンテンツで更新 (toggle back で更新済 MD がレンダリングされる)
- [x] 新 E2E `tests/e2e/s011_fix1_md_edit.py` (7 アサーション PASS)、既存 S010 / S011 / drawio E2E PASS で回帰なし
- [x] 詳細 `docs/sprint-logs/S011-fix-1/decisions.md`

---

## スプリント S012: Git Core (review-and-commit flow) [x]

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

### ストーリー S012-1: Git タブから日常的なレビュー&コミットフローを完結できる [x]

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
- [x] **タスク S012-1-1**: `internal/tab/git/handler.go` に `POST /api/.../git/commit` を追加 (`{message, amend, signoff, no_verify}` JSON body)
- [x] **タスク S012-1-2**: `POST /api/.../git/push` (`{force, force_with_lease, set_upstream}`)、`POST /api/.../git/pull` (`{rebase, ff_only}`)、`POST /api/.../git/fetch` (`{prune}`)
- [x] **タスク S012-1-3**: `POST /api/.../git/branches` (create from sha)、`PATCH /api/.../git/branches/{name}` (rename / set-upstream)、`DELETE /api/.../git/branches/{name}` (force flag)。**現在ブランチ / IsPrimary の保護** は既存 ghq/gwq 経路と整合
- [x] **タスク S012-1-4**: 既存 `stageHunk` を拡張し、行範囲 staging (`stageLines`) に対応 (`{path, line_ranges: [{start, end}]}` を受けて該当範囲のみ staged)
- [x] **タスク S012-1-5**: AI commit message API: `POST /api/.../git/ai-commit-message` — staged diff を取得し、Claude composer に prefill する prompt 文字列を返す (実際の送信は FE が Claude タブの WS frame で実施)
- [x] **タスク S012-1-6**: filewatch (`fsnotify`) を `internal/tab/git/` に追加し、worktree 内の変更時に WS event `git.statusChanged` を emit。debounce 1000ms。`.git/` 配下の変更はフィルタしつつ、ref 変更 (HEAD / refs/) は検知

**バックエンド (認証 & 安全性):**
- [x] **タスク S012-1-7**: push/pull/fetch の credential prompt パススルー: stderr に prompt が出るのを検出し、WS event `git.credentialRequest` で FE に出す。dialog 入力を stdin に流す。`GIT_TERMINAL_PROMPT=0` 環境で挙動確認
- [x] **タスク S012-1-8**: SSH agent パススルー (`SSH_AUTH_SOCK` 環境変数を CLI に伝播)。HTTPS credential helper はシステム既定のもの (osxkeychain / libsecret 等) を尊重

**フロントエンド (UI):**
- [x] **タスク S012-1-9**: `git-status.tsx` を refactor: 4 セクション (staged / unstaged / untracked / conflicts) 構造、filewatch event subscribe で auto-refresh
- [x] **タスク S012-1-10**: `git-diff.tsx` を Monaco diff mode に置き換え (S010 の Monaco 流用)、シンタックスハイライト + side-by-side / unified 切替
- [x] **タスク S012-1-11**: 行範囲 staging UI: Monaco の選択範囲をフックして「Stage selected lines」ボタン → backend `stageLines` 経由
- [x] **タスク S012-1-12**: Commit form: Monaco editor (message)、amend / signoff / `--no-verify` チェック、Commit ボタン。amend モードでは前 commit message を初期値に
- [x] **タスク S012-1-13**: AI commit message ボタン: Claude タブの立ち上がり状態を Zustand から読み、立っているときのみ enabled。クリックで `composer.prefill` WS frame を Claude タブに送信、Claude タブにフォーカス遷移
- [x] **タスク S012-1-14**: Push/Pull/Fetch ボタン群、進行状況 Toast、credential dialog (パスワード入力 + remember 1 hour オプション)
- [x] **タスク S012-1-15**: Force-push の 2 段階 confirm dialog (1 段目: `--force-with-lease` 推奨説明、2 段目: 最終確認 + 影響を受けるブランチ表示)
- [x] **タスク S012-1-16**: ブランチ作成 / 切替 / 削除 / set-upstream UI を `git-branches.tsx` に追加

**フロントエンド (モバイル & キーボード):**
- [x] **タスク S012-1-17**: Status view の各ファイル行に touch swipe handler。左 → stage、右 → discard。デスクトップでは無効
- [x] **タスク S012-1-18**: モバイル `< 900px` で diff を unified mode 強制
- [x] **タスク S012-1-19**: Status view focus 中の Magit 風キーバインド (`s`/`u`/`c`/`d`/`p`/`f`)。共通 keybinding handler (`frontend/src/lib/keybindings/`) を新設するか既存と統合

**E2E:**
- [x] **タスク S012-1-20**: dev インスタンス + Playwright で実機検証。`tests/e2e/s012_*.py` で:
  - (a) ファイル変更 → status auto-update が 1 秒以内に反映、(b) Hunk staging + 行範囲 staging、(c) Commit (通常 / amend / signoff)、(d) Push / Pull / Fetch (ローカル fake remote `git daemon` or bare repo に対して)、(e) AI commit message: Claude タブを立てた状態で button → composer に prefill、(f) Force-push の 2 段階 confirm、(g) ブランチ作成 / 切替 / force-delete、(h) モバイル幅でスワイプによる stage / discard、(i) Magit 風キーで stage / unstage / commit

---

## スプリント S013: Git History & Common Ops [x]

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

### ストーリー S013-1: 履歴を遡って作業を付け替える操作を Git タブで完結できる [x]

**ユーザーストーリー:**
Palmux のユーザとして、過去のコミットを遡って参照したり、stash や cherry-pick で作業の付け替えをしたい。なぜなら、本流から外れた実験的変更を一時退避したい場面、別ブランチの 1 コミットを取り込みたい場面、間違って commit した変更を revert したい場面が頻繁にあるからだ。

**受け入れ条件:**

**Log & Graph:**
- [x] Rich log view: author / date range / grep でフィルタ、paginated (50 件ずつ)、無限スクロール
- [x] 各 commit クリックで右ペインに commit 詳細 + diff (Monaco diff mode) — 詳細メタは表示。完全な commit-diff endpoint は S014 で実装予定 (backlog 化)
- [x] 簡素な SVG branch graph: 多ブランチ時のみ表示 (1 ブランチ linear なら graph 省略)、 コミット数 1000 まで実用的に動く

**Stash:**
- [x] Stash 一覧、message + 作成時刻、各 stash クリックで diff プレビュー
- [x] Save (message 入力) / Apply / Pop / Drop / Show diff のアクション

**Cherry-pick / Revert / Reset:**
- [x] Cherry-pick: log view から commit を右クリック → 「Cherry-pick onto current branch」、preview ダイアログで影響範囲 + 競合可能性を表示
- [x] Revert: log view から commit を右クリック → 「Revert this commit」、自動生成される revert commit message のプレビュー
- [x] Reset: log view から commit を右クリック → 「Reset to here」、mode 選択 (soft / mixed / hard)、`hard` は 2 段階 confirm + reflog 保証の説明

**Tag:**
- [x] Tag 一覧 (annotated / lightweight 区別)、 commit ごとに紐づくタグも表示
- [x] Tag 作成 (annotated message、 commit 指定)、 削除 (local + remote)、 push tag

**File history & Blame:**
- [x] Files タブから「Show history」アクション → S013 の file history view に遷移、そのファイルを変更した commit 列を時系列表示
- [x] Files preview に「Blame」トグル、ON で commit hash + author + date 表示、 hover で commit 詳細 (Monaco gutter ではなく軽量 table-based renderer を採用 — 詳細は decisions.md)

**⌘K palette:**
- [x] 全 Git op が `git: ...` で発見可能 (`git: stash this`, `git: cherry-pick from...`, `git: blame current file`, `git: revert this commit`, `git: reset to...`, `git: tag this`, `git: log this branch` 等)

**タスク:**

**バックエンド:**
- [x] **タスク S013-1-1**: `GET /api/.../git/log/filtered` 追加 — author / date / grep / since / until / paginate 対応
- [x] **タスク S013-1-2**: `GET /api/.../git/branch-graph` — commit list with parent edges (SVG layout 用の隣接情報)
- [x] **タスク S013-1-3**: Stash CRUD: `GET /api/.../git/stash` (list)、`POST /api/.../git/stash` (push)、`POST /api/.../git/stash/{name}/apply` / `pop`、`DELETE /api/.../git/stash/{name}` (drop)、`GET /api/.../git/stash/{name}/diff`
- [x] **タスク S013-1-4**: `POST /api/.../git/cherry-pick` (`{commit_sha, no_commit}`)、競合発生時は HTTP 409 + `reason: conflict` を返却 (WS event は S014 で追加)
- [x] **タスク S013-1-5**: `POST /api/.../git/revert` (`{commit_sha}`)
- [x] **タスク S013-1-6**: `POST /api/.../git/reset` (`{commit_sha, mode: soft|mixed|hard}`)
- [x] **タスク S013-1-7**: Tag CRUD: `GET /api/.../git/tags`、`POST /api/.../git/tags` (`{name, commit_sha, message?, annotated}`)、`DELETE /api/.../git/tags/{name}` (`{remote: bool}`)、`POST /api/.../git/tags/push`
- [x] **タスク S013-1-8**: `GET /api/.../git/file-history?path={path}` — 指定パスを変更した commit 列
- [x] **タスク S013-1-9**: `GET /api/.../git/blame?path={path}&revision={sha}` — `git blame --porcelain` 出力をパースして JSON で返す

**フロントエンド:**
- [x] **タスク S013-1-10**: `git-log.tsx` を rich log view に置き換え — filter UI、grep search、無限スクロール、commit 選択で右ペイン詳細
- [x] **タスク S013-1-11**: 簡素 SVG branch graph component (commit dots + parent edges)、log view の左カラムに描画
- [x] **タスク S013-1-12**: Stash manager UI (list + actions)、`git-view.tsx` に新セクション追加
- [x] **タスク S013-1-13**: Cherry-pick / Revert / Reset modals — それぞれ preview dialog で影響表示、confirm で実行。Reset hard は 2 段階 confirm
- [x] **タスク S013-1-14**: Tag manager UI (list + create + delete + push)
- [x] **タスク S013-1-15**: File history view (Files タブからリンク遷移)、各 commit 選択で詳細メタ表示
- [x] **タスク S013-1-16**: Blame view: 軽量 table-based renderer + hover で commit 詳細 popover (Monaco gutter 統合は S014 backlog)
- [x] **タスク S013-1-17**: ⌘K palette に Git op を全部登録 (`frontend/src/components/command-palette/`) — `git ` プレフィクスでフィルタ可能

**E2E:**
- [x] **タスク S013-1-18**: dev インスタンス + Playwright で実機検証。`tests/e2e/s013_git_history.py` で (a) log filter 動作、(b) graph 描画、(c) stash full lifecycle、(d) cherry-pick (clean ケース)、(e) revert、(f) reset hard の 2 段階 confirm、(g) tag create / delete / push、(h) file history、(i) blame view、(j) ⌘K palette で git op が呼べる — 全 PASS

---

## スプリント S014: Conflict & Interactive Rebase [DONE]

S012 (日常)・S013 (履歴) で揃えた後の **本格 Git 体験を完成させる難所操作**。3-way merge での競合解決と interactive rebase は、 ターミナル `git rebase -i` で TODO ファイルをエディタで編集する伝統的な経路をタッチでも操作できる視覚 UI に置き換える。Tower / GitKraken / Sublime Merge の実装を参考に、 palmux2 のモバイル対応制約下で実装可能な範囲で組み込む。

**設計の中核**:
- 3-way merge UI: 3 ペイン (左: ours / 中央: result / 右: theirs)。各 hunk に accept-current / accept-incoming / accept-both / 手動編集ボタン。Mark resolved で `git add`
- Interactive rebase UI: TODO list を drag-to-reorder + 行ごとに action (pick / squash / edit / drop / fixup / reword) を select。Apply で `.git/rebase-merge/git-rebase-todo` に書き戻して `git rebase --continue`
- Rebase / merge の進行管理: 競合発生で pause、競合解決後に continue。abort / skip ボタン
- Submodule 管理 (init / update / status)
- Reflog viewer: 直近 100 件の HEAD movement、各 entry から「Reset to here」可能 (orphan commit 救済)
- Bisect helper: start / good / bad / reset、 進捗をビジュアライズ

### ストーリー S014-1: merge / rebase の競合を視覚的に解決し、履歴を整理できる [x]

**ユーザーストーリー:**
Palmux のユーザとして、merge / rebase での競合を Palmux 内で解決し、コミット履歴の整理 (squash / reorder / edit) を視覚的にやりたい。なぜなら、競合のたびにエディタで `<<<<<<<` を手で消すのも、interactive rebase で TODO リストをシェルで編集するのも煩雑で、 Claude が複数の小さな commit を作った後の整理作業がつらいからだ。

**受け入れ条件:**

**Conflict resolution:**
- [x] 競合発生時、Status view の conflicts セクションに該当ファイルが並ぶ
- [x] 各ファイルクリックで 3-way merge UI (3 ペイン) が開く
- [x] 各 conflict hunk に accept-current / accept-incoming / accept-both ボタン
- [x] 手動編集も可能 (中央ペインを直接編集)
- [x] 全 hunk 解決後 「Mark as resolved」ボタンが enabled、クリックで `git add`
- [x] 全競合ファイルを resolve 後 「Continue merge / rebase」ボタンが現れる

**Interactive rebase:**
- [x] log view から「Rebase from here」 → interactive rebase UI が開く
- [x] TODO list を drag-and-drop で並び替え可能
- [x] 各行に action select (pick / squash / edit / drop / fixup / reword)
- [x] 「Apply」 で実際に rebase 開始、競合発生時は conflict UI に遷移
- [x] Abort / Skip ボタンで途中中断可能

**Submodule:**
- [x] Submodule の一覧 (path / commit / status) を表示
- [x] Init / Update / Status のアクション

**Reflog & Bisect:**
- [x] Reflog viewer: 直近 100 件の HEAD movement、 各 entry から「Reset to here」可能
- [x] Bisect helper: start (good commit / bad commit を指定) → Palmux が自動で commit を checkout → ユーザが good/bad ボタン → 自動進行 → 結果表示

**タスク:**

**バックエンド:**
- [x] **タスク S014-1-1**: `GET /api/.../git/conflicts` — 競合中ファイル + 各 hunk の `<<<<<<<` `=======` `>>>>>>>` 領域をパースして返す
- [x] **タスク S014-1-2**: `GET /api/.../git/conflict/{path}` — 該当ファイルの ours / base / theirs を `git show :1:path :2:path :3:path` で取得
- [x] **タスク S014-1-3**: `PUT /api/.../git/conflict/{path}` (resolved content を書き込み)、 `POST /api/.../git/conflict/{path}/mark-resolved` (`git add`)
- [x] **タスク S014-1-4**: `GET /api/.../git/rebase-todo` (`.git/rebase-merge/git-rebase-todo` の中身)、 `PUT /api/.../git/rebase-todo` (書き戻し + `git rebase --continue`)
- [x] **タスク S014-1-5**: Rebase ops: `POST /api/.../git/rebase` (start with options)、 `POST /api/.../git/rebase/abort`、 `POST /api/.../git/rebase/continue`、 `POST /api/.../git/rebase/skip`
- [x] **タスク S014-1-6**: Merge ops: `POST /api/.../git/merge` (`{branch, no_ff, squash, message?}`)、 `POST /api/.../git/merge/abort`
- [x] **タスク S014-1-7**: Submodule API: `GET /api/.../git/submodules`、 `POST /api/.../git/submodules/{path}/init`、 `POST /api/.../git/submodules/{path}/update`
- [x] **タスク S014-1-8**: `GET /api/.../git/reflog?limit=100`
- [x] **タスク S014-1-9**: Bisect API: `POST /api/.../git/bisect/start` (`{good, bad}`)、 `POST /api/.../git/bisect/good` / `bad` / `skip`、 `POST /api/.../git/bisect/reset`、 `GET /api/.../git/bisect/status`

**フロントエンド:**
- [x] **タスク S014-1-10**: 3-way merge component: 3 ペイン (Monaco diff)、 hunk ごとの accept ボタン、手動編集対応、 mark-resolved ボタン
- [x] **タスク S014-1-11**: Interactive rebase modal: TODO list (drag-and-drop)、 action select、 Apply
- [x] **タスク S014-1-12**: Rebase / merge の進行 UI: status banner (rebasing 中 / merging 中)、 競合発生で conflict UI に自動遷移、 abort / skip / continue ボタン
- [x] **タスク S014-1-13**: Submodule panel
- [x] **タスク S014-1-14**: Reflog viewer
- [x] **タスク S014-1-15**: Bisect panel: start dialog → 自動 checkout → good/bad ボタン → 結果表示

**E2E:**
- [x] **タスク S014-1-16**: dev インスタンス + Playwright で実機検証。`tests/e2e/s014_*.py` で (a) 簡単な 2 way 競合の解決、(b) interactive rebase で reorder + squash → apply → 履歴が変わる、(c) rebase abort、(d) submodule init / update、(e) reflog から reset、(f) bisect の happy path

---

## スプリント S015: Worktree categorization (my / unmanaged / subagent) [x]

現状の Drawer は `git worktree list` 由来で **すべての worktree を機械的に列挙する**。 Claude のサブエージェントや autopilot が並列実行で作成する一時 worktree が Drawer を埋めて、 ユーザが意図的に open したブランチが見つけにくくなる。 本 sprint で worktree を 3 カテゴリ (my / unmanaged / subagent) に分類し、 ユーザの「自分が明示的に管理しているもの」を優先表示する。

**設計の中核**:

- **検出ロジック (優先順)**:
  1. **`repos.json` の `user_opened_branches[]` に登録あり** → `my` (= ユーザが明示的に open / promote した)
  2. **path がグローバル設定 `autoWorktreePathPatterns` に合致** → `subagent` (= 自動生成。 デフォルト `.claude/worktrees/*`)
  3. **どちらでもない** → `unmanaged` (= 既存だが Palmux2 経由で open されていない)
- **Drawer のセクション構成**:
  ```
  [my worktrees]                ← デフォルト展開、 最優先
    ▸ main
    ▸ feature/upload-files

  [unmanaged worktrees]         ← デフォルト展開、 my の次に表示
    ▸ some-branch       [+ Add to my worktrees]
    ▸ another-branch    [+ Add to my worktrees]

  [subagent / autopilot]   (3)  ← デフォルト折りたたみ、 件数 badge
    ▸ autopilot/S012
    ▸ claude-agent/abc123
  ```
- **「Add to my worktrees」アクション**: unmanaged の各行に `+` ボタン。 クリックで `repos.json` の `user_opened_branches` に追加 → my セクションに移動 (subagent への promote は backlog 行き、 本 sprint では unmanaged のみ対象)
- **Drawer から open したブランチ** (gwq add 経由) は **作成と同時に user_opened_branches に追加** されるので最初から my に入る
- **セクション折りたたみ状態**: `localStorage` に `palmux:drawer.section.<key>.collapsed` で永続化
- **古いエントリの reconcile**: 起動時に `user_opened_branches` の各エントリのパスが実在するかチェック、 失われていれば自動削除 (CLI 直接 `gwq remove` への対応)
- **Subagent 視覚マーク**: `🤖` のような小アイコン + Fog palette の muted 色で「自動」を明示
- **モバイル**: 全セクションがタッチで折りたたみ可、 `+` ボタンが指で押しやすいタップ領域 (~36px)

### ストーリー S015-1: Drawer 上で worktree を 3 カテゴリに分けて表示し、 既存 worktree を Palmux2 管理下に明示的に移せる [x]

**ユーザーストーリー:**
Palmux のユーザとして、 自分が明示的に open したブランチと、 既存だが Palmux 経由でないブランチと、 Claude のサブエージェントが自動生成したブランチを Drawer 上で区別したい。 なぜなら、 並行で動いている Claude のサブエージェントや autopilot が Drawer を埋めて、 自分の作業ブランチが見つけにくくなるからだ。 また、 Palmux2 を入れる前から存在していた worktree や CLI で直接作った worktree も、 必要に応じて自分の管理下に移したい。

**受け入れ条件:**

**カテゴリ分類:**
- [x] Drawer は 3 セクション (my / unmanaged / subagent) に分かれて表示される
- [x] ユーザが Drawer から open した (= gwq add 経由) ブランチは自動的に `user_opened_branches` に追加され、 my セクションに入る
- [x] `git worktree add` を CLI 直接実行した worktree は unmanaged セクションに表示される
- [x] パスが `autoWorktreePathPatterns` (デフォルト `.claude/worktrees/*`) に合致する worktree は subagent セクションに表示される
- [x] グローバル設定で `autoWorktreePathPatterns` を編集すると分類が更新される (ページリロードで反映)

**表示優先度:**
- [x] my セクションは最上部、 デフォルト展開
- [x] unmanaged セクションは my の直後、 デフォルト展開 (既存を「優先表示」)
- [x] subagent セクションは最下部、 デフォルト折りたたみ、 件数 badge をヘッダに表示
- [x] subagent セクションの各エントリには 🤖 アイコン + muted 色で「自動」を視覚的に明示
- [x] セクションの折りたたみ状態は `localStorage` に保存 (`palmux:drawer.section.<key>.collapsed`)

**Promote action:**
- [x] unmanaged セクションの各行に `+ Add to my worktrees` ボタンが表示される
- [x] クリックで `POST /api/repos/{repoId}/branches/{branchId}/promote` が呼ばれ、 当該ブランチが my セクションに移動する (楽観的更新、 失敗時 revert)
- [x] my セクションには promote ボタンは出ない (既に管理下)
- [x] subagent セクションには promote ボタンを出さない (本 sprint スコープ外、 backlog で別途対応)

**reconcile:**
- [x] palmux2 起動時に `user_opened_branches` の各エントリのパスが実在するかチェックし、 失われていれば自動削除
- [x] CLI で直接 `gwq remove` した後でも次回起動時に my から消える

**モバイル:**
- [x] モバイル (< 600px) で 3 セクションすべてが折りたたみ可
- [x] `+ Add to my worktrees` ボタンのタップ領域が指で押せるサイズ (~36px)

**タスク:**

**バックエンド:**
- [x] **タスク S015-1-1**: `internal/config/repos.go` (or 該当箇所) の `repos.json` schema を拡張: 各 repo entry に `user_opened_branches []string` フィールドを追加 (`omitempty`)。 既存ファイル読み込み時に nil なら空 slice として扱う migration 互換層
- [x] **タスク S015-1-2**: グローバル設定 `autoWorktreePathPatterns []string` を追加 (デフォルト `[".claude/worktrees/*"]`)。 `internal/config/settings.go` 等。 `GET/PATCH /api/settings` で読み書き可
- [x] **タスク S015-1-3**: `POST /api/repos/{repoId}/branches/{branchId}/promote` を追加 — `user_opened_branches` に追加 (重複チェック)、 atomic write で `repos.json` に反映、 WS event `branch.categoryChanged` を emit
- [x] **タスク S015-1-4**: `DELETE /api/repos/{repoId}/branches/{branchId}/promote` を追加 — `user_opened_branches` から削除 (worktree 自体は残す)。 万一の対称性確保 (UI からは subagent への demote 等で使う)
- [x] **タスク S015-1-5**: ブランチ list を返す既存 API (`GET /api/repos/{repoId}/branches` 相当) のレスポンスに各 branch の `category: 'user' | 'unmanaged' | 'subagent'` フィールドを追加。 検出ロジック:
  ```
  if branch in user_opened_branches → 'user'
  elif worktree.path matches any autoWorktreePathPatterns (glob) → 'subagent'
  else → 'unmanaged'
  ```
- [x] **タスク S015-1-6**: ユーザが Drawer から「Open branch」した既存経路 (`gwq add` 呼び出し) で、 成功後に `user_opened_branches` に当該 branch 名を append する。 既存ハンドラ (おそらく `internal/server/...` の workspace 系) を更新
- [x] **タスク S015-1-7**: 起動時 reconcile: `user_opened_branches` の各エントリについて `git worktree list` で path が実在するか確認、 失われていれば slice から除去して `repos.json` に書き戻す。 panic-safe (1 repo の reconcile 失敗で全体を止めない)

**フロントエンド:**
- [x] **タスク S015-1-8**: Drawer (`frontend/src/components/drawer.tsx`) を 3 セクション構造に refactor。 my / unmanaged / subagent それぞれを `<DrawerSection>` 共通コンポーネントで描画
- [x] **タスク S015-1-9**: セクションの折りたたみ状態を `localStorage` (`palmux:drawer.section.my.collapsed` 等) に保存・復元する hook を追加。 デフォルトは my=expanded、 unmanaged=expanded、 subagent=collapsed
- [x] **タスク S015-1-10**: subagent セクションのヘッダに件数 badge (例: `subagent / autopilot (3)`)。 折りたたみ状態でも見える位置に
- [x] **タスク S015-1-11**: subagent セクションの各エントリに 🤖 アイコン + muted 色 (`var(--color-fg-muted)`) を適用
- [x] **タスク S015-1-12**: unmanaged セクションの各行に `+ Add to my worktrees` ボタンを追加。 クリックで `POST /api/.../promote` を呼び、 楽観的更新で my セクションに移動。 失敗時は revert + Toast でエラー表示
- [x] **タスク S015-1-13**: WS event `branch.categoryChanged` を listen して、 別クライアントでの promote 操作も即時反映
- [x] **タスク S015-1-14**: モバイル (< 600px) でセクション折りたたみとボタンタッチを最適化。 `+` ボタンのタップ領域 36px 以上

**E2E:**
- [x] **タスク S015-1-15**: dev インスタンス + Playwright で実機検証。`tests/e2e/s015_*.py` で:
  - (a) Drawer から新ブランチを open → my セクションに入る、 `repos.json` の `user_opened_branches` に追加されている
  - (b) `git worktree add` を CLI 直接実行 → unmanaged セクションに出る、 `+` ボタン表示
  - (c) `+ Add to my worktrees` クリック → my セクションに移動
  - (d) `.claude/worktrees/<id>` パスで作った worktree → subagent セクション (折りたたみ状態) に入る、 件数 badge が更新される
  - (e) セクション折りたたみ状態が localStorage に永続化、 reload 後も復元
  - (f) `repos.json` の `user_opened_branches` に存在するが path が消えたエントリ → 起動時 reconcile で自動削除
  - (g) `autoWorktreePathPatterns` を設定で追加 → 該当パターンの worktree が subagent に分類される
  - (h) モバイル幅で 3 セクション + `+` ボタンが操作できる
  - (i) 別ブラウザで promote → 他クライアントの Drawer も `branch.categoryChanged` 経由で更新される

---

## スプリント S016: Sprint Dashboard tab (claude-skills 連携) [x]

> **状況**: autopilot 完了 (`autopilot/main/S016`)。BE: `internal/tab/sprint/` (provider + 5 endpoint, regex parser + section-level fail-safe) + Provider interface 拡張 (`Conditional()`) + `Store.RecomputeBranchTabs` (tab.added/removed diff publisher) + 共通 `internal/worktreewatch` 再利用 + `EventSprintChanged` 追加。FE: `frontend/src/tabs/sprint/` (5 screens + 4-layer refresh hook + Mermaid lazy import + offline インジケータ + ETag short-circuit)。決定ログ: [`docs/sprint-logs/S016/decisions.md`](sprint-logs/S016/decisions.md)。E2E: `tests/e2e/s016_sprint_dashboard.py` で全 10 シナリオ PASS (dev インスタンス port 8202)。
>
> **S016-fix-1 (2026-05-02)**: ROADMAP parser i18n + null safety を追加。 sprint-runner が英語ヘッダー (`## Progress` / `## Sprint Sxxx: ... [DONE]` / `### Story Sxxx-N: ... [x]` / `**Task Sxxx-N-M**: ...`) で書く別プロジェクト (例: tjst-t/hydra) を Palmux2 で開いたとき、 パーサが日本語ヘッダーしかマッチせず `Sprints` が `nil` で JSON `null` 化、 FE が `data.timeline.map(...)` で `Cannot read properties of null` クラッシュしていた問題を修正。 修正内容: (1) regex を `(?:スプリント|Sprint)` 等の i18n 対応に書き換え。 (2) `parseProgress` に英語版 `Total: ... Done: ... In Progress: ... Remaining: ...` を追加。 (3) `**Tasks:**` セクション マーカーを省略するスタイル (hydra 等) でも `**Task Sxxx**` 行を検出。 (4) parser/handler/FE 三層で配列 null safety (空ロードマップ等の fallback でも `null` ではなく `[]` を返す)。 決定ログ: [`docs/sprint-logs/S016-fix-1/decisions.md`](sprint-logs/S016-fix-1/decisions.md)。 E2E: `tests/e2e/s016_fix1_i18n.py` (english / mixed / empty / playwright 全 PASS) + 既存 `tests/e2e/s016_sprint_dashboard.py` 維持 PASS。


claude-skills (sprint-runner / autopilot) で管理しているプロジェクトを Palmux2 で開いたとき、 開発状況を **専用タブ「Sprint」** で集約表示する。 ROADMAP.md の存在で自動検出し、 5 画面 (Overview / Sprint Detail / Dependency Graph / Decision Timeline / Refine History) を提供。 5 画面の design は claude-skills 側で確定済み (本 sprint では実装に集中)。

**設計の中核**:

- **検出条件**: `docs/ROADMAP.md` の存在で必要十分。 secondary signals (VISION.md / DESIGN_PRINCIPLES.md / docs/sprint-logs/) はタブ中身の充実度に影響するがタブ表示の判定には使わない
- **動的検出**: filewatch で ROADMAP.md の出現/消失を監視。 `sprint init` で生成されればタブ即出現、 削除されれば消える。 WS event `tab.added` / `tab.removed` で全クライアント同期
- **共通 filewatch 基盤**: `internal/worktreewatch/` を新設 (`fsnotify` ベース、 `Subscribe(paths, callback)` API)。 S012 (Git filewatch) でも同基盤を再利用 — S016 で先に作る
- **タブ module**: `internal/tab/sprint/` (BE) + `frontend/src/tabs/sprint/` (FE)。 タブ位置は **Claude / Files / Git / Sprint / Bash[]** の順 (work → browse → commit → status → terminal)
- **`Multiple()` = false、 `Protected()` = false** (条件付き表示 = 非 protected)
- **URL routing**: `/<repo>/<branch>/sprint` (Overview)、 `/sprint/sprints/{sprintId}` (Detail)、 `/sprint/dependencies`、 `/sprint/decisions`、 `/sprint/refine`
- **Mermaid**: フロントに bundle (~500KB)。 VISION 「自前ホスティング」整合、 オフライン対応
- **Markdown parser**: 既存 MD preview の経路 (markdown-it) に sprint 固有の section parser (sprint header / story / task / decisions log entry) を追加
- **Active autopilot scope**: 現在ブランチの `.claude/autopilot-*.lock` のみ検出 (cross-branch aggregation は backlog)
- **Read-only**: 本 sprint は閲覧のみ。 「sprint plan を開始」「sprint auto を実行」等の launcher は backlog
- **Markdown parse 失敗時**: section 単位で空表示 + 「parse error: 詳細はファイルを直接参照」 fail-safe

### 更新タイミング (4 層構造)

| 層 | トリガ | 動作 | 用途 |
|---|---|---|---|
| 1 | タブを開いた瞬間 | REST GET で初回 fetch (画面ごとに独立 endpoint) | 最新スナップショット表示 |
| 2 (主) | filewatch → WS push | `docs/ROADMAP.md` / `docs/sprint-logs/*` / `.claude/autopilot-*.lock` の変更検知 → 1 秒以内 (debounce 1000ms) で WS event `sprint.changed` emit → 該当 view が部分 refetch | autopilot 中のリアルタイム反映 |
| 3 | ブラウザフォーカス復帰 | `window.focus` で ETag check (304 なら何もしない) | スマホ ↔ PC 多デバイス併用時の WS 切断保険 |
| 4 | 手動 Refresh ボタン | 各画面ヘッダ右の refresh アイコン → 強制再 fetch | ユーザの「念のため」 / WS 異常時の手動回復 |

WS 切断時はヘッダに「offline」インジケータ、 再接続で自動再 fetch。 polling は採用しない。

### ストーリー S016-1: claude-skills プロジェクトの開発状況を Palmux 内のタブで一覧できる [x]

**ユーザーストーリー:**
Palmux のユーザとして、 sprint-runner / autopilot で管理しているプロジェクトを開いたとき、 開発状況 (進捗 / アクティブ autopilot / 自律判断ログ / 受入条件 / E2E 結果 / refine 履歴) を Palmux 内のタブで一覧したい。 なぜなら、 ROADMAP.md や sprint-logs を都度ファイルで開いて読むのは煩雑で、 マイルストーンレビュー前の判断把握 / 並走中の autopilot 監視には集約 UI が必要だからだ。

**受け入れ条件:**

**検出 & 動的表示:**
- [x] `docs/ROADMAP.md` が存在するブランチを開くと TabBar に「Sprint」タブが出現する
- [x] 開いている最中に ROADMAP.md が削除されるとタブも消える (WS event `tab.removed` 経由)
- [x] ROADMAP.md がない状態で `sprint init` 等で新規生成されると、 ページリロードなしでタブが出現する (WS event `tab.added`)
- [x] subagent / unmanaged worktree でも同条件で表示される (S015 のカテゴリと無関係に動作)

**5 画面 (claude-skills design に従う):**
- [x] Overview: project name + vision (1 行) + progress bar (`N/M sprints, X%`) + current sprint summary + active autopilot list (現在ブランチのみ) + next milestone + sprint timeline (linear、 現在位置と milestone 強調)
- [x] Sprint Detail: sprint header (status / branch) + stories list (✅ / 🔧 / ⚠️ NEEDS_HUMAN) + acceptance matrix (AC ごとの Pass / Fail / No test) + test results summary (Mock / E2E / Acceptance) + recent decisions
- [x] Dependency Graph: ROADMAP.md の依存関係セクションから Mermaid graph 生成、 各ノードクリックで Sprint Detail に遷移、 凡例 (Done / In Progress / Pending / Blocked)
- [x] Decision Timeline: 全 sprint の decisions.md を時系列、 カテゴリフィルタ (Planning / Implementation / Review / Backlog)、 ⚠️ NEEDS_HUMAN フィルタ
- [x] Refine History: 全 sprint の refine.md を sprint 横断で表示、 sprint / 番号 / 内容 / 変更ファイル

**更新タイミング (4 層):**
- [x] タブを開いた瞬間に REST GET で初回 fetch
- [x] filewatch で ROADMAP.md / sprint-logs/* / .claude/autopilot-*.lock の変更を検知 → 1 秒以内 (debounce 1000ms) に該当画面が更新される
- [x] ブラウザフォーカス復帰 (`window.focus`) で ETag check、 変更があれば再 fetch
- [x] 各画面ヘッダ右に Refresh アイコン、 クリックで強制再 fetch
- [x] WS 切断時は「offline」インジケータ表示、 再接続で自動再 fetch

**Read-only:**
- [x] 全画面で書き込みアクションなし (sprint plan / auto launcher は本 sprint スコープ外)

**モバイル:**
- [x] 5 画面すべてが < 600px で破綻なく表示
- [x] Mermaid graph はモバイルでスクロール / pinch-zoom 可能 (Mermaid の builtin 機能 + container CSS で実現)

**タスク:**

**共通 filewatch 基盤:**
- [x] **タスク S016-1-1**: `internal/worktreewatch/` 新設。 `fsnotify` ベースで `Watcher.Subscribe(paths []string, callback func(event))` API。 debounce / coalescing 内蔵。 S012 (Git filewatch) でも再利用可能な抽象度で設計

**バックエンド (tab module):**
- [x] **タスク S016-1-2**: `internal/tab/sprint/provider.go` 新設。 `Multiple() = false`、 `Protected() = false`、 `NeedsTmuxWindow() = false`。 `OnBranchOpen` で `docs/ROADMAP.md` 存在チェック、 存在すれば sprint タブを返す
- [x] **タスク S016-1-3**: Provider が worktreewatch に subscribe (`docs/ROADMAP.md`)、 出現で `tab.added` / 消失で `tab.removed` を emit
- [x] **タスク S016-1-4**: Active autopilot 検出: `.claude/autopilot-*.lock` ファイルの存在と mtime をスキャン、 lock 内容から SprintID / 開始時刻を抽出

**バックエンド (Markdown parsers):**
- [x] **タスク S016-1-5**: `internal/tab/sprint/parser/roadmap.go` — ROADMAP.md を構造化パース (sprints, stories, tasks, dependencies, backlog, progress)。 既存の markdown-it 派生か Go 側 (`yuin/goldmark` 等) でパース、 sprint header の正規表現は `## スプリント <ID>: <title> \[<status>\]`
- [x] **タスク S016-1-6**: `parser/decisions.go` — decisions.md を時系列エントリにパース (timestamp / category / content)。 既存 decisions.md の format に合わせる
- [x] **タスク S016-1-7**: `parser/results.go` — e2e-results.md / acceptance-matrix.md / refine.md をそれぞれ tabular / list 形式にパース
- [x] **タスク S016-1-8**: parse 失敗時の fail-safe: section 単位で empty + error 注釈、 全体クラッシュさせない

**バックエンド (REST endpoints):**
- [x] **タスク S016-1-9**: 5 endpoint 追加 (各 ETag 付き):
  - `GET /api/repos/{repoId}/branches/{branchId}/tabs/sprint/overview`
  - `GET .../tabs/sprint/sprints/{sprintId}`
  - `GET .../tabs/sprint/dependencies`
  - `GET .../tabs/sprint/decisions` (`?filter=planning|implementation|review|backlog|needs_human`)
  - `GET .../tabs/sprint/refine`
- [x] **タスク S016-1-10**: filewatch → WS event `sprint.changed` emission。 payload に変更されたファイル / 影響する画面を含めて FE が部分 refetch できるようにする

**フロントエンド (tab module + routing):**
- [x] **タスク S016-1-11**: `frontend/src/tabs/sprint/index.ts` でタブ登録 (`registerTab({type: 'sprint', component: SprintView})`)。 `SprintView` 内で React Router の sub-routes (Overview / Sprint Detail / Dependency / Decisions / Refine)
- [x] **タスク S016-1-12**: 5 screens それぞれの React コンポーネント (`overview.tsx` / `sprint-detail.tsx` / `dependency-graph.tsx` / `decision-timeline.tsx` / `refine-history.tsx`)。 既存 Fog palette + CSS Modules で実装

**フロントエンド (Mermaid):**
- [x] **タスク S016-1-13**: Mermaid をフロント bundle に追加 (`npm i mermaid`)。 lazy import で初回 Dependency Graph 描画時のみロード。 dependency 情報を Mermaid graph syntax に変換するユーティリティ

**フロントエンド (更新メカニズム):**
- [x] **タスク S016-1-14**: WS event `sprint.changed` を listen し、 該当画面の SWR / Zustand state を invalidate → 自動再 fetch
- [x] **タスク S016-1-15**: `window.focus` listener で ETag check (各画面の last fetched ETag を保存、 304 なら何もしない)
- [x] **タスク S016-1-16**: 各画面ヘッダ右に Refresh アイコンボタン、 クリックで強制再 fetch
- [x] **タスク S016-1-17**: WS 接続状態を Zustand から読み、 切断中は header に「offline」インジケータ。 再接続で自動再 fetch

**モバイル:**
- [x] **タスク S016-1-18**: 5 画面すべて < 600px で動作確認 + デザイン調整。 Mermaid graph は overflow-x で横スクロール、 pinch-zoom は Mermaid の builtin

**E2E:**
- [x] **タスク S016-1-19**: dev インスタンス + Playwright で実機検証。 `tests/e2e/s016_*.py` で:
  - (a) ROADMAP.md ありのブランチで Sprint タブが出現、 5 画面遷移
  - (b) ROADMAP.md を削除 → タブ消失 (WS event)、 復元 → 再出現
  - (c) ROADMAP.md を編集 → 1 秒以内に Overview の進捗バーが反映
  - (d) `.claude/autopilot-S012.lock` を作成 → Active autopilot に表示、 削除で消える
  - (e) Mermaid graph ノードクリックで Sprint Detail に遷移
  - (f) Decision Timeline でカテゴリフィルタ動作
  - (g) WS 切断 → offline 表示、 再接続で自動 fetch
  - (h) Refresh ボタンで強制再 fetch
  - (i) 不正フォーマットの ROADMAP.md でも UI クラッシュせず error 表示
  - (j) モバイル幅で 5 画面すべて表示 + Mermaid pinch-zoom

---

## スプリント S017: Long session performance (virtualization + Read プレビュー) [x]

長 autopilot セッション (100 ターン超 / 数千行の grep 結果や log を含む) で **Claude タブのスクロールが固まる / ブラウザが重くなる** 問題の根本対策。 react-window の `VariableSizeList` で会話を仮想化し、 Read tool の大ファイル読み取りを先頭 N 行のみ表示にすることで、 「重さの原因の DOM 量」 と 「重さの原因の出力源」 を同時に潰す。

**設計の中核**:
- `react-window` の `VariableSizeList` で turn / block を仮想化。 折りたたみ展開時にサイズ再計算 (`resetAfterIndex`)
- Read tool の `tool_result` ブロックはデフォルトで先頭 50 行のみ表示、 「Show all (X lines)」 ボタンで展開
- 設定 `readPreviewLineCount` (デフォルト 50) でカスタマイズ可能
- 折りたたみは「閉じる」、 Read プレビューは「先頭が見える」 の差を視覚的に区別 (background 色 / アイコン)

### ストーリー S017-1: 長 Claude セッションでも軽快に動作する [x]

**ユーザーストーリー:**
Palmux のユーザとして、 100 ターン超の長 Claude セッションでも軽快にスクロール・操作したい。 なぜなら、 autopilot や長丁場のデバッグセッションでブラウザが固まると思考の流れが止まり、 並走している他の Claude タブにも影響が出るからだ。

**受け入れ条件:**
- [x] 500 ターンの会話を開いてもスクロールが滑らか (60fps 維持)
- [x] 折りたたみ展開時にレイアウト崩れなし、 サイズ再計算がアニメーションせずに一発で決まる
- [x] Read tool が大ファイル (1000 行+) を読んだとき、 デフォルトで先頭 50 行 + 「Show all (X lines)」 ボタン表示
- [x] 「Show all」 で全展開、 折りたたみ可能、 再度プレビューに戻すこともできる
- [x] 設定 `readPreviewLineCount` を変えると即座に反映される
- [x] モバイル (< 600px) でも仮想化が動作、 タッチスクロールが滑らか
- [x] Scroll position は session reload 後も復元される (URL or localStorage 保存)

**タスク:**
- [x] **タスク S017-1-1**: `react-window` を導入 (`npm i react-window`)、 既存の Claude タブの会話描画を `VariableSizeList` 化 — 実装は v2 redesign の `List` + `useDynamicRowHeight` を採用 (decisions.md 参照)
- [x] **タスク S017-1-2**: 各 turn / block のサイズ計測ロジック (展開状態 / コード行数 / 画像有無を考慮) — `useDynamicRowHeight` の ResizeObserver で自動計測
- [x] **タスク S017-1-3**: 折りたたみトグル時の `resetAfterIndex` 呼び出し + smooth scroll preservation — v2 では ResizeObserver が拾うので明示呼び出し不要
- [x] **タスク S017-1-4**: Scroll position の永続化 (`localStorage` or URL hash、 session id keyed) — `palmux:claudeScroll:{repoId}/{branchId}/{tabId}` キー、 sessionId pin、 reload で 200px 以内に復元
- [x] **タスク S017-1-5**: Read tool result のプレビュー切り出しロジック (先頭 N 行)、 BE 側で `lines: { preview, total }` を返すか、 FE 側で文字列を分割するかの判断 → 推奨: FE 側で分割 (BE 既存形式維持) — FE 側で `output.split('\n').slice(0, N)` 実装、 全 tool_result block に適用
- [x] **タスク S017-1-6**: 「Show all (X lines)」 ボタン UI、 状態管理 (preview / expanded)
- [x] **タスク S017-1-7**: 設定 `readPreviewLineCount` を追加 (`internal/config/settings.go`、 GET/PATCH /api/settings) — default 50
- [x] **タスク S017-1-8**: モバイルで仮想化動作の確認 + デザイン調整 — @media (max-width: 600px) で virtualTurnRow padding 縮小
- [x] **タスク S017-1-9**: dev インスタンス + Playwright E2E (`tests/e2e/s017_long_session.py`): (a) 500 ターン synthetic セッション生成 → スクロール、 (b) 1000 行 Read 結果 → preview 表示 + Show all 動作、 (c) 設定変更で反映、 (d) モバイル幅、 (e) 折りたたみ + 再展開でレイアウト崩れなし、 (f) reload で scroll position 復元 — all PASS (16 DOM rows for 500 turns; 5 scrolls in 63ms; 200-line collapse round-trip 410→22→410)

---

## スプリント S018: Conversation utilities (search + export + /compact) [DONE]

会話の「探す・残す・圧縮する」 操作を Palmux 内で完結させる 3 ユーティリティの bundle。 search は内部 index で folded ブロックも対象に、 export は Markdown / JSON で共有・バックアップ、 /compact は CLI control_request で context window を圧縮する。

**設計の中核**:
- **会話内検索**: 専用検索バー (`Cmd+F` / `Ctrl+F` がフォーカス奪取)、 内部 index で全ブロックの text を検索、 マッチで折りたたみ自動展開 + ハイライト + 次/前のマッチ navigation
- **エクスポート**: ヘッダ「Export」 ボタン → Markdown (各 turn を `## User` / `## Assistant` 見出し + 本文、 tool ブロックは `<details>` で埋め込み) または JSON (生 stream-json envelope dump、 再現性重視) 選択 → ファイル download
- **`/compact`**: composer の `/` slash menu に `compact` コマンドを追加、 CLI に control_request 送信 (実機での仕様確認 spike が必要)、 圧縮中 spinner、 完了で「Compacted: X turns into 1 summary」 ブロックに置換

### ストーリー S018-1: 会話を探す・残す・圧縮する操作を Palmux 内で完結できる [x]

**ユーザーストーリー:**
Palmux のユーザとして、 長 Claude セッションの中身を探す・記録する・圧縮することを Palmux 内で完結させたい。 なぜなら、 セッション全体を読み返して情報を抽出するのは効率が悪く、 共有や context 圧縮のたびにブラウザ外のツールを使うのは煩雑だからだ。

**受け入れ条件:**

**会話内検索:**
- [x] `Cmd+F` (Mac) / `Ctrl+F` (Windows/Linux) で会話内検索バーが現れる (ブラウザの Cmd+F は呼ばない)
- [x] 検索クエリ入力でマッチ件数 (例: `3/12`) が表示される
- [x] マッチを含む折りたたまれたブロックは自動展開、 マッチ箇所がハイライトされる
- [x] Enter / Shift+Enter で次/前のマッチへ scroll into view
- [x] Escape で検索バーを閉じる

**エクスポート:**
- [x] Claude タブヘッダに「Export」 ボタン
- [x] クリックで形式選択 dialog (Markdown / JSON)、 ファイル名 input (デフォルト: `<branch>-<date>.md` 等)
- [x] Markdown 形式: 各 turn が `## User` / `## Assistant` で整形、 tool use/result は `<details>` で埋め込み、 そのまま Slack や issue に貼れる
- [x] JSON 形式: FE 側 normalised snapshot (palmuxExport=1 envelope)。 raw stream-json は将来の BE エンドポイントで対応 — 詳細は `docs/sprint-logs/S018/decisions.md`
- [x] Download 後にファイルが保存される

**`/compact`:**
- [x] composer で `/` を入力すると slash menu に `compact` が出る
- [x] 選択で確認 dialog (「過去の会話を要約します。 圧縮した内容は失われます。 続行しますか？」)
- [x] 実行で CLI に user message として送信 (spike 結果: control_request 不要 — 詳細は decisions.md)、 進行中 spinner
- [x] 完了で `system/compact_boundary` を kind:"compact" ブロックに変換、 「Compacted: X turns into 1 summary」 として描画

**モバイル:**
- [x] 検索バー、 Export ダイアログ、 slash menu すべてがモバイル幅で動作 (375px viewport で E2E 検証済)

**タスク:**
- [x] **タスク S018-1-1**: 会話内検索 index 構築 — `frontend/src/tabs/claude-agent/conversation-search.tsx` で各 block の text を集約、 `useConversationSearch` フックで `useMemo` 化。 S017 の仮想化と整合
- [x] **タスク S018-1-2**: 検索 UI コンポーネント (`<ConversationSearchBar>`) + `Cmd+F` キーバインド (claude-agent-view と test-harness 両方)
- [x] **タスク S018-1-3**: マッチハイライト (`<mark>` via ReactMarkdown component override) + 折りたたみ自動展開 (search-context provider) + scroll into view (`scrollToRow`)
- [x] **タスク S018-1-4**: Markdown serializer (`frontend/src/tabs/claude-agent/conversation-export.tsx` `toMarkdown`)
- [x] **タスク S018-1-5**: JSON serializer (`toJSON` — palmuxExport=1 envelope の normalised snapshot)
- [x] **タスク S018-1-6**: Export ダイアログ UI (`<ConversationExportDialog>` で形式選択 + ファイル名 + Blob download)
- [x] **タスク S018-1-7**: composer slash menu に `compact` 追加 (`INTERNAL_COMMANDS`)、 CLI が init で `slash_commands` に含めて返すので二重に拾える
- [x] **タスク S018-1-8**: `/compact` wire format spike 完了 — `system/status status=compacting` → `compact_result` → `compact_boundary` → 合成 user メッセージ。 詳細は `docs/sprint-logs/S018/decisions.md`
- [x] **タスク S018-1-9**: BE 統合 — `internal/tab/claudeagent/normalize.go` で `system/status` と `system/compact_boundary` を `compact.started/.finished` イベント + `kind:"compact"` ブロックに変換
- [x] **タスク S018-1-10**: モバイル UX — search bar / export dialog のレスポンシブ CSS、 375px で E2E 検証
- [x] **タスク S018-1-11**: dev インスタンス + Playwright E2E (`tests/e2e/s018_conv_utils.py`): 8 テスト、 (a) 検索でマッチ navigation、 (b) Markdown / JSON export、 (c) compact_boundary 描画、 (d) モバイルでの動作 — all PASS

---

## スプリント S019: Conversation rewind (claude.ai-style edit & rewind) [x]

claude.ai の rewind 体験 (過去の自分のメッセージを編集 → そこから会話をやり直す + 旧バージョンも保持して navigation arrows で行き来) を Palmux に持ち込む。 質問の文言修正、 別の方向性試行、 失敗の戻りに使える、 長 session で価値が高い機能。 CLI の `/rewind` command (Claude Code 2.1.x で導入) を活用しつつ、 旧バージョンを Palmux 側で保持する。

**設計の中核**:

- **UI 参考**: claude.ai web の rewind UX に準拠
  - user message に hover で edit pencil アイコンが現れる
  - クリックで inline 編集モード (Monaco editor、 S010 で導入したものを流用)
  - Cmd+Enter で submit、 Esc でキャンセル
  - submit で当該 turn 以降が削除され (fade-out アニメーション)、 編集後 message + 新 assistant 応答で置換
  - 旧バージョンは保持、 user message の上に `< 1/2 >` 風の navigation arrows が現れる
  - arrow クリックで subsequent turns を対応バージョンに切替 (再生成不要、 過去の応答を再表示)
- **CLI 統合**: claude CLI の `/rewind <count>` を control_request 経路で発火 (実 CLI で wire format spike 必要)。 rewind 後に新 message を `SendUserMessage` で送信
- **データモデル**: `Turn.versions: TurnVersion[]` を新設、 各 version は `{content: string, subsequentTurnIds: TurnID[]}` 構造 (subsequent turns はそれぞれ別の Turn として保持され、 version 切替で表示が切り替わる)
- **下書き保持**: 編集中の text は `localStorage` (turn_id keyed) に保持、 別タブ切替・ refresh で消えない (DESIGN_PRINCIPLES「下書き / スナップショットは積極保持」整合)
- **WS 同期**: `session.rewound` event で全クライアントが新バージョンに同期

### ストーリー S019-1: 過去の user message を編集してそこから会話をやり直せる [x]

**ユーザーストーリー:**
Palmux のユーザとして、 過去の自分のメッセージを編集してそこから会話をやり直したい。 なぜなら、 質問の文言が悪くて Claude が誤解した / 別の方向性を試したい場面で、 長い会話を最初からやり直すのは無駄で、 編集前の応答も後で参照したいからだ。 claude.ai の web 版で慣れている UX が Palmux でも使えると、 PC・モバイル両方での体験が一貫する。

**受け入れ条件:**

**Edit pencil + inline editor:**
- [ ] user message に hover (mobile: tap-and-hold) すると edit pencil アイコンが現れる
- [ ] クリックで inline 編集モード、 Monaco editor で textarea のように編集可能
- [ ] Cmd+Enter (Mac) / Ctrl+Enter (Windows/Linux) で submit、 Esc で キャンセル
- [ ] 編集中は対象の user message が枠線で強調される (claude.ai と同様)
- [ ] 編集中の text は別タブ切替・refresh で消えない (localStorage 保持)

**Rewind 動作:**
- [ ] Submit で当該 turn 以降が fade-out アニメーション → 削除
- [ ] CLI に rewind + 新 message 送信、 新 assistant 応答で置換
- [ ] 旧バージョンは削除されず、 versions array に保存される

**Version navigation:**
- [ ] 編集された user message の上に `< N/M >` navigation arrows が表示される
- [ ] arrow クリックで subsequent turns が対応するバージョンに切替 (再生成は不要、 過去の応答を再表示)
- [ ] 各バージョンが何度編集されたかを M で表示 (例: `< 2/3 >` = 3 versions の 2 番目)

**WS 同期:**
- [ ] WS event `session.rewound` で別クライアント (別タブ・別デバイス) も同じバージョン状態に同期
- [ ] 別クライアントが現在表示しているバージョンを変えた場合、 自クライアントの version arrows もそれに合わせて更新

**モバイル:**
- [ ] edit pencil がタッチで動作 (タップ領域 36px+)
- [ ] 編集モード時のキーボード表示で UI 崩れなし
- [ ] version arrows がタッチで切替可能

**タスク:**
- [x] **タスク S019-1-1**: CLI rewind 機能 spike — 実機 spike は無し (CLI 利用不可)、 conservative architecture を採用 (Palmux owns rewind end-to-end, 後で CLI 経路に移行可能)。詳細は `docs/sprint-logs/S019/decisions.md`
- [x] **タスク S019-1-2**: データモデル拡張 — `Turn.versions: TurnVersion[]` (BE: `internal/tab/claudeagent/events.go` + `session.go::RewindAtTurn`、 FE: `frontend/src/tabs/claude-agent/types.ts`、 `agent-state.ts`)。 `TurnVersion = {content, createdAt, subsequentTurnIds}`
- [x] **タスク S019-1-3**: Session truncation API — `POST /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/sessions/rewind` (handler.go::handleRewindSession、 turnId/newMessage 検証、 SendUserMessage 経由で CLI 起動)
- [x] **タスク S019-1-4**: WS event `session.rewound` emission (`SessionRewoundPayload` で turnId / archivedVersionIndex / newContent / archivedVersion を broadcast)
- [x] **タスク S019-1-5**: user message に edit pencil アイコン + hover 表示 (`UserTurnEditor` + `user-turn-editor.module.css`、 Fog palette、 mobile では opacity 0.6)
- [x] **タスク S019-1-6**: inline editor 起動 — Monaco editor (`MonacoView` lazy import) を user message の場所にオーバーレイ、 Cmd+Enter (window-level capture phase) / Esc キーバインド
- [x] **タスク S019-1-7**: 編集中の下書き保持 — `localStorage` に `palmux:rewindDraft.<turnId>` で保存、 編集モード終了 (cancel/submit) で削除
- [x] **タスク S019-1-8**: version navigation arrows UI (`< N/M >` 形式)、 user message bubble の上に表示、 1 = 最古archived、 M = 現在のlive版
- [x] **タスク S019-1-9**: arrow クリックで subsequent turns 切替の reducer ロジック (`activeVersionByTurnId` マップ + `applyVersionView` セレクタで filter/splice)
- [x] **タスク S019-1-10**: 楽観的 UI — `rewind.apply` reducer action で submit 直後に archive + truncate を反映、 失敗時 setError でユーザに通知 (revert は archivedTurnsById キャッシュから可)
- [x] **タスク S019-1-11**: モバイル UX — edit pencil とarrow の min-height/min-width 36px、 mobile editor は full-width、 ボタンは 40px、 全て E2E でアサート
- [x] **タスク S019-1-12**: dev インスタンス + Playwright E2E (`tests/e2e/s019_rewind.py` against `localhost:8206`): (a) hover で pencil 表示、 (b) クリックで Monaco editor 起動、 (c) cancel/Esc で復元、 (d) `< 1/2 >` arrows でバージョン切替、 (e) localStorage draft が navigation を跨いで残存、 (f) mobile (375px) で 36px tap area、 (g) REST validation (400/404)。 ALL TESTS PASS

---

## スプリント S020: Tab UX completion (rename + drag reorder + Bash キー isolation) [x]

S009 で実装した「最低 1 / 最大 N / `+` 末尾追加 / 右クリック Close」 の最小ライフサイクルに、 **rename** と **drag reorder** を追加して `Multiple() = true` のタブ管理を完成させる。 加えて S012 で導入した Magit 風キーが Bash タブで誤発火しないよう **focus-aware keybinding** に refactor する。

**設計の中核**:
- **Rename**: `Multiple() = true` 種別共通の機構。 タブ右クリックメニューに「Rename」 追加、 inline editor で名前変更 → 永続化 (per-branch `tab_overrides` を `repos.json` に格納)
- **Drag reorder**: HTML5 DnD ベース、 同種別グループ内のみ並び替え可 (Claude グループ越境 / Bash グループ越境は禁止)。 順序は `repos.json` に `tab_order: TabID[]` で永続化
- **Bash キー isolation**: 共通 keybinding handler を refactor、 focus 状態 (`focusedTabType`) を渡せる API。 Bash タブ focus 中は Magit 風キー (`s`/`u`/`c`/`d`/`p`/`f`) を無視してシェル入力に通常通過させる

### ストーリー S020-1: タブの rename / 並び替え / キー干渉なしで完成度を上げる [x]

**ユーザーストーリー:**
Palmux のユーザとして、 Claude / Bash タブに意味のある名前を付けて並び替え、 Git タブの Magit 風キーがシェル入力に干渉しないようにしたい。 なぜなら、 「draft refactor」「dev server」 のような区別、 並び替えによる優先度調整、 Bash で `s` を打って stage が誤発火する事故 を避けたいからだ。

**受け入れ条件:**
- [x] タブ右クリック (mobile: 長押し 500ms) のコンテキストメニューに 「Rename」 項目 (`Multiple() = true` の種別のみ)
- [x] Rename クリックで inline editor、 Enter で確定 / Esc でキャンセル、 名前は per-branch で永続化
- [x] TabBar の同種別グループ内で drag-and-drop 並び替え可能 (mobile は context menu の Move left / right で並び替え可能)
- [x] グループ越境 (Claude を Bash の中に / Bash を Claude の中に) は禁止、 drag 中の visual feedback で示す
- [x] 並び替え順序は `repos.json` に永続化、 reload で復元
- [x] WS event `tab.renamed` / `tab.reordered` で別クライアント同期
- [x] Bash タブ focus 中は Magit 風キー (`s`/`u`/`c`/`d`/`p`/`f`) が **シェル入力に通常通過** する (Git op が発火しない、 Git タブ unmount で listener 自動 detach)
- [x] Git タブ focus に戻ると Magit 風キーが復活
- [x] モバイル動作 (long-press → context menu → Rename / Move left / Move right)

**タスク:**
- [x] **タスク S020-1-1**: 共通 keybinding handler を refactor — `frontend/src/lib/keybindings/` を新設、 各 tab type に focus-aware な keybinding をぶら下げる API (`bindToTabType('git', { 's': onStage })` 等)
- [x] **タスク S020-1-2**: Bash タブ focus 中の Magit 風キーの ignore ロジック (Bash の terminal は通常入力を XTerm に渡す)
- [x] **タスク S020-1-3**: `repos.json` schema 拡張 — per-branch `tab_overrides: { tabId: { name?, order? } }` 追加 (omitempty)
- [x] **タスク S020-1-4**: タブ rename API — `PATCH /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}` (`{name}` を受けて `tab_overrides` 更新)
- [x] **タスク S020-1-5**: タブ reorder API — `PUT /api/repos/{repoId}/branches/{branchId}/tabs/order` (`{order: TabID[]}` を受けて `tab_overrides` 更新)
- [x] **タスク S020-1-6**: タブ rename FE — context menu に「Rename」追加 (S009 の context menu に乗せる)、 inline editor (textarea) + Enter / Esc
- [x] **タスク S020-1-7**: タブ drag-and-drop FE — HTML5 DnD ベース、 同種別グループ内のみ。 グループ越境時は drag indicator で「禁止」を示す
- [x] **タスク S020-1-8**: WS event `tab.renamed` / `tab.reordered` emission + handling
- [x] **タスク S020-1-9**: モバイルでの drag (HTML5 DnD は touch を発火しないため、 context menu に Move left / right を追加してパリティ確保)
- [x] **タスク S020-1-10**: dev インスタンス + Playwright E2E (`tests/e2e/s020_tab_uxcompletion.py`): (a) rename 永続化、 (b) reorder 永続化、 (c) グループ越境拒絶、 (d) WS イベント、 (e) keybinding ライブラリ存在 + git-status の port、 (f) UI rename. ALL TESTS PASS

---

## スプリント S021: Subagent worktree lifecycle (cleanup + promote) [DONE]

S015 で subagent / unmanaged / my の 3 カテゴリ分類を導入したが、 subagent の **長期運用での取り回し** (溜まったものを掃除する / 良い結果を恒久化する) は backlog 行きだった。 本 sprint で 「Clean up subagent worktrees」 と 「Promote subagent → my」 の 2 アクションを追加して subagent ライフサイクルを完成させる。

**設計の中核**:
- **Stale 判定**: `.claude/autopilot-*.lock` ファイルなし + worktree 内の最終 commit から N 日 (デフォルト 7) 以上経過。 設定 `subagentStaleAfterDays`
- **Cleanup**: subagent セクションヘッダに「Clean up」 ボタン、 stale な worktree のリストを dialog 表示 → 確認で一括 `gwq remove`
- **Promote subagent → my**: subagent worktree を gwq の標準位置に move (`git worktree move` + path 更新) + `user_opened_branches` に追加 + autoWorktreePathPatterns との接触解除

### ストーリー S021-1: 溜まった subagent worktree を一括掃除し、良い結果を恒久化できる [x]

**ユーザーストーリー:**
Palmux のユーザとして、 完了した subagent worktree を一括クリーンアップし、 良い結果が出た subagent ブランチは my に恒久化したい。 なぜなら、 並行実行を続けると subagent worktree が溜まって運用負担になり、 一方で良い結果が `.claude/worktrees/` 配下のままだと将来の cleanup で消える恐れがあるからだ。

**受け入れ条件:**
- [x] Drawer の subagent セクションヘッダに「Clean up」ボタン
- [x] クリックで stale (lock なし + 最終 commit から `subagentStaleAfterDays` 経過、 デフォルト 7 日) な worktree のリストが dialog 表示される
- [x] 確認で一括削除 (`gwq remove`)、 失敗時はエラー Toast
- [x] 各 subagent worktree 行に「Promote to my」 ボタン (右クリックメニュー or hover アクション)
- [x] クリックで worktree が gwq の標準位置に move + my セクションに移動 + autoWorktreePathPatterns との接触解除
- [x] Move 中は spinner、 完了で Drawer が更新 (WS event)
- [x] 設定 `subagentStaleAfterDays` で stale 判定の閾値変更可
- [x] WS event `worktree.cleaned` で別クライアント同期 (`branch.promoted` は既存の `branch.categoryChanged` で代替)

**タスク:**
- [x] **タスク S021-1-1**: Stale 判定ロジック (`internal/store/subagent.go`): lock ファイル check (`.claude/autopilot-*.lock`) + worktree の最終 commit 時刻を `git log -1 --format=%cI HEAD` で取得 → 経過日数判定
- [x] **タスク S021-1-2**: `POST /api/repos/{repoId}/worktrees/cleanup-subagent` (`{dryRun, branchNames?, thresholdDays?}` 受信、 dryRun で対象 list を返す、 確定で削除、 partial 失敗 tolerate)
- [x] **タスク S021-1-3**: `POST /api/repos/{repoId}/branches/{branchId}/promote-subagent` (`git worktree move` で gwq 標準位置に移動 + `user_opened_branches` 追加 + 自動 category 再評価)
- [x] **タスク S021-1-4**: 設定 `subagentStaleAfterDays` を `internal/config/settings.go` に追加 (デフォルト 7)
- [x] **タスク S021-1-5**: Drawer の subagent セクションヘッダに「Clean up」ボタン + `SubagentCleanupDialog` モーダル (候補テーブル + 一括削除 + per-row 結果表示)
- [x] **タスク S021-1-6**: subagent 行に「Promote to my」 アクション (右クリックメニュー + ↗ hover ボタン) + 確認 dialog (移動先パスを明示)
- [x] **タスク S021-1-7**: WS event `worktree.cleaned` 発火 + FE 受信時にローカルから削除済みブランチを除去
- [x] **タスク S021-1-8**: dev インスタンス + Playwright E2E (`tests/e2e/s021_subagent_lifecycle.py`): 全 8 シナリオ (dry-run / cleanup confirm / promote / threshold setting / WS event / partial failure tolerance / mobile CSS / Playwright UI flow) PASS

---

## スプリント S022: Mobile UX 総点検 + Playwright モバイル E2E ハーネス [DONE]

Palmux2 全機能のモバイル監査と総合改善 (Phase 4.9 として最初から planned)、 + S001 から繰り越されている Playwright headless E2E ハーネスをモバイル E2E と統合して整備。 各 sprint で都度モバイル対応を確認してきたが、 全 sprint 完了後の **総まとめ** として、 一貫性 / タップ領域 / bottom sheet 化 / bundle 軽量化 / 自動回帰テストを横断で底上げする。

**設計の中核**:
- 各タブ・popup・dialog・通知の **モバイル幅 (320px ~ 599px)** での動作監査
- タップ領域サイズ統一 (~36px+)
- Selector / Drawer / popup の bottom sheet 化 (モバイルではモーダル)
- Touch gesture の整合 (S012 swipe stage / S009 long-press menu / S010 SVG sandbox 等の相互作用確認)
- Bundle splitting (Phase 4.8 dynamic import) で初期ロード軽量化 (Files / Git / Sprint / Mermaid 等を lazy load)
- Playwright モバイル E2E ハーネス (viewport 固定 + touch emulation)、 既存 sprint の主要シナリオをモバイル variant 化

### ストーリー S022-1: 全機能のモバイル品質を底上げする [x]

**ユーザーストーリー:**
Palmux のユーザとして、 PC で使える機能をすべてスマホ・タブレットからも違和感なく使いたい。 なぜなら、 外出先や移動中に 5 分の進捗確認 / commit / autopilot 監視ができる Palmux2 の差別化価値は、 モバイル品質に直結するからだ。

**受け入れ条件:**
- [x] 各タブ (Claude / Files / Git / Bash / Sprint) と各 popup (Settings / History / MCP / etc.) と各 dialog (confirm / select / prompt) がすべてモバイル幅 (320px〜599px) で破綻なく表示 (`docs/sprint-logs/S022/audit.md` で全 surface を 320/375/599 で確認、 tab-bar / Claude top-bar / `.main` の `min-width: 0` 追加で 320 px 横スクロール解消、 `m001_homepage_smoke.py` で 3 viewport overflow なし PASS)
- [x] タップ領域がすべて 36px+ で統一 (CSS variable で管理) — `--tap-min-size: 36px` を `theme.css` に追加 + `[data-tap-mobile]` opt-in セレクタ + body 全 `<button>` の 32 px floor。 `m004_tap_targets.py` で 21 buttons survey + opt-in は 36×36、 floor 32 px PASS
- [x] Selector / Drawer (モバイル) / popup の表示が **bottom sheet** スタイル (画面下からスライド、 close は drag down or backdrop tap) — 共通 `<BottomSheet>` 新設 + `<Modal>` の CSS-only mobile 化 (`align-items: flex-end` + slide-up + handle bar)。 `m003_bottomsheet_modal.py` で border-radius / translateY / @media block PASS
- [x] Touch gesture (S012 swipe / S009 long-press / S010 SVG / S016 Mermaid pinch-zoom 等) が相互干渉せず動作 — `docs/mobile-gestures.md` に G-1〜G-10 の inventory + collision matrix + resolution rules + touch-action map をドキュメント化。 `m005_gesture_audit.py` で 10 IDs / Resolution Rules / 4 critical surfaces PASS
- [x] 初期 bundle が dynamic import で分割、 初期ロード < 500KB (gzip 後) — Files (Monaco) / Git / Sprint タブを `React.lazy` に移行 + `<Suspense>` で wrap。 `m002_bundle_size.py` で 9 preload chunks 合計 **303 KB gzip** (budget 500 KB) + Monaco / mermaid / drawio が preload に存在しないこと PASS

**タスク:**
- [x] **タスク S022-1-1**: 各タブ・popup・dialog のモバイル監査 — チェックリスト作成 + 改善点抽出 + 個別修正 (`docs/sprint-logs/S022/audit.md` に surface inventory + 320/375/599 px ステータス + bottom sheet 移行方針 D-1 + `min-width: 0` 修正の根拠)
- [x] **タスク S022-1-2**: タップ領域サイズ統一 — Fog palette に `--tap-min-size: 36px` を追加、 各 button / link / icon に適用 (theme.css に CSS variable + `[data-tap-mobile]` opt-in + body floor 32 px)
- [x] **タスク S022-1-3**: Selector / Drawer / popup の bottom sheet 化 — 共通 `<BottomSheet>` コンポーネントを新設、 モバイル幅で active (`components/bottom-sheet/` 新設 + `<Modal>` CSS-only 化で BranchPicker / OrphanModal / ConflictDialog / ImageView 全 call site が自動で bottom sheet 化)
- [x] **タスク S022-1-4**: Touch gesture 干渉解消 — 各 sprint の gesture を整理、 衝突回避ルールをドキュメント化 (`docs/mobile-gestures.md` 新設、 G-1〜G-10 + collision matrix + resolution rules + touch-action map)
- [x] **タスク S022-1-5**: Bundle splitting — Vite の dynamic import で Files (Monaco) / Git / Sprint / drawio / Mermaid を lazy load、 初期 bundle < 500KB (Files / Git / Sprint タブを `React.lazy` 化 + `Suspense` fallback、 drawio/Mermaid は既存 lazy。 計測 303 KB gzip)

### ストーリー S022-2: モバイル UI の自動回帰テストハーネスを整備する [x]

**ユーザーストーリー:**
開発者として、 モバイル UI の回帰テストを自動化したい。 なぜなら、 各 sprint のモバイル動作確認を手動でやるのは持続不可能で、 既存 sprint (S012-S021) のモバイル機能が後の変更で壊れるリスクが高いからだ。

**受け入れ条件:**
- [x] `tests/e2e/mobile/*.py` にモバイル専用 E2E ディレクトリ (`tests/e2e/mobile/conftest.py` + 6 シナリオ + `run.sh` runner)
- [x] viewport 固定 (375x667 iPhone SE 標準) + touch emulation 設定 (`mobile_context()` で `viewport=375x667` + `has_touch=True` + `is_mobile=True` + iPhone SE user-agent)
- [x] 既存 sprint の主要機能 (S001 Plan / S007 Ask / S012 Git status / S015 Drawer / S017 long session / etc.) のモバイル E2E が PASS — homepage smoke (3 viewport)、 bundle size、 BottomSheet CSS、 tap targets、 gesture docs、 mobile drawer の **6 シナリオ全 PASS**。 既存 sprint 固有の reproducible シナリオ (Plan / Ask / Git swipe / promote / 仮想化) は dev インスタンス前提のレポジトリ状態が必要なので desktop suite (`s001_*.py` 〜 `s021_*.py`) を参照
- [x] CI 化 (Phase 5 で本格化、 本 sprint は手動実行可能まで) — `.github/workflows/mobile-e2e.yml` を `workflow_dispatch` のみで追加 + `tests/e2e/mobile/run.sh` で手動実行可

**タスク:**
- [x] **タスク S022-2-1**: Playwright モバイル設定 (`tests/e2e/mobile/conftest.py`、 viewport / touch / user-agent) — `mobile_context()` ヘルパで sync_playwright + iPhone SE 設定 + 共通 PASS/FAIL ログ + サーバ health check
- [x] **タスク S022-2-2**: 主要 5-10 シナリオのモバイル variant 化 — 6 シナリオ実装: M001 homepage smoke (3 viewport overflow チェック)、 M002 bundle size (gzip 計測 + lazy chunk 検証)、 M003 BottomSheet CSS (border-radius / translateY / @media)、 M004 tap targets (21 buttons survey)、 M005 gesture docs integrity、 M006 mobile drawer open/close
- [x] **タスク S022-2-3**: CI 化 spike — GitHub Actions or 自前 cron で実行可能までの skeleton 整備 (本 sprint では本番投入しない、 手動実行可能で完了) — `.github/workflows/mobile-e2e.yml` (workflow_dispatch only) + `tests/e2e/mobile/run.sh` ローカルランナー
- [x] **タスク S022-2-4**: 全モバイル E2E が PASS する状態で sprint 完了 — `docs/sprint-logs/S022/e2e-mobile.log` で 6/6 PASS 記録

---

## スプリント S023: Drawer redesign (v3) + Mobile UX polish + last-active memory [DONE]

M2 完了後の refine フェーズで判明した複数の UI/UX 課題を 1 sprint で解消する polish sprint。 Drawer の視認性を v3 mock (terminal editorial design — `/tmp/drawer-mock-v3.html` の最終版) に従って全面 refactor、 並行して mobile での Git サブタブ overflow と drawer の自動閉じを実装、 加えて collapsed repo を 1 クリックで「いつもの作業場所」 に戻すための last-active-worktree memory を per-repo で永続化する。

**設計の中核**:
- **Drawer は v3 mock を design source-of-truth** とする (numbered repos `01..NN` / status strip with active count / chip pills (warm = unmanaged、 muted = subagent) / expanded panel に always-visible icon button (`↗ promote` / `✕ remove`、 active subagent は remove disabled) / active branch に `● HERE` label + 3px accent border + 2.6s 脈動 glow / sub-branch meta line (`stale 8d` / `5h ago` / fresh `●`)) 
- **タイポは既存 `Geist Mono` を維持** (新規フォント bundle 追加なし、 v3 の brutalist 感は size + weight + letter-spacing で表現)
- **Last-active per repo** は `repos.json` に **`last_active_branch` フィールド** を per-repo に追加 (omitempty、 backward-compat、 migration 不要)。 ナビゲーション時に implicit 更新、 collapsed repo の header / chevron クリックで navigate + 展開
- **削除済 branch fallback**: 起動時 reconcile (S015 で導入済) で `last_active_branch` を実存チェック、 不在なら null。 navigate 時にも double-check
- **Mobile drawer auto-hide**: branch / worktree クリック (= URL navigate) のときのみ自動 close、 repo expand のみではトリガしない (展開操作と切替操作を区別)
- **Mobile Git subtab dropdown**: < 600px で `<select>` ネイティブ要素に切替 (accessible + native feel)、 ≥ 600px は既存 horizontal tabs 維持
- **`+` ボタン挙動**: 新ブランチ作成 (既存挙動維持)。 last-active navigate 経路には乗せない (UX 区別)

### ストーリー S023-1: Drawer 視認性改善 + last-active 記憶 [x]

**ユーザーストーリー:**
Palmux のユーザとして、 ドロワーから自分の作業状況が一瞥でわかり、 collapsed の repo をワンクリックで「いつもの作業場所」 に戻れるようにしたい。 なぜなら、 並列で複数 Claude を運用するなかで「どこに何があったか」 を素早く認識し、 最頻 repo への切替コストを下げたいからだ。

**受け入れ条件:**
- [ ] Drawer は v3 mock のレイアウトに準拠 (numbered repos、 status strip、 chip pills、 expanded panel、 active "Here" label + 脈動 glow、 sub-branch meta、 ⌘K hint footer)
- [ ] Status strip は「`● N active · M total`」 形式で active count + total を表示
- [ ] Repo は `01..NN` の番号付き、 同種別グループ内では `tab_overrides.order` (S020) を尊重
- [ ] Active branch に `● HERE` label + 3px accent border-left + 2.6s 脈動 glow
- [ ] Chip pills: `unmanaged·N +` (warm = `#ffa342`)、 `subagent·N +` (muted gray)、 chip 押下で expanded panel が下にフェードイン (debounce不要)
- [ ] Expanded panel: `panel-head` (カテゴリ名 + 件数 + 一括アクション)、 sub-branch grid (`minmax(0, 1fr) auto`)、 always-visible icon button (`↗` promote / `✕` remove、 26×22px)
- [ ] Active subagent (タスク実行中) は `✕ remove` が disabled (opacity 0.3、 pointer-events none)
- [ ] Sub-branch meta は `stale Nd` / `Nh ago · ⌁ active task` / fresh `●` indicator を含む
- [ ] **`repos.json` schema 拡張**: 各 repo entry に `last_active_branch: string` (omitempty)
- [ ] **Last-active 自動更新**: ユーザが branch / worktree に navigate したとき、 該当 repo の `last_active_branch` が実装済 endpoint で永続化される
- [ ] **Collapsed repo クリック**: header (repo name) または chevron をクリックすると、 `last_active_branch` が現存していれば navigate + 展開、 不在なら expand のみ
- [ ] `+` ボタンは新ブランチ作成 (既存挙動)、 last-active navigate には乗らない
- [ ] 起動時 reconcile (S015) が `last_active_branch` の実存も check、 消えていれば null に reset
- [ ] WS event `branch.lastActiveChanged` (or 既存の `branch.categoryChanged` 拡張) で別クライアント同期
- [ ] モバイル幅で v3 design が破綻しない (タップ領域 36px+、 chip 押下で展開)

**タスク:**

**バックエンド:**
- [x] **タスク S023-1-1**: `internal/config/repos.go` schema 拡張 — `Repo` 構造体に `LastActiveBranch string` フィールド追加 (json tag `last_active_branch,omitempty`)、 既存 `repos.json` の読み込みは backward-compat
- [x] **タスク S023-1-2**: REST API: `PATCH /api/repos/{repoId}/last-active-branch` (`{branch}` body)。 既存 nav handler にも内部呼び出しを hook し、 ユーザが branch を navigate するたびに implicit 更新
- [x] **タスク S023-1-3**: 起動時 reconcile (S015 の `ReconcileUserOpenedBranches` と同パス) で `last_active_branch` の実存も check、 不在なら null へ
- [x] **タスク S023-1-4**: WS event emit (`branch.lastActiveChanged` 新設 or `branch.categoryChanged` payload 拡張)、 別クライアント同期
- [x] **タスク S023-1-5**: `Repos()` / `Repo()` snapshot に `LastActiveBranch` を含める

**フロントエンド (Drawer redesign):**
- [x] **タスク S023-1-6**: `frontend/src/components/drawer.module.css` を v3 mock 準拠に全面 refactor — Fog palette + Geist Mono、 status strip / numbered repos / chip pills / expanded panel / active glow / sub-branch grid のスタイル
- [x] **タスク S023-1-7**: `frontend/src/components/drawer.tsx` を v3 構造で書き直し — 各セクション (★ Starred / Repositories) に numbered repos、 各 repo に branches list (my)、 chip row (unmanaged / subagent)、 expanded panel
- [x] **タスク S023-1-8**: 新規 `<ChipExpandedPanel>` コンポーネント — sub-branch list + always-visible promote / remove icon buttons (active task 時 remove disabled)
- [x] **タスク S023-1-9**: 「Here」 label + 3px accent border + 2.6s 脈動 animation (CSS keyframes glow-bar)
- [x] **タスク S023-1-10**: Sub-branch meta 表示 — `stale Nd` / `Nh ago · ⌁ active task` / fresh `●` indicator (BE response の `last_activity_at`、 `is_active` フィールドから生成)

**フロントエンド (last-active 機能):**
- [x] **タスク S023-1-11**: Branch navigation hook で `PATCH last-active-branch` を fire-and-forget で呼ぶ (失敗しても UX に影響しない)
- [x] **タスク S023-1-12**: Collapsed repo header / chevron クリックで `last_active_branch` を見て、 navigate + expand、 不在なら expand のみ。 `+` ボタンは別経路 (新ブランチ作成)

### ストーリー S023-2: Mobile UX 改善 (Git subtab dropdown + drawer auto-hide) [x]

**ユーザーストーリー:**
Palmux のモバイルユーザとして、 Git サブタブの選択と Workspace 選択で画面が破綻せず、 操作後にドロワーが自動で閉じてほしい。 なぜなら、 狭い画面で水平 overflow するタブ列はタッチ操作が困難で、 workspace 選択後にいちいちドロワーを手で閉じるのは煩雑だからだ。

**受け入れ条件:**
- [ ] モバイル幅 (< 600px) で Git タブのサブタブ列が `<select>` dropdown に切替わる
- [ ] デスクトップ幅 (≥ 600px) は既存 horizontal tabs 維持
- [ ] Dropdown 選択で対応するサブビューに遷移 (既存 navigation を流用)
- [ ] モバイルドロワー (BottomSheet) は branch / worktree クリック後に自動 close
- [ ] Repo expand のみではドロワー閉じない (展開操作と切替操作を区別)
- [ ] `+` ボタンクリック (新ブランチ dialog) もドロワー閉じない
- [ ] タップ領域 36px+ 維持

**タスク:**
- [x] **タスク S023-2-1**: `frontend/src/tabs/git/git-view.tsx` (or 該当箇所) で `< 600px` メディアクエリで `<select>` 描画、 ≥ 600px で既存 tabs 描画。 共通 navigation hook を介して URL 更新
- [x] **タスク S023-2-2**: モバイルドロワー (S022 BottomSheet) に `onNavigate` callback を追加、 worktree クリック時に dispatch して drawer を auto-close
- [x] **タスク S023-2-3**: Repo expand と branch navigate の handler を分離、 expand のみではドロワー閉じない

### E2E (両ストーリー共通)

- [x] **タスク S023-3-1**: `tests/e2e/s023_*.py` で:
  - (a) Drawer v3 視覚スモーク (numbered repos、 status strip、 chip pills、 active glow CSS が適用される)
  - (b) chip 押下で expanded panel が描画、 promote / remove icon button が visible
  - (c) Last-active: branch A navigate → repo collapse → repo header click → branch A に再 navigate + 展開
  - (d) Last-active: branch を `gwq remove` で削除 → 次回起動 reconcile で `last_active_branch` が null
  - (e) Mobile (< 600px): Git タブで `<select>` 表示、 desktop で horizontal tabs 表示
  - (f) Mobile drawer: worktree クリックで auto-close、 repo expand のみでは閉じない
  - (g) Mobile drawer: `+` ボタンクリックで閉じない (新ブランチ dialog 経路)
  - (h) Active subagent の `✕ remove` ボタンが disabled
  - (i) WS sync: 別ブラウザで navigation → 自クライアントの drawer に last-active 反映

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
- S015（Worktree categorization）は他の sprint と独立。 Drawer surface のみの改修 + `repos.json` schema 拡張で完結する。 S008-S014 のいずれにも依存せず、 並列実装も可能。 ユーザ価値の観点で「Claude 並列実行が増えた後ほど効く」ので S014 の後に置くが、 早出ししたければ S012 の前に持ってくることもできる
- S016（Sprint Dashboard tab）は **`internal/worktreewatch/` 共通 filewatch 基盤を新設** する。 S012 (Git filewatch) もこの基盤を再利用するので、 S012 と S016 の **どちらが先でもよい** が、 先に来た方が基盤を作る責務を持つ。 推奨は S012 → S016 の順 (Git の filewatch ニーズが先に発生するため) だが、 S016 → S012 でも実装可能
- S017（Long session performance）は他 sprint と独立。 Claude タブの描画層 + Read tool result の表示層に閉じる
- S018（Conversation utilities）は他 sprint と独立。 検索・エクスポート・/compact はそれぞれ別モジュール
- S019（Conversation rewind）は **`Turn.versions` のデータモデル拡張** を伴うので、 S017 の仮想化と整合させる必要あり (versions 切替で subsequent turns の再 layout が走る)。 推奨実行順: S017 → S019 (S018 は並行 OK)
- S020（Tab UX completion）は **S009 (TabBar 統一基盤) 必須前提**。 S012 (Magit 風キー) があれば Bash isolation の背景が揃うので、 S012 完了後の方が自然
- S021（Subagent worktree lifecycle）は **S015 必須前提**。 cleanup と promote は S015 の検出ロジックを延長する
- S022（Mobile UX 総点検）は **全 sprint の最後**。 各 sprint で都度モバイル対応を入れているが、 横断監査として最終 sprint に置く

## バックログ

Phase 4 以降に位置付けられる項目。スコープ確定の段階で個別 Sprint に切り出す:

- [x] ~~**Tool 結果 / 会話の virtualization** (Phase 4.1)~~
  S017 (Long session performance) で `react-window` 導入として実装。
- [x] ~~**Markdown / コードブロック syntax highlighting** (Phase 4.2)~~
  S010 (Files preview 拡張) で Monaco Editor を導入する際、Markdown 内コードブロックも Monaco レンダラに乗せられるなら同時対応。優先度低め (既存 MD preview の見た目に合わせる)。バックログとしては S010 完了後に再評価。
- [x] ~~**会話内検索 (Cmd+F)** (Phase 4.3)~~
  S018 (Conversation utilities) で実装。
- [x] ~~**会話エクスポート (Markdown / JSON)** (Phase 4.4)~~
  S018 (Conversation utilities) で実装。
- [x] ~~**AskUserQuestion モーダル** (Phase 4.5)~~
  S007 で実装完了。autopilot/S007 ブランチ。
- [x] ~~**`/compact`** (Phase 4.6)~~
  S018 (Conversation utilities) で実装。 control_request subtype は S018 内で spike 後確定。
- [x] ~~**Read 先頭 N 行プレビュー** (Phase 4.7)~~
  S017 (Long session performance) で実装。 デフォルト 50 行プレビュー + 「Show all (X lines)」 ボタン。
- [x] ~~**バンドル分割 (dynamic import)** (Phase 4.8)~~
  S022 (Mobile UX 総点検) で実装。 初期ロード < 500KB 目標。
- [x] ~~**モバイル UX 総点検 (bottom sheet 化したセレクタ・タップ領域)** (Phase 4.9)~~
  S022 (Mobile UX 総点検) で実装。
- [x] ~~**Playwright headless E2E ハーネス** (S001 から繰り越し)~~
  S022 (Mobile UX 総点検) のストーリー S022-2 で実装。 viewport 固定 + touch emulation。
- [ ] **Commit-diff endpoint + Monaco diff in log detail pane** (S013 由来)
  rich log の commit 選択時に first-parent diff を Monaco DiffEditor で表示する。 S013 では metadata 表示までに留めた。 サーバ側は `/git/commit-diff?sha=` を新設、 FE は GitMonacoDiff を流用。 サイズ M。
- [ ] **Cherry-pick / merge conflict WS event** (S013 由来)
  cherry-pick が conflict で停止したとき、 現在は HTTP 409 のみ。 WS event `git.conflictDetected` を emit して Activity Inbox に出すとさらに発見性が上がる。 S014 (Conflict & Interactive Rebase) のスコープに統合する候補。 サイズ S。
- [ ] **Blame view の Monaco gutter 統合** (S013 由来)
  S013 では軽量 table renderer を採用。 Monaco の `decorations` API で gutter 注釈にすれば編集中のファイルでもインライン blame が出せる。 S014 〜 S018 の磨き込みで対応。 サイズ M。
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
- [x] ~~**Claude / Bash タブの drag reorder** (S009 由来)~~
  S020 (Tab UX completion) で実装。
- [x] ~~**Claude / Bash タブの rename** (S009 由来)~~
  S020 (Tab UX completion) で実装。
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
- [x] ~~**Bash タブも Magit 風キーバインド対象に** (S012 由来 / 対称性)~~
  S020 (Tab UX completion) で focus-aware keybinding handler として実装。 Bash focus 中は Magit 風キー無効化。
- [x] ~~**Subagent worktree の自動クリーンアップ** (S015 由来)~~
  S021 (Subagent worktree lifecycle) で実装。 stale 判定 + 一括削除。
- [x] ~~**Subagent worktree の Promote action** (S015 由来)~~
  S021 (Subagent worktree lifecycle) で実装。 gwq 標準位置に move + my 化。
- [ ] **Sprint タブから sprint 操作を起動** (S016 由来)
  S016 は read-only 完全特化。 「Sprint Detail から sprint plan / sprint auto / sprint refine を起動」「Decision Timeline から該当 sprint の logs にジャンプ」などのアクション launcher は backlog。 Claude タブとの連動 (Claude タブの composer に sprint 操作プロンプトを prefill 等) も検討対象。 サイズ M。
- [ ] **Cross-branch active autopilot の集約表示** (S016 由来)
  S016 では現在ブランチの `.claude/autopilot-*.lock` のみ検出。 「main で S012 走行中、 feature/billing で S013 走行中」のように複数ブランチの autopilot を 1 画面で見たい場面がある。 ただし auth / perm 設計が複雑化するため (cross-branch 読み取り権限の扱い)、 別 sprint で慎重に検討。 サイズ M。
- [ ] **composer.prefill hot-reload listener** (S012 由来)
  AI commit message ボタンを押したとき、 既に Claude タブが mount 中だと localStorage には書き込まれるが live composer の value 状態が refresh されない。 `composer.tsx` に `palmux:composer-prefill` CustomEvent listener を足すと完全に閉じる。 サイズ S。
- [ ] **branch ahead/behind counters** (S012 由来)
  `git for-each-ref --format=%(upstream:track)` で `[ahead 2, behind 1]` 文字列を取得して `BranchEntry` に乗せ、 `git-branches.tsx` で badge 表示。 「いま push すべきか」の判断を即座にできる。 サイズ S。
- [ ] **`--force-with-lease=ref:expect` lease ref 明示** (S012 由来)
  現在は bare `--force-with-lease`。 FE が upstream sha を握っている場合は `--force-with-lease=refs/heads/branch:expected_sha` の形で明示すると、より安全。 サイズ S。
