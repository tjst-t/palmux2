# Cloudflare Tunnel 外部共有 設計メモ

> Workspace コンテナ内で動く dev サービスを、**特定の人だけ**に外部公開して「触ってもらう」ための機能設計。
> palmux2 本体は公開しない。private network (NAT 裏 / オンプレ VM) からアウトバウンド接続だけで公開する。

## 1. 目的とスコープ

### やりたいこと

palmux2 で開発中のサービス（incus-container Workspace 内で listen している dev サーバ）を、
**招待した特定の人**に一時的に触ってもらう。開発ホストが private network の中にあっても公開でき、
共有相手は **Cloudflare Access** の認証（メール OTP / Google SSO）を通った人だけに限定する。

### 非目的

- **palmux2 本体（apex / Ports タブ / SSO）の公開ではない。** それは Sbe4eee + See8bd4 で別途実現済み。本機能は完全に独立した公開パス。
- 本番トラフィックの恒久公開ではない。デモ / レビュー用の一時公開。
- 不特定多数への無認証公開ではない（必ず Access ゲートを通す）。

### 既存の publish 経路との関係（3 つ目のモードになる）

incus Workspace の Ports タブには既に 2 つの公開モードがある:

| モード | 仕組み | 認証 | ドメイン要件 |
|---|---|---|---|
| **subdomain** (See8bd4) | host Caddy admin API に route 注入 | palmux SSO (Sbe4eee, forward_auth) | `*.<base>` wildcard 証明書 + inbound 到達性が必要 |
| **host-port** (S4c591a) | incus proxy device で host ポートに出す | なし（生ポート） | なし（LAN 内向け） |
| **★ cloudflare-share (本設計)** | CF Tunnel ingress + DNS + Access を CF API で動的生成 | **Cloudflare Access**（palmux SSO とは無関係） | **inbound 到達性不要**・wildcard 証明書不要 |

本モードの独自価値は **「inbound 開放も public IP も wildcard 証明書も要らず、Access で人を絞れる」** こと。
private network からの共有はこれが唯一の選択肢になる（subdomain モードは inbound 到達性を要求するため private network では使えない）。

## 2. サーベイ根拠（PoC 不要の裏取り）

`clinical-hishow/Mirante-demo` で**実働・検証済み**の同等機能を確認した（`docs/DEMO_DEPLOY.md`、`lama-demo.tjstkm.net` で稼働中）。
本設計はその CF API シーケンスを palmux 構造に移植したもの。実機で確認済みの重要事実:

1. **CF API 4 ステップで完結**（tunnel 作成 → ingress → DNS → Access app+policy）。付録 A に実コマンド。
2. **Universal SSL 制約は実機で踏んでいる** — `*.dev.tjstkm.net`（2 段目 wildcard）は無料証明書が無く TLS handshake failure。
   → **共有ホストは第1レベル `<label>.<zone>`（例 `share-xxx.tjstkm.net`）に限定**する。深い階層は Advanced Certificate Manager（有料）が要る。
3. **API トークンスコープ確定**（実働）: Account『Cloudflare One Connector: cloudflared』Edit / 『Access: Apps and Policies』Edit /
   『Access: Organizations, IdP, and Groups』Edit、Zone『DNS』Edit（zone を Include）。
4. tunnel は **remotely-managed**（`config_src:"cloudflare"`、ingress は CF 側に保存）。`cloudflared tunnel run --token` で常駐。

## 3. アーキテクチャ

```
共有相手ブラウザ
   │  HTTPS  https://share-<label>.<zone>/
   ▼
Cloudflare Edge ── Cloudflare Access (Group ポリシー: 招待メール OTP / Google SSO)
   │  暗号化トンネル (アウトバウンドのみ。VM の inbound 開放不要)
   ▼
開発ホスト: cloudflared (1 本常駐, systemd)
   │  ingress: share-<label>.<zone> → http://<containerIP>:<port>
   ▼
incus bridge ─▶ Workspace コンテナ内 dev サーバ :<port>
                  (localhost-only bind は既存 in-container relay で救済)
```

- **tunnel は palmux ホストに 1 本だけ常駐。** 共有ごとに ingress エントリを挿抜する（プロセス乱立なし）。
- upstream は See8bd4 と同じ `<containerIP>:<port>`（runtime.Status().Address + port）。
- **palmux SSO / Caddy admin route とは一切交わらない**（別 process・別ドメイン・別認証）。

