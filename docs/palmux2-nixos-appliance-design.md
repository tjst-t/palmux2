# Palmux2 NixOS アプライアンス化 — レイヤリング設計原則

> **本書の位置づけ**: 「どの設定をどのレイヤーで管理するか」の判断原則集。
> 実装の現状・経緯・実機検証の正典は [nixos-appliance-design.md](nixos-appliance-design.md)
> (Sb14caa)。本書の例のうち現状実装と異なる将来方針には **[提案]**、実装済みの
> ものには **[実装済]** を付す。迷ったら付録のフローチャートだけ見ればよい。

## 1. 前提となる思想

NixOS の中核は **「システムの状態 = 設定の純粋関数」**。

- `configuration.nix`(入力)から、システム全体(出力)が決定的に導出される。
- 同じ入力なら必ず同じ出力(再現性)。
- 「システムをいじる」のではなく「システムを記述して再構築する」。

これを支える実装:

- **Nix store (`/nix/store`)**: 全パッケージがハッシュアドレスで不変配置。書き換えない。world-readable。
- **Generation / Profile**: 現在のシステムは store 実体へのシンボリックリンク集。切替はアトミック、失敗は完全ロールバック。
- **モジュールシステム**: 「あるべき状態」を宣言すると、パッケージ配置・ユーザ作成・systemd unit 生成まで一括導出。

home-manager はこの同じ思想を **`$HOME` に適用する層**。ただしシステム層とは別レイヤー(standalone でも動く)。

## 2. 分類の判断軸 — 3つの問いと4つのバケツ

「システムか否か」の二分では不足。設定・ファイルが1つ出てきたら、以下の3つの問いを **この順** で当てる。

```
秘密か? ──Yes──> 秘密層(他より優先)
  │No
サーバ/実行時が書き換えるか? ──Yes──> Nix管理外
  │No
root/特権が要るか? ──Yes──> configuration.nix
  │No
  └──> home.nix
```

結果として4つのバケツになる。

| バケツ | 内容 | 管理 |
|---|---|---|
| システム | サービス・ネットワーク・特権・依存基盤 | `configuration.nix` |
| ユーザ環境 | dotfiles・個人ツール | `home.nix` |
| 秘密 | token・API キー・パスワード | 秘密層(下記) |
| 実行時可変状態 | サーバ/アプリが書き換えるファイル | Nix 管理外 |

各バケツの「なぜ」:

- **実行時可変状態を home.nix に入れてはいけない** — home.nix で宣言したファイルは store 内実体への**読み取り専用シンボリックリンク**になる。アプリが書き換えられず壊れる。
- **秘密を configuration.nix / home.nix に直書きしてはいけない** — どちらも store に平文で焼き込まれ、world-readable になる。

**秘密層の現状と将来**: 現在の実装は **user 所有 `secrets.env` (0600、store 外)** を
systemd `EnvironmentFile` と palmux2 の `--config-dir` 層が共有する方式 (Sa53137 /
appliance では `/persist/palmux/config/secrets.env`) **[実装済]**。git 管理したく
なった時点で **sops-nix**(暗号文のみ git 管理、平文は store 外で復号)への移行を
検討する **[提案]**。どちらも「store に平文を置かない」という同じ原則の実装である。

## 3. 「システムだがユーザ設定で動作が変わる」ものの扱い

Caddy が典型。**home.nix に置かない。** システムサービス(特権ポート・system unit)なので必ずシステム層。

ユーザに操作させたい点は if 文ではなく **公開オプション (`options`)** として切り出す。

```nix
# nixos/modules/palmux.nix (実物) [実装済]
options.services.palmux.domain = lib.mkOption {
  type = lib.types.nullOr lib.types.str;
  default = null;   # null = local-only
};
config.services.caddy.virtualHosts.${cfg.domain} = { ... };
```

ユーザは `services.palmux.domain = "dev.example.com";` と宣言するだけ。
システムがそれを入力として受け、設定を導出する。

→ `f(configuration.nix, home.nix)` にはならない。依存は一方向で、
最終的に **`システム = f(configuration.nix)`**(統合時は home.nix もその内側)。

