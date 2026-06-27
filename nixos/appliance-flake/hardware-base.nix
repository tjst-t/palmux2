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
  fileSystems."/persist" = {
    device = "/dev/disk/by-label/persist";
    fsType = "ext4";
    autoResize = true;
  };

  boot.loader.grub.enable = lib.mkDefault true;
  boot.loader.grub.efiSupport = lib.mkDefault false;
  boot.loader.timeout = lib.mkDefault 1;
  boot.kernelParams = [ "console=ttyS0" ];
}
