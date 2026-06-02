{ stdenv, fetchurl, lib }:

let
  version = "0.8.0";
  arch =
    if stdenv.hostPlatform.isx86_64 then "amd64"
    else if stdenv.hostPlatform.isAarch64 then "arm64"
    else throw "palmux2: unsupported arch ${stdenv.hostPlatform.system}";
  hashes = {
    amd64 = "sha256-Ec/87L6JBZIaXD4nSdExwlNP9lD/RkfNfeg03mjz11E=";
    arm64 = "sha256-FPOxhDciVz9JV71XU2KZX+czpN+Ul9V6yaKKm9fm94E=";
  };
in
stdenv.mkDerivation {
  pname = "palmux2";
  inherit version;

  src = fetchurl {
    url = "https://github.com/tjst-t/palmux2/releases/download/v${version}/palmux-linux-${arch}";
    hash = hashes.${arch};
  };

  dontUnpack = true;
  dontStrip = true; # Go binary, already optimized; stripping can break

  installPhase = ''
    runHook preInstall
    install -Dm755 $src $out/bin/palmux2
    runHook postInstall
  '';

  meta = with lib; {
    description = "Web-based tmux client for parallel Claude Code workflows";
    homepage = "https://github.com/tjst-t/palmux2";
    license = licenses.mit;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "palmux2";
  };
}