三層で捉える:

| 層 | 内容 | 誰が変える |
|---|---|---|
| アプライアンス実装 | Caddy の存在・unit・基本構造 | バージョンアップのみ |
| 公開オプション | `services.palmux.*` 等、意図的に露出した設定点 | ユーザ(宣言的に) |
| 純粋なユーザ環境 | dotfiles・個人ツール | ユーザ(home.nix) |

このため palmux モジュールは自分の設定値を全て `lib.mkDefault` で置く **[実装済]**。
運用者の drop-in (`/persist/palmux/nixos/local/*.nix`) は素の代入で必ず勝てる。

## 4. Palmux 設定の具体的な振り分け

```
configuration.nix (= nixos/modules/{palmux,appliance}.nix) [実装済]
  - palmux2 systemd service(--addr / --config-dir 等、非秘密フラグを埋め込み)
  - Caddy 本体 + 公開オプション(services.palmux.domain 等)
  - incus / nix-ld などシステム依存基盤
  - secrets.env を EnvironmentFile で service に注入

home.nix
  - ユーザツール・dotfiles(ghq / gwq / claude まわりの個人設定)

秘密層(user 所有 secrets.env [実装済] / 将来 sops-nix [提案])
  - PALMUX_SSO_SECRET / BASIC_AUTH_HASH / CLOUDFLARE_API_TOKEN

Nix 管理外(サーバ/実行時が所有) [実装済]
  - ~/.config/palmux/*.json (settings / repos / sessions)
    ※ appliance では /persist/palmux/config/、config.toml もここ
  - ~/.claude/ の認証・履歴、~/ghq/ のリポジトリ
```

### tmux.conf は Caddy と同型

Palmux は動作要件として特定の tmux 設定(`tmux-256color`, `mouse on` 等)を**要求**する。
「ユーザの dotfile だがアプライアンスが最低ラインを規定する」ケース。
`programs.tmux` でアプライアンスが基本を宣言し、ユーザが上書き可能にする(公開
オプションと同じ「規定 + 上書き可」パターンを `$HOME` 側に適用しただけ)。
純粋放任にすると要件が壊れて Palmux が動かなくなる。

## 5. コンテナ設計(Incus / 動的増減)

Palmux2 はコンテナを **動的に増減** させる。
→ 「今どのコンテナが存在するか」は可変な実行時状態。**Nix の宣言に含めない。**
(無理に宣言へ寄せると、Palmux の動的操作と Nix の宣言が主導権を奪い合って破綻する)

### 宣言するのは「金型」

個々のコンテナではなく、**全コンテナが従う共通形を1つ宣言的に固定する。**

- Nix が「コンテナ工場の金型」を宣言
- Palmux2 が「金型で製品を動的に打ち出す」

責任分界:

| 対象 | 扱い | 現状 |
|---|---|---|
| Incus デーモン・ネットワーク・ストレージプール・default プロファイル | `configuration.nix` (`virtualisation.incus` + `palmux-incus-reconcile`) | [実装済] |
| 共有 device(bind mount)群 | 金型の一部 | **[実装済 Sd44947]** palmux が単一 incus profile `palmux-shared` を config.toml + mounts[] ロジックから射影(profile-as-mold)。コンテナは `default + palmux-shared` で launch し per-container device add を廃止、10s scan ループに相乗りした reconcile が profile を宣言へ収束させる(手で device を消しても次 scan で復活)。**金型の所有者は palmux**(NixOS/install.sh 両ホストで incus runtime を賄うため configuration.nix ではなく palmux が config.toml を正として射影。NixOS ネイティブ profile 宣言は将来形 **[提案]**、決定 decisions.json D1)。 |
| コンテナのライフサイクル(launch/delete) | Palmux2(実行時・Nix 管理外) | [実装済] |
| コンテナ内の開発者環境(dotfiles・ツール) | 全コンテナ共通の一枚 | 現状は palmux-ws image 焼き込み + host dotfiles bind。home.nix 化は **[提案]** |
| コンテナ内のコード・実行状態 | Nix 管理外 | [実装済] |

