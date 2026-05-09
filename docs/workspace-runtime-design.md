# Workspace Runtime 設計提案

> 作成日: 2026-05-08
> ステータス: **Draft / レビュー待ち**（Sprint 化前の素案）
> 対象: Workspace の実行環境を host / LXD container / VM / remote に切り替え可能にする大型機能。 ports 管理 と portman subsume も同 scope。

## 0. TL;DR

- **Workspace の実行環境を抽象化** する。 現状は常に host で tmux を立てているが、 これを `host` / `lxd-container` / `lxd-vm` / `lxd-remote` / `ssh-remote` から選べるようにする
- **目的は host を汚さないこと**。 Claude が起こす dev server、 build artifact、 install パッケージで host を汚染するのを防ぐ
- **Claude 自身は runtime の中で動く**。 worktree と `~/.claude/` を bind-mount することで skill / memory / auth は透過的に共有される。 Claude には「コンテナで実行して」と毎回指示する必要がない（idmap で UID 一致 + 同じ絶対 path bind が前提、§ 4.4）
- **container/VM/remote 内では `palmux-agent` が常駐**。 ランタイム push 方式で palmux version と一致させる（image に焼かない）。 すべての runtime が agent RPC で統一実装される（§ 6.4）
- **Ports 管理は内製 allocator + LXD proxy device**。 portman 機能を palmux に内製化（依存ゼロ）、 `palmux port` subcommand で portman 互換 CLI も提供（Makefile/scripts 用、 § 5.2.3）。 portman repo はそのまま残置（§ 14.10.7）。 Claude には runtime に応じた MCP tool を提供: container/VM では `expose_port`、 host では `allocate_port`。 runtime hint も auto-inject（§ 5.2.2）
- **重い eval は別 runtime（`lxd-remote` / `ssh-remote`）で対応**。 v1 では `Runner` 概念は導入しない（§ 14.9）
- **container image は palmux チームでメンテ**（`ghcr.io/tjst-t/palmux-workspace:default`、§ 14.1）。 月 1〜2 時間想定、 CI 自動化前提、 GHCR 配布、 build は palmux 本体 repo
- **既存 Workspace は host runtime のまま**（後方互換、 migration UI なし）。 新規 Workspace のデフォルトは LXD があれば container、 なければ host
- **対象 OS は Ubuntu のみ**。 macOS は非サポート（§ 14.7）

## 1. 背景と動機

### 1.1 課題

現状、 palmux で Claude が起こすプロセスはすべて host 上で動く。 これにより:

- **port 衝突** — 複数 Workspace で同じ dev server (e.g. `:3000`) を立てると衝突
- **process leak** — Workspace を閉じても zombie process が host に残る
- **package 汚染** — `npm i -g`, `pip install`, `apt install` 等が host に蓄積
- **filesystem 汚染** — ビルド成果物・キャッシュ・ログが意図せず host に残る
- **試したいバージョンの分離不可** — Workspace ごとに異なる Node/Python/Go バージョンを持てない

### 1.2 ユースケース

| ケース | 必要な隔離 | 想定 runtime |
|---|---|---|
| 通常の開発 | host を汚さない | local LXD container |
| カーネル module / 完全に信用できないバイナリ | kernel 境界 | local LXD VM |
| GPU / big RAM が必要な開発 | 大型 host に dev ごと持っていく | remote LXD (container or VM) |
| 重い eval を走らせたい | 大型マシンで実行 | remote LXD (container or VM) |
| Ansible で外部サーバ管理 | network reach + auth 共有 | local LXD container + tailnet |
| LXD なし環境 (Ubuntu) | （隔離なし） | host |
| palmux 自身の dev | container 内で開発（§ 14.8） | local LXD container |

### 1.3 設計目標

1. **runtime 切替が透過** — Claude に「コンテナで実行して」と指示する必要がない
2. **skill / memory / auth が runtime をまたいで共有される** — `~/.claude/skills`, `~/.gitconfig`, SSH agent 等がそのまま使える
3. **runtime 選択は per-Workspace** — Workspace 単位で異なる runtime を使える
4. **既存ユーザを壊さない** — host runtime も first-class でサポート、 既存 Workspace は migration 不要
5. **portman の機能を subsume する** — palmux 単体で port allocation が完結
6. **macOS と LXD なし Linux でも動く** — host runtime にフォールバック

### 1.4 非目標 (out of scope, 少なくとも v1)

- **public internet への port 公開** — cloudflared / ngrok / tailscale funnel の統合は将来検討
- **cloud VM の autoprovisioning** — EC2 / GCE 等を palmux が立てる機能。 当面は user が事前に用意した remote を `lxc remote add` 経由で繋ぐ
- **GPU passthrough の自動化** — 設定は user 側に任せる
- **`Runner` 概念（外部 eval 送り）** — § 14.9 で除外決定。 重い eval は remote runtime の Workspace で代替
- **macOS サポート** — § 14.7 で除外決定。 Ubuntu のみ対象

## 2. ドメインモデルへの追加

### 2.1 `WorkspaceRuntime` 概念

各 Workspace は **1つの runtime** に紐づく。 runtime は Workspace のライフサイクル（open/close）と一致する形で起動・停止される。

```
Workspace (worktree path = identity)
├── Runtime  ← 新概念
│   └── kind: host | lxd-container | lxd-vm | lxd-remote | ssh-remote
└── TabSet
    └── Tabs (Claude / Bash / Files / Git / Sprint)
        └── 各 terminal タブの tmux session は runtime の中で動く
```

### 2.2 Runtime 種別と特性

| 種別 | 隔離強度 | 起動コスト | 主用途 |
|---|---|---|---|
| `host` | なし | ゼロ | 既存互換、 LXD なし環境のフォールバック |
| `lxd-container` | namespace + cgroup | 〜1秒 | 通常開発のデフォルト、 palmux 自身の dev |
| `lxd-vm` | カーネル | 〜30秒 | カーネル触る、 完全隔離、 ML 大型 eval |
| `lxd-remote` (container/vm) | 上記 + 別 host | 〜数秒 + ssh | GPU / big RAM、 外部 dev box、 重い eval |
| `ssh-remote` | host への root 信用次第 | ssh のみ | LXD 入れられない remote |

### 2.3 Workspace のメタデータ拡張

