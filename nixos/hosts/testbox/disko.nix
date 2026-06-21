# nixos/hosts/testbox/disko.nix
#
# Declarative partitioning for the Proxmox test VM (disko applies this during
# nixos-anywhere, wiping the disk). Single disk, UEFI: ESP + ext4 root.
#
# Device name depends on the Proxmox disk bus:
#   - VirtIO Block (virtio0)        → /dev/vda   (default below)
#   - VirtIO SCSI  (scsi0)          → /dev/sda
# Set the bus when creating the VM, or change `device` here to match.
#
# /persist (the appliance's mutable-state volume) is NOT created here — the test
# box for Stage 1-2 doesn't need it. Add a second disk + a /persist entry when
# validating the appliance image (Stage 3).
{
  disko.devices.disk.main = {
    type = "disk";
    device = "/dev/vda"; # ← /dev/sda if the VM uses VirtIO SCSI
    content = {
      type = "gpt";
      partitions = {
        ESP = {
          size = "512M";
          type = "EF00";
          content = {
            type = "filesystem";
            format = "vfat";
            mountpoint = "/boot";
            mountOptions = [ "umask=0077" ];
          };
        };
        root = {
          size = "100%";
          content = {
            type = "filesystem";
            format = "ext4";
            mountpoint = "/";
          };
        };
      };
    };
  };
}
