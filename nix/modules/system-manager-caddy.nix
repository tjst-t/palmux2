# system-manager module: Caddy on the host (Ubuntu/Debian via numtide/system-manager).
#
# Generates:
#   /etc/caddy/Caddyfile       — declarative, Nix-store-backed config
#   /etc/systemd/system/caddy.service — system service running as user 'caddy'
#
# Secrets (NOT in Nix store):
#   /etc/caddy/palmux.env (root:caddy 0640) is written by install.sh with:
#     CLOUDFLARE_API_TOKEN=...
#     BASIC_AUTH_USER=...  (optional)
#     BASIC_AUTH_HASH=...  (optional, bcrypt)
#   The Caddyfile references {env.X} so secret values never enter the Nix store.
#
# The 'caddy' system user/group is created by install.sh before this module
# activates, because system-manager's user management surface on non-NixOS is
# limited.
{ pkgs
, caddy-cloudflare
, domain
, acmeEmail ? null
, basicAuth ? { enable = false; user = null; }
}:
{ config, lib, ... }:
let
  acmeBlock = lib.optionalString (acmeEmail != null && acmeEmail != "") ''
    email ${acmeEmail}
  '';

  basicAuthBlock = lib.optionalString basicAuth.enable ''
    basic_auth {
        {env.BASIC_AUTH_USER} {env.BASIC_AUTH_HASH}
    }
  '';

  caddyfile = pkgs.writeText "Caddyfile" ''
    {
        ${acmeBlock}
    }

    ${domain} {
        ${basicAuthBlock}
        reverse_proxy 127.0.0.1:8080
        tls {
            dns cloudflare {env.CLOUDFLARE_API_TOKEN}
        }
        encode zstd gzip
    }
  '';
in
{
  config = {
    # Required for system-manager to operate on a non-NixOS host (Ubuntu).
    system-manager.allowAnyDistro = true;

    environment.etc."caddy/Caddyfile".source = caddyfile;

    systemd.services.caddy = {
      description = "Caddy web server (with caddy-dns/cloudflare for Let's Encrypt DNS-01)";
      wantedBy = [ "multi-user.target" ];
      after = [ "network-online.target" ];
      wants = [ "network-online.target" ];

      serviceConfig = {
        Type = "notify";
        User = "caddy";
        Group = "caddy";
        # Secrets live outside Nix store (root:caddy 0640).
        EnvironmentFile = "/etc/caddy/palmux.env";
        ExecStart = "${caddy-cloudflare}/bin/caddy run --environ --config /etc/caddy/Caddyfile";
        ExecReload = "${caddy-cloudflare}/bin/caddy reload --config /etc/caddy/Caddyfile --force";
        TimeoutStopSec = "5s";
        LimitNOFILE = 1048576;
        PrivateTmp = true;
        ProtectSystem = "full";
        AmbientCapabilities = [ "CAP_NET_BIND_SERVICE" ];
        Restart = "on-failure";
        RestartSec = 5;
      };
    };
  };
}
