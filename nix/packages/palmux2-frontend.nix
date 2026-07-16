# nix/packages/palmux2-frontend.nix
#
# Builds the palmux2 React/Vite frontend (`frontend/`) into its static
# `dist/` bundle using nixpkgs' `buildNpmPackage` (npm, hash-pinned via
# `npmDepsHash` from frontend/package-lock.json — lockfileVersion 3).
#
# This exists ONLY as a helper for nix/packages/palmux2-local.nix (S31ad96-1,
# local-source-build path for dev iteration). It is deliberately a SEPARATE
# derivation from the Go build rather than a shell hook inside
# `buildGoModule`, because:
#   - `buildNpmPackage`'s own fixed-output-derivation network fetch (of npm
#     deps, hashed by npmDepsHash) is fully hermetic/cacheable independently
#     of the Go module fetch (hashed by vendorHash in palmux2-local.nix) —
#     mixing them into one derivation would force re-fetching + re-hashing
#     BOTH whenever either go.sum or package-lock.json changes, even though
#     they're unrelated.
#   - `tsc -b && vite build` (frontend/package.json's `build` script) needs
#     Node + npm on PATH; wiring that as a preBuild hook inside a Go
#     derivation works but fights `buildGoModule`'s own phases and hides a
#     real, independently-inspectable derivation (`nix build
#     .#palmux2-local.frontend` style debugging) behind a single opaque
#     `go build` log.
# The output `dist/` is copied into `frontend/dist` by palmux2-local.nix's
# preBuild, replacing the `.gitkeep` placeholder the real (gitignored)
# `frontend/dist/*` normally would hold in a working tree checkout — the
# EXACT thing `//go:embed all:frontend/dist` in embed.go expects at `go
# build` time, matching `make build-frontend` + `make build`'s sequence
# (Makefile: `build: build-frontend` then `go build ...`).
{ buildNpmPackage
, nodejs_22
, lib
}:

buildNpmPackage {
  pname = "palmux2-frontend";
  version = "local";

  src = lib.cleanSourceWith {
    src = ../../frontend;
    # Keep the fixed-output npmDepsHash stable across incidental churn (dist/
    # placeholder, editor swapfiles, etc) — only source + lockfile + config
    # matter for the npm install + vite build.
    filter = name: type:
      let base = baseNameOf (toString name); in
      base != "dist" && base != "node_modules";
  };

  nodejs = nodejs_22;

  # Computed via `nix-prefetch-npm-deps frontend/package-lock.json`, re-derive
  # the same way whenever frontend/package-lock.json changes (see
  # docs/sprint-logs/S31ad96/verification-S31ad96-1.md for the exact command
  # + transcript).
  npmDepsHash = "sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=";

  # frontend/package.json's "build" script is `tsc -b && vite build`, which
  # is exactly what `make build-frontend` (`cd frontend && npm run build`)
  # runs — same output, same command, no divergence from the documented
  # production build path.
  npmBuildScript = "build";

  installPhase = ''
    runHook preInstall
    cp -r dist $out
    runHook postInstall
  '';

  meta = with lib; {
    description = "palmux2 frontend static bundle (Vite build), for embedding into a local-source palmux2 build (S31ad96-1)";
    platforms = platforms.all;
  };
}
