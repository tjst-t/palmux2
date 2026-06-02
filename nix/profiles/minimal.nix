# Minimal profile: palmux2 + 必須開発ツール (Nix 管理分)。
#
# 以下は install.sh が apt / npm / 直接 binary で入れるので Nix profile に
# 含めない:
#   - node + npm + claude-code (apt + npm -g、 /nix/store readonly 問題回避)
#   - gwq / port-manager (GitHub Release binary、 nixpkgs 不在)
#   - git / curl / jq / unzip (apt bootstrap で既に入っている)
#   - CA certs (apt の ca-certificates が /etc/ssl/certs/ を提供)
{ palmux2-pkg, pkgs }:
{
  packages = with pkgs; [
    palmux2-pkg
    tmux
    ghq
    go
    gcc
    gnumake
    gh
    python3
  ];
}
