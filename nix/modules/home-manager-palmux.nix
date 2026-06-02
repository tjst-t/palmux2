# home-manager module: palmux2 ユーザ systemd サービス + 環境変数 PATH 拡張。
#
# bindAddr は呼び出し側 (mkPalmuxHost) が host.caddy.enable から決定:
#   - Caddy 無効 → 0.0.0.0:8080  (LAN から直接アクセス)
#   - Caddy 有効 → 127.0.0.1:8080 (Caddy のみが proxy)
{ palmux2-pkg
, profilePackages
, username
, homeDirectory
, bindAddr ? "0.0.0.0:8080"
, stateVersion ? "24.11"
,
}:
{ config, lib, pkgs, ... }:
{
  home = {
    inherit username homeDirectory stateVersion;
    packages = profilePackages;

    # gwq / port-manager / claude は install.sh が ~/.local/bin か /usr/local/bin に置く
    sessionPath = [ "/usr/local/bin" "$HOME/.local/bin" ];
  };

  # gwq / port-manager 等 Nix 外バイナリも PATH で見つかるように
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
      # palmux2 自身が tmux / git を spawn するので PATH を整える
      Environment = [
        "PATH=/usr/local/bin:%h/.local/bin:/run/current-system/sw/bin:%h/.nix-profile/bin:/usr/bin:/bin"
      ];
    };

    Install = {
      WantedBy = [ "default.target" ];
    };
  };
}