```go
// 既存の Branch (= Workspace) に runtime config を追加
type Branch struct {
    // ... 既存フィールド
    Runtime RuntimeConfig
}

type RuntimeConfig struct {
    Kind    RuntimeKind // host | lxd-container | lxd-vm | lxd-remote | ssh-remote
    Remote  string      // lxd-remote / ssh-remote のとき (lxc remote name or ssh host)
    Image   string      // lxd-* のとき (e.g. "ubuntu:24.04")
    Network NetworkPolicy
    Mounts  []Mount     // 追加の bind mount (skill/auth は default で含まれる)
    Env     map[string]string
}

type NetworkPolicy struct {
    Mode    NetworkMode // bridged (default) | host-netns | tailnet
    Tailnet *TailnetConfig // mode=tailnet のとき
}
```

### 2.4 default runtime の選び方

新規 Workspace 作成時の優先順位（§ 9.6 詳述）:

```
1. Workspace に明示指定があれば            → それを使う
2. 同 repo の per-repo default があれば    → それを使う (§ 9.1 で初回設定)
3. settings.json に defaultRuntime があれば → それを使う
4. LXD installed                            → lxd-container (image: ghcr.io/tjst-t/palmux-workspace:default)
5. otherwise                                 → host (warning: 隔離なし)
```

Workspace 作成 modal および Repository 開く modal の "Runtime" セレクタには全選択肢を出す（§ 14.6 で決まったとおり、 LXD なし環境では LXD 系の選択肢は disabled + tooltip 表示）。 既存 Workspace は migration なし、 すべて `host` のまま。

## 3. WorkspaceRuntime インタフェース

### 3.1 Go interface

```go
package runtime

type Runtime interface {
    // ライフサイクル
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Status() Status

    // 実行
    // tmux session を runtime 内に立てる。 host なら tmux new-session 直接、
    // lxd-* なら lxc exec -- tmux new-session、 ssh-remote なら ssh remote tmux ...
    NewTmuxSession(ctx context.Context, sessionName string) error
    AttachTmuxSession(ctx context.Context, sessionName string) (io.ReadWriteCloser, error)
    Exec(ctx context.Context, cmd []string, opts ExecOpts) (ExecResult, error)

    // ポート
    ListListeningPorts(ctx context.Context) ([]Port, error)
    ExposePort(ctx context.Context, in InternalPort, out HostPort) (PortMapping, error)
    UnexposePort(ctx context.Context, mappingID string) error

    // ファイルアクセス (Files タブ用)
    ReadFile(ctx context.Context, path string) ([]byte, error)
    WriteFile(ctx context.Context, path string, data []byte) error
    Stat(ctx context.Context, path string) (FileInfo, error)
    Walk(ctx context.Context, path string, fn WalkFunc) error
}

type Status struct {
    State     RuntimeState // stopped | starting | ready | stopping | failed
    StartedAt time.Time
    Address   string // 例: container IP or "localhost" for host
    Error     string
}
```

### 3.2 実装ごとの戦略

非 host runtime はすべて **container agent (`palmux-agent`)** に対する RPC で実装される（§ 6.4）。 これにより host 用 native 実装と非 host 用 agent 実装の 2 つだけになり、 lxd-container / lxd-vm / lxd-remote / ssh-remote の差分は agent への到達経路だけになる。

#### 3.2.1 `host`
- `Start` / `Stop` は no-op（ただし port/process tracking のため Workspace lifecycle にフック）
- `NewTmuxSession`: 直接 `tmux new-session`
- `Exec`: `os/exec.Command`
- `ReadFile` / `Walk`: 通常の `os` パッケージ
- `ExposePort`: 不要（process が直接 host で listen している）。 ただし palmux 側で「Workspace が掴んでる port」は記録 (cleanup 用)
- agent を起動しない（in-process で完結）

#### 3.2.2 `lxd-container`
- `Start`: `lxc launch <image> <inst>` → bind-mount デバイス追加 → `lxc file push palmux-agent` → systemd unit で agent 起動
- `Stop`: `lxc stop <inst>` (option: `--force`)
- `NewTmuxSession`: agent RPC で `tmux new-session` を container 内で実行（`lxc exec` 経由ではなく agent 経由）
- `Exec` / `ReadFile` / `Walk` / `ListListeningPorts`: agent への RPC
- `ExposePort`: host 側で `lxc config device add <inst> <name> proxy listen=tcp:127.0.0.1:<host> connect=tcp:127.0.0.1:<container>`（これは host 側 LXD 操作なので agent 経由でない）
- agent への接続: UDS を bind-mount で host から見える場所に置く

#### 3.2.3 `lxd-vm`
- `lxc-container` と同じ API (`--vm` オプション付き launch)
- bind-mount は **virtiofs** 経由 (`lxc config device add ... disk source=... path=...`) — LXD が自動で virtiofs を選ぶ
- agent は同じ binary を push → systemd で起動
- 起動が遅い（10〜30秒）→ Workspace open 時の UX に **「VM 起動中」プログレス** が必要
- メモリ制限: 設定で per-Workspace に指定可能 (`lxc config set ... limits.memory=4GiB`)
- agent への接続: VM のため UDS bind-mount は使えず、 vsock もしくは LXD の TCP proxy 経由

#### 3.2.4 `lxd-remote` (container or vm)
- `lxc remote add` で remote 登録は user の事前作業
- 操作はすべて `lxc -r <remote> ...` でリダイレクトされる
- agent への接続: SSH tunnel 経由で agent UDS に到達（`ssh -L /local/sock:/remote/sock`）

#### 3.2.5 `ssh-remote`
- LXD なし remote 用のフォールバック
- `Start`: `ssh remote 'palmux-agent --socket /tmp/palmux-agent.sock &'` で remote 上に agent を起動（事前に `scp palmux-agent` で送る or 既設）
- `Stop`: agent kill
- 全 RPC は SSH 経由で agent に到達
- `ExposePort`: SSH local forward（`ssh -L <host>:<remote-host>:<remote-port>`）

## 4. bind-mount 戦略

### 4.1 host runtime
- bind-mount 不要（すべて host filesystem）

### 4.2 lxd-container / lxd-vm (local)
- **default で bind される**:
  - worktree path → `/workspace` (rw)
  - `~/.claude/skills` → `/home/<user>/.claude/skills` (ro)
  - `~/.claude/projects/<this-project>` → 同じ path (rw, memory 用)
  - `~/.gitconfig` → 同じ path (ro)
  - `~/.config/gh` → 同じ path (ro)
  - `~/.ansible/`, `~/.ansible.cfg` → 同じ path (rw / ro)
  - `$SSH_AUTH_SOCK` → 同 path (forward)