**教訓 [実装済]**: 金型の宣言は「first-run only の preseed」では成立しない。image に
焼かれた既初期化 DB には適用されないため、**毎 boot 冪等な reconcile**
(`palmux-incus-reconcile`: pool / bridge / profile devices を create-if-missing)で
収束させる。§8.5 の Reconcile 原則のシステム層での適用例。

## 6. ホスト→コンテナ公開(所与の土台)

アプライアンスが全コンテナに保証する共通基盤。disk device(docker の `-v` 相当)で公開する。

### 大原則: same-path マウント

**ホストとコンテナで同一絶対パスにマウントする**(`source=X path=X`)。理由は実地で確定済み:

- Claude の履歴・コンテキストはプロジェクトの**絶対パス由来の slug**
  (`~/.claude/projects/-home-ubuntu-ghq-…`)で引かれる。パスがずれると履歴が孤立する。
- palmux の incus runtime・claude-in-container は `/home/ubuntu` を前提にする。
  ホスト側 `$HOME` も `/home/ubuntu` に揃える(appliance は `/persist/palmux/home`
  を `/home/ubuntu` に bind して両立)。**[実装済・2026-07-04 の実障害から確定]**

### 現状の公開物 [実装済]

palmux-ws image(Ubuntu ベース、ツール焼き込み)+ 以下の bind(存在するものだけ、os.Stat ガード):

| 公開物 | 理由 |
|---|---|
| `~/ghq` | リポジトリ本体 |
| `~/.claude` `~/.claude.json` | Claude 認証・履歴の共用 |
| `~/.local/share/claude` `~/.local/bin` | claude native バイナリ(ホストと同一版) |
| `~/.bashrc` 系 dotfiles | シェル体験のホスト追従 |
| `~/.gitconfig` `~/.config/gh` `~/.ssh` | git/gh push まで動くフル認証 |
| palmux 自身 → `/usr/local/bin/palmux` | in-container hook |
| `raw.idmap "both 1000 1000"` | UID/GID 整合(全ての前提) |

### 将来形: NixOS ネイティブコンテナ [提案]

image 焼き込みをやめ、`/nix` を読み取り専用共有して home-manager プロファイルを金型に載せる:

```
incus profile device add palmux-dev nixstore  disk source=/nix path=/nix readonly=true
incus profile device add palmux-dev shprofile disk source=/home/ubuntu/.nix-profile path=/home/ubuntu/.nix-profile readonly=true
incus profile set palmux-dev environment.PATH "/home/ubuntu/.nix-profile/bin:/usr/local/bin:/usr/bin:/bin"
```

home-manager 管理ファイルは `/nix/store/...` へのシンボリックリンクなので、
リンクだけマウントしてもリンク先が無ければ dangling になる。
**`/nix` (ro) の共有が全ての前提**。これが通れば「image 再ビルド → 再生成」が
「`home-manager switch` 一発で全コンテナ反映」に変わる。

## 7. ユーザがアプリ/認証を追加する仕組み

責任分界の型(現在・将来で不変):

| 誰が | 何を | どこで |
|---|---|---|
| アプライアンス | 公開の**経路**(共有 mount・PATH・idmap・金型) | configuration.nix |
| ユーザ | 経路に**乗せるアプリ** | パッケージ集合に足すだけ |

### アプリの追加

- **現状 [実装済]**: `~/.local/bin` が全コンテナ+ホストに共有済みなので、そこに
  置いたバイナリは即全域で使える(claude 自身がこの経路)。image 常備にしたい
  ものは `images/workspace-default/build.sh` に足して再ビルド + 再生成。
- **将来形 [提案]**: `home.packages = [ pkgs.infisical ];` → `home-manager switch`
  だけで、共有 `/nix` 経由で全コンテナから即見える。ユーザは経路を敷かず、
  パッケージ集合に足すだけ。

### 認証状態の追加(例: `~/.infisical`)

CLI の認証トークンは `~/.claude` と同じ実行時可変状態。全コンテナで共有したいなら
共有 device を1本足す(現状はコード側 mounts[] への追加 = リリース要 **[現状]**、
将来はプロファイル device + GUI トグル **[提案]**)。

