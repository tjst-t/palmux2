# Palmux v2 アーキテクチャ設計書

> 作成日: 2026-04-03
> ステータス: Draft v2（全検討項目反映済み）

## 1. 設計方針

### 1.1 コア原則

1. **ドメインモデルファースト** — Repository > Branch > TabSet が一級概念。tmux はバックエンドの実装詳細として隠蔽する
2. **Tab Backend の抽象化** — ターミナルタブ（Bash/Claude）と UI タブ（Files/Git）を統一的な Tab インターフェースで扱う
3. **シングルバイナリ** — Go embed.FS でフロントエンドを埋め込み
4. **PC / モバイル両対応** — アダプティブレイアウト（画面幅に応じてモード切り替え）

### 1.2 現行からの主な変更点

| 項目 | 現行 (v1) | 新設計 (v2) |
|---|---|---|
| 一級概念 | tmux session/window | Repository / Branch / TabSet |
| Drawer | tmux セッション一覧 | Repository > Branch ツリー |
| API URL | `/api/sessions/{session}/...` | `/api/repos/{repoId}/branches/{branchId}/...` |
| ID 体系 | tmux セッション名 | Slug+Hash ID（人間可読 + 衝突回避） |
| 状態管理 | tmux の実態に追従 | repos.json + worktree list + tmux を導出 |
| 認証 | Bearer トークンのみ | Cookie ベース + Bearer フォールバック |
| 設定保存 | localStorage のみ | グローバル(サーバー) + デバイス固有(localStorage) |
| Split | tmux pane ベース | フロントエンド独自パネル |

## 2. ドメインモデル

### 2.1 概念図

```
Palmux Server
│
├─ Open Repositories（repos.json で管理）
│  │
│  ├─ Repository: tjst-t--palmux--a1b2
│  │  ├─ Branch: develop--e5f6            ← リポジトリ本体（IsPrimary, ブランチ名は任意）
│  │  │  └─ TabSet (tmux: _palmux_..._develop--e5f6)
│  │  │     ├─ palmux:claude:claude      ← tmux window
│  │  │     ├─ files                     ← REST view（tmux window なし）
│  │  │     ├─ git                       ← REST view（tmux window なし）
│  │  │     └─ palmux:bash:bash          ← tmux window
│  │  │
│  │  └─ Branch: feature--new-ui--7a8b   ← linked worktree（gwq で作成）
│  │     └─ TabSet (tmux: _palmux_..._feature--new-ui--7a8b)
│  │        ├─ palmux:claude:claude
│  │        ├─ files
│  │        ├─ git
│  │        ├─ palmux:bash:bash
│  │        └─ palmux:bash:bash-2
│  │
│  └─ Repository: tjst-t--ansible-nas--c3d4
│     └─ ...
│
└─ Orphan Sessions（_palmux_ プレフィクスなし tmux セッション）
   ├─ dev-server
   └─ scratch
```

### 2.2 エンティティ定義

```go
// Repository — ghq 管理リポジトリ（Open 状態のもの）
type Repository struct {
    ID           string   // "tjst-t--palmux--a1b2"（Slug+Hash）
    GHQPath      string   // "github.com/tjst-t/palmux"（ghq 相対パス）
    FullPath     string   // 絶対パス
    Starred      bool     // Drawer の Starred セクションに表示
    OpenBranches []Branch // Palmux で開いているブランチ
}

// Branch — 開いているブランチ（= worktree が存在する）
type Branch struct {
    ID           string    // "feature--new-ui--7a8b"（Slug+Hash）
    Name         string    // "feature/new-ui"（git ブランチ名）
    WorktreePath string    // worktree 絶対パス
    RepoID       string    // 親 Repository.ID
    IsPrimary    bool      // リポジトリ本体のチェックアウト（.git/ ディレクトリを持つ）
    TabSet       TabSet
    LastActivity time.Time
}

// TabSet — ブランチに紐づくタブ群
type TabSet struct {
    TmuxSession string // tmux セッション名
    Tabs        []Tab  // Provider が生成したタブの一覧（順序保持）
}

// Tab — タブの統一型（API レスポンス / Store 共通）
type Tab struct {
    ID          string `json:"id"`                   // "claude", "files", "git", "bash:bash"
    Type        string `json:"type"`                 // Provider の Type(): "claude", "bash", "files", "git"
    Name        string `json:"name"`                 // 表示名
    Protected   bool   `json:"protected"`            // 削除不可
    WindowName  string `json:"windowName,omitempty"` // tmux window name（terminal 系のみ）
    Multiple    bool   `json:"multiple"`             // 複数インスタンス可（Bash 等）
}
```

### 2.3 ID 体系（Slug+Hash）

全エンティティに人間可読かつ衝突のない ID を付与する。