### 1 共有 = 3 つの CF リソース

トグル ON で次の 3 つを生成し、OFF で 3 つとも撤去する:

1. **tunnel ingress エントリ** 1 件（`PUT /cfd_tunnel/{TID}/configurations` を read-modify-write。404 catch-all は末尾維持）
2. **DNS CNAME** 1 件（`share-<label>.<zone>` → `<TID>.cfargotunnel.com`、proxied）
3. **Access Application + Policy** 1 件（hostname に対し、選択 Group を include する allow ポリシー）

これは See8bd4 の「1 port = 1 Caddy route」と対称な構造で、palmux は既に同型の idempotent upsert/delete + resync パターン（`resyncExposedRoutes`）を持っている。

## 4. Group アタッチモデル（a/b 両対応）

ユーザ要件「グループを決めてアタッチ」は **Cloudflare Access Group** にそのまま対応する:

- **Access Group** = 再利用可能な人の集合（`team-A = これらのメール / ドメイン`）。Zero Trust ダッシュボード or `GET /accounts/{ACCT}/access/groups`。
- 共有作成時に **どの Group を許可するか**を palmux UI で選ぶ → Access Policy の `include` に `{"group":{"id":<gid>}}` を並べる。
- **a（全共有で同じ人）**: 共通 Group を毎回選ぶ。
- **b（共有ごとに別の人）**: 共有ごとに別 Group を選ぶ。

v1 では **Group の選択 + アタッチ**のみ palmux が担い、**Group 自体の CRUD は Zero Trust ダッシュボードに委ねる**（パレットを増やしすぎない）。Group CRUD の palmux 内蔵は backlog。

## 5. palmux への実装マッピング

### 5.1 新パッケージ `internal/share/cloudflare/`

host レベルの singleton。incus runtime とは疎結合（runtime registry から container address を引くだけ）。See8bd4 の `caddy_admin.go` を範に取る。

```go
// ShareManager は CF Tunnel/DNS/Access を CF API で管理する host レベル singleton。
type ShareManager struct {
    api      *cfAPIClient // tunnel configurations / dns / access apps+policies+groups
    acct     string       // CLOUDFLARE_ACCOUNT_ID
    zone     string       // 例 "tjstkm.net" (= DNS zone かつ第1レベルの apex)
    zoneID   string       // 起動時に GET /zones?name=<zone> で解決
    tunnelID string       // 既存 or 初回作成して secrets に永続化
    // state: shareKey -> {hostname, dnsRecordID, accessAppID, port, groups}
}

func (m *ShareManager) Share(ctx, repoID, branchID string, port int, groupIDs []string) (url string, err error)
func (m *ShareManager) Unshare(ctx, repoID, branchID string, port int) error
func (m *ShareManager) Groups(ctx) ([]AccessGroup, error)   // UI の picker 用
func (m *ShareManager) Reconcile(ctx)                       // 起動時: intent と CF 実体を突き合わせ orphan 掃除
```

- **shareKey / label**: `share-<repoLabel>-<wsLabel>-<port>`（`dnsLabel()` を再利用、第1レベルに収める。`--` 連結はせず単一ラベル化）。安定キーで idempotent upsert/delete。
- **localhost-only bind 救済**: See8bd4 同様、bind が loopback のみなら先に in-container relay を張る（`ExposePort`）。
- **ingress read-modify-write**: `PUT .../configurations` は全置換なので、現 ingress 配列を GET → 当該 hostname エントリを挿抜 → 404 を末尾に維持して PUT。
- **durability**: remotely-managed なので ingress/DNS/Access は CF 側に永続。Caddy admin route のような 10s resync は不要。起動時 `Reconcile` で「palmux が記憶している共有 ↔ CF 実体」を一度だけ突き合わせ、消し忘れ orphan を掃除する。

### 5.2 オプショナル runtime capability は不要

cloudflare-share は host レベルの publish パスで、incus runtime の内部状態に依存しない（container address は `runtime.Status().Address` で十分）。
よって `runtime.Runtime` に新 capability を生やさず、**store が ShareManager を直接保持**して `ShareWorkspacePort` / `UnshareWorkspacePort` を提供する。
incus でないときは `ErrPortsUnsupported` 相当を返す（Ports タブ自体が incus 限定なので UI からは届かない）。

