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

現在: **v0.12.0 リリース済み (Sb14caa = palmuxOS アプライアンス化マイルストーン)**。以降 main に **Sd44947 (Phase 6 マイルストーン: 共有フォルダの宣言化 profile-as-mold)** および **no-halt-agent マイルストーン (S3f2658 + S862203: palmux2 再起動を跨いだ claude 生存) を merge 済み** (次リリース候補)。Phase 0〜4 のコア + 磨き込みに加え、以降のスプリントで以下を実装済み:

- **S001〜S029**: Phase 0〜3 コア機能 + Phase 4 磨き込み (S016 Sprint Dashboard、S017 virtualization、S018 検索/export、S019 rewind、S020 タブ UX、S021 Subagent worktree、S022 モバイル UX、S023/S024 Drawer redesign、S025 fixture cleanup、S026 HTML preview、S027 Markdown SPA navigation、S028 JSON canonical roadmap、S029 [BREAKING] Git タブ minimal redesign)
- **S030〜S033**: ghq Repository 管理 UI、⌘K Command palette redesign、ユーザ定義 palette コマンド、Files タブ CRUD
- **S1e8d02 [BREAKING]**: Workspace-centric domain refactor (worktree path = identity、`git checkout` で ID 不変)
- **S43cfb1 / S4b9df4 / S13b16a / Saa8506**: Claude タブ・FE のリファクタリング + lint sweep + E2E hygiene
- **S1d2278 → S7ce250 → S0fd64b → S1f75ec → Sadf90e (Track B)**: PTY daemon + headless emulator による claude-tui タブ、mode のタブ単位設定化
- **claude-tui 通知の hook 化**: claude-tui タブの Activity Inbox 通知は、ターミナル画面のスクレイプ (BEL / permission 正規表現) をやめ、**Claude Code の公式 hook** で受ける。daemon spawn 時に `claude --settings '<json>'` で `Notification`/`Stop`/`UserPromptSubmit` hook を **そのプロセスにだけ** 注入 (ユーザの `~/.claude` も repo の `.claude/` も触らない)。識別子と callback は `PALMUX_NOTIFY_URL`/`PALMUX_TOKEN`/`PALMUX_REPO_ID`/`PALMUX_BRANCH_ID`/`PALMUX_TAB_ID` の env で渡し、hook コマンドは palmux 自身の `palmux hook` サブコマンド (`cmd/palmux/hook.go`) が stdin JSON + env を読んで `POST /api/notify` する。`Stop`=「あなたの番」/ `Notification`=許可待ち / `UserPromptSubmit`=自動クリア。安定 RequestID (`claudetui-hook-<tabId>`) で Inbox は 1 タブ 1 エントリに集約。設定生成は `internal/tab/claudetui/hooks.go`。
- **S67cb0e**: Sprint タブ UX polish (JIRA 風 table timeline + pushState 履歴 + Prev/Next/dropdown + Markdown レンダリング + 既定 Sprint 解決)
- **S0c6a1b**: リポジトリ非依存「Host」ターミナル。install 直後の `gh auth login` / `claude` ログイン用に、 リポジトリを Open せずに使える bash 専用ターミナル scope。予約 synthetic Repository (`repoId=host--0000` / `branchId=host`) を store に注入して既存タブ機構を再利用。tmux セッションは初回 attach 時に lazy 生成。`GET /api/host` で descriptor 公開、 Drawer 専用セクション + empty-state CTA から到達。 repos.json / `/api/repos` / repo-picker / sync_worktree / Orphans から除外 (`store.IsHostRepoID` ガード)

- **S8478ca [Phase 5, MILESTONE]**: Workspace Runtime — `host` / `incus-container` の 2 種を `runtime.Runtime` interface で抽象化し、tmux 直叩きを runtime 経由に付け替え。`incus-container` は **Incus**（LXD ではなく、distro 非依存・Debian13 既定の community fork）コンテナで Workspace を隔離（npm-g/pip/apt・プロセス・ポートの host 汚染を防止）。**unprivileged** + `raw.idmap "both 1000 1000"`、`~/ghq`+`~/.claude`+`~/.claude.json`+`~/.local/share/claude`+`~/.local/bin` を bind-mount（リポジトリ/認証/skill/memory/claude バイナリを共有 → 再認証なし・claude はイメージに焼かずホストの現バージョンを使用）。ポートは managed bridge + Caddy 直結（ホストポート確保なし）、localhost-only は in-container Python relay で救済、portman は host runtime 専用に残置。Header の runtime chip クリックで **host↔incus を in-place 切替**（新 runtime 起動成功を確認してから旧を破棄するトランザクショナル実装、失敗時はロールバック）。`palmux-ws` イメージは GitHub Release asset として配布し `palmux runtime install` で導入、`scripts/install.sh` が incus セットアップを一括自動化。設計は `docs/workspace-runtime-design.md`。**ホスト前提**: incus 導入 + ユーザが incus-admin グループ + `/etc/subuid`/`/etc/subgid` に `root:1000:1` + (Docker あれば) FORWARD 許可（`palmux runtime doctor` が検証）。Browser タブは S62374c で実装済（noVNC、下記）。

- **See8bd4 [Phase 5]**: incus コンテナポートの公開サブドメイン化。incus-container Workspace 内で listen している dev サーバを、ホスト Caddy 経由で `<port>--<workspace>--<repo>.<base>` の **HTTPS サブドメイン**として公開できる（portman が host runtime に対してやっていることの incus 版）。route 注入は **palmux → Caddy admin API (localhost:2019) 直叩き**（portman 非依存）で、安定 `@id` (`palmux-<inst>-<port>`) を `PUT .../routes/0` で先頭挿入（静的 `*.<base>` 502 catch-all より前に評価させる）。basic_auth は route ごとに注入し、`Public=true` の port だけ無認証で除外。admin-API route は Caddy reload で消えるため、port scan ループ（10s）が `resyncExposedRoutes` で公開中 route を毎回再注入し self-heal。host Caddy 側は `*.<base>` wildcard 証明書（Cloudflare DNS-01）+ admin API を持つ（`nix/modules/system-manager-caddy.nix` / install.sh）。palmux は公開ドメインを `--public-domain`、basic auth を env `BASIC_AUTH_USER`/`BASIC_AUTH_HASH`（install.sh が `/etc/palmux/runtime.env` に書く）で受け取る。UI は runtime=incus のときだけ出る Conditional **「Ports」タブ**（`internal/tab/ports/` + `frontend/src/tabs/ports/`）で、listening ports 一覧・公開トグル・公開 URL コピー・auth/public badge を提供。host runtime では portman 管轄の旨を案内。サブドメイン label は path 由来 hash を保持し衝突回避。実機検証は `palmux-deploy-test.tjstkm.net` で実施済み。

