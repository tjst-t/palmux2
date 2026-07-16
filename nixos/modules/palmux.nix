# nixos/modules/palmux.nix
#
# The reusable palmux host NixOS module. Importing it turns a NixOS machine into a
# palmux host: the palmux2 service, the incus workspace runtime, and the Caddy
# reverse proxy (+ optional SSO) are configured from a single `services.palmux.*`
# option set.
#
# Design rules:
#   - EVERY config value palmux sets is `lib.mkDefault`, so an operator (appliance
#     drop-in) or composing flake (source) overrides it with a plain assignment —
#     no mkForce needed. This is what makes the appliance user-extensible.
#   - Reuses nix/packages/palmux2.nix (the release-asset derivation) as the binary.
#   - On NixOS, incus + caddy are first-class, so this module is smaller than the
#     Ubuntu install.sh equivalent.
#
# NOTE: scaffold — not yet eval-checked. Items marked TODO(stageN) are validated in
# the corresponding stage of docs/nixos-appliance-design.md.
{ config, lib, pkgs, ... }:
let
  cfg = config.services.palmux;
in {
  options.services.palmux = {
    enable = lib.mkEnableOption "palmux2 web tmux/Claude client host";

    package = lib.mkOption {
      type = lib.types.package;
      default = pkgs.palmux2 or (throw "services.palmux.package must be set (overlay nix/packages/palmux2.nix as pkgs.palmux2, or set it explicitly)");
      defaultText = lib.literalExpression "pkgs.palmux2";
      description = "The palmux2 package (nix/packages/palmux2.nix).";
    };

    user = lib.mkOption {
      type = lib.types.str;
      default = "palmux";
      description = ''
        The user palmux2 runs as. Owns ~/ghq, ~/.claude, ~/.config/palmux and is in
        the incus-admin group. On the appliance this is the operator login.
      '';
    };

    stateDir = lib.mkOption {
      type = lib.types.path;
      default = "/var/lib/palmux";
      description = "Home of the palmux user (repos, Claude records, config live here or are bind-mounted here from /persist).";
    };

    bindAddr = lib.mkOption {
      type = lib.types.str;
      default = "127.0.0.1:7683";
      description = "Address palmux2 binds (Caddy reverse-proxies to it).";
    };

    configDir = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      example = "/persist/palmux/config/app";
      description = ''
        palmux2 `--config-dir` (config.toml + settings.json live here). null = the
        palmux2 default (~/.config/palmux). On the appliance this points into the
        OPERATOR CONFIG BUNDLE on /persist (config/app) so the operator's settings
        are a separated, backup-/restore-able file set, distinct from the immutable
        PalmuxOS core. See docs/nixos-appliance-design.md §運用者コンフィグ束.
      '';
    };

    domain = lib.mkOption {
      type = lib.types.nullOr lib.types.str;
      default = null;
      example = "dev.example.net";
      description = ''
        Public apex domain. When set, Caddy serves the apex (SSO) + a
        `*.<domain>` wildcard for incus port subdomains, and palmux2 runs with
        --public-domain (SSO + port publishing). null = local/loopback only.
      '';
    };

    secretsFile = lib.mkOption {
      type = lib.types.nullOr lib.types.path;
      default = null;
      description = ''
        Path to an EnvironmentFile (0600) with PALMUX_SSO_SECRET / BASIC_AUTH_HASH /
        CLOUDFLARE_API_TOKEN etc. Never put secrets in the Nix store — point this at
        a path on the persistent volume (e.g. /persist/palmux/secrets.env).
      '';
    };

    incus.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Enable the incus-container workspace runtime (virtualisation.incus + idmap prereqs).";
    };

    caddy.enable = lib.mkOption {
      type = lib.types.bool;
      default = true;
      description = "Front palmux2 with Caddy (TLS, SSO forward_auth, wildcard port subdomains).";
    };

    openFirewall = lib.mkOption {
      type = lib.types.bool;
      default = cfg.domain != null;
      defaultText = lib.literalExpression "config.services.palmux.domain != null";
      description = "Open 80/443 for Caddy when a public domain is configured.";
    };
  };

  config = lib.mkIf cfg.enable (lib.mkMerge [
    ##########################################################################
    # palmux user + the palmux2 service
    ##########################################################################
    {
      # plain (not mkDefault): environment.systemPackages is a list — a mkDefault
      # definition loses entirely to any plain-assigned list elsewhere (same trap
      # as allowedTCPPorts below) instead of merging, so the palmux CLI would
      # silently vanish from PATH. Plain concatenates with the rest of the system's
      # systemPackages, giving operators `palmux`/`palmux2` on the interactive
      # shell for `palmux runtime install` / `palmux runtime doctor` etc.
      environment.systemPackages = [ cfg.package ];

      users.users.${cfg.user} = {
        isNormalUser = lib.mkDefault true;
        # plain (not mkDefault): derived from the stateDir option, and must win
        # over NixOS's isNormalUser default home (/home/<user>), which is also
        # mkDefault — two mkDefaults would conflict.
        home = cfg.stateDir;
        createHome = lib.mkDefault true;
        # incus-admin so palmux2 can drive the workspace runtime; see
        # project memory: missing group → silent host rollback.
        extraGroups = lib.mkDefault (lib.optional cfg.incus.enable "incus-admin");
        linger = lib.mkDefault true;
      };

      systemd.services.palmux2 = {
        description = "palmux2 — web-based tmux/Claude client";
        wantedBy = [ "multi-user.target" ];
        after = [ "network-online.target" ] ++ lib.optional cfg.incus.enable "incus.service";
        wants = [ "network-online.target" ];
        # palmux2 requires tmux/ghq/gwq/git on PATH at startup, and shells out to
        # incus when the container runtime is enabled. gwq comes from the overlay
        # (nix/packages/gwq.nix); ghq is in nixpkgs.
        # NB: use the SAME incus as the daemon (config.virtualisation.incus.package,
        # = incus-lts), not pkgs.incus — otherwise palmux2's CLI pulls a second,
        # different incus build into the closure (~217MB of pure duplication) and
        # talks to the daemon with a mismatched client version.
        # config.nix.package puts `nix` on the service PATH so the app-card nixpkgs
        # validation (S41bdf2 `nix eval`) resolves natively even under this restricted
        # service PATH (AC-S41bdf2-4-1). Belt-and-braces: the binary also falls back to
        # /run/current-system/sw/bin/nix so older/unrebuilt hosts still validate.
        path = (with pkgs; [ tmux gitMinimal openssh ghq gwq ])
          ++ [ config.nix.package ]
          ++ lib.optional cfg.incus.enable config.virtualisation.incus.package;
        serviceConfig = {
          User = lib.mkDefault cfg.user;
          WorkingDirectory = lib.mkDefault cfg.stateDir;
          # serve resolves domain/secrets from config.toml [public]; --public-domain
          # stays out unless explicitly needed (mirrors home-manager module).
          ExecStart = lib.mkDefault ("${cfg.package}/bin/palmux2 serve --addr=${cfg.bindAddr}"
            + lib.optionalString (cfg.configDir != null) " --config-dir=${cfg.configDir}");
          # `-` prefix: tolerate a missing/not-yet-created secrets file instead of
          # failing the whole service (systemd `resources` error). On the appliance
          # palmux-state-init creates it before this runs; this is belt-and-braces.
          EnvironmentFile = lib.mkIf (cfg.secretsFile != null) "-${cfg.secretsFile}";
          Restart = lib.mkDefault "on-failure";
          RestartSec = lib.mkDefault 2;
        };
        environment = lib.mkMerge [
          (lib.mkIf (cfg.domain != null) { PALMUX_PUBLIC_DOMAIN = lib.mkDefault cfg.domain; })
        ];
      };
    }

    ##########################################################################
    # incus workspace runtime  (TODO(stage2): validate idmap + image install)
    ##########################################################################
    (lib.mkIf cfg.incus.enable {
      virtualisation.incus.enable = lib.mkDefault true;
      # Declarative incus init: a managed incusbr0 bridge (palmux's expected bridge,
      # See8bd4) + a dir storage pool + default profile. Without a preseed, incus
      # has no bridge/pool and containers can't launch.
      virtualisation.incus.preseed = lib.mkDefault {
        networks = [{
          name = "incusbr0";
          type = "bridge";
          config = {
            # STATIC, not "auto": the preseed is re-applied on every incus
            # (re)start, and "auto" re-randomises the subnet each time — the bridge
            # address keeps changing and containers never get a stable DHCP lease
            # (observed: 10.141→10.1.207→… and no container IP). A fixed address is
            # idempotent across re-applies.
            "ipv4.address" = "10.100.50.1/24";
            "ipv4.nat" = "true";
            "ipv6.address" = "none";
          };
        }];
        storage_pools = [{
          name = "default";
          driver = "dir";
        }];
        profiles = [{
          name = "default";
          devices = {
            eth0 = { name = "eth0"; network = "incusbr0"; type = "nic"; };
            root = { path = "/"; pool = "default"; type = "disk"; };
          };
        }];
      };
      # `incus admin init --preseed` is effectively FIRST-RUN-ONLY: against a daemon
      # that is already initialised it silently skips objects that already exist and
      # never backfills their sub-config. The appliance image bakes /var/lib/incus
      # (on the immutable root) with an empty `default` profile at image-build time,
      # and image regeneration (S7364e3) resets it the same way — so on first boot
      # the preseed created the storage pool (was missing) but left the default
      # profile with NO devices and never created incusbr0. Result: `incus init
      # palmux-ws` fails "No root device could be found" and no workspace can launch
      # (observed on the first palmuxOS appliance, Sb14caa-5). Reconcile the intended
      # pool/network/profile idempotently on every boot (create-if-missing), so a
      # fresh, image-baked, or regenerated daemon all converge to a launch-ready
      # state. Self-heal, same philosophy as palmux's Caddy-route resync (See8bd4).
      systemd.services.palmux-incus-reconcile = {
        description = "Ensure incus default profile/network/pool can launch workspaces";
        after = [ "incus.service" "incus-preseed.service" ];
        requires = [ "incus.service" ];
        wantedBy = [ "multi-user.target" ];
        serviceConfig = { Type = "oneshot"; RemainAfterExit = true; };
        path = [ config.virtualisation.incus.package ];
        script = ''
          set -eu
          # dir storage pool — source on /persist so container/image blobs survive
          # image swaps (the metadata DB on /var/lib/incus does not, hence reconcile).
          incus storage show default >/dev/null 2>&1 \
            || incus storage create default dir source=/persist/incus/storage
          # bridge — static subnet MUST match the preseed above (idempotent on re-apply).
          incus network show incusbr0 >/dev/null 2>&1 \
            || incus network create incusbr0 ipv4.address=10.100.50.1/24 ipv4.nat=true ipv6.address=none
          # default profile devices (root disk + nic) — the part preseed won't backfill.
          incus profile device list default 2>/dev/null | grep -qx root \
            || incus profile device add default root disk pool=default path=/
          incus profile device list default 2>/dev/null | grep -qx eth0 \
            || incus profile device add default eth0 nic network=incusbr0 name=eth0
        '';
      };
      # unprivileged + raw.idmap "both 1000 1000" needs a root:1000:1 sub{u,g}id
      # range. NixOS's incus module sets users.users.root.subUidRanges with mkForce
      # (to a huge 1000000:1000000000 range that does NOT include host uid 1000), so
      # adding via subUidRanges is dropped. Append root:1000:1 to the generated
      # /etc/sub{u,g}id after the `users` activation, idempotently (re-runs every
      # switch/boot). If we actually appended (i.e. the map changed) AND incus is
      # already running (a `nixos-rebuild switch`, not first boot), restart it so it
      # re-reads the map — otherwise an idmapped container launch fails until a manual
      # restart. On first boot the activation runs before incus starts, so the
      # try-restart is a harmless no-op there.
      system.activationScripts.palmuxIncusSubid = {
        deps = [ "users" ];
        text = ''
          changed=0
          for f in /etc/subuid /etc/subgid; do
            ${pkgs.gnugrep}/bin/grep -qxF 'root:1000:1' "$f" 2>/dev/null \
              || { echo 'root:1000:1' >> "$f"; changed=1; }
          done
          if [ "$changed" = 1 ] && [ -d /run/systemd/system ]; then
            ${pkgs.systemd}/bin/systemctl try-restart incus.service 2>/dev/null || true
          fi
        '';
      };
      networking.nftables.enable = lib.mkDefault true; # incus bridge fw
      # Trust the incus bridge: the firewall's input chain drops conntrack-invalid
      # packets (`ct state invalid : drop`) BEFORE the per-port accepts, and a
      # container's DHCP DISCOVER (src 0.0.0.0, broadcast) is classed invalid — so
      # it's dropped before reaching dnsmasq and the container never leases. Trusted
      # interfaces are accepted early, before the conntrack check. (plain list — it
      # must merge, not mkDefault.)
      networking.firewall.trustedInterfaces = [ "incusbr0" ];
      # Don't run the host firewall on L2-bridged frames. With br_netfilter's
      # bridge-nf-call-iptables=1 (its default once loaded), a workspace
      # container's DHCP DISCOVER/OFFER across incusbr0 traverses nftables and is
      # dropped — the container never gets a lease (no IP → no DNS → "couldn't
      # resolve host" inside the workspace). NAT for container→internet is routed
      # (postrouting masquerade), not bridged, so this does not affect it.
      boot.kernel.sysctl = {
        "net.bridge.bridge-nf-call-iptables" = 0;
        "net.bridge.bridge-nf-call-ip6tables" = 0;
        "net.bridge.bridge-nf-call-arptables" = 0;
        # Route container→internet. incus adds the masquerade rule for ipv4.nat but
        # NixOS does not enable IP forwarding by default, so packets from the
        # workspace container never leave the host (DNS resolves via the host's
        # dnsmasq, but TCP to the internet times out) without this.
        "net.ipv4.ip_forward" = 1;
      };
      # palmux-ws image install (S61c9a6-2): first boot has an empty incus image
      # store, so the incus-container Workspace runtime is unusable until an
      # operator manually runs `palmux2 runtime install`. Automate it as a
      # best-effort oneshot, same Type/After shape as palmux-incus-reconcile
      # above — with ONE deliberate difference, `unitConfig.DefaultDependencies
      # = false`, which is NOT optional here. Measured on a real boot (2026-07-16,
      # dev-host qemu smoke): a plain `Type = "oneshot"; wantedBy = [
      # "multi-user.target" ];` unit (DefaultDependencies=yes, systemd's
      # default) gets an AUTOMATIC `Before=multi-user.target` — this is
      # standard systemd behaviour for every unit enabled this way (verified:
      # palmux2.service, palmux-incus-reconcile.service, and even stock
      # sshd.service all carry the same implicit Before=), so it is not itself
      # a bug. But for `Type=oneshot`, "the start job is done" means "ExecStart
      # exited" — unlike Type=simple/notify, where the job resolves near-
      # instantly on fork/sd_notify. Combined, multi-user.target's own job does
      # not resolve until THIS unit's ExecStart exits. palmux-incus-reconcile
      # gets away with the same default shape because its ExecStart is local-
      # only and sub-second; ours runs `palmux2 runtime install`, which
      # downloads a ~1GB release asset — `systemd-analyze critical-chain`
      # showed multi-user.target reached only after 5min14s, ENTIRELY gated on
      # this unit's ~5min download (`palmux-ws-image-install.service @17.232s
      # +4min 56.963s` on the critical chain to multi-user.target). That is
      # exactly the boot-blocking behaviour AC-S61c9a6-2-2 forbids — a slow (or
      # hung, e.g. a captive portal that accepts but never completes the TCP
      # connection) network measurably delays boot completion, not just an
      # outright fast failure. `DefaultDependencies = false` drops the
      # implicit Before=multi-user.target/shutdown.target entirely (same fix
      # already used for palmux-grow-persist in appliance.nix, for an
      # unrelated ordering reason) — multi-user.target then starts this unit
      # concurrently via `Wants=` without waiting on it, so its success,
      # failure, OR duration can never delay boot. The explicit `after`
      # entries below are retained (independent of DefaultDependencies) so the
      # unit itself still waits for incus / the reconcile profile / network
      # before it tries to run — that ordering only constrains when THIS unit
      # starts, not when multi-user.target is considered reached.
      #
      # `palmux2 runtime install` is not itself a cheap no-op when the image is
      # already present (it re-downloads the ~1GB release asset every
      # invocation before importing), so guard it here: skip the download
      # entirely once the `palmux-ws` alias already resolves to an image. This
      # still converges an image-less host on every reboot (self-heal, same
      # philosophy as the reconcile unit) without repeatedly paying a ~1GB
      # download once installed. Image *upgrades* are handled separately by
      # the running palmux2 service (S7364e3 drift-detect + regenerate), not
      # by this first-install oneshot.
      systemd.services.palmux-ws-image-install = {
        description = "Install the palmux-ws workspace image into incus (best-effort, first-boot only)";
        after = [ "incus.service" "palmux-incus-reconcile.service" "network-online.target" ];
        wants = [ "network-online.target" ];
        requires = [ "incus.service" ];
        wantedBy = [ "multi-user.target" ];
        # See the long comment above: this is the load-bearing fix that keeps
        # a slow/hung download from delaying multi-user.target.
        unitConfig.DefaultDependencies = false;
        serviceConfig = {
          Type = "oneshot";
          RemainAfterExit = true;
          # Run as the palmux user (already in incus-admin when cfg.incus.enable,
          # see users.users.${cfg.user}.extraGroups above) rather than root —
          # mirrors how scripts/install.sh runs `palmux runtime install` as the
          # install user via `sg incus-admin`, not as root.
          User = lib.mkDefault cfg.user;
        };
        path = [ config.virtualisation.incus.package ];
        script = ''
          set -eu
          if incus image alias list --format csv 2>/dev/null | cut -d, -f1 | grep -qx palmux-ws; then
            echo "palmux-ws image already installed — skipping"
            exit 0
          fi
          exec ${cfg.package}/bin/palmux2 runtime install
        '';
      };
    })

    ##########################################################################
    # Caddy front (TLS + SSO + wildcard port subdomains)
    ##########################################################################
    (lib.mkIf (cfg.caddy.enable && cfg.domain != null) {
      # plain (not mkDefault): allowedTCPPorts is a list — an mkDefault definition
      # loses entirely to plain ones (e.g. openssh's [22]) instead of merging, so
      # 80/443 would silently drop. Plain concatenates → [22 80 443].
      networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall [ 80 443 ];
      services.caddy = {
        enable = lib.mkDefault true;
        # Caddy built with the Cloudflare DNS plugin (DNS-01 wildcard cert).
        package = lib.mkDefault pkgs.caddy-cloudflare;
        # palmux injects per-port subdomain routes via the admin API (See8bd4).
        globalConfig = lib.mkDefault "admin localhost:2019";
        # Apex = SSO (forward_auth → palmux /auth/verify), with /auth/* bypassed so
        # the login page is reachable. Mirrors the reconcile-system render (Sbe4eee).
        virtualHosts.${cfg.domain}.extraConfig = lib.mkDefault ''
          @palmux_auth path /auth/*
          handle @palmux_auth {
            reverse_proxy ${cfg.bindAddr}
          }
          handle {
            forward_auth ${cfg.bindAddr} {
              uri /auth/verify
            }
            reverse_proxy ${cfg.bindAddr}
          }
          tls {
            dns cloudflare {env.CLOUDFLARE_API_TOKEN}
          }
          encode zstd gzip
        '';
        # *.domain = wildcard (DNS-01 cert) + 502 catch-all that palmux's
        # admin-API per-port routes are inserted ahead of.
        virtualHosts."*.${cfg.domain}".extraConfig = lib.mkDefault ''
          tls {
            dns cloudflare {env.CLOUDFLARE_API_TOKEN}
          }
          respond "no upstream" 502
        '';
      };
      # Caddy reads CLOUDFLARE_API_TOKEN (for DNS-01) from the secrets file; systemd
      # reads the EnvironmentFile as root before dropping to the caddy user.
      systemd.services.caddy.serviceConfig = {
        EnvironmentFile = lib.mkIf (cfg.secretsFile != null) cfg.secretsFile;
        # caddy runs as a non-root user with NoNewPrivileges — grant the capability
        # to bind :80/:443 (the package override drops the module's default here).
        AmbientCapabilities = lib.mkForce [ "CAP_NET_BIND_SERVICE" ];
        CapabilityBoundingSet = lib.mkForce [ "CAP_NET_BIND_SERVICE" ];
      };
    })
  ]);
}
