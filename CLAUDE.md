# CLAUDE.md — Palmux v2

> Claude Code がコードを生成・修正する際に参照するプロジェクトルール。

## 関連ドキュメント

詳細仕様は `docs/original-specs/` にある。実装で迷ったら原典を参照する。

| ドキュメント | 内容 | 参照タイミング |
|---|---|---|
| [01-architecture.md](docs/original-specs/01-architecture.md) | アーキテクチャ全容（ドメインモデル、API、WS、ルーティング、ADR） | 設計判断・API追加時 |
| [02-CLAUDE-rules.md](docs/original-specs/02-CLAUDE-rules.md) | 本ルールの原典 | このCLAUDE.mdで不足したとき |
| [03-implementation-plan.md](docs/original-specs/03-implementation-plan.md) | 初期実装計画（Phase 0〜10）**※完了済みの歴史的文書** | 初期実装の経緯を辿るときだけ |
| [04-ui-requirements.md](docs/original-specs/04-ui-requirements.md) | UI 詳細（Activity Inbox、⌘K パレット、Toolbar 2モード） | UI 実装時 |
| [05-claude-agent-tab.md](docs/original-specs/05-claude-agent-tab.md) | Claude タブ (stream-json + MCP) Phase 1 設計書 | Claude タブ実装時 |
| [06-claude-tab-roadmap.md](docs/original-specs/06-claude-tab-roadmap.md) | Claude タブ Phase 2+ ロードマップとコア共通化計画 | 機能拡張時 |

**仕様の優先度**: `04-ui-requirements.md` は v2.1 で `02-CLAUDE-rules.md` より新しい記述（Toolbar 2モード化、Activity Inbox、⌘K パレット追加）を含む。UI 実装時は 04 を優先。Phase 2 以降の Claude タブ拡張は 06 を主、04 を補助参照。

## 実装ステータス

現在: **v0.16.0 リリース済み**。**次の一手はユーザ判断待ち** — Sc4f091 でリリースブロッカー 2 件 (in-container 通知フック欠落 / `palmux-shared` プロファイル競合) を修正済みで、これを踏まえて次リリースを出すかどうか。

- **未完了 Sprint**: `Sfa2bab` (実機ドッグフーディング検証) が `partial`。原因は Sc4f091 で解消済みだが Story ステータスは未クローズ。
- **恒久修正が残る既知課題**: `palmux-shared` incus プロファイルがホスト全体で単一な件は緩和策のみで、恒久対応 (per-instance namespacing 等) は backlog。
- **タブ層のリファクタ進行中** (branch `refactor/tab-reducer`): `Sd0e1a9` で ADR-0012 (タブ集合の導出を純粋関数化) を実装済み。**main へのマージは `Sfa2bab` のリリース判断後**。次は `S3f3cb2` (タブ操作のレイテンシ解消)。
- **計画中**: Control/Worker による並行開発の安全化、claude-tui の tmux 回帰 (ADR-0009)、Docs タブ (ADR-0010/0011)。

> **「Phase」という区切りは使わない**（2026-07-20 廃止）。原典 03 の Phase 0〜10・VISION の発展段階・ROADMAP の `phase` フィールドで 3 体系が並存し番号が衝突していたため、区切りは **ROADMAP.json の Sprint の並びと `milestone: true`** に一本化した。新しく Phase 番号を導入しないこと。

真実がどこにあるか:

| 知りたいこと | 見る場所 |
|---|---|
| 進捗・Sprint の現状 | **`docs/ROADMAP.json`** (source of truth) + git タグ |
| Sprint ごとの決定ログ | `docs/sprint-logs/{SprintID}/decisions.json` |
| 過去の実装内容・経緯 (Sprint 別) | **`docs/implementation-history.md`** |
| load-bearing な設計判断 | **`docs/DESIGN/`** — `adr/` · `domain.json` · `system.json` · `data.json` · `non-functional.json` |
| プロダクト意図 / 判断ルール | `docs/VISION.json` · `docs/DESIGN_PRINCIPLES.json` |

