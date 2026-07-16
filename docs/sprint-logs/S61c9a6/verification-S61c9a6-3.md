# S61c9a6-3 verification log

Story: 開発者として、フレッシュインストールのpalmuxOSアプライアンスでもclaude CLIが使えるようにしたい。
なぜなら、migration前提の設計だと真っさらな新規導入者はclaudeタブを一度も使えないから。

Branch: `worktree-S61c9a6-3-v2` (fresh worktree cut off `autopilot/main/S61c9a6`; a prior
attempt at this Story on a different branch was blocked and abandoned — this is a clean
restart, not a continuation of that branch).

## Revised mechanism (user decision, supersedes the original task text)

A prior attempt baked Anthropic's `curl | sh` install script into a systemd oneshot that
would run automatically, unattended, as root, on every fresh boot of the distributed
appliance image. This was correctly blocked by the harness's permission system as an
unauthorized "execute unvetted network-fetched code as root, unattended, on every deployed
box" pattern. The user's explicit, corrected decision: package Claude Code as a proper Nix
derivation, fetched and hash-pinned **at build time**, following the exact same pattern this
repo already uses for palmux2 itself (`nix/packages/palmux2.nix`).

## How the pinned URL + hash were derived

1. Read (never executed) Anthropic's public installer:
   ```
   curl -fsSL https://claude.ai/install.sh -o /tmp/claude-install.sh
   ```
   This resolves to a stable download-manifest based script. Key facts extracted by reading
   it (verbatim excerpts):
   - `DOWNLOAD_BASE_URL="https://downloads.claude.ai/claude-code-releases"`
   - Version resolution: `curl -fsSL "$DOWNLOAD_BASE_URL/latest"` (returns a bare version
     string, e.g. `2.1.211`)
   - Per-platform binary download: `"$DOWNLOAD_BASE_URL/$version/$platform/claude"` where
     `platform` is `<os>-<arch>` (`linux-x64`, `linux-arm64`, `linux-x64-musl`,
     `linux-arm64-musl`, `darwin-x64`, `darwin-arm64`, `win32-x64`, `win32-arm64`) — glibc
     Linux hosts get the plain `linux-<arch>` variant (musl only if
     `/lib/libc.musl-*.so.1` exists or `ldd` reports musl), which matches this appliance
     (NixOS + `nix-ld`, glibc dynamic linking already assumed elsewhere in this codebase).
   - Checksum verification: `"$DOWNLOAD_BASE_URL/$version/manifest.json"` publishes a plain
     sha256 **hex** checksum per platform (`.platforms["<platform>"].checksum`); the script
     `sha256sum`s the downloaded binary and compares before running it.
   - After download+verify, the script runs `"$binary_path" install` to self-install the
     versioned layout (`~/.local/share/claude/versions/<v>/claude` +
     `~/.local/bin/claude` symlink) — **this "install" step is what our Nix derivation does
     NOT do** (it is a `$HOME`-relative runtime side effect; `$HOME` doesn't exist at Nix
     build time). Instead `nixos/modules/appliance.nix`'s new
     `palmux-claude-bootstrap` oneshot reproduces just that on-disk layout at boot,
     symlinking straight into the already-fetched Nix store binary.

2. Resolved the current version (2026-07-16):
   ```
   $ curl -fsSL https://downloads.claude.ai/claude-code-releases/latest
   2.1.211
   ```

3. Fetched the manifest and its published checksums:
   ```
   $ curl -fsSL https://downloads.claude.ai/claude-code-releases/2.1.211/manifest.json
   { "version": "2.1.211", ...,
     "platforms": {
       "linux-x64":  { "binary": "claude", "checksum": "8272c8a474ac9ea1bc35f19b9f7c7e7dc4dc4eb6d5ad3e484b19335ac72446b2", "size": 262023992 },
       "linux-arm64":{ "binary": "claude", "checksum": "1fff7e8f947c07b19d10b1fbf714b7e547e9536253b9b58230d8adbc4624f867", "size": 258849520 },
       ... }}
   ```