- **default で bind しない**（必要なら settings から追加）:
  - `~/.ssh/` 全体 — SSH agent forward で代替
  - `~/.config/gcloud`, `~/.aws`, etc. — 必要な人だけ opt-in

### 4.3 lxd-remote / ssh-remote
- worktree は **remote 上で git clone** される（初回 open 時に palmux が remote に push or clone）
- skill / config の同期: 初回 open 時に `~/.claude/skills` 等を remote に rsync
- 同期は **初回のみ** が default。 setting で auto-sync (file watch) を有効化可能
- SSH agent forward: ssh の `-A` 相当を使う

### 4.4 Claude CLI が bind-mount された `~/.claude/` で正常動作するか

> レビュー Q: 「これはおそらく claude cli の期待する動作ではないけど、 正常に動作する？」

**結論: 動作する。 ただし以下を満たす必要がある:**

1. **UID/GID の一致** — container 内のユーザの UID が host のユーザと一致する必要がある（Claude CLI が session JSONL を書き込むため）
   - LXD は `idmap` (LXD 5.0+ の idmapped mounts) で透過的に処理可能。 `lxc config set <inst> raw.idmap "both 1000 1000"` で host UID 1000 と container UID 1000 を等価にする
   - これで bind-mount 越しに同じ perms で読み書きできる
2. **同じ絶対パス** — Claude CLI は `$HOME/.claude/` を見るので、 container 内の `$HOME` が host と一致するように bind する（例: host `/home/ubuntu/.claude` → container `/home/ubuntu/.claude`、 container user も `ubuntu`）
3. **同時アクセスを避ける** — host の Claude CLI と container の Claude CLI が同じ project の `~/.claude/projects/<X>/sessions/*.jsonl` を同時に書くと壊れる
   - palmux 側で「この Workspace では container の Claude のみ動かす」契約にする（host palmux と container palmux の二重起動禁止）
   - `~/.claude/skills` は read-only なので競合なし
   - 別プロジェクトの memory は触らせない（`~/.claude/projects/<this>` だけ bind）

**`settings.json` の扱いに注意:**

`~/.claude/settings.json` は MCP server path や hook command path 等を絶対パスで持つ。 host 用パスが container 内で解決できない可能性がある。 対策:

- `~/.claude/settings.json` は **bind しない**（container 内で別途生成）
- palmux が container 用の settings を runtime 起動時に注入（必要な MCP server 設定だけ）
- skill / memory のような **データは共有**、 **設定は分離** という線引き

**実機検証が必要な事項** (Phase A の AC に含める):

- [ ] `claude --resume` が container 内で host で開始したセッションを resume できる
- [ ] skill が container から読める（`~/.claude/skills/foo/SKILL.md` が見える）
- [ ] memory の追記が host から見える（双方向同期）
- [ ] 並行起動時の lockfile 等の挙動

### 4.5 一般的なセキュリティ考察

- **脅威モデル**: container 内の software が compromise されたとき、 bind-mount された credentials が読まれる
- **緩和策**:
  - `~/.ssh/` は default で bind しない (agent forward を使う)
  - `~/.claude/projects/<this>` だけ rw、 他の project の memory には触らせない
  - public read 可な部分は ro マウント
- **想定脅威外**: Claude 自身が悪意を持つケース（現状の host 直実行と同等のリスク、 これ以上悪化しない）

## 5. ポート管理

### 5.1 検出 (auto)

container/VM 内で listening しているポートを周期 poll で発見:

- `lxd-*`: `lxc exec <inst> -- ss -tln -H` を 5秒間隔で実行
- `ssh-remote`: `ssh remote ss -tln -H`
- `host`: `lsof -i -P -n | grep LISTEN` で Workspace 関連 process を絞り込む（PID tracking が必要）

検出された port は Workspace の Ports panel に **「未公開」** 状態で表示される。

### 5.2 公開 (manual expose)

ユーザが UI で "Expose" をクリック → palmux が:

1. portman 互換 allocator で free な host port を確保（`palmux port alloc --name ws-<id>-<port-name>`）
2. 確保した port で `lxc config device add ... proxy ...` 実行
3. mapping を Workspace state に記録（永続化）
4. URL を Activity Inbox にイベント発行（クリック可能）

**default は `127.0.0.1` バインド**。 LAN 公開は明示チェックボックスで `0.0.0.0` 化（確認 dialog 経由）。

### 5.2.1 Claude 用 MCP tool（§ 14.4）

palmux の in-process MCP server が **runtime に応じて異なる tool セット** を Claude タブに公開する:

**container / VM / remote runtime のとき:**

```
expose_port(port: int, name?: string, public?: bool) -> { url, host_port }
unexpose_port(name: string)
list_exposed_ports() -> [{ name, internal, host, url, public }]
```

container 内では netns 隔離があるため、 service は **任意の port に bind してよい**（衝突しない）。 service 起動後に `expose_port` で host port にマップする。

**host runtime のとき:**

```
allocate_port(name: string) -> { port }
release_port(name: string)
list_allocated_ports() -> [{ name, port }]
```

host runtime では netns 隔離がないため **bind 前に allocator から port を取る** 必要がある。 取得した port に service を bind すると、 palmux 側で「Workspace X が掴んでる port」として記録され、 Workspace close 時に cleanup される。

**runtime に応じて tool を切り替える理由:**
- Claude が host vs container を意識せずに済む
- 不適切な tool（host で `expose_port` 等）を呼んで失敗するシナリオを排除
- system message でも runtime hint を auto-inject（後述）

これにより Claude が dev server を起動した直後に自発的に expose して URL をユーザに提示できる。

**permission flow（§ 14.10.3）:**

| 設定 | 127.0.0.1 expose | 0.0.0.0 (LAN) expose |
|---|---|---|
| `claude_can_expose: "auto-allow-localhost"` (default) | 確認なしで即許可、 Activity Inbox に通知のみ | 毎回確認 dialog（always-allow 不可）|
| `claude_can_expose: "ask"` | 既存の permission framework で初回確認 → always-allow 可 | 毎回確認（always-allow 不可）|
| `claude_can_expose: false` | MCP tool 自体を hide | 同左 |

default の `auto-allow-localhost` で「Claude がほぼ自由に dev server を expose できる」体験を作る。 LAN 公開だけは security のため明示確認を残す。

`auto-detect → 全部自動 expose` モード（Claude を介さず ss -tln 検出だけで expose）は入れない（明示的判断 = MCP 経由 or UI クリック を必須にする）。

