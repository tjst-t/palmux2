# home-manager module: palmux2 ユーザ systemd サービス + shell 環境。
#
# bindAddr は呼び出し側 (mkPalmuxHost) が host.caddy.enable から決定:
#   - Caddy 無効 → 0.0.0.0:8080  (LAN から直接アクセス)
#   - Caddy 有効 → 127.0.0.1:8080 (Caddy のみが proxy)
#
# publicDomain (See8bd4-1): Caddy ワイルドカード + admin API が有効なとき、
#   mkPalmuxHost が domain を渡す。セットされると:
#   - ExecStart に --public-domain <domain> が追加される (port publishing 有効化)
#   - EnvironmentFile = /etc/palmux/runtime.env が追加される
#     (BASIC_AUTH_USER / BASIC_AUTH_HASH を palmux2 プロセスに渡す)
#   /etc/palmux/runtime.env は install.sh が root:<username> 0640 で書く
#   (/etc/caddy/palmux.env は root:caddy 0640 で palmux2 ユーザが読めないため)。
#
# profileName=="full" の時、 programs.* で shell-UX cluster (starship, fzf,
# zoxide, eza, bat, git-delta) を enable して bash 統合を自動セットアップ。
{ palmux2-pkg
, profilePackages
, profileName ? "minimal"
, username
, homeDirectory
, bindAddr ? "0.0.0.0:8080"
, stateVersion ? "24.11"
  # See8bd4-1: public base domain for wildcard subdomain port publishing.
  # null = port publishing disabled (no --public-domain flag).
, publicDomain ? null
}:
{ config, lib, pkgs, ... }:
let
  caddyEnabled = publicDomain != null;

  # /etc/palmux/runtime.env is written by install.sh with:
  #   PALMUX_PUBLIC_DOMAIN=<domain>
  #   BASIC_AUTH_USER=<user>      (when basicAuth.enable)
  #   BASIC_AUTH_HASH=<bcrypt>    (when basicAuth.enable)
  # The file is root:<username> 0640 so the palmux2 user service can read it
  # without giving caddy-group access to the bcrypt hash and without leaking
  # secrets into the Nix store.
  runtimeEnvFile = "/etc/palmux/runtime.env";
in
{
  config = lib.mkMerge [
    {
      home = {
        inherit username homeDirectory stateVersion;
        packages = profilePackages;
        sessionPath = [ "/usr/local/bin" "$HOME/.local/bin" ];
      };

      programs.bash.enable = true;

      # tmux: mouse wheel で履歴を見られるようにする。
      # programs.tmux.enable=true は tmux を home.packages にも追加するため
      # minimal/full profile から tmux を外して duplicate を避ける。
      programs.tmux = {
        enable = true;
        mouse = true;
        terminal = "tmux-256color";
        historyLimit = 50000;
        keyMode = "emacs";
      };

      systemd.user.services.palmux2 = {
        Unit = {
          Description = "palmux2 — web-based tmux client";
          After = [ "default.target" ];
        };

        Service = {
          Type = "simple";
          # Sa53137-2: config-driven launch. `serve` reads the user-owned master
          # (~/.config/palmux/config.toml + secrets.env) written by install.sh.
          # --addr is kept as an explicit flag (it wins over the file) so the
          # bind address is pinned regardless of the master; the public domain
          # now comes from config.toml [public].domain, so --public-domain is no
          # longer baked into the unit (changing the domain via the GUI /
          # `palmux apply` no longer needs a home-manager switch).
          ExecStart = "${palmux2-pkg}/bin/palmux2 serve --addr=${bindAddr}";
          Restart = "on-failure";
          RestartSec = 5;
          Environment = [
            "PATH=/usr/local/bin:%h/.local/bin:/run/current-system/sw/bin:%h/.nix-profile/bin:/usr/bin:/bin"
          ];
        } // lib.optionalAttrs caddyEnabled {
          # Back-compat: still source /etc/palmux/runtime.env when present so an
          # install that has not re-run install.sh (no config.toml yet) keeps its
          # secrets. The user-owned secrets.env supersedes it once written; env
          # values win over the file only for those keys, which is harmless since
          # install.sh writes identical values to both.
          EnvironmentFile = runtimeEnvFile;
        };

        Install = {
          WantedBy = [ "default.target" ];
        };
      };

      # Sa8e7d0-1: dedicated update unit, INDEPENDENT of palmux2.service.
      #
      # The 2026-06-20 incident: the GUI/CLI "Update" used to run
      # ~/update-palmux2.sh as a detached child of palmux2.service. Its
      # `home-manager switch` STOPS palmux2.service to apply the new generation;
      # systemd's control-group kill of palmux2 took the in-flight helper down
      # with it → the update died mid-way (binary updated, image stale, palmux2
      # never restarted → 502 + a re-Update loop).
      #
      # Fix: hold the update logic in its OWN oneshot user unit. It has its own
      # cgroup, so stopping/restarting palmux2.service during the switch does NOT
      # kill it — it completes (home-manager switch + `palmux runtime install`
      # image + palmux2 restart). The GUI/CLI now just `systemctl --user start
      # palmux-update[.service]` instead of running the helper in-process.
      #
      # ExecStart runs the install.sh-generated ~/update-palmux2.sh (flake re-pin
      # → home-manager switch → image install → restart). Type=oneshot so
      # `systemctl --user start --wait` (CLI) blocks until done and propagates
      # failure as a non-zero job result.
      systemd.user.services.palmux-update = {
        Unit = {
          Description = "palmux2 self-update (independent of palmux2.service so the update is not killed when home-manager switch restarts palmux2)";
          # Do NOT bind to palmux2.service: this unit must survive palmux2 being
          # stopped/restarted by the very update it runs.
        };

        Service = {
          Type = "oneshot";
          # Run in bash so the generated helper's shebang/set -euo pipefail apply.
          # %h is the user's home; the helper is written there by install.sh.
          ExecStart = "${pkgs.bash}/bin/bash %h/update-palmux2.sh";
          # Generous timeout: a first-run home-manager switch + image download can
          # take several minutes. 0 = no timeout would risk a hung switch wedging
          # the unit; 30m is comfortably above the worst observed switch+download.
          TimeoutStartSec = "30min";
          # Keep the helper's PATH consistent with palmux2.service so `palmux2
          # runtime install`, home-manager, nix, incus all resolve.
          Environment = [
            "PATH=/usr/local/bin:%h/.local/bin:/run/current-system/sw/bin:%h/.nix-profile/bin:/usr/bin:/bin"
          ];
        };

        # No Install/WantedBy: this unit is started on demand only (by the GUI/CLI
        # Update), never auto-started at login.
      };
    }

    (lib.mkIf (profileName == "full") {
      programs.starship.enable = true;
      programs.fzf.enable = true;
      programs.zoxide.enable = true;
      programs.eza.enable = true;
      programs.bat.enable = true;
      programs.git = {
        enable = true;
        delta.enable = true;
      };
    })
  ];
}