4. Downloaded the linux-x64 artifact ONCE (this is the same class of action
   `nix-prefetch-url` performs — a one-time, reviewable fetch to compute a pin, not a
   `curl | sh` runtime execution) and verified it locally against the manifest before
   trusting it:
   ```
   $ curl -fsSL "https://downloads.claude.ai/claude-code-releases/2.1.211/linux-x64/claude" -o claude-2.1.211-linux-x64
   $ sha256sum claude-2.1.211-linux-x64
   8272c8a474ac9ea1bc35f19b9f7c7e7dc4dc4eb6d5ad3e484b19335ac72446b2  claude-2.1.211-linux-x64   # exact match to manifest
   $ file claude-2.1.211-linux-x64
   claude-2.1.211-linux-x64: ELF 64-bit LSB executable, x86-64, ... dynamically linked, interpreter /lib64/ld-linux-x86-64.so.2, ...
   ```

5. Converted the verified sha256 hex to Nix SRI form for `fetchurl { hash = ...; }`:
   ```python
   import base64
   base64.b64encode(bytes.fromhex("8272c8a474ac9ea1bc35f19b9f7c7e7dc4dc4eb6d5ad3e484b19335ac72446b2")).decode()
   # -> "gnLIpHSsnqG8NfGbn3x+fcTcTrbVrT5ISxkzWsckRrI="
   ```
   giving `hash = "sha256-gnLIpHSsnqG8NfGbn3x+fcTcTrbVrT5ISxkzWsckRrI=";` for `linux-x64`.
   `linux-arm64`'s hash (`sha256-H/9+j5R8B7GdELH79xS35UfpU2JTubWCMNitvEYk+Gc=`) was derived
   the same way from the manifest checksum but **not** independently re-downloaded/verified
   (this appliance targets `x86_64-linux` only, per `flake.nix`'s `supportedSystems` and the
   Story's own scope note) — re-verify by download before an aarch64 appliance ever ships.

Pinned in `nix/packages/claude-code.nix`:
- **version**: `2.1.211`
- **URL pattern**: `https://downloads.claude.ai/claude-code-releases/${version}/linux-${arch}/claude`
- **hash (linux-x64)**: `sha256-gnLIpHSsnqG8NfGbn3x+fcTcTrbVrT5ISxkzWsckRrI=`
- **hash (linux-arm64, manifest-derived only)**: `sha256-H/9+j5R8B7GdELH79xS35UfpU2JTubWCMNitvEYk+Gc=`

## Implementation

- `nix/packages/claude-code.nix` (new): `stdenv.mkDerivation` + `fetchurl`, modeled 1:1 on
  `nix/packages/palmux2.nix` (same `version`/`hash` override-hook shape, same arch-switch
  pattern). Places the fetched, checksum-verified binary at `$out/bin/claude` in the Nix
  store — does **not** run `claude install` (no `$HOME` at build time).
- `flake.nix`: exposes `packages.<system>.claude-code` and `overlays.default.claude-code`
  (same wiring shape as `palmux2`/`caddy-cloudflare`/`gwq`).
- `nixos/modules/appliance.nix`: new `systemd.services.palmux-claude-bootstrap` oneshot.
  - `after = [ "palmux-state-init.service" ]` (needs the `~/home/ubuntu` bind mount up),
    `wantedBy = [ "multi-user.target" ]`, **no** `requiredBy`/`before` on `palmux2.service`
    — best-effort, cannot wedge boot.
  - No network access needed at all (unlike the S61c9a6-2 image-install oneshot, a real
    ~1GB runtime download) — the binary is already in the Nix store from the build-time
    fetch, so this unit only symlinks.
  - **Idempotent + non-clobbering**: exits immediately (no-op) if `~/.local/bin/claude`
    already exists (file, symlink, or otherwise) — a migrated install is left untouched.
  - Symlinks (not copies) into the Nix store, so a future `nixos-rebuild switch` that bumps
    `nix/packages/claude-code.nix`'s pinned version is picked up automatically — same
    generation-swap property palmux2 itself has.

## Real-VM verification

### Build

Attempted 3 times on `palmux-nix-builder` (an incus VM ON the dev host) — every attempt hit
`qemu-system-x86_64: Could not access KVM kernel module: Permission denied` inside the inner
disko-image-build step, because that VM sits at **3 levels of nesting**
(Proxmox → dev-host VM → palmux-nix-builder VM → disko-build qemu), which this hardware's
AMD nested-SVM does not support reliably. Abandoned that host for this build (kept running
for other Sprint uses per the coordinator).

Rebuilt successfully on `deploy-test` (`ssh ubuntu@192.168.1.43`, a real Proxmox VM — only
**2 levels of nesting** total, `/dev/kvm` already `crw-rw-rw-`), in a dedicated
`~/build-s61c9a6-3/palmux2` checkout (NOT the shared `~/palmux2-build` — that directory had
another Story's uncommitted staged work in flight, confirmed via `git status` before
touching anything):

```
$ git clone --branch autopilot/main/S61c9a6 --depth 1 https://github.com/tjst-t/palmux2.git   # local clone, then bundle-fetched
$ git fetch <bundle> worktree-S61c9a6-3-v2:worktree-S61c9a6-3-v2 && git checkout worktree-S61c9a6-3-v2
$ . /nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh
$ nix build .#appliance-qcow2 -o /home/ubuntu/build-s61c9a6-3/result --extra-experimental-features "nix-command flakes"
```

Result: `result -> /nix/store/gd6nsa2ak94fdzhyf3zij9ib2zm1nacq-nixos-disko-images`,
`main.raw` present (18253611008 bytes nominal, 2.9G actual/sparse). `grep -i "kvm|tcg"
build.log` found nothing — real KVM acceleration, no fallback, no errors.

The build log also confirms the claude-code derivation ran and matched the pinned hash
(no hash-mismatch failure), and shows the `palmux-claude-bootstrap.service` unit building
successfully as part of the system closure.

### Convert + transfer

```
$ rsync -avS ubuntu@192.168.1.43:.../result/main.raw ./main.raw          # pulled to the dev host (192.168.1.40)
$ qemu-img convert -O qcow2 -c main.raw palmuxos-s61c9a6-3.qcow2         # 944635904 bytes / 897 MiB compressed (virtual 17G)
```

### Boot (fresh, no migration)

Followed this repo's documented recipe (CLAUDE.md § `palmuxOS アプライアンス (qcow2) を
ローカルで評価する`), on the dev host itself (has `/dev/kvm`, `cpu=host`):

```
$ qemu-img create -f qcow2 -F qcow2 -b palmuxos-s61c9a6-3.qcow2 overlay.qcow2
$ genisoimage -output seed.iso -volid cidata -joliet -rock user-data meta-data   # cloud-init, users: - name: palmux (NOT ubuntu)
$ qemu-system-x86_64 -enable-kvm -cpu host -m 4096 -smp 2 \
    -drive file=overlay.qcow2,if=virtio,format=qcow2 \
    -drive file=seed.iso,if=virtio,format=raw \
    -netdev user,id=net0,hostfwd=tcp::12223-:22,hostfwd=tcp::17684-:7683 \
    -device virtio-net-pci,netdev=net0 -nographic -serial file:serial.log -display none -pidfile qemu.pid
```

### [AC-S61c9a6-3-1] fresh-install bootstrap path — PASS

```
$ ssh -p 12223 palmux@localhost 'systemctl is-active palmux2 incus palmux-state-init palmux-claude-bootstrap'
active
active
active
active

$ ssh -p 12223 palmux@localhost 'systemctl status palmux-claude-bootstrap --no-pager'
● palmux-claude-bootstrap.service - Project the Nix-pinned Claude Code CLI into ~/.local for a fresh (non-migrated) install
     Active: active (exited) ...
   Main PID: 741 (code=exited, status=0/SUCCESS)

$ ssh -p 12223 palmux@localhost 'ls -la ~/.local/bin/claude; find ~/.local/share/claude/versions -maxdepth 2; readlink -f ~/.local/bin/claude'
lrwxrwxrwx 1 palmux users 56 ... /home/ubuntu/.local/bin/claude -> /home/ubuntu/.local/share/claude/versions/2.1.211/claude
/home/ubuntu/.local/share/claude/versions/
/home/ubuntu/.local/share/claude/versions/2.1.211
/home/ubuntu/.local/share/claude/versions/2.1.211/claude
/nix/store/clnf7sqfblfvxan1y25jzq6rk56fiqaw-claude-code-2.1.211/bin/claude
```

Symlink chain resolves exactly into the on-disk layout
`internal/runtime/incus/incus.go`'s bind-mounts and
`internal/tab/{claudeagent,claudetui}`'s `containerClaudeBin` constants assume, pointing at
the Nix-store-pinned binary.

### [AC-S61c9a6-3-2] Claude tab actually works on a fresh boot — PASS

Confirmed genuinely fresh (no prior `~/.claude` state at all — not even a directory):
```
$ ssh -p 12223 palmux@localhost 'ls -la ~/.claude ~/.claude.json'
ls: cannot access '/home/ubuntu/.claude': No such file or directory
ls: cannot access '/home/ubuntu/.claude.json': No such file or directory
```

PATH resolution (login shell, same lookup path the host-runtime claude-agent/claude-tui
spawn uses):
```
$ ssh -p 12223 palmux@localhost 'bash -lc "which claude; claude --version"'
/home/ubuntu/.local/bin/claude
2.1.211 (Claude Code)
```

Interactively launched `claude` (via `script` + a `pexpect` driver against an `ssh -tt`
session, to capture the real TUI rather than just a version string) and captured its
genuine first-run screen:
```
Welcome to Claude Code v2.1.211
Let's get started.
Choose the text style that looks best with your terminal
To change this later, run /theme
  1. Auto (match terminal)
> 2. Dark mode (default)
  3. Light mode
  ...
```
This is Claude Code's real first-run onboarding wizard — the entry point that necessarily
precedes login/auth on a config-less install — proving the process starts correctly and
reaches its normal auth-bound flow, not a crash/not-found/broken-symlink state. No
`~/.claude` directory existed beforehand (see above), so this is unambiguously the
fresh-install path, not a migrated one. (No process was left running afterward — confirmed
`ps aux | grep claude` empty on the VM after the driver exited.)

Also confirmed the surrounding app is healthy so this is representative of what the actual
Claude tab would do (host runtime, since the palmux-ws image isn't installed yet —
out of scope, that's S61c9a6-2):
```
$ curl -s -o /dev/null -w "HTTP %{http_code}\n" http://localhost:17684/
HTTP 200
$ ssh -p 12223 palmux@localhost 'bash -lc "palmux2 runtime doctor"'
palmux runtime doctor
  ✓ incus: Client version: 6.0.5
  ✓ incus-admin group: active on this process
  ✗ palmux-ws image: NOT found — run: palmux runtime install   # expected, S61c9a6-2 scope
  ✓ /etc/subuid: root:1000:1 present
  ✓ /etc/subgid: root:1000:1 present
  ✓ Docker: not running (no FORWARD conflict)
```

### Cleanup

- Local qemu process killed (`kill $(cat qemu.pid)`); overlay/seed/serial-log scratch files
  removed. The base qcow2 remains in this session's scratchpad only (ephemeral, not part of
  the repo).
- Dedicated build directories removed on both `deploy-test` (`~/build-s61c9a6-3`) and
  `palmux-nix-builder` (`/root/build-s61c9a6-3` and stray bundle files from the abandoned
  3-levels-of-nesting attempt). `palmux-nix-builder` itself left running (shared Sprint
  resource); the shared `~/palmux2-build` checkout on `deploy-test` was never touched.

## Post-review fixes

An independent code review of the first pass above found 2 real bugs, both fixed on the
same branch before merge.

### Bug 1 — the non-clobber guard permanently early-exited after the first successful boot

Original guard: `if [ -e "$target" ] || [ -L "$target" ]; then ... exit 0; fi`. This can't
tell a genuinely migrated install apart from the symlink the oneshot itself created on a
prior boot — so after the very first successful bootstrap, every future boot would see
`~/.local/bin/claude` already exist (because the unit itself put it there) and permanently
skip re-linking, forever, contradicting the adjacent comment's claim that a future
`nix/packages/claude-code.nix` version bump would "automatically" reach the appliance via
`nixos-rebuild switch`. It would not have — the unit would never touch the symlink again
once created.

**Fix** (`nixos/modules/appliance.nix`, `palmux-claude-bootstrap`): resolve the existing
target with `readlink -f` and only treat it as "migrated, leave alone" if it resolves
OUTSIDE this package's Nix store output. If it resolves INTO
`/nix/store/*-claude-code-*/bin/claude` (i.e. it's already our own prior link — to any
generation, old or current), fall through and re-link to whatever the CURRENT pinned
version's store path is. This mirrors the same "reconcile drift on every run" idea already
used by `palmux-incus-reconcile` earlier in this same file (the oneshot this Story's design
comment already cited as its own model, but hadn't actually followed for this specific
case).

**Dry-run verification** (no second appliance build needed — this is a pure bash guard,
verified by exercising the exact `case "$resolved" in ... esac` pattern copied out of the
unit script against representative resolved-path strings):

```
$ bash guard_dry_run.sh
PASS scenario A (fresh, no target -- ...) -> leave    # (see script: the real unit script
    never evaluates this case at all for an absent target -- it skips straight to
    install+ln unconditionally, which IS the correct "proceed to link" behavior; the case
    only gates what happens when something already exists at $target)