### 5.2.2 Claude への runtime hint auto-inject

palmux は Claude タブ起動時に **runtime に応じた system message** を自動 inject する。 ユーザはプロジェクト固有 CLAUDE.md に port allocation のことを書く必要がない（portman 時代に各 CLAUDE.md に書いていた指示を palmux 側に移管）。

**host runtime の hint:**

```
[palmux runtime context]
This Workspace runs on host (no network isolation). Before binding to any
port, call the `allocate_port(name)` MCP tool to get a free port. Failing
to do so may cause collisions with other Workspaces or host processes.
```

**container / VM / remote runtime の hint:**

```
[palmux runtime context]
This Workspace runs in an isolated network namespace. Bind to any port
freely (no collision risk). After your service starts, call
`expose_port(port, name)` to publish it to the host so the user's browser
can reach it.
```

inject タイミング: Claude CLI session start 時に palmux が `--system-prompt` 相当で渡す（具体的な実装は CLAUDE タブの既存機構に乗せる）。 ユーザは設定で hint を disable できる（自前 CLAUDE.md で書きたい場合）。

### 5.2.3 CLI fallback: `palmux port exec`

MCP に乗らない context（`Makefile` / `package.json` script / shell script / CI ジョブ）では portman 時代と同じく CLI を使う:

```
palmux port exec --name dev-server -- npm run dev --port {}
palmux port exec --name api      -- go run . --port {}
```

これは runtime に依らず動く:
- host runtime: free port を allocator から取って `{}` に注入、 そのまま bind
- container runtime: agent 経由で内部の bind を行い、 host 側 forward port も自動確保
- remote runtime: 同上、 agent への RPC が SSH 経由になるだけ

`palmux port exec` は MCP tool と同じ allocator を使うため整合性が保たれる（Claude が `allocate_port` で取った port と Makefile の `palmux port exec` の port が衝突しない）。

### 5.3 Ports panel UI

**配置: Header の既存 "P" ボタンスロットを再利用、 常時表示。**

現在 `frontend/src/components/header.tsx:80-91` にある portman dashboard へのジャンプボタン (`{portmanURL && (...)}` で条件付き表示) を、 **palmux 内製の Ports panel ボタンに置き換える**。 portman 起動の有無に関わらず常時表示。

- ボタン: Header 右側のアイコン群（テーマ切替・split panel と同列）
- アイコン文字は "P" を維持（"Ports" の意で portman 由来でない）
- クリックで popover / Drawer 風 panel を開き、 現在 active な Workspace の port 一覧を表示

panel 内のレイアウト:

```
Ports  (Workspace: feature-x — runtime: lxd-container)
──────────────────────────────────────────────────────
  ● 5173  Vite (in container)        → http://localhost:15173       [Open] [Stop]
  ▸ 8080  detected (in container)    — not exposed                  [Expose →]
  ● 3000  api (in container)         → http://localhost:13000       [Open] [Stop]
                                       LAN exposed (0.0.0.0)        ⚠
──────────────────────────────────────────────────────
  External: portman dashboard ↗        (--portman-url が設定されているときのみ表示)
```

- ●= live、 ▸= idle (検出されたが未公開)
- runtime によらず統一 UI（host のときは "direct"、 lxd-* のときは proxy device、 remote のときは ssh tunnel と内部表記）
- クリックで browser open（`http://localhost:<host-port>`）
- 右クリックでコピー（URL）
- panel フッタの "External: portman dashboard ↗" は **`--portman-url` 起動時のみ表示**（palmux が portman に依存しなくなった後でも、 portman 単体で使うユーザのために hook を残す）

**implementation note:**

- 既存の `portmanURL && (...)` 条件分岐は撤去。 ボタン自体は無条件で render
- panel content は `/api/repos/{repoId}/branches/{branchId}/ports` から取得（既存 `handler_portman.go` を `handler_ports.go` に rename + 拡張）
- WebSocket イベント `port.detected` / `port.exposed` / `port.unexposed` で realtime 更新

### 5.4 lifecycle

- **Workspace open** 時に永続化された mapping を再構築（proxy device を再追加）
- **Workspace close** 時にすべての mapping を削除（host port を解放、 proxy device を削除）
- **runtime 再起動** 時に mapping を維持

### 5.5 remote runtime のとき

remote 上の container にある port を local browser から開きたい場合 2 オプション:

| オプション | 体験 | 実装 |
|---|---|---|
| **(a) palmux が自動 SSH local forward** | local の `:18080` で透過に開ける | ssh master conn を1本維持し forward を動的追加 |
| **(b) remote host の URL を直接提示** | `http://gpu-box:18080` を表示、 user が自分でアクセス | 何もしない |

(a) を default、 setting で (b) に切替可能。

## 6. ポート管理 — palmux 内製 allocator + container agent

### 6.1 動機と方針

- palmux が Workspace-scoped で port を管理するため、 portman 相当の機能を **内製化**（外部依存ゼロ）
- portman repo 自体は archive せず残す（§ 14.10.7）。 palmux は portman に依存しないが、 portman 単体としての価値は別途存続
- すべての runtime（host / container / VM / remote）が同じ allocator API を使う

### 6.2 内製 allocator

1. `internal/port/` に Go 実装
   - `Allocate(scope, name string) (port int, error)` — `scope` は global or workspace ID。 既存名なら同じ port を返す
   - `Free(scope, name string)` — 解放
   - `List(scope) []Mapping` — scope 内の全 mapping
   - 永続化: `~/.config/palmux/ports.json`（portman 互換フォーマット = 同じ場所に書くオプションあり）
2. `palmux port` subcommand を提供（portman の CLI と同じ I/F）
   ```
   palmux port exec --name foo -- npm run dev --port {}
   palmux port alloc --name foo
   palmux port list
   palmux port free foo
   ```
3. server 起動中は同じ allocator を内部で使い、 Workspace scope を被せる
4. `make dev` / `make serve` を `palmux port exec ...` に置き換え
5. **portman repo はそのまま残す**（archive せず、 deprecation notice も入れない）

### 6.3 runtime ごとの扱い

| runtime | port allocator の使われ方 |
|---|---|
| `host` | Workspace から起動した process の port を allocator に登録（cleanup 用に PID とも紐付け）。 expose は no-op（既に host で listen） |
| `lxd-container` / `lxd-vm` | host 側 forward port を allocator から確保 → `lxc config device add proxy listen=tcp:127.0.0.1:<host-port> connect=...` |
| `lxd-remote` / `ssh-remote` | host 側の SSH local forward port を allocator から確保 → ssh master conn 経由で remote container まで通す |

