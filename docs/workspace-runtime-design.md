# Workspace Runtime — 設計（trim 版）

> このドキュメントは `feat/workspace-runtime` ブランチの初版設計（5 ランタイム種別・
> palmux-agent push 配布・port allocator 内製・GHCR イメージ運用を含む大型版）を、
> **実際にやりたいこと＝「host を汚さずにアプリを起動でき、ネットワークが分離される」**
> に絞って再スコープしたものです。初版の広い設計は将来検討事項に降格し、本書を
> main 取り込みの正準とします。
>
> 関連: [01-architecture.md](original-specs/01-architecture.md)（ドメインモデル）、
> [CLAUDE.md](../CLAUDE.md)（タブモジュールシステム、tmux 命名、ID 体系）。

---

## 0. TL;DR

- Workspace の実行環境を **`host` / `incus-container` の 2 種**だけ抽象化する。初版にあった
  `incus-vm` / `incus-remote` / `ssh-remote` は**将来検討**に降格。
- 目的は **host 汚染の防止**（dev server のポート衝突・プロセス leak・`npm -g`/`pip`/`apt`
  によるパッケージ汚染・ビルド成果物の散乱）と、**ネットワーク分離**。
- **リポジトリ群は共有する**。worktree 単体ではなく **`~/ghq` 丸ごと** と `~/.claude` を
  bind-mount し、「システムは隔離・リポジトリ/認証/skill/memory は共有」というモデルにする。
  → 他レポを直接参照できる利便はそのまま、host 汚染だけ止まる。
- **ポート管理モデル（最重要決定, §5）**: 隔離下では各レポがポートを確保する必要が消える。
  managed bridge でコンテナに IP を与え、リッスンポートを自動検出し、**Caddy が
  サブドメイン → `<containerIP>:<port>` を直結**する。**ホスト側ポートの確保はしない**。
  portman は **`host` ランタイム（非隔離フォールバック）専用**として残置する。
- **将来 Neko（仮想ブラウザ）を Workspace のタブとして載せる**前提を非機能要件に織り込む
  （§7）。そのため Runtime interface の `ExposePort` は **TCP/UDP + public フラグ**を
  表現できる形を維持する（HTTP/Caddy 専用に作り込まない）。
- 既存 Workspace は **`host` のまま**（後方互換、migration UI なし）。新規 Workspace の
  既定は Incus があれば `incus-container`、なければ `host`。

### 初版から削ったもの（＝将来検討に降格）

| 項目 | 初版 | trim 版の扱い |
|---|---|---|
| ランタイム種別 | host/incus-container/incus-vm/incus-remote/ssh-remote | **host / incus-container のみ** |
| container agent | `palmux-agent` を起動時 push し RPC | **当面 `incus exec` で代替**。interface は agent 化できる粒度に保つ |
| ポート確保 | `internal/port` allocator で portman を内製置換 | **作らない**。隔離下は確保不要、非隔離は既存 portman |
| イメージ運用 | GHCR + 週次 CI + 1GB 予算の `palmux-workspace:default` | **ストックイメージ（`ubuntu:24.04`）or 薄い自作で開始**。GHCR/CI は後で |
| bind-mount 範囲 | worktree + `~/.claude` | **`~/ghq` 全体 + `~/.claude`**（クロスレポ可視性を優先） |

---

## 1. 背景と動機

現状 palmux で Claude が起こすプロセスはすべて host 上で動く。結果:

- **ポート衝突** — 複数 Workspace で同じ dev server（`:3000` 等）を立てると衝突。今は各レポの
  Makefile が `portman exec` で回避しているが、レポ側の責務になっている。
- **プロセス leak** — Workspace を閉じても zombie が host に残る。
- **パッケージ汚染** — `npm i -g` / `pip install` / `apt install` が host に蓄積。
- **filesystem 汚染** — ビルド成果物・キャッシュ・ログが host に残る。

`incus-container` ランタイムはこれらを **コンテナ境界**で解決する。コンテナは close で
`incus delete --force` する**揮発**設計なので、汚染はコンテナと一緒に消える。

