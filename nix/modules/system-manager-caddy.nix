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
#
# See8bd4-1: admin API + wildcard TLS
#   - Global options enable the admin API on localhost:2019 (never public-facing).
#   - A *.${domain} wildcard site is added on :443.  Caddyfile merges all :443
#     sites into a single server (srv0), so palmux can PATCH routes into srv0 via
#     the admin API.  The wildcard default handler returns 502 "no upstream" when
#     palmux has not injected a route for the requested subdomain — this prevents
#     accidental data leakage.
#   - basic_auth is NOT placed on the static wildcard site; palmux injects it
#     per-route via the admin API (D3 / D7).
#   - The apex vhost keeps its basic_auth + reverse_proxy unchanged.
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
        # Expose the Caddy admin API on localhost only (See8bd4-1 AC-1).
        # palmux uses this to inject per-port reverse_proxy routes at runtime.
        # Never bind to 0.0.0.0 — the admin API has no authentication.
        admin localhost:2019
        ${acmeBlock}
    }

    # ── Apex vhost ──────────────────────────────────────────────────────────────
    # Serves the palmux2 SPA.  basic_auth protects the entire site when enabled.
    # TLS certificate is obtained via Cloudflare DNS-01 (required for wildcards).
    ${domain} {
        ${basicAuthBlock}
        reverse_proxy 127.0.0.1:8080
        tls {
            dns cloudflare {env.CLOUDFLARE_API_TOKEN}
        }
        encode zstd gzip
    }

    # ── Wildcard subdomain vhost (See8bd4-1) ────────────────────────────────────
    # *.${domain} shares the :443 listener with the apex vhost above.  The
    # Caddyfile adapter merges both into a single Caddy server (srv0), which
    # is the server palmux patches routes into via the admin API.
    #
    # DNS prerequisite: *.${domain} must be a DNS wildcard (or individual A/AAAA
    # records) pointing to this host — HTTP-01 cannot validate wildcards; the
    # DNS-01 challenge below is required.
    #
    # Default handler: when palmux has injected no route for a given subdomain,
    # Caddy falls through to this respond directive and returns 502 "no upstream".
    # basic_auth is intentionally absent here — palmux injects it per-route (D3).
    *.${domain} {
        tls {
            dns cloudflare {env.CLOUDFLARE_API_TOKEN}
        }
        respond "no upstream" 502
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
