# palmuxOS — NixOS modules & appliance

Declarative, whole-host palmux: the palmux2 service, the incus workspace runtime,
and Caddy (+ SSO) configured from one `services.palmux.*` option set — plus an
appliance image you can flash and extend.

Full design + the staged validation plan: [`docs/nixos-appliance-design.md`](../docs/nixos-appliance-design.md).

> **Status: scaffold (stage 0).** Not yet eval-checked on a Nix host. Run
> `nix flake check ./nixos` and the stage-1+ validation before relying on it.

## What's here

| path | what |
|---|---|
| `flake.nix` | `nixosModules.{palmux,appliance}`, `nixosConfigurations.palmux-appliance`, `packages.*.appliance-qcow2` |
| `modules/palmux.nix` | the reusable host module — `options.services.palmux.*`, all wiring `mkDefault` |
| `modules/appliance.nix` | appliance: immutable image + `/persist` state split + operator drop-in hook |
| `examples/onappliance-flake/` | the `/etc/palmux` flake on a deployed appliance — extend via `local/*.nix` |
| `examples/user-flake/` | compose the palmux module into your own flake |

## Extend the appliance with your own config (no fork)

The appliance ships `/etc/palmux/` on its persistent volume. Add a fragment and rebuild:

```bash
sudo tee /etc/palmux/local/my-extras.nix <<'NIX'
{ pkgs, ... }: {
  environment.systemPackages = [ pkgs.tmux pkgs.ripgrep ];
  services.palmux.domain = "dev.example.net";   # overrides palmux's mkDefault
}
NIX
sudo nixos-rebuild switch --flake /etc/palmux#appliance
```

`local/` lives on `/persist`, so your config survives image upgrades. palmux sets
everything with `lib.mkDefault`, so your plain assignments win.

## Or compose it into your own flake

See `examples/user-flake/flake.nix` — import `palmux.nixosModules.palmux`, set
`services.palmux.*`, and use the full NixOS surface alongside.

## Build the appliance image

```bash
nix build ./nixos#appliance-qcow2     # → a qcow2; boot it, state lands on /persist
```