```go
// internal/domain/id.go

// RepoSlugID: "github.com/tjst-t/palmux" → "tjst-t--palmux--a1b2"
func RepoSlugID(ghqRelPath string) string {
    parts := strings.Split(ghqRelPath, "/")
    slug := strings.Join(parts[1:], "--") // host 除去、/ → --
    hash := sha256Hex(ghqRelPath, 4)
    return slug + "--" + hash
}

// BranchSlugID: "feature/new-ui" → "feature--new-ui--7a8b"
func BranchSlugID(repoFullPath, branchName string) string {
    slug := strings.ReplaceAll(branchName, "/", "--")
    hash := sha256Hex(repoFullPath+":"+branchName, 4)
    return slug + "--" + hash
}

func sha256Hex(input string, hexLen int) string {
    h := sha256.Sum256([]byte(input))
    return hex.EncodeToString(h[:])[:hexLen]
}
```

### 2.4 tmux セッション / ウィンドウ命名規則

```
セッション名: _palmux_{repoId}_{branchId}
  例: _palmux_tjst-t--palmux--a1b2_main--e5f6

ウィンドウ名:  palmux:{type}:{name}
  例: palmux:claude:claude
      palmux:bash:bash
      palmux:bash:bash-2
      palmux:bash:my-server
```

- `_palmux_` プレフィクスで Palmux 管理セッションを識別
- ウィンドウは name でルックアップ（index に依存しない）
- ユニーク性は Palmux が命名時に保証

```go
// 次の Bash ウィンドウ名を決定
func (ts *TabSet) NextBashWindowName() string {
    existing := map[string]bool{}
    for _, b := range ts.Bash {
        existing[b.WindowName] = true
    }
    if !existing["palmux:bash:bash"] {
        return "palmux:bash:bash"
    }
    for i := 2; ; i++ {
        name := fmt.Sprintf("palmux:bash:bash-%d", i)
        if !existing[name] {
            return name
        }
    }
}
```

### 2.5 TabProvider（タブモジュールシステム）

タブタイプの追加は Provider interface の実装 + レジストリへの登録で完結する。コア側の変更は不要。

```go
// internal/tab/provider.go

type Provider interface {
    // メタ情報
    Type() string              // "claude", "bash", "files", "git", "logs", ...
    DisplayName() string       // "Claude", "Files", ...
    Protected() bool           // 削除不可か
    Multiple() bool            // 複数インスタンス可か（Bash 等）
    NeedsTmuxWindow() bool     // tmux ウィンドウが必要か

    // ライフサイクル
    OnBranchOpen(ctx context.Context, branch *domain.Branch) ([]domain.Tab, error)
    OnBranchClose(ctx context.Context, branch *domain.Branch) error

    // API ルート登録（REST 系タブのみ）
    RegisterRoutes(mux *http.ServeMux, prefix string)
    // prefix = /api/repos/{repoId}/branches/{branchId}/{type}
}

// Registry — Provider の登録・管理
type Registry struct {
    providers []Provider // 登録順 = TabBar のデフォルト表示順
}

func (r *Registry) Register(p Provider) { r.providers = append(r.providers, p) }
```

組み込みプロバイダ:

| Provider | Type | tmux | Multiple | Protected | 概要 |
|---|---|---|---|---|---|
| `claude` | `"claude"` | ✓ | ✗ | ✓ | Claude Code ターミナル |
| `files` | `"files"` | ✗ | ✗ | ✓ | ファイルブラウザ（REST） |
| `git` | `"git"` | ✗ | ✗ | ✓ | Git ブラウザ（REST） |
| `bash` | `"bash"` | ✓ | ✓ | ✗ | Bash ターミナル |

登録（起動時）:

```go
// cmd/palmux/main.go
tabRegistry := tab.NewRegistry()
tabRegistry.Register(claude.New())
tabRegistry.Register(files.New())
tabRegistry.Register(git.New())
tabRegistry.Register(bash.New())
// 将来: tabRegistry.Register(logs.New())
```

ブランチ Open 時の TabSet 構築:

```go
func (s *Store) openBranch(ctx context.Context, ...) (*domain.Branch, error) {
    // ...
    var tabs []domain.Tab
    for _, provider := range s.tabRegistry.Providers() {
        providerTabs, err := provider.OnBranchOpen(ctx, branch)
        if err != nil { return nil, err }
        tabs = append(tabs, providerTabs...)
    }
    branch.TabSet.Tabs = tabs
    // ...
}
```

#### 新しいタブタイプの追加手順

1. `internal/tab/{type}/provider.go` を作成（Provider interface を実装）
2. REST API が必要なら `handler.go` も作成
3. `cmd/palmux/main.go` に `tabRegistry.Register({type}.New())` を追加
4. `frontend/src/tabs/{type}/` にコンポーネント作成
5. `frontend/src/tabs/{type}/index.ts` で `registerTab(...)` を呼ぶ

コアの store, server, tab-content は変更不要。

### 2.6 Tab ID の体系

Tab ID = tmux window name から `palmux:` プレフィクスを除いた部分。Files/Git は固定。

