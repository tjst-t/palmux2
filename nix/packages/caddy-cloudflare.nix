# Caddy with caddy-dns/cloudflare plugin, downloaded as a prebuilt binary from
# the official caddyserver.com custom-build endpoint.
#
# Rationale: nixpkgs `caddy` is the upstream binary without plugins; plugin
# composition normally needs xcaddy + buildGoModule. The caddyserver.com API
# returns the latest custom build with the requested plugins. Because the
# endpoint always returns the latest Caddy, the hash drifts on every upstream
# Caddy release.
#
# Hash handling: scripts/install.sh computes the hash at install time and
# passes it as `hash`. The committed `hashes` values below are a FALLBACK used
# when building directly from the flake (e.g. `nix build .#caddy-cloudflare`)
# without going through install.sh.  The amd64 fallback is kept current; the
# arm64 fallback is best-effort.  Pass `hash` to override both (install.sh
# always passes it for the target arch).
{ stdenv, fetchurl, lib
  # Runtime override: install.sh computes and passes this at install time so
  # the hash always matches the current upstream Caddy binary.  When null the
  # committed fallback hashes below are used.
, hash ? null
}:

let
  arch =
    if stdenv.hostPlatform.isx86_64 then "amd64"
    else if stdenv.hostPlatform.isAarch64 then "arm64"
    else throw "caddy-cloudflare: unsupported arch ${stdenv.hostPlatform.system}";
  # Fallback hashes — update amd64 when a direct `nix build` starts failing.
  # install.sh always overrides these at install time via the `hash` parameter.
  hashes = {
    amd64 = "sha256-DXKMXgCd2y7AHmnsKmlW8kyDUNoArul61v/EhX0gyvE=";
    arm64 = "sha256-qcpbJPIk7NxbULl1TP9AtPP7UfPBkzXEuRFFoIkQqSQ=";
  };
  effectiveHash = if hash != null then hash else hashes.${arch};
in
stdenv.mkDerivation {
  pname = "caddy-cloudflare";
  version = "custom"; # caddyserver.com returns latest at request time

  src = fetchurl {
    url = "https://caddyserver.com/api/download?os=linux&arch=${arch}&p=github.com%2Fcaddy-dns%2Fcloudflare";
    hash = effectiveHash;
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