- **Sbe4eee [Phase 5]**: palmux forward_auth SSO。Caddy basic_auth をやめ、**palmux 自身を SSO authority** にして apex(palmux本体) と See8bd4 の公開サブドメインを単一ログインで認証する。Caddy は `forward_auth → palmux GET /auth/verify` で認証を委譲（apex は静的 Caddyfile、per-port は admin API 注入の `subroute` で `/auth/verify`→2xx で backend へ、非2xx で 302 login）。`Public=true` の port は素通し。palmux の `/auth/login`(サーバレンダ HTML・SPAではない)・`/auth/verify`・`/auth/logout` は auth middleware の外に登録（`--public-domain` 設定時のみ）。ログイン成功で `Domain=.<base>` の **HMAC 署名 Cookie** (`palmux_sso`、HttpOnly/Secure/SameSite=Lax) を発行 → 1回ログインで全サブドメイン。パスワードは既存 `BASIC_AUTH_HASH`(bcrypt) を流用、**署名鍵は stable な `PALMUX_SSO_SECRET`**(install.sh が乱数生成・runtime.env に永続化、再起動でログアウトしない)。remember me ✓ → 365日永続 Cookie（同一端末は実質ずっと再認証不要）/ ✗ → セッション Cookie。`internal/auth/sso.go`(+`login_html.go`)。多層防御: login レート制限(10/min, loopback=global)・logout の same-site Origin/Referer チェック・open-redirect ガード(`safeRD`, base ドメイン限定)。local dev(`--public-domain` 未設定)は既存 `--token`/open 認証のまま無変更。実機(実ブラウザ単一ログイン SSO)検証は `palmux-deploy-test.tjstkm.net` で実施済み。

- **S62374c [Phase 5]**: incus-container Workspace 用 Browser タブ + Claude 向け `palmux-browser` CLI + Skill。各 Workspace コンテナ内の **共有 chromium** をユーザ（ライブ操作）と Claude（CDP 自動化）で同時に使う。Story 3: `palmux-browser` CLI（Node.js、`playwright-core connectOverCDP`）+ Skill（`/usr/local/share/palmux/.claude/skills/palmux-browser/SKILL.md`）。CLI サブコマンド: `status`/`start`/`stop`/`navigate`/`click`/`type`/`snapshot`/`screenshot`。REST lifecycle は PALMUX_NOTIFY_URL から自動導出、CDP は PALMUX_CDP_URL または `hostname -i` から。`palmux-browser start` は Activity Inbox に通知を投稿。Skill は `--add-dir /usr/local/share/palmux` で claude-tui に自動 inject。Node.js LTS + playwright-core は image に同梱（`connectOverCDP` のみ）。

  - **noVNC rework（自前 CDP screencast を全廃）**: 当初の自前 CDP screencast + `Input.dispatch` proxy は入力（IME / 座標 / フォーカス）が脆く、実環境で日本語も英字も入らなかったため、**remote-desktop の枯れた標準である noVNC** に載せ替えた。コンテナ内で **Xvfb(:99) 上に headful chromium をフル UI で起動**し、**x11vnc**（`-rfbport 5900`、`-listen` 省略で全 IF bind）が RFB を配信。**palmux は client WS ↔ x11vnc TCP(bridgeIP:5900) を raw バイナリで素通しする dumb byte-pipe**（`AttachVNC`、framing/parse なし、`/attach` は subprotocol `binary`）。フロントは **`@novnc/novnc` の RFB** が canvas に描画し、マウス/キーボード/IME を全て担当（`frontend/src/tabs/browser/browser-view.tsx`）。**ユーザは実 chromium 自身の UI（URL バー/タブ）を直接操作**するので、自前 URL バー/nav/screencast/input/`POST .../navigate` は全廃。描画は実フレームバッファで鮮明。日本語入力は **サーバ側 IME（fcitx5-mozc）** で確実化。起動順は **Xvfb → session dbus-daemon → fcitx5 profile 書込 → fcitx5 → chromium → x11vnc → CDP relay**（各 daemon は `sh -c "nohup CMD & echo $!"` で incus exec 終了後も生存）。**要点（実機で判明した真因）**: ① Chrome(GTK) は fcitx5 へ **session DBus 経由**で接続するため fcitx5+chromium が同一 `DBUS_SESSION_BUS_ADDRESS` を共有する必要がある、② **`fcitx5-frontend-gtk3/gtk4`/`-qt5`** が無いと `GTK_IM_MODULE=fcitx` が解決されず日本語が無言で効かない、③ fcitx5 の初期 profile に mozc が無いので mozc を default group に入れ Ctrl+Space を trigger にする profile を起動前に書く（`ensureFcitx5Config` + image 同梱）。CDP（`--remote-debugging-port=9222` + relay）は Claude/Story-3 用に維持。image 追加パッケージ: `xvfb x11vnc fcitx5 fcitx5-mozc fcitx5-frontend-gtk3/gtk4/qt5 dbus-x11 x11-utils`（`images/workspace-default/build.sh`）。**新コンテナで効かせるには palmux-ws image の再ビルド + re-alias が必要**（既存コンテナは drift、S7364e3 で更新 UX を提供予定）。実機検証は `palmux-deploy-test.tjstkm.net` の incus-container で、実ブラウザ Playwright が SSO ログイン→noVNC canvas に Ctrl+Space→`nihongo`+変換 で textarea に「日本語」入力成立を確認（PASS、`docs/sprint-logs/novnc-rework/decisions.json`）。

- **S5818e8 [Phase 5]**: incus コンテナの dev 環境共有。incus-container のコンテナ内シェルを、palmux が動くホストのシェルに揃える（素の Ubuntu bash → ホスト相当）。`~/.claude` 共有と同じ思想で **dev 環境ごと bind-mount**: incus runtime (`incus.go` の mounts[]) が host の `~/.bashrc` `~/.profile` `~/.bash_profile` `~/.bashrc.d` + GitHub 認証 `~/.gitconfig` `~/.config/gh` `~/.ssh` を同パスで追加マウント（`~/.config` 丸ごとではなく gh だけ）。**`$HOME` 外を指す symlink dotfile (Nix/home-manager → /nix/store) は skip**（broken link でログインシェルが壊れるため、Nix ホストは image 既定シェルに fallback。real-dotfile ホストはリッチシェル）。シェルUXツール（starship/eza/ripgrep/zoxide/fzf/git-delta/yazi）+ **gh** は host では /usr/local・/usr/bin にあり bind-mount で届かないため **palmux-ws image に同梱**（`images/workspace-default/build.sh`）。さらに image に **リッチ既定シェル**（`~/.bashrc` が starship/zoxide init + eza alias、`~/.bashrc.d/00-palmux-shell.bash`）を焼くので、**ホストの dotfiles に依存せず どのホストでもコンテナは即リッチ**（Nix ホストで host dotfiles が skip されても image 既定が出る。real-dotfile ホストは host の `~/.bashrc` mount が上書き）。これで「ホストで開けば starship + gh で push まで動くリッチシェル、別ホストならそのホスト相当」とコンテナがホストに自動追従。トレードオフ: SSH 秘密鍵 + gh トークンがコンテナに露出（フル機能・再認証なし、`~/.claude` 共有と同前提・ユーザ承認）。実機検証は `palmux-deploy-test.tjstkm.net`（各 link: mount 機構 / image tools+gh / starship capability / Nix-host clean shell を個別に PASS）。

