# nixos/hosts/testbox/configuration.nix
#
# The Sb14caa test host. Minimal NixOS + the palmux layer, with the author's
# GitHub keys for SSH. Iterate on it with:
#   sudo nixos-rebuild switch --flake .#testbox      (run ON the box), or
#   nixos-rebuild switch --flake .#testbox --target-host root@<ip>   (remote)
{ config, lib, pkgs, modulesPath, authorizedKeys, ... }:
{
  # Proxmox virtio guest drivers (disk/net) in initrd.
  imports = [ (modulesPath + "/profiles/qemu-guest.nix") ];

  # ── boot (Proxmox) ─────────────────────────────────────────────────────────
  # This VM is SeaBIOS (legacy) → GRUB. disko's EF02 partition already sets
  # boot.loader.grub.devices, so DON'T set grub.device here (duplicates
  # mirroredBoots). For UEFI (OVMF) VMs use systemd-boot + an ESP instead.
  boot.loader.grub.enable = true;
  boot.loader.grub.efiSupport = false;

  networking.hostName = "palmux-testbox";
  # Static IP. IMPORTANT: the NIC name must match the VM. Proxmox virtio is
  # typically `ens18` — VERIFY with `ip link` on the install ISO before relying on
  # this; a wrong name = no network after reboot (recover via the Proxmox console).
  # If the NIC is not ens18, either change the name below, or use the
  # name-independent systemd-networkd block at the bottom of this comment.
  networking.useDHCP = false;
  networking.interfaces.ens18.ipv4.addresses = [
    { address = "192.168.1.44"; prefixLength = 24; }
  ];
  # Real LAN gateway is .254 (verified from the working host; 192.168.1.1 does not
  # answer ARP on this segment). The LAN resolver is 192.168.0.254; public DNS
  # works too once routed via .254.
  networking.defaultGateway = "192.168.1.254";
  networking.nameservers = [ "1.1.1.1" "9.9.9.9" ];
  # Name-independent alternative (drop the three options above and use this if the
  # NIC name is unknown/variable):
  #   systemd.network.enable = true;
  #   systemd.network.networks."10-lan" = {
  #     matchConfig.Name = "en*";
  #     address = [ "192.168.1.44/24" ];
  #     routes = [ { Gateway = "192.168.1.254"; } ];
  #     networkConfig.DNS = [ "1.1.1.1" "9.9.9.9" ];
  #   };
  time.timeZone = "Asia/Tokyo";

  # ── SSH access (author's GitHub keys; this is a personal, non-shipped host) ──
  services.openssh.enable = true;
  services.openssh.settings.PasswordAuthentication = false;
  users.users.root.openssh.authorizedKeys.keys = authorizedKeys;

  # ── palmux ─────────────────────────────────────────────────────────────────
  services.palmux.enable = true;
  # Also let the GitHub keys into the palmux operator user (created by the module).
  users.users.${config.services.palmux.user}.openssh.authorizedKeys.keys = authorizedKeys;

  # Stage 1 = module parity WITHOUT incus first; flip to true for Stage 2.
  services.palmux.incus.enable = lib.mkDefault false;
  # Local-only until you have a real domain + Cloudflare token for SSO/wildcard.
  # For SSO testing set: services.palmux.domain = "testbox.example.net";
  services.palmux.domain = lib.mkDefault null;

  environment.systemPackages = with pkgs; [ git tmux htop ];

  system.stateVersion = "25.05";
}