### なぜ netns 単体ではなく container か（決定の記録）

`feat/netns-isolation`（S034）は rootless な per-worktree network namespace（slirp4netns +
`nsenter`）でネットワーク分離だけを軽量に実現した実績がある。だが trim 版では **container を
正準**とする。理由:

1. **成熟度と outbound の信頼性** — S034 は「netns 内からの outbound HTTPS が `HTTP 000`」を
   env-skip として未解決のまま残した。これは slirp4netns（ユーザ空間 TCP/IP）の DNS/MTU 由来の
   典型的な脆さ。palmux の中身は `claude`（api.anthropic.com 必須）と `npm`/`git` であり、
   **外に出られないと即死**する。ここを手組みネットワークスタックに賭けない。Incus は
   ネットワークをカーネル bridge + 実 DHCP/DNS に委譲するため「ただ動く」。
2. **パッケージ/FS 汚染も止めたい** — netns はネットワークしか分けない。container なら
   パッケージ/FS/プロセス/ネットワークを一括で隔離できる。
3. netns は「host で軽く済ませたい人向けの代替」として design 上は残す（本書 §8）。ただし
   実装の第一選択ではない。

---

## 2. ドメインモデルへの追加

### 2.1 `WorkspaceRuntime`

各 Workspace は実行ランタイムを 1 つ持つ。

```
Repository
└── Workspace (worktree path = identity, S1e8d02)
    ├── runtime: host | incus-container        ← 本書で追加
    └── TabSet
        ├── Claude  (terminal)
        ├── Files / Git / Sprint (REST view)
        ├── Bash[]  (terminal)
        └── Browser (embed view)             ← 将来 (§7, Neko)。Bash の隣
```

- ランタイムは **per-Workspace**。Workspace ごとに `host` / `incus-container` を選べる。
- `host` は隔離なし（現状の挙動）。Incus 未インストール環境の自動フォールバックでもある。
- ランタイムが何であれ、**ドメインモデルとタブの構造は不変**。tmux と同様、ランタイムは
  「タブの中の terminal がどこで動くか」という実装詳細であり、UI/ドメインに漏らさない。

### 2.2 設定の解決順（priority chain）

```
per-Workspace 指定  →  per-repo 既定  →  global 既定 (settings.json)  →  host fallback
```

- per-Workspace: `repos.json` の該当 branch エントリの `runtime`
- per-repo: `repos.json` のリポジトリ既定
- global: `~/.config/palmux/settings.json` の `defaultRuntime`
- いずれも未指定なら Incus 在否を見て `incus-container` / `host` を自動選択。

### 2.3 後方互換

- 既存 `repos.json` の `runtime` 無し Workspace は **`host`** とみなす（挙動不変）。
- migration UI は作らない。ユーザが明示的に切り替えたときだけ container 化する。

---

## 3. Runtime interface（trim 版）

ランタイム差は 1 つの Go interface に閉じ込める。**`tmux.Client` を直接呼ぶ箇所を、
ランタイム経由に付け替える**のが実装の肝（初版が飛ばした「実行系への配線」はここ）。

```go
// internal/runtime
type Kind string // "host" | "incus-container"

type Runtime interface {
    Kind() Kind
    Config() Config

    // ライフサイクル。host は no-op に近い（即 ready）。incus は launch/delete。
    Start(ctx context.Context) error
    Stop(ctx context.Context) error
    Status() Status // State(ready/starting/stopped/error), Address, Error

    // terminal 起動。ここが tmux 直叩きの置き換え点。
    // host:   従来どおりホスト上で tmux。
    // incus:    `incus exec <inst> -- tmux ...`（将来 agent 化可能な粒度）。
    NewTmuxSession(ctx context.Context, session string) error
    AttachTmuxSession(ctx context.Context, session string) (io.ReadWriteCloser, error)
    Exec(ctx context.Context, cmd []string, opts ExecOpts) (ExecResult, error)

    // ポート。§5。**TCP/UDP 両対応・public フラグ必須**（§7 Neko/WebRTC のため）。
    ListListeningPorts(ctx context.Context) ([]ListeningPort, error)
    ExposePort(ctx context.Context, spec PortSpec) (PortMapping, error) // proto: tcp|udp
    UnexposePort(ctx context.Context, mappingID string) error
}

type PortSpec struct {
    Internal int
    Proto    string // "tcp" | "udp"
    Name     string
    Public   bool   // 認証境界の外に出すか
    // HostPort 0 = 確保しない（bridge 直結, §5）。>0 = proxy device でホストに写す。
    HostPort int
}
```

