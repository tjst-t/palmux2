{
  description = "palmux2 — Nix flake for declarative Ubuntu/NixOS deployment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    home-manager = {
      url = "github:nix-community/home-manager";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    system-manager = {
      url = "github:numtide/system-manager";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # palmuxOS appliance image generator (Sb14caa Stage 3). Additive — only the
    # appliance-qcow2 package consumes it; the install.sh / home-manager path is
    # untouched.
    nixos-generators = {
      url = "github:nix-community/nixos-generators";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    inputs@{ self
    , nixpkgs
    , home-manager
    , system-manager
    , nixos-generators
    , ...
    }:
    let
      supportedSystems = [ "x86_64-linux" "aarch64-linux" ];
      forEachSystem = f:
        nixpkgs.lib.genAttrs supportedSystems (system: f {
          inherit system;
          pkgs = nixpkgs.legacyPackages.${system};
        });
    in
    {
      packages = forEachSystem ({ system, pkgs }: {
        palmux2 = pkgs.callPackage ./nix/packages/palmux2.nix { };
        caddy-cloudflare = pkgs.callPackage ./nix/packages/caddy-cloudflare.nix { };
        default = self.packages.${system}.palmux2;

        # palmuxOS appliance image (Sb14caa Stage 3). A disposable qcow2 built from
        # nixosModules.appliance: immutable image, all durable state on a separate
        # /persist volume (repos, ~/.claude, config, secrets, operator drop-ins).
        # Ships with ZERO baked SSH keys / passwords — access is provisioned at
        # first boot (cloud-init / palmux onboarding). domain=null → local-only by
        # default; the deployer sets services.palmux.domain in their own drop-in.
        #   nix build .#appliance-qcow2   →  result/nixos.qcow2
        appliance-qcow2 = nixos-generators.nixosGenerate {
          inherit system;
          format = "qcow";
          modules = [
            { nixpkgs.overlays = [ self.overlays.default ]; }
            self.nixosModules.appliance
            ({ lib, config, pkgs, modulesPath, ... }: {
              services.palmux.domain = lib.mkDefault null; # local-only until the deployer sets it
              # Don't bake a full copy of the nixpkgs channel into the image (~0.9GB
              # of dead weight — the appliance is flake-managed, not nix-channel).
              # nixos-generators' qcow format calls make-disk-image with copyChannel
              # defaulting to true; re-issue the same call with copyChannel=false.
              system.build.qcow = lib.mkForce (import "${toString modulesPath}/../lib/make-disk-image.nix" {
                inherit lib config pkgs;
                inherit (config.virtualisation) diskSize;
                format = "qcow2";
                partitionTableType = "hybrid";
                copyChannel = false;
              });
            })
          ];
        };
      });

      lib.mkPalmuxHost = import ./nix/lib/mkPalmuxHost.nix { inherit inputs; };

      # palmuxOS NixOS appliance (Sb14caa). Additive — does not affect the
      # install.sh / lib.mkPalmuxHost (home-manager on Ubuntu) path. The overlay
      # exposes pkgs.palmux2 / pkgs.caddy-cloudflare for the module's references;
      # the root flake keeps nix/packages in-tree, so there is no cross-flake-root
      # path issue (unlike a nixos/-subdir flake).
      overlays.default = final: prev: {
        palmux2 = final.callPackage ./nix/packages/palmux2.nix { };
        caddy-cloudflare = final.callPackage ./nix/packages/caddy-cloudflare.nix { };
        gwq = final.callPackage ./nix/packages/gwq.nix { }; # palmux2 runtime dep (d-kuro/gwq)
      };

      nixosModules = {
        palmux = ./nixos/modules/palmux.nix;       # services.palmux.* host layer
        appliance = ./nixos/modules/appliance.nix; # appliance: state split + drop-in
        default = ./nixos/modules/appliance.nix;
      };
    };
}
