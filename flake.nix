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
    };
}