> **設計上の不可侵点**: `ExposePort` を「HTTP を Caddy で出す」専用に縮めないこと。UDP と
> public フラグを interface に残すかどうかが、将来 Neko（WebRTC, §7）が **retrofit ではなく
> 素直に載る**かの分岐点になる。第一実装が TCP/Caddy 経路だけを wire していてもよいが、
> **型は一般形を保つ**。

agent 化は将来の最適化（`incus exec` の往復が重い/PTY 制御が要るとき）。interface がこの
粒度なら、内部実装を `incus exec` → `palmux-agent` RPC に差し替えても呼び出し側は不変。

---

## 4. bind-mount 戦略

| マウント元 (host) | マウント先 (container) | mode | 目的 |
|---|---|---|---|
| `~/ghq` | 同一絶対パス | rw | **全リポジトリの共有**（クロスレポ参照の利便を維持） |
| `~/.claude` | 同一絶対パス | rw | 認証・skill・memory の透過共有（`claude --resume` が成立） |
| `~/.gitconfig`, SSH agent socket 等 | 同一パス | ro/rw | git 操作の継続 |

- **同一絶対パス + `raw.idmap "both 1000 1000"`** を前提にする。host の UID 1000 を
  コンテナに 1:1 で写すことで、bind-mount したファイルの owner が一致し、`claude` が
  「同じ絶対パスで自分の `~/.claude`」を見られる。Claude に「コンテナで実行して」と毎回
  指示する必要はない。**コンテナは UNPRIVILEGED のまま**（特権コンテナにはしない — 特権化は
  bind-mount 越しに in-container root = host root となり隔離の意味を失う）。
- **ホスト前提条件 (S8478ca-2 で確定)**: `raw.idmap "both 1000 1000"` は Incus デーモン (root 実行) が
  **host uid/gid 1000 を map する許可**を要求する。デフォルトの `/etc/subuid`・`/etc/subgid` は
  root に `root:1000000:…` の範囲しか与えないため uid 1000 を含まず、`incus start` が newuidmap で
  失敗する (エラーは `incus start` stderr ではなく `incus info --show-log` にのみ出る)。**両ファイルに
  `root:1000:1` の行を追加し `incus` を再起動**すると unprivileged のまま idmap が通る。これは
  install 手順 / INSTALL doc に明記する。実装は idmap/start 失敗時に privileged へ落とさず、この前提を
  示してエラーにする。
- **ホスト前提条件 (ネットワーク, S8478ca-2 で確定)**: コンテナは **外向きインターネットが必須**
  (in-container `claude` が api.anthropic.com に、`npm`/`apt` が各リポジトリに出る)。Incus の
  managed bridge (`incusbr0`) は NAT を張るが、**ホストに Docker が入っていると Docker の
  iptables `FORWARD` policy=DROP が incusbr0 の外向き転送を遮断**し、コンテナが gateway 以遠に
  到達できなくなる (apt も claude も死ぬ)。対策: Docker 不要なら停止/削除、必要なら
  `iptables -I DOCKER-USER -i incusbr0 -j ACCEPT` 等で incusbr0 forwarding を許可する。
  install 手順に明記する。これは netns 版の outbound-HTTPS 問題と同じ「隔離環境から外に出られないと
  即死」クラスの落とし穴で、Incus では host firewall を正すことで解消できる (slirp4netns のような
  ユーザ空間スタックの脆さとは別)。
