# palmuxOS — NixOS アプライアンス設計

> ステータス: **設計 + scaffold**（2026-06）。`nixos/` ツリーは scaffold 済みだが、
> Nix を持たない設計箱のため **まだ eval 検証していない**。下記 Stage 1〜5 が検証・
> 構築計画。Nix ファイルは「提案する形」であり、staged plan に沿って
> `nix flake check` と実機検証を行う前提で読むこと。

## なぜ NixOS か（決定の振り返り）

palmux ホストは既に Nix 基盤の上にある: `palmux2` は Nix パッケージ
(`nix/packages/palmux2.nix`)、home-manager でデプロイ
(`nix/modules/home-manager-palmux.nix`)、ホストレベルの Caddy は **system-manager**
(`nix/modules/system-manager-caddy.nix`)、残りのホスト設定（swap, sysctl,
unattended-upgrades, subuid/subgid, apt パッケージ）は `scripts/install.sh` の末尾で
命令的に処理している。

ホスト**全体**を宣言的に管理し、palmux を**アプライアンス image** として配布するには、
OS 自体が NixOS である必要がある —— フルの `configuration.nix`（`nixos-rebuild`）と
アプライアンス image（`nixos-generators`）は NixOS 限定。Ubuntu 上の system-manager は
サブセット（systemd unit, `/etc`, Nix パッケージ）しかカバーせず、カーネル/ブート/init や
アプライアンス image は扱えない。

NixOS は palmux ホストモジュールを Ubuntu インストーラより**むしろ簡潔**にする:
- `virtualisation.incus.enable` が手動 incus セットアップ + subuid/subgid 配線を置換
- `services.caddy.virtualHosts` が `system-manager-caddy.nix` を置換
- NixOS systemd service が home-manager user unit + install.sh の末尾命令を置換
- `system.autoUpgrade` / 世代（generations）が unattended-upgrades + self-update ヘルパを置換

## ディレクトリ構成

```
nixos/
├── flake.nix                     # inputs + outputs: nixosModules.{palmux,appliance,default},
│                                 #   nixosConfigurations.palmux-appliance, packages.*.appliance-qcow2
├── modules/
│   ├── palmux.nix                # 再利用可能なホストモジュール: options.services.palmux.* + 配線
│   │                             #   (palmux2 service, incus, caddy/SSO, subuid/subgid) — 全部 mkDefault
│   └── appliance.nix             # アプライアンス固有: 永続 state ボリューム + ユーザ drop-in
│                                 #   import フック + autoUpgrade + 最小ベース
├── examples/
│   ├── onappliance-flake/        # アプライアンスの永続ボリューム上 /etc/palmux に置かれる flake
│   │   ├── flake.nix             #   nixosModules.appliance + ./local/*.nix を import → ここでユーザ拡張
│   │   └── local/example.nix     #   ユーザ drop-in の例（NixOS 全 option が使える）
│   └── user-flake/flake.nix      # ソース合成: ユーザ自身の flake が nixosModules.palmux を import
└── README.md
```

`nix/packages/palmux2.nix`（リリースアセットの derivation）はそのまま再利用。NixOS
モジュールはパッケージを参照するだけ。

## ユーザ拡張の2つの仕組み（中核要件）

palmux アプライアンスのユーザは、palmux を fork せずに**自分の宣言的設定を palmux の上に
足せる**必要がある。NixOS のモジュールシステムがこれを無償で提供する。2つの形で公開する。

palmux 側の設定は**全部 `lib.mkDefault`** なので、ユーザは素の代入で上書きできる
（`mkForce` 不要）。

### ① ソース合成（NixOS 流儀 — flake で管理する人向け）

palmux は `nixosModules.palmux` として配布される。ユーザは自分の flake の中で自分の
モジュールと合成する。モジュールシステムがマージし、ユーザは palmux の `mkDefault` を
素の代入（または `mkForce`）で上書きする。`examples/user-flake/` 参照。

```nix
# ユーザの flake.nix
{
  inputs.palmux.url = "github:tjst-t/palmux2";
  outputs = { nixpkgs, palmux, ... }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      modules = [
        palmux.nixosModules.palmux         # palmux レイヤ
        ./hardware-configuration.nix
        ({ pkgs, ... }: {
          services.palmux.domain = "dev.example.net";   # palmux の option
          environment.systemPackages = [ pkgs.htop ];   # …そして NixOS の全 option surface
          networking.firewall.allowedTCPPorts = [ 1234 ];
        })
      ];
    };
  };
}
```

### ② アプライアンス drop-in（配備済み image の運用者向け — fork も flake 作成も不要）