### 6.4 container agent（`palmux-agent`）

§ 14.10.6 の決定に基づく詳細設計。

#### 6.4.1 役割

- container/VM/remote 内で常駐し、 host palmux からの RPC を処理
- 提供する operation:
  - `ListListeningPorts() []Port` — `/proc/net/tcp` を直接読む
  - `ReadFile(path) bytes` / `WriteFile(path, bytes)` / `Stat(path)` / `Walk(path) []Entry`
  - `StartProcess(cmd, env, cwd)` / `KillProcess(pid)` / `ListProcesses()`
  - `Subscribe(event_type)` — 新規 listening port 検出 / 子プロセス終了 等を host に push

#### 6.4.2 配布: image に焼かず push 方式

**理由:**
- image を palmux version に縛らない（image は CLI 一式だけ持つ）
- palmux update 時に image rebuild 不要
- agent と palmux server の version 整合が常に保たれる

**手順（runtime Start 時）:**
1. `lxc launch palmux/workspace:default <inst>` で container 起動
2. `lxc file push <palmux-binary-dir>/palmux-agent <inst>:/usr/local/bin/palmux-agent`
3. `lxc exec <inst> -- systemd-run --unit palmux-agent /usr/local/bin/palmux-agent --socket /run/palmux-agent.sock`
4. host 側で `lxc exec <inst> -- nc -U /run/palmux-agent.sock` 経由で接続、 もしくは UDS を bind-mount 経由で host から直接 dial

#### 6.4.3 binary spec

- Go static binary、 cgo なし、 `make build-agent` で `bin/palmux-agent` を出す
- size 目標: 10 MB 以下（push 時間 < 1 秒）
- 単一 binary、 OS 依存なし（container 内の Linux で動けば十分）
- `palmux serve` と `palmux-agent` は別 binary（agent は subset 機能のみ）

#### 6.4.4 通信プロトコル

- **JSON-RPC over UDS**（local container）/ JSON-RPC over SSH-tunneled UDS（remote）
- gRPC は将来検討、 v1 は依存最小で JSON-RPC
- 接続は host palmux が agent に常時 1 本維持、 lifecycle = container lifecycle
- agent からの async event は同じ stream に push

#### 6.4.5 host runtime での扱い

- host runtime は agent を必要としない（all native）
- ただし interface は同じ Go interface (`runtime.Runtime`) を実装するため、 host 用の implementation はすべて in-process で完結
- これにより上位コードは runtime 種別を意識しない

### 6.5 bootstrap 問題

palmux server 起動前にも `make dev` で port が必要 → `palmux port` は **server 起動不要の純粋 CLI モード** で動く。 server が起動中ならその allocator と協調（同じ ports.json を読み書き、 ファイルロックで競合回避）。

## 7. ネットワークポリシー

Workspace ごとに network mode を選択:

| Mode | 挙動 | 用途 |
|---|---|---|
| `bridged` (default) | LXD bridge `lxdbr0` 経由で NAT outbound、 host port は見える、 host 経由で internet 到達 | 通常開発、 内部 LAN 到達可 |
| `host-netns` | container が host の network namespace を共有、 host VPN がそのまま見える | 企業 VPN 環境、 host の Tailscale を利用 |
| `tailnet` | container 自身が Tailscale で tailnet に join | tailnet 上のサーバ管理（Ansible 等）、 host とは独立 |
| `none` | network なし | offline 検証 |

`tailnet` mode の details:

- container image 起動後に `tailscale up --auth-key=<key>` を自動実行
- `auth-key` は palmux の credential store で管理（`~/.config/palmux/secrets.json` を OS keyring 経由で暗号化）
- container は tailnet 上に固有の hostname / IP を持つ（`ws-<workspace-id>`）

## 8. Runner (β: 重い eval を外部に送る) — **deferred**

> **§ 14.9 で v1 範囲外と決定。**
>
> 「重い eval を外部マシンで走らせる」要件は **`lxd-remote` / `ssh-remote` runtime** で代替できる: 専用の Workspace を作り、 そちらの runtime を remote 大型マシンに向けるだけ。 別概念として `Runner` を切るのは過剰設計と判断。
>
> 将来的に「snapshot 送信 + job キュー + 並列管理」のような CI 的需要が明確になったら別 Sprint で再提案する。 当面は本セクションは履歴として残すだけで、 実装計画には含めない。

## 9. UI 仕様

### 9.1 Repository 開く時の runtime 確認 modal

**新規。** 既存の Repository Open フロー（`frontend/src/components/repo-picker.tsx` の `RepoPicker` modal、 Drawer フッタの "📂 Open Repository…" ボタンから起動）に **runtime 選択ステップ** を追加する。

理由: Repository を開くと **primary worktree が自動で Workspace として開かれる** (palmux の 2-stage Open モデル)。 この implicit な Workspace 作成にも runtime 選択が必要。 さもなくば user の知らないうちに default runtime で起動されることになる。

modal の構成（既存の Browse / Clone モードに加える）:

```
┌───────────────────────────────────────────────┐
│ Open Repository                               │
│ ─────────────────────────────────────────────│
│ [ Browse | Clone ] (既存)                     │
│                                               │
│ Selected: github.com/tjst-t/palmux2           │
│                                               │
│ Runtime: ( ● lxd-container  default )         │
│          ( ○ lxd-vm                  )        │
│          ( ○ lxd-remote: gpu-box     )        │
│          ( ○ host  ⚠ no isolation     )       │
│                                               │
│ Network: [ bridged ▾ ]                        │
│ Image:   [ ghcr.io/.../workspace:default ▾ ]  │
│                                               │
│   [Cancel]                          [Open]    │
└───────────────────────────────────────────────┘
```

- 選択した runtime は **primary Workspace に適用** + **per-repo default として記録**（`repos.json` の repo entry に保存）
- 同 repo 内で後から worktree を追加する (`gwq add` 経由) ときの default になる
- "Don't ask again, use default for all repos" チェックボックス → 設定 (§ 9.5) の Global default を更新するショートカット
- LXD なし環境では LXD 系の選択肢は disabled + tooltip（§ 14.6）

### 9.2 Workspace 作成 modal（既存 worktree 追加時）

repo を既に開いている状態で新 worktree を作るときの modal:

- "Runtime" セレクタ（default = repo の per-repo default、 設定なしなら Global default）
- 選択肢: `host` / `lxd-container` / `lxd-vm` / `lxd-remote (...)` / `ssh-remote (...)`
- "Network" セレクタ: bridged / host-netns / tailnet
- "Image" 入力（lxd-* のとき、 default は per-repo default → Global default → `ghcr.io/tjst-t/palmux-workspace:default`）
- "Memory limit" / "CPU limit" 入力（lxd-vm のとき）

### 9.3 Workspace 行（Drawer）

- runtime kind の icon を branch 名の横に表示
- container/VM 起動中は spinner、 ready で点灯、 failed で警告
- runtime kind を変更したい場合は右クリック → "Change runtime…" で再起動を伴う変更（worktree 中身は維持）

### 9.4 Ports panel

§ 5.3 参照（Header の "P" ボタン位置、 常時表示）。

### 9.5 設定画面

- Global settings に "Default runtime" / "Default network" / "Default image"
- Per-repo override（repos.json に保存、 §9.1 の選択結果）
- Tailnet auth key 登録 UI
- LXD remote 登録 UI（`lxc remote add` のラッパ、 cert 認証 or trust password）

### 9.6 設定優先順位

```
runtime 決定: per-Workspace → per-repo default → Global default → 自動判定（LXD あり? → lxd-container, なし → host）
```

下位の設定が指定されていれば上位を上書きする。 `repos.json` の Workspace entry に `runtime` が明示されていればそれが最優先。

## 10. マイグレーションと後方互換

### 10.1 既存 Workspace

- すべて `runtime: host` として扱う（既存挙動と完全一致）
- migration スクリプト不要

### 10.2 既存 URL / API

- Workspace ID 体系は変更なし
- API は `Branch` の中に `runtime` フィールドが追加されるだけ
- frontend は新フィールドを optional として扱い、 未指定なら `host`

### 10.3 portman 移行

- 既存 `portman exec ...` 呼び出しは残す方が安全（少なくとも 1 phase）
- `make dev` / `make serve` は `palmux port exec` に置き換え（互換動作確認後）
- **portman repo はそのまま残置**（§ 14.10.7、 archive せず deprecation notice も入れない）
- palmux 側 README に「palmux に内製化された機能。 palmux 外でも `portman` 単体で同等のことができる」と紹介

## 11. リスクと対策

| リスク | 影響 | 対策 |
|---|---|---|
| LXD 起動失敗（環境による） | 開発不可 | host fallback、 起動時診断で明確なエラー |
| bind-mount 崩壊（権限・symlink）| skill/memory が見えない | 起動時に必須 mount の health check（§ 4.4 の AC で検証） |
| Claude CLI が container 内で誤動作 | session resume 失敗、 skill 不可視 | UID idmap + 同 path bind + settings.json 分離（§ 4.4） |
| VM 起動時間で UX 劣化 | Workspace open が遅い | 起動状態を progress UI で見せる、 pool 化を future work |
| remote runtime の network 切断 | tab が固まる | reconnect 機構 + retry policy |
| portman 移行漏れ | port 衝突再発 | `make dev/serve` を一括書き換え、 deprecation 期間を設ける |
| credentials の bind-mount 経由漏洩 | auth 流出 | SSH は agent forward、 ro マウント、 必要最小限のみ default 化 |
| host runtime での process leak | 既存問題そのまま | host runtime にも cleanup 機構を入れる（§ 12 参照）|
| custom image のメンテ落ち | 古い CLI version で破綻 | CI で週次 rebuild、 monitoring（§ 14.1）|
| agent push 失敗（network / disk full） | runtime 起動できない | retry + clear error、 agent binary を host 側で sanity check |
| agent と palmux のバージョン skew | RPC 互換性破綻 | push 方式で常に palmux と同 version を保つ（§ 14.10.6）|

## 12. host runtime の cleanup 機構（オプション拡張）

container/VM では Workspace close で自動的に process が掃除される。 host runtime ではこれが起きないので、 補完機構として:

- Workspace から起動した process の PID を tracking（cgroup or process group）
- 掴んでる port も tracking（§ 5.1 の host case）
- Workspace close 時に確認 dialog: "12 processes still running on host. Kill them?"
- 設定で auto-kill / leave / always-ask

実装はやや重いので **後続フェーズ**。 host runtime の魅力を上げる polish。

## 13. 実装フェーズ案（参考、 Sprint 化前）

ここは Sprint 提案ではなく、 実装の「塊」感を見るための参考分割。

| Phase | 内容 | 依存 |
|---|---|---|
| **A0** | `palmux-agent` binary 設計 + JSON-RPC プロトコル定義 + UDS で local PoC | — |
| **A** | `WorkspaceRuntime` interface + `host` 実装（trivial）+ `lxd-container` 実装（agent push + systemd start）+ bind-mount 戦略（idmap, 同 path, settings.json 分離）+ Repository open modal の Runtime 選択 (§ 9.1) + Workspace 作成 modal の Runtime 選択 (§ 9.2) + § 4.4 の動作検証 AC | A0 |
| **A'** | custom image `ghcr.io/tjst-t/palmux-workspace:default` の build パイプライン（palmux 本体 repo `images/`、 GHCR 配布、 CI 週次 rebuild）| A の途中で並行 |
| **B** | Ports panel UI（Header の "P" ボタン置換、 常時表示、 §5.3）+ LXD proxy device 連携 + auto-detect（agent 内 ticker で軽量 poll）+ host runtime での port tracking | A |
| **B'** | Claude 用 MCP tool (`expose_port` 系 for container / `allocate_port` 系 for host) + runtime に応じた tool 切替 + runtime hint auto-inject + `auto-allow-localhost` 設定 | B |
| **C** | portman 機能の内製 allocator（`internal/port/`）+ `palmux port` subcommand + `make dev/serve` 移行（palmux は portman に依存しなくなる、 portman repo はそのまま残置）| 独立、 B と並行可 |
| **D** | `lxd-vm` 実装 + virtiofs bind-mount + agent vsock/TCP 接続 + 起動 progress UI | A, A' |
| **E** | `lxd-remote` 実装 + agent への SSH tunnel + remote port forward | A, B |
| **F** | `ssh-remote` 実装（LXD 入れられない remote 対応、 agent を `scp` push）| A |
| **G** | network policy（host-netns / tailnet auth-key 管理）| A |
| **H** | host runtime の cleanup 機構（process / port tracking）| A |
| **J** | INSTANCE 廃止 + palmux dev workflow を LXD container 化 + `--tmux-prefix` 整理 | A, A', C |