- **S7364e3 [Phase 5]**: Workspace コンテナのイメージ更新（drift 検出 + ワンクリック再生成）。palmux-ws image を再ビルド + re-alias しても既存 incus-container は作成時の image fingerprint に固定されたまま（新ツールが入らない）課題を、「drift 検出 → ユーザ起点のワンクリック再生成」で解決。**drift 判定**: incus 専用 optional capability `runtime.ImageDriftChecker`（host は image 概念なし → 実装せず store が type-assert）。コンテナの `volatile.base_image` を現行 `palmux-ws` alias の fingerprint（`incus image list <alias> -f json`、**exact alias 名一致のみ**信頼）と比較し stale 判定。alias 不在/host では false。既存 10s `scanPorts` ループに相乗りし変化時のみ `branch.runtimeDrift` を publish、結果は store の drift cache に持ち RuntimeViewFor が **incus のときだけ** `RuntimeView.stale` に載せる（host で badge 誤表示しない、close/runtime切替で cache clear）。**再生成**: `runtime.ContainerRegenerator`（incus の `Regenerate`）が **throwaway ephemeral probe で新 image の起動可能性を検証してから**旧コンテナを破棄→再作成（probe 失敗時は旧コンテナ維持＝トランザクショナル）。コンテナ rootfs は使い捨て（全 state は bind-mount）なので claude は `--resume` で復帰。store `RegenerateBranchContainer` が per-branch in-flight guard + session 再作成 + events を統括、`POST /api/repos/{repoId}/branches/{branchId}/runtime/regenerate`。**自動再生成はしない**（走行中の claude/tmux を勝手に落とさない）。UI は runtime chip + Drawer エントリの **「⬆ update」バッジ**（stale な incus のみ）+ chip メニューの「Update container」→ 確認モーダル（session 再起動を警告）→ 進行（updating→ready）/ 失敗時インラインエラー（旧コンテナ維持）。`internal/runtime/incus/incus.go`・`internal/store/{branch,store,sync_worktree}.go`・`internal/server/handler_runtime.go`・`frontend/src/components/{header,drawer,update-container-confirm}.tsx`。実機検証は `palmux-deploy-test.tjstkm.net`（real-incus backend acceptance ALL PASS、実ブラウザ SSO で badge→更新→ready を確認）。副次: `incusBridgeListenAddr` が wildcard `--addr`（0.0.0.0/::）で `""` を返すよう修正（bridge listener が wildcard bind と衝突する bug、prod は `127.0.0.1` 指定で従来通り）。

- **S4d8b1c [Phase 5]**: Claude をコンテナ内で動かす (in-container claude)。incus-container Workspace で `claude` CLI 本体をホストではなく**コンテナ内**で実行する。背景: 従来 Claude タブ (agent/tui どちらも) は palmux ホストから `exec.CommandContext` 直叩きで、コンテナを通るのはユーザの Bash タブだけ → **Claude の Bash/npm/pip/ファイル操作が host を汚染**し incus 隔離が半分破れていた。加えて `palmux-browser` skill/CLI はコンテナ image にしか無く host claude から不達 (S62374c の積み残し、`--add-dir` は skill 未登録=`--plugin-dir` が正)。両方を in-container 化で同時解決。**仕組み**: runtime に optional capability `PTYCommander` (TUI 用、`incus exec -t … -- claude` を host pty で包む、bash タブの AttachByIndex と同型) と `ExecCommander` (agent 用、`incus exec` **-t 無し**で plain pipe、stream-json の binary-clean stdio + 分離 stderr を保持) を追加。**claude-tui daemon** は spawn する PTY プロセスを host claude→`incus exec -t -- /home/ubuntu/.local/bin/claude` に差替 (ring/emulator/respawn 無改造)。**claudeagent client** は incus 時 `ExecCommand` で cmd build (StdinPipe/StdoutPipe/StderrPipe + stream-json pump + in-process MCP 権限 server + respawn は無改造)。skill は `--add-dir`→`--plugin-dir` に変更し image を plugin レイアウト化 (`.claude-plugin/plugin.json` + `skills/`)。hook は稼働中 palmux 静的バイナリを `/usr/local/bin/palmux` に bind-mount (`ensureHookBinMount` で既存コンテナにも idempotent hot-plug) + `PALMUX_NOTIFY_URL` を bridge gateway URL で注入。認証は既存 `~/.claude` マウントで再認証不要、worktree は `--cwd` で同一パス。実機検証 (`palmux-deploy-test.tjstkm.net`): TUI/agent 両モードで claude がコンテナ内起動・skill `palmux:palmux-browser` ロード・**隔離 (claude の Bash の hostname=コンテナ)**・stream-json/MCP を確認 (`tests/acceptance/s4d8b1c_{,agent_}incontainer.py` ALL PASS)。spike で stream-json over incus-exec(非-t) の binary-clean 性を事前確認。残課題 (backlog): in-container claude の確実な reap、hook binary mount の inode staleness、wildcard `--addr` での bridge notify。設計は `docs/claude-in-container-design.md`。

- **S18d013 [Phase 5]**: portman 連携の削除。incus 化で冗長化した palmux↔portman の連携を整理。**削除**= palmux 内 read-only 連携 (`internal/portman` パッケージ・`GET .../portman` エンドポイント・`--portman-url` フラグ + `HealthDetail.portmanURL`・ヘッダの portman dashboard リンク・workspace-actions の lease popover) + install.sh の `PORTMAN_ROUTING=1` model-B 経路 (`/etc/caddy/caddy.json` + portman-serve/sync/gc systemd units + `/etc/portman` config + 残存 edge basic_auth)。**維持**= `make dev`/`make serve` の portman ポート割当・portman バイナリ install・host runtime・既定 Caddyfile 経路 (apex forward_auth SSO + `*.<domain>` wildcard、PORTMAN_ROUTING 削除後はこれが唯一の Caddy 経路)。