アプライアンスは**永続 state ボリューム上**に `/etc/palmux/` を小さな flake として同梱する
（`examples/onappliance-flake/`）。その configuration は `nixosModules.appliance` **に加えて
`/etc/palmux/local/` 配下の全 `*.nix`** を import する。運用者は次の手順で拡張する:

```bash
# 宣言的な断片を置く — NixOS の全 option surface が使える
sudo tee /etc/palmux/local/my-extras.nix <<'NIX'
{ pkgs, ... }: {
  environment.systemPackages = [ pkgs.tmux pkgs.ripgrep ];
  services.openssh.settings.X11Forwarding = true;
  # palmux の default を素直に上書き（palmux は mkDefault で設定している）:
  services.palmux.bindAddr = "127.0.0.1:9000";
}
NIX
sudo nixos-rebuild switch --flake /etc/palmux#appliance
```

`/etc/palmux/local/` は **flake のソースツリー内（永続ボリューム上）**にあるので、import は
flake-pure（`--impure` 不要、flake の外から `/etc` を eval 時に読まない）。`/etc/palmux/local/`
は永続データボリュームにあり、image の入替/アップグレードを跨いで残る。これがアプライアンスが
約束する仕組み: *自分の設定を palmux の上に宣言的に重ね、その場で `nixos-rebuild switch`*。

## 不変 image + 永続 state の分離

アプライアンス image は不変。可変 state は全て**別の永続ボリューム**（`/persist` にマウント、
bind-mount で各所へ）に置き、image を再ビルド/差し替え/アップグレードしてもデータが失われない
ようにする:

| state | パス | マウント元 |
|---|---|---|
| リポジトリ | `~/ghq` | `/persist/ghq` |
| Claude 記録（履歴, memory, projects） | `~/.claude`, `~/.claude.json` | `/persist/claude/...` |
| palmux 設定 (config.toml/settings.json) | `--config-dir` | `/persist/palmux/config/app` |
| secrets | `secrets.env` | `/persist/palmux/secrets.env` (0600) |
| 運用者 NixOS オーバーライド | `/etc/palmux/local/` | `/persist/palmux/config/nixos` |
| incus（ワークスペースコンテナは使い捨て、state は bind-mount） | `/var/lib/incus` | ポリシー次第で永続/揮発 |

これは dev 移行で話した `~/.claude` / `~/ghq` 引き継ぎの懸念とそのまま直結する。アプライアンス
化は state/image 分離を**強制する**ので、そのデータが設計上「明示的・可搬・バックアップ対象」に
なり、むしろ綺麗に解決する。

## 運用者コンフィグ束（operator config bundle）— コアと運用者設定の切り分け

**PalmuxOS コア**（＝`nixosModules.{palmux,appliance}` の Nix モジュール、image に焼かれる不変・
バージョン管理対象・運用者は触らない）と、**運用者が設定する部分**（WebUI / 手で設定）を、
`/persist` 上の**切り分けたファイル束**として明確に分離する。将来の「設定だけバックアップ／
GitHub に保存・別ホストへ restore」を一操作にするのが狙い（実装は `nixos/modules/{palmux,appliance}.nix`
で配線済み: `services.palmux.configDir` を追加し、drop-in 注入先と config-dir を束へ向ける）。

```
/persist/palmux/
├─ config/                    ← 運用者コンフィグ束（バックアップ/restore 対象・git-friendly）
│  ├─ nixos/  *.nix           宣言的 drop-in（domain, 追加 pkg 等）。on-appliance flake が注入
│  ├─ app/    config.toml     palmux2 の app/server 設定 + settings.json（--config-dir）
│  └─ manifest.json           束のメタ（版・中身）— restore 用（将来）
├─ secrets.env                ← 秘密（CFトークン / SSO secret / bcrypt）— 別扱い
└─ home/                      ← データ（~/ghq, ~/.claude）— 別扱い・大きい
```

**バックアップ3層**（一緒に GitHub に置かない）:

| 層 | 中身 | GitHub バックアップ |
|---|---|---|
| **`config/`** | drop-in `.nix` + `config.toml` | **そのまま OK**（宣言的・秘密なし）= 設定の git バックアップ/restore の本体 |
| **`secrets.env`** | CFトークン / SSO secret / bcrypt | **平文 NG**。age/sops 暗号化で置くか git 除外して別経路 restore |
| **`home/`** | `~/ghq`, `~/.claude` | データ。別バックアップ（大きい・独自履歴） |

restore＝新アプライアンスに `config/` を戻す → secrets を別途復元 → `nixos-rebuild switch`。
コア（image）は起動した版そのまま。これで「設定だけ持ち運び・git 管理・別ホストへ復元」が成立する。

