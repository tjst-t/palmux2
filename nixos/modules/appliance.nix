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

  # ── the on-/persist layout (Sb14caa, docs/nixos-appliance-design.md) ─────────
  # The image ships with TWO partitions (disko, see nixos/modules/disko-layout.nix):
  #   /        ext4 LABEL=nixos    16G fixed  ← Nix store + OS (can't be filled)
  #   /persist ext4 LABEL=persist  rest,last  ← ALL mutable state, autoResize
  # nixos-rebuild's generation switch only swaps /nix/store + /run/current-system, so
  # everything under /persist (state, config, update flake, home, incus storage) is
  # untouched by updates and survives reboots/re-image. `qm resize` + growPartition
  # grow ONLY the last partition (/persist) → user data expands, root stays bounded.
  #
  #   /persist/palmux/nixos/     ← ON-APPLIANCE FLAKE (update + extend via nixos-rebuild)
  #     ├─ flake.nix             pins palmux; nixos-rebuild switch --flake .#appliance
  #     ├─ hardware-base.nix     by-label root(fixed) + /persist(autoResize) + grub
  #     ├─ grub-device.nix       generated on first boot (detected bootsector disk)
  #     └─ local/  *.nix         operator drop-ins (domain, extra pkgs, …)
  #   /persist/palmux/config/    ← palmux2 --config-dir (config.toml + settings.json)
  #     └─ secrets.env           ← SECRETS (CF token / SSO secret / bcrypt), 0600;
  #                                 same file palmux2 reads/writes AND systemd's
  #                                 EnvironmentFile (services.palmux.secretsFile)
  #   /persist/palmux/home/      ← DATA (= palmux $HOME: ~/ghq, ~/.claude, dotfiles)
  #   /persist/incus/storage/    ← incus dir storage pool (container/image volumes)
  flakeDir = "/persist/palmux/nixos";   # on-appliance flake (+ local/ drop-ins)
  dropinDir = "${flakeDir}/local";
  appCfgDir = "/persist/palmux/config"; # palmux2 --config-dir (config.toml + settings.json)
  appFlakeNix = ../appliance-flake/flake.nix;
  appHwBase = ../appliance-flake/hardware-base.nix;

  # Shared `nixos-rebuild switch` body: project the GUI-saved public domain into a
  # drop-in, switch, then a Caddy safety-net that reverts a domain that bricks
  # reachability. Used verbatim by palmux-rebuild.service (apply domain/TLS — pin
  # unchanged) AND, with a `nix flake update palmux` prepended, by
  # palmux-rebuild-update.service (version update — S673a42-2). Factoring keeps the
  # two privileged units in lock-step (the Caddy safety net protects BOTH paths)
  # while staying a single fixed script (no argument reaches root → the polkit
  # authorization remains verb-limited).
  applianceSwitchBody = ''
    set -eu

    # ── S52fc2c-2: persist.mount rc=1 is fixed at the ROOT CAUSE, not here ────
    # switch-to-configuration-ng (NixOS 25.05, Rust) UNCONDITIONALLY *restarts* any
    # changed .mount unit (only `-.mount`=/ and nix.mount are reload-only, hardcoded
    # — verified in its src/main.rs). A restart stop→starts the mount; /persist is
    # always in use, so the umount fails "target is busy" and `nixos-rebuild switch`
    # exits non-zero even though the switch otherwise fully applied → the exit-code-
    # driven update UX shows a false "更新失敗". The ROOT CAUSE is that persist.mount's
    # What= (device) differed between generations because /persist is declared in TWO
    # separate files that disagreed: the disko IMAGE build (disko-layout.nix) baked
    # `by-partlabel/disk-main-persist`, while the ON-BOX system (appliance-flake/
    # hardware-base.nix) declared `by-label/persist`. The FIRST image→flake switch thus
    # saw a changed What= → restart. FIX (S52fc2c-2): hardware-base.nix now references
    # /persist by the SAME by-partlabel id the image bakes, so persist.mount is IDENTICAL
    # across generations → switch-to-configuration never restarts it → `nixos-rebuild
    # switch` returns rc=0 NATIVELY. Verified on a real appliance (switch#1 by-label→
    # by-partlabel reproduced rc=1; switch#2 stable by-partlabel returned rc=0). So this
    # unit just runs a plain switch; a genuine failure still propagates under `set -eu`.
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
        nixos-rebuild switch --flake .#appliance || true   # re-apply reachable no-domain config; report failure below
        exit 1
      fi
    fi
  '';
