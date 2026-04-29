# 設計原則

このドキュメントは Palmux v2 の自律実装で参照される判断基準。
[`VISION.md`](VISION.md) と矛盾するルールは置かない（VISION が優先）。

## 判断に迷ったときのルール（優先順）

1. **CLI が真実 > Palmux が真実** — 会話履歴・権限設定・skills/agents は claude 側で管理する。Palmux はミラー描画と入力中継に徹し、CLI が持つべき状態を独自に持たない
2. **タブ間の対称性 > Claude 専用 API** — 通知・状態 pip 等の機構は全タブが乗れる汎用基盤として作る。Claude タブのために専用 API を生やさない
3. **Phase 区切り > 半完成投入** — 各 Phase 終了時点で main で動く。中間状態は残さない
4. **lazy spawn > eager spawn** — リソース消費（claude CLI spawn・tmux session 作成等）はユーザの能動操作トリガで開始。起動だけで API クォータを消費しない
5. **責務越境最小 > 便利さ** — `~/.claude/` や `.claude/settings.json` を Palmux が触るときは UI で明示同意フローを通す
6. **既存資産活用 > 新規実装** — 標準ライブラリ・既存コア機構の再利用を優先。車輪を再発明しない
7. **明示的 > 暗黙的** — ポート番号ハードコード禁止 (portman 経由)、マジックナンバー回避、暗黙の規約は避ける
8. **ナビゲーション保持 > UI state 保持** — ブラウザ戻る/進むで戻れる範囲は `pushState` で URL に乗せる。Drawer 開閉や Toolbar モード等の UI 一時状態は state にとどめる
9. **下書き / スナップショットは積極保持** — 画面遷移で消えるべきでないもの (composer 下書き、エージェント状態) は localStorage / module-level cache に置く
10. **mobile parity > desktop only** — 機能追加は両方でテスト。モバイルでだけ壊れる UI は出荷しない

## コーディング規約

### Go

- パッケージは `internal/` 以下のみ。`pkg/` は使わない
- エラーは `fmt.Errorf("xxx: %w", err)` でラップ。naked return 禁止
- `context.Context` は全パブリック関数の第 1 引数
- tmux は `internal/tmux.Client` interface 経由。`exec.Command("tmux", ...)` 直接呼び禁止
- フレームワーク不使用（標準 `net/http`）
- ログは `log/slog`
- JSON は `json.NewEncoder(w).Encode`

### TypeScript / React

- 関数コンポーネントのみ。`React.FC` は使わない
- 状態管理は Zustand。コンポーネント state は UI 一時状態のみ
- API は `lib/api.ts`、WebSocket は `lib/ws.ts`、xterm は `lib/terminal-manager.ts` に集約
- スタイルは CSS Modules `*.module.css`。CSS 変数はテーマファイルに集約
- import 順序: react → 外部ライブラリ → stores → hooks → components → lib → styles → types

### 命名

| 対象 | 規則 | 例 |
|---|---|---|
| Go ファイル | snake_case | `handler_branch.go` |
| Go 型/関数 | PascalCase | `ListBranches` |
| TS ファイル | kebab-case | `branch-item.tsx` |
| TS コンポーネント | PascalCase | `BranchItem` |
| CSS Modules キー | camelCase | `styles.branchItem` |
| API URL | kebab-case | `/api/repos/{repoId}/branches` |

## UI/UX の方針

- **Fog palette v2.1** (Dark/Light テーマ両対応、Accent `#7c8aff`)
- ターミナルフォント: Geist Mono / Cascadia Code / Fira Code
- UI フォント: Geist / Noto Sans JP
- レスポンシブブレークポイント: ≥900px / 600〜899px / <600px の 3 段階
- アクション後は即時フィードバック（楽観的更新）
- エラーメッセージは具体的に。「エラーが発生しました」のような曖昧な文言禁止
- モバイルでは Drawer モーダル化、Toolbar 常時表示、Split 無効
- 通知集約は Activity Inbox（Linear 的レイアウト）
- アイコンは Claude 関連で `<ClaudeIcon />` (8 本のレイ放射状 SVG)、絵文字に頼らない

## アーキテクチャの方針

