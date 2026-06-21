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
  };

  outputs =
    inputs@{ self
    , nixpkgs
    , home-manager
    , system-manager
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
