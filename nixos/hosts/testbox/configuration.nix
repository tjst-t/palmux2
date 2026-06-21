# nixos/hosts/testbox/configuration.nix
#
# The Sb14caa test host. Minimal NixOS + the palmux layer, with the author's
# GitHub keys for SSH. Iterate on it with:
#   sudo nixos-rebuild switch --flake .#testbox      (run ON the box), or
#   nixos-rebuild switch --flake .#testbox --target-host root@<ip>   (remote)
{ config, lib, pkgs, authorizedKeys, ... }:
{
  # ── boot (Proxmox) ─────────────────────────────────────────────────────────
  # Assumes UEFI (Proxmox VM → BIOS = OVMF). For SeaBIOS/legacy, switch to GRUB
  # + a bios_grub partition in disko.nix.
  boot.loader.systemd-boot.enable = true;
  boot.loader.efi.canTouchEfiVariables = true;

  networking.hostName = "palmux-testbox";
  # DHCP by default (matches the minimal ISO). For a stable homelab address,
  # easiest is a DHCP reservation by MAC on the router/Proxmox — then leave this
  # as-is. To pin a static IP in-config instead, comment useDHCP and set the
  # interface block below (find the NIC name with `ip link`; Proxmox virtio is
  # typically ens18):
  networking.useDHCP = lib.mkDefault true;
  # networking.useDHCP = false;
  # networking.interfaces.ens18.ipv4.addresses = [
  #   { address = "192.168.1.50"; prefixLength = 24; }
  # ];
  # networking.defaultGateway = "192.168.1.1";
  # networking.nameservers = [ "1.1.1.1" "9.9.9.9" ];
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
