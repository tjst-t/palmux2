# Static hardware base for the single-disk appliance, matching what the qcow2 image
# was built with: virtio guest, an ext4 root found by LABEL=nixos that auto-resizes
# to fill the disk (so `qm resize` + reboot grows it), BIOS/GRUB, serial console.
# The actual bootsector disk (e.g. /dev/sda on virtio-scsi, /dev/vda on virtio-blk)
# is supplied separately by grub-device.nix, generated on first boot.
{ modulesPath, lib, ... }:
{
  imports = [ "${modulesPath}/profiles/qemu-guest.nix" ];

  fileSystems."/" = {
    device = "/dev/disk/by-label/nixos";
    fsType = "ext4";
    autoResize = true;
  };
  boot.growPartition = true;

  boot.loader.grub.enable = lib.mkDefault true;
  boot.loader.grub.efiSupport = lib.mkDefault false;
  boot.loader.timeout = lib.mkDefault 1;
  boot.kernelParams = [ "console=ttyS0" ];
}
