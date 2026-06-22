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
let
  pUser = config.services.palmux.user;
  # The palmux user's PRIMARY GROUP. services.palmux sets isNormalUser without an
  # explicit `group`, so NixOS defaults it to "users" (gid 100) — there is no
  # "palmux" group. tmpfiles rules must chown to this real group, not to `${pUser}`
  # (which would be an unresolvable group name and make the rule fail).
  pGroup = config.users.users.${pUser}.group;

  # ── the OPERATOR CONFIG BUNDLE (Sb14caa, docs/nixos-appliance-design.md) ────
  # Everything the operator sets (vs the immutable PalmuxOS core = the Nix modules
  # in the image) lives as a SEPARATED, backup-/restore-able file set on /persist:
  #
  #   /persist/palmux/config/        ← the bundle (git/GitHub-friendly, no secrets)
  #     ├─ nixos/  *.nix             declarative drop-ins (domain, extra pkgs, …)
  #     └─ app/    config.toml       palmux2 app/server settings + settings.json
  #   /persist/palmux/secrets.env    ← SECRETS (CF token / SSO secret / bcrypt) — NOT
  #                                    git-plaintext; back up encrypted / restore apart
  #   /persist/palmux/home/          ← DATA (~/ghq, ~/.claude) — separate, large
  #
  # Keeping config/ a single self-contained dir is what makes "back up the config,
  # restore it on a fresh appliance, store it in git" a one-directory operation.
  cfgBundle = "/persist/palmux/config";
  dropinDir = "${cfgBundle}/nixos"; # operator NixOS drop-ins, injected by the on-appliance flake
  appCfgDir = "${cfgBundle}/app";   # palmux2 --config-dir (config.toml + settings.json)
in
{
  imports = [ ./palmux.nix ];

  # ── palmux defaults suitable for an appliance ──────────────────────────────
  services.palmux.enable = lib.mkDefault true;
  services.palmux.stateDir = lib.mkDefault "/persist/palmux/home";   # DATA
  services.palmux.configDir = lib.mkDefault appCfgDir;               # operator config bundle
  services.palmux.secretsFile = lib.mkDefault "/persist/palmux/secrets.env";

  # ── immutable image / mutable state split ──────────────────────────────────
  # All durable user data lives on /persist — a SEPARATE volume the operator
  # attaches (labelled "persist"). The image itself is disposable: rebuild/swap it
  # and /persist (repos, ~/.claude, config, secrets, operator drop-ins) survives.
  # `nofail` so the appliance still boots to fix things if /persist isn't attached
  # yet; the palmux service (stateDir under /persist) just runs degraded until it is.
  fileSystems."/persist" = {
    device = lib.mkDefault "/dev/disk/by-label/persist";
    fsType = lib.mkDefault "ext4";
    options = [ "nofail" "x-systemd.device-timeout=10s" ];
  };
  # Ensure the state subtree exists on /persist (the palmux user's home, the
  # secrets file, and the operator drop-in dir). ~/ghq and ~/.claude live under
  # the home → automatically on /persist.
  systemd.tmpfiles.rules = [
    "d /persist/palmux 0755 root root -"
    "d /persist/palmux/home 0700 ${pUser} ${pGroup} -"                 # DATA
    "d ${cfgBundle} 0755 root root -"                                  # operator config bundle
    "d ${dropinDir} 0755 root root -"                                  # *.nix drop-ins (root reads at rebuild)
    "d ${appCfgDir} 0750 ${pUser} ${pGroup} -"                         # config.toml (palmux2 writes via deploy API)
    "f /persist/palmux/secrets.env 0600 ${pUser} ${pGroup} -"         # SECRETS
  ];

  system.stateVersion = lib.mkDefault "25.05";

  # ── generation-based upgrades (replaces unattended-upgrades + self-update) ──
  system.autoUpgrade = {
    enable = lib.mkDefault false; # opt-in; appliance updates are operator-driven
    flake = lib.mkDefault "/etc/palmux";
  };

  # ── login / access — NEVER bake an author/operator key into the image ──────
  # SECURITY: a distributed appliance image MUST ship with ZERO baked SSH keys /
  # passwords. Baking the author's pubkey here would be a backdoor into every
  # deployed PalmuxOS. Access is provisioned by the DEPLOYER at first boot:
  #   1. cloud-init (primary, Proxmox-native): the deployer attaches their own
  #      SSH pubkey via the platform's cloud-init drive → injected on first boot.
  #   2. palmux first-boot onboarding/claim (web): the operator claims the
  #      instance (password / SSO secret / optional key) before anything is
  #      exposed (extends the Sa53137 onboarding wizard).
  #   3. source/flake users put THEIR OWN key in THEIR OWN flake (examples/user-flake).
  # The operator's own key is later layered via /etc/palmux/local/*.nix — AFTER
  # they have claimed access. This module deliberately sets NO authorizedKeys.
  services.openssh.enable = lib.mkDefault true;
  services.openssh.settings.PasswordAuthentication = lib.mkDefault false;
  services.cloud-init.enable = lib.mkDefault true; # first-boot key/user injection from the platform

  # This module sets NO authorizedKeys — that is the whole point. The deployer's
  # key is provisioned at first boot (cloud-init / onboarding) or layered in their
  # own /etc/palmux/local AFTER claiming. So a deployed+customized appliance WILL
  # have the deployer's key; the *distributed image build* must not. That "no
  # baked keys in the shipped artifact" invariant is enforced by an image-build CI
  # check (grep the built image's authorized_keys), NOT by an eval assertion —
  # an assertion can't tell the shipped-image build from an operator's rebuild and
  # would wrongly break legitimate operator customization. TODO(stage3): add the
  # image-build no-baked-keys CI check.

  # NOTE: the OPERATOR DROP-IN import is wired in the ON-APPLIANCE flake
  # (examples/onappliance-flake/flake.nix), which does:
  #     imports = [ palmux.nixosModules.appliance ]
  #            ++ lib.filesystem.listFilesRecursive ./local;
  # where the flake's ./local is a symlink to ${dropinDir}
  # (/persist/palmux/config/nixos) — the drop-in slice of the operator config
  # bundle. Fragments there are merged with the full NixOS surface + override
  # palmux's mkDefaults, and travel with the bundle on backup/restore.
}
