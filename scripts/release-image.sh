#!/usr/bin/env bash
# scripts/release-image.sh
#
# Systematized "release the palmux-ws image WITHOUT rebuilding it" path.
#
# Context: the container image (images/workspace-default/build.sh output) only
# changes when something under images/ changes. A binary-only release ships the
# SAME image content as the previous release — but the self-update machinery ties
# the image's reported version to the release tag (internal/selfupdate +
# cmd/palmux/runtime.go: installedImageVersion() vs the release tag), so a release
# still needs a palmux-ws.tar.gz asset carrying the NEW version, or:
#   - `palmux runtime install` (stable) fails: no asset on the latest release, and
#   - the self-update badge falsely lights "image 更新あり" forever, and the
#     PALMUX_REQUIRE_IMAGE completion guard rejects a verbatim re-upload of the old
#     tarball (installed < expected).
#
# A full apt rebuild for an UNCHANGED image is wasteful (and brittle — the pinned
# upstream .deb can be purged). This script instead RE-STAMPS the previous
# release's image: it rewrites only the version in the unified incus tarball and
# re-attaches it. No apt, no incus, no VM — pure tar surgery, so it runs in CI.
#
# The exported incus unified tarball is `metadata.yaml` + a plain `rootfs/` tar
# tree (NOT squashfs), and the version lives in exactly two places:
#   1. metadata.yaml  -> properties.version           (incus `version` property;
#                                                        read first by
#                                                        installedImageVersion())
#   2. rootfs/etc/palmux-ws-version                    (baked fallback, re-stamped
#                                                        by ensureImageVersionProperty)
# We rewrite both and repack. The rootfs holds root-owned system files (and
# possibly setcap binaries / device nodes), so we extract + repack AS ROOT (sudo
# when available) with --numeric-owner --xattrs --acls — exactly how incus itself
# round-trips an image — and keep metadata.yaml first. (A non-root extract would
# rewrite every file to the caller's uid and drop file-capabilities, corrupting
# the image.)
#
# Usage:
#   scripts/release-image.sh <new-tag> [options]
#
# Options:
#   --prev <tag>     Source release tag to re-stamp from. Default: newest prior
#                    release carrying a palmux-ws.tar.gz asset (via gh).
#   --src <tarball>  Use a local palmux-ws.tar.gz instead of downloading --prev.
#   --out <path>     Output path (default: ./dist/palmux-ws.tar.gz).
#   --repo <o/r>     GitHub repo (default: $GITHUB_REPOSITORY or tjst-t/palmux2).
#   --images-dir <d> Dir whose git diff decides rebuild-vs-restamp (default images).
#   --upload         Upload the result to the <new-tag> release (gh release upload).
#   --force-restamp  Skip the images/ git-diff gate (testing / known-unchanged).
#   --no-diff        Alias for --force-restamp.
#
# Exit codes:
#   0  re-stamped (and uploaded if --upload) successfully
#   3  images/ CHANGED since the source tag — a real rebuild is required. CI must
#      build palmux-ws on a VM (images/workspace-default/build.sh) and upload the
#      asset manually; this script will not silently ship a stale image.
#   2  usage / precondition error
set -euo pipefail

die()  { echo "release-image: ERROR: $*" >&2; exit 2; }
log()  { echo "release-image: $*" >&2; }
gherr() { echo "::error::$*" >&2; }

# ─── args ───────────────────────────────────────────────────────────────────
NEW_TAG=""
PREV_TAG=""
SRC=""
OUT="dist/palmux-ws.tar.gz"
REPO="${GITHUB_REPOSITORY:-tjst-t/palmux2}"
IMAGES_DIR="images"
DO_UPLOAD=0
FORCE_RESTAMP=0

while [ $# -gt 0 ]; do
  case "$1" in
    --prev)        PREV_TAG="${2:?--prev needs a value}"; shift 2 ;;
    --src)         SRC="${2:?--src needs a value}"; shift 2 ;;
    --out)         OUT="${2:?--out needs a value}"; shift 2 ;;
    --repo)        REPO="${2:?--repo needs a value}"; shift 2 ;;
    --images-dir)  IMAGES_DIR="${2:?--images-dir needs a value}"; shift 2 ;;
    --upload)      DO_UPLOAD=1; shift ;;
    --force-restamp|--no-diff) FORCE_RESTAMP=1; shift ;;
    -h|--help)     sed -n '2,60p' "$0"; exit 0 ;;
    -*)            die "unknown option: $1" ;;
    *)             if [ -z "$NEW_TAG" ]; then NEW_TAG="$1"; shift; else die "unexpected arg: $1"; fi ;;
  esac
