{
  # The ON-APPLIANCE flake. Shipped to /persist/palmux/nixos on first boot by the
  # palmux-state-init service (so it lives on persistent, writable storage). This is
  # how a deployed single-disk appliance UPDATES and is EXTENDED:
  #
  #   # update palmux2 (+ optionally the OS) and switch atomically (rollback-able):
  #   cd /persist/palmux/nixos
  #   sudo nix flake update palmux        # bump the palmux pin to latest
  #   sudo nixos-rebuild switch --flake .#appliance
  #   # roll back if needed:
  #   sudo nixos-rebuild switch --rollback   # or pick an older generation at boot
  #
  #   # extend declaratively: drop a *.nix into ./local and switch
  #   sudo $EDITOR ./local/10-public.nix      # e.g. services.palmux.domain = "...";
  #   sudo nixos-rebuild switch --flake .#appliance
  #
  # The OS image is the SEED; after the first switch the box is managed by this
  # flake. State (~/ghq, ~/.claude, config, secrets) lives elsewhere under /persist
  # and is untouched by generation switches.
  description = "palmuxOS appliance — on-box config (nixos-rebuild updates + drop-ins)";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    # The palmux modules + packages, tracking main. `nix flake update palmux` bumps
    # the appliance to latest main (module source); the palmux2 BINARY follows
    # palmux2.nix's defaultVersion on main, which release.yml bumps to each tag.
    # Operators who want to freeze a version can pin a tag in a local drop-in, e.g.
    # `palmux.url = "github:tjst-t/palmux2/v0.12.0";`.
    palmux.url = "github:tjst-t/palmux2";
  };

  outputs = { self, nixpkgs, palmux, ... }:
    let
      lib = nixpkgs.lib;
      # operator drop-ins — every *.nix under ./local is merged in (skip non-.nix
      # like the .keep placeholder).
      dropins = builtins.filter (p: lib.hasSuffix ".nix" (toString p))
        (lib.filesystem.listFilesRecursive ./local);
    in
    {
      nixosConfigurations.appliance = lib.nixosSystem {
        system = "x86_64-linux";
        modules = [
          { nixpkgs.overlays = [ palmux.overlays.default ]; }
          palmux.nixosModules.appliance
          ./hardware-base.nix    # static: virtio + by-label root + growPartition
          ./grub-device.nix      # generated on first boot: the detected boot disk
        ] ++ dropins;
      };
    };
}
