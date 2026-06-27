# Hardware bits for the disko-built appliance IMAGE: virtio guest, BIOS/GRUB on the
# whole disk, serial console. Filesystems are NOT declared here — disko-layout.nix
# provides them (root + /persist by label). Counterpart of appliance-flake/
# hardware-base.nix, which hand-declares the same filesystems for the running system
# (gen 2+, the on-appliance flake) without pulling in disko at runtime.
{ modulesPath, lib, ... }:
{
  imports = [ "${modulesPath}/profiles/qemu-guest.nix" ];

  boot.loader.grub.enable = lib.mkDefault true;
  boot.loader.grub.efiSupport = lib.mkDefault false;
  # GRUB installs to the whole disk (embeds in the BIOS-boot partition disko made).
  boot.loader.grub.device = lib.mkDefault "/dev/sda";
  boot.loader.timeout = lib.mkDefault 1;
  boot.kernelParams = [ "console=ttyS0" ];
}
