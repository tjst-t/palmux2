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
        path = with pkgs; [ tmux git gitMinimal openssh ghq gwq ]
          ++ lib.optional cfg.incus.enable pkgs.incus;
        serviceConfig = {
          User = lib.mkDefault cfg.user;
          WorkingDirectory = lib.mkDefault cfg.stateDir;
          # serve resolves domain/secrets from config.toml [public]; --public-domain
          # stays out unless explicitly needed (mirrors home-manager module).
          ExecStart = lib.mkDefault "${cfg.package}/bin/palmux2 serve --addr=${cfg.bindAddr}";
          EnvironmentFile = lib.mkIf (cfg.secretsFile != null) cfg.secretsFile;
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
            "ipv4.address" = "auto";
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
      # unprivileged + raw.idmap "both 1000 1000" needs a root:1000:1 sub{u,g}id
      # range. NixOS's incus module sets users.users.root.subUidRanges with mkForce
      # (to a huge 1000000:1000000000 range that does NOT include host uid 1000), so
      # adding via subUidRanges is dropped. Append root:1000:1 to the generated
      # /etc/sub{u,g}id after the `users` activation, idempotently (re-runs every
      # switch/boot). incus is restarted once below so it picks the new map up.
      system.activationScripts.palmuxIncusSubid = {
        deps = [ "users" ];
        text = ''
          for f in /etc/subuid /etc/subgid; do
            ${pkgs.gnugrep}/bin/grep -qxF 'root:1000:1' "$f" 2>/dev/null \
              || echo 'root:1000:1' >> "$f"
          done
        '';
      };
      # (On a running system a `systemctl restart incus` is needed after the append
      # so incus re-reads the map; on fresh boot the activation runs before incus
      # starts. TODO(stage2): order an incus restart trigger on the subid file.)
      networking.nftables.enable = lib.mkDefault true; # incus bridge fw
      # palmux-ws image install (`palmux runtime install`) is a runtime step (1GB
      # download), run post-switch by the operator or a oneshot — not declarative.
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