| tmux window name | Tab ID | 表示名 |
|---|---|---|
| `palmux:claude:claude` | `claude` | Claude |
| （なし） | `files` | Files |
| （なし） | `git` | Git |
| `palmux:bash:bash` | `bash:bash` | Bash |
| `palmux:bash:bash-2` | `bash:bash-2` | Bash 2 |
| `palmux:bash:my-server` | `bash:my-server` | my-server |

グローバルキー（タブキャッシュ等）: `{repoId}/{branchId}/{tabId}`

### 2.6 ブランチのライフサイクル

**2段階 Open モデル**: Repository にも Branch にも Open / Close がある。

#### Repository の Open / Close

```
Open:  repos.json に追加 → Sync が全 worktree を検出 → tmux セッション作成
Close: 全ブランチの tmux セッション kill → repos.json から削除（worktree は残す）
```

#### Branch の Open / Close

**Open 中のリポジトリにおいて、worktree の存在 = ブランチが開いている。**

#### ソースオブトゥルース

```
~/.config/palmux/repos.json  → Open 中のリポジトリ一覧 + スター状態
git worktree list            → 各リポジトリの開いているブランチ
tmux sessions                → 上記から導出（なければ復元）
```

repos.json の構造:

```json
[
  { "id": "tjst-t--palmux--a1b2", "ghqPath": "github.com/tjst-t/palmux", "starred": true },
  { "id": "tjst-t--ansible-nas--c3d4", "ghqPath": "github.com/tjst-t/ansible-nas", "starred": false }
]
```

#### ブランチの4状態（ピッカー用）

| 状態 | 条件 | 表示場所 |
|---|---|---|
| open | worktree あり（Open リポジトリ内） | Drawer |
| available | worktree あり（リポジトリ未 Open） | 表示しない |
| local | ローカルブランチのみ（worktree なし） | ピッカー |
| remote | リモートブランチのみ | ピッカー |

#### Open フロー

```
POST /api/repos/{repoId}/branches/open { "branchName": "feature/new-ui" }

1. 既存 worktree があるか？
   - ある → 再利用
   - ない → gwq add -b {branchName} で worktree 作成
2. tmux new-session: claude コマンド起動（window 0）+ bash（window 1）
3. Store に Branch 追加 → Event "branch.opened"
```

#### Close フロー

```
DELETE /api/repos/{repoId}/branches/{branchId}

1. tmux kill-session
2. IsPrimary（リポジトリ本体）? → worktree は残す / それ以外 → gwq remove
3. Store から Branch 削除 → Event "branch.closed"
```

#### 全障害シナリオ

| シナリオ | 動作 |
|---|---|
| 正常な Open | gwq add → tmux 作成 |
| 正常な Close | tmux kill → gwq remove |
| 外部から worktree 追加 | syncWorktree(30s) が検出 → tmux セッション自動作成 |
| 外部から worktree 削除 | syncWorktree(30s) が検出 → ゾンビ tmux kill → Drawer から消える |
| 外部から tmux kill | syncTmux(5s) が検出 → worktree 残存 → tmux 復元（claude --resume） |
| tmux サーバーごとクラッシュ | syncTmux(5s) で全 Open ブランチの tmux を再作成 |
| Palmux 再起動 | repos.json + worktree list から状態を完全復元 |
| Primary ブランチ Close | tmux kill のみ（リポジトリ本体の worktree は消さない） |

### 2.7 Claude コマンドの起動

```go
type ClaudeOpts struct {
    Model  string // 空ならデフォルト
    Resume bool
}

func buildClaudeCommand(opts ClaudeOpts) string {
    args := []string{"claude"}
    if opts.Resume {
        args = append(args, "--resume")
    }
    if opts.Model != "" {
        args = append(args, "--model", opts.Model)
    }
    return strings.Join(args, " ")
}
```

| シナリオ | コマンド |
|---|---|
| 新規 Open | `claude`（デフォルトモデル） |
| tmux 復元時 | `claude --resume` |
| UI から Restart | `claude --model {userChoice}` |
| UI から Resume | `claude --resume` |

cwd: 常に worktree ルートディレクトリ（tmux の `-c` オプション）

### 2.8 Orphan Sessions

`_palmux_` プレフィクスがない tmux セッションは v1 互換の簡易モードで表示。

- ウィンドウ一覧をフラット表示（TabSet 構造なし）
- Files / Git / Claude タブなし
- ターミナル attach のみ可能
- ウィンドウの追加 / 削除 / リネーム対応

## 3. バックエンドアーキテクチャ

### 3.1 パッケージ構成