- **Sa53137 [Phase 5]**: 統一設定プレーン (master config + GUI + apply)。palmux のサーバ/インフラ設定を単一マスターに集約。`~/.config/palmux/config.toml` (`[server]`/`[public]`) + user 所有 `~/.config/palmux/secrets.env` (0600、`PALMUX_SSO_SECRET`/`BASIC_AUTH_HASH`/`CLOUDFLARE_API_TOKEN`/token を root 所有 runtime.env から移行)。`cmd/palmux/main.go` の解決チェーンは **`flag > env > file > default`**。新サブコマンド `palmux2 serve` (config 駆動、bare 起動も後方互換)。install.sh は config.toml + secrets.env を生成し systemd ExecStart を `palmux2 serve` 化、Nix module (`nix/modules/home-manager-palmux.nix`) を薄化。設定 GUI: 既存 settings パネルを「アプリ」「デプロイ」2タブに拡張 (`internal/config/settings.go` に fsnotify watch → 直接編集も即時反映、`internal/deploy/` + `handler_deploy.go` で `GET /api/deploy`・`POST /api/deploy/apply`・`/api/deploy/secrets`)。`palmux apply` が config 差分を hot/restart/root に分類 (hot=in-process、restart=`systemctl --user restart palmux2`)。初回オンボーディング ウィザード。特権境界: 公開ドメイン/TLS のみ root が必要 → `sudo palmux reconcile-system` 単一 verb (user 所有 master 読み→hostname 厳格バリデーション→固定テンプレで `/etc/caddy/Caddyfile` 再レンダ→`systemctl reload caddy`、verb 限定 sudoers、注入拒否)。secrets は GUI でマスク・書込のみ。実機検証 (deploy VM smoke ALL PASS: config 駆動 serve・user-restart 反映・reconcile Caddyfile + SSO 無停止・不正 domain 拒否)。設計は `docs/unified-config-design.md`。

- **S4c591a [Phase 5]**: incus ホストポート公開モード。wildcard サブドメイン (See8bd4) を用意できない環境向けフォールバック。公開ドメイン (wildcard DNS) 未設定のときだけ Ports タブが host-port モードになり、incus native の **proxy device** (`listen=tcp:0.0.0.0:<hostPort> connect=tcp:<containerBridgeIP>:<containerPort>`) で `http://<hostIP>:<hostPort>` 公開。ホストポートは空きを自動割当 (競合時 +10000 再割当)、unpublish で device 削除、**portman 非依存**。Caddy/SSO を通らない無認証公開なので UI に常時 `⚠ 無認証` 警告。公開ドメイン設定済みなら従来のサブドメイン公開 (SSO 保護) のみ表示。host runtime 無影響 (`KindIncusContainer` ゲート)。

- **S6ab0ed [Phase 5]**: GUI/CLI セルフアップデート。リポジトリ内 manifest で管理対象 (コア=palmux 本体 + palmux-ws image、周辺ツールは明示分) を宣言し、6h 間隔の `GITHUB_TOKEN`-aware GitHub poll で installed vs latest を比較 → 右上「更新あり」バッジ + 更新パネル (`internal/selfupdate/`)。「すべてまとめて更新」= install.sh 生成の `~/update-palmux2.sh` 経路 (flake 再 pin→home-manager switch、Nix 世代ロールバック) に委譲 + image は S7364e3 regenerate を再利用 → 本体自己再起動 → FE 再接続ハンドシェイク (WS drop→`/health` version polling→再接続→完了トースト)。CLI `palmux update` / `palmux update --check` が同経路を共有。Nix 非管理インストールは手動更新案内。

- **Sa8e7d0 [Phase 5]**: セルフアップデート堅牢化 (更新が自分を殺さない + 完了ガード)。2026-06-20 deploy VM 実障害の修正: S6ab0ed の更新ヘルパー (`~/update-palmux2.sh`) が **palmux2.service の cgroup 内子プロセス**として走り、`home-manager switch` が palmux2.service を停止する瞬間に **systemd の control-group kill で道連れ**になって更新が途中死 (binary は更新されたが image は旧版のまま、palmux2 再起動されず 502 → バッジ消えず再 Update→再障害ループ)。**Story 1 (ライフサイクル分離)**: 更新ロジックを palmux2 から独立した **`palmux-update.service` (systemd user oneshot)** に移す (`nix/modules/home-manager-palmux.nix` で palmux2 の sibling 定義)。GUI/CLI Update は自プロセス実行をやめ `systemctl --user start palmux-update` で**独立 cgroup のユニット**を起動 → switch で palmux2 が停止/再起動されてもユニットは生存し完走 (`internal/selfupdate/service.go`: `RunUpdate`=`--no-block`+ActiveState watcher で in-flight guard 解放、`RunUpdateForeground`=`--wait`+journal follow で CLI 進捗・終了コードに成否)。S6ab0ed の FE 再接続ハンドシェイク (WS drop→`/health`→toast) はトリガ機構非依存なので無改造で機能。unit 不在の旧インストールは従来の detached-helper に graceful fallback、Nix 非管理は従来の手動案内。**Story 2 (完了ガード + バッジ誤点灯防止)**: (a) **image install 完了ガード** — `palmux runtime install` 後に installed image version が latest **より古くない** ことを検証 (`imageIsStrictlyOlder`、`selfupdate.UpdateAvailable` 再利用で **exact 一致は要求しない** ← image は version release と `workspace-image` pre-release の両方に上がり、build.sh の baked 版は `git describe` 形なので exact match は正常 install を誤って失敗させる)。古いまま残れば update を **失敗扱い** (明示エラー + 旧状態維持、install.sh は update 時 `PALMUX_REQUIRE_IMAGE=1` で die)。(b) **取得不能ソースを誤点灯させない** — `ComponentStatus.Fetchable` を追加。GitHub 404 (= release 無し、例 `tjst-t/gwq`) は typed `NoReleasesError` で **un-fetchable だが Degraded にしない** (rate-limit バナー誤表示を防ぐ)、transient (rate-limit/network) のみ Degraded。GUI/CLI は「取得不可」表示で `available` には数えない。(c) **image version 検出安定化** — `ensureImageVersionProperty` が baked `/etc/palmux-ws-version` から incus `version` プロパティを再 stamp、空/不明でもバッジは永続点灯しない (`UpdateAvailable` が空 installed に保守的)。実機検証 (`palmux-deploy-test.tjstkm.net`): **独立 oneshot ユニットが palmux2.service 停止中も完走する survival mechanism を直接確認** (SURVIVAL_PASS ×2)、live `/api/selfupdate` で gwq=取得不可・degraded=false、badge-honesty GUI E2E + reconnect-live E2E PASS。**残: 旧版ホストでの full live old→new GUI Update (実バージョン bump で完走→バッジ消滅) は installed より新しい release が要るため manual-smoke 保留** (priority_rule 0、S6ab0ed MS-1 と同 gating)。

