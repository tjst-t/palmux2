# Unified Configuration — マスター設定ファイル + GUI + apply

> Status: **DESIGN** (未実装)。検討フェーズの成果物。実装はフェーズ分割で進める。
> 関連: [workspace-runtime-design.md](workspace-runtime-design.md), [claude-in-container-design.md](claude-in-container-design.md)

## 目的

palmux の設定を **単一のマスター設定ファイル** に集約し、

1. install 引数からそのファイルを生成する
2. インストール後は **GUI からも同じファイルを編集** できる
3. ファイルを更新して **コマンド (`palmux apply`) を打つと反映** される

を実現する。現状、設定が `settings.json` / `/etc/palmux/runtime.env` / `/etc/palmux/flake.nix` / install.sh 引数 / CLI フラグの **5箇所に分散** し、インフラ系の変更は home-manager switch + 再起動が必要で重い。

## スコープ決定 (ユーザ承認済み)

- **フルスコープ Phase 1-4**: アプリ設定 + インフラ設定 + Caddy/secrets の特権 apply まで。
- **秘密は別ファイルに分離**: 非機密はユーザ所有の master、機密 (SSO 署名鍵・BasicAuth bcrypt ハッシュ) は `root:user 0640` の別ファイル。GUI はマスク表示 + 特権エンドポイント経由でのみ更新。

## 現状マップ (調査結果)

### 系統A — アプリ設定 `settings.json` (既に理想形に近い)
- `~/.config/palmux/settings.json`、`internal/config/settings.go` の `Settings` 構造体。
- `GET/PATCH /api/settings` (`internal/server/handler_settings.go`) で読み書き。
- PATCH は **即時反映・再起動不要** + WS `settings.updated` ブロードキャスト。
- 弱点: ① GUI は `palette.userCommands` 編集 (`frontend/src/components/user-commands-modal.tsx`) のみ。② **ディスク直接編集は再起動まで未反映** (file-watch 無し、起動時1回ロード)。

### 系統B — デプロイ/インフラ設定 (問題)
| ソース | 中身 | 反映 |
|---|---|---|
| `/etc/palmux/runtime.env` | `PALMUX_PUBLIC_DOMAIN` `PALMUX_SSO_SECRET` `BASIC_AUTH_USER/HASH` | systemd `EnvironmentFile` → 再起動 |
| `/etc/palmux/flake.nix` | `domain` `basicAuth.user` → `bindAddr`/`--public-domain` を決定 | `home-manager switch` 必須 |
| install.sh env 引数 | 上記を生成 | install.sh 再実行 |

- `cmd/palmux/main.go` フラグ (`main.go:113-129`): `--addr --config-dir --token --base-path --max-connections --portman-url --tmux-prefix --public-domain --caddy-admin --claude-bin --claude-arg`。
- 優先順: `--public-domain` だけ flag > env (`PALMUX_PUBLIC_DOMAIN`)。`BASIC_AUTH_*`/`PALMUX_SSO_SECRET` は env-only。他は flag-only。
- systemd unit は Nix 生成 (`nix/modules/home-manager-palmux.nix:66-91`)。`ExecStart` は `--addr` と `--public-domain` をベタ書き、`EnvironmentFile=/etc/palmux/runtime.env`。

## パラメータ分類 (反映コスト)

apply の挙動は「何が変わったか」で分岐する。これが設計の核。

| クラス | パラメータ | 反映 | apply 動作 |
|---|---|---|---|
| **hot (即時)** | 系統A 全部 (maxTabs, previewMaxBytes, palette, defaultRuntime, …) | メモリ更新 + WS | 既存の Patch 経路 |
| **hot (provider refresh)** | public_domain のルート default、basic-auth の per-port default、claude_bin/claude_arg | provider を差し替え | route は 10s ループで self-heal 済み。SSO ログインは別 (下記) |
| **restart 必須** | addr/port, base_path, token, tmux_prefix, max_connections, sso_secret | プロセス再起動 | `systemctl --user restart palmux2` (or GUI に「要再起動」を返す) |
| **root + Caddy 必須** | 公開ドメインの TLS 証明書、Caddy edge の forward_auth/basic_auth、ACME/Cloudflare | 特権ヘルパー | Phase 4。palmux ユーザプロセス単独では不可 |

