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

# Directory holding this script + its sidecar assets (palmux-browser CLI, skills/).
# Use this for context-file pushes so the build works regardless of CWD (e.g.
# when the build context is unpacked to a temp dir on a build host).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

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
    eza ripgrep zoxide fzf git-delta unzip openssh-client gpg \
    fonts-noto-cjk fonts-noto-color-emoji \
    xvfb x11vnc fcitx5 fcitx5-mozc dbus-x11 x11-utils
'
# CJK + emoji fonts: without these, the in-container chromium (Browser tab)
# renders Japanese/Chinese/Korean as tofu (□). fonts-noto-cjk covers JP/CN/KR;
# fonts-noto-color-emoji covers emoji. (S62374c-followup)

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

# chromium (S62374c-1): headless browser for the Browser tab.
# Needed for CDP-based browser automation (--remote-debugging-port=9222).
# Runs as ubuntu/uid 1000 inside the container; --no-sandbox is required for
# unprivileged incus containers.  CDP is never Caddy-exposed (bridge-only).
# The palmux browser manager calls `chromium` (binary name); ensure that name
# exists regardless of whether the package installs chromium or chromium-browser.
log "   Installing chromium (google-chrome-stable .deb) ..."
# IMPORTANT: on Ubuntu 24.04 the `chromium`/`chromium-browser` apt package is a
# SNAP wrapper. snapd cannot set up its devpts mounts inside an unprivileged
# incus container (`mount devpts ... Permission denied`), so that install fails
# at build time. We install Google Chrome's real .deb from Google's apt repo
# (no snap), which is headless-capable with --no-sandbox in the container, and
# symlink `chromium` → it so the browser manager's `chromium` binary name works.
incus exec "${BUILD_INST}" </dev/null -- sh -c '
  set -e
  export DEBIAN_FRONTEND=noninteractive
  apt-get install -y --no-install-recommends ca-certificates curl gnupg
  install -d -m 0755 /etc/apt/keyrings
  curl -fsSL https://dl.google.com/linux/linux_signing_key.pub \
    | gpg --dearmor -o /etc/apt/keyrings/google-chrome.gpg
  echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/google-chrome.gpg] http://dl.google.com/linux/chrome/deb/ stable main" \
    > /etc/apt/sources.list.d/google-chrome.list
  apt-get update
  apt-get install -y --no-install-recommends google-chrome-stable
  # Expose the binary under the `chromium` name the browser manager invokes.
  ln -sf "$(command -v google-chrome-stable)" /usr/local/bin/chromium
'

log "   Verifying installed binaries ..."
for b in tmux git python3 gh starship eza rg zoxide fzf delta yazi chromium \
          Xvfb x11vnc fcitx5; do
  incus exec "${BUILD_INST}" </dev/null -- sh -c "command -v $b >/dev/null 2>&1" \
    || die "$b not found after install"
done
log "   base + gh + shell-UX tools + chromium + Xvfb/x11vnc/fcitx5 present"

# ─── 3c. Node.js + playwright-core (S62374c-3) ────────────────────────────────
# playwright-core is used by `palmux-browser` CLI to connectOverCDP to the
# running chromium instance. We use connectOverCDP which attaches to an already-
# running browser — NO browser download is needed (PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1).
# Node.js is installed from nodesource (LTS) since Ubuntu 24.04's nodejs package
# may be older than what playwright-core requires.
log "3c. Installing Node.js LTS + playwright-core (for palmux-browser CLI) ..."
incus exec "${BUILD_INST}" </dev/null -- sh -c '
  set -e
  export DEBIAN_FRONTEND=noninteractive
  # Install Node.js 20 LTS from NodeSource.
  curl -fsSL https://deb.nodesource.com/setup_20.x | bash -
  apt-get install -y --no-install-recommends nodejs
  # Install playwright-core globally; skip browser download (we use connectOverCDP).
  PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install -g --no-fund --no-audit playwright-core
  # Global npm modules are not on the default require() path. Export NODE_PATH
  # system-wide so any login/interactive shell that runs palmux-browser resolves
  # playwright-core. (The CLI also self-resolves via `npm root -g` as a fallback.)
  echo "export NODE_PATH=\"$(npm root -g)\"" > /etc/profile.d/10-palmux-node-path.sh
'

