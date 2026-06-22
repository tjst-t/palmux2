{
  # Source-based composition (the NixOS-idiomatic way): bring palmux into YOUR
  # own flake as a module and add your config alongside it. Use this when you
  # manage the machine from your own flake rather than the shipped appliance.
  description = "example: a user's NixOS host that imports the palmux module";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-25.05";
    palmux.url = "github:tjst-t/palmux2";
  };

  outputs = { self, nixpkgs, palmux, ... }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      system = "x86_64-linux";
      modules = [
        { nixpkgs.overlays = [ palmux.overlays.default ]; }
        palmux.nixosModules.palmux        # the palmux layer (services.palmux.*)
        ./hardware-configuration.nix
        ({ pkgs, ... }: {
          # palmux options:
          services.palmux.enable = true;
          services.palmux.domain = "dev.example.net";
          services.palmux.secretsFile = "/persist/palmux/secrets.env";

          # ...and the full NixOS surface, yours to control:
          environment.systemPackages = [ pkgs.git pkgs.htop ];
          networking.hostName = "myhost";
          system.stateVersion = "25.05";
        })
      ];
    };
  };
}