- **汚染が止まる理由**: `~/ghq` と `~/.claude` だけが共有され、`/usr`・`/opt`・コンテナの
  `~/.npm` global・`apt` データベース等は**コンテナ固有**。`npm -g` も `apt install` も
  host に届かず、close で消える。
- **`~/.claude` 同時書き込み**: 同一 `~/.claude/projects/<path>` を複数ランタイムが同時に
  触る二重起動を防ぐため `.palmux-lock` を置く（初版の §4.4 を踏襲）。

### 揮発 container のコスト（明記）

container は close で破棄されるため、**コンテナ内に入れた dev ツールチェイン（node/go/rust 等）は
毎セッション消える**。これがパッケージ隔離の代償。吸収策:

- リポジトリの bootstrap スクリプト（mise/asdf 等）を open 時に流す、または
- よく使うツールを薄い自作イメージに焼く。

長寿命 container は **out of scope**（揮発が汚染防止の本質）。

---

## 5. ネットワーク / ポート管理モデル（最重要）

### 5.1 隔離が「ポート確保問題」を消す

現状 `internal/portman` は `portman list --json` を**読むだけ**で、確保しているのは各レポの
Makefile（`portman exec`）。`incus-container` 下では各コンテナが**独立した network stack**を
持つため、`:3000` も `:5173` も衝突しようがない。→ **各レポがポートを確保する理由が消滅**する。
レポは任意ポートに bind してよい（既存 Makefile の `portman exec` も、コンテナ内の私的な
ポート空間で動くので害なく動作するが、もはや load-bearing ではない）。

### 5.2 到達経路: managed bridge + Caddy 直結（案A・正準）

```
コンテナが incusbr0 上に IP を持つ (例 10.x.x.5)
  → palmux が `incus exec <inst> -- ss -tlnH` 等でリッスンポートを自動検出
  → Caddy が  <workspace>.palmux.<domain> → <containerIP>:<port>  のスニペットを張る
     （file-based snippet + `caddy reload`。netns/S034 の internal/netns/caddy.go が前例）
  → ★ ホスト側ポートの確保は一切不要
```

`incus admin init` の managed bridge では **host が incusbr0 の `.1` を持ち、コンテナ subnet に直結**して
いるため、host 上の Caddy → `containerIP:port` は追加ルーティングなしで届く。ネットワーク層の
到達性は**デフォルト構成で満たされる**。

### 5.3 localhost-bind 問題（方式不問の実害）と救済

ネットワーク層が通っていても、**dev サーバが `127.0.0.1:port`（localhost のみ）で listen** すると
`containerIP:port` は閉じている。Vite/Next 等のデフォルトがこれ。救済（**案A を崩さず、ホスト
ポート確保なし**）:

```
# コンテナ"内側"にリスナを置く proxy device（bind=instance）
incus config device add <inst> p3000 proxy \
    listen=tcp:0.0.0.0:3000 connect=tcp:127.0.0.1:3000 bind=instance
```

これで「コンテナの bridgeIP:3000 → 自分の 127.0.0.1:3000」が転送され、localhost-only サーバも
到達可能になる。**ホスト側ポートは消費しない**。補助的に `HOST=0.0.0.0` 等の runtime hint 注入も
併用してよいが、第三者製サーバを常に制御はできないため proxy device を堅い保険とする。

### 5.4 portman の最終的な居場所

| ランタイム | ポート確保の主体 | portman |
|---|---|---|
| **incus-container** | 誰も確保しない（任意 bind → 自動検出 → Caddy 直結） | **使わない** |
| **host（非隔離 fallback）** | 従来どおり各レポが `portman exec` | **そのまま残置** |
| UI の URL 一覧表示 | — | 既存 read 配線（`internal/portman`）そのまま |

→ 「palmux が portman を駆動して確保する」方式には**しない**。隔離 + Caddy が確保問題そのものを
消し、portman は非隔離フォールバック用に残す。

### 5.5 host ポート確保が必要になる例外: UDP / WebRTC（→ §7）

HTTP/TCP は上記で確保不要だが、**WebRTC（UDP メディア）は Caddy(HTTP) を通らない**。ここだけは
**UDP の host ポート（mux 1 ポート）を proxy device で出す**必要がある。詳細は §7。`ExposePort` の
`Proto="udp"` / `HostPort>0` 経路がこれを担う。