in
{
  imports = [ ./palmux.nix ];

  # ── palmux defaults suitable for an appliance ──────────────────────────────
  services.palmux.enable = lib.mkDefault true;
  # The palmux $HOME is the conventional /home/ubuntu — NOT the /persist path. The
  # incus workspace runtime and claude-in-container hardcode the container user's
  # home (`/home/ubuntu`: wsHome + `/home/ubuntu/.local/bin/claude`), and same-path
  # bind-mounts mean the host home path must equal it or in-container claude fails
  # with "Command not found". Claude's project-history dir slugs are also derived
  # from the absolute worktree path (~/.claude/projects/-home-ubuntu-ghq-…), so a
  # non-/home/ubuntu home orphans that history too. The DATA still lives on the
  # persist partition (/persist/palmux/home) and palmux-state-init bind-mounts it
  # onto /home/ubuntu, so state survives image swaps. (See the state-init bind and
  # createHome=false below.)
  services.palmux.stateDir = lib.mkDefault "/home/ubuntu";          # $HOME (bind of /persist/palmux/home)
  services.palmux.configDir = lib.mkDefault appCfgDir;               # operator config bundle
  # The systemd EnvironmentFile MUST be the SAME secrets.env that palmux2's
  # --config-dir layer reads/writes (config.WriteSecrets → <configDir>/secrets.env,
  # per the Sa53137 model). Pointing it elsewhere splits secrets across two files:
  # the GUI's RotateSecrets writes <configDir>/secrets.env while systemd loads the
  # other, so a GUI-set SSO secret / password never reaches the process env (and a
  # state-init-generated SSO secret never shows in the GUI). Unify on configDir.
  services.palmux.secretsFile = lib.mkDefault "${appCfgDir}/secrets.env";

  # The claude CLI is the user's migrated native install at ~/.local/bin/claude
  # (= /home/ubuntu/.local/bin, per the home above). Put that dir on:
  #   (a) the palmux2 SERVICE PATH — the Claude Agent tab preflights `claude` with
  #       exec.LookPath on the palmux2 process PATH (claudeagent CheckAuth), and the
  #       host-runtime agent/tui spawn resolves `claude` the same way; without this
  #       the tab shows "Claude Code CLI (`claude`) is not on PATH". (Incus-runtime
  #       claude uses the in-container /home/ubuntu/.local/bin/claude already.)
  #   (b) interactive/login shells — so `claude` typed in a Bash tab / the Host
  #       terminal resolves too.
  # Both then run via nix-ld (below). NB systemd.services.<n>.path appends `/bin`
  # (and `/sbin`) to each entry, so pass the PARENT (/home/ubuntu/.local) to get
  # /home/ubuntu/.local/bin on PATH.
  systemd.services.palmux2.path = [ "/home/ubuntu/.local" ];
  environment.loginShellInit = ''
    case ":$PATH:" in *":$HOME/.local/bin:"*) ;; *) export PATH="$HOME/.local/bin:$PATH" ;; esac
  '';

  # Let generic (non-Nix) dynamically-linked ELF binaries run on the NixOS host —
  # most importantly the user's ~/.local/bin/claude (the downloaded native/glibc
  # Claude Code binary). Without a dynamic loader NixOS aborts these with "Could
  # not start dynamically linked executable", which breaks a HOST-runtime Claude
  # tab (the incus-runtime tab runs claude inside the Ubuntu container and is
  # unaffected). nix-ld installs /lib64/ld-linux + a base library set so the host
  # can run them too, restoring parity with the Ubuntu install path.
  programs.nix-ld.enable = lib.mkDefault true;

  # /home/ubuntu (= services.palmux.stateDir above) is a bind-mount that
  # palmux-state-init sets up on the persistent backing dir; don't let the user
  # module try to create it as a plain directory on the OS root (it would race the
  # mount and, if it won, hide the persistent home behind an empty root-fs dir).
  users.users.${pUser}.createHome = lib.mkForce false;

  # Put the incus `dir` storage pool (container + image volumes — the part that
  # GROWS) on /persist, not its default under /var/lib/incus (the OS root). With
  # root fixed at 16G, a workspace clone/build/image pull must not be able to fill
  # it; on /persist it grows with the data partition instead. The source dir is
  # created by palmux-state-init before incus starts. (mkForce: replace palmux.nix's
  # mkDefault pool list wholesale — lists don't deep-merge.)
  virtualisation.incus.preseed.storage_pools = lib.mkForce [{
    name = "default";
    driver = "dir";
    config.source = "/persist/incus/storage";
  }];

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

  # ── grow /persist (the LAST partition) into free disk space ────────────────
  # NixOS's boot.growPartition + a filesystem's autoResize only grow the ROOT
  # partition (cloud-image convention); nothing grows a non-root last partition. So
  # after a `qm resize`, growpart the /persist partition BEFORE it is mounted, then
  # its autoResize (systemd-growfs) fills the enlarged partition. growpart is
  # idempotent (NOCHANGE → nonzero exit when already max), so this is a no-op when
  # there is nothing to grow. Runs in both the image (gen 1) and the on-appliance
  # flake (gen 2+) since it lives in this shared module.
  systemd.services.palmux-grow-persist = {
    description = "Grow the /persist partition into free disk space";
    wantedBy = [ "persist.mount" ];
    before = [ "persist.mount" ];
    after = [ "local-fs-pre.target" ];
    wants = [ "local-fs-pre.target" ];
    unitConfig.DefaultDependencies = false;
    path = with pkgs; [ cloud-utils util-linux coreutils ];
    serviceConfig.Type = "oneshot";
    script = ''
      set -u
      # Resolve /persist's partition (give udev a moment for the by-label symlink).
      dev=""
      for _ in 1 2 3 4 5; do
        dev=$(readlink -f /dev/disk/by-label/persist 2>/dev/null || true)
        [ -b "$dev" ] && break
        udevadm settle || true; sleep 1
      done
      [ -b "$dev" ] || { echo "grow-persist: /persist device not found, skipping" >&2; exit 0; }
      base=$(basename "$dev")
      disk="/dev/$(lsblk -no PKNAME "$dev" | head -n1)"
      num=$(cat "/sys/class/block/$base/partition" 2>/dev/null || true)
      [ -b "$disk" ] && [ -n "$num" ] || exit 0
      growpart "$disk" "$num" || true   # NOCHANGE => nonzero; that is expected
    '';
  };

  # ── /persist state + on-appliance update flake ─────────────────────────────
  # /persist is its own ext4 partition (LABEL=persist, disko-layout.nix). palmux-
  # state-init creates the durable state subtree under it AND ships the on-appliance
  # flake to /persist/palmux/nixos so the box can `nixos-rebuild switch` to update.
  #
  # Why a oneshot (not systemd.tmpfiles.rules): tmpfiles-setup (ordered only
  # After=local-fs.target) could race ahead of the /persist mount and create the
  # dirs on the underlying root fs, then the mount hides them → palmux2 fails
  # (missing WorkingDirectory/EnvironmentFile, `resources`). A oneshot with
  # RequiresMountsFor=/persist + before/requiredBy palmux2 is deterministic. Idempotent.
  systemd.services.palmux-state-init = {
    description = "Create the palmux /persist state subtree + on-appliance update flake";
    # Before palmux2 (needs config/secrets) AND incus (its dir storage pool lives
    # under /persist/incus so container/image growth can't fill the OS root).
    before = [ "palmux2.service" "incus.service" ];
    requiredBy = [ "palmux2.service" ]; # palmux2 Requires + waits for this
    unitConfig.RequiresMountsFor = "/persist";
    path = with pkgs; [ coreutils util-linux grub2 ];
    serviceConfig = {
      Type = "oneshot";
      RemainAfterExit = true;
      ExecStart = pkgs.writeShellScript "palmux-state-init" ''
        set -eu
        # durable state subtree (palmux2 needs these to start)
        install -d -m 0755 /persist/palmux
        install -d -m 0700 -o ${pUser} -g ${pGroup} /persist/palmux/home
        install -d -m 0750 -o ${pUser} -g ${pGroup} ${appCfgDir}
        # Expose the palmux $HOME at the conventional /home/ubuntu (= stateDir) by
        # bind-mounting the persistent backing dir there. Done here (not via
        # fileSystems) because the backing dir is created just above and this unit
        # is ordered after /persist is mounted and before palmux2 — a fileSystems
        # bind would race the dir's creation on a fresh appliance. Idempotent.
        install -d -m 0755 /home/ubuntu
        mountpoint -q /home/ubuntu || mount --bind /persist/palmux/home /home/ubuntu
        # secrets.env is the SAME file palmux2's --config-dir layer reads/writes
        # (config.WriteSecrets → ${appCfgDir}/secrets.env) AND the systemd
        # EnvironmentFile (services.palmux.secretsFile, unified above). One file.
        secrets=${appCfgDir}/secrets.env
        [ -e "$secrets" ] \
          || install -m 0600 -o ${pUser} -g ${pGroup} /dev/null "$secrets"

        # incus dir storage pool lives on /persist (See virtualisation.incus.preseed
        # override below) so container/image data grows on the data partition, not the
        # OS root. Create its source dir before incus starts.
        install -d -m 0711 /persist/incus /persist/incus/storage

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
        install -d -m 0755 ${flakeDir}
        # S41bdf2: the drop-in dir is palmux-owned so palmux2 (the non-root ${pUser})
        # can write the GUI-generated app package drop-in (local/20-apps.nix) itself,
        # then kick the root palmux-rebuild unit. root (palmux-rebuild.service) still
        # writes local/10-public.nix here too — writing into a user-owned dir as root
        # is fine. The rest of the flake (flake.nix, hardware) stays root-owned.
        install -d -m 0755 -o ${pUser} -g ${pGroup} ${dropinDir}
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
          # S52fc2c-3 (bus-agnostic MBR heal): physically re-install GRUB to the
          # DETECTED disk on first boot. gen1 (the sealed image closure) bakes
          # boot.loader.grub.devices = ["/dev/vda"] — disko's make-disk-image.nix forces
          # the build VM's disk (/dev/vda) into the closure and we cannot override it
          # without breaking the image build (see image-hardware.nix S52fc2c-3 note). On
          # a Proxmox virtio-scsi host the actual disk is /dev/sda, so the GRUB that the
          # image build embedded points at the wrong device name. This first-boot
          # grub-install plants a CORRECT MBR on the real disk regardless of bus type
          # (sda or vda), and the grub-device.nix written just above pins that same disk
          # for gen 2+ `nixos-rebuild switch`. After this, forward generations install
          # GRUB correctly on any hardware. (The one residual case — a `--rollback` to
          # gen1, whose sealed closure still calls grub-install /dev/vda — prints rc=1 on
          # virtio-scsi but is harmless: this planted MBR stays valid and the box boots.)
          # Non-fatal: a failure here just logs; gen2+ use the grub-device.nix disk.
          if [ -b "$disk" ]; then
            # --target=i386-pc is REQUIRED: without it grub-install probes the host
            # platform and (on this BIOS/GPT+EF02 appliance) mis-defaults to
            # i386-ieee1275, failing with "…/i386-ieee1275/modinfo.sh doesn't exist".
            # The appliance is always BIOS-boot (bios EF02 partition in disko-layout),
            # so pin the PC target explicitly. Verified on a fresh appliance (S935ab8-1):
            # `grub-install --no-floppy` → rc=1; `grub-install --target=i386-pc
            # --no-floppy` → "Installation finished. No error reported." rc=0.
            grub-install --target=i386-pc --no-floppy "$disk" 2>&1 \
              || echo "palmux-state-init: grub-install $disk returned non-zero (non-fatal, gen2+ will use correct grub-device.nix)" >&2
          fi
        fi
      '';
    };
  };

  # ── claude CLI fresh-install bootstrap (S61c9a6-3) ─────────────────────────
  # Migration-based deploys carry a pre-existing ~/.local/bin/claude (copied
  # over from the deployer's prior host, per the design assumed everywhere
  # else in this codebase — see internal/runtime/incus/incus.go's bind-mount
  # comment). A genuinely FRESH appliance has none, so the Claude tab is dead
  # on arrival until the operator manually installs it. Rather than curl the
  # official installer script at runtime (unattended `curl | sh` as root on
  # every deployed box — explicitly rejected; see
  # docs/sprint-logs/S61c9a6/verification-S61c9a6-3.md), the binary is
  # fetched + checksum-pinned at Nix BUILD time by
  # nix/packages/claude-code.nix (same pattern as palmux2 itself). This
  # oneshot's only job at boot is to PROJECT that already-fetched Nix-store
  # binary into the conventional ~/.local/bin + ~/.local/share/claude/
  # versions/<v> layout — no network access needed here at all, so unlike
  # the palmux-ws image install (S61c9a6-2, a real ~1GB runtime download)
  # this cannot fail for network reasons. It is still made best-effort
  # (`|| true` throughout, no requiredBy on palmux2.service) on general
  # principle: a first-boot oneshot must never be able to wedge boot.
  #
  # MUST NOT clobber a migrated install, but MUST self-heal / re-link a
  # target that is already OUR OWN prior link (so a future
  # nix/packages/claude-code.nix version bump is picked up on the next boot
  # or `nixos-rebuild switch`, per the comment above). Distinguish the two by
  # resolving the target: if it resolves into THIS package's Nix store
  # output (/nix/store/*-claude-code-*), it is safe — and correct — to
  # always re-link to whatever the CURRENT generation's store path is (same
  # "reconcile drift every run" idea as palmux-incus-reconcile above). Only
  # a target resolving OUTSIDE the Nix store (a real pre-existing migrated
  # binary, or nothing yet) is left untouched / created fresh.
  systemd.services.palmux-claude-bootstrap = {
    description = "Project the Nix-pinned Claude Code CLI into ~/.local for a fresh (non-migrated) install, and keep it in sync with the pinned version";
    after = [ "palmux-state-init.service" ]; # needs the ~/home/ubuntu bind mount up
    wantedBy = [ "multi-user.target" ];
    unitConfig.RequiresMountsFor = "/persist";
    serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
    path = with pkgs; [ coreutils ];
    script =
      let
        claudePkg = pkgs.claude-code;
        home = config.services.palmux.stateDir; # = /home/ubuntu on the appliance
      in
      ''
        set -u
        target="${home}/.local/bin/claude"
        if [ -e "$target" ] || [ -L "$target" ]; then
          resolved=$(readlink -f "$target" 2>/dev/null || true)
          case "$resolved" in
            /nix/store/*-claude-code-*/bin/claude)
              # our own prior link into a (possibly older) claude-code
              # generation — fall through and re-link to the current one.
              ;;
            *)
              echo "palmux-claude-bootstrap: $target already present and does not resolve into this package's Nix store output (migrated install) — leaving untouched"
              exit 0
              ;;
          esac
        fi
        versionDir="${home}/.local/share/claude/versions/${claudePkg.version}"
        install -d -m 0755 -o ${pUser} -g ${pGroup} "${home}/.local/bin" || exit 0
        install -d -m 0755 -o ${pUser} -g ${pGroup} "$versionDir" || exit 0
        # Symlink INTO the Nix store copy (not a file copy) so a future
        # `nixos-rebuild switch` that bumps nix/packages/claude-code.nix's
        # pinned version is picked up automatically on next boot/switch —
        # same "generation swap, no manual reinstall" property as palmux2
        # itself. A genuinely migrated real install (early-exit above) is
        # unaffected; this ln -sfn is a no-op re-link when already current.
        ln -sfn "${claudePkg}/bin/claude" "$versionDir/claude" || exit 0
        ln -sfn "$versionDir/claude" "$target" || exit 0
        chown -h ${pUser}:${pGroup} "$versionDir/claude" "$target" 2>/dev/null || true
        echo "palmux-claude-bootstrap: linked claude ${claudePkg.version} -> $target"
      '';
  };

  # ── keep the pinned Nix-store claude binary from silently self-updating ──
  # Claude Code has a built-in auto-updater that, given network access, will
  # replace the running binary (and its ~/.local/share/claude/versions/<v>
  # dir contents / active symlink) on its own — defeating the entire point
  # of this Story (the original runtime `curl | sh` bootstrap was rejected
  # specifically because it lets unpinned/unverified code run unattended;
  # letting Claude's OWN auto-updater silently replace the checksum-verified
  # Nix-store-pinned binary the first time it runs would reintroduce exactly
  # that hole through the back door). `DISABLE_AUTOUPDATER=1` is Claude
  # Code's documented env var for this (confirmed against the fetched
  # 2.1.211 binary itself — its own env-driven "why is auto-update off"
  # status resolver checks `DISABLE_UPDATES` then `DISABLE_AUTOUPDATER`).
  # Set in TWO places so both paths are covered:
  #   (a) systemd.services.palmux2.environment — the Claude tab (agent/tui,
  #       host runtime) is spawned by palmux2 via exec.Command inheriting
  #       os.Environ() (internal/tab/claudeagent/client.go,
  #       internal/tab/claudetui/daemon.go), so this reaches every
  #       palmux-launched claude process.
  #   (b) environment.variables — covers a user manually invoking `claude`
  #       in an interactive shell (Bash/Host tab, SSH), which does not go
  #       through palmux2's process env at all.
  # (incus-container workspaces are a separate runtime with their own `incus
  # exec --env` list — S4d8b1c scope, not this Story's.)
  systemd.services.palmux2.environment.DISABLE_AUTOUPDATER = "1";
  environment.variables.DISABLE_AUTOUPDATER = "1";

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
    script = applianceSwitchBody;
  };

  # ── GUI-kicked VERSION update (S673a42-2): sibling of palmux-rebuild ──────────
  # palmux-rebuild.service (above) applies the CURRENT flake pin (domain/TLS). This
  # sibling bumps the pin FIRST — `nix flake update palmux` rewrites flake.lock's
  # palmux input to latest main — then runs the identical switch body. It is a
  # SEPARATE fixed unit (not an argument to palmux-rebuild) so the polkit rule that
  # lets the non-root palmux user `start` it stays verb-limited: the palmux user can
  # only start these two named units and cannot inject any command into root. The
  # switch restarts palmux2 onto the new binary/version, which the FE observes via
  # the S6ab0ed WS-drop → /health reconnect handshake. A failure BEFORE the switch
  # (e.g. a flake-update/eval error) leaves the old generation intact (atomic), and
  # the FE catches it by polling this unit's state (GET /api/selfupdate/rebuild).
  systemd.services.palmux-rebuild-update = {
    description = "Update palmux to latest (nix flake update palmux + nixos-rebuild switch) (GUI-triggered)";
    restartIfChanged = false;
    path = [ config.system.build.nixos-rebuild pkgs.nix pkgs.git pkgs.coreutils pkgs.gnugrep pkgs.gnused pkgs.systemd ];
    serviceConfig = {
      Type = "oneshot";
      Environment = "HOME=/root"; # nix + nixos-rebuild write caches under $HOME
    };
    script = ''
      set -eu
      cd ${flakeDir}
      echo "palmux-rebuild-update: bumping palmux pin (nix flake update palmux)…"
      # Bump ONLY the palmux input (not the whole lock) to latest main. nixpkgs et al
      # stay pinned so a version update doesn't drag in an unrelated world rebuild.
      nix flake update palmux
    '' + applianceSwitchBody;
  };

  # Authorize ONLY `${pUser}` to start ONLY the two fixed palmux-rebuild units over
  # the system bus (no password, no wheel). Scoped to those units + start/restart so
  # it grants nothing else. polkit is the NixOS-idiomatic equivalent of
  # reconcile-system's single verb-limited sudoers entry. Both units run a FIXED
  # script (no argument reaches root), so this stays a pure verb-limited grant even
  # though it now covers the version-update sibling (S673a42-2).
  security.polkit.enable = lib.mkDefault true;
  security.polkit.extraConfig = lib.mkAfter ''
    polkit.addRule(function(action, subject) {
      if (action.id == "org.freedesktop.systemd1.manage-units" &&
          (action.lookup("unit") == "palmux-rebuild.service" ||
           action.lookup("unit") == "palmux-rebuild-update.service") &&
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