### 越えられない境界 (正直に記載)
- **listener/router** (addr, base_path): `http.Server` を貼り直すため再起動必須。
- **Caddy 結合**: system-level root 権限が要る。ユーザ systemd の palmux + GUI からは触れない → Phase 4 の特権ヘルパーが担当。
- **secrets**: SSO 署名鍵・bcrypt ハッシュ。GUI からは値を出さずマスク、更新は専用経路。

## 特権境界 (Phase 4 詳細)

調査の結果、Sbe4eee の SSO 移行後は **本当に root が要るのは公開ドメイン/TLS の topology 変更だけ** と判明した。

### root 不要 (Sbe4eee 後)
| アクション | 理由 |
|---|---|
| palmux 自身の再起動 | user systemd service (`systemctl --user restart palmux2`) |
| per-port ルート注入/削除 | Caddy admin API `localhost:2019` への HTTP (`internal/runtime/incus/caddy_admin.go:228`) |
| 認証 (login/forward_auth/bcrypt 比較) | apex は `forward_auth → palmux /auth/verify`、Caddy basic_auth 廃止 (`install.sh:869-882`)。bcrypt 比較は palmux Go 内 (`internal/auth/sso.go:89`) |

### root 必要 (稀・topology レベル)
- ベースドメイン変更: root 所有 `/etc/caddy/Caddyfile` に domain がリテラルで焼かれる + system Caddy 再起動。
- Cloudflare DNS token ローテーション: `/etc/caddy/palmux.env` (root:caddy 0640)。
- system Caddy の reload/restart。

### 方針 (2段構え)

**4a. まず特権面を縮める — palmux 自身の設定/秘密を user 所有へ**
現状 `/etc/palmux/runtime.env` (root:user 0640、group 読みのみ) の `PALMUX_SSO_SECRET` / `BASIC_AUTH_HASH` / `PALMUX_PUBLIC_DOMAIN` を **`~/.config/palmux/secrets.env` (user 0600)** に移す。SSO 移行後はこれらを root 所有にする必然性が無く、秘密がメモリに載った user プロセスを乗っ取れた時点で Cookie 偽造可能なので root 所有の防御利得はほぼ無い。
→ 認証パスワード変更・SSO ローテ・addr/base_path/token/ドメイン値・認証 ON/OFF が **全部 `systemctl --user restart palmux2` だけで完結 (sudo ゼロ)**。「インストール後に触りたい設定」の約9割をカバー。

**4b. 残る root アクション (ベースドメイン/TLS/system Caddy) だけを扱う** — まず簡素な方で出す:
- **MVP (新規特権ゼロ)**: ドメイン変更は `install.sh` 再実行に委ねる。稀な操作なので許容。
- **便利版 (allowlist 特権)**: `sudo palmux reconcile-system` という宣言的な単一 verb。user 所有 master を読んで `/etc/caddy/Caddyfile` を固定テンプレから再レンダ + `systemctl reload caddy`。sudoers はこの verb だけ NOPASSWD、任意コマンド/パスは渡さない。Option C (root の systemd `.path`/socket が spool を reconcile) なら sudo すら無くせるが部品増。

### セキュリティの肝 (便利版)
- reconcile は user/Web から書ける config を **信頼しない入力**として扱う。domain は hostname 正規表現で厳格バリデーション、テンプレは Caddy ディレクティブ注入が起きない形に固定。
- sudoers は verb 限定。Caddy は既に admin API で palmux にルート注入を許しているので信頼増分は「root Caddyfile を書く + system Caddy 再起動」分のみ。

### caveat
~~`PORTMAN_ROUTING=1` の代替パス (`/etc/caddy/caddy.json`) は Sbe4eee で未変換で Caddy basic_auth が残る~~ — S18d013 で `PORTMAN_ROUTING` (caddy.json model-B) 経路を install.sh から削除済み。Caddy 経路は default Caddyfile (apex forward_auth SSO + `*.<base>` wildcard) のみとなり、この caveat は解消した。

