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
      networking.firewall.allowedTCPPorts = lib.mkIf cfg.openFirewall (lib.mkDefault [ 80 443 ]);
      services.caddy = {
        enable = lib.mkDefault true;
        # TODO(stage1): Cloudflare DNS-01 needs the caddy-cloudflare build
        # (nix/packages/caddy-cloudflare.nix) as services.caddy.package for the
        # wildcard cert. Apex = forward_auth → palmux /auth/verify (SSO);
        # *.domain = wildcard 502 catch-all that palmux's admin-API routes front.
        virtualHosts.${cfg.domain}.extraConfig = lib.mkDefault ''
          forward_auth ${cfg.bindAddr} {
            uri /auth/verify
            copy_headers X-Palmux-User
          }
          reverse_proxy ${cfg.bindAddr}
        '';
        virtualHosts."*.${cfg.domain}".extraConfig = lib.mkDefault ''
          # palmux injects per-port subroutes via the Caddy admin API; this is the
          # 502 catch-all evaluated after them. (See See8bd4.)
          respond "no route" 502
        '';
      };
    })
  ]);
}