PASS scenario B (our own prior link, OLDER version 2.1.200) -> relink
PASS scenario B2 (our own prior link, CURRENT version 2.1.211) -> relink
PASS scenario C (migrated real binary, outside /nix/store) -> leave
PASS scenario D (unrelated /nix/store/*/bin/claude, not claude-code) -> leave

ALL SCENARIOS PASS
```
Script: `guard_dry_run.sh` (scratch, not committed to the repo — logic is inline in
`nixos/modules/appliance.nix` and this transcript is the record of having exercised it).

Scenario B is the one that actually reproduces the original bug (it FAILed against the
pre-fix guard logic — confirmed by running the same test before the fix was applied — and
PASSes against the fixed logic above). Scenario B2 additionally confirms the common
every-boot-after-the-first case (already-current version) is a harmless idempotent re-link,
not an error. Scenario C confirms the non-clobber contract for a genuine migration is still
intact after the fix. Real-VM re-verification of scenario A (fresh boot) is in
"[AC-S61c9a6-3-1/2] re-verification" below; scenarios B/B2 (an actual version bump on a real
booted appliance) were not additionally re-proven end-to-end on hardware — that would
require a second pinned version to bump to, which is unnecessary to prove this bash
conditional's correctness and was waived per the reviewer's own suggested scope
("reasoning through the fixed guard logic + maybe a local shell dry run ... is enough").

### Bug 2 — nothing disabled Claude Code's own auto-updater

The entire point of this Story is defeated if, the first time the bootstrapped `claude`
process gets network access, its own built-in auto-updater silently replaces the
checksum-verified Nix-store-pinned binary with something un-pinned and unverified —
reintroducing exactly the "unpinned code executes unattended" hole the original
runtime-`curl | sh` approach was rejected for, just one step later. The first pass's
verification doc claimed the Nix-store symlink alone prevented this; that claim was
**wrong** (see the strikethrough correction above) — only the single `claude` file is a
store symlink, the containing `versions/<v>/` directory is a plain writable directory.

**Research**: downloaded the same checksum-verified `claude` 2.1.211 binary already fetched
during the URL/hash derivation above and inspected it directly (`strings` + targeted
`grep`) for how it decides whether auto-update is disabled:
```
$ strings claude-2.1.211-linux-x64 | grep -o "DISABLE_AUTOUPDATER\|DISABLE_UPDATES" | sort -u
DISABLE_AUTOUPDATER
DISABLE_UPDATES
$ grep -a -o -P '.{0}DISABLE_AUTOUPDATER.{0}' claude-2.1.211-linux-x64  # (context excerpt)
...function h$e(){if(ye.DISABLE_UPDATES)return{type:"env",envVar:"DISABLE_UPDATES"};
   if(ye.DISABLE_AUTOUPDATER)return{type:"env",envVar:"DISABLE_AUTOUPDATER"}...
```
This is Claude Code's own internal "why is the auto-updater disabled" status resolver
(surfaced in its `/status`-style UI as `autoUpdaterDisabledReason`) — it checks the env vars
`DISABLE_UPDATES` and `DISABLE_AUTOUPDATER` (either one set disables it), plus a separate
`~/.claude.json` `"autoUpdates": false` setting. This is the real, currently-shipping
mechanism, not a guess — read directly out of the exact binary this Story pins.

**Fix** (`nixos/modules/appliance.nix`): set `DISABLE_AUTOUPDATER = "1"` in **two** places,
matching how `internal/tab/claudeagent/client.go` and `internal/tab/claudetui/daemon.go`
actually build the spawned claude process's environment (both start from `os.Environ()` —
i.e. palmux2's own process env — via `appendOrReplaceEnv`/`appendOrReplace`):
1. `systemd.services.palmux2.environment.DISABLE_AUTOUPDATER = "1"` — reaches every
   claude process the Claude tab spawns (host runtime, both agent and tui modes), since
   both inherit palmux2's process environment.
2. `environment.variables.DISABLE_AUTOUPDATER = "1"` — reaches a user manually running
   `claude` in an interactive shell (Bash/Host tab, SSH), which does not go through
   palmux2's environment at all.
(incus-container workspace claude has its own separate `incus exec --env` list —
S4d8b1c's scope, not touched here; out of scope for this Story since the fresh-install
bootstrap targets the host-runtime path this Story's ACs actually exercise.)

### [AC-S61c9a6-3-1/2] re-verification — PASS

Rebuilt on `deploy-test` from the same branch (now with both fixes) using the identical
recipe as the first pass (dedicated `~/build-s61c9a6-3-v2` checkout, real KVM, `nix build
.#appliance-qcow2 -o ...`). Converted + transferred + booted fresh (no migration) exactly as
before.

```
$ ssh -p 12223 palmux@localhost 'systemctl is-active palmux2 incus palmux-state-init palmux-claude-bootstrap'
active
active
active
active

$ ssh -p 12223 palmux@localhost 'readlink -f ~/.local/bin/claude'
/nix/store/clnf7sqfblfvxan1y25jzq6rk56fiqaw-claude-code-2.1.211/bin/claude   # (1) symlink correctly resolves into the Nix store on a fresh boot

$ ssh -p 12223 palmux@localhost 'bash -lc "claude --version"'
2.1.211 (Claude Code)

$ ssh -p 12223 palmux@localhost 'systemctl show palmux2 -p Environment'
Environment=... DISABLE_AUTOUPDATER=1 ...        # (3) present in the palmux2 service env

$ ssh -p 12223 palmux@localhost 'bash -lc "echo DISABLE_AUTOUPDATER=\$DISABLE_AUTOUPDATER"'
DISABLE_AUTOUPDATER=1                             # (3) present in an interactive login shell too

$ ssh -p 12223 palmux@localhost 'bash -lc "claude doctor 2>&1" | grep -i -A1 "auto.?update"'
Auto-updater: disabled (env: DISABLE_AUTOUPDATER)  # claude's OWN doctor confirms it sees the env var and honors it
```
(2) — re-linking on a hypothetical version bump — is covered by the dry-run above per the
reviewer's own stated scope; not re-proven with a second real pinned version on this boot.

## Out of scope / follow-ups

- `linux-arm64`'s pinned hash is manifest-derived only, not independently re-downloaded and
  verified (this Story and the appliance target `x86_64-linux` only). Re-verify by download
  before an aarch64 appliance ever ships.
- ~~Claude Code's own in-app auto-update mechanism will not be able to write into
  `~/.local/share/claude/versions/<v>` when that path is a symlink into the read-only Nix
  store~~ — **this claim was wrong and has been corrected; see "Post-review fixes" below.**
  Only the single `claude` file inside `versions/<v>/` is a store symlink;
  `versions/<v>/` itself is a plain, `${pUser}`-owned, writable directory (created by
  `install -d`), so nothing at the filesystem level stopped Claude's own auto-updater from
  writing into it. `DISABLE_AUTOUPDATER` is now set explicitly instead of relying on this
  non-existent protection.
- `palmux-ws` image auto-install (S61c9a6-2) is a separate Story; `palmux runtime doctor`
  correctly reporting `palmux-ws image: NOT found` on this fresh boot is expected, not a
  regression.