## 提案アーキテクチャ

### 1. マスターファイル `config.toml`
`~/.config/palmux/config.toml` (本番) / `./tmp/config.toml` (dev)。非機密のみ。

```toml
[server]
addr = "127.0.0.1:8080"
base_path = "/"
max_connections = 0
tmux_prefix = "_palmux_"
caddy_admin = "http://localhost:2019"
claude_bin = "claude"
claude_args = []

[public]
domain = "example.tjstkm.net"      # 空なら公開機能オフ
basic_auth_user = "admin"           # 任意
```

機密は別ファイル `/etc/palmux/secrets.env` (`root:user 0640`):
```
PALMUX_SSO_SECRET=...
BASIC_AUTH_HASH=...   # bcrypt
PALMUX_TOKEN=...      # --token 相当 (任意)
```

### 2. 解決チェーン (`main.go`)
flag 解決の前に master + secrets をロードし、優先順を **`flag > env > config.toml/secrets.env > default`** に拡張。dev のフラグ上書きは温存。`internal/config` に `LoadServerConfig(dir)` を追加。

### 3. install.sh はマスターを生成
runtime.env + flake 属性のバラ撒きをやめ master + secrets.env を書く。systemd `ExecStart` を `palmux2 serve` (config 駆動) に変え、フラグのベタ書きを除去。→ **ドメイン変更ごとの home-manager switch が不要** (最大の改善)。Nix module は「binary + unit + config dir 用意」まで薄くする。

### 4. GUI — 設定パネル拡張
既存設定 UI を「アプリ」「デプロイ」タブに拡張。
- アプリタブ: 系統A 全フィールド (現状 GUI 未対応分を網羅)。
- デプロイタブ: server/public セクション。保存時、restart 必須フィールドには「要再起動 — [Apply]」バッジ。secrets はマスク + 特権エンドポイント。

### 5. `palmux apply` (ファイル編集 → コマンドで反映)
master を再読込 → 差分を分類 (上表) → hot は in-process 適用、restart 必須は `systemctl --user restart`、root+Caddy は特権ヘルパー呼び出し。GUI の [Apply] も同じ経路を叩く。
- ディスク直接編集にも対応するため、`settings.json`/`config.toml` に **fsnotify watch** を足す (既存 `internal/worktreewatch`, `claudetui/sessions.go` のパターン流用) → 直接編集も apply 相当で拾う。

### 6. 副次メリット
deploy VM は Nix 非管理 (手置き binary + systemd drop-in)。config 駆動にすると Nix 環境でも手置き環境でも同じ設定機構になり二重メンテが消える。

## 実装フェーズ

1. **Phase 1 (軽・高価値)**: 系統A の GUI を全フィールドに拡張 + `settings.json` file-watch で直接編集も即反映。既存機構の延長、リスク小。
2. **Phase 2**: master `config.toml` + secrets.env 導入、`main.go` 解決チェーン、`ExecStart` の serve 化、install.sh 生成切替、Nix module 薄化。
3. **Phase 3**: `palmux apply` (差分分類 → hot/restart) + GUI デプロイタブ。
4. **Phase 4**: 特権境界。**4a** palmux 自身の設定/秘密を user 所有へ移し sudo 不要化 (Phase 2 で前倒し推奨)。**4b** 残るベースドメイン/TLS/system Caddy 変更のみ MVP=install.sh 再実行、便利版=allowlist された `palmux reconcile-system`。詳細は「特権境界」節。

各フェーズは E2E (Playwright headless, `make serve INSTANCE=dev`) で AC 検証してから次へ。

## セキュリティ

- master = ユーザ所有 0600。secrets = `root:user 0640` (現 runtime.env と同等)。
- GUI/`/api` は secrets の **値を返さない** (設定有無の bool のみ)。更新は特権エンドポイント経由で write-only。
- 特権ヘルパー (Phase 4) は許可コマンドを allowlist 化 (Caddy reload / route 注入 / hash 計算のみ)。任意コマンド実行にしない。
- token 変更・sso_secret 変更は全セッション無効化を伴う旨を GUI で警告。
