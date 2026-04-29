# プロジェクトロードマップ: Palmux v2 — Phase 3

> Phase 1 (stream-json + MCP) と Phase 2 (Cores A〜E、入力補完、ツール出力リッチ化、セッション運用、安全機能) は完了済み。本ロードマップは **Phase 3 (拡張機能の本命)** をスプリント単位で管理する。Phase 1/2 の経緯は [`docs/original-specs/06-claude-tab-roadmap.md`](original-specs/06-claude-tab-roadmap.md)、Phase 4/5+ の予定も同ファイル参照。

## 進捗

- **現在のスプリント: S001 — Plan モード UI**
- 合計: 6 スプリント | 完了: 0 | 進行中: 1 | 残り: 5
- [░░░░░░░░░░░░░░░░░░░░] 0%

## 実行順序

**S001** → S002 → S003 → S004 → S005 → S006

---

## スプリント S001: Plan モード UI [IN PROGRESS]

Claude Code の **Plan モード** (CLI が立てた実行計画をユーザに提示してから実行する流れ) を Palmux 上で受けられるようにする。Phase 2 では `permission_mode = plan` を選択しても専用 UI がなく、ExitPlanMode の出力が普通の text ブロックとして垂れ流されていた。Phase 3 の中で最も Claude Code Desktop 体験に直結する項目。

### ストーリー S001-1: ExitPlanMode を専用ブロックで描画 [ ]

**ユーザーストーリー:**
Palmux のユーザとして、Claude が立てた計画を専用 UI で読みたい。なぜなら、長文の text ブロックに混ざると見落とすからだ。

**受け入れ条件:**
- [ ] `permission_mode=plan` で Claude が `ExitPlanMode` を呼ぶと、会話に「計画」として識別できる専用ブロックが描画される
- [ ] 計画ブロックは Markdown でレンダリングされ、可読性は通常の assistant 出力と同等以上
- [ ] 計画ブロックは折りたたみ可能で、長い計画でも会話全体を圧迫しない

**タスク:**
- [ ] **タスク S001-1-1**: stream-json の ExitPlanMode 入力を normalize 段階で `kind: "plan"` ブロックに変換
- [ ] **タスク S001-1-2**: フロントの `BlockView` に `PlanBlock` を追加し、`blocks.module.css` に専用スタイル追加
- [ ] **タスク S001-1-3**: 過去ターンに ExitPlanMode が含まれるトランスクリプトを resume したときも正しく表示できることを確認

### ストーリー S001-2: 計画→実行ボタンで Permission モードを遷移 [ ]

**ユーザーストーリー:**
Palmux のユーザとして、提示された計画を承認したらワンクリックで実行に移したい。なぜなら、計画読了後に手で `permission_mode` を切り替えるのは煩雑だからだ。

**受け入れ条件:**
- [ ] 計画ブロック内に「Approve & Run」ボタンが表示される
- [ ] クリックで `permission_mode` が `plan` から既定の実行モード（`acceptEdits` 等、ブランチ prefs に従う）へ自動的に切り替わる
- [ ] 計画後の手動修正が必要なケースのために「Reject」または「Stay in plan」操作も提供される

**タスク:**
- [ ] **タスク S001-2-1**: PlanBlock に承認 / 却下 ボタンを追加し、対応する WS frame (`permission_mode.set` + 任意の `user.message` 補足) を送る
- [ ] **タスク S001-2-2**: 承認時のモード復帰先を `BranchPrefs.PermissionMode` から決定するロジックを追加
- [ ] **タスク S001-2-3**: ブラウザで Plan → Approve → 実行が一連でつながることを Playwright で確認

---

## スプリント S002: `.claude/settings.json` editor [ ]

Phase 2 で `Always-allow` が `.claude/settings.json` の `permissions.allow` を書き込むようになった。同じファイルの permissions / hooks / etc. を **GUI から閲覧・編集** できるようにし、責務越境のリスクを「ユーザが見える形で管理する」方向に収斂させる。

### ストーリー S002-1: settings.json をブランチ単位で閲覧する [ ]

**ユーザーストーリー:**
Palmux のユーザとして、現在のブランチに効いている `.claude/settings.json` の内容を一目で確認したい。なぜなら、いつの間にか追加された permissions.allow が事故の元になるからだ。