---

## 6. UI / 状態表示

- ランタイム選択は Repo Open 時 / Workspace 設定で。`incus-container` は Incus 未インストール時
  グレーアウト + Incus インストール案内のツールチップ。
- Header にランタイム chip、Drawer の Workspace エントリに state badge（ready/starting/
  stopped/error）。状態は `Runtime.Status()` 由来。
- modal は書き込みをしない（state の source は settings/repos.json）。初版 S0c6a1b の
  「Host ターミナル scope」（`repoId=host--0000`）とは**別概念**なので、`runtime.kind=host` と
  「Host タブ」の名前衝突に注意（UI ラベルで区別する）。

---

## 7. 将来: Neko ブラウザを Workspace タブとして載せる（非機能要件）

### 7.1 ドメイン上の位置づけ

**Browser タブは Workspace 配下のタブ**（Claude/Files/Git/Sprint/Bash と同列、UI 上は
**Bash の隣**）。タブモジュールシステムの新 Provider として追加する:

- `internal/tab/browser/provider.go` — `NeedsTmuxWindow() == false`（terminal でも REST でもない
  **embed view**）。`Multiple()` は当面 false（1 Workspace に 1 ブラウザ）、将来拡張可。
- frontend は **Neko の web UI を iframe で embed** するだけ。WebRTC のクライアント処理は Neko 側が
  行うので、palmux 自身は WebRTC を実装しない。iframe の向き先は Caddy 経由の
  `https://neko-<workspace>.palmux.<domain>/`。

> ねらい: ユーザが認証したブラウザセッション（ログイン済み cookie）を Neko が保持し、
> Claude Code が **playwright-cli の `connectOverCDP`** でその実ブラウザを操作する。

### 7.2 トポロジ

```
[workspace ランタイム]  Claude Code + playwright-cli
        │ CDP (bridge 経由, §7.4 の対策込み)
        ▼
[Neko ランタイム]  authenticated browser + WebRTC server
   ├─ HTTP/WS  → Caddy リバプロ（§5.2 どおり）→ Browser タブの iframe
   ├─ WebRTC   → UDP mux 1 ポートを proxy device でホストに（§7.3）
   └─ profile  → ホスト永続パスに bind-mount（§7.5）
```

Neko は workspace 本体とは**別コンテナ**にするのが素直（ライフサイクルを分離し、認証セッションを
workspace の開閉から切り離せる）。Neko が Docker 前提なら **Docker-in-Incus**（`security.nesting=true`,
systemd PID1）に乗る。重ければ Neko を直接 Incus コンテナとして palmux が起こす。
**ただしドメイン上は Workspace 配下のタブ**であることは変わらない（実装の container 配置と
ドメインのタブ位置は別レイヤ）。

### 7.3 WebRTC は Caddy を通らない（§5.5 の具体）

- Neko の web UI / シグナリング（HTTP+WS）→ Caddy リバプロで OK。
- WebRTC メディア（UDP）→ ICE でクライアントがコンテナの UDP ポートに直接到達する必要があり、
  Caddy(HTTP) は通らない。**ホスト到達可能な UDP ポートへの forward + NAT 告知が必須**。
- 軽減策: Neko を **`NEKO_WEBRTC_UDPMUX`（+任意 TCPMUX）固定 + `NEKO_WEBRTC_NAT1TO1=<ホスト到達IP>`**
  で構成し、必要を **UDP 1 ポート（+TCP 1）だけ**にする。エフェメラル range は開けない。
  その 1 ポートを `ExposePort{Proto:"udp", HostPort:NNNNN}` → UDP proxy device で出す。
- リモート / 対称 NAT を確実にするなら **coturn を 1 台**立てて `ICELITE + TURN` に寄せ、メディアを
  TURN の単一ポートに集約する。

### 7.4 CDP（playwright → Neko ブラウザ）の localhost / Origin ガード

