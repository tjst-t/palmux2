# palmuxOS test host (Proxmox) — Sb14caa

A disposable NixOS test box for the palmuxOS work. **Personal, non-distributed**
host config: SSH keys come from `https://github.com/tjst-t.keys` (pinned flake
input). This does NOT violate the appliance's no-baked-keys rule — that rule is
about the *distributed* image/modules, not your own hosts.

> Stage 0 scaffold — not yet eval-checked. `nix flake check` + the Stage-1
> validation first. Per the execution constraint, run this on a **disposable VM
> (e.g. 192.168.1.41), never the dev VM**.

## Install on Proxmox via nixos-anywhere (recommended)

1. **Create the VM** (Proxmox): QEMU/KVM (not LXC — incus nests poorly in LXC),
   BIOS = **OVMF (UEFI)**, a VirtIO disk (≥20G), ≥4G RAM, 2 vCPU.
2. **Boot a foothold** so nixos-anywhere can SSH in as root — easiest is the
   **NixOS minimal ISO**: attach it, boot, then in the console:
   ```
   sudo passwd root        # temporary install-time password (throwaway)
   ip a                    # note the VM IP
   ```
   (sshd already runs on the ISO. This is the install-time foothold key/password
   — separate from the GitHub keys that get baked into the result.)
3. **Install from your workstation** (in this dir):
   ```
   nix run github:nix-community/nixos-anywhere -- \
     --flake .#testbox --target-host root@<VM-IP>
   ```
   disko wipes the disk, NixOS is installed, the box reboots.
4. **After reboot**, log in with your GitHub key (now baked from `tjst-t.keys`):
   ```
   ssh root@<VM-IP>
   ```
   The step-2 temporary password is irrelevant now (config controls auth).

### Iterate (no reinstall per change)
```
nixos-rebuild switch --flake .#testbox --target-host root@<VM-IP>
# or run ON the box: sudo nixos-rebuild switch --flake .#testbox
```
For local module iteration against your checkout:
```
... --override-input palmux path:/home/ubuntu/ghq/github.com/tjst-t/palmux2/nixos
```

## Notes
- `services.palmux.incus.enable = false` for Stage 1 (module parity). Flip to
  `true` for Stage 2 (incus-on-NixOS).
- `services.palmux.domain = null` (local-only) until you have a real domain +
  Cloudflare token for SSO/wildcard; then set the domain + `secretsFile`.
- VirtIO SCSI disk → change `device` in `disko.nix` to `/dev/sda`.
- SeaBIOS (legacy) instead of OVMF → switch `disko.nix` to GRUB + a `bios_grub`
  partition and `boot.loader.grub` in `configuration.nix`.
- Refresh the GitHub keys: `nix flake update tjstKeys`.
- **Networking on the minimal ISO**: wired DHCP is on by default — `ip a` shows
  the IP, no setup needed. No DHCP / want static in the live session:
  `sudo ip addr add <ip>/24 dev ens18 && sudo ip route add default via <gw>` (find
  the NIC with `ip link`). The installed system's networking is declared in
  `configuration.nix` (DHCP default; static example commented there). For a stable
  homelab address, a DHCP reservation by MAC is simplest.
