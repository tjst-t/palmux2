# Track B go/no-go 判定 & ロードマップ — S1d2278-6

## 0. 判定

**Track B 続行 (GO)** — 4 軸すべてが green / overage (経済的根拠) であり、pivot 期限 2026-06-07 まで余裕あり。

---

## 1. 4 軸 go/no-go マトリクス

| 軸 | 判定 | 根拠ファイル | 一行サマリ |
|---|---|---|---|
| emulator 実用性 | **green** | [`emulator-comparison.md`](emulator-comparison.md) | charmbracelet/x/vt が必須機能 (alt screen / OSC 52 / bracketed paste / mouse / scrollback) を満たし、 active maintainer |
| PTY daemon 安定性 | **green** | [`pty-daemon-spike.md`](pty-daemon-spike.md) | Go の creack/pty で PTY 所有 + 1MiB ring buffer + multi-client attach + signal handling が PoC で成立 |
| autopilot 経路の影響 | **green (no impact)** | [`autopilot-billing-path.md`](autopilot-billing-path.md) | autopilot/sprint skill は Agent SDK / claude -p を呼ばない。 Agent tool 経由 = subscription billing |
| SDK クレジット見通し | **overage (= Track B 続行の経済的根拠)** | [`sdk-credit-analysis.md`](sdk-credit-analysis.md) | Max 20x の SDK 月次クレジット $200 を現状の palmux2 利用パターンで 2 週間で消費。 sprint 集中月は $50–$150 超過 |

---

## 2. 続行 (Track B) ケースの次 sprint 群骨子

### Sprint A — `S7ce250`: Production PTY daemon + claude-tui タブ統合

**Goal**: PoC PTY daemon を palmux2 本体に組み込み、 `claude-tui` タブとして desktop UX を実用水準にする。 claude-agent タブはopt-in高機能パスとして残存。

**Sprint ID 算出フレーズ**: `track-b-production-claude-tui-2026-05-17`

#### Stories

| Story | タイトル | 1行サマリ |
|---|---|---|
| S7ce250-1 | Production PTY daemon 移植 | `cmd/poc-pty/` の実装を `internal/tab/claude-tui/daemon.go` へ移植。 `spawnOnce` を `respawnLoop` (claude --resume 対応) に置換。 `--claude-args` を repeated flag に変更 |
| S7ce250-2 | claude-tui タブ Provider 実装 | `internal/tab/claude-tui/provider.go` を実装。 `OnBranchOpen` で daemon 起動、 `OnBranchClose` で daemon 停止。 `NeedsTmuxWindow() == false` |
| S7ce250-3 | terminal-view WS 配線 + PTY resize 修正 | 既存 `terminal-view.tsx` を claude-tui タブに接続。 `FitAddon` のリサイズイベントから `ioctl TIOCSWINSZ` (SIGWINCH) を daemon まで伝播させ、 Story 3 の known gap を解消 |
| S7ce250-4 | セッション永続化 (claude --resume) | daemon が claude session id を `fsnotify` で `~/.claude/projects/` から自動検出し、 再起動時に `claude --resume <id>` を発行 |
| S7ce250-5 | E2E テスト + Story 3 manual smoke 補完 | `tests/e2e/s7ce250_claude_tui.py` で branch open → claude-tui attach → ring replay → resize → branch close を検証。 xterm.js canvas の text introspect 代替として `data-testid` ベースのステータス検証を採用 |

**依存**: S1d2278 (PoC 成果物一式)
**対象外**: mobile chat UI (Sprint B)、 ESC ESC 編集 (Sprint C)、 claude-agent タブの廃止 (別途検討)

---

### Sprint B — `S0fd64b`: サーバーサイド emulator + grid diff プロトコル + mobile chat PoC

**Goal**: charmbracelet/x/vt を palmux2 に統合し、 PTY バイトストリームをサーバー側でグリッドとして管理する。 grid diff プロトコルを WS に追加し、 mobile chat-style UI を抽出・検証する。