- **Sb14caa [Phase 5, MILESTONE]**: palmuxOS — NixOS アプライアンス化 (Stage 1〜4)。install.sh(Ubuntu+home-manager)とは別経路として、palmux ホストを **NixOS の宣言設定だけ**で立てる。**root flake.nix が正典** (PD-1): `nixosModules.{palmux,appliance,default}` + `overlays.default`(palmux2/caddy-cloudflare/gwq) + `packages.<sys>.appliance-qcow2`(nixos-generators) を公開 (install.sh の `lib.mkPalmuxHost` に無影響・追加のみ)。重複した scaffold `nixos/flake.nix`(`../nix` 参照=cross-flake-root 不純)は廃止。**`nixos/modules/palmux.nix`** が `services.palmux.*`(全値 `mkDefault`=運用者が素代入で上書き可) で palmux2 service + incus runtime + Caddy(SSO) を統括。incus-on-NixOS の関門は: idmap `both 1000 1000` 用の `root:1000:1` sub{u,g}id を activationScript で追記 (NixOS incus module が subUidRanges を mkForce で奪うため)、incusbr0 preseed の **静的** ipv4.address、container DHCP/DNS/internet のための `trustedInterfaces=[incusbr0]`(conntrack invalid drop 回避)・`bridge-nf-call=0`・`ip_forward=1`。Caddy は `caddy-cloudflare`(DNS-01 wildcard) + forward_auth SSO、`AmbientCapabilities=[CAP_NET_BIND_SERVICE]`(mkForce) と `allowedTCPPorts` plain[80 443](list+mkDefault が openssh の[22]に負ける罠)。**Stage 3 (`nixos/modules/appliance.nix`)**: 不変 image + 可変 state 分離 — **disko で 2 パーティション**(`nixos/modules/disko-layout.nix`、ID-31: `bios(1M)+root(16G固定,LABEL=nixos,/)+persist(残り末尾,LABEL=persist,/persist)` をイメージビルド時に焼込、デプロイ者はパーティション操作不要)。`/persist` に全 mutable state(config・home=$HOME=~/ghq+~/.claude・nixos flake・incus/storage)。`qm resize` で **/persist だけ伸びる**(`palmux-grow-persist` oneshot が mount 前に末尾を growpart→autoResize が FS 拡張。boot.growPartition は root しか伸ばさない cloud-image 慣習なので自前 oneshot 必須)、root 16G 固定でフル保護。incus dir storage pool は `/persist/incus/storage`(コンテナ/イメージ増大が root を埋めない)。ビルダは disko(`appliance-qcow2 = sys.config.system.build.diskoImages`、main.raw → `qemu-img convert -c` で ~810MB qcow2)。当初は nixos-generators qcow(単一root, make-disk-image)。 + `palmux-state-init` oneshot(`RequiresMountsFor=/persist` + before/requiredBy palmux2 で tmpfiles レースを排除、state subtree 作成 + on-appliance flake を seed-only で配置) + `stateDir=/persist/palmux/home`/`secretsFile=/persist/palmux/config/secrets.env`(systemd EnvironmentFile と palmux2 --config-dir が同一 secrets.env を読む様に統一)。配布 image は disko 出力 main.raw を **qemu-img convert -c で compressed qcow2** 化 + qemu_test 化/minio stub/docs・locale・nixpkgs-source 削減で **~810MB**(virtual 17G、root 16G 固定のため; 当初の単一partition版は 786M)。channel は `nixpkgs-appliance`(nixos-25.05) input で image・CI・on-appliance flake を統一。**Proxmox 既定の virtio-scsi で起動させるため `boot.initrd.availableKernelModules` に virtio_scsi 等を明示**(IMAGE ビルドは hardware-base.nix=qemu-guest を含まず、無いと stage-1 が by-label/nixos を見つけられず panic、virtio-blk のみ偶発的に起動の罠)。配布 image は **SSH 鍵ゼロ** (`cloud-init.enable`+`PasswordAuthentication=false` 既定、authorizedKeys 不設定)、不変条件は eval assertion でなく `scripts/check-no-baked-keys.sh`(built toplevel の authorized_keys を grep)で担保 (双方向実機 PASS)。tmpfiles group bug: palmux primary group は `users`(isNormalUser default)で `palmux` 名 group は無い → `config.users.users.<u>.group` 参照に修正。**Stage 4**: 運用者 drop-in 拡張 = on-appliance flake の `imports = [appliance] ++ listFilesRecursive ./local` で `/persist/palmux/nixos-local/*.nix` が mkDefault を上書き + 自分の鍵を layer (image 入替を跨いで残存・flake-pure)。更新は `nixos-rebuild switch`(世代切替=アトミック + `--rollback`/旧世代 boot で**無償ロールバック**)で install.sh self-update の cgroup 道連れ/半端更新/自己上書きが**構造的に消える**。GUI Update バッジは NixOS では nixos-rebuild guidance に写像 (`selfupdate.detectNixOSHost`=/etc/NIXOS|os-release ID=nixos → `Snapshot.NixOSHost`、`update-panel.tsx` が最優先分岐で在app one-click 抑止=S7364e3 の operator-driven 方針)。**実機検証 (testbox 192.168.1.44, nixos-anywhere で controller 192.168.1.43 から導入)**: palmux2+SSO+incus workspace 起動/隔離 (Stage1/2 ALL PASS)、appliance mode で state が generation swap(gen2↔gen3) + `--rollback` を生存・palmux2 通し healthy、drop-in hello↔cowsay 適用/revert、`/api/selfupdate` が実 NixOS で `nixOSHost:true`。**AC-3-1 (bootable qcow2)**: `nix build .#appliance-qcow2`(disko, ~810MB qcow2)を生成。**実 Proxmox (pve-01, VM 9001) で end-to-end 検証 PASS**: `qm importdisk` → 既定の **virtio-scsi**(`scsi0`/`scsihw=virtio-scsi-pci`) + `qm resize +25G` + cloud-init(ciuser/sshkeys/static IP) + `cpu=host`(既定 kvm64 は 25.05 kernel に古く RIP spin → host 必須)で起動 → `/dev/sda` 認識・by-label/nixos を root mount・**root 16G 固定**・`palmux-grow-persist`+autoResize で **/persist だけ 1G→26G 拡張**・home/incus が persist・palmux2 active(`192.168.1.45:7683` health 200)・incus active・`/persist{config,home,nixos,secrets.env}` 正常・鍵ゼロ image に cloud-init 鍵注入で SSH ログイン可。当初 virtio-scsi は initrd に virtio_scsi 不在で stage-1 panic、virtio-blk のみ起動 → `boot.initrd.availableKernelModules` 追加(commit 76a8ea0)で両バス起動に修正。**Stage 5 (dev VM 本番採用・blue-green) は user GO 待ち**。設計は `docs/nixos-appliance-design.md`。