```
palmux/
├── cmd/palmux/
│   └── main.go
├── internal/
│   ├── domain/          # エンティティ + ID 生成
│   ├── config/          # repos.json + settings.json
│   ├── store/           # 状態ストア + ハイブリッドポーリング
│   ├── tmux/            # tmux Client interface + 実装
│   ├── tab/             # タブモジュールシステム
│   │   ├── provider.go  # Provider interface + Registry
│   │   ├── claude/      # Claude タブ（terminal 系）
│   │   │   └── provider.go
│   │   ├── bash/        # Bash タブ（terminal 系、複数可）
│   │   │   └── provider.go
│   │   ├── files/       # Files タブ（REST 系）
│   │   │   ├── provider.go
│   │   │   ├── browser.go
│   │   │   ├── security.go
│   │   │   └── handler.go
│   │   └── git/         # Git タブ（REST 系）
│   │       ├── provider.go
│   │       ├── git.go
│   │       ├── diff.go
│   │       └── handler.go
│   ├── ghq/             # ghq list
│   ├── gwq/             # gwq add/remove（worktree 操作）
│   ├── worktree/        # git worktree list（読み取り専用）
│   ├── notify/          # Claude Code 通知ハブ
│   ├── commands/        # コマンド自動検出（30秒キャッシュ）
│   ├── auth/            # Cookie + Bearer 認証
│   └── server/          # HTTP ハンドラ + ルーティング（コア部分のみ）
├── frontend/
├── embed.go
├── Makefile
└── go.mod
```

### 3.2 状態ストア

```go
type Store struct {
    mu            sync.RWMutex
    repos         map[string]*domain.Repository
    orphans       []OrphanSession
    notifications map[string]domain.Notification
    connections   map[string]*Connection // group session 管理

    tmux     tmux.Client
    ghq      ghq.Client
    config   *config.Config
    eventHub *EventHub
}
```

#### ハイブリッドポーリング

```go
go s.syncTmuxLoop(ctx, 5*time.Second)      // tmux セッション健全性
go s.syncWorktreeLoop(ctx, 30*time.Second)  // worktree 増減検出
```

- **syncTmux(5s)**: `tmux list-sessions`(1コマンド) → 欠損復元、ゾンビ kill、group session 掃除
- **syncWorktree(30s)**: 各 Open リポジトリで `git worktree list` → 新規/消失検出
- **Palmux 自身の操作**: API ハンドラ内で即時 Store 更新 + Event 発行

### 3.3 tmux 抽象化

```go
type Client interface {
    ListSessions(ctx context.Context) ([]Session, error)
    NewSession(ctx context.Context, opts NewSessionOpts) error
    KillSession(ctx context.Context, name string) error
    HasSession(ctx context.Context, name string) (bool, error)

    ListWindows(ctx context.Context, session string) ([]Window, error)
    NewWindow(ctx context.Context, session string, opts NewWindowOpts) error
    KillWindowByName(ctx context.Context, session, windowName string) error
    RenameWindow(ctx context.Context, session, oldName, newName string) error
    WindowIndexByName(ctx context.Context, session, windowName string) (int, error)

    Attach(ctx context.Context, session, windowName string) (io.ReadWriteCloser, ResizeFunc, error)
    NewGroupSession(ctx context.Context, target, groupName string) error
}
```

#### 背圧制御

```go
// pty → WebSocket の間にバッファ付きチャネル
outCh := make(chan []byte, 256)

// プロデューサー（pty read — ブロックしない）
select {
case outCh <- data:
default:
    <-outCh      // 最古をドロップ
    outCh <- data // 最新を投入
}

// コンシューマー（WebSocket write — 5秒タイムアウト）
```

#### Session Group ライフサイクル

- attach 時: `_palmux_...__grp_{connId}` group session 作成
- 正常切断: `defer` で group session kill + Store から connection 削除
- 異常切断: サーバーから 30秒間隔 ping、60秒で切断判定 → kill
- 最終防御: syncTmux(5s) で Store に対応 connId がないゾンビ group session を kill

### 3.4 認証

| モード | 条件 | 動作 |
|---|---|---|
| オープン | `--token` 未指定 | 全アクセス許可。自動 Cookie セット |
| 保護 | `--token xxx` 指定 | Cookie or Bearer で認証 |

Cookie: `palmux_session`、HMAC-SHA256 署名、HttpOnly、SameSite=Strict、90日有効

通知 API: `--token` ありの場合は Bearer 必須。

```
# 起動コンソール出力（--token 指定時のみ）
Palmux listening on http://0.0.0.0:8080
Authenticate at: http://localhost:8080/auth?token=a1b2c3d4...
```

### 3.5 API 設計

