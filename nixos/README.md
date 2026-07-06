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
| `appliance-flake/` | the on-appliance flake shipped to **`/persist/palmux/nixos`** — extend via `local/*.nix`, update via `nix flake update palmux` |
| `examples/onappliance-flake/` | illustrative copy of the on-appliance flake — see `appliance-flake/` for the shipped source |
| `examples/user-flake/` | compose the palmux module into your own flake |

## Extend the appliance with your own config (no fork)

The appliance ships its flake at **`/persist/palmux/nixos`** on the persistent
volume (this is the real `flakeDir` in `modules/appliance.nix`; it is also the
single source the GUI update panel renders via `applianceFlakeTarget`). Add a
fragment and rebuild:

```bash
sudo tee /persist/palmux/nixos/local/my-extras.nix <<'NIX'
{ pkgs, ... }: {
  environment.systemPackages = [ pkgs.tmux pkgs.ripgrep ];
  services.palmux.domain = "dev.example.net";   # overrides palmux's mkDefault
}
NIX
sudo nixos-rebuild switch --flake /persist/palmux/nixos#appliance
```

`local/` lives on `/persist`, so your config survives image upgrades. palmux sets
everything with `lib.mkDefault`, so your plain assignments win.

## Update palmux to the latest release

```bash
cd /persist/palmux/nixos
sudo nix flake update palmux                       # bump the palmux pin to latest main
sudo nixos-rebuild switch --flake /persist/palmux/nixos#appliance
```

This is exactly what the GUI update panel's **本体を更新 (nixos-rebuild)** button
kicks (via the verb-limited `palmux-rebuild-update.service`), so you rarely need to
run it by hand. Generation switch is atomic; roll back with
`sudo nixos-rebuild switch --flake /persist/palmux/nixos#appliance --rollback` or by
booting the previous generation.

## Or compose it into your own flake

See `examples/user-flake/flake.nix` — import `palmux.nixosModules.palmux`, set
`services.palmux.*`, and use the full NixOS surface alongside.

## Build the appliance image

```bash
nix build ./nixos#appliance-qcow2     # → a qcow2; boot it, state lands on /persist
```
