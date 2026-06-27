# Declarative 2-partition layout for the palmuxOS appliance image, baked at image
# BUILD time (via disko) so the deployer never has to partition anything:
#
#   bios   (1M)        GRUB BIOS-boot
#   root   (16G fixed) /         ← Nix store + OS. autoResize OFF → stays bounded.
#   persist(rest,last) /persist  ← repos/~ (home), config, secrets, incus storage.
#                                   autoResize ON. MUST be the last partition so a
#                                   later `qm resize` grows it (growpart extends the
#                                   last partition into the new free space).
#
# Result: growing the VM disk grows ONLY /persist — user data expands while the OS
# root can never be filled by a runaway clone / container / build. Single disk, but
# the immutable-OS / mutable-state split is physical, and transparent to the
# deployer (it ships pre-partitioned in the image).
{ lib, ... }:
{
  disko.devices.disk.main = {
    type = "disk";
    # Seed image total size: bios(1M) + root(16G) + persist(rest). disko defaults to
    # a tiny 2G image which can't hold the 16G root, so set it explicitly. The qcow2
    # compresses the empty space, so the DISTRIBUTED file stays small; persist is a
    # ~1G seed that autoResizes to fill the disk on deploy.
    imageSize = "17G";
    # Nominal for the image build (disko writes an image file, not this device).
    # The running system finds its filesystems by LABEL (nixos / persist), so the
    # actual bus (sda/vda) at deploy time doesn't matter.
    device = lib.mkDefault "/dev/sda";
    content = {
      type = "gpt";
      partitions = {
        bios = {
          size = "1M";
          type = "EF02"; # BIOS boot partition so GRUB can embed on a GPT disk
        };
        root = {
          size = "16G";
          content = {
            type = "filesystem";
            format = "ext4";
            mountpoint = "/";
            extraArgs = [ "-L" "nixos" ];
          };
        };
        persist = {
          size = "100%"; # rest of the disk — keep LAST for grow-on-resize
          content = {
            type = "filesystem";
            format = "ext4";
            mountpoint = "/persist";
            extraArgs = [ "-L" "persist" ];
          };
        };
      };
    };
  };

  # Only /persist grows when the disk grows; root is fixed at 16G. The PARTITION is
  # enlarged by the palmux-grow-persist oneshot (appliance.nix); autoResize then
  # grows the ext4 FS to fill it. (boot.growPartition only handles root, so it's not
  # used here.)
  fileSystems."/persist".autoResize = true;
}
