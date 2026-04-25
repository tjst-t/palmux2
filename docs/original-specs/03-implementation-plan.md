# Palmux v2 実装計画

> 各フェーズは独立してビルド・動作確認可能な単位。フェーズ完了ごとに動く状態を維持する。

## フェーズ一覧

| Phase | 名前 | 概要 | 見積 |
|---|---|---|---|
| 0 | Scaffold | プロジェクト骨格、ビルドパイプライン | 0.5日 |
| 1 | Core Backend | ドメインモデル + ID生成 + tmux抽象化 + Store + 認証 + 設定 | 2.5日 |
| 2 | Terminal Attach | WebSocket ターミナル接続（最小UI） | 1.5日 |
| 3 | Repository/Branch UI | Drawer + TabBar + ブランチ管理 + Event WS | 2.5日 |
| 4 | Files Tab | ファイルブラウザ（閲覧 + 検索） | 1.5日 |
| 5 | Git Tab | Git status/diff/stage/discard | 2日 |
| 6 | Split Mode | 左右分割パネル + ミニセレクタ | 1.5日 |
| 7 | Toolbar | 修飾キー + ショートカット + コマンド + Claude モード | 1.5日 |
| 8 | Mobile | タッチジェスチャー + IME + PWA | 1.5日 |
| 9 | Notifications | Claude Code 通知連携 | 1日 |
| 10 | Polish | テーマ統合、連携機能、セキュリティ、ドキュメント | 1.5日 |

**合計: 約 17日**

---

## Phase 0: Scaffold

**ゴール**: `make dev` で空ページが表示される。

### タスク

1. `go mod init github.com/tjst-t/palmux`
2. `cmd/palmux/main.go` — CLI フラグ（pflag）、HTTP サーバー起動
3. `embed.go` — `frontend/dist` を embed.FS
4. `npm create vite@latest frontend -- --template react-ts`
5. `vite.config.ts` — `/api` を Go バックエンドにプロキシ（WS 含む `ws: true`）
6. CSS 変数ファイル `theme.css` — Fog パレット定義
7. Makefile: dev / build / build-linux / build-arm / test / lint
8. `.gitignore`, `.golangci.yml`, `eslint.config.js`

### 完了条件
- `make dev` → ブラウザで空の Palmux シェルが表示
- `make build` → シングルバイナリが生成される

---

## Phase 1: Core Backend

**ゴール**: ドメインモデル + Store が動作し、REST API でリポジトリ・ブランチ・タブを操作できる。

### タスク

1. **`internal/domain/`** — エンティティ + ID 生成
   - `repository.go`, `branch.go`, `tabset.go`, `tab.go`
   - `id.go` — `RepoSlugID()`, `BranchSlugID()`, `sha256Hex()`

2. **`internal/config/`** — 設定ファイル管理
   - `repos.go` — `~/.config/palmux/repos.json`（Open リポジトリ一覧 + starred）
   - `global.go` — `~/.config/palmux/settings.json`（グローバル設定）

3. **`internal/tmux/`** — Client interface + exec 実装
   - `client.go` — interface 定義
   - `exec_client.go` — exec.Command ベース
   - セッション: ListSessions, NewSession, KillSession, HasSession
   - ウィンドウ: ListWindows, NewWindow, KillWindowByName, RenameWindow, WindowIndexByName
   - セッション名ヘルパー: Encode/Decode
   - ウィンドウ命名: `palmux:{type}:{name}` の生成・パース

4. **`internal/tab/`** — TabProvider interface + Registry
   - `provider.go` — Provider interface, Registry
   - `claude/provider.go` — Claude タブ（terminal、OnBranchOpen で claude コマンド起動）
   - `bash/provider.go` — Bash タブ（terminal、Multiple=true）
   - ※ files / git の Provider は Phase 4, 5 で実装。この段階では claude + bash のみ

5. **`internal/ghq/`** — List, Root

6. **`internal/gwq/`** — worktree 作成/削除
   - `gwq.go`: `Add(repoPath, branchName)` → `gwq add -b {branchName}`
   - `Remove(repoPath, branchName)` → `gwq remove {branchName}`

7. **`internal/worktree/`** — worktree 一覧取得（読み取り専用）
   - `List(repoPath)` → `git worktree list --porcelain` パース
   - IsPrimary 判定: `.git/` ディレクトリ（ファイルではなく）を持つ worktree
   - `ListAllBranches(repoPath)` — ピッカー用（worktree 未作成含む）