playwright は既存の認証済みブラウザに `connectOverCDP` する（新規 launch ではない）。Chrome の
2 ガードに当たる:

- `--remote-debugging-port` は **127.0.0.1 バインド**がデフォルト → `--remote-debugging-address=0.0.0.0`。
- Chrome M111+ は **Host/Origin ヘッダ検査** → `--remote-allow-origins=*` 等。

対処は §5.3 の `bind=instance` proxy device と同じ仕組みで吸収（Neko コンテナ内で
`bridgeIP:9222 → 127.0.0.1:9222`）。または **playwright を Neko と同居**させ CDP を localhost に
閉じる。新しい機構は不要。

### 7.5 認証セッションの永続化（揮発 container の例外）

この機能の肝は「ユーザが認証したセッション」の保持。一方 container は揮発（§4）。よって
**Neko のブラウザプロファイル（cookie/ログイン）はホストの永続パスに bind-mount** する:

```
~/.config/palmux/neko-profiles/<WorkspaceID>  →  container の browser profile dir  (rw)
```

`WorkspaceID` は worktree path 由来で **`git checkout` でも不変**（S1e8d02）なので、同じ
Workspace を閉じて開き直しても認証が残る。「**コンテナは揮発・プロファイルは永続**」という
**意図的な例外**として明記する。

### 7.6 この機能のための今すぐの設計判断

将来 Neko を retrofit ではなく素直に載せるため、trim 版でも以下を**最初から**満たす:

1. `ExposePort` は **TCP/UDP + public フラグ**の一般形を interface に残す（§3）。
2. ランタイムは「揮発が原則だが、明示した path は永続 bind-mount できる」ことを表現できる
   （プロファイル volume の例外, §7.5）。
3. ドメインモデルに **Browser タブ（embed view, NeedsTmuxWindow=false）**の席を用意しておく
   （Bash の隣, §7.1）。第一実装は不要だが、タブ種別の追加がコア変更を要さない現行
   モジュールシステムの範囲で収まることを確認しておく。

---

## 8. 代替として残す: netns（host を軽く済ませたい人向け）

`feat/netns-isolation`(S034) の rootless netns 方式は、Incus を入れたくない/入れられない Linux で
**ネットワーク分離だけ**を軽量に得る代替として design 上は残す。採用条件:

- **前提**: §1.1 の outbound HTTPS（slirp4netns の `HTTP 000`）問題を解決できること。これが
  直らない限り `claude`/`npm` が外に出られず実用にならない。
- パッケージ/FS 汚染は防げない（ネットワークのみ）。
- 実装の**第一選択ではない**。container を正準とする。

---

## 9. 段階的な取り込み計画

1. **本書（trim 設計）を main に取り込む**（docs-first）。`feat/workspace-runtime` の初版実装は
   そのままマージしない（足場のみ・未配線・スコープ過大のため）。
2. **実装スプリント**: `host` + `incus-container` の 2 種 + bind-mount（`~/ghq` + `~/.claude`）+
   **ライフサイクルへの実配線**（tmux 直叩き → Runtime 経由）。ポートは §5（隔離=確保なし /
   Caddy 直結、非隔離=portman 残置）。`internal/port` 内製・agent push・GHCR は**入れない**。
3. **検証**: test VM（192.168.1.41）で Incus 上の Claude/Bash 起動、`~/.claude` 共有、`claude --resume`、
   2 Workspace の同ポート非衝突 + Caddy 到達、localhost-only サーバの bind=instance 救済を E2E で確認。
4. **将来スプリント（需要が固まってから）**: Browser タブ + Neko（§7）、必要なら agent 化 /
   薄い自作イメージ / incus-remote。

---

## 付録: 初版設計（大型版）との関係

`feat/workspace-runtime` ブランチの `docs/workspace-runtime-design.md`（920 行）は、本書が降格した
項目（5 ランタイム種別・agent push・port 内製・GHCR 運用）の詳細な検討記録として参照価値がある。
将来それらを再導入する際の一次資料として扱う（本書がそれらを否定するのではなく、**v1 スコープ外**に
置くだけ）。
