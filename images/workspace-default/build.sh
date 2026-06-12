#!/usr/bin/env bash
# images/workspace-default/build.sh
#
# Build the claude-free `palmux-ws` Incus base image and export a unified
# Incus tarball suitable for `incus image import`.
#
# Usage:
#   ./images/workspace-default/build.sh [--out-dir <dir>]
#
# Environment variables:
#   OUT_DIR     Output directory for the tarball (default: ./dist)
#   IMAGE_BASE  Incus base image (default: images:ubuntu/24.04)
#
# Output:
#   <OUT_DIR>/palmux-ws.tar.gz   — Incus unified tarball (import with
#                                   `incus image import ... --alias palmux-ws`)
#
# Requirements:
#   - incus CLI on PATH, incusd running, admin access
#   - Internet access (the build instance runs apt-get)
#
# IMPORTANT incus stdin gotcha: every `incus` invocation here uses </dev/null
# to prevent the CLI from reading stdin as YAML config (a known incus quirk).
#
# Design:
#   - The image is intentionally claude-FREE. claude is bind-mounted from the
#     host by palmux at Workspace start time (see internal/runtime/incus/incus.go,
#     the Start() bind-mount block and docs/workspace-runtime-design.md §4).
#   - What IS baked: tmux, git, curl, ca-certificates, python3 (for the
#     localhost relay in ExposePort), plus the ubuntu user at UID 1000.
#   - The image uses ubuntu/24.04 (noble) and stays unprivileged; raw.idmap
#     "both 1000 1000" is set by palmux at runtime (see subuid prereq below).

set -euo pipefail

# ─── configuration ────────────────────────────────────────────────────────────
OUT_DIR="${OUT_DIR:-./dist}"
IMAGE_BASE="${IMAGE_BASE:-images:ubuntu/24.04}"
BUILD_INST="palmux-ws-build-$$"   # unique name per run so parallel builds don't clash
TEMP_ALIAS="palmux-ws-build-temp-$$"

# ─── helpers ──────────────────────────────────────────────────────────────────
log() { printf '\n\033[1;34m[build.sh]\033[0m %s\n' "$*" >&2; }
die() { printf '\n\033[1;31m[build.sh ERROR]\033[0m %s\n' "$*" >&2; exit 1; }

cleanup() {
  local rc=$?
  log "cleanup: removing build instance ${BUILD_INST} and temp alias ${TEMP_ALIAS} ..."
  incus delete --force "${BUILD_INST}" </dev/null 2>/dev/null || true
  incus image alias delete "${TEMP_ALIAS}" </dev/null 2>/dev/null || true
  if [ $rc -ne 0 ]; then
    die "Build failed (exit ${rc}). See output above."
  fi
}
trap cleanup EXIT

# ─── preflight ────────────────────────────────────────────────────────────────
if ! command -v incus >/dev/null 2>&1; then
  die "incus not found on PATH. Install it with: sudo apt-get install incus"
fi

incus info </dev/null >/dev/null 2>&1 || die "incus daemon not reachable. Is incusd running?"

mkdir -p "${OUT_DIR}"

# ─── 1. launch build instance ─────────────────────────────────────────────────
log "1. Launching build instance '${BUILD_INST}' from ${IMAGE_BASE} ..."
incus launch "${IMAGE_BASE}" "${BUILD_INST}" </dev/null

# ─── 2. wait for agent + systemd/init ────────────────────────────────────────
log "2. Waiting for incus agent (container to become exec-able) ..."
# Strategy: poll `incus exec -- true` until it succeeds, meaning the in-container
# agent is accepting commands.  This works whether or not cloud-init is present.
# Minimal Ubuntu container images from images.linuxcontainers.org do NOT ship
# cloud-init, so we cannot rely on `cloud-init status --wait`.
WAIT_TIMEOUT=120
ELAPSED=0
while true; do
  if incus exec "${BUILD_INST}" </dev/null -- true >/dev/null 2>&1; then
    log "   incus agent ready"
    break
  fi
  ELAPSED=$((ELAPSED + 2))
  if [ "${ELAPSED}" -ge "${WAIT_TIMEOUT}" ]; then
    die "Timed out waiting for incus agent after ${WAIT_TIMEOUT}s"
  fi
  sleep 2
done

# If cloud-init IS present (full Ubuntu cloud image), wait for it to finish
# before running apt-get — otherwise apt-get may conflict with cloud-init's own
# package ops.
if incus exec "${BUILD_INST}" </dev/null -- sh -c 'command -v cloud-init >/dev/null 2>&1' >/dev/null 2>&1; then
  log "   cloud-init detected — waiting for it to finish ..."
  incus exec "${BUILD_INST}" </dev/null -- cloud-init status --wait >/dev/null 2>&1 || true
  log "   cloud-init done (or timed out)"
else
  log "   cloud-init not present in base image (minimal image) — skipping"
fi

