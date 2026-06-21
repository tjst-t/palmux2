# palmuxOS — NixOS appliance design

> Status: **design + scaffold** (2026-06). The `nixos/` tree is scaffolded but
> NOT yet eval-validated on a Nix host (this design box has no Nix). Stages 1–5
> below are the validation/build-out plan. Treat the Nix files as the proposed
> shape, to be `nix flake check`'d and hardware-validated per the staged plan.

## Why NixOS (recap of the decision)

palmux hosts already sit on a Nix substrate: `palmux2` is a Nix package
(`nix/packages/palmux2.nix`), deployed via home-manager
(`nix/modules/home-manager-palmux.nix`), with host-level Caddy via **system-manager**
(`nix/modules/system-manager-caddy.nix`) and the remaining host bits (swap, sysctl,
unattended-upgrades, subuid/subgid, apt packages) done imperatively in the tail of
`scripts/install.sh`.

To manage the *whole* host declaratively and to ship palmux as an **appliance
image**, the OS itself must be NixOS — a full `configuration.nix` (`nixos-rebuild`)
and an appliance image (`nixos-generators`) are NixOS-only. system-manager on Ubuntu
only covers a subset (systemd units, `/etc`, Nix packages) — not kernel/boot/init or
the appliance image.

NixOS also makes the palmux host module **simpler** than the Ubuntu installer:
- `virtualisation.incus.enable` replaces the manual incus setup + subuid/subgid wiring
- `services.caddy.virtualHosts` replaces `system-manager-caddy.nix`
- a NixOS systemd service replaces the home-manager user unit + the install.sh tail
- `system.autoUpgrade` / generations replace unattended-upgrades + the self-update helper

## Layout

```
nixos/
├── flake.nix                     # inputs + outputs: nixosModules.{palmux,appliance,default},
│                                 #   nixosConfigurations.palmux-appliance, packages.*.appliance-qcow2
├── modules/
│   ├── palmux.nix                # the reusable host module: options.services.palmux.* + wiring
│   │                             #   (palmux2 service, incus, caddy/SSO, subuid/subgid) — all mkDefault
│   └── appliance.nix             # appliance specifics: persistent state volume, the USER drop-in
│                                 #   import hook, autoUpgrade, minimal base
├── examples/
│   ├── onappliance-flake/        # what ships at /etc/palmux on the appliance's PERSISTENT volume
│   │   ├── flake.nix             #   imports nixosModules.appliance + ./local/*.nix  → user extends here
│   │   └── local/example.nix     #   example operator drop-in (full NixOS surface)
│   └── user-flake/flake.nix      # source-based composition: a user's own flake importing nixosModules.palmux
└── README.md
```

`nix/packages/palmux2.nix` (the release-asset derivation) is reused as-is; the
NixOS module just references the package.

## The two extensibility mechanisms (the core requirement)

A palmux appliance user must be able to **add their own declarative config on top
of palmux's**, without forking palmux. NixOS gives this for free via the module
system; we expose it in two shapes:

### 1. Source composition (NixOS-idiomatic — for people who manage a flake)

palmux is shipped as `nixosModules.palmux`. A user composes it with their own
modules in their own flake; the module system merges + the user overrides palmux's
`mkDefault`s with plain assignments (or `mkForce`). See `examples/user-flake/`.

```nix
# user's flake.nix
{
  inputs.palmux.url = "github:tjst-t/palmux2?dir=nixos";
  outputs = { nixpkgs, palmux, ... }: {
    nixosConfigurations.myhost = nixpkgs.lib.nixosSystem {
      modules = [
        palmux.nixosModules.palmux         # the palmux layer
        ./hardware-configuration.nix
        ({ pkgs, ... }: {
          services.palmux.domain = "dev.example.net";   # palmux options
          environment.systemPackages = [ pkgs.htop ];   # ...and the FULL NixOS surface
          networking.firewall.allowedTCPPorts = [ 1234 ];
        })
      ];
    };
  };
}
```

### 2. Appliance drop-in (for deployed-image operators — no fork, no flake authoring)

The appliance ships `/etc/palmux/` **on its persistent state volume** as a small
flake (`examples/onappliance-flake/`). Its `configuration` imports
`nixosModules.appliance` **plus every `*.nix` under `/etc/palmux/local/`**. So an
operator extends a running appliance by:

```bash
# drop a declarative fragment — the FULL NixOS option surface is available
sudo tee /etc/palmux/local/my-extras.nix <<'NIX'
{ pkgs, ... }: {
  environment.systemPackages = [ pkgs.tmux pkgs.ripgrep ];
  services.openssh.settings.X11Forwarding = true;
  # override a palmux default cleanly (palmux sets it with mkDefault):
  services.palmux.bindAddr = "127.0.0.1:9000";
}
NIX
sudo nixos-rebuild switch --flake /etc/palmux#appliance
```

Because `/etc/palmux/local/` lives **inside the flake's source dir on the
persistent volume**, the import is flake-pure (no `--impure`, no reading
`/etc` at eval from outside the flake). `/etc/palmux/local/` survives image
swaps/upgrades because it is on the persistent data volume, not the immutable
image. This is the mechanism the appliance promises: *declaratively layer your
own config onto palmux's, in place, and `nixos-rebuild switch`.*

palmux sets **all** of its own config with `lib.mkDefault` so any operator
assignment wins without `mkForce`.

## Immutable image + persistent state separation

The appliance image is immutable; all mutable state lives on a **separate
persistent volume** mounted at `/persist` (bind-mounted into place), so the image
can be rebuilt/swapped/upgraded without data loss:

| state | path | mounted from |
|---|---|---|
| repos | `~/ghq` | `/persist/ghq` |
| Claude records (history, memory, projects) | `~/.claude`, `~/.claude.json` | `/persist/claude/...` |
| palmux config | `~/.config/palmux` | `/persist/palmux/config` |
| secrets | `secrets.env` | `/persist/palmux/secrets.env` (0600) |
| operator NixOS overrides | `/etc/palmux/local/` | `/persist/palmux/nixos-local` |
| incus (workspace containers are disposable; state is bind-mounts) | `/var/lib/incus` | persistent or ephemeral per policy |

This is exactly the `~/.claude` / `~/ghq` migration concern from the dev rebuild —
appliance-ization **forces** the state/image split, which makes that data the
explicit, portable, backed-up part by design.

## Staged build-out + validation plan

The risk is concentrated in incus-on-NixOS; the plan front-loads it and reuses the
project's real-mode smoke discipline. **Pilot on the dev rebuild** (it's getting
rebuilt anyway and is disposable).

- **Stage 0 — scaffold (this commit):** `nixos/` flake + module + extensibility
  hook + examples + this doc. Not yet eval-checked (no Nix on the design box).
- **Stage 1 — module parity (no incus):** stand up a NixOS VM, `nix flake check`,
  `nixos-rebuild switch` → palmux2 runs via the NixOS service + `services.caddy`
  vhost + SSO + config plane. Validate the app path end-to-end (real-browser SSO,
  Files/Git/Claude tabs on host runtime).
- **Stage 2 — incus-on-NixOS (gating risk):** `virtualisation.incus.enable`, validate
  idmap `both 1000 1000` + `/etc/subuid`/`subgid` + bridge + `palmux runtime install`
  (palmux-ws image) + a workspace container launches + Browser/ports/SSO-subdomain.
  Real-mode acceptance like `tests/acceptance/*`.
- **Stage 3 — appliance image:** add `nixos-generators` qcow2/ISO targets; wire the
  `/persist` state split + the `/etc/palmux/local` drop-in; boot the image, prove
  state survives an image swap and an operator drop-in `nixos-rebuild switch` applies.
- **Stage 4 — extensibility + docs:** finalize `nixosModules.palmux`, the user-flake
  example, the drop-in mechanism; document "how to add your own config" both ways.
- **Stage 5 — adopt:** run the rebuilt **dev** as the NixOS appliance. Once stable,
  optionally migrate ndev / deploy-test (they're single-tenant, low-risk). Ubuntu +
  install.sh remains the supported path for non-NixOS / existing boxes.

## Relationship to install.sh (it does not go away)

`install.sh` stays the **quick installer for non-NixOS / existing boxes** (Ubuntu +
Determinate Nix + home-manager). The NixOS module is the path for *your* fleet and
the appliance. Both consume the same `nix/packages/palmux2.nix`. Over time, logic
common to both (config plane shape, Caddy vhost shape) should be factored so the two
deployment fronts don't drift — but that's an optimization, not a blocker.