### 5.3 API / WS

`internal/tab/ports/` を拡張（既存 `expose`/`unexpose` と対称に）:

```
POST   .../ports/{port}/share     body {groupIds:[...]}  → {port, shareUrl, groups}
DELETE .../ports/{port}/share     → 204
GET    .../share/groups           → [{id,name}]          # Access Group picker
```

`runtime.PortView` に共有状態フィールドを追加（`branch.portsChanged` WS にも乗る）:

```go
Shared      bool     `json:"shared"`      // CF share が存在
ShareURL    string   `json:"shareUrl"`    // https://share-<label>.<zone>
ShareGroups []string `json:"shareGroups"` // 許可中の Access Group 名
```

### 5.4 UI（Ports タブ）

各 port 行に subdomain / host-port と並ぶ第3トグル **「外部共有 (Cloudflare)」**:

1. トグル ON → Group マルチセレクト（`GET .../share/groups`）を出す → 確定で `POST .../share`。
2. 生成された `https://share-<label>.<zone>` を表示 + コピー。**Access 保護バッジ** + 許可 Group 名を表示。
3. トグル OFF → `DELETE .../share`（ingress/DNS/Access を撤去 → 即非公開）。

Workspace close 時は store が当該 Workspace の全 share を撤去。

### 5.5 設定 / secrets

master config / secrets.env（install.sh が書く。`firstNonEmpty(env, config)` 解決は既存パターン踏襲）:

| キー | 用途 |
|---|---|
| `PALMUX_CF_API_TOKEN` | CF API トークン（§2 のスコープ）。secrets.env (0600) |
| `PALMUX_CF_ACCOUNT_ID` | Cloudflare Account ID |
| `PALMUX_CF_SHARE_ZONE` | 共有ホストの zone = 第1レベル apex（例 `tjstkm.net`）。未設定で本機能 disable |
| `PALMUX_CF_TUNNEL_ID` / `..._TOKEN` | 共有用 named tunnel。初回作成して永続化 or install で先行作成 |

- 未設定（`PALMUX_CF_SHARE_ZONE` 空）なら Ports タブの外部共有トグルは出ない（subdomain/host-port は従来どおり）。
- **cloudflared 常駐**: install.sh が `cloudflared` 導入 + `cloudflared tunnel run --token` の systemd service を設置。`palmux runtime doctor` がトークンスコープ + cloudflared 稼働 + zone 解決を検証。

## 6. セキュリティ / ガードレール

- **共有は必ず Access ゲート経由**（無認証共有を作らせない）。Public=true 相当の無認証モードは本パスでは提供しない。
- **palmux2 本体は露出しない**: 共有ホストは palmux ホストの **兄弟ホスト名**（`share-*.<zone>`）で、palmux apex のサブドメインではない。tunnel ingress は当該 port にしか向かない。
- **secrets はコンテナに渡さない**: CF API トークンは palmux host process のみ保持。コンテナ内には届かない。
- **撤去は即時**: トグル OFF / Workspace close / tunnel 停止のいずれでも到達不可になる。入室ログは Zero Trust › Logs に残る。
- **越境注意**（Mirante と同じ前提）: CF edge で TLS 終端＝経路が国外を通り得る。機微データを載せる共有では利用者が判断する（palmux はインフラのみ提供）。

## 7. 未決事項（実装前に確定したい）

1. **zone を `tjstkm.net` 共用にするか、共有専用 zone を切るか。** 共用で兄弟ホスト名運用が最小コスト（推奨）。
2. **Access Policy include の正確なフィールド名**（`{"group":{"id":...}}`）を実装初手で CF API に対し 1 回叩いて確認（Mirante は inline email で実証済、group 参照は未実証の唯一の点）。
3. **tunnel を install で先行作成するか、palmux 初回 share 時に lazy 作成するか。** lazy だと install が軽いが、初回 share にレイテンシ + 失敗ハンドリングが要る。

## 8. Sprint Plan（提案）

Phase 5 / milestone 級。`docs/ROADMAP.json` への正式登録は `/sprint` で行う前提のドラフト。

