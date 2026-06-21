# nixos/hosts/testbox/disko.nix
#
# Declarative partitioning for the Proxmox test VM (disko applies this during
# nixos-anywhere, wiping the disk). Matches the actual test VM verified on
# 192.168.1.44: SeaBIOS (legacy boot) + a VirtIO SCSI / SATA disk at /dev/sda.
#
# Layout: GPT with a 1M BIOS-boot partition (EF02, for legacy GRUB) + ext4 root.
# disko wires boot.loader.grub.devices from the EF02 partition — so do NOT also
# set boot.loader.grub.device in configuration.nix (that duplicates mirroredBoots).
#
# If your VM instead uses UEFI (OVMF) + VirtIO Block (/dev/vda), switch to an ESP
# (size 512M, type EF00, vfat /boot) + systemd-boot — see git history.
#
# /persist (the appliance's mutable-state volume) is NOT created here — the test
# box for Stage 1-2 doesn't need it. Add a second disk + a /persist entry when
# validating the appliance image (Stage 3).
{
  disko.devices.disk.main = {
    type = "disk";
    device = "/dev/sda"; # VirtIO SCSI / SATA (this VM); /dev/vda for VirtIO Block
    content = {
      type = "gpt";
      partitions = {
        boot = {
          size = "1M";
          type = "EF02"; # BIOS boot partition for legacy GRUB (SeaBIOS)
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