**未実装（後続スプリント、`docs/ROADMAP.json` に story 化）**: ① 公開ドメイン未設定時に WebUI を
LAN へ出す first-boot bind、② WebUI/CLI のデプロイ設定（domain/CF トークン等）を NixOS では
`config/nixos/*.nix` drop-in + `secrets.env` へ書いて `nixos-rebuild switch` まで自動化（Ubuntu の
`reconcile-system` 経路の NixOS 版＝GUI→nixos-rebuild 写像の config 版）、③ `config/` 束の
backup/GitHub restore 機能（manifest + secrets 暗号化）。

## アクセスと鍵 — 配布物に作者/運用者の鍵を焼かない（セキュリティ要件）

**配布するアプライアンス image / `nixosModules.*` には SSH 鍵・パスワードを一切焼かない。**
作者の公開鍵を image に焼けば、それは**全 PalmuxOS への作者バックドア**になる。配るのは
**コード（モジュール）であって鍵ではない**。作者/個人の鍵は、配布しない自分のホスト config
（`nixos/hosts/testbox/` 等）にだけ存在してよい。

デプロイ**する人**が、初回ブート時に**自分の鍵を注入**する。手段:

1. **cloud-init（主、Proxmox ネイティブ）** — image は鍵ゼロ。Proxmox の cloud-init ドライブで
   デプロイ者の公開鍵を渡す → 初回ブートで注入（`services.cloud-init.enable`）。Ubuntu cloud
   image が鍵を焼かずにログインできるのと同じ。
2. **palmux 初回 onboarding/claim（web）** — 初回ブートを未 claim 状態にし、最初のアクセスで
   運用者がパスワード/SSO secret（＋任意で鍵）を設定するまで何も公開しない（Sa53137 onboarding
   の延長）。cloud-init を使わない人向けの palmux ネイティブ経路。
3. **flake 合成派** — 自分の flake に自分の鍵を書いて自分でビルド（`examples/user-flake/`）。

運用者が自分の鍵を `/etc/palmux/local/*.nix` に足すのは、上記で**最初のアクセスを得た後**。
つまり「**image は鍵ゼロで出荷、鍵はデプロイ単位で初回注入**」。

`modules/appliance.nix` は `services.openssh.enable` + `services.cloud-init.enable` +
`PasswordAuthentication=false` を既定にし、**authorizedKeys は一切設定しない**。
「配布 image に鍵が焼かれていない」不変条件は **eval assertion ではなく image-build の CI
チェック**（ビルドした image の authorized_keys を grep）で担保する（assertion は「出荷 image の
ビルド」と「運用者の rebuild（鍵を足してよい）」を区別できず、正当なカスタマイズを壊すため）。
TODO(stage3): image-build の no-baked-keys CI チェックを追加。

## 更新（Update）— ここが install.sh self-update を根本から置き換える

palmux の v0.11.x は self-update の堅牢化に何度も苦しんだ（Sa8e7d0: 更新ヘルパが
palmux2.service の cgroup 内で走り home-manager switch に道連れで死ぬ → 半端更新ループ /
atomic 書き込み問題 / バッジ誤点灯）。**NixOS の世代モデルはこのクラスの障害を構造的に消す。**

### 何を、どう更新するか

| 対象 | 方法 | 性質 |
|---|---|---|
| **palmux2 本体** | `/etc/palmux/flake.nix` の `palmux` input pin を bump → `nixos-rebuild switch` | アトミック・世代切替・自動ロールバック |
| **OS / 基盤** | `nixpkgs` input を bump → `nixos-rebuild switch` | 同上（同じ単一動詞） |
| **palmux-ws image** | モジュールで版を宣言（`services.palmux.workspaceImage.version`）→ switch 時に oneshot が `palmux runtime install` を実行。または既存の GUI 再生成（S7364e3） | 宣言的 pin。詳細は Stage 2/3 で確定 |
| **運用者の追加設定** | `/etc/palmux/local/*.nix` はそのまま。本体更新で palmux の新 mkDefault を拾いつつ、運用者の上書きは保持 | 更新と拡張が衝突しない |

### なぜ install.sh self-update より強いか

- **アトミック + 無償ロールバック**: `nixos-rebuild switch` は新世代を作って切り替えるだけ。
  失敗時は `nixos-rebuild switch --rollback`、または**ブートメニューから旧世代を選ぶ**で確実に
  戻る。Sa8e7d0 が手作業で防いでいた「更新が自分を殺す / 502 ループ / バッジ消えない」が
  **設計から消える**。
