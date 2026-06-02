# Caddy with caddy-dns/cloudflare plugin, downloaded as a prebuilt binary from
# the official caddyserver.com custom-build endpoint.
#
# Rationale: nixpkgs `caddy` is the upstream binary without plugins; plugin
# composition normally needs xcaddy + buildGoModule. The caddyserver.com API
# returns a custom build with deterministic hash for a given (Caddy version,
# plugin set), which fits Nix's hash-pinned fetchurl model cleanly.
#
# Hash must be re-pinned when Caddy version bumps. To refresh:
#   curl -fsSL "https://caddyserver.com/api/download?os=linux&arch=amd64&p=github.com%2Fcaddy-dns%2Fcloudflare" | sha256sum
{ stdenv, fetchurl, lib }:

let
  arch =
    if stdenv.hostPlatform.isx86_64 then "amd64"
    else if stdenv.hostPlatform.isAarch64 then "arm64"
    else throw "caddy-cloudflare: unsupported arch ${stdenv.hostPlatform.system}";
  hashes = {
    amd64 = "sha256-oMcBm34amkyZG9Rgx2cYsINGDaoQEL05SSQyZIOW6OA=";
    arm64 = "sha256-qcpbJPIk7NxbULl1TP9AtPP7UfPBkzXEuRFFoIkQqSQ=";
  };
in
stdenv.mkDerivation {
  pname = "caddy-cloudflare";
  version = "custom"; # caddyserver.com returns latest at request time

  src = fetchurl {
    url = "https://caddyserver.com/api/download?os=linux&arch=${arch}&p=github.com%2Fcaddy-dns%2Fcloudflare";
    hash = hashes.${arch};
  };

  dontUnpack = true;
  dontStrip = true; # Go binary

  installPhase = ''
    runHook preInstall
    install -Dm755 $src $out/bin/caddy
    runHook postInstall
  '';

  meta = with lib; {
    description = "Caddy web server with caddy-dns/cloudflare plugin";
    homepage = "https://caddyserver.com";
    license = licenses.asl20;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "caddy";
  };
}
