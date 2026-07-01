# Static hardware base for the running appliance (generation 2+, driven by the
# on-appliance flake) — and the CI build. It hand-declares the SAME two filesystems
# the disko-built image created (so it does NOT pull disko in at runtime):
#
#   /        ext4  LABEL=nixos    (16G, fixed — Nix store + OS; NOT autoResized)
#   /persist ext4  LABEL=persist  (last partition; autoResized so `qm resize` grows
#                                   user data: ~ (home), repos, config, incus storage)
#
# The actual bootsector disk (e.g. /dev/sda on virtio-scsi, /dev/vda on virtio-blk)
# is supplied separately by grub-device.nix, generated on first boot.
{ modulesPath, lib, ... }:
{
  imports = [ "${modulesPath}/profiles/qemu-guest.nix" ];

  # Root is FIXED (no autoResize) so a growing disk doesn't expand the OS partition.
  fileSystems."/" = {
    device = "/dev/disk/by-label/nixos";
    fsType = "ext4";
  };

  # /persist is the LAST partition and the ONLY one that grows. The palmux-grow-persist
  # oneshot (appliance.nix) enlarges the partition after a `qm resize`; autoResize
  # then grows the ext4 FS to fill it.
  #
  # S52fc2c-2: reference /persist by its PARTLABEL (disk-main-persist), NOT its fs
  # LABEL, because that is exactly what the disko-built IMAGE bakes into gen1
  # (`fileSystems."/persist".device = /dev/disk/by-partlabel/disk-main-persist`). This
  # on-box file and the image's disko-layout.nix each declare /persist separately, so
  # if they use DIFFERENT device ids the FIRST image→flake `nixos-rebuild switch` sees
  # persist.mount's What= change → switch-to-configuration-ng UNCONDITIONALLY restarts
  # the changed .mount (only / and /nix are reload-only, hardcoded in its src/main.rs)
  # → but /persist is always in use, so the umount fails "target is busy" and the switch
  # exits non-zero even though it fully applied → the update UX shows a false "更新失敗".
  # Matching the image's by-partlabel id makes persist.mount byte-identical across
  # generations, so switch-to-configuration never restarts it → `nixos-rebuild switch`
  # returns rc=0 NATIVELY. by-partlabel/disk-main-persist and by-label/persist point at
  # the same partition (verified on a real appliance: both → sda3), so this is a pure
  # id-stabilization with no behavior change. (root `/` stays by-label: `-.mount` is
  # reload-only so its image→flake ref change reloads harmlessly and never fails.)
  fileSystems."/persist" = {
    device = "/dev/disk/by-partlabel/disk-main-persist";
    fsType = "ext4";
    autoResize = true;
  };

  boot.loader.grub.enable = lib.mkDefault true;
  boot.loader.grub.efiSupport = lib.mkDefault false;
  boot.loader.timeout = lib.mkDefault 1;
  boot.kernelParams = [ "console=ttyS0" ];
}
