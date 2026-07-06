# Loamium 約10秒ごとの自動フルリロード — 調査結果

- 調査日: 2026-07-06
- ホスト: tjst-dev-01
- 対象: https://8203--loamium-7323--loamium-201b.ndev.tjstkm.net/ （および同ホストの他アプリ全般）

## 結論（真犯人）

**palmux2 (0.12.3) のリコンサイル・ループが10秒ごとに Caddy をフルリロードし、全 WebSocket を能動的に叩き落としている。**

事前の見立て「Caddy / ndev トンネルのアイドルWSタイムアウト ≒ 10秒」は**外れ**。
アイドルタイムアウトではなく、プロキシ設定の**能動的な再投入（no-op PUT）→ Caddy 全体リロード**が原因。

## 症状

- Vite HMR用 WebSocket（`/@vite/client`）が10秒周期で切断。
- ブラウザ Console に `server connection lost. Polling for restart...` → 再接続時にフルリロード。
- 同ホスト上の別アプリ（別ポート）でも同時に発生。

## 決定的証拠

Caddy ログ上で `palmux2`（`User-Agent: Go-http-client/1.1`）が**きっちり10.00秒周期**で以下を繰り返す:

```
GET  /config/apps/http/servers                  ← 現状取得
PUT  /config/apps/http/servers/srv0/routes/0    ← ルート0を再投入
→ caddy: "servers shutting down with eternal grace period"  ← HTTPアプリ全体をリロード
```

実測（直近2分・12回）:
- PUT 間隔: `+10.01 / +9.98 / +10.09 / +9.93 / +10.00 …` → **常に10秒**
- PUT ペイロード: **30回すべて Content-Length 623**（＝中身が変わらない無意味な再投入）

`routes/0` の正体 = 問題のアプリのルートそのもの:

```
0  palmux-tjst-t-loamium-201b-loamium-7323-607d3fb4-8203
   → 8203--loamium-7323--loamium-201b.ndev.tjstkm.net
   → reverse_proxy 10.89.121.235:8203   (incus コンテナ内の Vite dev サーバ)
```

palmux2 バイナリ内文字列の裏づけ:
`reconcile: %w` / `reconcile: panic` / `reconcile-system` / `config: patch:` / `caddy: reloaded` / `ReconcileLastActiveBranches` / `ReconcileUserOpenedBranches`。

## 発生メカニズム

Caddy は admin API に `PUT /config/...` を受けるたびに HTTP アプリ全体をリロードする。
このとき `reverse_proxy` が中継中の**既存 WebSocket ストリームはハンドラのコンテキストごと破棄される**
（`eternal grace period` はリスナーのドレイン用で、プロキシ中継中のWSは維持されない）。
→ `/@vite/client` の HMR WS が切れる → Vite が `server connection lost` → 再接続でフルリロード。

リロードは **Caddy 全体で1回**起きるため、公開中の**全アプリのWSが同時に**落ちる
（＝「別アプリでも起きる」の説明）。

## 切り分け（無実の確定）

| 疑い | 判定 | 根拠 |
|---|---|---|
| Caddy `transport http { read/write_timeout }` | 無実 | 設定ファイル・稼働中コンフィグのどこにもタイムアウト無し（デフォルト＝WS無制限） |
| ndev トンネル / frp / cloudflared | 無実 | データ経路に存在せず。Caddy → incus コンテナ `10.89.121.235:8203` へ直結 |
| `vite.config.ts` の `server.hmr` | 無実 | 未設定だが原因ではない（プロキシが能動的に切っている） |
| クライアント経路（Tailscale/Wi-Fi/回線/認証） | 無実 | 事前切り分け済み |

「前は大丈夫だった」= palmux2 のこの無条件リコンサイル挙動が最近のバージョンで入った回帰の可能性が高い（現在 0.12.3）。

## 構成メモ

- Caddy: `/nix/store/.../caddy-cloudflare-custom`、`--config /etc/caddy/Caddyfile`、admin `localhost:2019`。
  - `*.ndev.tjstkm.net` は既定 502、palmux2 が admin API 経由でポート別ルートを動的注入。
  - 各アプリルートは forward_auth (`127.0.0.1:8080` = palmux2, `/auth/verify`) + reverse_proxy (コンテナIP:port)。
- palmux2: `palmux2-0.12.3`、`serve --addr=127.0.0.1:8080`、systemd `palmux2.service`。
  - config: `/home/ubuntu/.config/palmux/config.toml`（`caddy_admin`, `public.domain=ndev.tjstkm.net`）。
  - **リコンサイル間隔を変える設定は config.toml / --help に露出していない。**
- Vite: incus コンテナ `10.89.121.235:8203`（`vite packages/ui --host 0.0.0.0 --port 8203 --strictPort`）。
- loamium repo: `/home/ubuntu/ghq/github.com/tjst-t/loamium`。
  - `packages/ui/vite.config.ts` は `allowedHosts`（`LOAMIUM_UI_ALLOWED_HOSTS`）と `/api` プロキシのみ。`server.hmr` 未設定。

## 対処法（推奨順）

1. **本丸＝palmux2 修正（恒久策）**
   リコンサイルを冪等にする。すでに `GET` で現状取得しているので、**desired == current なら `PUT` をスキップ**（no-op で Caddy をリロードしない）。
   あるいは全体リロードではなく変更のあったルートだけを触る／変更が無ければ何もしない。
   ※ palmux2 は nix ビルド済みバイナリでソースがこのホストに無い。修正は palmux2 リポジトリ側で必要。回帰を入れた変更を特定して直す。

2. **暫定緩和**
   - リコンサイル間隔を延ばす設定は現状ユーザー側で変更不可（未露出）。
   - HMR だけプロキシ迂回（`server.hmr` をコンテナ直アドレス `10.89.121.235:8203` へ）は理屈上可能だが到達性前提で脆く非推奨。

3. **検証方法**
   palmux2 修正後、以下で `PUT .../routes/0` の10秒周期が消える（＝no-opリロード停止）ことを確認。
   ブラウザ Console の `server connection lost` の10秒周期も消える。

   ```bash
   journalctl -u caddy -f | grep -E 'PUT.*routes|shutting down'
   ```