- **Sd44947 [Phase 6, MILESTONE]**: 共有フォルダの宣言化 (profile-as-mold) + 実行時反映。incus-container の共有 device 群 (ghq/.claude/dotfiles/gh/ssh/hook-bin) を per-container device add から、**単一の host-wide incus profile `palmux-shared` へ集約**。コンテナは `default + palmux-shared` の 2 profile で launch し、個別 device add を廃止。profile = 金型 (mold) で、既存 10s scan ループに相乗りした `ReconcileShared` が宣言と稼働状態のドリフトを毎 tick 自己修復 (profile から device を手で消しても次 scan で復活)。旧 per-container device コンテナは `applySharedProfile` (profile add + legacy device remove) で**無停止移行**。**ユーザ拡張**: `config.toml [workspace] shared_dirs = ["~/.infisical", ...]` + 設定 GUI「デプロイ」タブの新「共有フォルダ」セクション (一覧/追加/削除/⚠ 全コンテナ露出警告) から、`~/.claude` 相当の共有を palmux のコード変更なしに追加でき、`/api/deploy/apply` が `class=workspace` として **profile を書換 + 稼働中コンテナへ device add/remove を live 伝播** (palmux2 再起動なし)。パスは client/server 両方で **`$HOME` スコープ検証** (範囲外 400)、source 不在は skip。実装は `internal/runtime/incus/shared_profile.go`・`incus.go`・`internal/config/shared_dirs.go`・`internal/deploy/controller.go`・`internal/server/handler_deploy.go`・`internal/store/sync_worktree.go`・`frontend/src/components/settings-panel.tsx`。GET/POST `/api/deploy` に `workspace.sharedDirs` を追加。reconcile guard は「instance が管理する ready な incus-container が ≥1」で発火 (pure-host デプロイでは `incus` を spawn しない)。**実機検証は 2 独立実 incus ホストで real-mode smoke PASS**: dev ボックス (新バイナリ + 使い捨て probe) と deploy-test 192.168.1.43 (incus 6.0.0、隔離 throwaway instance、11/11 PASS、既存コンテナ/managed service 非干渉)。Nix 管理ホストの `/nix/store` symlink dotfile は mold から skip。設計は `docs/palmux2-nixos-appliance-design.md` §5–§8。

- **S3f2658 + S862203 [no-halt-agent, MILESTONE]**: palmux2 の再起動 (self-update / `systemctl restart` / クラッシュ) を跨いで claude プロセスを生かす。従来は palmux2 が claude を `exec` 直叩きで所有していたため再起動で実行中の agent 作業ごと死んでいた。これを **detached `palmux ptyhost` プロセス**が claude を保持し、palmux2 は unix socket で再接続する構造に変えた。設計は `docs/no-halt-agent-design.md`、ADR は `docs/DESIGN/adr/ADR-0001〜0004`。
  - **`internal/ptyhost/` (新規、ADR-0002 thin holder)**: claude/incus/tmux を一切知らない**汎用プロセスホルダ**。opaque な argv/env/cwd を PTY 付き (`pty` モード) または PTY なし stdio (`pipe` モード、stream-json 用) で spawn し、絶対 offset 付き ring + framed socket protocol (`HELLO`/`ATTACH`/`DATA`/`INPUT`/`RESIZE`/`ACK`/`STATUS`/`SHUTDOWN` + pipe 用 `MsgStderrData`) を提供。永続化 (last-ack offset) はしない (palmux2 側の責務)。
  - **cgroup 脱出 (ADR-0003)**: `systemd-run --user --scope --collect` を試行し、失敗 (非 systemd / D-Bus 不在) を**実行時検出**して `setsid` + double-fork に fallback。scope/unit 名に instancePrefix を埋め、INSTANCE=dev とホストが互いを claim/GC しない。
  - **S3f2658 (Sprint 1)**: claude-**tui** の `claudetui.Daemon` を ptyhost の thin socket client 化 (Emulator/roleCoordinator/hooks/gateRespawn/`--resume`/argv 組み立ては palmux2 側に残置、respawn = 新 ptyhost spawn)。再接続時は ring replay → Emulator feed → SIGWINCH ジグルで画面復元、protocol version 不一致は殺さず degrade。起動時 discovery で生きた ptyhost を adopt (attach、respawn しない)、既存 10s scan ループ相乗りの orphan GC (未参照 ptyhost に SHUTDOWN、参照中は不可侵)。incus workspace ではコンテナ内 claude が同一 pid で生存、SHUTDOWN で in-container reap。**実機検証: 実 systemd restart+kill-9 SURVIVAL_PASS、実 incus 7.1 で生存+reap PASS**。
  - **S862203 (Sprint 2)**: claude-**agent** の `claudeagent.Client` を pipe モード ptyhost の thin client 化。stream-json stdout を**絶対 offset 付き行 ring** に貯め、palmux2 側 `OffsetStore` が処理済み offset を永続化 → 再接続で last-ack 以降を replay しロスレス復元 (kill-9 でも二重処理/欠落なし)。ring 溢れは「復元不能」を明示し新規セッション扱い (黙って欠けた transcript を出さない)。**再起動中に来た permission 要求も replay で UI に届き応答可能** (冒頭 spike で実 claude が遅延 permission 応答を ≥60s 受理することを確認、ADR-0004 gate クリア)。実行中ターンの transcript は claude の `.jsonl` (真実の所在) から backfill + replay の合わせ技で無欠損復元。parse/MCP 権限サーバ/permstate/transcript は無改造。**実機検証: 実 claude で再起動跨ぎの transcript 無欠損 + permission 応答成立 E2E PASS**。副次で既存バグ 2 件も修正 (agent タブがそもそもどの再起動でも resume できていなかった `SetLastInit` の migration guard 潰し、起動 shutdown が `DetachAll` でなく全 kill だった件)。

Phase 5+ は需要が明確になってから検討 (`docs/VISION.json` 参照)。リリース履歴・進捗の source of truth は git タグと **`docs/ROADMAP.json`**。

実装の生きた進捗は **`docs/ROADMAP.json`** が source of truth。各 Sprint の決定ログは `docs/sprint-logs/{SprintID}/decisions.json` を参照。

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

### palmuxOS アプライアンス (qcow2) をローカルで評価する (2026-07-15確立)

palmuxOS (Sb14caa) は NixOS アプライアンスなので通常の `make serve` では評価できない。以前は都度外部 VM (testbox/green 等) に手作業でデプロイして確認していたが、**dev 箱自体の Proxmox VM が KVM 対応済み**（CPU type を `host` に変更 + コールドリスタート済み、`kvm-ok` で確認可）になったため、**このホスト上で直接 qcow2 を起動して評価できる**。

**前提**: 作業する Claude 自身は通常このリポジトリの incus コンテナ (Workspace) の中で動いている (`findmnt -no SOURCE /` で incus の rootfs パスが出ればコンテナ内)。コンテナには `/dev/kvm` が渡っていないため、**`ssh <user>@<dev箱のホストIP>` で一度ホスト本体に抜けてから** 作業する必要がある（コンテナ内で `incus`/`qemu` を叩いても、それはホストではなくコンテナのネームスペースで実行される）。

**手順**:
1. リリース済み qcow2 を取得（CI が minor リリースごとに appliance qcow2 を release asset として添付する。ローカル `nix build` は不要）:
   ```
   gh release download vX.Y.0 -R tjst-t/palmux2 -p 'palmuxos-vX.Y.0.qcow2'
   ```
