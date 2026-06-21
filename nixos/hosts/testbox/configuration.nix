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
  networking.useDHCP = lib.mkDefault true;
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
