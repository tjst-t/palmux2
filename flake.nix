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
    # disko (Sb14caa): declaratively partition the appliance image at BUILD time —
    # root + /persist baked in, so the deployer never partitions anything. Only the
    # appliance-qcow2 build consumes it; the running system finds its fs by label.
    disko = {
      url = "github:nix-community/disko";
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
    , disko
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
        # Claude Code CLI, build-time pinned (S61c9a6-3) — see nix/packages/claude-code.nix.
        claude-code = pkgs.callPackage ./nix/packages/claude-code.nix { };
        default = self.packages.${system}.palmux2;

        # S31ad96-1: palmux2 built from THIS repo's own local working-tree
        # source (Go + embedded frontend), NOT a fetched release asset —
        # see nix/packages/palmux2-local.nix for the full rationale. Purely
        # additive: `default` above stays the release/fetchurl package, and
        # nothing else references palmux2-local unless a caller (e.g. a
        # one-off verification nixosSystem, or `nix build .#palmux2-local`)
        # explicitly opts in.
        palmux2-local = pkgs.callPackage ./nix/packages/palmux2-local.nix { };

        # palmuxOS appliance image (Sb14caa Stage 3). A qcow2 built via DISKO with a
        # 2-partition layout baked in: root (16G, fixed) + /persist (rest, last,
        # autoResize). All durable state lives on /persist (repos / ~ (home) /
        # config / secrets / incus storage / operator drop-ins); growing the VM disk
        # grows ONLY /persist, so the OS root can't be filled by a runaway clone or
        # container. Immutable-OS / mutable-state split is physical and transparent —
        # the image ships pre-partitioned, the deployer never partitions anything.
        # Ships with ZERO baked SSH keys / passwords (cloud-init / onboarding at
        # first boot). domain=null → local-only until the deployer sets it.
        #   nix build .#appliance-qcow2   →  result/main.raw (sparse; compress with
        #   `qemu-img convert -O qcow2 -c` for distribution — verified 2026-07-13,
        #   disko's diskoImages does not emit qcow2 directly)
        appliance-qcow2 =
          let
            sys = nixpkgs-appliance.lib.nixosSystem {
              inherit system;
              modules = [
                { nixpkgs.overlays = [ self.overlays.default ]; }
                disko.nixosModules.disko
                ./nixos/modules/disko-layout.nix
                ./nixos/modules/image-hardware.nix
                self.nixosModules.appliance
                ({ lib, ... }: { services.palmux.domain = lib.mkDefault null; })
              ];
            };
          in
          # disko builds the image(s) for disko.devices in a VM (partitions, mkfs by
          # label, copies the closure, installs GRUB). One disk → one qcow2.
          sys.config.system.build.diskoImages;
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
        claude-code = final.callPackage ./nix/packages/claude-code.nix { }; # S61c9a6-3 fresh-install bootstrap
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