2. ベースイメージは触らず COW オーバーレイを作る: `qemu-img create -f qcow2 -F qcow2 -b palmuxos-vX.Y.0.qcow2 overlay.qcow2`
3. cloud-init NoCloud seed (`user-data`/`meta-data` → `genisoimage -output seed.iso -volid cidata -joliet -rock user-data meta-data`) で自分の SSH 公開鍵を注入する。**`users: - name: ubuntu` ではなく `name: palmux` を使うこと**（アプライアンスの実ユーザー名は `ubuntu` ではなく `palmux`, uid 1000, home は `/home/ubuntu`。`ubuntu` 名で作ると cloud-init が別 uid の無関係なユーザーを新規作成し、SSH 鍵が effective にならない — 2026-07-15 に実際にハマった）
4. 起動: `qemu-system-x86_64 -enable-kvm -cpu host -m 4096 -smp 2 -drive file=overlay.qcow2,if=virtio,format=qcow2 -drive file=seed.iso,if=virtio,format=raw -netdev user,id=net0,hostfwd=tcp::12222-:22,hostfwd=tcp::17683-:7683 -device virtio-net-pci,netdev=net0 -nographic -serial file:serial.log -display none -pidfile qemu.pid`（`hostfwd=tcp::PORT-:PORT` は既定で `0.0.0.0` bind なので、ホストの LAN IP からも外部アクセス可能）
5. 確認: `ssh -p 12222 palmux@<host>` でログイン、`systemctl is-active palmux2 incus` / `curl http://<host>:17683/` で疎通確認
6. 後片付け: qemu プロセスを kill するだけ（overlay なのでベース qcow2 は無傷、次回も使い回せる）

**未リリースの変更 (まだ GitHub Release に qcow2 が無いブランチ) を検証したい場合**: `nix build .#appliance-qcow2` でローカルビルドが要る。**ビルドする箱は dev 箱そのもの(L1) か `deploy-test` (192.168.1.43, L1) のような Proxmox 直下の VM を使うこと — dev 箱の上に建てた incus VM (L2) の中でビルドしてはいけない**。理由 (2026-07-16 に実際にハマった): `nix build .#appliance-qcow2` は内部で nixos-generators/disko が **qemu を起動してディスクイメージをフォーマットする** ため、ビルドを実行する箱自身が「もう1段 nested virtualization できる」必要がある。dev 箱 (L1, Proxmox の上) でビルドすれば内部 qemu は L2 で収まる (動作確認済みの深さ)。だが dev 箱の上に建てた incus VM (L2) の中でビルドすると、内部 qemu は **L3 (ネストのネスト)** になり、AMD SVM はこの深さを実質サポートしない → `/dev/kvm` へのアクセスが `Permission denied` になり TCG (ソフトウェアエミュレーション) に落ちて極端に遅くなる、または実質進まない。`chmod 666 /dev/kvm` のような権限系の対処では直らない (ハードウェア/カーネルの制約なので)。
- **`deploy-test` (192.168.1.43)** が実績のあるビルドホスト。Determinate Nix 導入済み・`/dev/kvm` 既に world-rw・既存 checkout `~/palmux2-build` (Nix store に前回ビルドのキャッシュが効く)。ただし 1 core・ディスク残 13G 程度とリソースは小さいので、他の作業と衝突しないよう配慮すること (共有ホスト)。
- dev 箱自体に Nix を入れて L1 でビルドする選択肢もあるが、常用機への新規ツールインストールになるので実行前にユーザー確認を取ること。
- ビルド後の qcow2 は dev 箱 (192.168.1.40) に転送し (`scp`/`incus file pull` 等)、上記の起動手順で評価する。

**既にインストール済みの「リリース版」qcow2 を、ローカルの未リリース変更に更新して検証したい場合 (S31ad96-2確立)**: 上の手順1〜6でリリース版 qcow2 を起動した後、qcow2 を作り直さずに **稼働中のそのインスタンスに `nixos-rebuild switch` で流し込む**。`nix/packages/palmux2-local.nix` (S31ad96-1、`src = ../..` でこのリポジトリの作業ツリーから Go+frontend をビルドする Nix パッケージ) と、アプライアンスの on-appliance flake (`/persist/palmux/nixos/flake.nix`、`nixos/appliance-flake/flake.nix` が初回起動時にシードされたもの) の `palmux` 入力を `path:` でローカルソースに向け直す組み合わせで実現する:

1. 起動済みインスタンスにローカルのソースツリーを持ち込む (git 履歴は不要、`.gitignore` 越しの余計なファイルも持ち込まない):
   ```
   git archive --format=tar HEAD | gzip > /tmp/palmux2-local-src.tar.gz
   scp -P 12222 /tmp/palmux2-local-src.tar.gz root@<host>:/root/
   ssh -p 12222 root@<host> 'mkdir -p /root/palmux2-local-src && tar xzf /root/palmux2-local-src.tar.gz -C /root/palmux2-local-src'
   ```
   （`root` への SSH は cloud-init の `users:` に `- name: root` を追加鍵登録すれば通る。`palmux` ユーザーは無 sudo/無パスワードなので、この一時検証用 VM 限定で自分の鍵を root にも入れる。実運用アプライアンスの `palmux` ユーザーの権限モデルには影響しない）
2. on-appliance flake の `palmux` 入力を `path:` にし、`nix flake update palmux` で再ロック:
   ```
   ssh -p 12222 root@<host> \
     'sed -i "s#palmux.url = \"github:tjst-t/palmux2\";#palmux.url = \"path:/root/palmux2-local-src\";#" /persist/palmux/nixos/flake.nix'
   ssh -p 12222 root@<host> 'cd /persist/palmux/nixos && nix flake update palmux'
   ```
3. `services.palmux.package` をローカルビルド (`palmux2-local`) に切り替える。**`./local/*.nix` オペレータ drop-in ではなく、flake.nix 自身の `nixosConfigurations.appliance.modules` に直接 1 モジュール追加する** — dropin は `{ pkgs, ... }` しか受け取らず flake 入力 `palmux` を参照できないため、`nixpkgs.overlays = [ palmux.overlays.default ]` 経由の `pkgs.palmux2-local` はこのアプライアンス flake が pin する `nixpkgs` (nixos-25.05 = Go 1.24) で評価されてしまい、`go.mod` の `go >= 1.25.0` 要求を満たせず**ビルド失敗する** (2026-07-16 に実際にハマった)。代わりに **`palmux.packages.${system}.palmux2-local`** を直接参照する — これは `palmux` 入力 (= ローカル path) 自身の `nixpkgs` 入力 (root flake.nix の `nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable"`、Go ≥1.25 を含む) で評価されるため、アプライアンス側の nixpkgs バージョンに左右されない:
   ```nix
   # /persist/palmux/nixos/flake.nix の let に system = "x86_64-linux"; を追加した上で、
   # nixosConfigurations.appliance.modules に1行追加:
   { services.palmux.package = palmux.packages.${system}.palmux2-local; }
   ```
