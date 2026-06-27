# nixos/modules/appliance.nix
#
# Appliance specifics layered on top of modules/palmux.nix:
#   - immutable image + mutable state on a separate /persist volume
#   - the OPERATOR DROP-IN extensibility hook (see docs/nixos-appliance-design.md §2.2)
#   - generation-based upgrades
#
# The operator extends the appliance by dropping *.nix fragments into the flake's
# ./local/ dir (which lives on /persist) and running `nixos-rebuild switch`. The
# import is flake-PURE because ./local is part of the on-appliance flake's source
# tree (examples/onappliance-flake/), not an out-of-tree /etc read.
#
# NOTE: scaffold — not yet eval-checked. TODO(stageN) markers per the design doc.
{ config, lib, pkgs, ... }:
let
  pUser = config.services.palmux.user;
  # The palmux user's PRIMARY GROUP. services.palmux sets isNormalUser without an
  # explicit `group`, so NixOS defaults it to "users" (gid 100) — there is no
  # "palmux" group. tmpfiles rules must chown to this real group, not to `${pUser}`
  # (which would be an unresolvable group name and make the rule fail).
  pGroup = config.users.users.${pUser}.group;

  # ── the SINGLE-DISK on-/persist layout (Sb14caa, docs/nixos-appliance-design.md)
  # The appliance is a SINGLE disk: /persist is just a directory on the root fs, not
  # a separate volume. nixos-rebuild's generation switch only swaps /nix/store +
  # /run/current-system, so everything under /persist (state, config, the update
  # flake) is untouched by updates and survives reboots. `qm resize` + growPartition
  # grow the disk for more space. (An operator who wants the OLD separate-volume
  # model for whole-image-swap updates can add `fileSystems."/persist"` in a drop-in.)
  #
  #   /persist/palmux/nixos/     ← ON-APPLIANCE FLAKE (update + extend via nixos-rebuild)
  #     ├─ flake.nix             pins palmux; nixos-rebuild switch --flake .#appliance
  #     ├─ hardware-base.nix     virtio + by-label root + growPartition
  #     ├─ grub-device.nix       generated on first boot (detected bootsector disk)
  #     └─ local/  *.nix         operator drop-ins (domain, extra pkgs, …)
  #   /persist/palmux/config/    ← palmux2 --config-dir (config.toml + settings.json)
  #     └─ secrets.env           ← SECRETS (CF token / SSO secret / bcrypt), 0600;
  #                                 same file palmux2 reads/writes AND systemd's
  #                                 EnvironmentFile (services.palmux.secretsFile)
  #   /persist/palmux/home/      ← DATA (~/ghq, ~/.claude)
  flakeDir = "/persist/palmux/nixos";   # on-appliance flake (+ local/ drop-ins)
  dropinDir = "${flakeDir}/local";
  appCfgDir = "/persist/palmux/config"; # palmux2 --config-dir (config.toml + settings.json)
  appFlakeNix = ../appliance-flake/flake.nix;
  appHwBase = ../appliance-flake/hardware-base.nix;