```
# === 認証 ===
GET    /auth?token=xxx                                  # Cookie 発行 → / にリダイレクト

# === Repository ===
GET    /api/repos                                       # Open 中のリポジトリ一覧
GET    /api/repos/available                             # ghq 全リポジトリ（Open 候補）
POST   /api/repos/{repoId}/open                         # リポジトリを Open
POST   /api/repos/{repoId}/close                        # リポジトリを Close
POST   /api/repos/{repoId}/star                         # スター付与
POST   /api/repos/{repoId}/unstar                       # スター解除
POST   /api/repos/clone                                 # ghq clone

# === Branch ===
GET    /api/repos/{repoId}/branches                     # 開いているブランチ一覧
GET    /api/repos/{repoId}/branch-picker                # 全ブランチ（open/local/remote）
POST   /api/repos/{repoId}/branches/open                # ブランチ Open
DELETE /api/repos/{repoId}/branches/{branchId}          # ブランチ Close
GET    /api/repos/{repoId}/branches/{branchId}/merged   # マージ済み確認

# === TabSet ===
GET    /api/repos/{repoId}/branches/{branchId}/tabs
POST   /api/repos/{repoId}/branches/{branchId}/tabs
DELETE /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}
PATCH  /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}

# === Terminal ===
WS     /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/attach

# === Files ===
GET    /api/repos/{repoId}/branches/{branchId}/files?path=.
GET    /api/repos/{repoId}/branches/{branchId}/files/search?query=...&path=.
GET    /api/repos/{repoId}/branches/{branchId}/files/grep?pattern=...&path=.

# === Git ===
GET    /api/repos/{repoId}/branches/{branchId}/git/status
GET    /api/repos/{repoId}/branches/{branchId}/git/log
GET    /api/repos/{repoId}/branches/{branchId}/git/diff
GET    /api/repos/{repoId}/branches/{branchId}/git/branches
POST   /api/repos/{repoId}/branches/{branchId}/git/stage
POST   /api/repos/{repoId}/branches/{branchId}/git/unstage
POST   /api/repos/{repoId}/branches/{branchId}/git/stage-hunk
POST   /api/repos/{repoId}/branches/{branchId}/git/unstage-hunk
POST   /api/repos/{repoId}/branches/{branchId}/git/discard
POST   /api/repos/{repoId}/branches/{branchId}/git/discard-hunk

# === Claude ===
POST   /api/repos/{repoId}/branches/{branchId}/claude/restart
POST   /api/repos/{repoId}/branches/{branchId}/claude/resume

# === Commands ===
GET    /api/repos/{repoId}/branches/{branchId}/commands

# === Notifications (Activity Inbox) ===
GET    /api/notifications                                       # 通知一覧（全ブランチ）
POST   /api/notifications                                       # 通知登録（Claude Code Hook）
DELETE /api/notifications                                       # 全通知クリア
DELETE /api/notifications/{notifId}                              # 個別通知クリア
POST   /api/notifications/{notifId}/action                      # インラインアクション実行
       # body: { "action": "yes" | "no" | "resume" }
       # → 対応ブランチの Claude タブに WebSocket 経由でキー入力を送信

# === Palette (⌘K) ===
GET    /api/palette/search?q={query}                            # 全カテゴリ横断検索
       # Response: { workspaces: [...], files: [...], commands: [...] }
GET    /api/palette/recent                                      # 最近のブランチ・ファイル・コマンド

# === Settings ===
GET    /api/settings
PATCH  /api/settings

# === Misc ===
GET    /api/repos/{repoId}/branches/{branchId}/portman-urls
GET    /api/repos/{repoId}/branches/{branchId}/github-url
POST   /api/upload
GET    /api/connections

# === Event Stream ===
WS     /api/events

# === Orphan Sessions ===
GET    /api/orphan-sessions
GET    /api/orphan-sessions/{name}/windows
WS     /api/orphan-sessions/{name}/windows/{idx}/attach
```

### 3.6 WebSocket

**ターミナル接続**（1:1 pty I/O）:
```
WS /api/repos/{repoId}/branches/{branchId}/tabs/{tabId}/attach

Client→Server: { "type": "input"|"resize", ... }
Server→Client: { "type": "output", "data": "..." }
```

**イベントストリーム**（broadcast）:
```
WS /api/events

Server→Client:
  repo.opened / repo.closed / repo.starred / repo.unstarred
  branch.opened / branch.closed
  tab.added / tab.removed / tab.renamed
  notification
  settings.updated
```

再接続時: クライアントが `GET /api/repos` + `GET /api/notifications` でフル状態リロード。

## 4. フロントエンドアーキテクチャ

### 4.1 技術スタック

React 19 + TypeScript, Vite, React Router v7, xterm.js 5.x, Zustand, CSS Modules

### 4.2 状態管理

```typescript
interface PalmuxStore {
  // ドメイン状態
  repos: Repository[];
  orphanSessions: OrphanSession[];
  notifications: Notification[];  // Activity Inbox 用（全ブランチ横断）

  // グローバル設定（サーバー保存）
  globalSettings: {
    branchSortOrder: 'name' | 'activity';
    lastActiveBranch: string | null;
  };

  // デバイス設定（localStorage）
  deviceSettings: {
    theme: 'light' | 'dark';
    fontSize: number;
    toolbarVisible: boolean;
    drawerPinned: boolean;
    drawerWidth: number;
    splitEnabled: boolean;
    splitRatio: number;
    scrollbackLines: number;
    keyboardMode: 'none' | 'direct' | 'ime';
  };

  // UI 一時状態
  activeRepoId: string | null;
  activeBranchId: string | null;
  activeTabId: { left: string; right: string | null };
  rightPanelBranchKey: { repoId: string; branchId: string } | null;
  focusedPanel: 'left' | 'right';
  drawerOpen: boolean;
  inboxOpen: boolean;           // Activity Inbox 表示状態
  paletteOpen: boolean;         // ⌘K パレット表示状態
  connectionStatus: 'connected' | 'connecting' | 'disconnected';
  modifiers: { ctrl: ModifierState; alt: ModifierState };
  toolbarMode: 'normal' | 'claude';  // 2モード（Claude タブフォーカスで自動切替）
}

interface Notification {
  id: string;
  repoId: string;
  branchId: string;
  branchName: string;       // 表示用: "palmux / feature/new-ui"
  type: 'urgent' | 'warning' | 'info';
  message: string;          // "Claude is waiting for confirmation"
  detail?: string;          // "Apply changes to 3 files?"
  actions?: NotificationAction[];  // インラインアクション
  createdAt: string;
  read: boolean;
}

interface NotificationAction {
  label: string;    // "Yes (y)", "No (n)", "Resume"
  action: string;   // "yes", "no", "resume"
}
```

