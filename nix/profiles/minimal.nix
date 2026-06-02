# Minimal profile: palmux2 + 必須開発ツール (Nix 管理分)。
#
# gwq / port-manager / @anthropic-ai/claude-code は install.sh が
# Nix の外で binary or npm 経由で入れる (Story-1 範囲では Nix 化しない)。
{ palmux2-pkg, pkgs }:
{
  packages = with pkgs; [
    palmux2-pkg
    tmux
    git
    ghq
    nodejs_20
    go
    gcc
    gnumake
    python3
    python3Packages.pip
    gh
    jq
    unzip
    curl
    ca-certificates
  ];
}