注意:
1. **UID/GID 整合** — `raw.idmap` が効いている前提。
2. **既存コンテナには即反映されない** — 新規起動には効くが、稼働中は再起動 or 個別 `incus config device add` が要る(§8.4 の2フェーズ)。
3. **事前にホストでディレクトリを作る** — 先に `infisical login` してから公開するのが安全。

共有するか否かは運用判断:
- 単一アカウントで複数プロジェクトを跨ぐ → 共有が正解
- コンテナを顧客/環境の分離境界にする → 非共有

## 8. WebGUI 設定 UI への落とし込み

### 8.1 核心の緊張: 宣言的設定 vs GUI 編集

GUI で設定を変える = 実行時に書き換える行為。しかし4バケツのうち3つ
(configuration.nix / home.nix / 秘密層)は宣言的で、通常はファイル編集 + rebuild が要る。
GUI の「保存したら即効く」期待とぶつかる。

方針(確定):
- **設定メニューに入れるものは低頻度**。よって rebuild コストは許容できる。
- ゾーンを物理分割する必要はなく、**項目ごとに「変更には rebuild が要る」とインライン表示**すれば足りる。
- **GUI が基本**(アプライアンス OS として)、**上級者はテキスト編集 + GitHub 連携**、
  **ロールバックは Nix の generation 機能をそのまま出す**。

### 8.2 source of truth: 構造化データを正とし .nix を生成

GUI とテキスト編集を両立させると「どちらが正か」問題が出る。採用する方針:

- **GUI は構造化データを正とし、そこから `.nix` を一方向生成する。**
- 手書き `.nix` の双方向パースはしない(任意の Nix 式の再生成は実装地獄で、アプライアンスに過剰)。
- 上級者向けテキスト編集 = **生成元の構造化データを直接編集** +
  はみ出す分だけ「生の `.nix` オーバーレイ」に限定。
- GitHub 連携 = その構造化データ(+オーバーレイ)をリポジトリ同期。
- ロールバック = Nix の generation を一覧表示し、`nixos-rebuild --rollback` 相当を呼ぶ。

これで権威が一意に決まり、GUI/テキスト/GitHub が同じソースを指す。

**最小実装が既に稼働している [実装済]**: `config.toml [public].domain`(構造化データ)
→ `palmux-rebuild.service` が `local/10-public.nix` を生成 → `nixos-rebuild switch`。
オーバーレイ = `local/` のその他の drop-in。本節はこの路線の一般化である。

#### オーバーレイと GUI 生成物の衝突解決

同じオプションを GUI 生成物とオーバーレイの両方が設定した場合:

- **勝敗: オーバーレイ後勝ち。** ただし理由は「上級者だから」ではなく、
  **Nix の優先度機構(`lib.mkForce` / `mkOverride`)に素直に乗るから**。
  自前の後勝ちロジックを書かず、オーバーレイ層を高優先度で合成する
  (現行実装ではモジュール側が全て `mkDefault` なので drop-in の素の代入が勝つ —
  同じ機構の現在形)。
- **沈黙の後勝ちは禁止。** GUI である項目を編集したとき、それがオーバーレイで
  上書きされているなら「この項目はオーバーレイで上書きされています(GUI の値は
  無視されます)」と警告表示する **[提案]**。勝たせるが、隠さない。
- 理由: 黙って勝たせると、上級者ほど「GUI で変えたのに効かない」という最悪の
  デバッグ体験に陥る。勝敗ルールは後勝ちのまま、衝突の存在だけは可視化する。

### 8.3 GUI のモデル: 「アプリ」を第一級の単位にする [提案]

頻度が相対的に高い操作(アプリ導入・フォルダ共有)を UI の中心に据える。
1アプリ = 1カード。裏で複数レイヤーに書き込むが、ユーザには1枚に見せる。

```
[Infisical]
  ├ インストール              → パッケージ集合(§7)            [要 rebuild(軽)]
  └ 認証フォルダを共有(~/.infisical)
                             → 金型に disk device 追加        [要 rebuild + 動的適用]
```