### 4.3 TabRenderer レジストリ（フロントエンド）

```typescript
// frontend/src/lib/tab-registry.ts

interface TabRenderer {
  type: string;                                  // "files", "git", ...
  icon: React.ComponentType;                     // TabBar のアイコン
  component: React.ComponentType<TabViewProps>;   // メインコンテンツ
}

interface TabViewProps {
  repoId: string;
  branchId: string;
}

const registry = new Map<string, TabRenderer>();
export function registerTab(renderer: TabRenderer) { registry.set(renderer.type, renderer); }
export function getRenderer(type: string) { return registry.get(type); }
```

```typescript
// frontend/src/tabs/tab-content.tsx
function TabContent({ tab, repoId, branchId }: Props) {
  // terminal 系（claude, bash）→ 共通 TerminalView
  if (tab.windowName) {
    return <TerminalView repoId={repoId} branchId={branchId} tabId={tab.id} />;
  }
  // REST 系 → レジストリから Renderer を取得
  const renderer = getRenderer(tab.type);
  if (!renderer) return <div>Unknown tab type: {tab.type}</div>;
  return <renderer.component repoId={repoId} branchId={branchId} />;
}
```

各タブモジュールはエントリポイント（`index.ts`）で `registerTab()` を呼ぶ。`main.tsx` で import するだけで登録完了。

### 4.4 TerminalManager（3段階キャッシュ）

```
Active  — 画面表示中。Terminal + WebSocket 両方生存
Cached  — 非表示。Terminal + WebSocket 維持。即座に切り替え可能（上限 6）
Evicted — dispose + close 済み。再表示時に再接続

LRU で Cached が 6 超過 → 最古を Evict
Evict 後の再表示 → tmux 側 scrollback から復元
```

scrollback: デフォルト 5000行、設定可能（1000〜50000、localStorage 保存）

### 4.5 Split Mode

```
┌─────────────────────┬─────────────────────────┐
│ Header (左パネル連動)│                          │
├─────────────────────┤ [repo ▾] [branch ▾]     │ ← 右パネルのミニセレクタ
│ TabBar (左パネル)    │ [Claude][Files][Bash][+] │ ← 右パネルの TabBar
├─────────────────────┼─────────────────────────┤
│  左パネル            │  右パネル                │
│  TabContent          │  TabContent              │
├─────────────────────┴─────────────────────────┤
│ Toolbar                                        │
└────────────────────────────────────────────────┘
```

- 左パネル: Drawer + Header + TabBar 連動
- 右パネル: ミニセレクタバー（repo ▾ + branch ▾）+ 独立 TabBar
- Divider ドラッグ: 20%〜80%、localStorage 保存
- 幅 < 900px で自動シングルパネル

## 5. 設定管理

### 5.1 グローバル設定（全デバイス共有）

保存先: `~/.config/palmux/settings.json`
API: `GET/PATCH /api/settings`

```json
{
  "branchSortOrder": "name",
  "lastActiveBranch": "tjst-t--palmux--a1b2/main--e5f6",
  "imageUploadDir": "/tmp/palmux-uploads/",
  "toolbar": {
    "normalMode": {
      "rows": [[ /* modifier, key, arrow, popup, fontsize, ime, speech ボタン定義 */ ]]
    },
    "shortcutMode": {
      "rows": [[ /* ctrl-key ボタン定義 */ ]]
    },
    "claudeMode": {
      "rows": [
        [
          { "type": "key", "send": "y\r", "label": "y" },
          { "type": "key", "send": "n\r", "label": "n" },
          { "type": "arrow", "direction": "up" },
          { "type": "key", "send": "\r", "label": "⏎" },
          { "type": "ctrl-key", "key": "c", "label": "^C" },
          { "type": "key", "send": "\u001b", "label": "Esc" }
        ],
        [
          { "type": "command", "send": "/compact\r", "label": "/compact" },
          { "type": "command", "send": "/clear\r", "label": "/clear" },
          { "type": "command", "send": "/help\r", "label": "/help" },
          { "type": "command", "send": "/cost\r", "label": "/cost" },
          { "type": "command", "send": "/status\r", "label": "/status" }
        ]
      ]
    },
    "commandMode": "auto"
  }
}
```

