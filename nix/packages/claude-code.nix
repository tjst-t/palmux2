# nix/packages/claude-code.nix
#
# Claude Code CLI (Anthropic), packaged from the OFFICIAL native/standalone
# installer's release channel — fetched and hash-pinned at BUILD time (same
# `fetchurl { url; hash; }` pattern nix/packages/palmux2.nix already uses for
# palmux2 itself), NOT executed as an unattended `curl | sh` at runtime on
# every deployed appliance. This exists so a genuinely FRESH (non-migrated)
# palmuxOS appliance can bootstrap a working `claude` binary (S61c9a6-3).
#
# ── Provenance of the URL pattern + checksum (full transcript:
#    docs/sprint-logs/S61c9a6/verification-S61c9a6-3.md) ──────────────────
# Anthropic's public installer (`curl -fsSL https://claude.ai/install.sh`,
# read — never executed — to derive this) resolves the latest version from
# `https://downloads.claude.ai/claude-code-releases/latest`, then downloads
# the per-platform raw ELF binary from:
#     https://downloads.claude.ai/claude-code-releases/<version>/<platform>/claude
# verifying it against `.../<version>/manifest.json`, which publishes a
# plain sha256 HEX checksum per platform (no npm involved — this is the
# native/standalone channel). `platform` for a standard glibc Linux host
# (this appliance; nix-ld already assumes the glibc build — see
# nixos/modules/appliance.nix's `programs.nix-ld.enable` comment and
# internal/runtime/incus/incus.go's "native/glibc" bind-mount comment) is
# `linux-<arch>`, NOT the `-musl` variant the installer picks on musl hosts.
#
# ── On-disk layout this mirrors ─────────────────────────────────────────
# The raw downloaded binary IS the complete `claude` executable; the
# installer's `claude install` self-installs it to
# ~/.local/share/claude/versions/<version>/claude + symlinks
# ~/.local/bin/claude to that path — the exact layout
# internal/runtime/incus/incus.go's bind-mounts and
# internal/tab/{claudeagent,claudetui}'s `containerClaudeBin` constants
# assume. This derivation does NOT run `claude install` (that is a
# runtime, $HOME-relative side effect, and $HOME does not exist at Nix
# build time); it just places the fetched, checksum-verified binary at
# $out/bin/claude in the Nix store. nixos/modules/appliance.nix's
# `palmux-claude-bootstrap` oneshot is what projects it into
# ~/.local/{bin,share/claude/versions/<v>} on first boot — mirroring the
# installer's on-disk layout without ever executing untrusted/unpinned code.
{ stdenv
, fetchurl
, lib
  # Override hooks, mirroring nix/packages/palmux2.nix, for a future version
  # bump without editing this file (or for a maintainer script to pass a
  # freshly-resolved version + prefetched hash).
, version ? null
, hash ? null
,
}:

let
  # Resolved 2026-07-16 via:
  #   curl -fsSL https://downloads.claude.ai/claude-code-releases/latest
  defaultVersion = "2.1.211";
  # sha256 HEX checksums published by
  # https://downloads.claude.ai/claude-code-releases/2.1.211/manifest.json,
  # converted to Nix SRI form (`sha256-<base64>`). linux-x64 was additionally
  # downloaded once and locally `sha256sum`-verified against the manifest
  # (exact match) before conversion; linux-arm64 is manifest-derived only
  # (not locally re-downloaded — this appliance targets x86_64-linux, per
  # flake.nix's `supportedSystems`; re-verify by download before an aarch64
  # appliance ships). See the verification doc for the exact commands.
  defaultHashes = {
    x64 = "sha256-gnLIpHSsnqG8NfGbn3x+fcTcTrbVrT5ISxkzWsckRrI=";
    arm64 = "sha256-H/9+j5R8B7GdELH79xS35UfpU2JTubWCMNitvEYk+Gc=";
  };

  effectiveVersion = if version != null then version else defaultVersion;
  arch =
    if stdenv.hostPlatform.isx86_64 then "x64"
    else if stdenv.hostPlatform.isAarch64 then "arm64"
    else throw "claude-code: unsupported arch ${stdenv.hostPlatform.system}";
  effectiveHash = if hash != null then hash else defaultHashes.${arch};
in
stdenv.mkDerivation {
  pname = "claude-code";
  version = effectiveVersion;

  src = fetchurl {
    url = "https://downloads.claude.ai/claude-code-releases/${effectiveVersion}/linux-${arch}/claude";
    hash = effectiveHash;
  };

  dontUnpack = true;
  dontStrip = true; # prebuilt binary; stripping can corrupt it

  installPhase = ''
    runHook preInstall
    install -Dm755 $src $out/bin/claude
    runHook postInstall
  '';

  meta = with lib; {
    description = "Claude Code CLI (Anthropic) — native/standalone build, build-time pinned";
    homepage = "https://code.claude.com";
    platforms = [ "x86_64-linux" "aarch64-linux" ];
    mainProgram = "claude";
    # NOTE: no `license` attr — Claude Code is Anthropic proprietary
    # software, not an OSI-approved license nixpkgs has a `licenses.*` entry
    # for. Setting `licenses.unfree` here would trip nixpkgs' unfree-license
    # eval gate (`config.allowUnfree`) for every consumer of this flake;
    # omitting the attr (same as nix/packages/gwq.nix, which also
    # redistributes a prebuilt third-party binary) sidesteps that without
    # misrepresenting the license.
    sourceProvenance = [ sourceTypes.binaryNativeCode ];
  };
}
