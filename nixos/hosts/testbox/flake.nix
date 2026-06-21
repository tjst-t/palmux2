{
  # Sb14caa test host for Proxmox. This is a PERSONAL, NON-DISTRIBUTED host config
  # (the author's own box) — so baking the author's keys here is fine and does NOT
  # violate the appliance's no-baked-keys rule (that rule is about the distributed
  # image / nixosModules, not your own hosts). See docs/nixos-appliance-design.md.
  #
  # SSH keys are fetched from https://github.com/tjst-t.keys as a pinned flake
  # input (flake.lock), so they're pure + refreshable with `nix flake update tjstKeys`.
  #
  # NOTE (stage 0 scaffold): not yet eval-checked. Resolve the palmux input + the
  # nixos flake's ../nix purity TODO in stage 1.
  description = "palmuxOS Sb14caa test host (Proxmox)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    disko = {
      url = "github:nix-community/disko";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    # The palmux NixOS modules + overlay. For local module iteration, override:
    #   nixos-anywhere ... --override-input palmux path:/path/to/palmux2/nixos
    palmux.url = "github:tjst-t/palmux2?dir=nixos";
    # The author's public SSH keys — a non-flake file input, pinned in flake.lock.
    tjstKeys = {
      url = "https://github.com/tjst-t.keys";
      flake = false;
    };
  };

  outputs = { self, nixpkgs, disko, palmux, tjstKeys, ... }:
    let
      system = "x86_64-linux";
      # one key per line → list, dropping blanks
      authorizedKeys =
        nixpkgs.lib.filter (s: s != "")
          (nixpkgs.lib.splitString "\n" (builtins.readFile tjstKeys));
    in {
      nixosConfigurations.testbox = nixpkgs.lib.nixosSystem {
        inherit system;
        specialArgs = { inherit authorizedKeys; };
        modules = [
          { nixpkgs.overlays = [ palmux.overlays.default ]; }
          disko.nixosModules.disko
          palmux.nixosModules.palmux     # the palmux host layer (services.palmux.*)
          ./disko.nix
          ./configuration.nix
        ];
      };
    };
}
