# /etc/palmux/local/example.nix
#
# Example operator drop-in. Copy/rename and edit. Anything NixOS supports is
# available here; palmux's own settings are mkDefault so a plain assignment wins.
#
# Apply with:  sudo nixos-rebuild switch --flake /etc/palmux#appliance
{ pkgs, ... }:
{
  # ── your palmux settings (override palmux mkDefaults) ──────────────────────
  services.palmux.domain = "dev.example.net";
  # services.palmux.bindAddr = "127.0.0.1:9000";

  # ── your own packages / services — full NixOS surface ──────────────────────
  environment.systemPackages = with pkgs; [ htop ripgrep ];
  # programs.neovim.enable = true;
  # services.tailscale.enable = true;
  # networking.firewall.allowedTCPPorts = [ 1234 ];

  # ── your users (in addition to the palmux operator) ────────────────────────
  # users.users.alice = {
  #   isNormalUser = true;
  #   openssh.authorizedKeys.keys = [ "ssh-ed25519 AAAA... alice" ];
  # };
}
