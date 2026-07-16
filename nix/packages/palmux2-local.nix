# nix/packages/palmux2-local.nix
#
# S31ad96-1: builds palmux2 from THIS repo's own local working-tree source
# (`src = ../..;`, the flake's own checkout — NOT a fetched tarball/release
# asset), with the frontend actually compiled and embedded, exactly matching
# what `make build` produces for a developer running it by hand:
#
#   Makefile:  build: build-frontend
#              build-frontend: cd frontend && npm run build   (tsc -b && vite build)
#              go build -ldflags "-X main.Version=$(VERSION)" -o bin/palmux ./cmd/palmux
#   embed.go:  //go:embed all:frontend/dist   var FrontendFS embed.FS
#
# This is a SEPARATE package from nix/packages/palmux2.nix (which stays
# exactly as-is: fetchurl against a published GitHub Release asset, the
# release/production path every install.sh / palmuxOS deploy uses by
# default). palmux2-local exists purely for dev iteration — verifying
# UNRELEASED Go/frontend changes on a real appliance qcow2 boot before they
# ship (see docs/ROADMAP.json S31ad96's rationale: S61c9a6's real-VM
# verification silently tested a fixed v0.15.0 binary because
# palmux2.nix has no local-source path at all).
#
# ── Why buildGoModule + a separate buildNpmPackage frontend derivation,
#    not a single derivation with an npm preBuild hook ──────────────────
# Two independent hash-pinned fetches are involved here: the Go module
# fetch (vendorHash, from go.mod/go.sum) and the npm dependency fetch
# (npmDepsHash, from frontend/package-lock.json, computed in
# nix/packages/palmux2-frontend.nix). Keeping them as two derivations means
# each is invalidated ONLY by its own lockfile changing, each is
# independently inspectable/buildable (`nix build .#palmux2-local-frontend`
# to debug the frontend alone), and neither build phase's tool (npm vs. go)
# has to coexist inside the other's sandboxed PATH/phases. This is the
# standard nixpkgs shape for "Go binary with an embedded npm-built frontend"
# (mirrors how nixpkgs itself packages e.g. Grafana, Gitea, Hugo-with-npm-assets).
{ buildGoModule
, lib
, callPackage
  # Override hooks, mirroring nix/packages/palmux2.nix's version/hash
  # pattern — lets a maintainer script or `nix build --arg` pass an exact
  # git rev-derived version string without editing this file.
, versionSuffix ? null
}:

let
  frontendDist = callPackage ./palmux2-frontend.nix { };

  # No release tag makes sense for a local-source build; mirror
  # `make build`'s VERSION fallback shape (`git describe ...-dirty`) as a
  # static default since flake pure-eval has no `git describe` at hand here.
  # A caller (e.g. a verification script) can override with the real
  # `git describe --tags --always --dirty` output via versionSuffix.
  version = if versionSuffix != null then versionSuffix else "0.0.0-local";
in
buildGoModule {
  pname = "palmux2-local";
  inherit version;

  # The flake's own source tree (this repo's working copy at eval time, git-
  # tracked files only — flakes filter untracked/gitignored paths out of
  # `./.` automatically under pure evaluation). This is the whole point of
  # this package: it does NOT fetch a release artifact.
  src = ../..;

  # Computed via `go mod vendor` hash bootstrapping (see
  # docs/sprint-logs/S31ad96/verification-S31ad96-1.md for the exact
  # command + transcript). Re-derive whenever go.mod/go.sum change.
  vendorHash = "sha256-TnTy/Pe+/lfcEkV+M+lJUS/Injf9r+Ugfd7C9JvnCAM=";

  subPackages = [ "cmd/palmux" ];

  ldflags = [ "-X main.Version=v${version}" ];

  # embed.go's `//go:embed all:frontend/dist` needs real built assets in
  # frontend/dist BEFORE `go build` runs — replace the gitignored-in-real-
  # checkouts placeholder (frontend/dist/.gitkeep, the only frontend/dist/*
  # entry git tracks per .gitignore) with the actual Vite build output.
  preBuild = ''
    rm -rf frontend/dist
    cp -r ${frontendDist} frontend/dist
    chmod -R u+w frontend/dist
  '';

  # dontStrip left at default (false) is fine here — unlike
  # nix/packages/palmux2.nix (a prebuilt binary fetched from elsewhere,
  # where stripping is risky/pointless), this IS a fresh `go build`, so
  # normal Go toolchain stripping behavior applies safely.

  # Match nix/packages/palmux2.nix's installed binary name exactly
  # ($out/bin/palmux2) — NOT the Go-derived "palmux" (buildGoModule names
  # the binary after cmd/palmux's directory basename) — so this package is
  # a drop-in substitute wherever `${cfg.package}/bin/palmux2` is invoked
  # (nixos/modules/palmux.nix's ExecStart, systemPackages, etc).
  postInstall = ''
    mv $out/bin/palmux $out/bin/palmux2
  '';

  doCheck = false; # `go test` needs tmux/ghq/gwq/git on PATH + a real FS; out of scope for a package build (CI runs `make test` separately).

  meta = with lib; {
    description = "palmux2, built from local working-tree source (Go + embedded frontend) — dev-iteration package, NOT the release path (see nix/packages/palmux2.nix)";
    homepage = "https://github.com/tjst-t/palmux2";
    license = licenses.mit;
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "palmux2";
  };
}