8. **`internal/store/`** — 状態ストア
   - `store.go` — repos map, orphans, connections, tabRegistry
   - `sync_tmux.go` — 5秒ポーリング（セッション健全性 + group session 掃除）
   - `sync_worktree.go` — 30秒ポーリング（worktree 増減検出）
   - `events.go` — StoreEvent 定義 + EventHub

9. **`internal/auth/`** — Cookie 認証 + Bearer フォールバック
   - `GET /auth?token=xxx` → Set-Cookie → リダイレクト
   - `--token` なし時はオープンアクセス + 自動 Cookie

10. **`internal/server/`** — ルーティング + ハンドラ
    - `handler_auth.go` — `/auth`
    - `handler_repo.go` — repos, repos/available, open, close, star, unstar, clone
    - `handler_branch.go` — branches, branch-picker, open, close, merged
    - `handler_tab.go` — tabs CRUD（Registry 経由で Provider の RegisterRoutes を呼ぶ）
    - `handler_settings.go` — GET/PATCH /api/settings
    - `handler_orphan.go` — orphan-sessions

### 完了条件
- `GET /api/repos` → Open リポジトリ一覧を JSON で返す
- `POST /api/repos/{repoId}/open` → リポジトリが Open され worktree が検出される
- `POST /api/repos/{repoId}/branches/open` → worktree 作成 + tmux セッション作成
- `DELETE /api/repos/{repoId}/branches/{branchId}` → tmux kill + gwq remove（IsPrimary なら tmux kill のみ）
- tmux セッション命名が `_palmux_{repoId}_{branchId}` 形式
- ウィンドウが name ベースで管理されている
- 外部から tmux kill → 5秒以内に復元される（claude --resume）
- Cookie 認証が動作する（--token 指定時）
- テスト: tmux.Client モックを使った Store.Sync のユニットテスト

---

## Phase 2: Terminal Attach

**ゴール**: ブラウザで xterm.js ターミナルが表示され操作できる。

### タスク

1. **`internal/tmux/attach.go`**
   - Attach: window name → index 解決 → pty I/O
   - Session group 作成（`__grp_{connId}`）
   - 背圧制御: バッファ 256 チャネル + 最古ドロップ
   - ping/pong: 30秒間隔、60秒タイムアウト

2. **`internal/server/handler_ws.go`**
   - `WS /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/attach`
   - `WS /api/orphan-sessions/{name}/windows/{idx}/attach`
   - defer で group session kill + connection 削除

3. **`frontend/src/lib/`**
   - `api.ts` — fetch ラッパー（Cookie 自動送信）
   - `ws.ts` — WebSocket ラッパー（自動再接続、指数バックオフ 1s→30s）
   - `terminal-manager.ts` — 3段階キャッシュ（Active/Cached/Evicted、MAX_CACHED=6）
   - `tab-registry.ts` — TabRenderer レジストリ（registerTab / getRenderer）

4. **`frontend/src/tabs/`**
   - `terminal-view.tsx` — 共通ターミナルビュー（Claude / Bash 共用）
   - `tab-content.tsx` — tab.type に応じて TerminalView or レジストリから Renderer を取得して表示
5. **xterm.js 設定**
   - FitAddon + Unicode11Addon + WebLinksAddon
   - ResizeObserver → fit() → resize メッセージ送信
   - テーマ適用（CSS 変数読み取り）
   - 再接続オーバーレイ "Reconnecting..."
   - クリップボード: attachCustomKeyEventHandler で Ctrl+V インターセプト + OSC 52 ハンドリング

6. 最小 UI: ハードコードした repo/branch で1ターミナル表示

### 完了条件
- ブラウザでシェルが操作できる
- 入出力、リサイズが正常動作
- 切断時に再接続オーバーレイ → 自動復帰
- 2ブラウザタブから同時接続できる（session group）
- 大量出力でブロックしない（背圧制御）

---

## Phase 3: Repository/Branch UI

**ゴール**: Drawer でリポジトリ・ブランチを操作し、TabBar でタブを切り替えられる。

### タスク

1. **Event WS**
   - `WS /api/events` — Hub パターンで broadcast
   - `frontend/src/hooks/use-event-stream.ts` — 接続 + ストア更新 + 再接続時フルリロード

2. **Zustand ストア**
   - `frontend/src/stores/palmux-store.ts` — 全状態定義
   - `replaceState()` — 再接続時フルリロード
   - `applyEvent()` — 差分更新

