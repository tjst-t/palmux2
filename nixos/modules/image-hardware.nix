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
  #
  # S52fc2c-3 note: this mkDefault value is a no-op in the image-build context — disko's
  # make-disk-image.nix ALSO sets boot.loader.grub.devices via lib.mkForce (priority 50)
  # to the build VM's disk (/dev/vda, derived by prepareDiskoConfig scanning the EF02
  # bios partition in disko-layout.nix), and mkForce wins over this mkDefault. So gen1's
  # SEALED closure bakes grub.devices = ["/dev/vda"]. We deliberately do NOT try to
  # mkForce ["/dev/sda"] here: for a `listOf`, an equal-priority (mkForce/50) definition
  # would MERGE with disko's rather than replace it, yielding ["/dev/vda" "/dev/sda"] →
  # grub-install /dev/sda fails inside the QEMU build VM (no such device) →
  # `nix build .#appliance-qcow2` breaks. The correct fix lives at runtime:
  # palmux-state-init re-installs GRUB to the actual detected disk on first boot
  # (appliance.nix, S52fc2c-3), and gen 2+ use the first-boot-generated grub-device.nix.
  # The only residual limitation is that `nixos-rebuild switch --rollback` to gen1 (the
  # sealed image closure) prints an rc=1 from grub-install /dev/vda on virtio-scsi hosts
  # — harmless, the MBR planted at first boot stays valid and the box boots fine
  # (accepted inherent disko limitation: gen1's closure is sealed at image-build time).
  boot.loader.grub.device = lib.mkDefault "/dev/sda";
  boot.loader.timeout = lib.mkDefault 1;
  boot.kernelParams = [ "console=ttyS0" ];
}
