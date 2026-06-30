# S52fc2c Nix Implementation Log

Sprint: S52fc2c — NixOS appliance stability (persist.mount restart + grub.device mismatch)

## Files Changed

| File | Change |
|------|--------|
| `nixos/modules/appliance.nix` | S52fc2c-2: added `environment.etc` drop-in for `X-StopOnReconfiguration = false` on `persist.mount`; S52fc2c-3: added `grub2` to `palmux-state-init` path and a bus-agnostic `grub-install` step on first boot |
| `nixos/modules/image-hardware.nix` | S52fc2c-3: replaced `boot.loader.grub.device = lib.mkDefault "/dev/sda"` with `boot.loader.grub.device = lib.mkForce "nodev"` + `boot.loader.grub.devices = lib.mkForce [ "/dev/sda" ]` |

---

## Story S52fc2c-2: persist.mount restart on nixos-rebuild switch

### Root Cause

NixOS 25.05 uses `switch-to-configuration-ng` (Rust rewrite, default since 24.11) to apply generation changes. When the `persist.mount` unit definition differs between old and new generations (e.g. `autoResize` flag added, `disko-layout.nix` tweaked), the tool detects the change and tries to `stop → restart` the unit.

`/persist` is always busy while the system runs (home directories, config, incus storage, and the on-appliance flake itself all live there). The unmount fails:

```
umount: /persist: target is busy
```

`nixos-rebuild switch` returns rc=1 even though everything else succeeded — the binary was updated, the boot entry was installed, palmux2 is running correctly. The onboarding wizard's `palmux-rebuild.service` polls the exit code, so rc=1 falsely shows "更新失敗".

### Mechanism chosen: `X-StopOnReconfiguration = false` via systemd drop-in

`X-StopOnReconfiguration` is a NixOS-specific extension to the systemd unit `[Unit]` section. When set to `false`, `switch-to-configuration-ng` skips stopping/restarting the unit even if its definition changed between generations.

This is the correct tool for the job:
- `neededForBoot = true` was considered but rejected: it mounts `/persist` in initrd, but `switch-to-configuration-ng` still sees the `persist.mount` unit as changed and tries to restart it. Does not address the root cause.
- `systemd.units."persist.mount"` override was considered but rejected: `fileSystems."/persist"` auto-generates an entry in `systemd.units."persist.mount"` with `text = "..."`. Merging another `text` from our module causes a module-system conflict (the `text` attribute is `types.str`, not `types.lines`). Using `asDropin` without a conflicting `text` might work but the merge semantics are implementation-defined.
- `environment.etc` drop-in (chosen): creates `/etc/systemd/system/persist.mount.d/palmux-no-restart-on-switch.conf` in a generation-specific path that `switch-to-configuration-ng` reads when checking `X-StopOnReconfiguration`. No conflict with the auto-generated unit.

**File added (appliance.nix, before `palmux-grow-persist`):**
```nix
environment.etc."systemd/system/persist.mount.d/palmux-no-restart-on-switch.conf".text = ''
  [Unit]
  X-StopOnReconfiguration = false
'';
```

### Scope

Applies to both the image-build generation (gen1) and all on-appliance generations (gen2+) since the change is in the shared `appliance` module. Affects all deployments where `fileSystems."/persist"` is present (i.e., every palmuxOS appliance).

---

## Story S52fc2c-3: grub.device mismatch (gen1 bakes /dev/vda, deployed disk is /dev/sda)

### Root Cause

`disko`'s `diskoImages` build runs a QEMU VM to partition, format, and install NixOS into the raw image. In that VM, the disk appears as `/dev/vda` (virtio-blk, QEMU's default disk interface). Disko internally overrides `disko.devices.disk.main.device` — and consequently `boot.loader.grub.devices` — to `/dev/vda` for its image-build evaluation context.

Result: gen1's NixOS closure has `boot.loader.grub.devices = ["/dev/vda"]`. The image is bootable (disko uses its own partitioning step to install GRUB into the MBR using the actual build device, independently of the NixOS `installBootLoader` config). But when a gen1 activation runs (e.g. `nixos-rebuild switch --rollback` to gen1), the generated `installBootLoader` script calls:

```
grub-install /dev/vda
```

On Proxmox virtio-scsi targets the disk is at `/dev/sda` — `/dev/vda` does not exist:

```
grub-install: cannot find a GRUB drive for /dev/vda
```

`nixos-rebuild switch --rollback` returns rc=1. On Proxmox virtio-blk targets (disk = `/dev/vda`), rollback succeeds because the baked device happens to match.

### Fix Part 1: image-hardware.nix — prevent /dev/vda from being baked into gen1

`image-hardware.nix` is included ONLY in the `flake.nix` image-build config (not in the on-appliance flake). Using `lib.mkForce` (priority 50) overrides disko's internal override (plain assignment, priority 100):

```nix
boot.loader.grub.device = lib.mkForce "nodev";    # defer to the devices list
boot.loader.grub.devices = lib.mkForce [ "/dev/sda" ]; # Proxmox virtio-scsi default
```

`grub.device = "nodev"` suppresses the single-device code path; `grub.devices` (non-empty list) is used exclusively. The NixOS grub module assertion (`devices != [] || device != "nodev"`) is satisfied because `devices = ["/dev/sda"]` is non-empty.