done

[ -n "$NEW_TAG" ] || die "missing <new-tag> (e.g. v0.11.5)"
command -v tar >/dev/null || die "tar not found"

# ─── resolve the source (prev) release tag ──────────────────────────────────
# Only needed when we are downloading (no --src). The prev release is the newest
# release before NEW_TAG that actually carries a palmux-ws.tar.gz asset.
need_gh() { command -v gh >/dev/null || die "gh CLI not found (needed without --src)"; }

if [ -z "$SRC" ] && [ -z "$PREV_TAG" ]; then
  need_gh
  log "resolving previous release with a palmux-ws.tar.gz asset (repo=$REPO) ..."
  # Newest-first list of release tags; pick the first (other than NEW_TAG) that
  # has the asset.
  while IFS= read -r tag; do
    [ -n "$tag" ] || continue
    [ "$tag" = "$NEW_TAG" ] && continue
    if gh release view "$tag" -R "$REPO" --json assets \
         --jq '.assets[].name' 2>/dev/null | grep -qx 'palmux-ws.tar.gz'; then
      PREV_TAG="$tag"
      break
    fi
  done < <(gh release list -R "$REPO" --limit 40 \
             --json tagName,createdAt \
             --jq 'sort_by(.createdAt) | reverse | .[].tagName' 2>/dev/null)
  [ -n "$PREV_TAG" ] || {
    gherr "no prior release carries a palmux-ws.tar.gz asset — build palmux-ws on a VM (images/workspace-default/build.sh) and upload it for $NEW_TAG"
    exit 3
  }
  log "previous image-bearing release: $PREV_TAG"
fi

# ─── decide: rebuild-required vs re-stamp ────────────────────────────────────
# If anything under $IMAGES_DIR changed since the source tag, the image content
# is different and CANNOT be re-stamped — a real (incus) rebuild is required.
if [ "$FORCE_RESTAMP" -ne 1 ] && [ -n "$PREV_TAG" ]; then
  if ! command -v git >/dev/null; then
    die "git not found (needed to diff $IMAGES_DIR; pass --force-restamp to skip)"
  fi
  if ! git rev-parse -q --verify "$PREV_TAG^{commit}" >/dev/null 2>&1; then
    die "cannot resolve $PREV_TAG in git history (fetch tags, or pass --force-restamp)"
  fi
  # Markdown docs under images/ are not build inputs — a README-only change must
  # not force a rebuild. Everything else under $IMAGES_DIR (build.sh + baked
  # sidecars) is treated as image content.
  DIFF_PATHSPEC=("$IMAGES_DIR" ":(exclude)$IMAGES_DIR/**/*.md")
  if git diff --quiet "$PREV_TAG"..HEAD -- "${DIFF_PATHSPEC[@]}"; then
    log "$IMAGES_DIR unchanged since $PREV_TAG (docs ignored) → re-stamp (no rebuild needed)"
  else
    log "$IMAGES_DIR CHANGED since $PREV_TAG:"
    git diff --name-only "$PREV_TAG"..HEAD -- "${DIFF_PATHSPEC[@]}" | sed 's/^/  /' >&2
    gherr "$IMAGES_DIR changed since $PREV_TAG — the palmux-ws image must be REBUILT on a VM (images/workspace-default/build.sh) and uploaded for $NEW_TAG; release-image.sh re-stamps only, it does not rebuild"
    exit 3
  fi
fi

# ─── acquire the source tarball ─────────────────────────────────────────────
WORK="$(mktemp -d "${RUNNER_TEMP:-${TMPDIR:-/tmp}}/relimg.XXXXXX")"
cleanup() { rm -rf "$WORK"; }
trap cleanup EXIT

IN_GZ="$WORK/in.tar.gz"
if [ -n "$SRC" ]; then
  [ -f "$SRC" ] || die "--src file not found: $SRC"
  log "using local source tarball: $SRC"
  cp "$SRC" "$IN_GZ"
else
  need_gh
  log "downloading $PREV_TAG palmux-ws.tar.gz ..."
  gh release download "$PREV_TAG" -R "$REPO" --pattern 'palmux-ws.tar.gz' \
     --dir "$WORK" --clobber || die "gh release download failed for $PREV_TAG"
  mv "$WORK/palmux-ws.tar.gz" "$IN_GZ"
fi