A0 → A & A' & C が最初の山。 A' (image build) は A 並行、 C (allocator) は完全独立。 D 以降は需要に応じて。 J は A〜C が安定してから。

`Runner` 概念は § 14.9 で v1 範囲外に決定。

## 14. 決定事項（レビュー反映 2026-05-08）

初回レビューで判断が出た論点。 残りの **真の Open** は § 14.10 にまとめ直し。

### 14.1 container image を palmux 側で提供する（custom image 方式）

**決定: カスタム image `ghcr.io/tjst-t/palmux-workspace:default` を palmux チームでメンテする。**

- `tailscale`, `ansible`, `gh`, `claude`, `git`, `tmux`, `systemd` 等を pre-install
- **`palmux-agent` は焼き込まない**（§ 14.10.6）— ランタイム push で palmux version と一致させる
- 想定メンテコスト: **月 1〜2 時間**（CI 自動化前提）
  - GitHub Actions で base (ubuntu:24.04 LTS) のセキュリティアップデート追従 (週次 rebuild)
  - 主要 CLI の version bump (claude / gh / tailscale 等が release されたとき trigger)
  - 配布先: **GHCR**（§ 14.10.1）— LXD 5.0+ は OCI 直 launch 可
  - build 場所: **palmux 本体 repo の `images/workspace-default/`**（§ 14.10.2）
- バリアント: `:default` / `:gpu` (CUDA 入り) / `:minimal` (CLI なし、 軽量) を用意
- ユーザは settings で `image: ubuntu:24.04` などに override 可能（生 ubuntu に戻せる、 ただし agent push は同じく動く必要あり = systemd と最低限の utility は要る）

### 14.2 Workspace と container の lifecycle は 1:1

**決定: 1:1。 Workspace close = container destroy。**

- long-live container option は **入れない**（リークしてゴミが残るリスクの方が大きい）
- 「state を保ちたい」ニーズが将来出てきたら、 LXD snapshot を別 UI で扱う（Workspace fork 的な）

### 14.3 Files タブは runtime API 経由で読む（一貫実装）

**決定: 全 runtime で同じ runtime API パスを通す。**

- bind-mount は実装の最適化として host / lxd-container 内部で fast path を取ってよい（API 表面は統一）
- パフォーマンス課題が出たら個別最適化、 API 二重化はしない
- remote runtime と同じコードパスで動くため、 機能差が生じない

### 14.4 port expose は手動 + Claude 用 MCP API

**決定: UI からは manual expose のみ。 ただし Claude が自分で expose できる MCP tool を提供。**

- palmux に in-process MCP server を生やし、 以下のツールを Claude に公開:
  - `expose_port(port: int, name?: string, public?: bool) -> URL`
  - `unexpose_port(name: string)`
  - `list_exposed_ports() -> [{name, internal, host, url}]`
- Claude が dev server を起動した直後、 自発的に expose して URL をユーザに見せられる
- "auto-detect → 全部自動 expose" モードは入れない（security リスク回避）
- 設定で `claude_can_expose: false` に切れば MCP tool 自体を hide

### 14.5 runtime config はワークスペース単位で保存

**決定: per-Workspace に `repos.json` 内格納。 settings.json には global default のみ。**

```json
// repos.json (例)
{
  "repos": [{
    "id": "tjst-t--palmux2--a1b2",
    "branches": [{
      "id": "feature--x--7a8b",
      "runtime": {
        "kind": "lxd-container",
        "image": "palmux/workspace:default",
        "network": { "mode": "bridged" }
      }
    }]
  }]
}

// settings.json (例)
{
  "defaultRuntime": {
    "kind": "lxd-container",
    "image": "palmux/workspace:default"
  }
}
```

### 14.6 LXD なし環境の UI

**決定: 自動インストール促進はしない。 選択肢は greyed out + tooltip。**

```
Runtime:
  ○ host                    (always available)
  ○ lxd-container           [disabled — LXD not installed. See: ...]
  ○ lxd-vm                  [disabled — LXD not installed]
```

tooltip からは installation guide へのリンクのみ出す（押し付けない）。

### 14.7 macOS は **サポート対象外**

**決定: Ubuntu のみサポート。 macOS の動作保証はしない。**

- 影響: § 2.2 の比較表から macOS 列を削除
- § 2.4 default runtime 選択ロジックから macOS 分岐を削除
- README に "Supported OS: Ubuntu (Linux)" を明記
- Docker / Multipass 経由の擬似サポートも検討しない
- 開発者は Ubuntu か Ubuntu container 内で開発

### 14.8 INSTANCE 機構は将来的に廃止、 palmux 自身も LXD container 内で開発

**決定: `make dev INSTANCE=...` は新仕組み稼働後に削除。 palmux 開発も LXD container 内で行う。**

- bootstrap 問題は「palmux dev = LXD container 内で動く palmux」で解決:
  - host palmux は普段使い用（host tmux を管理）
  - container palmux は dev 用（container 内 tmux を管理、 host palmux と完全独立）
  - tmux session が namespace 分離されるので INSTANCE の sync_tmux 衝突問題が消える
- `--tmux-prefix` カスタマイズも将来的に不要になる可能性あり（要検証）
- 移行は Phase A 完了後（Workspace runtime が LXD で実用に達してから）。 完全削除までは INSTANCE は残す

### 14.9 Runner（β: 外部 eval 送り）は Phase 1 から除外

**決定: `Runner` 概念は v1 に入れない。 重い eval は `lxd-remote` / `ssh-remote` runtime で対応。**

- 理由: Workspace runtime 自体が remote 対応する以上、 別概念は冗長
- 「重い eval を送る」use case = 「remote runtime の Workspace を1つ作って、 そこで実行する」で済む
- snapshot 送信・job キュー・並列 runner 管理のような CI 的概念は palmux の責務外
- 必要が見えてきたら別 Sprint で再提案

→ § 8 はこの方針に合わせて「**deferred**」に変更し、 詳細は最小化。

### 14.10 決定事項（レビュー Round 2 反映 2026-05-08）

#### 14.10.1 container image registry: **GHCR**

- `ghcr.io/tjst-t/palmux-workspace:default` 等で配布
- LXD 5.0+ は OCI image を直接 launch 可能（`lxc launch docker:ghcr.io/tjst-t/palmux-workspace:default`）
- 別 LXD image server を立てる必要なし

