# mkPalmuxHost: host-specific /etc/palmux/flake.nix が呼ぶ entry point。
#
# 公開パラメータ (domain / acmeEmail / basicAuth.user 等) を受け取り、
# home-manager configurations と、 caddy 有効時は system-manager
# systemConfigs を返す。
#
# 秘密 (CF token / basic_auth password hash) は /etc/caddy/palmux.env で
# 注入し、 ここには現れない。
{ inputs }:

{ system ? "x86_64-linux"
, username
, homeDirectory ? "/home/${username}"
, hostname ? "palmux-host"

, domain ? null
, acmeEmail ? null
, basicAuth ? { enable = false; user = null; }

, profile ? "minimal"

  # palmux2 version override (install.sh が `latest` を resolve した結果を渡す)。
  # null の時は palmux2.nix の baked-in default を使う。
, palmux2Version ? null
, palmux2Hash ? null
}:

let
  lib = inputs.nixpkgs.lib;
  pkgs = inputs.nixpkgs.legacyPackages.${system};

  palmux2-pkg = pkgs.callPackage ../packages/palmux2.nix {
    version = palmux2Version;
    hash = palmux2Hash;
  };
  caddy-cloudflare = pkgs.callPackage ../packages/caddy-cloudflare.nix { };

  caddyEnabled = domain != null;
  bindAddr = if caddyEnabled then "127.0.0.1:8080" else "0.0.0.0:8080";

  profileSet = {
    minimal = pkgs.callPackage ../profiles/minimal.nix { inherit palmux2-pkg; };
    full = pkgs.callPackage ../profiles/full.nix { inherit palmux2-pkg; };
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
    profileName = profile;
  };

  caddyModule = import ../modules/system-manager-caddy.nix {
    inherit pkgs caddy-cloudflare domain acmeEmail basicAuth;
  };
in
{
  homeConfigurations.${username} = inputs.home-manager.lib.homeManagerConfiguration {
    inherit pkgs;
    modules = [ homeManagerModule ];
  };

  # systemConfigs.default は Caddy 有効時のみ出力 (install.sh が空判定で
  # system-manager switch をスキップ可能)
  systemConfigs = lib.optionalAttrs caddyEnabled {
    default = inputs.system-manager.lib.makeSystemConfig {
      modules = [ caddyModule ];
    };
  };
}
