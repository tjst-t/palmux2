# CLAUDE.md — Palmux v2

> Claude Code がコードを生成・修正する際に参照するプロジェクトルール。

## 関連ドキュメント

詳細仕様は `docs/original-specs/` にある。実装で迷ったら原典を参照する。

| ドキュメント | 内容 | 参照タイミング |
|---|---|---|
| [01-architecture.md](docs/original-specs/01-architecture.md) | アーキテクチャ全容（ドメインモデル、API、WS、ルーティング、ADR） | 設計判断・API追加時 |
| [02-CLAUDE-rules.md](docs/original-specs/02-CLAUDE-rules.md) | 本ルールの原典 | このCLAUDE.mdで不足したとき |
| [03-implementation-plan.md](docs/original-specs/03-implementation-plan.md) | Phase 0〜10 の実装計画 | フェーズ着手時 |
| [04-ui-requirements.md](docs/original-specs/04-ui-requirements.md) | UI 詳細（Activity Inbox、⌘K パレット、Toolbar 2モード） | UI 実装時 |
| [05-claude-agent-tab.md](docs/original-specs/05-claude-agent-tab.md) | Claude タブ (stream-json + MCP) Phase 1 設計書 | Claude タブ実装時 |
| [06-claude-tab-roadmap.md](docs/original-specs/06-claude-tab-roadmap.md) | Claude タブ Phase 2+ ロードマップとコア共通化計画 | 機能拡張時 |

**仕様の優先度**: `04-ui-requirements.md` は v2.1 で `02-CLAUDE-rules.md` より新しい記述（Toolbar 2モード化、Activity Inbox、⌘K パレット追加）を含む。UI 実装時は 04 を優先。Phase 2 以降の Claude タブ拡張は 06 を主、04 を補助参照。

## 実装ステータス

現在: **Phase 0（Scaffold）実装中**。

### 確定済みプロジェクト規約