**Sprint ID 算出フレーズ**: `track-b-grid-diff-mobile-chat-2026-05-17`

#### Stories

| Story | タイトル | 1行サマリ |
|---|---|---|
| S0fd64b-1 | charmbracelet/x/vt emulator 統合 | `internal/tab/claude-tui/emulator.go` に `charmbracelet/x/vt` をラップ。 OSC 52 コールバックをクリップボード通知として palmux2 の notify hub に接続 |
| S0fd64b-2 | grid diff WS プロトコル設計 + 実装 | `CellAt(x,y)` のフレーム差分を JSON で送出する `gridDiff` WS メッセージ型を定義。 既存 raw-bytes モードと共存できる negotiate メカニズムを追加 |
| S0fd64b-3 | multi-client 調整 (active-client follows) | 複数クライアントが同一 PTY に attach したとき、 最後に入力したクライアントを「アクティブ」として input を受け付け、 他クライアントは読み取り専用に設定 |
| S0fd64b-4 | mobile chat UI 抽出 PoC | grid diff を受信する React コンポーネント `MobileChatView` を実装。 xterm.js 不使用で最終行だけをメッセージバブルとして表示する MVP を検証 |

**依存**: S7ce250 (Sprint A 完了)
**対象外**: ESC ESC injection (Sprint C)、 Activity Inbox 連携 (Sprint C)

---

### Sprint C — `S1f75ec`: ESC ESC 編集 + UX 統合ポリッシュ

**Goal**: ESC ESC メッセージ編集、 claude-agent ⇄ claude-tui 並走 UX、 Toolbar モード連携、 Sprint Dashboard / Activity Inbox 互換性を完成させる。

**Sprint ID 算出フレーズ**: `track-b-esc-esc-ux-polish-2026-05-17`

#### Stories

| Story | タイトル | 1行サマリ |
|---|---|---|
| S1f75ec-1 | ESC ESC injection + 編集 dialog | 2 回連続 ESC でインタラクティブ claude に「中断」シグナルを送り、 直前のプロンプトをブラウザ上の edit dialog に展開して再送できる UI を実装 |
| S1f75ec-2 | claude-agent ⇄ claude-tui タブ切替 UX | ⌘K パレットの `>switch-claude-mode` コマンドで claude-agent / claude-tui を切り替えられる機能を追加。 branch 設定に永続化 |
| S1f75ec-3 | Toolbar mode 自動切替 | claude-tui タブがフォーカスされたとき Toolbar が自動的に `claude` モードに切り替わるよう既存の Toolbar 2モード機構に claude-tui を登録 |
| S1f75ec-4 | Activity Inbox 互換 + Sprint Dashboard 疎通確認 | claude-tui の notification event (claude からの `permission_prompt` / task complete) を Activity Inbox に流す。 Sprint Dashboard タブが claude-tui セッションと競合しないことを E2E で確認 |

**依存**: S0fd64b (Sprint B 完了)
**対象外**: sixel グラフィクス対応、 palmux v1 互換 API、 claude-agent タブの廃止

---

## 3. pivot (Track A) ケースの骨子

> 判定は GO (Track B 続行) だが、 pivot 基準を超えた場合の fallback として記録する。

### Sprint X (fallback): Track A pivot — tmux ベース Claude タブ復元

**Goal**: 旧 tmux ベース Claude タブ (`git show ad0749e^:internal/tab/claude/`) を `claude-tui` tab id として復元し、 claude-agent と並走させる。 production-grade ではなく「subscription quota で動く最低限の経路」。

#### Stories

