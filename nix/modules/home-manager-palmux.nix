# home-manager module: palmux2 ユーザ systemd サービス + shell 環境。
#
# bindAddr は呼び出し側 (mkPalmuxHost) が host.caddy.enable から決定:
#   - Caddy 無効 → 0.0.0.0:8080  (LAN から直接アクセス)
#   - Caddy 有効 → 127.0.0.1:8080 (Caddy のみが proxy)
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
,
}:
{ config, lib, pkgs, ... }:
{
  config = lib.mkMerge [
    {
      home = {
        inherit username homeDirectory stateVersion;
        packages = profilePackages;
        sessionPath = [ "/usr/local/bin" "$HOME/.local/bin" ];
      };

      programs.bash.enable = true;

      systemd.user.services.palmux2 = {
        Unit = {
          Description = "palmux2 — web-based tmux client";
          After = [ "default.target" ];
        };

        Service = {
          Type = "simple";
          ExecStart = "${palmux2-pkg}/bin/palmux2 --addr=${bindAddr}";
          Restart = "on-failure";
          RestartSec = 5;
          Environment = [
            "PATH=/usr/local/bin:%h/.local/bin:/run/current-system/sw/bin:%h/.nix-profile/bin:/usr/bin:/bin"
          ];
        };

        Install = {
          WantedBy = [ "default.target" ];
        };
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