3. **Drawer コンポーネント群**
   - `drawer.tsx` — ピン固定 / 幅調整 / PC 常時表示 / モバイルモーダル
   - `repo-list.tsx` / `repo-item.tsx` — リポジトリ展開
   - `branch-item.tsx` — 選択 / 通知バッジ / ソート
   - `branch-picker.tsx` — フィルタ付き全ブランチ一覧（open/local/remote）
   - `orphan-session-list.tsx`

4. **Header** — ブランチ名、接続状態、各種ボタン

5. **TabBar**
   - `tab-bar.tsx` — ドラッグスクロール（pointer 座標差分 > 5px でドラッグ判定）
   - `tab-item.tsx` — バッジ
   - `tab-context-menu.tsx`

6. **リポジトリ Open/Close UI**
   - 「Open Repository...」→ ghq 全リポジトリから選択

7. **ブランチ Open/Close**
   - ピッカーから Open（worktree 作成 + tmux 作成）
   - 右クリック/長押し → Close（マージ済み確認ダイアログ）

8. **localStorage 永続化**: Drawer ピン/幅、ブランチソート、最後のアクティブタブ

9. **ルーティング・履歴管理**
   - React Router v7: `BrowserRouter` + `Routes` + `Route`（`basename={basePath}`）
   - ルート定義: `/` → Redirect、`/:repoId/:branchId/:tabId/*` → MainLayout
   - `useParams` / `useNavigate` / `useSearchParams` でブランチ・タブ・Split 右パネルを管理
   - Go サーバー: `/api/`, `/auth` 以外の GET → `index.html`（SPA フォールバック）
   - 初回アクセス: URL パース → 該当ブランチ・タブを表示 / なければフォールバック

### 完了条件
- Drawer にリポジトリ・ブランチがツリー表示
- ブランチ選択で TabBar 更新 → ターミナル切り替え
- リポジトリ Open/Close、ブランチ Open/Close が動作
- Event WS でリアルタイム反映
- 外部から worktree 追加 → 30秒以内に Drawer に出現
- ブラウザの戻る/進むでブランチ・タブが切り替わる
- URL 直接アクセスで該当画面が表示される

---

## Phase 4: Files Tab

**ゴール**: ファイル閲覧・検索が動作する。

### タスク

1. `internal/tab/files/provider.go` — Provider interface 実装（NeedsTmuxWindow=false）
2. `internal/tab/files/browser.go` — ListDir, ReadFile, ReadRaw, Search, Grep
3. `internal/tab/files/security.go` — パストラバーサル防止
4. `internal/tab/files/handler.go` — HTTP ハンドラ（RegisterRoutes で登録）
5. `frontend/src/tabs/files/` — index.ts（registerTab）+ コンポーネント群
6. コンポーネント: files-view, file-list, breadcrumb, file-search, file-preview, grep-results
7. プレビュー: Markdown (react-markdown + remark-gfm), コード (highlight.js), 画像, drawio
8. grep → 行ジャンプ → ハイライト（3s フェードアウト）

### 完了条件
- ディレクトリ閲覧、プレビュー、検索、grep が動作
- パストラバーサルが 400 で拒否

---

## Phase 5: Git Tab

**ゴール**: diff 表示、hunk 単位の stage/unstage/discard。

### タスク

1. `internal/tab/git/provider.go` — Provider interface 実装（NeedsTmuxWindow=false）
2. `internal/tab/git/git.go` — Status, Log, Diff, Branches, Stage/Unstage/Discard（ファイル + hunk）
3. `internal/tab/git/diff.go` — 構造化 diff パーサー
4. `internal/tab/git/handler.go` — HTTP ハンドラ（RegisterRoutes で登録）
5. `frontend/src/tabs/git/` — index.ts（registerTab）+ コンポーネント群
6. コンポーネント: git-view, git-status, git-diff, git-hunk, git-log, git-branches
7. hunk アクションボタン、Discard 確認ダイアログ

### 完了条件
- diff がファイル・hunk 単位で表示
- hunk 単位 stage/unstage/discard が動作

---

## Phase 6: Split Mode

**ゴール**: 2タブ同時表示（ブランチまたぎ対応）。

### タスク

1. Zustand に Split 状態追加
2. `main-area.tsx`, `panel.tsx`, `divider.tsx`（20%〜80%、ドラッグ）
3. `right-panel-selector.tsx` — ミニセレクタバー（repo ▾ + branch ▾ + TabBar）
4. `Ctrl+Shift+←/→` パネル間フォーカス
5. 幅 < 900px で自動シングルパネル
6. localStorage: splitEnabled, splitRatio