**受け入れ条件:**
- [ ] Claude タブのトップバーまたは設定パネルから「Settings」を開ける
- [ ] project (`.claude/settings.json`) と user (`~/.claude/settings.json`) の両方の現在値が読める
- [ ] 各エントリ（permissions.allow / deny / hooks / 他）が分類別に列挙される

**タスク:**
- [ ] **タスク S002-1-1**: project / user の settings.json を読み込み、構造化して返す REST API を追加
- [ ] **タスク S002-1-2**: フロントエンドで Settings popup ないしモーダルコンポーネントを追加
- [ ] **タスク S002-1-3**: project と user の差分を視覚的に区別する（バッジ等）

### ストーリー S002-2: 個別エントリを削除する [ ]

**ユーザーストーリー:**
Palmux のユーザとして、誤って追加してしまった許可エントリを GUI から削除したい。なぜなら、エディタで JSON を直接いじるのはリスキーだからだ。

**受け入れ条件:**
- [ ] permissions.allow の各エントリに削除ボタンがある
- [ ] 削除前に「project / user のどちらから消すか」と最終確認が出る
- [ ] 削除は immediate に CLI に反映される（claude が再起動された場合も矛盾しない）

**タスク:**
- [ ] **タスク S002-2-1**: settings.json の特定エントリを削除する REST API を追加（atomic write）
- [ ] **タスク S002-2-2**: フロントエンドで削除確認ダイアログを既存の confirm dialog に統合
- [ ] **タスク S002-2-3**: 削除後の状態でセッションを継続して問題が起きないことを実機で確認

---

## スプリント S003: Sub-agent (Task) 入れ子ツリー [ ]

Claude が `Task` ツールでサブエージェントを呼び出すと、現状は親ターンと子ターンが時系列にフラットに並ぶ。`parent_tool_use_id` を辿って **親 Task ブロックの下に子のターン群をネスト表示** できるようにし、長い自律ワークフローの可読性を上げる。

### ストーリー S003-1: Task ツールの下に子ターンをネスト表示する [ ]

**ユーザーストーリー:**
Palmux のユーザとして、サブエージェントが何をやっているかをトップレベルの会話と区別して見たい。なぜなら、`Task` を多用すると会話が肥大して何が親で何が子かわからなくなるからだ。

**受け入れ条件:**
- [ ] `Task` ツールブロックを展開すると、その下にサブエージェントのターン列がインデント付きで表示される
- [ ] サブエージェントの `tool_use` / `tool_result` も同じ折りたたみ規約で動作する
- [ ] `Task` が完了するとそのまとまり全体が折りたたまれ、要約だけが見える状態になる

**タスク:**
- [ ] **タスク S003-1-1**: `parent_tool_use_id` を session 内で記録し、ターンに親ポインタを付与
- [ ] **タスク S003-1-2**: `agent-state.ts` の reduce で親子関係を保持し、`turns` を木構造または親 ID で参照可能にする
- [ ] **タスク S003-1-3**: フロントエンドで Task ブロックを再帰的に描画する `<TaskTree>` コンポーネントを追加
- [ ] **タスク S003-1-4**: 過去 transcript を resume してもネスト構造が復元されることを確認

---

## スプリント S004: MCP server 一覧 UI [ ]

`system/init` が MCP サーバの接続状態をすでに返しており、バックエンドは `Session.MCPServers` でトラックしている。**フロントエンドが `mcpServers={[]}` で空配列を渡している** だけなので、データを描画する小さなスプリント。

### ストーリー S004-1: MCP サーバの接続状態を確認できる [ ]

**ユーザーストーリー:**
Palmux のユーザとして、Claude にどの MCP サーバが繋がっているかを確認したい。なぜなら、MCP の不調がツール実行失敗の原因になることがあるからだ。

**受け入れ条件:**
- [ ] Claude タブのトップバーまたは popup で MCP サーバの一覧が見える
- [ ] サーバ名と接続状態（connected / disconnected / error）が表示される
- [ ] サーバの再起動などは Phase 3 ではスコープ外（表示のみ）

**タスク:**
- [ ] **タスク S004-1-1**: `claude-agent-view.tsx` の `TopBar` に MCP インジケータを追加し、`state.mcpServers` を渡す
- [ ] **タスク S004-1-2**: クリックで詳細 popup が開き、サーバ名・状態・最終接続時刻が見られる
- [ ] **タスク S004-1-3**: テーマトークンに準拠したスタイルを `claude-agent-view.module.css` に追加

