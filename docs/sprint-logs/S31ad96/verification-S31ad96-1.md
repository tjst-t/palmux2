# S31ad96-1 verification — local-source-build Nix package

Story: 開発者として、palmux2をローカルの作業ツリーソースからビルドしたい。なぜなら、
fetchurlでのリリース済みバイナリ取得だと未リリースの変更(Go/frontend)を一切検証できないから。

## What was built

- `nix/packages/palmux2-frontend.nix` — `buildNpmPackage` derivation that runs
  `frontend/`'s own `npm run build` (`tsc -b && vite build`, identical to
  `make build-frontend`) and installs the resulting `dist/` as its output.
  Kept as a **separate** derivation from the Go build (see the file's header
  comment for the full rationale: two independently-hashed fixed-output
  fetches — npm deps vs. Go module deps — invalidate independently, each is
  separately buildable/debuggable, and neither toolchain has to run inside
  the other's phases).
- `nix/packages/palmux2-local.nix` — `buildGoModule` derivation with
  `src = ../..` (the flake's own working tree, NOT a fetched tarball).
  `preBuild` replaces the gitignored `frontend/dist/.gitkeep` placeholder
  with the real `palmux2-frontend.nix` output before `go build` runs, so
  `embed.go`'s `//go:embed all:frontend/dist` picks up real assets — exactly
  matching `make build`'s `build-frontend` → `go build` sequence.
  `postInstall` renames the Go-derived `bin/palmux` to `bin/palmux2` so this
  package is a drop-in substitute anywhere `${cfg.package}/bin/palmux2` is
  invoked (matches `nix/packages/palmux2.nix`'s installed binary name).
- `flake.nix` — added `packages.<system>.palmux2-local` as an **additional**
  output. `default` and `overlays.default.palmux2` are untouched and still
  resolve to the release fetchurl package
  (`nix/packages/palmux2.nix`, unmodified).

## Why buildNpmPackage + buildGoModule, not a shell hook

See the header comments in both new `.nix` files for the in-line rationale.
Short version: this is the standard nixpkgs shape for "Go binary with an
embedded npm-built frontend" (mirrors how nixpkgs itself packages e.g.
Grafana/Gitea-style projects) — two independently cacheable/inspectable
fixed-output derivations, rather than fighting `buildGoModule`'s phases with
an npm preBuild hook inside the same derivation.

## Verification approach for AC-1-3 (real qcow2 boot)

`nixos/modules/palmux.nix` already exposes `services.palmux.package` as a
plain `lib.types.package` option (not previously known to me before reading
the module — confirmed via `grep -n package nixos/modules/palmux.nix`), so
no module change was needed to make it overridable. Per the Story's guidance
("prefer whichever is less invasive"), verification used approach (b): a
**temporary, uncommitted** edit to the build host's copy of `flake.nix`
that added one extra module to the `appliance-qcow2` output's module list:

```nix
({ lib, ... }: { services.palmux.package = lib.mkForce self.packages.${system}.palmux2-local; })
```

This edit was made only on the `deploy-test` build host's throwaway clone
(`~/palmux2-build-S31ad96`), never committed, and reverted (`git reset
--hard origin/worktree-S31ad96-1`) immediately after the qcow2 was built —
the committed `flake.nix` in this repo has no such override; `nix build
.#appliance-qcow2` on the real branch still uses the release package by
default, unchanged.

## Build host

Per CLAUDE.md's "palmuxOS アプライアンス (qcow2) をローカルで評価する" section:
**`deploy-test` (192.168.1.43)**, NOT `palmux-nix-builder` (that host's L2
nested-virt depth cannot run disko's internal qemu — see the 2026-07-16
addition to that CLAUDE.md section). `deploy-test` has Determinate Nix
3.21.1, `/dev/kvm` already world-rw, 1 core / ~11-16G disk free (shared
host — cleaned up before/after, see below).

## Hash bootstrapping — exact commands (re-derive when go.sum / package-lock.json change)

### `npmDepsHash` (nix/packages/palmux2-frontend.nix)

```
ssh 192.168.1.43
. /nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh
nix --extra-experimental-features 'nix-command flakes' shell nixpkgs#prefetch-npm-deps \
  -c prefetch-npm-deps frontend/package-lock.json
# -> sha256-FKyIqO1QFpy1bQstzi4jvkUR36emT6NJZRK/EIKiFnE=
```

(Binary is named `prefetch-npm-deps`, not `nix-prefetch-npm-deps` as the
package name might suggest — `ls $(nix build nixpkgs#prefetch-npm-deps
--no-link --print-out-paths)/bin` if the exact name changes upstream.)

### `vendorHash` (nix/packages/palmux2-local.nix)

Standard "fake hash, read the mismatch" bootstrap — set a placeholder
(`sha256-AAAA...=`), attempt the build, and read the `got:` hash back:

```
cd ~/palmux2-build-S31ad96   # fresh clone of this branch
nix --extra-experimental-features 'nix-command flakes' build .#palmux2-local \
  -o /tmp/s31ad96-local-result
# error: hash mismatch in fixed-output derivation
#   '.../palmux2-local-0.0.0-local-go-modules.drv':
#   specified: sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
#   got:       sha256-TnTy/Pe+/lfcEkV+M+lJUS/Injf9r+Ugfd7C9JvnCAM=
```

Both hashes were then written into the two `.nix` files, committed, and
pushed to `worktree-S31ad96-1` so the deploy-test clone could `git fetch` +
`git reset --hard` them in.

## AC-1-1 — package exists, release package unaffected

```
$ nix build .#palmux2-local -o /tmp/s31ad96-local-result
$ /tmp/s31ad96-local-result/bin/palmux2 --version
v0.0.0-local

$ nix build .#palmux2 -o /tmp/s31ad96-release-result   # unmodified release package
$ /tmp/s31ad96-release-result/bin/palmux2 --version
v0.15.0
```

Both build successfully from the same working tree. The release package
still resolves to the pinned `v0.15.0` GitHub Release asset exactly as
before — `nix/packages/palmux2.nix` was not touched. **PASS.**

Additionally confirmed the built frontend bundle actually contains real
local source (not a stale/cached bundle):

```
$ grep -rl 'drawer-host-gh-hint' /nix/store/*-palmux2-frontend-local/
/nix/store/iq534b89v00l9zbgc8m8rnyn9d71lvyc-palmux2-frontend-local/assets/index-Cz4dINXQ.js
```

(`drawer-host-gh-hint` is the `data-testid` from S61c9a6-4's
`frontend/src/components/drawer.tsx`, already merged into this branch's
history — a real, meaningful marker, not a synthetic one.)

## AC-1-2 — flake output

```
$ nix flake show . 2>&1 | grep -i palmux2
        ├───palmux2: package
        └───palmux2-local: package
```

`default` unchanged (`nix build .` still resolves to the release package).
**PASS.**

## AC-1-3 — real qcow2 boot reflects local source

1. Built `appliance-qcow2` on deploy-test with the temporary `flake.nix`
   override described above:
   ```
   nix build .#appliance-qcow2 -o /tmp/s31ad96-appliance-result
   # -> result/main.raw (18GB sparse, ~3.0G actual)
   ```
   Build succeeded (`EXIT_CODE:0`), no OOM (deploy-test currently has enough
   headroom; this build was run serially, no concurrent build on the host).
2. Compressed to qcow2 and transferred to the dev box:
   ```
   nix shell nixpkgs#qemu -c qemu-img convert -O qcow2 -c \
     /tmp/s31ad96-appliance-result/main.raw /tmp/palmuxos-s31ad96-1-local.qcow2
   # -> 970,719,232 bytes (~925MB)
   scp 192.168.1.43:/tmp/palmuxos-s31ad96-1-local.qcow2 <dev-box>:~/appliance-test-s31ad96/
   ```
3. Booted on the dev box directly (this box is Proxmox-KVM-capable per
   CLAUDE.md's qcow2-eval section — confirmed already-on-L1, not inside an
   incus Workspace container, for this session), cloud-init seed with user
   `palmux` (not `ubuntu` — the appliance's real user):
   ```
   qemu-img create -f qcow2 -F qcow2 -b palmuxos-s31ad96-1-local.qcow2 overlay.qcow2
   genisoimage -output seed.iso -volid cidata -joliet -rock user-data meta-data
   qemu-system-x86_64 -enable-kvm -cpu host -m 4096 -smp 2 \
     -drive file=overlay.qcow2,if=virtio,format=qcow2 \
     -drive file=seed.iso,if=virtio,format=raw \
     -netdev user,id=net0,hostfwd=tcp::12226-:22,hostfwd=tcp::17687-:7683 \
     -device virtio-net-pci,netdev=net0 \
     -nographic -serial file:serial.log -display none -pidfile qemu.pid
   ```
4. Checked services + version over SSH:
   ```
   $ ssh -p 12226 palmux@localhost 'systemctl is-active palmux2 incus; which palmux2; palmux2 --version'
   active
   active
   /run/current-system/sw/bin/palmux2
   v0.0.0-local
   ```
   `v0.0.0-local` (NOT `v0.15.0`) is itself already strong evidence this
   boot is running the local-source-build package, not the release binary.
5. Ran the existing real E2E test (`tests/e2e/s61c9a6_gh_onboarding_hint.py`,
   S61c9a6-4's gh-hint check — chosen per the Story's guidance as a real,
   already-merged, meaningful marker) against the booted instance:
   ```
   $ PALMUX2_DEV_PORT_OVERRIDE=17687 python3 tests/e2e/s61c9a6_gh_onboarding_hint.py
     [AC-S61c9a6-4-1] drawer-host-section present
     [AC-S61c9a6-4-3] drawer-host-gh-hint present + visible
     [AC-S61c9a6-4-1] hint mentions gh + App Catalog nav path: 'gh (GitHub CLI) が未インストールなら 設定 → デプロイ設定 → アプリ からインストールできます'
     [AC-S61c9a6-4-1] 設定 → デプロイ設定 → アプリ actually lists gh (nav path verified real)

   ALL PASS
   ```

**PASS** — a real qcow2, built via `nix build .#appliance-qcow2` with the
`palmux2-local` package substituted in, boots and demonstrably serves
locally-built frontend/backend code (distinct version string + a real,
already-merged frontend marker reachable through the live UI).

## Cleanup

- qemu boot-test process killed; overlay/seed/base qcow2 and the whole
  `~/appliance-test-s31ad96` directory removed from the dev box.
- deploy-test: `/tmp/s31ad96-appliance-result` symlink,
  `/tmp/s31ad96-local-result`, `/tmp/s31ad96-release-result`, and the
  `/tmp/palmuxos-s31ad96-1-local.qcow2` compressed image all removed.
  The temporary `flake.nix` override was reverted
  (`git reset --hard origin/worktree-S31ad96-1`) — deploy-test's
  `~/palmux2-build-S31ad96` clone (~50M, source only, no build outputs)
  was left in place as a ready-to-reuse checkout for S31ad96-2, mirroring
  how the S61c9a6 verification left its builder host's clone/base qcow2
  around for reuse. `df -h /` on deploy-test returned to its pre-existing
  baseline (~11-12G free, no net growth left behind).

## Out-of-scope / not done here

- S31ad96-2 (the "update an already-installed release appliance to local
  source via `nixos-rebuild switch`" story, plus the CLAUDE.md doc update)
  is a separate Story and was not attempted.
- No changes to `nixos/modules/palmux.nix` were needed or made — its
  existing `services.palmux.package` option was already overridable.