**Critical assumption**: Disko's physical GRUB MBR installation is done independently of the NixOS `installBootLoader` config option (using the actual loop/QEMU build device). If this assumption is wrong and disko DOES use the NixOS `installBootLoader` to install GRUB, the image build would try `grub-install /dev/sda` inside a QEMU VM where only `/dev/vda` exists → image build would fail. The orchestrator must verify the image build succeeds after this change.

The on-appliance flake (gen2+) is NOT affected — it uses `hardware-base.nix` + `grub-device.nix` (generated on first boot), which completely replaces `image-hardware.nix`.

### Fix Part 2: appliance.nix — bus-agnostic grub-install on first boot

Added to `palmux-state-init` (runs once, on first boot, inside the existing `if [ ! -e ${flakeDir}/grub-device.nix ]` block):

```bash
if [ -b "$disk" ]; then
  grub-install --no-floppy "$disk" 2>&1 \
    || echo "palmux-state-init: grub-install $disk returned non-zero (non-fatal, gen2+ will use correct grub-device.nix)" >&2
fi
```

This detects the ACTUAL disk at runtime (the parent block device of the root partition) and physically re-installs GRUB to it. This works regardless of the bus type (virtio-scsi = `/dev/sda`, virtio-blk = `/dev/vda`) and provides a complementary safety net for:
- Deployments on virtio-blk targets (where Part 1's `/dev/sda` would still be wrong in gen1 without the heal)
- Future image builds that might accidentally bake in a wrong device

After this runs, GRUB is correctly installed. Gen2+ use `grub-device.nix` (seeded by the same first-boot run) which correctly declares the detected disk. Any subsequent `nixos-rebuild switch --rollback` to gen1 would call `grub-install /dev/sda` (from the Part 1 fix) which succeeds on virtio-scsi.

`grub2` was added to `palmux-state-init`'s `path` list.

### Scope

- Part 1 (`image-hardware.nix`): affects only the NEXT image build. Existing deployed gen1 is not changed (already on disk); the Part 2 first-boot heal covers this case.
- Part 2 (`appliance.nix`): affects all appliances on first boot (or next boot if the grub-device.nix check passes), both gen1 and via on-appliance flake.

---

## Real-Machine Verification Steps (for orchestrator on 192.168.1.45)

### AC-S52fc2c-2-1 (persist.mount not restarted)

1. SSH into the appliance: `ssh ubuntu@192.168.1.45`
2. Check that the drop-in file exists in the new generation: `cat /etc/systemd/system/persist.mount.d/palmux-no-restart-on-switch.conf`
3. Run a generation switch that causes `persist.mount` to be "changed" (if no existing change, make a benign edit to `disko-layout.nix` in the on-appliance flake, then `nixos-rebuild switch --flake .#appliance`):
   ```bash
   nixos-rebuild switch --flake /persist/palmux/nixos#appliance 2>&1 | tail -30
   echo "rc=$?"
   ```
4. Expected: `rc=0`, no `umount: /persist: target is busy` in output, `/persist` remains mounted.

### AC-S52fc2c-2-2 (palmux-rebuild.service succeeds)

1. Trigger via GUI (Deploy panel → 適用) or API: `systemctl start palmux-rebuild.service && sleep 30 && systemctl status palmux-rebuild.service`
2. Expected: `ActiveState=inactive ExecMainStatus=0`, no "更新失敗" in the frontend.

### AC-S52fc2c-3-1 (new image build: gen1 does not bake /dev/vda)

1. On a builder machine with Nix + KVM: `nix build .#appliance-qcow2`
2. Confirm image build succeeds (rc=0).
3. Deploy to a fresh Proxmox VM (virtio-scsi, disk=/dev/sda): `qm importdisk ...`
4. Boot the VM. After first boot, check: `cat /etc/nixos/nixos-rebuild.log` or `systemctl status palmux-state-init`.
5. In the on-appliance flake: `cat /persist/palmux/nixos/grub-device.nix` — should show `/dev/sda`.
6. Check gen1's grub config: `nixos-rebuild list-generations` → find gen1, check `boot.loader.grub.devices` by inspecting the gen1 closure: `nix eval --impure --expr '(import /run/booted-system/nixpkgs {}).config.boot.loader.grub.devices'` (approximate).

### AC-S52fc2c-3-2 (rollback to gen1 does not fail grub-install)

1. After the first `nixos-rebuild switch` creates gen2, roll back: `nixos-rebuild switch --rollback`
2. Expected: `rc=0`, no `grub-install: cannot find a GRUB drive` error.
3. Confirm GRUB is still installed: `grub-install --recheck /dev/sda; echo "rc=$?"` (should succeed).

**Note on the two-step safety**: If Part 1 (`mkForce ["/dev/sda"]`) breaks the image build (i.e., disko DOES call the NixOS `installBootLoader` and `/dev/sda` doesn't exist in the QEMU build VM), revert `image-hardware.nix` to the old `lib.mkDefault "/dev/sda"` line and rely solely on Part 2 (the first-boot `grub-install` heal) for the virtio-scsi rollback fix. Part 2 alone fixes the problem for all deployments after first boot; it only leaves a window where rolling back to gen1 BEFORE the first `nixos-rebuild switch` (i.e., immediately after imaging) would still fail on virtio-scsi.