- **Go モジュールパス**: `github.com/tjst-t/palmux2`（リポジトリ名と一致。仕様書中の `github.com/tjst-t/palmux` は palmux v1 の名前で、v2 では palmux2 を使う）
- **設定ファイル保存先**: 開発時は `./tmp/`（既存の本物 palmux と干渉させないため）。本番想定の `~/.config/palmux/` は CLI フラグ `--config-dir` で切り替える
- **ポート**: dev サーバーは [portman](https://github.com/tjst-t/port-manager) 経由で起動する。`make dev` / `make serve` が `portman exec --name {svc} -- cmd --port {}` を呼ぶ。ソースにポート番号をハードコードしない
- **パッケージマネージャ**: npm（pnpm/bun も利用可だがデフォルトは npm）
- **Toolbar モード数**: 2モード（normal / claude）。02-CLAUDE-rules には normal/shortcut/claude/command の4モード記載があるが、04-ui-requirements v2.1 で 2モード化されており **04 が正**

### サーバー起動

- `make dev` — Vite dev + Go サーバー（hot reload）。portman 経由
- `make serve` — Go サーバー単体（embed 済みフロント）。portman 経由
- `make {dev,serve} INSTANCE=<name>` — portman 名にサフィックスを付け、ホスト用 instance と並走させる。詳細は [docs/development.md](docs/development.md)
- サーバー起動スクリプトを作成・変更する場合は portman ガイドを参照: https://raw.githubusercontent.com/tjst-t/port-manager/main/docs/CLAUDE_INTEGRATION.md
- `.env` ファイルは `.gitignore` に追加（git commit しない）

### palmux2 自身の中で palmux2 を開発するときの注意

ホスト用 palmux2（普段 Claude CLI を動かしている方）の `make serve` は **その palmux2 が管理している tmux セッション ＝ 自分が今操作している Claude CLI** を巻き込んで死ぬ。bootstrap 問題なので、開発は `gwq add -b dev` で別ブランチの worktree を切り、`INSTANCE=dev` で別 portman 名・別ポートで起動する。具体的な手順は [docs/development.md](docs/development.md) を参照。

実装が進んだら、本 CLAUDE.md を必要に応じて更新する（ディレクトリ構成の実態反映、確定した規約の追記、仕様変更の反映など）。

## プロジェクト概要

Palmux は Web ベースのターミナルクライアント。tmux セッションをブラウザから操作する。Go シングルバイナリ（フロントエンド embed）、PC / モバイル両対応。**複数の Claude Code を並行運用する**ユースケースを重視。

## 技術スタック

| レイヤー | 技術 |
|---|---|
| バックエンド | Go 1.23+, net/http, nhooyr.io/websocket |
| フロントエンド | React 19, TypeScript, Vite, React Router v7 |
| ターミナル | xterm.js 5.x |
| 状態管理 | Zustand |
| スタイリング | CSS Modules |
| ビルド | Makefile, embed.FS |

## ドメインモデル（最重要）

**tmux はバックエンドの実装詳細。UI やドメインロジックに漏れ出してはならない。**

```
Repository (ghq, Open されたもの)
└── Branch (git worktree の存在 = Open)
    └── TabSet (タブは Provider が生成。順序 = 登録順)
        ├── Claude  (terminal — protected, 1つ固定)
        ├── Files   (REST view — protected, 1つ固定, tmux window なし)
        ├── Git     (REST view — protected, 1つ固定, tmux window なし)
        └── Bash[]  (terminal — 1つ以上必須, 追加/削除可)
```

### タブモジュールシステム

新しいタブタイプの追加はコア変更不要。以下の手順で完結する:

1. `internal/tab/{type}/provider.go` — Provider interface 実装
2. `cmd/palmux/main.go` — `tabRegistry.Register({type}.New())`
3. `frontend/src/tabs/{type}/index.ts` — `registerTab(...)` + コンポーネント

Provider interface: `Type()`, `DisplayName()`, `Protected()`, `Multiple()`, `NeedsTmuxWindow()`, `OnBranchOpen()`, `OnBranchClose()`, `RegisterRoutes()`

### 2段階 Open モデル

1. **Repository Open**: repos.json に登録。以降そのリポジトリの worktree 変更を追跡
2. **Branch Open**: worktree が存在すれば Open。tmux セッションは worktree から導出

ソースオブトゥルース: `repos.json`（Open リポジトリ）→ `git worktree list`（Open ブランチ）→ tmux（導出）

## ディレクトリ構成（計画）

```
palmux/
├── cmd/palmux/main.go
├── internal/
│   ├── domain/          # エンティティ + ID 生成。外部依存ゼロ
│   ├── config/          # repos.json + settings.json
│   ├── store/           # メモリ状態ストア + ハイブリッドポーリング
│   ├── tmux/            # tmux Client interface + exec 実装
│   ├── tab/             # タブモジュールシステム
│   │   ├── provider.go  # Provider interface + Registry
│   │   ├── claude/      # Claude タブ（terminal 系）
│   │   ├── bash/        # Bash タブ（terminal 系、複数可）
│   │   ├── files/       # Files タブ（REST 系、browser + security + handler）
│   │   └── git/         # Git タブ（REST 系、git + diff + handler）
│   ├── ghq/             # ghq list
│   ├── gwq/             # gwq add/remove（worktree 操作）
│   ├── worktree/        # git worktree list（読み取り専用）
│   ├── notify/          # Claude Code 通知ハブ
│   ├── commands/        # Makefile/package.json コマンド自動検出
│   ├── auth/            # Cookie + Bearer 認証
│   └── server/          # HTTP ハンドラ + ルーティング（コア部分のみ）
├── frontend/src/
│   ├── components/      # React コンポーネント（Drawer, Header, TabBar 等）
│   ├── tabs/            # タブモジュール（1タブタイプ = 1ディレクトリ）
│   │   ├── terminal-view.tsx  # 共通ターミナルビュー（Claude / Bash 共用）
│   │   ├── files/       # Files タブ（index.ts で registerTab）
│   │   └── git/         # Git タブ（index.ts で registerTab）
│   ├── stores/          # Zustand ストア
│   ├── hooks/           # カスタムフック
│   ├── lib/             # api client, ws, terminal-manager, tab-registry
│   ├── styles/          # CSS Modules + テーマ変数
│   └── types/
├── embed.go
├── Makefile
└── go.mod
```

## ID 体系

Slug+Hash 方式。人間可読 + 衝突回避。

```
Repository ID: tjst-t--palmux--a1b2    (owner--repo--hash4)
Branch ID:     feature--new-ui--7a8b   (branch_safe--hash4)
Tab ID:        claude | files | git | bash:bash | bash:bash-2
```

- hash4 = SHA256 先頭4文字
- API URL にそのまま使える（スラッシュなし）
- グローバルキー: `{repoId}/{branchId}` または `{repoId}/{branchId}/{tabId}`

## tmux 命名規則

```
セッション: _palmux_{repoId}_{branchId}
ウィンドウ: palmux:{type}:{name}

例:
  _palmux_tjst-t--palmux--a1b2_main--e5f6
  palmux:claude:claude
  palmux:bash:bash
  palmux:bash:my-server
```

- `_palmux_` プレフィクスで Palmux 管理セッション識別
- ウィンドウは **name でルックアップ**（index に依存しない）
- Palmux が命名を独占管理しユニーク性を保証

## コーディング規約

### Go

- `internal/` 以下にすべてのパッケージ。`pkg/` は使わない
- エラーは `fmt.Errorf("xxx: %w", err)` でラップ。naked return 禁止
- `context.Context` は全パブリック関数の第1引数
- tmux コマンドは必ず `internal/tmux.Client` interface 経由。`exec.Command("tmux", ...)` 直接呼び禁止
- ハンドラは `http.HandlerFunc`。フレームワーク不使用
- JSON: `json.NewEncoder(w).Encode`
- ログ: `log/slog`

### TypeScript / React

- 関数コンポーネントのみ
- `React.FC` 不使用。Props を明示的に型定義
- 状態は Zustand に集約。コンポーネント state は UI 一時状態のみ
- API クライアント: `lib/api.ts` に集約
- WebSocket: `lib/ws.ts` に集約
- xterm.js: `lib/terminal-manager.ts` に集約
- CSS Modules: `*.module.css`。CSS 変数はテーマファイルに集約
- import 順序: react → 外部ライブラリ → stores → hooks → components → lib → styles → types

### 命名規則

| 対象 | 規則 | 例 |
|---|---|---|
| Go ファイル | snake_case | `handler_branch.go` |
| Go 型/関数 | PascalCase | `ListBranches` |
| TS ファイル | kebab-case | `branch-item.tsx` |
| TS コンポーネント | PascalCase | `BranchItem` |
| CSS Modules | camelCase | `styles.branchItem` |
| API URL | kebab-case | `/api/repos/{repoId}/branches` |

## ルーティング・履歴管理

URL スキーム: `/{repoId}/{branchId}/{tabId}`。Files/Git はサブパスあり。

```
/tjst-t--palmux--a1b2/main--e5f6/claude
/tjst-t--palmux--a1b2/main--e5f6/files/src/main.go
/tjst-t--palmux--a1b2/main--e5f6/git/status
/?right=...                                          # Split 右パネル
```

- ブランチ・タブ・Files パス・Git ビューの切り替えは `history.pushState`
- Drawer 開閉、モーダル、Toolbar モード等の UI 一時状態は pushState しない
- ブラウザの戻る/進むで画面遷移可能
- React Router v7 を使用（`BrowserRouter`, `Routes`, `Route`, `useNavigate`, `useParams`）
- loader/action は使わない
- `--base-path` 対応: `<BrowserRouter basename={basePath}>`
- Go サーバー: `/api/`, `/auth` 以外の GET は SPA フォールバック（`index.html`）
- ストアは直接 navigate を呼ばない。URL 管理はコンポーネント層で行う

## 認証

```
--token なし: オープンアクセス。自動 Cookie セット。通知 API も認証不要
--token あり: Cookie（HttpOnly, SameSite=Strict, 90日）+ Bearer フォールバック
```

- Cookie 名: `palmux_session`（HMAC-SHA256 署名）
- 通知 API（外部 Hook）: `--token` ありなら Bearer 必須
- 初回認証: `GET /auth?token=xxx` → Cookie 発行 → `/` リダイレクト

## 設定管理

```
グローバル（全デバイス共有）: ~/.config/palmux/settings.json → GET/PATCH /api/settings
デバイス固有（ブラウザ）:    localStorage（プレフィクス palmux:）
```

グローバル設定には Toolbar の **2モード**（normal / claude）のボタン構成が含まれる。各ボタンは `type`（modifier, key, ctrl-key, arrow, page, popup, fontsize, ime, speech, command）で定義。設定ファイルで省略されたモードはデフォルト値で補完する。claudeMode の rows を編集すればスラッシュコマンドのカスタマイズが可能。

> v2.1 で Toolbar は 2モード（normal / claude）に簡略化された。コマンド検索は ⌘K パレットの `>` プレフィクスに移行。

## WebSocket

```
ターミナル:  WS /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/attach
イベント:   WS /api/events （broadcast）
```

ターミナル WS: input/resize → output（バイナリ pty I/O）
イベント WS: JSON イベント（branch.opened, tab.added, notification 等）
再接続時: クライアントが REST でフル状態リロード

## TerminalManager キャッシュ

```
Active(表示中) → Cached(非表示, WS維持, 上限6) → Evicted(dispose済み, 再表示時に再接続)
```

scrollback: デフォルト 5000行、設定可能

## UI コンポーネント要件（v2.1 で追加）

- **Activity Inbox**: 全ブランチのエージェントイベントを集約。Header の 🔔 ボタンで開く。インラインアクション（y/n/Resume）対応
- **⌘K コマンドパレット**: VS Code スタイル。Workspaces / Files / Commands / Slash の横断検索（プレフィクス: `@` `/` `>` `:`）
- **Toolbar 2モード**: normal / claude（Claude タブフォーカスで自動切替）

詳細は [04-ui-requirements.md](docs/original-specs/04-ui-requirements.md) 参照。

## テーマ（Fog パレット v2.1）

- Accent: `#7c8aff`、Accent Light: `#9ba6ff`、Accent Dark: `#5c6ae0`
- ターミナルフォント: `"Geist Mono", "Cascadia Code", "Fira Code", monospace`
- UI フォント: `"Geist", "Noto Sans JP", -apple-system, BlinkMacSystemFont, sans-serif`
- Dark: bg `#0f1117`, surface `#13151c`, elevated `#1a1c25`, border `#1e2028`, fg `#d4d4d8`, fg-muted `#8b8fa0`, fg-dim `#6b6f7b`, fg-faint `#4a4e5c`, fg-ghost `#3d4150`
- Light: bg `#fafafa`, surface `#f4f4f5`, elevated `#ffffff`, border `#e4e4e7`, fg `#18181b`, fg-muted `#52525b`
- Terminal: bg `#0c0e14`, green `#64d2a0`, yellow `#e8b45a`, blue `#7c8aff`, gray `#6b6f7b`
- Semantic: success `#64d2a0`, warning `#f59e0b`（パルスアニメ付き）, error `#ef4444`, info `#7c8aff`

## レスポンシブブレークポイント

| 幅 | レイアウト |
|---|---|
| ≥ 900px | PC フル。Drawer ピン、Split 可 |
| 600〜899px | PC コンパクト。Drawer ピン可、Split 無効 |
| < 600px | モバイル。Drawer モーダル、Toolbar 常時、Split 無効 |

## セキュリティルール

- Files API: worktree 相対パスのみ。`../` → 400。symlink → EvalSymlinks で検証
- 認証: 全 `/api/*` に Cookie or Bearer
- 接続制限: `--max-connections` でブランチあたり WS 上限

## テスト方針

- Go: `*_test.go`。`tmux.Client` は interface でモック可能
- TS: Vitest で stores / lib のユニットテスト
- E2E: 手動確認主体

## ビルド

```bash
make dev          # vite dev + air (Go hot reload)
make build        # プロダクション（embed シングルバイナリ）
make build-linux  # Linux amd64
make build-arm    # Linux arm64
make test         # Go + TS
make lint         # golangci-lint + eslint
```

## 注意事項

- `_palmux_` プレフィクスは変更禁止（Orphan 判定に使用）
- Claude タブ = 常に tmux window name `palmux:claude:claude`
- リポジトリ本体（IsPrimary）の Close は tmux kill のみ（worktree は消さない）。ブランチ名は main とは限らない
- IsPrimary の判定: `git worktree list --porcelain` で `.git/` ディレクトリ（ファイルではなく）を持つ worktree
- worktree の作成/削除は `gwq` コマンド経由。`git worktree add/remove` を直接呼ばない
- 起動時に tmux, ghq, gwq, git の存在をチェック。なければエラー終了
- 複数デバイス同時接続は tmux session group。attach 時に `__grp_{connId}` 作成、detach 時に kill
- Files/Git タブは tmux window を持たない。REST API のみ
- localStorage キープレフィクス: `palmux:`
- tmux 復元時は `claude --resume` で起動
- pty → WS の背圧制御: バッファ 256 チャネル、満杯時は最古ドロップ

## クリップボード

- **コピー**: tmux の OSC 52 → xterm.js がハンドリング → `navigator.clipboard.writeText()`
- **テキストペースト**: `Ctrl+V` / `Cmd+V` を `attachCustomKeyEventHandler` でインターセプト（`\x16` を送らない）→ `navigator.clipboard.readText()` → WS input
- **画像ペースト**: paste イベントで Blob 検出 → `POST /api/upload` → サーバーが `imageUploadDir`（デフォルト `/tmp/palmux-uploads/`）に保存 → 絶対パスを WS input として送信
- `imageUploadDir` はグローバル設定（`settings.json`）で変更可能

## このファイルの更新ポリシー

実装が進む過程で本 CLAUDE.md は更新する。更新の指針:

- **更新する**: 確定した規約・命名・パッケージ境界・実装ステータス
- **更新しない**: ロジックの詳細（コードを読めば分かる）、一時的な作業状態
- **原典との整合**: 本ルールと `docs/original-specs/` で食い違いが出たら原典を優先するか、原典自体を更新するかを明示する
