# S31ad96-2 verification — release→local-source appliance update flow

Story: 運用者として、既にインストール済みのアプライアンス(リリース版)を、ローカルの未リリース変更に
更新したい。なぜなら、リリースを経ずに開発中の変更を実機で検証したいから。

User-requested workflow (verbatim, from the Sprint's own rationale): 「qcow2でインストール後に、
最新にアップデートして確認してほしかった。ただその最新はローカルにしかないけど。」 — i.e. (1) install
a REAL RELEASED qcow2, (2) point its on-appliance flake at the LOCAL git checkout instead of the
pinned `github:tjst-t/palmux2` input, (3) `nixos-rebuild switch` on the SAME running instance, (4)
confirm the local-only change is reflected.

## What was built (repo changes)

**None required in the end.** `nix/packages/palmux2-local.nix` and `flake.nix`'s
`packages.<system>.palmux2-local` (both from S31ad96-1) were already sufficient — see
"Detour: the overlay approach that didn't work" below for what was tried and reverted, and
why. The entire release→local-source update procedure lives in operator actions against the
*deployed instance's* on-appliance flake (`/persist/palmux/nixos/flake.nix`), documented in
CLAUDE.md's "palmuxOS アプライアンス (qcow2) をローカルで評価する" §更新, not in this repo's
own `flake.nix`/`nix/packages/`.

## Detour: the overlay approach that didn't work (kept in git history for the record)

The first instinct was to expose `palmux2-local` through `overlays.default` (mirroring how
`palmux2`/`caddy-cloudflare`/`gwq`/`claude-code` are exposed), so an operator drop-in could say
`services.palmux.package = pkgs.palmux2-local;`. This was committed
(`nix(flake): expose palmux2-local via overlays.default (S31ad96-2)`), tried on the real VM, and
**failed**: the on-appliance flake's own `nixpkgs` input is pinned to `nixos-25.05` (Go 1.24.10),
but `go.mod` requires `go >= 1.25.0` — nixos-25.05 predates Go 1.25's release, so there is no
compatible Go in that package set at all. Building `pkgs.palmux2-local` through the overlay means
building it with the *consuming* flake's nixpkgs, which for the on-appliance flake is the
Go-1.24-only nixos-25.05.

The fix: don't route through the overlay. Reference `palmux.packages.${system}.palmux2-local`
directly — this resolves through the `palmux` flake input's **own** `nixpkgs` (the root
`flake.nix`'s `nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable"`, which has Go ≥1.25),
independent of whatever nixpkgs the on-appliance flake itself pins. This is exactly the mechanism
S31ad96-1's own verification used (`self.packages.${system}.palmux2-local`) — re-derived here the
hard way after temporarily going down the overlay path. The overlay commit was reverted
(`git revert`, kept in history rather than squashed, since the failure mode and its fix are
themselves the useful artifact here for anyone who tries the overlay shortcut again).

## Build host / test instance

Directly on **the dev box itself** (`dev.tjstkm.net`) — this session was NOT running inside an
incus Workspace container this time (`findmnt -no SOURCE /` showed the host's LVM root, not an
incus rootfs), so no ssh-out-to-host hop was needed. `/dev/kvm` present and usable directly.

## AC-S31ad96-2-1 — procedure established & documented

Established through the real-VM run below; documented in `CLAUDE.md`'s
"palmuxOS アプライアンス (qcow2) をローカルで評価する" §「既にインストール済みの『リリース版』
qcow2 を、ローカルの未リリース変更に更新して検証したい場合」. Summary of the 6 steps (see
CLAUDE.md for the exact commands):

1. Get the local source tree into the running instance (`git archive` → tarball → scp → untar
   as root; no `.git`, no `node_modules`/`dist` — `git archive` only emits tracked files).
2. Repoint the on-appliance flake's `palmux.url` from `github:tjst-t/palmux2` to
   `path:/root/palmux2-local-src`, `nix flake update palmux` to re-lock.
3. Add ONE module directly in the on-appliance `flake.nix`'s own `modules` list (not a `./local`
   drop-in — drop-ins don't have the `palmux` flake input in scope) setting
   `services.palmux.package = palmux.packages.${system}.palmux2-local;`.
4. `nixos-rebuild switch --flake /persist/palmux/nixos#appliance` (same command the GUI/CLI
   `palmux-rebuild-update.service`, S673a42, runs — done here via direct root SSH since this is a
   throwaway verification VM without the polkit-authorized non-root palmux user wired up for a
   full onboarding flow; AC-S31ad96-2-2 explicitly allows either).
5. Verify `palmux2 --version` changed on the SAME instance.
6. Rollback path: `nixos-rebuild switch --rollback`, or repoint `palmux.url` back to `github:...`.

**PASS.**

## AC-S31ad96-2-2 — real qcow2 boot → nixos-rebuild switch → local-only change reflected, same instance

### 1. Boot a REAL RELEASED qcow2

```
$ gh release download v0.15.0 -R tjst-t/palmux2 -p 'palmuxos-v0.15.0.qcow2'
# -> palmuxos-v0.15.0.qcow2, 819M
$ qemu-img create -f qcow2 -F qcow2 -b palmuxos-v0.15.0.qcow2 overlay.qcow2
```

cloud-init seed with BOTH `palmux` (the appliance's real operator user) and `root` (added purely
for this throwaway verification VM, so `nixos-rebuild switch` could be run without first wiring up
a full onboarding/wheel-sudo flow — the appliance ships key-zero/password-off, so this required
adding our own key to `users: - name: root` in the seed; does not touch the real appliance's
`palmux` user permission model).

```
$ qemu-system-x86_64 -enable-kvm -cpu host -m 4096 -smp 2 \
    -drive file=overlay.qcow2,if=virtio,format=qcow2 \
    -drive file=seed.iso,if=virtio,format=raw \
    -netdev user,id=net0,hostfwd=tcp::12222-:22,hostfwd=tcp::17683-:7683 \
    -device virtio-net-pci,netdev=net0 \
    -nographic -serial file:serial.log -display none -pidfile qemu.pid
```

Boot + SSH reachable within ~5s (overlay image, already-booted-once base).

### 2. BEFORE — confirm the running release version

```
$ ssh -p 12222 palmux@localhost 'systemctl show palmux2 -p ExecStart'
ExecStart={ path=/nix/store/zic5snij1aysbn2by132az2rfgm504l9-palmux2-0.14.13/bin/palmux2 ; ... }

$ ssh -p 12222 palmux@localhost \
    '/nix/store/zic5snij1aysbn2by132az2rfgm504l9-palmux2-0.14.13/bin/palmux2 --version'
v0.14.13
```

(Noted as an out-of-scope finding below: the v0.15.0-tagged release's qcow2 asset embeds
`palmux2` v0.14.13, not v0.15.0 — the `chore(nix): bump palmux2 default to v0.15.0` commit landed
*after* the v0.15.0 tag. Irrelevant to this Story's AC — v0.14.13 is still a genuine, distinct
release version, exactly what we need as the "before" baseline — but worth a maintainer's
attention separately.)

`who -b` at this point: `system boot  2026-07-16 22:12`. `nix-env --list-generations -p
/nix/var/nix/profiles/system` at this point: 1 generation (`2026-07-14 00:51:49`, the image's
build-time closure).

### 3. Get local source into the instance + repoint the on-appliance flake

```
$ git archive --format=tar HEAD | gzip > palmux2-local-src.tar.gz   # 11M, worktree-S31ad96-2 @ commit c9cdf06
$ scp -P 12222 palmux2-local-src.tar.gz root@localhost:/root/
$ ssh -p 12222 root@localhost 'mkdir -p /root/palmux2-local-src && tar xzf /root/palmux2-local-src.tar.gz -C /root/palmux2-local-src'

$ ssh -p 12222 root@localhost \
    'sed -i "s#palmux.url = \"github:tjst-t/palmux2\";#palmux.url = \"path:/root/palmux2-local-src\";#" /persist/palmux/nixos/flake.nix'
$ ssh -p 12222 root@localhost 'cd /persist/palmux/nixos && nix flake update palmux'
# -> re-locked palmux + its transitive inputs (nixpkgs, nixpkgs-appliance, home-manager,
#    system-manager, nixos-generators, disko) against the local path
```

### 4. Add the package-override module (direct flake.nix edit, NOT a ./local dropin)

First attempt used a `./local/90-dev-local-source.nix` dropin with
`services.palmux.package = pkgs.palmux2-local;` — **failed** with:

```
error: builder for '.../palmux2-local-0.0.0-local-go-modules.drv' failed with exit code 1
> go: go.mod requires go >= 1.25.0 (running go 1.24.10; GOTOOLCHAIN=local)
```

Root cause + fix: see "Detour" section above. Rewrote `/persist/palmux/nixos/flake.nix` to add
`system = "x86_64-linux";` to the `let` block and one module in `nixosConfigurations.appliance.
modules`:

```nix
{ services.palmux.package = palmux.packages.${system}.palmux2-local; }
```

(First pass after this fix hit `error: undefined variable 'system'` — the module list is plain
attrsets, `system` has to be bound in the flake's own `let`, not implicitly available. Fixed by
adding `system = "x86_64-linux";` next to the existing `lib = nixpkgs.lib;` binding.)

### 5. `nixos-rebuild switch` — the actual update

```
$ ssh -p 12222 root@localhost 'cd /persist/palmux/nixos && nixos-rebuild switch --flake .#appliance'
```

Ran to completion inside the 4GB RAM / 2 vCPU VM (fetched Go module deps + npm deps, built
`palmux2-frontend-local` then `palmux2-local-0.0.0-local`, then the NixOS system closure):

```
building '/nix/store/...-palmux2-frontend-local.drv'...
building '/nix/store/...-palmux2-local-0.0.0-local-go-modules.drv'...
building '/nix/store/...-palmux2-local-0.0.0-local.drv'...
...
building '/nix/store/...-nixos-system-nixos-25.05.20260102.ac62194.drv'...
updating GRUB 2 menu...
stopping the following units: palmux2.service
activating the configuration...
starting the following units: palmux2.service
Done. The new configuration is /nix/store/s2azrgfbrp0rczrk6xja1785fgvy6dcc-nixos-system-nixos-25.05.20260102.ac62194
```

No reboot — `nixos-rebuild switch` restarted only `palmux2.service` (visible in the log:
"stopping the following units: palmux2.service" / "starting the following units:
palmux2.service"), exactly the generation-swap-without-reboot behavior the appliance design
relies on.

### 6. AFTER — same instance, confirm local source is now live

```
$ ssh -p 12222 palmux@localhost 'systemctl show palmux2 -p ExecStart'
ExecStart={ path=/nix/store/wxpv1dsqw95r2vx4myvwwkfx0scqg42r-palmux2-local-0.0.0-local/bin/palmux2 ; ... }

$ ssh -p 12222 palmux@localhost \
    '/nix/store/wxpv1dsqw95r2vx4myvwwkfx0scqg42r-palmux2-local-0.0.0-local/bin/palmux2 --version'
v0.0.0-local
```

**Before/after on the SAME running instance: `v0.14.13` → `v0.0.0-local`.** The store path itself
(`palmux2-local-0.0.0-local`, the exact derivation name from `nix/packages/palmux2-local.nix`)
independently confirms this is genuinely the local-source-build package, not a coincidentally-
labeled release build.

Same-instance (not a fresh boot) confirmed three ways:
```
$ ssh -p 12222 root@localhost 'who -b'
system boot  2026-07-16 22:12          # UNCHANGED from step 2 — no reboot occurred

$ ssh -p 12222 root@localhost 'cat /proc/sys/kernel/random/boot_id'
f49b304a-5552-496e-8e80-be7c6a50702e   # stable boot_id, single boot session throughout

$ ssh -p 12222 root@localhost 'nix-env --list-generations -p /nix/var/nix/profiles/system'
   1   2026-07-14 00:51:49
   2   2026-07-16 22:29:32   (current)   # generation 2, created during THIS session, no reboot
```

Additional corroboration — the served frontend bundle changed to a fresh local build and (as an
extra, stronger check than the version string alone) contains a real marker from this repo's
history:

```
$ ssh -p 12222 palmux@localhost 'curl -s http://localhost:7683/ | grep -oP "index-[A-Za-z0-9_-]+\.js"'
index-Cz4dINXQ.js   # DIFFERENT from the pre-switch release bundle's index-BX-lkkZs.js

$ ssh -p 12222 palmux@localhost \
    'curl -s http://localhost:7683/assets/index-Cz4dINXQ.js | grep -o "drawer-host-gh-hint"'
drawer-host-gh-hint
```

(`drawer-host-gh-hint` is S61c9a6-4's `data-testid`, the same real already-merged marker
S31ad96-1's verification used — chosen again here for consistency. Notably the asset filename
`index-Cz4dINXQ.js` is IDENTICAL to the one S31ad96-1's verification produced on a different day,
different host, different build — Vite's content-hashed filenames are deterministic, so two
independent builds of the same source tree producing the same hash is itself a small additional
confirmation of a genuine, reproducible local-source build rather than a fluke.)

**PASS.**

## AC-S31ad96-2-3 — CLAUDE.md updated

Added a new paragraph to CLAUDE.md's existing "palmuxOS アプライアンス (qcow2) をローカルで評価する"
section (after the "未リリースの変更を検証したい場合" / local-build-of-qcow2 paragraph), covering
this release→local-source in-place update flow end-to-end, including the overlay pitfall and its
fix so the next person doesn't have to re-discover the Go-version mismatch. **PASS.**

## Cleanup

- qemu process killed (`kill $(cat qemu.pid)`), confirmed gone via `ps`.
- `~/s31ad96-2-appliance-test/` (overlay qcow2, seed ISO, downloaded base qcow2, logs, local
  source tarball) removed entirely from the dev box.
- `df -h /` on the dev box returned to its pre-existing baseline (no net growth left behind).
- No changes made to any persistent/shared infrastructure — the entire verification ran inside a
  disposable qemu VM (COW overlay over a freshly-downloaded, unmodified release qcow2) that was
  destroyed at the end.

## Out-of-scope / not done here

- The v0.15.0 release qcow2 asset embedding `palmux2` v0.14.13 (rather than v0.15.0) is a real gap
  in the release process's version-bump ordering relative to the qcow2 CI job — worth a
  maintainer's separate look, but did not block this Story (v0.14.13 is still a valid, distinct
  "before" baseline).
- `examples/onappliance-flake/` (the docs-referenced copy of the on-appliance flake structure)
  has drifted from the actual shipped `nixos/appliance-flake/` (different `outputs` shape,
  `hardware-configuration.nix` vs. `hardware-base.nix`/`grub-device.nix`, different description
  text) — noticed while reading the code for this Story, not touched here since it's a pre-
  existing docs/code divergence unrelated to S31ad96-2's scope.
- Did not exercise the GUI-driven `palmux-rebuild-update.service` (S673a42) path specifically —
  used direct root SSH `nixos-rebuild switch` instead, which AC-S31ad96-2-2 explicitly allows
  ("または既存のGUI更新キック機構"). The GUI path runs the identical `nixos-rebuild switch --flake
  /persist/palmux/nixos#appliance` command as a systemd oneshot; nothing about this Story's
  flake-input/package-override mechanism is specific to how that command gets invoked.
- Did not test rollback (`nixos-rebuild switch --rollback`) on this VM — documented in CLAUDE.md
  as the revert path but not independently re-verified here (rollback itself is exercised
  elsewhere, e.g. Sb14caa's own verification).

## Concerns

None. The mechanism established here (`palmux.packages.${system}.palmux2-local` referenced
directly in the on-appliance flake's own outputs, bypassing the appliance's pinned nixpkgs) is
robust to future nixpkgs-appliance bumps catching up to Go 1.25+ eventually — at that point the
overlay-based `pkgs.palmux2-local` route would also start working, but the direct-reference route
documented here will keep working regardless, so no follow-up is required for correctness.

## New user-observable surface

None. This Story is dev tooling + documentation only: no new package, module, flag, endpoint, or
UI surface. The one repo code change explored (exposing `palmux2-local` via `overlays.default`)
was reverted after proving unnecessary for the actual working procedure.