# ─── 3. install packages ──────────────────────────────────────────────────────
# Base tools + the shell-UX cluster (S5818e8) so the container's interactive
# shell matches the host once palmux bind-mounts ~/.bashrc / ~/.bashrc.d. We bake
# exactly the tools the host shell invokes (eza/ripgrep/zoxide/fzf/git-delta from
# apt; starship + yazi from upstream releases). bat/fd are intentionally omitted
# (the reference host does not ship them).
log "3. Installing base packages + shell-UX cluster ..."
incus exec "${BUILD_INST}" </dev/null -- sh -c '
  set -e
  export DEBIAN_FRONTEND=noninteractive
  apt-get update -qq
  apt-get install -y --no-install-recommends \
    tmux git curl ca-certificates python3 \
    eza ripgrep zoxide fzf git-delta unzip openssh-client gpg
'

# gh (GitHub CLI) — the agent uses it for GitHub ops; bake it via the official
# apt repo (not in Ubuntu main). The token/identity is bind-mounted by palmux
# (~/.config/gh, ~/.gitconfig, ~/.ssh) at Workspace start (S5818e8-1).
log "   Installing gh (GitHub CLI) ..."
incus exec "${BUILD_INST}" </dev/null -- sh -c '
  set -e
  export DEBIAN_FRONTEND=noninteractive
  curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg \
    | tee /usr/share/keyrings/githubcli-archive-keyring.gpg >/dev/null
  chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" \
    > /etc/apt/sources.list.d/github-cli.list
  apt-get update -qq
  apt-get install -y --no-install-recommends gh
'

# starship (prompt) — official installer → /usr/local/bin/starship.
log "   Installing starship (prompt) ..."
incus exec "${BUILD_INST}" </dev/null -- sh -c '
  set -e
  curl -fsSL https://starship.rs/install.sh | sh -s -- -y -b /usr/local/bin
'

# yazi (TUI file manager) — pinned upstream release binary → /usr/local/bin.
YAZI_VER="${YAZI_VER:-0.4.2}"
log "   Installing yazi ${YAZI_VER} ..."
incus exec "${BUILD_INST}" </dev/null -- sh -c "
  set -e
  cd /tmp
  curl -fsSL -o yazi.zip https://github.com/sxyazi/yazi/releases/download/v${YAZI_VER}/yazi-x86_64-unknown-linux-gnu.zip
  unzip -oq yazi.zip
  install -m755 yazi-x86_64-unknown-linux-gnu/yazi /usr/local/bin/yazi
  install -m755 yazi-x86_64-unknown-linux-gnu/ya /usr/local/bin/ya 2>/dev/null || true
  rm -rf yazi.zip yazi-x86_64-unknown-linux-gnu
"

log "   Verifying installed binaries ..."
for b in tmux git python3 gh starship eza rg zoxide fzf delta yazi; do
  incus exec "${BUILD_INST}" </dev/null -- sh -c "command -v $b >/dev/null 2>&1" \
    || die "$b not found after install"
done
log "   base + gh + shell-UX tools present (starship/eza/rg/zoxide/fzf/delta/yazi)"

# ─── 4. verify ubuntu user UID 1000 ──────────────────────────────────────────
log "4. Verifying ubuntu user (UID 1000) ..."
UID_CHECK=$(incus exec "${BUILD_INST}" </dev/null -- id -u ubuntu 2>/dev/null || echo "")
if [ "${UID_CHECK}" != "1000" ]; then
  die "Expected ubuntu user to have UID 1000, got '${UID_CHECK}'"
fi
log "   ubuntu UID=${UID_CHECK} OK"

# Explicitly confirm claude is NOT in the image (we do NOT bake it).
if incus exec "${BUILD_INST}" </dev/null -- sh -c 'command -v claude >/dev/null 2>&1' >/dev/null 2>&1; then
  die "BUG: claude binary found inside the build instance — it must NOT be baked"
fi
log "   claude NOT present in image (correct — will be bind-mounted by palmux)"

# ─── 5. stop, publish, export ─────────────────────────────────────────────────
log "5. Stopping build instance ..."
incus stop "${BUILD_INST}" </dev/null

log "   Publishing as image alias '${TEMP_ALIAS}' ..."
incus publish "${BUILD_INST}" </dev/null --alias "${TEMP_ALIAS}"

OUT_PATH="${OUT_DIR}/palmux-ws.tar.gz"
log "   Exporting image to ${OUT_PATH} ..."
incus image export "${TEMP_ALIAS}" "${OUT_DIR}/palmux-ws" </dev/null
# incus image export writes `palmux-ws.tar.gz` (unified format).
if [ ! -f "${OUT_PATH}" ]; then
  die "Expected output file ${OUT_PATH} not found after export"
fi

# ─── 6. report ────────────────────────────────────────────────────────────────
SIZE=$(du -h "${OUT_PATH}" | cut -f1)
log "Build complete."
printf '\n\033[1;32mOutput:\033[0m %s  (%s)\n' "${OUT_PATH}" "${SIZE}"
printf '\nTo import into incus on this host:\n'
printf '  incus image alias delete palmux-ws </dev/null 2>/dev/null || true\n'
printf '  incus image import %s --alias palmux-ws </dev/null\n\n' "${OUT_PATH}"
