# palmuxOS — NixOS modules & appliance

> 日本語版は [本ファイル後半](#palmuxos-日本語) を参照。

Declarative, whole-host palmux: the palmux2 service, the incus workspace runtime,
and Caddy (+ SSO) configured from one `services.palmux.*` option set — plus an
appliance image you can flash and extend.

Full design + the staged validation plan: [`docs/nixos-appliance-design.md`](../docs/nixos-appliance-design.md).

> **Status: Stages 1–4 implemented and verified on real hardware** (Proxmox +
> a bare NixOS testbox — see `docs/nixos-appliance-design.md` for the full
> verification log). Stage 5 (adopting it as the primary dev host) is pending.

## What's here

| path | what |
|---|---|
| `flake.nix` | `nixosModules.{palmux,appliance}`, `nixosConfigurations.palmux-appliance`, `packages.*.appliance-qcow2` |
| `modules/palmux.nix` | the reusable host module — `options.services.palmux.*`, all wiring `mkDefault` |
| `modules/appliance.nix` | appliance: immutable image + `/persist` state split + operator drop-in hook |
| `appliance-flake/` | the on-appliance flake shipped to **`/persist/palmux/nixos`** — extend via `local/*.nix`, update via `nix flake update palmux` |
| `examples/onappliance-flake/` | illustrative copy of the on-appliance flake — see `appliance-flake/` for the shipped source |
| `examples/user-flake/` | compose the palmux module into your own flake |

## Extend the appliance with your own config (no fork)

The appliance ships its flake at **`/persist/palmux/nixos`** on the persistent
volume (this is the real `flakeDir` in `modules/appliance.nix`; it is also the
single source the GUI update panel renders via `applianceFlakeTarget`). Add a
fragment and rebuild:

```bash
sudo tee /persist/palmux/nixos/local/my-extras.nix <<'NIX'
{ pkgs, ... }: {
  environment.systemPackages = [ pkgs.tmux pkgs.ripgrep ];
  services.palmux.domain = "dev.example.net";   # overrides palmux's mkDefault
}
NIX
sudo nixos-rebuild switch --flake /persist/palmux/nixos#appliance
```

`local/` lives on `/persist`, so your config survives image upgrades. palmux sets
everything with `lib.mkDefault`, so your plain assignments win.

## Update palmux to the latest release

```bash
cd /persist/palmux/nixos
sudo nix flake update palmux                       # bump the palmux pin to latest main
sudo nixos-rebuild switch --flake /persist/palmux/nixos#appliance
```

This is exactly what the GUI update panel's **本体を更新 (nixos-rebuild)** button
kicks (via the verb-limited `palmux-rebuild-update.service`), so you rarely need to
run it by hand. Generation switch is atomic; roll back with
`sudo nixos-rebuild switch --flake /persist/palmux/nixos#appliance --rollback` or by
booting the previous generation.

## Or compose it into your own flake

See `examples/user-flake/flake.nix` — import `palmux.nixosModules.palmux`, set
`services.palmux.*`, and use the full NixOS surface alongside.

## Build the appliance image

The root flake (not this `nixos/` subdir) is the canonical build entry point:

```bash
nix build .#appliance-qcow2     # → result/main.raw (sparse, disko's native output)
qemu-img convert -O qcow2 -c result/main.raw palmuxos.qcow2   # → ~810MB compressed
```

`nix build .#appliance-qcow2` needs `/dev/kvm` (disko partitions/formats/installs
GRUB inside a real QEMU VM). You usually don't need to build this yourself — every
**minor** release (`vX.Y.0`, not patch releases) publishes a ready-made
`palmuxos-vX.Y.0.qcow2` asset on the [GitHub release](https://github.com/tjst-t/palmux2/releases).
Patch releases (`vX.Y.Z`, Z>0) don't rebuild it: a deployed appliance tracks the
latest patch via `nixos-rebuild switch` (see "Update palmux to the latest release"
above), so the base image only needs to be current as of the last minor bump.

## Deploy the qcow2 on Proxmox (CLI)

Ships with **zero baked SSH keys or passwords** — the image is identical for every
deployer, and you inject your own key via the Proxmox cloud-init drive on first
boot (`docs/nixos-appliance-design.md` § "アクセスと鍵"). Root is a **fixed 16G**
partition; growing the disk only ever grows `/persist` (the state partition),
so a runaway container/build can't fill the OS.

```bash
# 1. Download the qcow2 from a minor release (patch releases don't carry one).
wget https://github.com/tjst-t/palmux2/releases/download/vX.Y.0/palmuxos-vX.Y.0.qcow2

# 2. Create an empty VM shell. cpu=host is REQUIRED — the default kvm64 model is
#    too old for the NixOS 25.05 kernel and hangs at boot (RIP spin). No EFI disk:
#    the image uses a BIOS/GRUB-legacy boot partition (disko-layout.nix), so the
#    Proxmox default `bios: seabios` is correct — don't switch to OVMF.
qm create <VMID> \
  --name palmuxos \
  --memory 4096 --cores 2 --cpu host \
  --net0 virtio,bridge=<bridge> \
  --scsihw virtio-scsi-pci \
  --ostype l26

# 3. Import the qcow2 into a storage pool and attach it as scsi0 (Proxmox's
#    default disk bus). virtio-blk also happens to boot, but virtio-scsi is what
#    was actually verified — the image's initrd carries both drivers either way.
qm importdisk <VMID> palmuxos-vX.Y.0.qcow2 <storage-pool>
qm set <VMID> --scsi0 <storage-pool>:vm-<VMID>-disk-0
qm set <VMID> --boot order=scsi0

# 4. Cloud-init drive: inject your key for the "palmux" user (the appliance's
#    services.palmux.user default — the key must land on THIS account).
qm set <VMID> --ide2 <storage-pool>:cloudinit
qm set <VMID> --ciuser palmux --sshkeys ~/.ssh/id_ed25519.pub
qm set <VMID> --ipconfig0 ip=dhcp   # or ip=192.168.x.x/24,gw=192.168.x.1

# 5. Grow /persist (root stays 16G regardless of how much you add here).
qm resize <VMID> scsi0 +25G

qm start <VMID>
```

First boot: `palmux-grow-persist` extends `/persist` to fill the resized disk,
and with no `services.palmux.domain` configured yet the WebUI is reachable
directly (no SSO in front of it yet — trusted-LAN only) at `http://<vm-ip>:7683`.
SSH in as `palmux` with the key you injected. From there, follow "Extend the
appliance with your own config" above to set a domain and layer your own config.

---

# palmuxOS (日本語)

宣言的な、ホスト丸ごとの palmux: palmux2 サービス、incus ワークスペース
ランタイム、Caddy (+ SSO) を単一の `services.palmux.*` オプション群から構成
する。加えて、そのまま焼いて拡張できるアプライアンス image も配布する。

設計全容 + 段階的な検証計画: [`docs/nixos-appliance-design.md`](../docs/nixos-appliance-design.md)。

> **状態: Stage 1〜4 は実装済みで実機検証済み**（Proxmox + ベアメタルの NixOS
> testbox — 検証ログの全容は `docs/nixos-appliance-design.md` を参照）。
> Stage 5（メインの dev ホストとして採用すること）は保留中。

## ここに何があるか

| パス | 内容 |
|---|---|
| `flake.nix` | `nixosModules.{palmux,appliance}`、`nixosConfigurations.palmux-appliance`、`packages.*.appliance-qcow2` |
| `modules/palmux.nix` | 再利用可能なホストモジュール — `options.services.palmux.*`、すべて `mkDefault` で配線 |
| `modules/appliance.nix` | アプライアンス: 不変 image + `/persist` state 分離 + 運用者 drop-in フック |
| `appliance-flake/` | **`/persist/palmux/nixos`** に配置される on-appliance flake — `local/*.nix` で拡張、`nix flake update palmux` で更新 |
| `examples/onappliance-flake/` | on-appliance flake の説明用コピー — 実際に配布されるソースは `appliance-flake/` を参照 |
| `examples/user-flake/` | palmux モジュールを自分の flake に組み込む例 |

## 自分の設定をアプライアンスに足す（フォーク不要）

アプライアンスは永続ボリューム上の **`/persist/palmux/nixos`** に自身の
flake を配置している（`modules/appliance.nix` の実際の `flakeDir` であり、
GUI の update パネルが `applianceFlakeTarget` として描画する単一のソース
でもある）。断片を足して rebuild する:

```bash
sudo tee /persist/palmux/nixos/local/my-extras.nix <<'NIX'
{ pkgs, ... }: {
  environment.systemPackages = [ pkgs.tmux pkgs.ripgrep ];
  services.palmux.domain = "dev.example.net";   # palmux 側の mkDefault を上書き
}
NIX
sudo nixos-rebuild switch --flake /persist/palmux/nixos#appliance
```

`local/` は `/persist` 上にあるので、image の更新を跨いでも設定は残る。palmux
側はすべて `lib.mkDefault` で設定しているので、素の代入を書けばそちらが勝つ。

## palmux を最新リリースに更新する

```bash
cd /persist/palmux/nixos
sudo nix flake update palmux                       # palmux の pin を最新の main に進める
sudo nixos-rebuild switch --flake /persist/palmux/nixos#appliance
```

これは GUI の update パネルにある **本体を更新 (nixos-rebuild)** ボタンが
（verb を限定した `palmux-rebuild-update.service` 経由で）実行しているのと
全く同じ処理なので、手で叩く必要はほぼ無い。世代の切り替えはアトミックで、
`sudo nixos-rebuild switch --flake /persist/palmux/nixos#appliance --rollback`
または前の世代からの起動でロールバックできる。

## あるいは自分の flake に組み込む

`examples/user-flake/flake.nix` を参照 — `palmux.nixosModules.palmux` を
import し `services.palmux.*` を設定すれば、NixOS の全機能と併用できる。

## アプライアンス image をビルドする

正典のビルド起点はこの `nixos/` 配下ではなく **root の flake** :

```bash
nix build .#appliance-qcow2     # → result/main.raw（スパース、disko のネイティブ出力）
qemu-img convert -O qcow2 -c result/main.raw palmuxos.qcow2   # → 圧縮後 ~810MB
```

`nix build .#appliance-qcow2` は `/dev/kvm` を必要とする（disko が実際の
QEMU VM の中でパーティショニング/フォーマット/GRUB インストールを行うため）。
基本的に自分でビルドする必要はない — **minor** リリース（`vX.Y.0`、patch
リリースではない）ごとに、ビルド済みの `palmuxos-vX.Y.0.qcow2` が
[GitHub release](https://github.com/tjst-t/palmux2/releases) のアセットとして
公開される。patch リリース（`vX.Y.Z`、Z>0）では再ビルドしない: デプロイ済みの
アプライアンスは `nixos-rebuild switch`（上の「palmux を最新リリースに更新
する」参照）で最新 patch に追従するので、base image は直近の minor bump 時点
のものであれば十分だからだ。

## qcow2 を Proxmox に CLI でデプロイする

image は **SSH 鍵・パスワードを一切焼かずに**出荷される — 誰に配っても
同一の image で、Proxmox の cloud-init ドライブ経由で自分の鍵を初回ブート
時に注入する（`docs/nixos-appliance-design.md` の「アクセスと鍵」節）。
root パーティションは **固定 16G**。ディスクを拡張しても伸びるのは常に
`/persist`（state パーティション）だけなので、暴走したコンテナ/ビルドが
OS 領域を食い潰すことはない。

```bash
# 1. minor リリースから qcow2 をダウンロードする（patch リリースには付属しない）。
wget https://github.com/tjst-t/palmux2/releases/download/vX.Y.0/palmuxos-vX.Y.0.qcow2

# 2. 空の VM を作る。cpu=host は必須 — 既定の kvm64 モデルは NixOS 25.05 の
#    カーネルに対して古すぎ、起動時にハングする（RIP spin）。EFI ディスクは
#    不要: image は BIOS/GRUB-legacy のブートパーティションを使う
#    (disko-layout.nix) ので、Proxmox 既定の `bios: seabios` のままで良い
#    （OVMF には切り替えないこと）。
qm create <VMID> \
  --name palmuxos \
  --memory 4096 --cores 2 --cpu host \
  --net0 virtio,bridge=<bridge> \
  --scsihw virtio-scsi-pci \
  --ostype l26

# 3. qcow2 をストレージプールに import し、scsi0（Proxmox の既定ディスクバス）
#    として接続する。virtio-blk でも起動はする（image の initrd は両方の
#    ドライバを積んでいる）が、実際に検証したのは virtio-scsi の方。
qm importdisk <VMID> palmuxos-vX.Y.0.qcow2 <storage-pool>
qm set <VMID> --scsi0 <storage-pool>:vm-<VMID>-disk-0
qm set <VMID> --boot order=scsi0

# 4. cloud-init ドライブ: "palmux" ユーザー（アプライアンスの
#    services.palmux.user の既定値）宛に自分の鍵を注入する
#    — このアカウント宛でないと鍵が届かない。
qm set <VMID> --ide2 <storage-pool>:cloudinit
qm set <VMID> --ciuser palmux --sshkeys ~/.ssh/id_ed25519.pub
qm set <VMID> --ipconfig0 ip=dhcp   # または ip=192.168.x.x/24,gw=192.168.x.1

# 5. /persist を拡張する（ここでいくら足しても root は 16G のまま）。
qm resize <VMID> scsi0 +25G

qm start <VMID>
```

初回起動時: `palmux-grow-persist` が拡張後のディスクいっぱいまで
`/persist` を伸ばす。まだ `services.palmux.domain` を設定していない段階
では WebUI は直接（SSO はまだ前段に無い — 信頼できる LAN 限定という前提で）
`http://<vm-ip>:7683` に到達できる。注入した鍵で `palmux` ユーザーとして
SSH ログインできる。そこから先は、上の「自分の設定をアプライアンスに足す」
に従ってドメインを設定し、自分の設定を重ねていけば良い。