Toolbar のボタン type 一覧:

| type | 説明 | 必須プロパティ |
|---|---|---|
| `modifier` | Ctrl/Alt（ワンショット/ロック） | `key` |
| `key` | 任意の文字列を送信 | `send`, `label` |
| `ctrl-key` | Ctrl+{key} を送信 | `key`, `label` |
| `arrow` | 矢印キー（長押しリピート） | `direction` |
| `page` | PgUp/PgDn | `direction` |
| `popup` | タップ + 上スワイプで別キー | `send`, `swipeSend`, `label` |
| `fontsize` | フォントサイズ増減 | `action` |
| `ime` | キーボードモード切替 | — |
| `speech` | 音声入力 | — |
| `command` | 文字列をそのまま送信 | `send`, `label` |

- 各モードは `rows`（配列の配列）で複数行定義可能
- `commandMode: "auto"` は Makefile/package.json からの自動検出。将来的に固定コマンドとの併用も可能
- `toolbar` キーが未設定 or 部分的に省略された場合はデフォルト値で補完

### 5.2 デバイス固有設定（ブラウザごと）

保存先: localStorage（キープレフィクス `palmux:`）

```
palmux:theme, palmux:fontSize, palmux:toolbarVisible,
palmux:drawerPinned, palmux:drawerWidth, palmux:splitEnabled,
palmux:splitRatio, palmux:scrollbackLines, palmux:keyboardMode,
palmux:lastTab:{repoId}:{branchId}
```

## 6. ルーティング・履歴管理

### 6.1 URL スキーム

```
/{repoId}/{branchId}/{tabId}
/{repoId}/{branchId}/files/{path}
/{repoId}/{branchId}/git/{view}

例:
/tjst-t--palmux--a1b2/main--e5f6/claude
/tjst-t--palmux--a1b2/main--e5f6/files/src/components
/tjst-t--palmux--a1b2/main--e5f6/git/status
/tjst-t--palmux--a1b2/main--e5f6/bash:bash
```

Split 右パネルはクエリパラメータ:
```
/...?right=tjst-t--palmux--a1b2/feature--new-ui--7a8b/bash:bash
```

`--base-path /palmux/` 指定時: `<base href="/palmux/">` を HTML に埋め込み、全 URL にプレフィクス。

### 6.2 pushState する操作 / しない操作

| 操作 | pushState | URL 変化 |
|---|---|---|
| ブランチ切り替え | する | `/{repo}/{branch}/{tab}` |
| タブ切り替え | する | `/{repo}/{branch}/{tab}` |
| Files: ディレクトリ移動 | する | `/{repo}/{branch}/files/{path}` |
| Files: ファイルを開く | する | `/{repo}/{branch}/files/{path}` |
| Git: ビュー切り替え | する | `/{repo}/{branch}/git/{view}` |
| Split 右パネル変更 | する | `?right=...` 更新 |
| Drawer 開閉 | しない | UI 一時状態 |
| モーダル表示 | しない | UI 一時状態 |
| Toolbar モード | しない | UI 一時状態 |
| フォントサイズ変更 | しない | UI 一時状態 |

ブラウザの戻る/進むボタンでブランチ・タブ・Files パス・Git ビューを遷移できる。

### 6.3 実装方針

React Router v7（`BrowserRouter` + `Routes` + `Route` + `useNavigate` + `useParams`）を使用。loader/action は使わない。

```typescript
// frontend/src/App.tsx
<BrowserRouter basename={basePath}>
  <Routes>
    <Route path="/" element={<Redirect />} />
    <Route path="/:repoId/:branchId/:tabId/*" element={<MainLayout />} />
  </Routes>
</BrowserRouter>

// MainLayout 内で useParams
const { repoId, branchId, tabId } = useParams();
const [searchParams] = useSearchParams();
const rightPanel = searchParams.get('right'); // Split 右パネル

// ナビゲーション
const navigate = useNavigate();
navigate(`/${repoId}/${branchId}/files/src/main.go`);
```

Files のサブパスと Git のビューは `/*` ワイルドカードで受け取り、コンポーネント内でパースする。

### 6.4 Zustand ストアとの統合

```typescript
// URL → ストア（ルート変更時）
useEffect(() => {
  store.applyRoute({ repoId, branchId, tabId, tabPath, rightPanel });
}, [repoId, branchId, tabId]);

// ストア → URL（Drawer/TabBar クリック時）
selectBranch(repoId, branchId) {
  set({ activeRepoId: repoId, activeBranchId: branchId });
  // navigate は呼び出し元のコンポーネントが行う
}
```

ストアが直接 navigate を呼ばない。URL はコンポーネント層で管理し、ストアはドメイン状態のみ保持する。

### 6.5 初回アクセス / 直接アクセス

```
GET / → <Redirect> コンポーネントが lastActiveBranch にナビゲート（なければ最初の Open ブランチ）
GET /{repoId}/{branchId}/{tabId} → MainLayout が URL をパースして該当画面を表示
  → ブランチが存在しない場合は / にフォールバック
```