| Story | タイトル | 1行サマリ |
|---|---|---|
| SX-1 | tmux Claude タブ backend revert | `git show ad0749e^:internal/tab/claude/` をベースに `internal/tab/claude-tui/` として復元。 tab id を `claude-tui`、 tmux window name を `palmux:claude-tui:claude-tui` に変更 |
| SX-2 | frontend tmux Claude タブ revert | 旧 `frontend/src/tabs/claude/` (interactive tmux terminal) を `claude-tui` として復元。 existing claude-agent タブは `claude-agent` id のまま残存 |
| SX-3 | tab id rename + ルーティング整合 | URL `/claude` → `/claude-tui` の redirect + React Router 更新。 既存ブックマーク互換の 302 redirect をサーバーに追加 |
| SX-4 | Toolbar モード統合 | claude-tui タブフォーカス時の Toolbar `claude` モード自動切替を既存機構に登録 |
| SX-5 | smoke test | E2E で branch open → claude-tui attach → 入力送信 → tmux window 生存確認 |

---

## 4. pivot 期限と運用ルール

| 項目 | 内容 |
|---|---|
| **pivot 期限** | **2026-06-07** (Anthropic 課金変更 2026-06-15 から 1 週間 buffer) |
| **go/no-go 判定点** | Sprint A (S7ce250) の **Story 1 — Production PTY daemon 移植** が 2026-06-07 までに green になるかどうか |
| **判定が red の場合** | 即時 Track A pivot (Sprint X) を発動。 claude-agent タブは一時的に subscription quota 外で動くが、 2026-06-15 まで猶予あり |
| **判定責任者** | **ユーザ承認** — sprint A demo phase で sprint verify の結果をもとに提示し、 ユーザが continue/pivot を選択 |
| **sprint A 中の pivot 判断頻度** | Sprint A の各 Story 完了時に go/no-go を再評価。 Story 1 が 6/7 まで green にならない場合のみ自動的に pivot 発動とする |

---

## 5. 既知の rough edges — sprint A へのマッピング

Story 2/3 で発見した out-of-scope findings を sprint A の対象 Story にマッピングする。

| Rough edge | 発見 Story | sprint A 対処 Story |
|---|---|---|
| PTY master の `SetReadDeadline` Linux で unreliable — goroutine + channel パターン必須 | S1d2278-2 (scenario-1) | **S7ce250-1** — production daemon で goroutine-based タイムアウトを標準実装として採用 |
| `exec.CommandContext(reqCtx)` 罠 — WS disconnect で subprocess が死ぬ | S1d2278-2 (scenario-5 #2) | **S7ce250-1** — `daemonCtx/daemonCancel` 分離パターンを production daemon に継承 |
| `--claude-args` の `strings.Fields` 制限 — スペース含む引数不可 | S1d2278-2 (scenario-5 #7) | **S7ce250-1** — `--claude-args` を repeated flag (`--claude-arg`) に置換 |
| SIGWINCH / TIOCSWINSZ 未伝播 — ウィンドウリサイズで出力 reflow 不正 | S1d2278-3 (scenario-3) | **S7ce250-3** — `FitAddon` リサイズイベント → `daemon.Resize()` → PTY `creack/pty.Setsize` の伝播を実装 |
| xterm.js canvas のテキスト introspect 困難 — E2E で canvas 内容を直接検証できない | S1d2278-3 (scenario-3 / gui-spec) | **S7ce250-5** — E2E は `data-testid` ベースのステータス / ring buffer replay の byte-level 検証を採用し、 canvas text 依存を回避 |
| `spawnOnce sync.Once` — daemon が subprocess を 1 回しか起動できない | S1d2278-2 (scenario-4) | **S7ce250-1** — `respawnLoop` goroutine で `StateDead` 時に `claude --resume <lastSessionID>` を自動再起動 |
| ring buffer replay の微小 race — subscribe 前の live bytes を取りこぼす可能性 | S1d2278-2 (scenario-3 #4) | **S7ce250-1** — ring snapshot と subscriber 登録をアトミックに行う `SnapshotAndSubscribe` API を production daemon に追加 |