ルール:
- 両トグルとも**全コンテナ一律**(個別出し分けはしない → 金型=単一プロファイルを維持)。
- 「フォルダ共有」は「インストール」に**従属**。未インストールなら共有トグルはグレーアウト。
- **rebuild 境界を区別表示**: インストールのみ = 軽・即。
  フォルダ共有 = システム rebuild + 既存コンテナへの反映が要る(重)。

### 8.4 公開/非公開の動的適用: 2フェーズ

フォルダ共有・アプリ公開の ON/OFF は、宣言更新と稼働コンテナへの反映を分けて2フェーズで行う。

- **ON** = 宣言に追加 → 全稼働コンテナへ `incus config device add`
- **OFF** = 宣言から削除 → 全稼働コンテナから `incus config device remove`

これはコンテナのライフサイクル(動的・API 側)が担う。
Nix は宣言、実行時反映は API、という§5の原則に沿う。

### 8.5 2フェーズの弱点と、それを閉じる原則

動的適用を許すと、稼働コンテナの状態が「宣言」と「後から動的に足したもの」の
重ね合わせになり、**ドリフト**(宣言と実態のズレ)が起きうる。放置すると
「宣言が唯一の正」が崩れ、再現性が死ぬ。以下で閉じる。

1. **一方向を固定**: 操作は必ず「宣言を更新 → 稼働コンテナに射影」の順。
   逆(先に動的に足して後で宣言に書く)を許さない。手動 `incus config device add` の導線を作らない。
2. **動的適用は宣言と同一内容**: 稼働コンテナに足す device 定義は、
   宣言に書いたものをそのまま使う。別物を足さない。
3. **収束先の一致**: 新規コンテナ(宣言から生成)と既存コンテナ(動的適用済み)が、
   再起動しても同じ状態に収束する(冪等性)。
4. **Reconcile 操作**: 宣言と稼働状態を突き合わせて修復する操作を用意する。
   一部コンテナで `add`/`remove` が失敗した場合、宣言を正として全コンテナを揃え直す。
   これが無いと失敗が静かに蓄積してドリフトする。
5. **失敗時の扱い**: N台中一部が失敗しても全体ロールバックはせず、
   **失敗を記録 → Reconcile で収束**(アプライアンスでは全体ロールバックより現実的)。

> 2フェーズ設計は「宣言→射影の一方向」+「Reconcile での収束保証」とセットで初めて成立する。
> これが無い2フェーズはドリフトする時限爆弾になる。

**先例 [実装済]**: この原則は既に2箇所で使われている。
① Caddy admin-API route はreload で消えるため、port scan ループが公開中 route を
毎回再注入して self-heal(See8bd4 `resyncExposedRoutes`)。
② incus の pool/bridge/profile は毎 boot `palmux-incus-reconcile` が create-if-missing
で収束(§5)。新しい2フェーズ機構を作るときは同じ形に載せる。

### 8.6 GUI 設計が分類にフィードバックする

重要な逆算: **「GUI で頻繁に変えたい」= 実行時可変 = Nix 管理外**、が一貫した帰結。
ある設定を GUI に出したくて rebuild が煩わしいと感じるなら、それは公開オプションではなく
実行時可変状態に降ろすべきというサイン。GUI 要件がバケツ分類を検証する。

## 付録: 判断フローチャート(クイックリファレンス)

設定・ファイルが1つ出てきたら:

```
秘密か? ──Yes──> 秘密層(secrets.env / 将来 sops-nix)
  │No
サーバ/実行時が書き換えるか? ──Yes──> Nix管理外
  │No
root/特権が要るか? ──Yes──> configuration.nix
  │No
  └──> home.nix
```

コンテナ関連なら:

```
コンテナの存在自体(動的) ──> Palmux2(Nix管理外)
コンテナの金型(プロファイル・共有device) ──> configuration.nix
コンテナ内のユーザ環境 ──> 共通一枚(現状 image+bind / 将来 home.nix)
コンテナ内のコード・実行状態 ──> Nix管理外
```
