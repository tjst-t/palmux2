# mkPalmuxHost: host-specific /etc/palmux/flake.nix が呼ぶ entry point。
#
# 公開パラメータを受け取り、 home-manager + (Story-2/-3 で) system-manager の
# configuration を出力する。 秘密 (CF token / basic_auth password hash) は
# /etc/caddy/palmux.env で注入し、 ここには現れない。
{ inputs }:

{ system ? "x86_64-linux"
, username
, homeDirectory ? "/home/${username}"
, hostname ? "palmux-host"

  # Caddy / HTTPS / basic_auth は Story-2 で追加
, domain ? null
, acmeEmail ? null
, basicAuth ? { enable = false; user = null; }

  # 開発体験 (minimal / full) は Story-3 で full を追加
, profile ? "minimal"
}:

let
  pkgs = inputs.nixpkgs.legacyPackages.${system};

  palmux2-pkg = pkgs.callPackage ../packages/palmux2.nix { };

  caddyEnabled = domain != null;
  bindAddr = if caddyEnabled then "127.0.0.1:8080" else "0.0.0.0:8080";

  profileSet = {
    minimal = pkgs.callPackage ../profiles/minimal.nix { inherit palmux2-pkg; };
    # full は Story-3 で追加
  };

  selectedProfile =
    if !(builtins.hasAttr profile profileSet)
    then throw "mkPalmuxHost: unknown profile '${profile}' (available: ${
      builtins.toString (builtins.attrNames profileSet)
    })"
    else profileSet.${profile};

  homeManagerModule = import ../modules/home-manager-palmux.nix {
    inherit palmux2-pkg username homeDirectory bindAddr;
    profilePackages = selectedProfile.packages;
  };
in
{
  homeConfigurations.${username} = inputs.home-manager.lib.homeManagerConfiguration {
    inherit pkgs;
    modules = [ homeManagerModule ];
  };

  # Story-2 / Story-3 で systemConfigs.default に caddy / server-stability modules を追加。
  # Story-1 段階では system-level 変更なしのため未定義 (install.sh が条件分岐で system-manager
  # switch をスキップする)。
}