- **タブモジュールシステム**: 新タブタイプ追加でコア変更不要。`tab.Provider` 実装 + `tabRegistry.Register` + `frontend/src/tabs/{type}/index.ts`
- **2 段階 Open**: Repository Open（`repos.json` 登録）→ Branch Open（worktree 存在で Open 扱い）
- **真実の所在**: `repos.json` → `git worktree list` → tmux（導出）
- **ID 体系**: Slug+Hash 方式（人間可読 + 衝突回避、URL 直書き可）
- **WebSocket**: ターミナル WS（pty I/O）+ イベント WS（broadcast）の 2 系統。再接続時はクライアントが REST でフル状態をリロード
- **設定 2 層**: グローバル `settings.json`（`~/.config/palmux/`）+ デバイス固有 `localStorage`（`palmux:` prefix）
- **認証 2 経路**: Cookie (HMAC-SHA256, 90 日) + Bearer フォールバック
- **タブ間ナビゲーション**: `frontend/src/lib/tab-nav.ts` 経由で URL を組む。各タブの URL 規約は内部実装

## やってはいけないこと

- `_palmux_` プレフィクスを変更しない（Orphan 判定に使用）
- ポート番号をソースに直接書かない（必ず portman 経由）
- `.env` を git commit しない
- 既存の API レスポンス形式を破壊的変更しない（パッチ間互換）
- worktree の作成/削除を `git worktree add/remove` で直接呼ばない（必ず gwq 経由）
- tmux コマンドを `exec.Command("tmux", ...)` で直接呼ばない（`internal/tmux.Client` 経由）
- IsPrimary worktree を `gwq remove` しない（リポ本体の close は tmux kill のみ、worktree は消さない）
- `~/.claude/` 配下を勝手に書き換えない（UI 経由の明示同意フロー必須）
- ホスト用 palmux2 をフォアグラウンドで再起動して Claude セッションを巻き込まない（host は `make serve` バックグラウンド、開発は別 worktree + `INSTANCE=`）
- マルチユーザ前提のコード（ユーザ別 namespace、共有制御）を入れない（VISION でスコープ外宣言済み）

## 自律実行 (autopilot / sprint auto) の検証ルール

`autopilot` や `sprint auto` がサブエージェントに実装を委譲するとき、**コンパイルと unit test だけで「完了」と判定しない**。フロントエンドとバックエンドの統合まで実機検証する。

### 必須検証ステップ

1. **dev インスタンスを立てる**
   `make serve INSTANCE=dev` で別ポート（portman 経由で割当）の独立 palmux2 を起動。ホスト用 palmux2 (`tmp/palmux.pid`) は絶対に触らない（再起動するとこのセッションが死ぬ）
2. **E2E (Playwright headless) で検証**
   実装した機能を `http://localhost:<dev-port>/...` 経由で叩く Playwright 試験を書き、最低限以下を確認:
   - UI が描画される（DOM クラスや要素を実機で確認）
   - クリック / 入力 → WebSocket フレーム送信 → バックエンド処理 → UI 反映 が通る
   - 関連 REST API エンドポイントが実際に呼ばれて期待値を返す
3. **dev インスタンスの後始末**
   - dev で起こした副作用（`sessions.json` の prefs 変更等）は最小限に
   - dev インスタンスの停止 (`make serve-stop INSTANCE=dev`) はサブエージェント終了時にやらなくてよい（次のサブエージェントが再利用）
4. **検証ログの提出**
   `decisions.md` に「実施した E2E 試験スクリプトの場所」「失敗ケースと再試行で得られた結果」「観測された副次バグ」を残す

### スキップが許される条件

以下のいずれかに該当する場合は E2E をスキップしてよい。**スキップ理由は `decisions.md` に明記する**:
- 機能が純粋に内部実装（ログ整形、型定義のみ、ビルドターゲット等）で UI / API 経路に出ない
- 試験に必要な外部サービスが利用不可（要料金 API、認証必須の外部 OAuth 等）で、unit / integration test で十分カバーされる
- E2E 環境の整備自体が別 sprint のスコープ（その場合は backlog に追加）

### 過去事例 (S001-S003 マイルストーンより)

最初は unit test のみで「完了」報告したが、後追い E2E で:
- `bypassPermissions` 動的切替時の CLI クラッシュ
- mode 切替時の WS / context lifecycle race

など複数の追加課題が判明。**ビルドが通ること = 動くこと、ではない**。autopilot で 3 sprint 連続実装するときは、各 sprint 完了時に dev インスタンスを叩いて確認する手順を必須にする。