Go サーバー側: `/{repoId}/...` に一致しない GET リクエストはすべて `index.html` を返す（SPA フォールバック）。API（`/api/`）と認証（`/auth`）は除く。

## 7. セキュリティ設計

- **認証**: Cookie（HttpOnly, SameSite=Strict）+ Bearer フォールバック
- **ファイルアクセス**: worktree 相対パスのみ、`../` 拒否、symlink 検証
- **接続制限**: `--max-connections` でブランチあたりの WS 上限、429 応答
- **画像アップロード**: `imageUploadDir` で指定したディレクトリのみ書き込み可。ディレクトリが存在しなければ自動作成

## 7.1 クリップボード

### コピー（tmux → ブラウザクリップボード）

```
tmux 内でテキスト選択（コピーモード）
  ↓
tmux が OSC 52 エスケープシーケンスを出力
  ↓
xterm.js が OSC 52 を検出・ハンドリング
  ↓
navigator.clipboard.writeText()
```

前提条件: HTTPS または localhost。tmux 側で `set -g set-clipboard on` が必要。

### テキストペースト（ブラウザクリップボード → tmux）

```
Ctrl+V / Cmd+V
  ↓
xterm.js の attachCustomKeyEventHandler でインターセプト
  （Ctrl+V のバイト値 \x16 を tmux に送らない）
  ↓
navigator.clipboard.readText()
  ↓
WebSocket: { "type": "input", "data": "クリップボードテキスト" }
```

### 画像ペースト（ブラウザクリップボード → ファイル保存 → 絶対パスをペースト）

```
Ctrl+V / Cmd+V
  ↓
paste イベントで clipboardData.items を検出
  ↓
画像 Blob（image/png, image/jpeg 等）を検出
  ↓
POST /api/upload (multipart/form-data)
  ↓
サーバー: imageUploadDir に保存
  ファイル名: paste-{YYYYMMDD}-{HHmmss}-{random4}.{ext}
  ↓
レスポンス: { "path": "/tmp/palmux-uploads/paste-20260403-120000-a1b2.png" }
  ↓
WebSocket: { "type": "input", "data": "/tmp/palmux-uploads/paste-20260403-120000-a1b2.png" }
```

画像アップロード設定（`~/.config/palmux/settings.json`）:

```json
{
  "imageUploadDir": "/tmp/palmux-uploads/"
}
```

デフォルト: `/tmp/palmux-uploads/`

## 8. テーマ・デザイン

Fog パレット v2.1

- Accent: `#7c8aff`（ペリウィンクルブルー）
- Dark: bg `#0f1117`, surface `#13151c`, elevated `#1a1c25`, fg `#d4d4d8`, muted `#8b8fa0`
- Light: bg `#fafafa`, surface `#f4f4f5`, elevated `#ffffff`, fg `#18181b`, muted `#52525b`
- Terminal: bg `#0c0e14`, green `#64d2a0`, yellow `#e8b45a`, blue `#7c8aff`
- Semantic: success `#64d2a0`, warning `#f59e0b`, error `#ef4444`
- フォント: Geist Mono（ターミナル）、Geist + Noto Sans JP（UI）

## 9. ADR（設計判断記録）

| ADR | 決定 | 根拠 |
|---|---|---|
| 001 | Slug+Hash ID | URL ルーティング安全 + 人間可読。hash4 で衝突回避 |
| 002 | Event WS 分離 | pty バイナリ I/O と JSON イベントの責務分離 |
| 003 | Zustand | ボイラープレート少、WS ハンドラから直接更新可能 |
| 004 | CSS Modules | Fog パレット + テーマ切り替えに CSS 変数が最適 |
| 005 | worktree = Open | マーカーファイル不要。ソースオブトゥルースが単一で明快 |
| 006 | window name ベース | index 欠番問題を回避。可読性・デバッグ性向上 |
| 007 | ハイブリッドポーリング | tmux 5s（リアルタイム性重要）/ worktree 30s（レアケース許容） |
| 008 | Cookie 認証 | WS にクエリパラメータ不要。URL にトークン露出しない |
| 009 | gwq で worktree 管理 | Palmux は worktree パスを決めない。gwq の config に委任 |
| 010 | IsPrimary（main 非固定） | リポジトリ本体のブランチは main に限らない。`git worktree list --porcelain` で `.git/` ディレクトリ保持の worktree を Primary と判定 |
| 011 | React Router v7 | pushState/popstate の自前管理はエッジケースでバグを踏みやすい。basename で base-path 対応も一発。loader/action は使わず最小限の機能のみ利用 |

### 外部依存（起動時チェック）

Palmux 起動時に以下のコマンドの存在を確認し、見つからなければエラーメッセージを表示して終了する:

| コマンド | 用途 |
|---|---|
| `tmux` | ターミナルセッション管理 |
| `ghq` | リポジトリ一覧取得 |
| `gwq` | worktree 作成/削除 |
| `git` | worktree list, status, diff 等 |