#### 14.10.2 image build パイプライン: **palmux 本体 repo に `images/` ディレクトリ**

- `images/workspace-default/Dockerfile` (or `cloud-init.yaml`)
- `.github/workflows/build-image.yml` で週次 + tag push 時に rebuild + push to GHCR
- 別 repo に分けない（version sync が楽）

#### 14.10.3 MCP tool 権限フロー: **既存の Claude タブ permission framework に乗せる**

> レビュー Q: 「ここよく理解できないので詳しく知りたいけど、 セキュリティよりは今と同じように Palmux が使えることが優先」

**詳しく:**

palmux の Claude タブは現状すでに **permission framework** を持っている（CLAUDE.md `04-ui-requirements.md` および `05-claude-agent-tab.md` 参照）。 Bash や WebFetch のような tool を Claude が呼ぶと、 palmux が permission_prompt を出して user に「許可 / 拒否 / always-allow」を選ばせる。 always-allow を選ぶと `.claude/settings.json` に書かれて、 以降そのコマンドは prompt なしで通る。

`expose_port` も同じ機構に乗せる:

| 動作 | 既定の挙動 |
|---|---|
| Claude が `expose_port(5173)` を呼ぶ | 初回は permission_prompt を表示 → user が "always-allow for this Workspace" を選択 |
| 以降の `expose_port` 呼び出し | 確認なしで即 expose、 Activity Inbox に通知のみ |
| `expose_port(port, public=true)` (LAN 公開) | **常に確認**、 always-allow 不可 |

**「今と同じように使える」という user 要望への対応:**

- `claude_can_expose: "auto-allow-localhost"` を default にする setting を提供
- これを on にすると、 `expose_port(public=false)` (= 127.0.0.1) は **初回も含め即許可**、 通知だけ
- LAN 公開だけは security のため確認を残す
- セキュリティ重視のユーザは `auto-allow-localhost` を off にして従来の prompt 動作にできる

つまり default は「ほぼ Claude が自由に expose できる」体験。 既存 Bash 等よりむしろ permissive な扱いになる。

#### 14.10.4 UID 戦略: **host 1ユーザ前提、 idmap で 1:1**

- host UID = container UID = `1000` (or 起動 user の UID) で固定
- 複数 host ユーザ・複雑な mapping は当面サポートしない（必要が出たら個別対応）
- `lxc config set <inst> raw.idmap "both 1000 1000"` を palmux が自動付与

#### 14.10.5 Workspace migration: **手動再作成、 UI なし**

- 既存 host runtime Workspace は host runtime のまま
- container 化したい場合は user が新 Workspace を作って worktree を切り直す
- 「不便すぎたら考える」スタンス、 v1 では migration UI を作らない

#### 14.10.6 container 内 palmux agent: **入れる（push 方式）**

> レビュー: 「あったほうがきれいなら、 入れる前提で考えたい。 よく検討して。」

**結論: 入れる。 ただし image に焼き込まずランタイムで push する。** § 6.4 で詳述。

採用の理由:

| 観点 | agent あり | agent なし |
|---|---|---|
| **`lxc exec` syscall コスト** | 接続 1 本維持で amortize | 操作ごとに `lxc exec` 起動（10ms オーダ） |
| **Files API** | native fs op、 fast | `lxc file pull/push` (fork) もしくは bind-mount fast path のみ |
| **Port 検出** | 内部 ticker で軽量 | `lxc exec -- ss -tln` を周期 fork |
| **remote runtime と同じ実装** | ◎ | local 専用最適化と remote 専用 SSH 実装で分岐 |
| **process tracking** | /proc を直接読める | `lxc exec` で都度実行 |

**push 方式の意図:**

- image に焼き込むと **palmux version と image version の skew** が発生する（palmux update 時に image rebuild 必須）
- 解決: container 起動直後に palmux が `lxc file push palmux-agent <inst>:/usr/local/bin/palmux-agent` で **静的 binary を毎回 push**、 そのあと systemd 経由 or `lxc exec -- palmux-agent --socket=...` で起動
- これで agent version と palmux version が常に一致、 image は CLI 一式（claude/gh/tailscale/ansible）のみ持てば良い
- size: Go static binary 5〜15 MB、 push は 1 秒以下

詳細設計は § 6.4。

#### 14.10.7 portman の扱い: **palmux が独自 allocator を持つ。 portman repo は残す**

> レビュー: 「host runtime 用のポート管理ってどうする？ そこが問題ないなら portman は使わない。 でもどのみち portman のレポジトリ自体はそのまま残しておく。 これはこれで価値があると思うので。」

**決定:**

- palmux に **独自 port allocator** を実装（`internal/port/`）
- すべての runtime（host / container / VM / remote）で同じ allocator を使う
  - host runtime: 「Workspace が掴んだ port」を allocator に登録 → cleanup 時に kill
  - container/VM runtime: host 側の forward port を allocator から取得
- `make dev` / `make serve` も `palmux port exec ...` に切替
- **palmux は portman に依存しない**（外部依存ゼロ化）
- **portman repo は残す**（archive しない、 deprecation notice も入れない）
  - 非 palmux ユーザの Claude CLI 用ツールとして独立した価値を持つ
  - 互換 format（`~/.config/portman/ports.json`）は palmux も同じ場所に書くオプションを提供 → 共存可能
  - palmux の README で「palmux に統合された機能。 palmux 外で使うなら portman 単独で」と紹介

## 15. 次のステップ

1. **本ドキュメントを再レビュー**（§ 14.10 Round 2 決定の確認、 § 6.4 agent 設計の妥当性チェック）
2. **PoC（Sprint 化前の地固め）**:
   - (a) `lxc launch palmux/workspace:default` → bind-mount (`/home/ubuntu/.claude` etc.) → `claude --resume` で host で開始したセッションが container 内で resume できることを確認
   - (b) `palmux-agent` の最小プロトタイプ（`ListListeningPorts` + `ReadFile` のみ）を Go で書き、 UDS 経由の RPC を通す
   - (c) `lxc config device add proxy` で port forward が想定通り動くことを確認（特に container 再起動時の永続性）
3. PoC 結果を反映して Phase 分割 (§ 13) を Sprint 提案 (`docs/ROADMAP.json` への追加) に変換
   - **A0** (agent プロトコル定義 + PoC) → **A** (`WorkspaceRuntime` + host + lxd-container + bind-mount) → **A'** (image build) を Phase 1 として束ねる候補
   - **C** (port allocator 内製) は完全独立で並行
4. Phase A 着手
