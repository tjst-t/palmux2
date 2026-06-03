# Full profile: minimal + shell-UX cluster。
#
# home-manager-palmux モジュールが profileName="full" の時に
# programs.starship/fzf/zoxide/eza/bat/git-delta を enable し、
# それぞれの shell integration (bash init) を自動セットアップする。
# packages 配列にも明示的に含めることで、 nix shell や直接呼び出しでも
# 使えるようにする。
{ palmux2-pkg, pkgs }:
let
  minimal = pkgs.callPackage ./minimal.nix { inherit palmux2-pkg; };
in
{
  packages = minimal.packages ++ (with pkgs; [
    bat
    ripgrep
    fd
    delta
    eza
    fzf
    starship
    zoxide
    yazi
  ]);
}