# Copy the palmux-browser CLI and the palmux-browser skill.
log "   Copying palmux-browser CLI → /usr/local/bin/ ..."
incus file push "${SCRIPT_DIR}"/palmux-browser "${BUILD_INST}"/usr/local/bin/palmux-browser </dev/null
incus exec "${BUILD_INST}" </dev/null -- chmod +x /usr/local/bin/palmux-browser

log "   Copying palmux-browser skill → /usr/local/share/palmux/.claude/skills/ ..."
incus exec "${BUILD_INST}" </dev/null -- mkdir -p /usr/local/share/palmux/.claude/skills/palmux-browser
incus file push "${SCRIPT_DIR}"/skills/palmux-browser/SKILL.md \
  "${BUILD_INST}"/usr/local/share/palmux/.claude/skills/palmux-browser/SKILL.md </dev/null

log "   Verifying palmux-browser toolchain ..."
incus exec "${BUILD_INST}" </dev/null -- sh -c "command -v node >/dev/null 2>&1" \
  || die "node not found after install"
incus exec "${BUILD_INST}" </dev/null -- sh -c "command -v palmux-browser >/dev/null 2>&1" \
  || die "palmux-browser not found in /usr/local/bin"
incus exec "${BUILD_INST}" </dev/null -- sh -c "test -x /usr/local/bin/palmux-browser" \
  || die "palmux-browser not executable"
incus exec "${BUILD_INST}" </dev/null -- sh -c \
  "test -f /usr/local/share/palmux/.claude/skills/palmux-browser/SKILL.md" \
  || die "palmux-browser skill file not found"
incus exec "${BUILD_INST}" </dev/null -- sh -c \
  "NODE_PATH=\$(npm root -g) node -e 'require(\"playwright-core\")' >/dev/null 2>&1" \
  || die "playwright-core not importable from node (NODE_PATH=\$(npm root -g))"
# Also confirm the CLI's own runtime resolution path works end-to-end.
incus exec "${BUILD_INST}" </dev/null -- sh -c \
  "node -e 'const cp=require(\"child_process\");const p=require(\"path\");require(p.join(cp.execSync(\"npm root -g\").toString().trim(),\"playwright-core\"))' >/dev/null 2>&1" \
  || die "palmux-browser playwright-core fallback resolution failed"
log "   node + palmux-browser + playwright-core + skill all present"

# ─── 3b. rich default shell (S5818e8) ─────────────────────────────────────────
# Bake a sensible rich shell into the image so EVERY container has a good shell
# regardless of host (starship prompt + eza/zoxide/fzf wiring). When palmux
# bind-mounts the host's real ~/.bashrc / ~/.bashrc.d (real-dotfile hosts), those
# take precedence; on hosts whose dotfiles are skipped (Nix → /nix symlinks) the
# container falls back to THIS rich default. ~/.bashrc still sources ~/.bashrc.d,
# so a host that mounts only ~/.bashrc.d (real) still gets its functions loaded.
log "3b. Baking rich default shell (starship + tool wiring) ..."
incus exec "${BUILD_INST}" </dev/null -- sh -c '
  set -e
  install -d -o ubuntu -g ubuntu -m 0755 /home/ubuntu/.bashrc.d
  cat > /home/ubuntu/.bashrc.d/00-palmux-shell.bash <<RC
# palmux-ws default interactive shell wiring (S5818e8)
[[ \$- == *i* ]] || return
alias ll="eza -la --git --group-directories-first"
alias la="eza -a --group-directories-first"
alias ls="eza --group-directories-first"
alias lt="eza --tree --level=2"
command -v fzf >/dev/null && [ -r /usr/share/doc/fzf/examples/key-bindings.bash ] && . /usr/share/doc/fzf/examples/key-bindings.bash
RC
  # Append the loader + prompt inits to the default ~/.bashrc (idempotent guard).
  if ! grep -q "palmux: rich shell" /home/ubuntu/.bashrc 2>/dev/null; then
    cat >> /home/ubuntu/.bashrc <<RC

# palmux: rich shell defaults (S5818e8)
if [[ \$- == *i* ]]; then
  for _f in ~/.bashrc.d/*.bash; do [ -r "\$_f" ] && . "\$_f"; done 2>/dev/null; unset _f
  command -v starship >/dev/null && eval "\$(starship init bash)"
  command -v zoxide   >/dev/null && eval "\$(zoxide init bash)"
fi
RC
  fi
  chown -R ubuntu:ubuntu /home/ubuntu/.bashrc /home/ubuntu/.bashrc.d
'
log "   rich default shell baked (interactive container bash → starship prompt)"

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