4. 通常の更新コマンドで switch (GUI/CLI の `palmux-rebuild-update.service` (S673a42) 経由でもよいが、検証時は root SSH から直接で十分):
   ```
   ssh -p 12222 root@<host> 'cd /persist/palmux/nixos && nixos-rebuild switch --flake .#appliance'
   ```
   Go のビルド (`palmux2-local-0.0.0-local-go-modules` の fetch 込み) + npm/vite のフロントエンドビルドがその場で走るため、リリース版 qcow2 の初回 switch より数分長くかかる (4GB RAM / 2 vCPU のテスト VM で数分程度、実績あり)。
5. 確認: `palmux2 --version` が release バージョン (例 `v0.14.13`) から **`v0.0.0-local`** に変わっていれば、稼働中インスタンスがローカルソースへ切り替わった証拠 (`nix-env --list-generations -p /nix/var/nix/profiles/system` で世代が増え、`who -b` の起動時刻が switch 前後で不変 = 再起動なしの in-place 更新であることも確認できる)。
6. 元に戻すには `nixos-rebuild switch --rollback`、または `flake.nix` の `palmux.url` を `github:tjst-t/palmux2` に戻して `nix flake update palmux && nixos-rebuild switch --flake .#appliance`。

この手順で実機検証済み (`docs/sprint-logs/S31ad96/verification-S31ad96-2.md`)。`nix/packages/palmux2-local.nix` 自体(S31ad96-1)への変更は不要 — 上記はすべて対象インスタンス上の一時的なファイル操作 (on-appliance flake の編集) であり、リポジトリの `flake.nix`/`nix/packages/` はこの用途のために変更不要 (overlay 経由の公開は上記の理由で機能しないため見送った)。

### palmux2 の Go ソース変更を「実 NixOS アプライアンス」で素早く検証する (バイナリ差し替え、qcow2ビルド不要)

**NixOS モジュール/パッケージング変更を伴わない、純粋な Go (backend/frontend embed) ソース変更**の実機検証には、qcow2 を作り直すより **`palmux-nixos-test.tjstkm.net` (= 192.168.1.44、実 Proxmox VM 上の永続的な NixOS アプライアンステスト機)** へのバイナリ差し替えの方が桁違いに速い。

- このホストの `palmux2.service` は systemd drop-in (`/run/systemd/system/palmux2.service.d/override.conf`) で `ExecStart` が `/var/lib/palmux/palmux2-test serve --addr=127.0.0.1:7683` に上書き済み。
- 手順:
  1. `make build-linux` でこのリポジトリの作業ツリーから `bin/palmux-linux-amd64` をビルド
  2. **既存バイナリをバックアップ** (共有ホストなので上書き前に必須): `ssh root@palmux-nixos-test.tjstkm.net 'cp /var/lib/palmux/palmux2-test /var/lib/palmux/palmux2-test.bak-$(date +%s)'`
  3. `scp bin/palmux-linux-amd64 root@palmux-nixos-test.tjstkm.net:/var/lib/palmux/palmux2-test`
  4. `ssh root@palmux-nixos-test.tjstkm.net systemctl restart palmux2`
  5. `/var/lib/palmux/palmux2-test --version` で新バイナリが起動していることを確認。実 incus (`incus list`) も稼働中なので、コンテナ経由の挙動もここで確認できる
  6. **検証が終わったら必ず元のバイナリに戻す** (手順2でバックアップしたファイルを `cp` で書き戻し、`systemctl restart palmux2`) — 他の用途にも使われる共有の永続ホストなので、自分の検証用ビルドを稼働させたまま放置しない
- **NixOS モジュール変更 (`nixos/modules/*.nix`) の検証にはこの方法は使えない** (バイナリ差し替えだけで、`nixos-rebuild` を経ないため設定変更は反映されない) — その場合は上のqcow2ビルド手順、または `nixos-rebuild switch --flake` を直接このホストに対して実行する方法を使う。
- 用途の使い分け: この手順 = Go ソースだけの素早い実機smoke。qcow2ローカル評価 = NixOSモジュール/パッケージング変更の検証。S31ad96-2のupdateフロー = 「リリース→ローカル更新」という遷移そのものの検証。

### autopilot / sprint auto でサブエージェントに実装を委譲するときのルール

**コンパイル + unit test だけで「完了」とせず、必ず E2E 検証まで行う**。`make serve INSTANCE=dev` で立てた別ポートの独立インスタンスに対して Playwright (headless) で UI / WS / API 経路を叩いて確認する。詳細と「スキップが許される条件」は [docs/DESIGN_PRINCIPLES.json](docs/DESIGN_PRINCIPLES.json) の `forbidden` / 自律実行ルール (S028 で .md → .json に正典化) を参照。

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
│   │   ├── git/         # Git タブ（REST 系、S029 で minimal redesign — status/log/diff/branches/stage/commit/push/pull/fetch のみ）
│   │   └── sprint/      # Sprint Dashboard タブ（S016, S028 — claude-skills sprint runner と連携、ROADMAP.json + sprint-logs/*.json をパース）
│   ├── ghq/             # ghq list
│   ├── gwq/             # gwq add/remove（worktree 操作）
│   ├── worktree/        # git worktree list（読み取り専用）
│   ├── notify/          # Claude Code 通知ハブ
│   ├── ptyhost/         # detached プロセスホルダ（no-halt-agent、ADR-0001/0002）: ring + socket protocol + cgroup 脱出 launcher（claude/incus/tmux 非依存の汎用）
│   ├── commands/        # Makefile/package.json コマンド自動検出
│   ├── auth/            # Cookie + Bearer 認証
│   └── server/          # HTTP ハンドラ + ルーティング（コア部分のみ）
├── frontend/src/
│   ├── components/      # React コンポーネント（Drawer, Header, TabBar 等）
│   ├── tabs/            # タブモジュール（1タブタイプ = 1ディレクトリ）
│   │   ├── terminal-view.tsx  # 共通ターミナルビュー（Claude / Bash 共用）
│   │   ├── claude-agent/ # Claude タブ（stream-json + MCP）
│   │   ├── files/       # Files タブ（index.ts で registerTab）
│   │   ├── git/         # Git タブ（index.ts で registerTab、S029 で minimal redesign）
│   │   └── sprint/      # Sprint Dashboard タブ（S016, S028）
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

- **更新する**: 確定した規約・命名・パッケージ境界・実装ステータス
- **更新しない**: ロジックの詳細（コードを読めば分かる）、一時的な作業状態
- **原典との整合**: 本ルールと `docs/original-specs/` で食い違いが出たら原典を優先するか、原典自体を更新するかを明示する
