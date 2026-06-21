{
  description = "palmuxOS — NixOS modules + appliance for palmux2";

  # NOTE (stage 0 scaffold): this flake imports the palmux2 package derivation from
  # ../nix/packages/palmux2.nix. That relative path is ABOVE this flake dir, so in a
  # pure flake eval it must be in-tree — the canonical home for this is a REPO-ROOT
  # flake. TODO(stage1): decide root-flake-at-repo-root vs keeping nixos/ a subdir
  # and either move this up or vendor the package expression. Not eval-checked yet.

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    nixos-generators = {
      url = "github:nix-community/nixos-generators";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, nixos-generators, ... }:
    let
      system = "x86_64-linux";

      # Overlay that exposes the palmux2 release-asset package as pkgs.palmux2.
      palmuxOverlay = final: prev: {
        palmux2 = final.callPackage ../nix/packages/palmux2.nix { };
      };

      pkgs = import nixpkgs {
        inherit system;
        overlays = [ palmuxOverlay ];
      };
    in {
      overlays.default = palmuxOverlay;

      # ── reusable modules (the extensibility surface) ──────────────────────
      nixosModules = {
        palmux = ./modules/palmux.nix;        # the host module: services.palmux.*
        appliance = ./modules/appliance.nix;  # appliance: state split + drop-in hook
        default = ./modules/appliance.nix;
      };

      # ── a buildable example appliance system ──────────────────────────────
      # Operators normally use the ON-APPLIANCE flake (examples/onappliance-flake)
      # which adds ./local drop-ins; this one is the minimal reference build.
      nixosConfigurations.palmux-appliance = nixpkgs.lib.nixosSystem {
        inherit system;
        modules = [
          { nixpkgs.overlays = [ palmuxOverlay ]; }
          self.nixosModules.appliance
          # TODO(stage3): hardware/disk + /persist fileSystems for the target.
          ({ ... }: {
            services.palmux.domain = nixpkgs.lib.mkDefault null; # local-only by default
            system.stateVersion = "25.05";
          })
        ];
      };

      # ── appliance image (immutable) ───────────────────────────────────────
      # Same config, built as a disposable image. State lives on /persist.
      packages.${system} = {
        appliance-qcow2 = nixos-generators.nixosGenerate {
          inherit system pkgs;
          format = "qcow"; # also: "iso", "amazon", "sd-aarch64", ...
          modules = [
            { nixpkgs.overlays = [ palmuxOverlay ]; }
            self.nixosModules.appliance
            ({ ... }: { system.stateVersion = "25.05"; })
          ];
        };
      };
    };
}