| Story | 内容 | 主要 AC |
|---|---|---|
| **-1 host plumbing** | install.sh で cloudflared 導入 + named tunnel + systemd 常駐、CF API トークンスコープ文書化、`PALMUX_CF_*` 設定解決、`runtime doctor` 検証 | doctor が token/zone/cloudflared を緑判定、tunnel が `--token` 常駐 |
| **-2 share manager (backend)** | `internal/share/cloudflare/` — CF API client（configurations RMW / DNS / Access app+policy / groups list）、`ShareWorkspacePort`/`Unshare`、起動時 Reconcile、localhost-only relay 連携 | コンテナ :N の dev サーバが share 後 `https://share-<label>.<zone>` から到達（302 Access → 認証後 200・証明書 valid）、Unshare で ingress/DNS/Access が消える |
| **-3 API + WS + Group picker** | ports endpoints に share/unshare/groups 追加、`PortView` に Shared/ShareURL/ShareGroups、`branch.portsChanged` に反映 | `POST .../share` が group 付きで共有作成し PortView/WS に共有状態が出る、`GET .../share/groups` が Access Group を返す |
| **-4 UI (Ports タブ)** | 第3トグル「外部共有」+ Group マルチセレクト + URL コピー + Access バッジ + 撤去、未設定時は非表示 | E2E: トグル ON→group 選択→URL 表示→外部到達確認→OFF で非公開（headless Playwright + 実 CF or mock） |
| **-5 実機受け入れ**（任意分離） | ndev / deploy-test VM で実 CF API に対し作成→到達→撤去を通す acceptance | `tests/acceptance/*_cf_share.py` ALL PASS |

依存: S8478ca（incus runtime + container address / ExposePort）、See8bd4（admin-API upsert/resync の知見）。両方 done。

---

## 付録 A: 実証済み CF API シーケンス（Mirante `docs/DEMO_DEPLOY.md` より）

`ACCT`=Account ID, `AUTH`="Authorization: Bearer $CLOUDFLARE_API_TOKEN", `HOST`=`share-<label>.<zone>`, `PORT`=container 内ポート。

```bash
# 1) named tunnel (remotely-managed)
POST /accounts/$ACCT/cfd_tunnel            {"name":"palmux-share","config_src":"cloudflare"}
   → .result.id (TID), .result.token (cloudflared run 用)

# 2) ingress (全置換。複数共有時は配列に積む。404 を末尾維持)
PUT  /accounts/$ACCT/cfd_tunnel/$TID/configurations
   {"config":{"ingress":[
       {"hostname":"$HOST","service":"http://<containerIP>:$PORT"},
       ...他の共有...,
       {"service":"http_status:404"}]}}

# 3) DNS CNAME (proxied)
GET  /zones?name=<zone>                    → .result[0].id (ZID)
POST /zones/$ZID/dns_records               {"type":"CNAME","name":"$HOST","content":"$TID.cfargotunnel.com","proxied":true}

# 4) Access app + policy (Group 参照)
POST /accounts/$ACCT/access/apps           {"name":"...","domain":"$HOST","type":"self_hosted","session_duration":"24h"}
   → .result.id (AID)
POST /accounts/$ACCT/access/apps/$AID/policies
   {"name":"invited","decision":"allow","include":[{"group":{"id":"<access-group-id>"}}]}
   # Mirante は inline {"email":{"email":...}} / {"email_domain":{"domain":...}} で実証。group 参照は本設計で採用。

# 撤去: 上の 2(該当 ingress を除いて PUT) / 3(DELETE dns_records/$RID) / 4(DELETE access/apps/$AID)

# 常駐起動
cloudflared tunnel run --token "$TUNNEL_TOKEN"
```

CF API トークンスコープ（実働確認済み）:
- Account › Cloudflare One Connector: cloudflared : Edit
- Account › Access: Apps and Policies : Edit
- Account › Access: Organizations, Identity Providers, and Groups : Edit
- Zone › DNS : Edit（Zone Resources に対象 zone を Include）

## 付録 B: 参照

- `clinical-hishow/Mirante-demo` `docs/DEMO_DEPLOY.md` — 実働レシピ（sprint S46a1c0）
- `internal/runtime/incus/caddy_admin.go` — 範とする idempotent upsert/delete + resync
- `internal/tab/ports/{provider,handler}.go` — 拡張対象の Ports タブ
- `docs/workspace-runtime-design.md` / `See8bd4` 系 — incus publish の既存設計