> `docs/DESIGN/` は `design` skill が管理する。**accepted な ADR は sprint plan / run で交渉不可の制約**として扱い、矛盾する実装が必要になったら `design adr` でユーザに amend/supersede を諮る。`tentative: true` の ADR は助言扱い。

### 確定済みプロジェクト規約

- **Go モジュールパス**: `github.com/tjst-t/palmux2`（リポジトリ名と一致。仕様書中の `github.com/tjst-t/palmux` は palmux v1 の名前で、v2 では palmux2 を使う）
- **設定ファイル保存先**: 開発時は `./tmp/`（既存の本物 palmux と干渉させないため）。本番想定の `~/.config/palmux/` は CLI フラグ `--config-dir` で切り替える
- **ポート**: dev サーバーは [portman](https://github.com/tjst-t/port-manager) 経由で起動する。`make dev` / `make serve` が `portman exec --name {svc} -- cmd --port {}` を呼ぶ。ソースにポート番号をハードコードしない
- **パッケージマネージャ**: npm（pnpm/bun も利用可だがデフォルトは npm）
- **Toolbar モード数**: 2モード（normal / claude）。02-CLAUDE-rules には normal/shortcut/claude/command の4モード記載があるが、04-ui-requirements v2.1 で 2モード化されており **04 が正**

### サーバー起動

- `make dev` — Vite dev + Go サーバー（hot reload）。portman 経由、フォアグラウンド実行
- `make serve` — Go サーバー単体（embed 済みフロント）を **バックグラウンド** で起動して即シェルに戻る。再実行すると前のプロセスを kill してから起動しなおす。PID: `tmp/palmux.pid`、ログ: `tmp/palmux.log`
- `make serve-stop` — バックグラウンド instance を停止
- `make serve-logs` — `tmp/palmux.log` を tail
- `make {dev,serve,serve-stop,serve-logs} INSTANCE=<name>` — portman 名・PID/ログファイルにサフィックスを付け、ホスト用 instance と並走させる。**S009-fix-3 で `--tmux-prefix=_pmx_<name>_` も自動付与**。これでホスト用 palmux2 (`_palmux_*` セッション) と dev 用 palmux2 (`_pmx_dev_*` セッション) は tmux 名前空間が分離され、 双方の `sync_tmux` ループが互いのセッションを zombie として kill しあわない。詳細は [docs/development.md](docs/development.md)
- サーバー起動スクリプトを作成・変更する場合は portman ガイドを参照: https://raw.githubusercontent.com/tjst-t/port-manager/main/docs/CLAUDE_INTEGRATION.md
- `.env` ファイルは `.gitignore` に追加（git commit しない）

### palmux2 自身の中で palmux2 を開発するときの注意

ホスト用 palmux2（普段 Claude CLI を動かしている方）の `make serve` は **その palmux2 が管理している tmux セッション ＝ 自分が今操作している Claude CLI** を巻き込んで死ぬ。bootstrap 問題なので、開発は `gwq add -b dev` で別ブランチの worktree を切り、`INSTANCE=dev` で別 portman 名・別ポートで起動する。具体的な手順は [docs/development.md](docs/development.md) を参照。

### palmuxOS アプライアンス (qcow2) を実機評価する (2026-07-15確立、2026-07-17整理)

palmuxOS (Sb14caa) は NixOS アプライアンスなので通常の `make serve` では評価できない。**実機評価用に3種類のホスト役割がある**（具体的なホスト名/IP/認証情報は `docs/local/eval-hosts.md` — gitignore 対象、リポジトリにcommitしない）:

1. **ビルド用ホスト**: 未リリースソースから `nix build .#appliance-qcow2` / `palmux2-local` をビルドする。**ネスト仮想化の深さ制約に注意**: ビルド内部で nixos-generators/disko がさらに qemu を起動するため、ビルドを実行する箱自身が「もう1段 nested virtualization できる」必要がある。素の実機 (L1) や L1 上に直接建てた VM でビルドすること — その VM のさらに上に建てた入れ子 VM (L2) の中でビルドしてはいけない (内部 qemu が L3 になり KVM アクセス不可、`chmod 666 /dev/kvm` 等の権限系対処では直らないハードウェア制約)。
2. **差分適用評価用ホスト**: 既に動いている永続インスタンスに対して、(a) `make build-linux` したバイナリを差し替えて `systemctl restart` するか、(b) on-appliance flake の `palmux` 入力をローカルソースへの `path:` に向け直して `nixos-rebuild switch` するか、のどちらかで変更を適用して検証する。前者は Go ソースだけの素早いsmoke (NixOSモジュール変更やフレッシュインストール検証には使えない)。後者は「リリース→ローカル更新」という遷移そのものの検証 (S31ad96-2で確立、`docs/sprint-logs/S31ad96/verification-S31ad96-2.md`)。**どちらも「今動いているインスタンスの挙動」しか確認できず、フレッシュインストール直後の状態は確認できない** (初回起動oneshotは再実行されない、既存stateが残る)。
3. **クリーンインストール用ホスト**: 評価のたびに作り直す。フレッシュインストール系Story (初回起動oneshot、オンボーディング等) の検証はここでしかできない。手順: リリース版なら `gh release download` で取得、未リリースならビルド用ホストでビルドして転送。COWオーバーレイ (ベースqcow2は無傷) + 新規cloud-init NoCloud seed で毎回真っさらに起動する。**cloud-init seedは`name: ubuntu`ではなく`name: palmux`を使うこと**（アプライアンスの実ユーザー名は`ubuntu`ではなく`palmux`, uid 1000, home=`/home/ubuntu`。`ubuntu`名だと別uidの無関係ユーザーが作られSSH鍵が効かない — 2026-07-15に実際にハマった）。

**前提**: 作業する Claude 自身は通常このリポジトリの incus コンテナ (Workspace) の中で動いている (`findmnt -no SOURCE /` で incus の rootfs パスが出ればコンテナ内)。コンテナには `/dev/kvm` が渡っていないため、**ホスト実機に一度SSHで抜けてから**作業する必要がある（コンテナ内で `incus`/`qemu` を叩いても、それはホストではなくコンテナのネームスペースで実行される）。

具体的なホスト名・IP・SSHコマンド・完全な手順は `docs/local/eval-hosts.md` を参照。このファイルはgitignore対象なので、初めて作業する環境では無い場合がある — その場合はこの節の方針に沿って新規に用意し、`docs/local/eval-hosts.md` に追記していく。

### autopilot / sprint auto でサブエージェントに実装を委譲するときのルール

**コンパイル + unit test だけで「完了」とせず、必ず E2E 検証まで行う**。`make serve INSTANCE=dev` で立てた別ポートの独立インスタンスに対して Playwright (headless) で UI / WS / API 経路を叩いて確認する。詳細と「スキップが許される条件」は [docs/DESIGN_PRINCIPLES.json](docs/DESIGN_PRINCIPLES.json) の `forbidden` / 自律実行ルール (S028 で .md → .json に正典化) を参照。

#### 長時間かかる実機テストの待ち方 (2026-07-19 に実際に事故った、このリポジトリ固有の注意点)

このリポジトリの検証は実 incus コンテナ・実 qemu VM・実 qcow2 ビルド・リモートホストへの SSH など、**1回が数分〜数十分かかる実機作業**が中心になる。他のリポジトリではほぼ発生しないが、ここでは「起動して完了を待つ」パターンが頻出し、待ち方を間違えるとサブエージェントが応答を返さないまま止まる。S2b5691 / Sfa2bab / Sc4f091 で計6回以上発生し、一度は放置された実インスタンスが**他の無関係な稼働中コンテナに10分以上干渉する**インシデントになった。

**ルール:**

- **10分以内で終わるなら `run_in_background: false`(同期実行)**。Bash はコマンド終了までブロックするので、これが一番確実かつ単純。incus コンテナの E2E (5〜10分) はほぼこれで足りる。
- **10分を超えるなら**(Bash の timeout 上限が10分のため) `run_in_background: true` で**実コマンドそのもの**を起動する。detach されるので10分制限は掛からず、**プロセス終了時に完了通知が1回来る**。
- **待機目的で `Monitor` を使わない**。Monitor は「複数のイベントを流し続ける」ためのツールで、「終わったら1回教えて」用途には設計されていない。特に **`tail -f` は禁止** — 自分から終了しないので終端シグナルが原理的に発生せず、エージェントは永久に来ない通知を待つ (実際にこれで2回停止した)。
- 条件待ちのループを書く場合、**成功マーカーだけを待たない**。`until grep -q "ALL PASS"` のような書き方は、プロセスが SIGTERM/クラッシュで死んで成功マーカーを書かずに終わった場合に永久に回り続ける (実際にこれで停止した。`timeout` の SIGTERM で Python の `finally` が走らず cleanup マーカーが書かれなかったケース)。失敗・異常終了パターンも必ずマッチさせる。
- **サブエージェント(レビュー/検証)に、さらに孫エージェントを生やさせない**。孫の完了通知が親に届かず、親が2時間沈黙した事例あり。追加調査が必要ならトップレベルのオーケストレーターが並列で直接立てる。
- 起動した throwaway インスタンス/コンテナ/tmux セッションは、**失敗・タイムアウト時も含めて同一ターン内で必ず片付ける**。`timeout` による SIGTERM では Python の `finally` が走らないことがあるので、片付けを script の `finally` だけに依存しない。

実装が進んだら、本 CLAUDE.md を必要に応じて更新する（ディレクトリ構成の実態反映、確定した規約の追記、仕様変更の反映など）。

## プロジェクト概要

Palmux は Web ベースのターミナルクライアント。tmux セッションをブラウザから操作する。Go シングルバイナリ（フロントエンド embed）、PC / モバイル両対応。**複数の Claude Code を並行運用する**ユースケースを重視。

## 技術スタック

| レイヤー | 技術 |
|---|---|
| バックエンド | Go 1.25+, net/http, nhooyr.io/websocket |
| フロントエンド | React 19, TypeScript, Vite, React Router v7 |
| ターミナル | xterm.js 5.x |
| 状態管理 | Zustand |
| スタイリング | CSS Modules |
| ビルド | Makefile, embed.FS |

## ドメインモデル（最重要）

**tmux はバックエンドの実装詳細。UI やドメインロジックに漏れ出してはならない。**

```
Repository (ghq, Open されたもの)
└── Workspace (git worktree path = Open)   ← S1e8d02: 旧称 Branch
    │  identity = worktree path (固定)
    │  attribute: name = 現在の HEAD branch (動的)
    └── TabSet (タブは Provider が生成。順序 = 登録順)
        ├── Claude  (terminal — protected, 1つ固定)
        ├── Files   (REST view — protected, 1つ固定, tmux window なし)
        ├── Git     (REST view — protected, 1つ固定, tmux window なし)
        ├── Sprint  (REST view — Conditional: docs/ROADMAP.json があるときだけ生成, tmux window なし)
        └── Bash[]  (terminal — 1つ以上必須, 追加/削除可)
```

### S1e8d02 用語対応表

実装上の Go 型名・URL の path segment は当面 `Branch` のまま (rename は将来 Sprint で実施)。 ドキュメント・概念モデル上は **Workspace = worktree path identity** が正。

| ドメイン語 | 実装 (Go) | URL path | 意味 |
|---|---|---|---|
| **Workspace** | `domain.Branch` | `/branches/{branchId}` | 1 worktree path = 1 identity |
| **head branch** | `Branch.Name` | (URL には出ない) | 動的属性。in-place `git checkout` で変わる |
| **WorkspaceID** | `Branch.ID` | `branchId` | path 由来 (`domain.WorkspaceSlugIDFromPath`) |

**identity rule**: Workspace は worktree path で identity を取る。 同じ path の上で `git checkout other-branch` が起きても、 ID・ tmux session・ Claude エージェント・ タブ・ Drawer 位置・ URL すべて不変。 変わるのは `Branch.Name` (= 表示ラベル) のみで、 `branch.head_changed` event が発行される。 close/open は path の出現/消失でしか起きない。

### タブモジュールシステム

新しいタブタイプの追加はコア変更不要。以下の手順で完結する:

1. `internal/tab/{type}/provider.go` — Provider interface 実装
2. `cmd/palmux/main.go` — `tabRegistry.Register({type}.New())`
3. `frontend/src/tabs/{type}/index.ts` — `registerTab(...)` + コンポーネント

Provider interface: `Type()`, `DisplayName()`, `Protected()`, `Multiple()`, `NeedsTmuxWindow()`, `OnBranchOpen()`, `OnBranchClose()`, `RegisterRoutes()`

オプショナルな capability:
- `tab.HeadChangedHook.OnBranchHeadChanged()` (S1e8d02) — 同 worktree 上で `git checkout` が走ったときに呼ばれる。 Provider が branch 名で内部 state を持っているなら実装する。 default 動作は no-op (= 何も実装しなくてよい)

### 2段階 Open モデル

1. **Repository Open**: repos.json に登録。以降そのリポジトリの worktree 変更を追跡
2. **Workspace Open**: worktree が存在すれば Open。tmux セッションは worktree path から導出 (S1e8d02)

ソースオブトゥルース: `repos.json`（Open リポジトリ）→ `git worktree list`（Open Workspace = path）→ tmux（導出）

### sync_worktree のイベント分類 (S1e8d02)

| ファイルシステム上の変化 | 発行される event | Drawer / Claude / tmux への影響 |
|---|---|---|
| 新 worktree path 出現 (`gwq add` など) | `branch.opened` | 新エントリ追加・ Provider の `OnBranchOpen` 起動 |
| 既存 worktree path 消失 (`gwq remove` など) | `branch.closed` | エントリ削除・ tmux kill |
| **同 path 上で `git checkout` (= head の付け替え)** | **`branch.head_changed`** | **エントリ残存・ Claude 生存・ ラベルのみ更新** |

## ディレクトリ構成（実態）

```
palmux2/
├── cmd/palmux/          # main.go + サブコマンド (hook, ptyhost, runtime, update, workspace...)
├── internal/
│   ├── domain/          # エンティティ + ID 生成。外部依存ゼロ
│   ├── config/          # repos.json + settings.json + config.toml/secrets.env
│   ├── store/           # メモリ状態ストア + ハイブリッドポーリング (sync_worktree)
│   ├── tmux/            # tmux Client interface + exec 実装
│   ├── runtime/         # Workspace Runtime 抽象 (host / incus-container)
│   ├── ptyhost/         # detached プロセスホルダ (no-halt-agent、ADR-0001〜0003)
│   ├── agent/           # マルチエージェント registry / Provider 抽象 (claude / codex / opencode)
│   ├── tab/             # タブモジュールシステム
│   │   ├── provider.go  # Provider interface + Registry
│   │   ├── agenttui/    # エージェント TUI タブ (claude-tui 等)
│   │   ├── agenttab/    # エージェント構造化タブ ※claudeagent とロジック重複 (backlog)
│   │   ├── claudeagent/ # Claude タブ (stream-json + MCP)
│   │   ├── bash/ files/ git/ sprint/ browser/ ports/
│   ├── server/          # HTTP ハンドラ + ルーティング (コア部分)
│   ├── auth/            # Cookie + Bearer + forward_auth SSO
│   ├── deploy/          # 設定 apply / reconcile
│   ├── selfupdate/      # GUI/CLI セルフアップデート
│   ├── notify/          # エージェント通知ハブ
│   ├── ghq/ gwq/ worktree/ worktreewatch/   # リポジトリ・worktree 操作
│   ├── commands/ apps/ attachment/ incusgroup/ static/
├── frontend/src/
│   ├── components/      # Drawer, Header, TabBar 等
│   ├── tabs/            # 1タブタイプ = 1ディレクトリ (+ 共通 terminal-view.tsx)
│   │   └── agent-tui/ claude-agent/ files/ git/ sprint/ browser/ ports/
│   ├── stores/ hooks/ lib/ styles/ types/
├── docs/                # ROADMAP.json · VISION.json · DESIGN/ · sprint-logs/ 等
├── images/ nix/ nixos/  # palmux-ws イメージ · Nix モジュール · palmuxOS
├── embed.go · Makefile · go.mod · flake.nix
```

## ID 体系

Slug+Hash 方式。人間可読 + 衝突回避。

```
Repository ID:  tjst-t--palmux2--a1b2          (owner--repo--hash4)
Workspace ID:   palmux2--7a8b   (primary)      (slug = repo dir name)   ← S1e8d02
                feature-x--3c4d (linked)       (slug = worktree dir basename)
Tab ID:         claude | files | git | bash:bash | bash:bash-2
```

- hash4 = SHA256 先頭4文字 (primary は repo path、 linked worktree は worktree path で hash)
- **WorkspaceID は worktree path から導出する (S1e8d02)**。 path が固定なので `git checkout` で ID は変わらない
- API URL にそのまま使える（スラッシュなし）
- グローバルキー: `{repoId}/{workspaceId}` または `{repoId}/{workspaceId}/{tabId}`
- 既存 (pre-S1e8d02) の URL ・ sessions.json は起動時 migration + 302 redirect で互換維持
- 旧 BranchID 計算 (`domain.BranchSlugID`) は migration の一回限りの参照用に残置されている。 新規コードは `domain.WorkspaceSlugIDFromPath` のみを呼ぶ

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

- `_palmux_` プレフィクスで Palmux 管理セッション識別 (デフォルト)。 `--tmux-prefix` で上書き可能 (S009-fix-3)
- ウィンドウは **name でルックアップ**（index に依存しない）
- Palmux が命名を独占管理しユニーク性を保証
- `IsPalmuxSession` は **prefix 完全一致 + post-prefix repoID に `--` を含む** ことを要求 (S009-fix-3)。 これにより別 instance の `_palmux_<word>_<repo>_<branch>` を誤って claim しない

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

URL スキーム: `/{repoId}/{branchId}/{tabId}`。Files はサブパスを持つ (`/files/<path>`)。Git は S029 で 2 カラム単一画面に統合され、 status / log / diff のサブルートは廃止。Sprint タブは `?view=` クエリ (`overview` / `detail` / `dependencies` / `decisions` / `refine`) でビュー切替。

```
/tjst-t--palmux--a1b2/main--e5f6/claude
/tjst-t--palmux--a1b2/main--e5f6/files/src/main.go
/tjst-t--palmux--a1b2/main--e5f6/git
/tjst-t--palmux--a1b2/main--e5f6/sprint?view=detail&sprintId=S010
/?right=...                                          # Split 右パネル
```

- ブランチ・タブ・Files パス・Sprint ビュー / Git 内の選択 (commit sha 等) の切り替えは `history.pushState`
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
アプリ設定（全デバイス共有）:   ~/.config/palmux/settings.json → GET/PATCH /api/settings
サーバ/インフラ master (Sa53137): ~/.config/palmux/config.toml（[server]/[public]）→ GET /api/deploy・POST /api/deploy/apply
シークレット (Sa53137):         ~/.config/palmux/secrets.env（user 所有 0600、GUI でマスク・書込のみ → POST /api/deploy/secrets）
デバイス固有（ブラウザ）:       localStorage（プレフィクス palmux:）
```

- `settings.json` は Toolbar/UI 等のアプリ設定、`config.toml` + `secrets.env` は serve のサーバ/インフラ設定の単一マスター（解決チェーン `flag > env > file > default`）。差分の反映は `palmux apply`（hot=in-process / restart=`systemctl --user restart palmux2`）、公開ドメイン/TLS の root 反映は `sudo palmux reconcile-system`。設計は `docs/unified-config-design.md`。

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
- E2E: `tests/e2e/sNNN_*.py` (Python + Playwright headless) で AC 単位の検証。 `make serve INSTANCE=dev` で立てた dev インスタンス (別ポート) を実体として叩く。 Sprint ごとに 1 ファイル、 acceptance criteria は `[AC-{StoryID}-{N}]` タグで紐付ける (sprint verify が自動チェック)。
- **重い実機テストは env-gate 慣習**: 実 systemd/実 incus/実 claude を要する or 実プロセスを多数 spawn するテストは、default `go test` (= `make test`) の並列実行で CPU 競合 flake + プロセス churn を招くため、env フラグで opt-in にする (`PALMUX_SURVIVAL_SMOKE=1` = ptyhost 生存/並走系、`PALMUX_REALINCUS_SMOKE=1` = 実 incus 系、`PALMUX_SPIKE_S862203_1=1` = 実 claude spike)。ロジックの fast な単体テストは default gate に残す。**priority_rule 0 に注意**: gate したテストも sprint verify で明示的にフラグ付き実行し、PASS 証跡を残す (skip して done は禁止)。default gate から外すこと自体は「実行しない」ではない。

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

- `_palmux_` プレフィクスはデフォルト値 (Orphan 判定に使用)。 S009-fix-3 で `--tmux-prefix` 経由で per-process に上書き可能になったが、 ユーザ向けの本番デプロイは `_palmux_` のままにする。 `INSTANCE=<name>` の dev rig だけが `_pmx_<name>_` を使う
- Claude タブ = 常に tmux window name `palmux:claude:claude`
- リポジトリ本体（IsPrimary）の Close は tmux kill のみ（worktree は消さない）。ブランチ名は main とは限らない
- IsPrimary の判定: `git worktree list --porcelain` で `.git/` ディレクトリ（ファイルではなく）を持つ worktree
- worktree の作成/削除は `gwq` コマンド経由。`git worktree add/remove` を直接呼ばない
- 起動時に tmux, ghq, gwq, git の存在をチェック。なければエラー終了
- 複数デバイス同時接続は tmux session group。attach 時に `__grp_{connId}` 作成、detach 時に kill
- Files / Git / Sprint タブは tmux window を持たない。REST API のみ (Provider が `NeedsTmuxWindow() == false`)
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

- **更新する**: 確定した規約・命名・パッケージ境界・現在のステータス（数行）
- **更新しない**: ロジックの詳細（コードを読めば分かる）、一時的な作業状態
- **Sprint 完了時にここへ実装内容を書き足さない**: 本ファイルは毎セッション全文が読まれるコンテキストコストなので、Sprint 別の実装内容・経緯は **`docs/implementation-history.md`** に追記し、ここは「現在の状態」数行のみ差し替える。過去に 46% がこの履歴で埋まり肥大化した（2026-07-20 に退避）
- **原典との整合**: 本ルールと `docs/original-specs/` で食い違いが出たら原典を優先するか、原典自体を更新するかを明示する
- **目安**: 本ファイルは 35KB 程度を上限とする。超えたら履歴・詳細を `docs/` へ退避する
