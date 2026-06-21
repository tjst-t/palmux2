# nixos/modules/appliance.nix
#
# Appliance specifics layered on top of modules/palmux.nix:
#   - immutable image + mutable state on a separate /persist volume
#   - the OPERATOR DROP-IN extensibility hook (see docs/nixos-appliance-design.md §2.2)
#   - generation-based upgrades
#
# The operator extends the appliance by dropping *.nix fragments into the flake's
# ./local/ dir (which lives on /persist) and running `nixos-rebuild switch`. The
# import is flake-PURE because ./local is part of the on-appliance flake's source
# tree (examples/onappliance-flake/), not an out-of-tree /etc read.
#
# NOTE: scaffold — not yet eval-checked. TODO(stageN) markers per the design doc.
{ config, lib, pkgs, ... }:
{
  imports = [ ./palmux.nix ];

  # ── palmux defaults suitable for an appliance ──────────────────────────────
  services.palmux.enable = lib.mkDefault true;
  services.palmux.stateDir = lib.mkDefault "/persist/palmux/home";
  services.palmux.secretsFile = lib.mkDefault "/persist/palmux/secrets.env";

  # ── immutable image / mutable state split ──────────────────────────────────
  # All durable user data lives on /persist (a separate volume), bind-mounted into
  # place so the image stays disposable. (TODO(stage3): the actual fileSystems for
  # /persist depend on the generator target / disk layout — qcow2 vs cloud vs bare.)
  systemd.tmpfiles.rules = [
    "d /persist/palmux 0755 root root -"
    "d /persist/palmux/home 0700 ${config.services.palmux.user} ${config.services.palmux.user} -"
    "d /persist/palmux/nixos-local 0755 root root -"
    "f /persist/palmux/secrets.env 0600 ${config.services.palmux.user} ${config.services.palmux.user} -"
  ];
  # ~/ghq and ~/.claude live under the bind-mounted home → already on /persist.

  # ── generation-based upgrades (replaces unattended-upgrades + self-update) ──
  system.autoUpgrade = {
    enable = lib.mkDefault false; # opt-in; appliance updates are operator-driven
    flake = lib.mkDefault "/etc/palmux";
  };

  # ── login / access ─────────────────────────────────────────────────────────
  services.openssh.enable = lib.mkDefault true;

  # NOTE: the OPERATOR DROP-IN import is wired in the ON-APPLIANCE flake
  # (examples/onappliance-flake/flake.nix), which does:
  #     imports = [ palmux.nixosModules.appliance ]
  #            ++ lib.filesystem.listFilesRecursive ./local;
  # so fragments in /persist/palmux/nixos-local (symlinked as the flake's ./local)
  # are merged with full NixOS surface + override palmux's mkDefaults.
}