### 完了条件
- 左右に異なるブランチのタブを同時表示
- Divider ドラッグ、キーボードフォーカス切り替え

---

## Phase 7: Toolbar

**ゴール**: 設定ファイルで全モードのボタン構成をカスタマイズ可能な4モードツールバー。

### タスク

1. `internal/commands/detect.go` — Makefile/package.json 等パース（30秒キャッシュ）
2. `GET /api/repos/{repoId}/branches/{branchId}/commands`
3. **Toolbar ボタン定義スキーマ**
   - `frontend/src/types/toolbar.ts` — ToolbarButton 型定義（type: modifier/key/ctrl-key/arrow/page/popup/fontsize/ime/speech/command）
   - デフォルト設定の定数定義
   - `GET /api/settings` の `toolbar` セクションから動的にボタン生成
4. コンポーネント: toolbar, normal-mode, shortcut-mode, command-mode, claude-mode
   - 各モードは `settings.toolbar.{mode}.rows` からボタン一覧を読み取って動的レンダリング
   - ボタン type ごとのレンダラーコンポーネント
5. 修飾キー: ワンショット/ロック（`use-modifiers.ts`）
6. 矢印キー長押し: 400ms → 80ms リピート
7. ポップアップキー: タップ + 上スワイプで別キー
8. Claude タブフォーカスで自動 Claude モード切り替え
9. 設定未指定時はデフォルト値で補完、部分上書き対応（deep merge）

### 完了条件
- 4モードが正常動作
- 修飾キーのワンショット/ロック
- コマンド自動検出
- settings.json の toolbar セクションを編集するとボタン構成が変わる
- claudeMode の rows を編集してスラッシュコマンドを追加/削除できる

---

## Phase 8: Mobile

**ゴール**: スマートフォンで快適操作。

### タスク

1. タッチジェスチャー: 上下スワイプ（スクロール）、左右（タブ切り替え）、ピンチ（フォント）
2. IME: none → direct → ime 循環、`inputmode="none"` / 通常 input
3. 音声入力: Web Speech API
4. ビューポートリサイズ対応（Visual Viewport API）
5. Drawer: モバイルモーダル（☰ トグル）
6. PWA: manifest.json + Service Worker（App Shell キャッシュのみ）

### 完了条件
- モバイルでターミナル操作、日本語入力、タブ切り替えが動作
- PWA としてフルスクリーン動作

---

## Phase 9: Notifications

**ゴール**: Claude Code 通知連携。

### タスク

1. `internal/notify/hub.go` — 通知ストア + broadcast + tmux session → branch 逆引き
2. `~/.config/palmux/env.<port>` 自動生成
3. Drawer: amber パルスドット、TabBar: Claude バッジ
4. Notification API + navigator.vibrate
5. Claude タブフォーカスで通知クリア

### 完了条件
- Hook POST → バッジ表示 + ブラウザ通知

---

## Phase 10: Polish

**ゴール**: プロダクション品質。

### タスク

1. テーマ統合: Fog パレット完全適用、Light/Dark
2. Portman 連携: ヘッダーボタン（ポートリースがある場合のみ）
3. GitHub 連携: ヘッダーボタン
4. Claude Restart（モデル選択ダイアログ）/ Resume
5. コンテキストメニュー: PC 右クリック / モバイル長押し
6. 画像ペースト: Blob 検出 → upload API
7. OSC 52 クリップボード同期
8. embed.FS のキャッシュ: Vite ハッシュ付きファイル名に依存
9. README.md、tmux 推奨設定ドキュメント

### 完了条件
- 仕様書の全機能が動作
- PC / モバイル両対応
- シングルバイナリ生成

---

## 依存関係

```
Phase 0 (Scaffold)
  │
  ▼
Phase 1 (Core Backend)
  │
  ├───────▼
  │  Phase 2 (Terminal)
  │      │
  │      ▼
  │  Phase 3 (Repo/Branch UI)
  │      │
  │      ├── Phase 4 (Files)      ┐
  │      ├── Phase 5 (Git)        │ 並行着手可能
  │      ├── Phase 6 (Split)      │
  │      └── Phase 7 (Toolbar)    ┘
  │               │
  │               ▼
  │           Phase 8 (Mobile)
  │
  ├── Phase 9 (Notifications) ← Phase 3 以降いつでも
  │
  └── Phase 10 (Polish) ← 全フェーズ後
```
