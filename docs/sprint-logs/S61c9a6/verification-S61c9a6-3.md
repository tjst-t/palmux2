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

## Out of scope / follow-ups

- `linux-arm64`'s pinned hash is manifest-derived only, not independently re-downloaded and
  verified (this Story and the appliance target `x86_64-linux` only). Re-verify by download
  before an aarch64 appliance ever ships.
- Claude Code's own in-app auto-update mechanism will not be able to write into
  `~/.local/share/claude/versions/<v>` when that path is a symlink into the read-only Nix
  store (by design — version bumps go through `nix/packages/claude-code.nix` +
  `nixos-rebuild switch`, matching how this whole appliance treats every other component).
  Not addressed here; no existing behavior assumed otherwise.
- `palmux-ws` image auto-install (S61c9a6-2) is a separate Story; `palmux runtime doctor`
  correctly reporting `palmux-ws image: NOT found` on this fresh boot is expected, not a
  regression.