---

## スプリント S005: Hook events 表示 [ ]

CLI が `--include-hook-events` フラグで PreToolUse / PostToolUse などのフック実行イベントを stream-json に流せる。これを **折りたたみブロックとして可視化** することで、CLAUDE_CODE 側で構築済みの自動化が見えるようにする。

### ストーリー S005-1: hook イベントが会話ログに表示される [ ]

**ユーザーストーリー:**
Palmux のユーザとして、自分の hooks が走ったタイミングを会話の流れの中で見たい。なぜなら、いつ何が hooks 経由で操作したかが追えないと、エージェントの挙動を信頼できないからだ。

**受け入れ条件:**
- [ ] CLI が hook イベントを emit すると、対応する位置に「hook: PreToolUse」のような折りたたみブロックが現れる
- [ ] 展開すると hook の出力 / exit code / 修正後の payload が見える
- [ ] hook イベントの表示はオプトイン（`--include-hook-events` を有効にした場合のみ）

**タスク:**
- [ ] **タスク S005-1-1**: `claudeagent.ClientOptions` に `IncludeHookEvents` フラグを追加し、`--include-hook-events` を渡す
- [ ] **タスク S005-1-2**: stream-json の `hook_event` メッセージを normalize 段階で `kind: "hook"` ブロックに変換
- [ ] **タスク S005-1-3**: フロントの `BlockView` に `HookBlock` を追加（ToolUseBlock と類似の折りたたみ）
- [ ] **タスク S005-1-4**: ユーザが `settings.json` で hooks を設定 → 実機で発火を確認

---

## スプリント S006: `--add-dir` / `--file` UI [ ]

Composer の attach メニューを拡張して、**会話に追加コンテキスト（ディレクトリ・ファイル）を渡せる** ようにする。クロスレポ作業や、worktree 外の参照ドキュメントを同時に渡したい場面で使う。

### ストーリー S006-1: 追加ディレクトリ / ファイルを Composer から選んで送る [ ]

**ユーザーストーリー:**
Palmux のユーザとして、現在の worktree に含まれていないコードや仕様書を Claude に参照させたい。なぜなら、複数リポジトリ横断の作業や設計仕様書を見ながらの実装で必要だからだ。

**受け入れ条件:**
- [ ] Composer の `+` メニューから「Add directory」「Add file」が選べる
- [ ] 選択したパスは送信時に `--add-dir <path>` または `--file <path>` として CLI に渡る
- [ ] 添付済みのパスはチップ列に「📁 path/」「📄 file」のように表示される
- [ ] チップ削除で対応する CLI 引数も外れる

**タスク:**
- [ ] **タスク S006-1-1**: バックエンドで `--add-dir` / `--file` を `ClientOptions` 経由で渡せるようにし、必要に応じて respawn
- [ ] **タスク S006-1-2**: Composer の Attachment 型を拡張（kind を `image` | `dir` | `file` に）し、UI でファイル選択ピッカーを追加
- [ ] **タスク S006-1-3**: 添付チップの見た目をディレクトリ・ファイルでも統一感のあるスタイルに揃える
- [ ] **タスク S006-1-4**: ホスト機の `~/` 配下のファイルが選択できるか、サーバ側のセキュリティ範囲を `imageUploadDir` の方針に合わせて確認

---

## 依存関係

- S001 〜 S006 はそれぞれ **独立** に実装可能。実行順序の根拠はユーザ価値の累積効果（Plan モード UI が最も体験に直結する）
- S001 完了後に S002（Settings editor）を続けるのは、Plan モード UI で `permissions.allow` への自動追加を促すフローが Settings editor で確認できる流れになるため
- S003（Sub-agent ツリー）は他のスプリントとデータ依存はないが、描画ロジックが大きいので独立 PR として扱う

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
- [ ] **AskUserQuestion モーダル** (Phase 4.5)
  CLI 応答経路の確認込み。
- [ ] **`/compact`** (Phase 4.6)
  control_request subtype の仕様確認後。
- [ ] **Read 先頭 N 行プレビュー** (Phase 4.7)
- [ ] **バンドル分割 (dynamic import)** (Phase 4.8)
- [ ] **モバイル UX 総点検 (bottom sheet 化したセレクタ・タップ領域)** (Phase 4.9)
