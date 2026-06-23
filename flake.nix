{
  description = "palmux2 — Nix flake for declarative Ubuntu/NixOS deployment";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    # The appliance image + its on-appliance update flake both build from the
    # STABLE release, so an in-place `nixos-rebuild` minimises the closure delta and
    # doesn't swap kernel/systemd versions out from under the box (the install.sh /
    # home-manager path keeps tracking unstable above).
    nixpkgs-appliance.url = "github:NixOS/nixpkgs/nixos-25.05";
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
    # untouched. Built against nixpkgs-appliance (25.05) to match the on-appliance flake.
    nixos-generators = {
      url = "github:nix-community/nixos-generators";
      inputs.nixpkgs.follows = "nixpkgs-appliance";
    };
  };

  outputs =
    inputs@{ self
    , nixpkgs
    , nixpkgs-appliance
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
              # qcow2-compressed: qemu-img convert -c compresses the qcow2 clusters
              # ~2-3x for distribution. Reads are decompressed transparently and the
              # inner ext4 store stays READ-WRITE, so `nixos-rebuild switch` + operator
              # drop-ins keep working (unlike a read-only squashfs store). NOTE: this
              # is a SINGLE-disk appliance — /persist is a directory on the root fs, so
              # runtime writes (repos, ~/.claude, nix-store growth on rebuild) DO land
              # on this disk and the qcow2 grows from its compressed seed size as the
              # box is used. The compression only shrinks the DISTRIBUTED artifact.
              system.build.qcow = lib.mkForce (import "${toString modulesPath}/../lib/make-disk-image.nix" {
                inherit lib config pkgs;
                inherit (config.virtualisation) diskSize;
                format = "qcow2-compressed";
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

      # A buildable appliance system (nixos-25.05), for CI build/eval + the
      # no-baked-keys check. Same modules the qcow2 image + on-appliance flake use.
      nixosConfigurations.appliance = nixpkgs-appliance.lib.nixosSystem {
        system = "x86_64-linux";
        modules = [
          { nixpkgs.overlays = [ self.overlays.default ]; }
          self.nixosModules.appliance
          ./nixos/appliance-flake/hardware-base.nix
          ./nixos/appliance-flake/grub-device.nix
          ({ lib, ... }: { services.palmux.domain = lib.mkDefault null; })
        ];
      };
    };
}