# ─── re-stamp via extract-as-root + repack ──────────────────────────────────
# The rootfs holds root-owned system files (and possibly setcap binaries / device
# nodes). Extracting/repacking as a non-root user would rewrite every file to the
# caller's uid and drop file-capabilities — corrupting the image. So we extract
# faithfully as root (sudo when available), edit the two version files, and repack
# with metadata.yaml first (matching incus's original layout). This is exactly how
# incus itself round-trips an image, so the result is guaranteed importable.
SUDO=""
if [ "$(id -u)" -ne 0 ]; then
  if command -v sudo >/dev/null 2>&1 && sudo -n true 2>/dev/null; then
    SUDO="sudo"
  else
    log "WARNING: not root and no passwordless sudo — rootfs ownership/caps may not be preserved"
  fi
fi
# cleanup() may need root to remove root-owned extracted files.
cleanup() { rm -rf "$WORK" 2>/dev/null || true; [ -d "$WORK" ] && ${SUDO:-} rm -rf "$WORK" 2>/dev/null || true; }

X="$WORK/x"
mkdir -p "$X"
# Faithful flags: numeric ids, security/file-capability xattrs, ACLs.
TAR_FAITHFUL=(--numeric-owner --xattrs '--xattrs-include=*' --acls)
log "extracting source rootfs (preserving ownership / xattrs / file-caps) ..."
$SUDO tar "${TAR_FAITHFUL[@]}" -xpf "$IN_GZ" -C "$X"
rm -f "$IN_GZ"

[ -f "$X/metadata.yaml" ] \
  || die "source tarball has no top-level metadata.yaml (not a unified incus image?)"
[ -f "$X/rootfs/etc/palmux-ws-version" ] \
  || die "source tarball has no rootfs/etc/palmux-ws-version"

# Rewrite metadata.yaml's properties.version (the only indented `version:` line).
matches="$($SUDO grep -cE '^[[:space:]]+version:[[:space:]]' "$X/metadata.yaml" || true)"
[ "$matches" = "1" ] || die "expected exactly 1 indented 'version:' line in metadata.yaml, found $matches"
$SUDO sed -i -E "s|^([[:space:]]+)version:[[:space:]].*|\1version: ${NEW_TAG}|" "$X/metadata.yaml"
$SUDO grep -qE "^[[:space:]]+version: ${NEW_TAG}\$" "$X/metadata.yaml" \
  || die "metadata.yaml version rewrite did not take"

# Rewrite the baked version file.
printf '%s\n' "$NEW_TAG" | $SUDO tee "$X/rootfs/etc/palmux-ws-version" >/dev/null

mkdir -p "$(dirname "$OUT")"
log "repacking + compressing → $OUT (metadata.yaml first) ..."
# Repack EVERY top-level member (metadata.yaml first, then rootfs, templates/, and
# anything else incus shipped). Dropping templates/ (hostname.tpl, hosts.tpl, …)
# imports fine but breaks the LXC pre-start hook at launch. Repack as root
# (faithful), gzip as the caller so $OUT is caller-owned.
mapfile -t OTHERS < <(cd "$X" && find . -maxdepth 1 -mindepth 1 ! -name metadata.yaml -printf '%f\n')
$SUDO tar "${TAR_FAITHFUL[@]}" -cf - -C "$X" metadata.yaml "${OTHERS[@]}" | gzip > "$OUT"
$SUDO rm -rf "$X"

# ─── verify ─────────────────────────────────────────────────────────────────
got_meta="$(tar xzf "$OUT" -O metadata.yaml 2>/dev/null | grep -E '^[[:space:]]+version:' | head -1 | awk '{print $2}')"
got_baked="$(tar xzf "$OUT" -O rootfs/etc/palmux-ws-version 2>/dev/null | tr -d '\n')"
[ "$got_meta" = "$NEW_TAG" ]  || die "verify: metadata.yaml version is '$got_meta', expected '$NEW_TAG'"
[ "$got_baked" = "$NEW_TAG" ] || die "verify: baked version is '$got_baked', expected '$NEW_TAG'"
sz="$(stat -c%s "$OUT" 2>/dev/null || wc -c < "$OUT")"
log "re-stamped OK: $OUT ($sz bytes), version=$NEW_TAG (from ${SRC:-$PREV_TAG})"

# ─── optional upload ────────────────────────────────────────────────────────
if [ "$DO_UPLOAD" -eq 1 ]; then
  need_gh
  log "uploading palmux-ws.tar.gz to release $NEW_TAG ..."
  gh release upload "$NEW_TAG" "$OUT" -R "$REPO" --clobber \
    || die "gh release upload failed for $NEW_TAG"
  log "uploaded to $NEW_TAG"
fi

echo "$OUT"