- **cgroup 道連れ問題が消える**: 更新は systemd の世代切替であって、palmux2.service の子
  プロセスとして走るヘルパではない。Sa8e7d0 で導入した独立 `palmux-update.service` の役割を
  NixOS の activation が肩代わりする。
- **自己上書きレースが消える**: install.sh が実行中の `~/update-palmux2.sh` を上書きして
  起きた spurious exit（atomic mv で直した件）も、そもそも存在しない。
- **更新 = 設定**: バージョンは flake の pin（宣言）。「今この箱が何版か」が常に config から
  自明で、ドリフトしない。

### GUI / CLI からの更新（S6ab0ed との接続 — Stage 4 で確定）

- in-app の「更新あり」バッジ（S6ab0ed）は NixOS では「`nixos-rebuild` で上げてください」を
  指す。GUI の Update ボタンを `nixos-rebuild switch`（または `palmux-update.service` 相当の
  oneshot）に写像するかは Stage 4 で決める。FE の再接続ハンドシェイク（WS drop→/health→toast）は
  トリガ非依存なので無改造で機能する。
- 非NixOS（install.sh）箱は従来どおり `palmux update` / `~/update-palmux2.sh`。

### 自動更新（任意）

`system.autoUpgrade`（`modules/appliance.nix`、既定 off）で定期 `nixos-rebuild switch` を
opt-in 可能。失敗しても世代ロールバックがあるので安全側。アプライアンスの既定は**運用者主導**
（走行中の claude/tmux を勝手に落とさない、という S7364e3 の方針を踏襲）。

### 注意（ブートストラップ）

`nixos-rebuild switch` は palmux2 service を再起動する → 「自分が乗っている service を入れ替える」
問題は NixOS でも残る。アプライアンス更新は palmux の web ターミナルからではなく、**素の ssh /
コンソールから**行う（あるいは FE 再接続ハンドシェイクで短時間の reconnect を許容）。

## 段階的な構築 + 検証計画

リスクは incus-on-NixOS に集中するので、計画はそれを前倒しし、プロジェクトの実機 smoke 規律を
再利用する。**作り直す dev をパイロットに使う**（どうせ rebuild するし使い捨て可）。

- **Stage 0 — scaffold（このコミット）:** `nixos/` flake + module + 拡張フック + examples +
  本 doc。まだ eval 未（設計箱に Nix 無し）。
- **Stage 1 — モジュール疎通（incus なし）:** NixOS VM を立て、`nix flake check`、
  `nixos-rebuild switch` → palmux2 が NixOS service + `services.caddy` vhost + SSO + 設定
  プレーンで動く。アプリ経路を端から端まで検証（実ブラウザ SSO、host runtime の Files/Git/Claude タブ）。
- **Stage 2 — incus-on-NixOS（関門リスク）:** `virtualisation.incus.enable`、idmap
  `both 1000 1000` + `/etc/subuid`/`subgid` + bridge + `palmux runtime install`（palmux-ws
  image）+ ワークスペースコンテナ起動 + Browser/ports/SSO サブドメインを検証。
  `tests/acceptance/*` 同様の実機 acceptance。
- **Stage 3 — アプライアンス image:** `nixos-generators` の qcow2/ISO ターゲット追加、`/persist`
  state 分離 + `/etc/palmux/local` drop-in を配線。image を boot し、image 入替で state が残ること、
  運用者 drop-in の `nixos-rebuild switch` が効くことを実証。
- **Stage 4 — 拡張性 + docs + 更新 UX 確定:** `nixosModules.palmux`、user-flake 例、drop-in を
  確定。GUI/CLI Update の `nixos-rebuild` 写像（更新観点）を確定。
- **Stage 5 — 採用:** 作り直した **dev** を NixOS アプライアンスとして稼働。安定したら任意で
  ndev / deploy-test も移行（単一テナントで低リスク）。Ubuntu + install.sh は非NixOS/既存箱の
  サポート経路として残す。

## Proxmox デプロイの実務 — ディスクバスと initrd

実 Proxmox でのデプロイで判明した落とし穴: qcow2 IMAGE ビルドは `self.nixosModules.appliance`
だけを取り込み `appliance-flake/hardware-base.nix`（= `qemu-guest` profile）は**含まない**。その
ため、出荷 initrd のストレージドライバは `nixos-generators` の qcow デフォルト + appliance.nix
が明示したものに限られる。Proxmox の**既定ディスクバスは virtio-scsi**（`scsi0` /
`scsihw=virtio-scsi-pci`）で、initrd に `virtio_scsi` が無いと stage-1 が
`/dev/disk/by-label/nixos` を待ち続けてタイムアウト → `switch_root` 失敗 → kernel panic
（"Attempted to kill init"）になる。virtio-blk（`virtio0`）は stock initrd に `virtio_blk` がある
ため偶然動いてしまい、切り分けを誤らせる。