in
{
  imports = [ ./palmux.nix ];

  # ── palmux defaults suitable for an appliance ──────────────────────────────
  services.palmux.enable = lib.mkDefault true;
  services.palmux.stateDir = lib.mkDefault "/persist/palmux/home";   # DATA
  services.palmux.configDir = lib.mkDefault appCfgDir;               # operator config bundle
  # The systemd EnvironmentFile MUST be the SAME secrets.env that palmux2's
  # --config-dir layer reads/writes (config.WriteSecrets → <configDir>/secrets.env,
  # per the Sa53137 model). Pointing it elsewhere splits secrets across two files:
  # the GUI's RotateSecrets writes <configDir>/secrets.env while systemd loads the
  # other, so a GUI-set SSO secret / password never reaches the process env (and a
  # state-init-generated SSO secret never shows in the GUI). Unify on configDir.
  services.palmux.secretsFile = lib.mkDefault "${appCfgDir}/secrets.env";

  # ── first-boot LAN exposure (so onboarding is reachable on the IP) ─────────
  # Before a public domain is set, bind the WebUI to the LAN so the deployer can
  # reach http://<ip>:7683 directly (cloud-init assigns the IP) for first-boot
  # setup — no SSH tunnel needed. Once services.palmux.domain is set, bind back to
  # loopback so palmux2 sits behind Caddy + SSO and can't be hit directly (which
  # would bypass SSO).
  # SECURITY: with no domain there is NO auth yet (the onboarding/claim gate is
  # still backlog), so the WebUI — terminals included — is open to anyone on the
  # LAN until SSO/domain is configured. Operator-accepted for a trusted LAN.
  services.palmux.bindAddr = lib.mkDefault (
    if config.services.palmux.domain == null then "0.0.0.0:7683" else "127.0.0.1:7683"
  );
  # The firewall (on by default) would otherwise drop incoming :7683 even with the
  # 0.0.0.0 bind, so open it in the no-domain LAN-exposed mode. (mkIf list → merges
  # with the Caddy block's [80 443] when a domain is set.)
  networking.firewall.allowedTCPPorts =
    lib.mkIf (config.services.palmux.domain == null) [ 7683 ];

  # ── slim the appliance: stub out incus features the appliance never uses ───
  # The NixOS incus module hardwires, into incus.service, big dependencies that
  # only its VM driver / S3 storage backend need:
  #   - qemu_kvm (+ GUI/audio backends: SDL, GTK4, pipewire, gstreamer, spice,
  #     zenity) ≈ 1GB for the VM driver
  #   - minio / minio-client ≈ 140MB for the S3 object-storage pool
  # palmux uses incus CONTAINERS ONLY and the `dir` storage pool, so neither is
  # ever invoked. Replace them with empty stubs (the daemon starts fine; it just
  # reports VM instances / S3 pools as unavailable). An operator who actually wants
  # incus VMs or S3 storage overrides these back in their own drop-in overlay.
  # (nixpkgs.overlays is a list → this merges with the flake's overlay.)
  # NOTE: qemu can't be fully stubbed — `make-disk-image` (the qcow2 builder) runs
  # its own bootloader-install VM with real qemu-system-x86_64, so a stub breaks the
  # IMAGE BUILD itself. Use the GUI-less nixos-test qemu instead: real qemu (build +
  # incus both work), but without the ~1GB SDL/GTK/pipewire/spice desktop stack.
  # minio (incus S3 object-storage pool) IS fully stubbable — neither the builder nor
  # the `dir` storage pool palmux uses ever invokes it.
  nixpkgs.overlays = [
    (final: prev:
      let stub = name: prev.runCommand "${name}-stub-0" { } "mkdir -p $out/bin $out/libexec";
      in {
        qemu_kvm = prev.qemu_test;
        minio = stub "minio";
        minio-client = stub "minio-client";
      })
  ];

  # Headless appliance: no man pages / NixOS manual / info (~150MB).
  documentation.enable = lib.mkDefault false;

  # Minimal locales: the full glibcLocales archive is ~200MB; ship just UTF-8.
  i18n.supportedLocales = lib.mkDefault [ "en_US.UTF-8/UTF-8" "C.UTF-8/UTF-8" ];

  # Don't embed the full nixpkgs SOURCE tree (~0.5-1GB of pkgs/by-name/…) into the
  # system closure. A flake-built NixOS pins its nixpkgs into NIX_PATH + the flake
  # registry by default (so `nix-shell -p` / `nix run nixpkgs#…` work), which drags
  # the whole nixpkgs checkout into the image. The appliance is flake-managed (it
  # updates via `nixos-rebuild --flake /etc/palmux`, which carries its own nixpkgs),
  # so the embedded copy is dead weight. Operators who want ad-hoc `nix-shell -p`
  # can re-enable these in a drop-in.
  nixpkgs.flake.setNixPath = lib.mkDefault false;
  nixpkgs.flake.setFlakeRegistry = lib.mkDefault false;

  # Stage-1 storage controllers. The qcow2 IMAGE build pulls in only nixosModules.
  # appliance (NOT hardware-base.nix / the qemu-guest profile), so the shipped initrd
  # had no virtio_scsi and could not see a virtio-SCSI root disk — Proxmox's DEFAULT
  # bus. Stage 1 then hung "waiting for /dev/disk/by-label/nixos" → panic. virtio-blk
  # happened to work only because virtio_blk is in the stock initrd. Bake the common
  # virtio + SATA/AHCI controllers into the initrd here (this module IS in the image
  # build, the CI config, AND the on-appliance flake) so the appliance boots on
  # whichever disk bus the platform hands it — scsi (sda) or blk (vda).
  boot.initrd.availableKernelModules = [
    "virtio_pci" "virtio_scsi" "virtio_blk" "sd_mod" "sr_mod" "ahci"
  ];

  # cloud-init manages networking so the deployer's static IP/GW (or DHCP) from the
  # platform's cloud-init drive is applied. NixOS leaves this OFF by default (its
  # networking is declarative), which is why a Proxmox-set static IP was previously
  # ignored — the per-instance modules (ssh keys/hostname) ran but networking did not.
  services.cloud-init.network.enable = lib.mkDefault true;

  # Use systemd-networkd as the SINGLE network manager. cloud-init renders its
  # network config as networkd .network files, so without this NixOS also runs the
  # scripted dhcpcd backend and the two fight over the same interface (NixOS warns
  # this "can lead to loss of networking"); a cloud-init static IP in particular can
  # race a dhcpcd DHCP lease. With networkd authoritative, static + DHCP both apply
  # cleanly through cloud-init.
  networking.useNetworkd = lib.mkDefault true;

  # palmux2/incus order after network-online.target; with networkd that pulls
  # systemd-networkd-wait-online, which by default waits for ALL managed links to be
  # online and can HANG to its timeout (~2min) on a down/virtual interface (e.g. the
  # incus bridge before a container starts), delaying boot. Wait for ANY one link.
  systemd.network.wait-online.anyInterface = lib.mkDefault true;

  # ── /persist state + on-appliance update flake (single disk) ────────────────
  # /persist is a directory on the ROOT fs (single-disk model — no separate volume).
  # palmux-state-init creates the durable state subtree AND ships the on-appliance
  # flake to /persist/palmux/nixos so the box can `nixos-rebuild switch` to update.
  #
  # Why a oneshot (not systemd.tmpfiles.rules): historically /persist was a `nofail`
  # mount and tmpfiles-setup (ordered only After=local-fs.target) could race ahead
  # of the mount and create the dirs on the underlying fs, then the mount hid them →
  # palmux2 failed (missing WorkingDirectory/EnvironmentFile, `resources`). A oneshot
  # with RequiresMountsFor=/persist + before/requiredBy palmux2 is deterministic and
  # works for both the root-fs dir and a future separate-volume drop-in. Idempotent.
  systemd.services.palmux-state-init = {
    description = "Create the palmux /persist state subtree + on-appliance update flake";
    before = [ "palmux2.service" ];
    requiredBy = [ "palmux2.service" ]; # palmux2 Requires + waits for this
    unitConfig.RequiresMountsFor = "/persist";
    path = with pkgs; [ coreutils util-linux ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = pkgs.writeShellScript "palmux-state-init" ''
        set -eu
        # durable state subtree (palmux2 needs these to start)
        install -d -m 0755 /persist/palmux
        install -d -m 0700 -o ${pUser} -g ${pGroup} /persist/palmux/home
        install -d -m 0750 -o ${pUser} -g ${pGroup} ${appCfgDir}
        # secrets.env is the SAME file palmux2's --config-dir layer reads/writes
        # (config.WriteSecrets → ${appCfgDir}/secrets.env) AND the systemd
        # EnvironmentFile (services.palmux.secretsFile, unified above). One file.
        secrets=${appCfgDir}/secrets.env
        [ -e "$secrets" ] \
          || install -m 0600 -o ${pUser} -g ${pGroup} /dev/null "$secrets"

        # Generate a STABLE SSO signing key once (PALMUX_SSO_SECRET) if absent. SSO
        # cookies are HMAC-signed with it; an empty key breaks the apex forward_auth
        # login and a per-boot-random one would log everyone out on every restart.
        # install.sh does the same for the Ubuntu path. Seed-only; never overwrite.
        if ! grep -q '^PALMUX_SSO_SECRET=' "$secrets" 2>/dev/null; then
          secret=$(od -An -tx1 -N32 /dev/urandom | tr -d ' \n')
          printf 'PALMUX_SSO_SECRET=%s\n' "$secret" >> "$secrets"
          chown ${pUser}:${pGroup} "$secrets"
          chmod 0600 "$secrets"
        fi

        # on-appliance flake for `nixos-rebuild switch` updates + operator drop-ins.
        # SEED-ONLY: only write these if absent, so an operator who edits flake.nix
        # (e.g. bumps the palmux pin) or whose `nix flake update` rewrote flake.lock
        # isn't silently reverted on the next reboot.
        install -d -m 0755 ${flakeDir} ${dropinDir}
        [ -e ${flakeDir}/flake.nix ]        || install -m 0644 ${appFlakeNix} ${flakeDir}/flake.nix
        [ -e ${flakeDir}/hardware-base.nix ] || install -m 0644 ${appHwBase} ${flakeDir}/hardware-base.nix
        # generate grub-device.nix with THIS VM's actual bootsector disk (the parent
        # of the root partition — /dev/sda on virtio-scsi, /dev/vda on virtio-blk) so
        # `nixos-rebuild` installs grub to the right place across hardware variants.
        # Only on first boot, and only if we resolved a REAL block device (an empty
        # pkname would yield "/dev/" and make a later rebuild's grub-install fail —
        # fall back to the shipped placeholder in that case).
        if [ ! -e ${flakeDir}/grub-device.nix ]; then
          rootsrc=$(findmnt -nfo SOURCE / || true)
          disk=/dev/$(lsblk -no pkname "$rootsrc" 2>/dev/null | head -n1)
          if [ -b "$disk" ]; then
            printf '{ boot.loader.grub.device = "%s"; }\n' "$disk" > ${flakeDir}/grub-device.nix
          else
            install -m 0644 ${../appliance-flake/grub-device.nix} ${flakeDir}/grub-device.nix
          fi
        fi
      '';
    };
  };

  system.stateVersion = lib.mkDefault "25.05";

  # ── generation-based upgrades (replaces unattended-upgrades + self-update) ──
  system.autoUpgrade = {
    enable = lib.mkDefault false; # opt-in; appliance updates are operator-driven
    flake = lib.mkDefault "${flakeDir}#appliance";
  };
  # The update path is `nixos-rebuild switch --flake ${flakeDir}#appliance`, so the
  # appliance needs flakes enabled.
  nix.settings.experimental-features = lib.mkDefault [ "nix-command" "flakes" ];

  # ── GUI/CLI-kickable nixos-rebuild (apply public domain/TLS without a shell) ──
  # palmux2 runs as the NON-root `${pUser}` user, which on the appliance has no
  # password and is not in `wheel` (key-zero image) — so it can run neither
  # `nixos-rebuild` nor `sudo`. (This is exactly what dead-ends the onboarding
  # wizard's old `sudo palmux reconcile-system` step, which is an Ubuntu-only verb
  # anyway; on NixOS Caddy is declarative.) Expose the switch as a ROOT system
  # oneshot and let `${pUser}` START it over the system bus via a polkit rule. The
  # unit runs in its OWN cgroup, so the switch restarting palmux2.service does NOT
  # kill the rebuild mid-flight — the Sa8e7d0 self-update lesson, applied to the OS.
  # palmux2's POST /api/deploy/rebuild does `systemctl start --no-block
  # palmux-rebuild.service`; the GUI deploy panel / onboarding wizard surface it as
  # the “適用 (nixos-rebuild)” button, and the FE reconnect handshake covers the
  # palmux2 restart.
  systemd.services.palmux-rebuild = {
    description = "Apply palmux appliance config via nixos-rebuild switch (GUI/CLI-triggered)";
    # Don't let the very switch we run restart this oneshot out from under itself.
    restartIfChanged = false;
    path = [ config.system.build.nixos-rebuild pkgs.nix pkgs.git pkgs.coreutils pkgs.gnugrep pkgs.gnused pkgs.systemd ];
    serviceConfig = {
      Type = "oneshot";
      Environment = "HOME=/root"; # nixos-rebuild writes its eval cache under $HOME
    };
    # Materialize the NixOS-side public config from the GUI-saved master, THEN
    # switch. The GUI/onboarding writes the domain into config.toml [public] (which
    # palmux2 reads for its own --public-domain), but Caddy's vhost is generated
    # from the `services.palmux.domain` NixOS OPTION — set only via the flake. So
    # this unit projects config.toml's domain into a drop-in (${dropinDir}/10-public.nix)
    # so that the same `nixos-rebuild switch` actually stands up Caddy (TLS + apex
    # SSO + *.domain). Empty domain → remove the drop-in (revert to local mode).
    # The drop-in is written by THIS root unit (the palmux user can't write the
    # root-owned flake dir), keeping palmux2's role to just `systemctl start`.
    script = ''
      set -eu
      cfg=${appCfgDir}/config.toml
      secrets=${appCfgDir}/secrets.env
      domain=""
      if [ -f "$cfg" ]; then
        # Extract `domain = "..."` from the [public] section of palmux's own writer.
        domain=$(sed -n '/^\[public\]/,/^\[/p' "$cfg" \
          | grep -E '^[[:space:]]*domain[[:space:]]*=' \
          | head -n1 | sed -E 's/^[^=]*=[[:space:]]*"?//; s/"[[:space:]]*$//; s/[[:space:]]*$//')
      fi
      mkdir -p ${dropinDir}
      if [ -n "$domain" ]; then
        # Defense-in-depth: only a hostname charset may reach the generated .nix.
        case "$domain" in
          *[!a-zA-Z0-9.-]*) echo "palmux-rebuild: refusing invalid domain '$domain'" >&2; exit 1 ;;
        esac
        # PRE-FLIGHT: enabling a domain firewalls off :7683 and routes ONLY through
        # Caddy, which needs a Cloudflare token for its DNS-01 wildcard cert. With an
        # empty token Caddy can't even start → the box would be unreachable. Refuse
        # BEFORE switching (cheap, no lockout) rather than discover it after.
        if ! grep -qE '^CLOUDFLARE_API_TOKEN=.+' "$secrets" 2>/dev/null; then
          echo "palmux-rebuild: refusing to enable domain '$domain' — CLOUDFLARE_API_TOKEN is empty. Set the Cloudflare API token in deploy settings first (Caddy needs it for the *.$domain cert)." >&2
          exit 1
        fi
        printf '{ ... }: {\n  # Generated from config.toml [public].domain by palmux-rebuild.service.\n  services.palmux.domain = "%s";\n}\n' "$domain" > ${dropinDir}/10-public.nix
      else
        rm -f ${dropinDir}/10-public.nix
      fi
      cd ${flakeDir}
      nixos-rebuild switch --flake .#appliance

      # SAFETY NET: a domain switch closes :7683 and serves only via Caddy. If Caddy
      # can't come up (bad/expired token, DNS, config), the box is locked out — only
      # SSH/console can recover it. Verify Caddy is healthy; if not, undo the domain
      # everywhere and re-switch to the reachable no-domain config, then fail. This
      # keeps a botched domain apply from ever bricking GUI/LAN access (S7364e3
      # transactional-regenerate philosophy, applied to the public-domain flip).
      if [ -n "$domain" ]; then
        sleep 6
        if ! systemctl is-active --quiet caddy; then
          echo "palmux-rebuild: Caddy failed to start under domain '$domain' — rolling back to keep the box reachable (:7683 LAN). Check the Cloudflare token / DNS, then retry." >&2
          rm -f ${dropinDir}/10-public.nix
          sed -i -E '/^[[:space:]]*domain[[:space:]]*=/ s/=.*/= ""/' "$cfg"
          nixos-rebuild switch --flake .#appliance
          exit 1
        fi
      fi
    '';
  };

  # Authorize ONLY `${pUser}` to start ONLY palmux-rebuild.service over the system
  # bus (no password, no wheel). Scoped to that single unit + action so it grants
  # nothing else. polkit is the NixOS-idiomatic equivalent of reconcile-system's
  # single verb-limited sudoers entry.
  security.polkit.enable = lib.mkDefault true;
  security.polkit.extraConfig = lib.mkAfter ''
    polkit.addRule(function(action, subject) {
      if (action.id == "org.freedesktop.systemd1.manage-units" &&
          action.lookup("unit") == "palmux-rebuild.service" &&
          (action.lookup("verb") == "start" || action.lookup("verb") == "restart") &&
          subject.user == "${pUser}") {
        return polkit.Result.YES;
      }
    });
  '';

  # ── login / access — NEVER bake an author/operator key into the image ──────
  # SECURITY: a distributed appliance image MUST ship with ZERO baked SSH keys /
  # passwords. Baking the author's pubkey here would be a backdoor into every
  # deployed PalmuxOS. Access is provisioned by the DEPLOYER at first boot:
  #   1. cloud-init (primary, Proxmox-native): the deployer attaches their own
  #      SSH pubkey via the platform's cloud-init drive → injected on first boot.
  #   2. palmux first-boot onboarding/claim (web): the operator claims the
  #      instance (password / SSO secret / optional key) before anything is
  #      exposed (extends the Sa53137 onboarding wizard).
  #   3. source/flake users put THEIR OWN key in THEIR OWN flake (examples/user-flake).
  # The operator's own key is later layered via /etc/palmux/local/*.nix — AFTER
  # they have claimed access. This module deliberately sets NO authorizedKeys.
  services.openssh.enable = lib.mkDefault true;
  services.openssh.settings.PasswordAuthentication = lib.mkDefault false;
  services.cloud-init.enable = lib.mkDefault true; # first-boot key/user injection from the platform

  # This module sets NO authorizedKeys — that is the whole point. The deployer's
  # key is provisioned at first boot (cloud-init / onboarding) or layered in their
  # own /etc/palmux/local AFTER claiming. So a deployed+customized appliance WILL
  # have the deployer's key; the *distributed image build* must not. That "no
  # baked keys in the shipped artifact" invariant is enforced by an image-build CI
  # check (grep the built image's authorized_keys), NOT by an eval assertion —
  # an assertion can't tell the shipped-image build from an operator's rebuild and
  # would wrongly break legitimate operator customization. TODO(stage3): add the
  # image-build no-baked-keys CI check.

  # NOTE: the OPERATOR DROP-IN import is wired in the ON-APPLIANCE flake shipped to
  # ${flakeDir}/flake.nix (source: nixos/appliance-flake/), which does:
  #     imports = [ palmux.nixosModules.appliance ./hardware-base.nix ./grub-device.nix ]
  #            ++ lib.filesystem.listFilesRecursive ./local;
  # so *.nix dropped into ${dropinDir} are merged with the full NixOS surface +
  # override palmux's mkDefaults on the next `nixos-rebuild switch --flake ${flakeDir}#appliance`.
}
