{
  # This is the flake that ships at /etc/palmux on the appliance's PERSISTENT
  # volume. It is how an operator extends a deployed appliance:
  #
  #   1. drop a declarative fragment:
  #        sudo $EDITOR /etc/palmux/local/my-extras.nix
  #   2. apply it:
  #        sudo nixos-rebuild switch --flake /etc/palmux#appliance
  #
  # Everything under ./local is imported with the FULL NixOS option surface and
  # merged over palmux's mkDefaults. ./local is a symlink to the drop-in slice of
  # the OPERATOR CONFIG BUNDLE (/persist/palmux/config/nixos), so it survives image
  # swaps/upgrades and travels with the bundle on backup/restore. No --impure:
  # ./local is part of this flake's source. (The other bundle slices —
  # config/app/config.toml and the separate secrets.env — are read by palmux2, not
  # imported here.) See docs/nixos-appliance-design.md §運用者コンフィグ束.
  description = "palmux appliance — operator config (extend via ./local/*.nix)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    # Pin the palmux appliance modules. On a real appliance this is pinned to the
    # shipped version; `nixos-rebuild` after a version bump picks up new defaults.
    palmux.url = "github:tjst-t/palmux2";
  };

  outputs = { self, nixpkgs, palmux, ... }:
    let system = "x86_64-linux"; in {
      nixosConfigurations.appliance = nixpkgs.lib.nixosSystem {
        inherit system;
        modules =
          [
            { nixpkgs.overlays = [ palmux.overlays.default ]; }
            palmux.nixosModules.appliance
            # hardware-configuration.nix is generated on first boot of the image.
            ./hardware-configuration.nix
          ]
          # ── operator drop-ins: every *.nix under ./local is merged in ──────
          ++ nixpkgs.lib.filesystem.listFilesRecursive ./local;
      };
    };
}
