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
  # S52fc2c-3: Override disko's build-time device substitution so gen1 (the image-
  # baked NixOS generation) uses the correct DEPLOYED device rather than the QEMU
  # image-build VM's device.
  #
  # Root cause: disko's diskoImages runs a QEMU VM where the disk appears as /dev/vda
  # (virtio-blk, QEMU's default). Disko internally overrides disko.devices.disk.main.device
  # (and therefore boot.loader.grub.devices) to /dev/vda for its image-build evaluation
  # context, so gen1's closure has grub.devices = ["/dev/vda"]. On Proxmox virtio-scsi
  # targets (disk = /dev/sda) rolling back to gen1 then fails:
  #   grub-install: cannot find a GRUB drive for /dev/vda
  #
  # lib.mkForce (priority 50, higher than disko's plain-assignment priority 100) overrides
  # disko's /dev/vda with the actual deployed-target device.
  #
  # Disko's PHYSICAL GRUB install into the MBR is done via its own build-time
  # partitioning step (calling grub-install on the actual loop/QEMU device), which is
  # INDEPENDENT of this NixOS config option. The image therefore boots correctly on any
  # bus type. This setting only affects what `grub-install` is called with during
  # `nixos-rebuild switch` activations (including rollbacks to gen1).
  #
  # grub.device = "nodev" suppresses the single-device code path; grub.devices (the list)
  # is used exclusively — satisfying the NixOS grub module assertion (devices != []).
  #
  # NOTE: if deploying to virtio-blk targets (disk = /dev/vda, not /dev/sda), either
  #   (a) change /dev/sda to /dev/vda here before building the image, OR
  #   (b) rely on palmux-state-init's first-boot grub-install heal (appliance.nix,
  #       S52fc2c-3) which detects the actual disk regardless of what gen1 baked in.
  # The appliance's on-box flake (gen 2+) uses the first-boot-generated grub-device.nix
  # and is not affected by this setting.
  boot.loader.grub.device = lib.mkForce "nodev";    # defer to the devices list
  boot.loader.grub.devices = lib.mkForce [ "/dev/sda" ]; # Proxmox virtio-scsi default
  boot.loader.timeout = lib.mkDefault 1;
  boot.kernelParams = [ "console=ttyS0" ];
}
