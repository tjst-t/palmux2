# S52fc2c Nix Implementation Log

Sprint: S52fc2c — NixOS appliance stability (persist.mount restart + grub.device mismatch)

> This log reflects the **post-review (NEEDS-WORK)** design. The first pass used
> `X-StopOnReconfiguration` (Story 2) and `lib.mkForce` grub override (Story 3); the
> reviewer demonstrated from `switch-to-configuration-ng` and disko `make-disk-image.nix`
> source that both were wrong. The mechanisms below are the corrected ones.

## Files Changed

| File | Change |
|------|--------|
| `nixos/modules/appliance.nix` | S52fc2c-2: wrapped the `nixos-rebuild switch` in `palmux-rebuild.service` with a `run_switch` shell function that absorbs a persist.mount-only restart failure when palmux2 is healthy. S52fc2c-3: `grub2` added to `palmux-state-init` path + first-boot `grub-install` MBR heal on the detected disk. |
| `nixos/modules/image-hardware.nix` | S52fc2c-3: reverted to the original single `boot.loader.grub.device = lib.mkDefault "/dev/sda";` line + a comment explaining why a `mkForce` override would break the image build. |

---

## Story S52fc2c-2: false "更新失敗" from persist.mount restart

### Root cause (confirmed from switch-to-configuration-ng source)

NixOS 25.05 uses `switch-to-configuration-ng` (the Rust rewrite). Its `handle_modified_unit`
**unconditionally** places any changed `.mount` unit into `units_to_restart` — the only
exceptions are `-.mount` (`/`) and `nix.mount`, which are reload-only. There is **no**
config flag to suppress this:
- `X-StopOnReconfiguration` is checked **only for `.target` units** (a dependency-ordering
  bookkeeping concern); it has no effect on `.mount` units.
- WORSE: `X-StopOnReconfiguration` is **not** in the unit-section ignore list, so adding it
  as a `persist.mount` drop-in makes the unit compare-unequal on the first activation,
  which would *trigger* the very restart it was meant to suppress. (This is why the first-pass
  drop-in approach was removed.)

When `persist.mount`'s generated unit changes between generations (the first image→flake
switch; any `disko-layout.nix` change), the switch tries to stop→start it. `/persist` is
always busy (home, config, incus storage, the on-appliance flake all live there), so the
umount fails (`target is busy`) and `nixos-rebuild switch` exits non-zero — even though the
switch otherwise fully applied (new system profile activated, boot entry installed, palmux2
running the new closure).

`palmux2`'s `POST /api/deploy/rebuild` and the onboarding wizard `handleRebuild` poll the
`palmux-rebuild.service` exit code, so a non-zero rc shows a false "更新失敗".

### Mechanism chosen: exit-code wrapper at the `palmux-rebuild.service` layer

Since `nixos-rebuild` itself cannot be made to return 0 without patching
switch-to-configuration-ng (out of scope), the fix is at the UX/exit-code layer — the place
the GUI actually polls. A `run_switch()` shell function wraps the switch:

1. Run `nixos-rebuild switch --flake .#appliance`, capturing combined output to a temp log
   and the exit code (`set +e` around it so `set -eu` doesn't abort).
2. `rc == 0` → success.
3. `rc != 0` → override to success **only if BOTH**:
   - `systemctl is-active --quiet palmux2.service` (the new closure is actually running), AND
   - the `the following units failed: ...` summary lists **exactly** `persist.mount`
     (extracted + normalised to space-separated tokens, compared to `"persist.mount"`).
   On override it logs a clear note ("persist.mount could not be remounted while in use —
   expected and harmless; /persist stays mounted") and returns 0.
4. Otherwise propagate the real non-zero exit (build error, a different unit failed, palmux2
   down → all still fail correctly).

**Why not also gate on a `target is busy` string?** That detail is emitted to the journal by
the mount unit, not reliably to `nixos-rebuild`'s captured stdout/stderr (systemctl prints
"Job for persist.mount failed … see journalctl"). Requiring it would re-introduce false
"更新失敗" via a false-negative. On a running appliance `/persist` mounted cleanly at boot and
its FS is valid, so a persist.mount *restart* can only fail because /persist is in use —
exactly the harmless case. The exact-failed-units match (only persist.mount) + palmux2-healthy
is the conservative, precise signal for "no other unit failed".

`run_switch` replaces both `nixos-rebuild switch` calls in the unit: the primary apply and the
domain-rollback re-apply (the latter as `run_switch || true` since that path reports the
domain failure with `exit 1` regardless).

`palmux-rebuild.service` already has `pkgs.coreutils`, `pkgs.gnugrep`, `pkgs.gnused`,
`pkgs.systemd` in its `path`, so `mktemp`/`cat`/`tr`/`rm`/`grep`/`sed`/`systemctl` all resolve.

### Revised AC framing

**AC-S52fc2c-2-3** is satisfied when the GUI / `palmux-rebuild.service` update path does NOT
report a false "更新失敗" for a persist.mount-only restart failure while palmux2 is healthy —
NOT by making `nixos-rebuild` itself return 0 (which would require patching
switch-to-configuration-ng, out of scope).

### Note on frequency

Routine binary-only updates do NOT change `persist.mount`'s generated unit, so they never hit
this path. The rc=1 only occurs on the image→flake first switch or a disko-layout change. The
wrapper makes the UX robust regardless.

---

## Story S52fc2c-3: grub.device mismatch (gen1 bakes /dev/vda)

### Root cause (confirmed from disko make-disk-image.nix)

disko's image builder (`diskoImages` → `make-disk-image.nix`) runs a QEMU VM to partition,
format, and install. In that VM the disk is `/dev/vda` (virtio-blk). disko's
`prepareDiskoConfig` scans the `EF02` (BIOS-boot) partition declared in `disko-layout.nix` and
sets `boot.loader.grub.devices` with **`lib.mkForce`** (priority 50) to the build VM's disk
(`/dev/vda`). So gen1's **sealed** closure bakes `grub.devices = ["/dev/vda"]`.

On a Proxmox virtio-scsi target the actual disk is `/dev/sda`, so a gen1 activation
(`nixos-rebuild switch --rollback` to gen1) calls `grub-install /dev/vda` → fails:
`grub-install: cannot find a GRUB drive for /dev/vda`.

### Why the first-pass `mkForce ["/dev/sda"]` was WRONG (reverted)

`boot.loader.grub.devices` is a `listOf`. disko sets it with `mkForce` (priority 50). My
first-pass `mkForce [ "/dev/sda" ]` is **also** priority 50, and for a `listOf` equal-priority
definitions **merge** rather than replace → `["/dev/vda" "/dev/sda"]` → during the image build
`grub-install /dev/sda` fails inside the QEMU VM (no such device) → `nix build .#appliance-qcow2`
**breaks**. (My earlier assumption that disko used plain-assignment priority 100 was incorrect.)

So `image-hardware.nix` is **reverted** to the original single line:
```nix
boot.loader.grub.device = lib.mkDefault "/dev/sda";
```
`mkDefault` (priority 1000) is a harmless no-op in the build context — disko's `mkForce` wins —
and does not break the build. A comment documents the trap so it isn't re-attempted.

### Fix kept: first-boot grub-install MBR heal (appliance.nix, Part 2)

`palmux-state-init` (oneshot, runs once on first boot, inside the existing
`if [ ! -e grub-device.nix ]` block) now:
- detects the actual boot disk (`lsblk -no pkname` of the root partition's source),
- writes `grub-device.nix` pinning that disk (existing behaviour, for gen 2+), and
- **physically re-installs GRUB to the detected disk**: `grub-install --no-floppy "$disk"`
  (non-fatal — logs on failure). `grub2` was added to the service `path`.

This plants a correct MBR on the real disk regardless of bus type (sda or vda). Forward
generations (gen 2+) install GRUB correctly because they use the first-boot-generated
`grub-device.nix`. The reviewer approved this as the correct bus-agnostic heal.

### Accepted residual limitation

`nixos-rebuild switch --rollback` to **gen1** (the sealed image closure, which still bakes
`/dev/vda`) prints rc=1 from `grub-install /dev/vda` on a virtio-scsi host. This is **harmless**:
the MBR planted at first boot stays valid and the system boots correctly. This is an inherent
disko limitation — gen1's closure is sealed at image-build time and cannot be corrected
post-hoc without breaking the build. Accepted.

---

## Verification

### Syntax / build

- `nix-instantiate --parse` and `nix build .#appliance-qcow2` **could not be run** in this
  worktree environment: there is no Nix store (`/nix` absent), no `nix`/`nix-instantiate`
  binary, and no `/dev/kvm`. The Story-3 build-verify (the key gate that the
  `image-hardware.nix` revert lets `nix build .#appliance-qcow2` build) **must be run by the
  orchestrator on a Nix builder with KVM**.
- Manual review performed: Nix antiquotation (`${appCfgDir}`/`${dropinDir}`/`${flakeDir}` are
  intended; no shell `${var}` that Nix would misparse — all shell vars use `$x`/`$(...)`),
  shell function syntax under `set -eu` (the `set +e`/`set -e` bracketing around the switch is
  correct), and `path` completeness for the new commands.

### Orchestrator: build gate (Story 3)

```bash
nix build .#appliance-qcow2 --print-build-logs   # MUST succeed (was the failure mode)
```

### Orchestrator: real-machine (deferred — do NOT run from this agent)

AC-S52fc2c-2-3 (no false "更新失敗"):
1. On an appliance whose `persist.mount` unit will differ in the next generation (first
   image→flake switch, or make a benign `disko-layout.nix` edit in the on-appliance flake):
   `systemctl start palmux-rebuild.service && sleep 60 && systemctl status palmux-rebuild.service`
2. Expected: `ExecMainStatus=0` (unit succeeds), the journal shows the "persist.mount could
   not be remounted while in use … Treating the rebuild as SUCCESS (S52fc2c-2)" note, `/persist`
   stays mounted, and the GUI/onboarding wizard shows success (not "更新失敗").
3. Negative check: introduce a genuine build error (or a second failing unit) and confirm
   `palmux-rebuild.service` still **fails** (ExecMainStatus != 0) — the override must not mask
   real failures.

AC-S52fc2c-3 (grub heal):
1. `nix build .#appliance-qcow2`, deploy to a fresh Proxmox VM (virtio-scsi, disk=/dev/sda).
2. After first boot: `cat /persist/palmux/nixos/grub-device.nix` → `/dev/sda`;
   `journalctl -u palmux-state-init` shows the grub-install ran.
3. `nixos-rebuild switch --flake /persist/palmux/nixos#appliance` (creates gen2) → rc handled by
   `run_switch`; then `nixos-rebuild switch --rollback` to gen2/gen1.
4. Rollback to a flake-generated gen (gen2+) installs grub to `/dev/sda` and succeeds. Rollback
   to gen1 may print the documented harmless rc=1 (`grub-install /dev/vda`); confirm the box
   still boots from the first-boot-planted MBR.