対策として `modules/appliance.nix` で
`boot.initrd.availableKernelModules = [ virtio_pci virtio_scsi virtio_blk sd_mod sr_mod ahci ]`
を明示する。このモジュールは **IMAGE ビルド・CI config（`nixosConfigurations.appliance`）・
on-appliance flake のすべて**が取り込むので、プラットフォームが渡すバス（scsi=`/dev/sda` /
blk=`/dev/vda`）に依らず起動する。`grub-device.nix` は初回ブートで実ブートセクタディスク
（root パーティションの親）を解決して書くので、どちらのバスでも `nixos-rebuild` の grub-install
が正しい場所に当たる。

実機検証（pve-01, VM 9001）: 786M qcow2 を `qm importdisk` → `scsi0`（virtio-scsi-pci）+
`qm resize +20G` で再デプロイ → virtio-scsi で完全起動を確認（`/dev/sda` 認識・by-label/nixos を
root mount・cloud-init growpart/resize2fs で root fs 23G に拡張・palmux2 active /
`192.168.1.45:7683` health 200・incus active・`/persist{config,home,nixos,secrets.env}` 正常）。

## 特権 apply（公開ドメイン/TLS）— NixOS では nixos-rebuild を GUI からキック

Ubuntu/install.sh 経路の特権 apply は単一 verb `sudo palmux reconcile-system`（user 所有
master を読んで固定テンプレで `/etc/caddy/Caddyfile` を再レンダ → `systemctl reload caddy`）。
**NixOS アプライアンスではこれは使えない**: ① Caddy は宣言的（`services.palmux.domain` →
flake の `virtualHosts`）で `/etc/caddy/Caddyfile` は nix 管理、② palmux ユーザは password 未設定
かつ wheel 外（鍵ゼロ image の帰結）なので `sudo` 自体が成立しない。実機オンボーディングで公開
ドメインを設定したユーザがまさにここで詰まった（`reconcile-system` のパスワードプロンプトが無限）。

解決は **polkit で特権境界を越える**:

- `modules/appliance.nix` が **root system oneshot `palmux-rebuild.service`**（`nixos-rebuild
  switch --flake ${flakeDir}#appliance` を実行）を定義。
- **polkit ルールで palmux ユーザに「`palmux-rebuild.service` の start だけ」をシステムバス経由で
  許可**（`org.freedesktop.systemd1.manage-units` + `unit==palmux-rebuild.service` + `verb in
  {start,restart}` + `subject.user==palmux` → YES）。no password / no wheel で、reconcile-system の
  単一 verb-sudoers の NixOS 等価。スコープはその1ユニット1アクションに限定。
- ユニットは **独立 cgroup** で走るので、switch が palmux2.service を再起動しても rebuild は道連れに
  ならない（S6ab0ed/Sa8e7d0 の自己アップデート教訓を OS レベルに適用）。

palmux2（非 root）の `POST /api/deploy/rebuild` は `systemctl start --no-block
palmux-rebuild.service` を呼ぶだけ。`GET /api/deploy/rebuild` が `systemctl show` の
ActiveState/Result を返し、GUI（オンボーディングウィザード / 設定デプロイパネルの「適用
(nixos-rebuild)」ボタン）が進捗を poll する。switch 中の palmux2 再起動は既存の reconnect
handshake（WS drop → `/health` → 再接続）が吸収。CLI `palmux apply` も NixOS 検出時は
`systemctl start palmux-rebuild.service`（root 不要）/ root シェルでの `nixos-rebuild switch` を案内。
`GET /api/deploy` の `nixOSHost` フラグ（`selfupdate.IsNixOSHost()`）で GUI/CLI が分岐する。

**bootstrap**: 既存コンテナに unit + polkit を入れる初回の `nixos-rebuild switch` だけは root シェル
で手動（その後は GUI からキック可）。これは「image は seed、初回 switch 後は on-appliance flake が
正典」という Stage 4 の構造そのもの。

## install.sh との関係（無くならない）

`install.sh` は**非NixOS/既存箱向けの簡易インストーラ**として残る（Ubuntu + Determinate Nix +
home-manager）。NixOS モジュールは自分のフリート + アプライアンス用の経路。両者は同じ
`nix/packages/palmux2.nix` を消費する。両デプロイ前線で共通のロジック（設定プレーンの形、Caddy
vhost の形）は将来ファクタリングしてドリフトを防ぐべきだが、それは最適化でありブロッカーではない。
