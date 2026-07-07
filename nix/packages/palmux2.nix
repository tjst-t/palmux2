{ stdenv
, fetchurl
, lib
  # Override hooks: mkPalmuxHost passes these when install.sh has resolved
  # "latest" to a concrete tag and computed its hash. When null, the baked-in
  # defaults below are used.
, version ? null
, hash ? null
,
}:

let
  defaultVersion = "0.14.2";
  defaultHashes = {
    amd64 = "sha256-Yt0RmkcohOncIZ4svoCM8vVIpsgOqCjrhqTTW2NAb/w=";
    arm64 = "sha256-/KXZ/8K598pV3u21GcNDY+qtKLnC9rAb1snI1p5r7Gk=";
  };

  effectiveVersion = if version != null then version else defaultVersion;
  arch =
    if stdenv.hostPlatform.isx86_64 then "amd64"
    else if stdenv.hostPlatform.isAarch64 then "arm64"
    else throw "palmux2: unsupported arch ${stdenv.hostPlatform.system}";
  effectiveHash = if hash != null then hash else defaultHashes.${arch};
in
stdenv.mkDerivation {
  pname = "palmux2";
  version = effectiveVersion;

  src = fetchurl {
    url = "https://github.com/tjst-t/palmux2/releases/download/v${effectiveVersion}/palmux-linux-${arch}";
    hash = effectiveHash;
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
