# nix/packages/gwq.nix
#
# gwq — git worktree manager (d-kuro/gwq), a runtime dependency of palmux2 (it
# is one of the binaries palmux2 requires on PATH at startup). install.sh fetches
# the same prebuilt release tarball outside Nix; this packages it for the NixOS
# module. The release binary is a statically-linked Go executable, so it needs no
# patchelf — just install it.
{ stdenv, fetchurl, lib }:
let
  version = "0.1.1";
  arch =
    if stdenv.hostPlatform.isx86_64 then "x86_64"
    else if stdenv.hostPlatform.isAarch64 then "arm64"
    else throw "gwq: unsupported platform ${stdenv.hostPlatform.system}";
  hashes = {
    x86_64 = "sha256-oO8v5GtZV/pzAXoIXVv0QQExSsm2oYdFMgVM6kzm05Q=";
    arm64 = "sha256-tWRhIBYsAe708s2YnrHsn95vocqvWusZPyRVZROSQCc=";
  };
in
stdenv.mkDerivation {
  pname = "gwq";
  inherit version;

  src = fetchurl {
    url = "https://github.com/d-kuro/gwq/releases/download/v${version}/gwq_Linux_${arch}.tar.gz";
    hash = hashes.${arch};
  };

  sourceRoot = ".";
  dontConfigure = true;
  dontBuild = true;

  installPhase = ''
    runHook preInstall
    install -Dm0755 gwq $out/bin/gwq
    runHook postInstall
  '';

  meta = with lib; {
    description = "git worktree manager (d-kuro/gwq) — palmux2 runtime dependency";
    homepage = "https://github.com/d-kuro/gwq";
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    sourceProvenance = [ sourceTypes.binaryNativeCode ];
  };
}
