#!/usr/bin/env bash
#
# palmux2 — Nix-based installer/updater for Ubuntu/Debian.
#
# One-liner (production):
#   curl -fsSL https://raw.githubusercontent.com/tjst-t/palmux2/main/scripts/install.sh | bash
#
# Re-running the same command upgrades to the latest pinned versions atomically
# (Nix world: home-manager switch creates a new generation; failure rolls back).
#
# Environment overrides (all optional):
#   PALMUX_VERSION         palmux2 release tag (informational, actual pin is in flake.nix)
#   PROFILE                "minimal" | "full"        (default: minimal — Story-3 adds full)
#   PALMUX_FLAKE_REF       palmux2 flake input URL  (default: github:tjst-t/palmux2)
#                          test override: PALMUX_FLAKE_REF=path:/tmp/palmux2-src
#   PALMUX_INSTALLER_URL   URL the generated ~/update-palmux2.sh re-fetches this
#                          script from (default: derived from PALMUX_FLAKE_REF)
#
# This installer writes ~/update-palmux2.sh on success — re-run that to update
# and re-apply config without re-supplying options/secrets (secrets are reused
# from /etc/caddy/palmux.env; export CLOUDFLARE_API_TOKEN / BASIC_AUTH_PASSWORD
# to rotate them).
#   GHQ_VERSION            ghq release tag (default: latest)
#   GWQ_VERSION            gwq release tag (default: latest)
#   PORTMAN_VERSION        port-manager release tag (default: latest)
#   NODE_MAJOR             Node.js major version line (default: 20)
#   SKIP_NODE=1            skip Node.js + @anthropic-ai/claude-code install
#   SKIP_SERVICE=1         skip systemctl --user enable palmux2
#   CLAUDE_BYPASS_PERMISSIONS=1
#                          set ~/.claude/settings.json permissions.defaultMode=
#                          bypassPermissions (disables ALL Claude permission
#                          prompts — single-user autonomous box only). Persisted
#                          into ~/update-palmux2.sh so updates keep re-applying it.
#
# HTTPS via Caddy + Cloudflare DNS-01 (Story-2 — set BOTH to enable):
#   DOMAIN                 e.g. palmux.example.com
#   CLOUDFLARE_API_TOKEN   scoped CF token (Zone:DNS:Edit + Zone:Zone:Read)
#   ACME_EMAIL             (optional) email for Let's Encrypt notifications
#
# HTTP basic auth at the Caddy edge (Story-2 — requires Caddy):
#   BASIC_AUTH_USER        username
#   BASIC_AUTH_PASSWORD    plaintext password (bcrypt-hashed by Caddy)
#   BASIC_AUTH_BCRYPT_COST bcrypt cost factor (default 8). Caddy runs bcrypt on
#                          EVERY request (basic_auth is not cached), so a high
#                          cost adds that much latency per request (~2x per +1:
#                          cost14≈900ms, 12≈230ms, 10≈55ms, 8≈14ms on a small
#                          vCPU). 8 is a good speed/security balance for a
#                          single-user box behind TLS; raise it if you prefer.
#
# Portman dynamic subdomain routing (S85caca — opt-in):
#   PORTMAN_ROUTING=1      opt-in: switch Caddy to portman-owned dynamic subdomain
#                          routing (model B). Requires DOMAIN + CLOUDFLARE_API_TOKEN.
#                          Default (unset/0): unchanged single-reverse_proxy Caddyfile.
#
# Incus container runtime (S8478ca — default ON, requires Ubuntu 24.04+):
#   SKIP_INCUS=1           skip the entire Incus runtime setup step.
#   PALMUX_WS_IMAGE_FILE   path to a local palmux-ws.tar.gz to import (skips GitHub download).
#   PALMUX_WS_IMAGE_URL    URL to download palmux-ws.tar.gz from (skips GitHub API lookup).
#   PALMUX_WS_PRE=1        pass --pre to `palmux runtime install` (include pre-releases/RCs).
#                          Useful during the RC period before the first stable release.
#
#   During the RC period there may be no stable release with a palmux-ws asset.
#   In that case set PALMUX_WS_PRE=1 or PALMUX_WS_IMAGE_URL/FILE.  If none of these
#   are set and no asset is found, the incus setup still completes (daemon + group +
#   subuid) and the image can be imported later with `palmux runtime install`.
#
set -euo pipefail

# --- env / defaults --------------------------------------------------------

PALMUX_VERSION="${PALMUX_VERSION:-latest}"
PROFILE="${PROFILE:-minimal}"
PALMUX_FLAKE_REF="${PALMUX_FLAKE_REF:-github:tjst-t/palmux2}"

GHQ_VERSION="${GHQ_VERSION:-latest}"
GWQ_VERSION="${GWQ_VERSION:-latest}"
PORTMAN_VERSION="${PORTMAN_VERSION:-latest}"
NODE_MAJOR="${NODE_MAJOR:-20}"
CLAUDE_BYPASS_PERMISSIONS="${CLAUDE_BYPASS_PERMISSIONS:-0}"

DOMAIN="${DOMAIN:-}"
CLOUDFLARE_API_TOKEN="${CLOUDFLARE_API_TOKEN:-}"
ACME_EMAIL="${ACME_EMAIL:-}"
BASIC_AUTH_USER="${BASIC_AUTH_USER:-}"
BASIC_AUTH_PASSWORD="${BASIC_AUTH_PASSWORD:-}"
BASIC_AUTH_BCRYPT_COST="${BASIC_AUTH_BCRYPT_COST:-8}"

PORTMAN_ROUTING="${PORTMAN_ROUTING:-0}"

SKIP_INCUS="${SKIP_INCUS:-0}"
PALMUX_WS_IMAGE_FILE="${PALMUX_WS_IMAGE_FILE:-}"
PALMUX_WS_IMAGE_URL="${PALMUX_WS_IMAGE_URL:-}"
PALMUX_WS_PRE="${PALMUX_WS_PRE:-0}"

GHQ_REPO="x-motemen/ghq"
GWQ_REPO="d-kuro/gwq"
PORTMAN_REPO="tjst-t/port-manager"

PREFIX="/usr/local"
BIN_DIR="${PREFIX}/bin"

# --- helpers ---------------------------------------------------------------

log() { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die() {
  printf '\033[1;31merror:\033[0m %s\n' "$*" >&2
  exit 1
}

# --- preflight -------------------------------------------------------------

[ "$(id -u)" -ne 0 ] || die "run as a regular user; the script uses sudo as needed"
command -v sudo >/dev/null 2>&1 || die "sudo is required"
[ "$(uname -s)" = "Linux" ] || die "unsupported OS: $(uname -s)"

case "$(uname -m)" in
  x86_64 | amd64)
    NIX_SYSTEM="x86_64-linux"
    GHQ_ARCH=amd64
    GWQ_ARCH=x86_64
    PORTMAN_ARCH=amd64
    ;;
  aarch64 | arm64)
    NIX_SYSTEM="aarch64-linux"
    GHQ_ARCH=arm64
    GWQ_ARCH=arm64
    PORTMAN_ARCH=arm64
    ;;
  *) die "unsupported arch: $(uname -m)" ;;
esac

if [ -r /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  case "${ID:-}-${ID_LIKE:-}" in
    ubuntu-* | debian-* | *-*ubuntu* | *-*debian*) : ;;
    *) warn "non-Debian/Ubuntu distro (${ID:-unknown}); apt steps will likely fail" ;;
  esac
fi

# --- reuse persisted secrets on re-install ----------------------------------
# So the generated update helper (~/update-palmux2.sh) and routine re-runs need
# not re-supply secrets: when a non-secret option is set (DOMAIN /
# BASIC_AUTH_USER) but the matching secret is not, read it back from the host.
# The plaintext basic-auth password is never stored — only its bcrypt hash — so
# that is reused as-is (provide BASIC_AUTH_PASSWORD to rotate it).
CADDY_ENV_FILE="/etc/caddy/palmux.env"
REUSE_BASIC_AUTH_HASH=""
if [ -n "$DOMAIN" ] && [ -z "$CLOUDFLARE_API_TOKEN" ] && sudo test -r "$CADDY_ENV_FILE" 2>/dev/null; then
  _tok="$(sudo sed -n 's/^CLOUDFLARE_API_TOKEN=//p' "$CADDY_ENV_FILE" 2>/dev/null || true)"
  if [ -n "$_tok" ]; then
    CLOUDFLARE_API_TOKEN="$_tok"
    log "reusing CLOUDFLARE_API_TOKEN from ${CADDY_ENV_FILE}"
  fi
  unset _tok
fi
if [ -n "$BASIC_AUTH_USER" ] && [ -z "$BASIC_AUTH_PASSWORD" ] && sudo test -r "$CADDY_ENV_FILE" 2>/dev/null; then
  REUSE_BASIC_AUTH_HASH="$(sudo sed -n 's/^BASIC_AUTH_HASH=//p' "$CADDY_ENV_FILE" 2>/dev/null || true)"
  [ -n "$REUSE_BASIC_AUTH_HASH" ] && log "reusing basic-auth hash from ${CADDY_ENV_FILE} (set BASIC_AUTH_PASSWORD to rotate)"
fi

# PORTMAN_ROUTING=1 preflight: requires DOMAIN + CLOUDFLARE_API_TOKEN
if [ "$PORTMAN_ROUTING" = "1" ]; then
  [ -n "$DOMAIN" ] || die "PORTMAN_ROUTING=1 requires DOMAIN to be set"
  [ -n "$CLOUDFLARE_API_TOKEN" ] || die "PORTMAN_ROUTING=1 requires CLOUDFLARE_API_TOKEN to be set"
fi

# Caddy + basic_auth env validation (片肺禁止)
if { [ -n "$DOMAIN" ] && [ -z "$CLOUDFLARE_API_TOKEN" ]; } || \
   { [ -z "$DOMAIN" ] && [ -n "$CLOUDFLARE_API_TOKEN" ]; }; then
  die "DOMAIN and CLOUDFLARE_API_TOKEN must be set together"
fi
# A user without a password is allowed only when an existing hash can be reused.
if [ -n "$BASIC_AUTH_USER" ] && [ -z "$BASIC_AUTH_PASSWORD" ] && [ -z "$REUSE_BASIC_AUTH_HASH" ]; then
  die "BASIC_AUTH_USER set without BASIC_AUTH_PASSWORD (and no existing hash to reuse)"
fi
if [ -z "$BASIC_AUTH_USER" ] && [ -n "$BASIC_AUTH_PASSWORD" ]; then
  die "BASIC_AUTH_PASSWORD set without BASIC_AUTH_USER"
fi
if [ -n "$BASIC_AUTH_USER" ] && [ -z "$DOMAIN" ]; then
  die "BASIC_AUTH_* requires DOMAIN + CLOUDFLARE_API_TOKEN (Caddy)"
fi

# Caddy is enabled by a configured DOMAIN, or forced on by PORTMAN_ROUTING=1
# (which the preflight above guarantees has a DOMAIN). Computed here from the
# documented inputs only — not read from a pre-exported CADDY_ENABLED — so a
# stray environment value can't enter the Caddy block with an empty DOMAIN.
CADDY_ENABLED=0
if [ -n "$DOMAIN" ] || [ "$PORTMAN_ROUTING" = "1" ]; then
  CADDY_ENABLED=1
fi

USERNAME="$(id -un)"
USER_HOME="$(getent passwd "$USERNAME" | cut -d: -f6)"
HOSTNAME_VALUE="$(hostname)"

log "User: ${USERNAME}, Home: ${USER_HOME}, Host: ${HOSTNAME_VALUE}"
log "Profile: ${PROFILE}, Flake ref: ${PALMUX_FLAKE_REF}"

# --- resolve palmux2 version + hash (so re-run always pulls latest) ---------

PALMUX_REPO="tjst-t/palmux2"

if [ "$PALMUX_VERSION" = "latest" ]; then
  log "resolving latest palmux2 release tag from GitHub"
  PALMUX_TAG="$(curl -fsSL "https://api.github.com/repos/${PALMUX_REPO}/releases/latest" | jq -r .tag_name)"
  [ -n "$PALMUX_TAG" ] && [ "$PALMUX_TAG" != "null" ] || die "failed to resolve latest palmux2 tag"
else
  # accept "v0.9.2" or "0.9.2"
  case "$PALMUX_VERSION" in
    v*) PALMUX_TAG="$PALMUX_VERSION" ;;
    *) PALMUX_TAG="v$PALMUX_VERSION" ;;
  esac
fi
PALMUX_TAG_BARE="${PALMUX_TAG#v}"
case "$NIX_SYSTEM" in
  x86_64-linux) PALMUX_BIN_ARCH="amd64" ;;
  aarch64-linux) PALMUX_BIN_ARCH="arm64" ;;
esac
log "computing sha256 of palmux2 ${PALMUX_TAG} (${PALMUX_BIN_ARCH})"
PALMUX_BIN_URL="https://github.com/${PALMUX_REPO}/releases/download/${PALMUX_TAG}/palmux-linux-${PALMUX_BIN_ARCH}"
PALMUX_HASH_HEX="$(curl -fsSL "$PALMUX_BIN_URL" | sha256sum | awk '{print $1}')"
[ -n "$PALMUX_HASH_HEX" ] || die "failed to compute palmux2 sha256"
PALMUX_HASH_SRI="sha256-$(printf '%s' "$PALMUX_HASH_HEX" | xxd -r -p | base64 -w0)"
log "palmux2 ${PALMUX_TAG}: ${PALMUX_HASH_SRI}"

# --- compute caddy-cloudflare hash (for install.sh-based Nix builds) --------
# caddyserver.com/api/download always returns the LATEST Caddy build so the
# hash drifts on every upstream release.  We fetch at install time so the
# caddy-cloudflare.nix derivation always sees the correct hash, preventing
# "hash mismatch in fixed-output derivation" build failures.
# Only meaningful when Caddy is going to be built (CADDY_ENABLED=1), but
# computing it unconditionally is harmless and keeps the logic simple.
CADDY_DL_URL="https://caddyserver.com/api/download?os=linux&arch=${PALMUX_BIN_ARCH}&p=github.com%2Fcaddy-dns%2Fcloudflare"
log "computing sha256 of caddy-cloudflare (${PALMUX_BIN_ARCH})"
CADDY_HASH_HEX="$(curl -fsSL "$CADDY_DL_URL" | sha256sum | awk '{print $1}')"
[ -n "$CADDY_HASH_HEX" ] || die "failed to compute caddy-cloudflare sha256"
CADDY_HASH_SRI="sha256-$(printf '%s' "$CADDY_HASH_HEX" | xxd -r -p | base64 -w0)"
log "caddy-cloudflare: ${CADDY_HASH_SRI}"

# --- apt: base packages (curl/git/jq are bootstrap deps) ----------------------

log "installing base apt packages (curl, git, ca-certificates, jq, unzip, xz-utils)"
sudo apt-get update -qq
sudo apt-get install -y -qq curl git ca-certificates gnupg jq unzip xz-utils

# --- Nix install (Determinate Systems installer) -----------------------------

if ! command -v nix >/dev/null 2>&1; then
  log "installing Nix (Determinate Systems installer, multi-user / systemd)"
  curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix \
    | sudo sh -s -- install linux --no-confirm --init systemd
else
  log "Nix already installed: $(nix --version | head -1)"
fi

# Source Nix profile for current shell so subsequent nix invocations work.
# Determinate puts it in /etc/profile.d/nix.sh (system) or
# /nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh.
for f in \
  /etc/profile.d/nix.sh \
  /nix/var/nix/profiles/default/etc/profile.d/nix-daemon.sh \
  "$USER_HOME/.nix-profile/etc/profile.d/nix.sh"; do
  # shellcheck disable=SC1090
  [ -r "$f" ] && . "$f"
done

command -v nix >/dev/null 2>&1 || die "Nix install succeeded but nix not on PATH; reopen shell"

# Ensure experimental-features
sudo install -d -m 0755 /etc/nix
if ! sudo grep -qE "^experimental-features.*flakes" /etc/nix/nix.conf 2>/dev/null; then
  log "enabling Nix experimental-features = nix-command flakes"
  echo "experimental-features = nix-command flakes" | sudo tee -a /etc/nix/nix.conf >/dev/null
  # Reload nix-daemon if running
  sudo systemctl reload nix-daemon 2>/dev/null || sudo systemctl restart nix-daemon 2>/dev/null || true
fi

# --- Node.js (NodeSource, apt) ---------------------------------------------

# Node is apt-managed (not Nix) so that `sudo npm install -g` works against
# the system prefix /usr/lib/node_modules without Nix-store-readonly issues.
if [ "${SKIP_NODE:-0}" != "1" ]; then
  current_node_major=""
  if command -v node >/dev/null 2>&1; then
    current_node_major="$(node -p 'process.versions.node.split(".")[0]' 2>/dev/null || true)"
  fi
  if [ "$current_node_major" != "$NODE_MAJOR" ]; then
    log "installing Node.js ${NODE_MAJOR}.x from NodeSource"
    curl -fsSL "https://deb.nodesource.com/setup_${NODE_MAJOR}.x" | sudo -E bash -
    sudo apt-get install -y -qq nodejs
  else
    log "Node.js ${NODE_MAJOR}.x already present"
  fi

  log "installing/upgrading @anthropic-ai/claude-code"
  sudo npm install -g --silent @anthropic-ai/claude-code
fi

# --- Claude Code: optional bypass-permissions default ----------------------
#
# Opt-in (CLAUDE_BYPASS_PERMISSIONS=1) because it disables ALL of Claude Code's
# permission prompts — appropriate for a single-user autonomous dev box, NOT a
# safe silent default for a public installer. Merges into the user's real
# ~/.claude/settings.json (preserving theme etc.); NOT managed by home-manager
# so palmux2's own UI can still write to it.
if [ "$CLAUDE_BYPASS_PERMISSIONS" = "1" ]; then
  log "configuring Claude Code: permissions.defaultMode = bypassPermissions"
  CLAUDE_SETTINGS="${USER_HOME}/.claude/settings.json"
  install -d -m 0755 "${USER_HOME}/.claude"
  [ -f "$CLAUDE_SETTINGS" ] || echo '{}' > "$CLAUDE_SETTINGS"
  CS_TMP="$(mktemp)"
  jq '.permissions.defaultMode = "bypassPermissions"
      | .skipDangerousModePermissionPrompt = true' \
    "$CLAUDE_SETTINGS" > "$CS_TMP" && mv "$CS_TMP" "$CLAUDE_SETTINGS"
fi

# --- ghq / gwq / port-manager (binary releases, outside Nix) ---------------

resolve_tag() {
  local repo="$1" tag="$2"
  if [ "$tag" = "latest" ]; then
    curl -fsSL "https://api.github.com/repos/${repo}/releases/latest" | jq -r .tag_name
  else
    printf '%s\n' "$tag"
  fi
}

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT

# gwq
GWQ_TAG="$(resolve_tag "$GWQ_REPO" "$GWQ_VERSION")"
if [ -n "$GWQ_TAG" ] && [ "$GWQ_TAG" != "null" ]; then
  installed_gwq=""
  if command -v gwq >/dev/null 2>&1; then
    installed_gwq="$(gwq version 2>/dev/null | awk 'NR==1 {print $3}' || true)"
  fi
  gwq_clean="${GWQ_TAG#v}"
  if [ "$installed_gwq" = "$gwq_clean" ] || [ "$installed_gwq" = "$GWQ_TAG" ]; then
    log "gwq ${GWQ_TAG} already installed"
  else
    log "installing gwq ${GWQ_TAG}"
    curl -fsSL -o "${WORKDIR}/gwq.tar.gz" \
      "https://github.com/${GWQ_REPO}/releases/download/${GWQ_TAG}/gwq_Linux_${GWQ_ARCH}.tar.gz"
    tar -xzf "${WORKDIR}/gwq.tar.gz" -C "${WORKDIR}"
    sudo install -m 0755 "${WORKDIR}/gwq" "${BIN_DIR}/gwq"
  fi
else
  warn "could not resolve gwq tag; skipping"
fi

# port-manager
PORTMAN_TAG="$(resolve_tag "$PORTMAN_REPO" "$PORTMAN_VERSION")"
if [ -n "$PORTMAN_TAG" ] && [ "$PORTMAN_TAG" != "null" ]; then
  installed_portman=""
  if command -v portman >/dev/null 2>&1; then
    installed_portman="$(portman --version 2>/dev/null | awk '{print $NF}' || true)"
  fi
  portman_clean="${PORTMAN_TAG#v}"
  if [ "$installed_portman" = "$portman_clean" ] || [ "$installed_portman" = "$PORTMAN_TAG" ]; then
    log "port-manager ${PORTMAN_TAG} already installed"
  else
    log "installing port-manager ${PORTMAN_TAG}"
    curl -fsSL -o "${WORKDIR}/portman" \
      "https://github.com/${PORTMAN_REPO}/releases/download/${PORTMAN_TAG}/port-manager_${portman_clean}_linux_${PORTMAN_ARCH}"
    chmod +x "${WORKDIR}/portman"
    sudo install -m 0755 "${WORKDIR}/portman" "${BIN_DIR}/portman"
  fi
else
  warn "could not resolve port-manager tag; skipping"
fi

# --- /etc/palmux/flake.nix generation --------------------------------------

log "preparing /etc/palmux/ (user-owned so home-manager can write flake.lock)"
sudo install -d -m 0755 -o "$USERNAME" -g "$USERNAME" /etc/palmux

# Build the optional attrs block (domain / acmeEmail / basicAuth)
# carefully — using printf to avoid shell-expansion accidents.
OPTIONAL_ATTRS=""
if [ -n "$DOMAIN" ]; then
  OPTIONAL_ATTRS+=$'\n    domain = "'"$DOMAIN"'";'
fi
if [ -n "$ACME_EMAIL" ]; then
  OPTIONAL_ATTRS+=$'\n    acmeEmail = "'"$ACME_EMAIL"'";'
fi
if [ -n "$BASIC_AUTH_USER" ]; then
  OPTIONAL_ATTRS+=$'\n    basicAuth = { enable = true; user = "'"$BASIC_AUTH_USER"'"; };'
fi

log "generating /etc/palmux/flake.nix (palmux2=${PALMUX_TAG})"
cat > /etc/palmux/flake.nix <<EOF
# Generated by scripts/install.sh — do not edit by hand.
# Re-run the installer (with updated env vars) to regenerate.
{
  description = "palmux2 host configuration for ${HOSTNAME_VALUE}";

  inputs.palmux2.url = "${PALMUX_FLAKE_REF}";

  outputs = { palmux2, ... }: palmux2.lib.mkPalmuxHost {
    system = "${NIX_SYSTEM}";
    username = "${USERNAME}";
    homeDirectory = "${USER_HOME}";
    hostname = "${HOSTNAME_VALUE}";
    profile = "${PROFILE}";
    palmux2Version = "${PALMUX_TAG_BARE}";
    palmux2Hash = "${PALMUX_HASH_SRI}";
    caddyHash = "${CADDY_HASH_SRI}";${OPTIONAL_ATTRS}
  };
}
EOF

# --- home-manager switch ---------------------------------------------------

log "running home-manager switch (this may take a few minutes on first run)"
# Always start with a fresh lock — palmux2 input may have changed (path: source
# rsynced from dev box, or new GH commit on the production ref). Lock regen is
# fast since only the palmux2 input chain needs re-resolving; other inputs are
# fetched from cache.
rm -f /etc/palmux/flake.lock
nix run \
  --extra-experimental-features 'nix-command flakes' \
  home-manager/master -- \
  switch --flake "/etc/palmux#${USERNAME}" -b backup

# --- Server stability: unattended-upgrades + swap + sysctl (Story-3) ----------
#
# These are run unconditionally — they harden any palmux2 host (whether or not
# Caddy is enabled). Idempotent: configs are overwritten each run, swapfile is
# created only if missing.
SWAP_FILE="${SWAP_FILE:-/swapfile}"
SWAP_SIZE_GB="${SWAP_SIZE_GB:-8}"
SWAP_SWAPPINESS="${SWAP_SWAPPINESS:-10}"
KERNEL_PANIC_REBOOT_SECONDS="${KERNEL_PANIC_REBOOT_SECONDS:-10}"

# unattended-upgrades
log "configuring unattended-upgrades"
sudo apt-get install -y -qq unattended-upgrades
sudo tee /etc/apt/apt.conf.d/20auto-upgrades >/dev/null <<'EOF'
APT::Periodic::Update-Package-Lists "1";
APT::Periodic::Unattended-Upgrade "1";
APT::Periodic::Download-Upgradeable-Packages "1";
APT::Periodic::AutocleanInterval "7";
EOF
sudo tee /etc/apt/apt.conf.d/50unattended-upgrades >/dev/null <<'EOF'
Unattended-Upgrade::Allowed-Origins {
    "${distro_id}:${distro_codename}-security";
    "${distro_id}ESMApps:${distro_codename}-apps-security";
    "${distro_id}ESM:${distro_codename}-infra-security";
};
Unattended-Upgrade::Package-Blacklist {
    "linux-image";
    "linux-headers";
    "linux-generic";
    "grub";
    "shim";
};
Unattended-Upgrade::AutoFixInterruptedDpkg "true";
Unattended-Upgrade::MinimalSteps "true";
Unattended-Upgrade::InstallOnShutdown "false";
Unattended-Upgrade::Remove-Unused-Dependencies "true";
Unattended-Upgrade::Automatic-Reboot "false";
Unattended-Upgrade::DevRelease "auto";
EOF
sudo systemctl enable --now unattended-upgrades.service >/dev/null 2>&1 || true

# swap
if [ ! -f "$SWAP_FILE" ]; then
  log "creating ${SWAP_SIZE_GB}G swapfile at ${SWAP_FILE}"
  sudo fallocate -l "${SWAP_SIZE_GB}G" "$SWAP_FILE"
  sudo chmod 600 "$SWAP_FILE"
  sudo mkswap "$SWAP_FILE" >/dev/null
fi
if ! swapon --show=NAME --noheadings | grep -qx "$SWAP_FILE"; then
  sudo swapon "$SWAP_FILE" || warn "swapon failed (already enabled?)"
fi
if ! grep -qE "^${SWAP_FILE}[[:space:]]" /etc/fstab; then
  echo "${SWAP_FILE} none swap sw 0 0" | sudo tee -a /etc/fstab >/dev/null
fi

# sysctl: swappiness + panic-on-oom + auto-reboot on panic
log "writing /etc/sysctl.d/99-palmux.conf"
sudo tee /etc/sysctl.d/99-palmux.conf >/dev/null <<EOF
# Managed by palmux2 install.sh (Sprint Sfccb3f Story-3).
vm.swappiness = ${SWAP_SWAPPINESS}
vm.panic_on_oom = 1
kernel.panic = ${KERNEL_PANIC_REBOOT_SECONDS}
EOF
sudo sysctl --system >/dev/null

# --- Caddy + Cloudflare DNS-01 + edge basic_auth (Story-2) ------------------
#
# Note: We bypass numtide/system-manager here because the pinned commit
# (dc1baae, 2026-05-11) references NixOS modules that were removed from
# nixos-unstable (e.g., security/dhparams.nix). install.sh writes /etc/caddy/
# config + /etc/systemd/system/caddy.service directly, using a Nix-built
# caddy-cloudflare binary. This is a hybrid: Nix manages the binary, bash
# manages the system glue. system-manager can be revisited later when the
# upstream compatibility issue is resolved.

if [ "$CADDY_ENABLED" = "1" ]; then
  log "preparing Caddy (system user + /var/lib/caddy)"
  if ! id -u caddy >/dev/null 2>&1; then
    sudo useradd --system --home /var/lib/caddy --create-home \
      --user-group --shell /usr/sbin/nologin caddy
  fi
  sudo install -d -m 0750 -o caddy -g caddy /var/lib/caddy
  sudo install -d -m 0755 /etc/caddy

  log "building caddy-cloudflare via Nix (will use binary from /nix/store)"
  # --no-write-lock-file: when PALMUX_FLAKE_REF is a remote (github:) ref, nix
  # cannot write a lock file back to it. A committed flake.lock pins inputs;
  # this flag lets the build proceed against the remote ref regardless.
  CADDY_BIN_DIR="$(
    nix build --no-link --print-out-paths --no-write-lock-file \
      --extra-experimental-features 'nix-command flakes' \
      "${PALMUX_FLAKE_REF}#packages.${NIX_SYSTEM}.caddy-cloudflare"
  )"
  CADDY_BIN="${CADDY_BIN_DIR}/bin/caddy"
  [ -x "$CADDY_BIN" ] || die "caddy binary not found at $CADDY_BIN"
  log "caddy binary: $CADDY_BIN"

  BASIC_AUTH_HASH=""
  if [ -n "$BASIC_AUTH_USER" ]; then
    if [ -n "$BASIC_AUTH_PASSWORD" ]; then
      # bcrypt runs on every request (Caddy basic_auth is not cached), so the
      # cost factor is per-request latency. Default 8 keeps edge auth snappy;
      # override with BASIC_AUTH_BCRYPT_COST. (Caddy hash-password default 14 ≈ ~900ms.)
      log "hashing basic-auth password (bcrypt cost=${BASIC_AUTH_BCRYPT_COST} via $CADDY_BIN)"
      BASIC_AUTH_HASH="$("$CADDY_BIN" hash-password \
        --algorithm bcrypt --bcrypt-cost "$BASIC_AUTH_BCRYPT_COST" \
        --plaintext "$BASIC_AUTH_PASSWORD")"
    else
      # No password provided — reuse the existing hash read back above.
      log "keeping existing basic-auth hash (no BASIC_AUTH_PASSWORD provided)"
      BASIC_AUTH_HASH="$REUSE_BASIC_AUTH_HASH"
    fi
  fi

  log "writing /etc/caddy/palmux.env (root:caddy 0640)"
  # printf to avoid shell-expansion of bcrypt $... segments
  {
    printf 'CLOUDFLARE_API_TOKEN=%s\n' "$CLOUDFLARE_API_TOKEN"
    if [ -n "$BASIC_AUTH_USER" ]; then
      printf 'BASIC_AUTH_USER=%s\n' "$BASIC_AUTH_USER"
      printf 'BASIC_AUTH_HASH=%s\n' "$BASIC_AUTH_HASH"
    fi
  } | sudo tee /etc/caddy/palmux.env >/dev/null
  sudo chown root:caddy /etc/caddy/palmux.env
  sudo chmod 0640 /etc/caddy/palmux.env

  # --- Caddy config: PORTMAN_ROUTING=1 → caddy.json, else → Caddyfile -------

  if [ "$PORTMAN_ROUTING" = "1" ]; then
    log "PORTMAN_ROUTING=1: writing /etc/caddy/caddy.json (model B)"
    sudo rm -f /etc/caddy/Caddyfile

    # Build caddy.json using jq so it is always valid JSON.
    # The edge-basic-auth route (no match = applies to all hosts, non-terminal)
    # is included only when BASIC_AUTH_USER is set.
    # The ACME email field is included only when ACME_EMAIL is set.
    # The bcrypt hash MUST remain as the literal placeholder {env.BASIC_AUTH_HASH}
    # (NOT the real hash) — the real hash lives only in /etc/caddy/palmux.env.
    if [ -n "$BASIC_AUTH_USER" ]; then
      # With edge basic auth route
      if [ -n "$ACME_EMAIL" ]; then
        sudo jq -n \
          --arg domain "$DOMAIN" \
          --arg email "$ACME_EMAIL" \
          '{
            "admin": { "listen": "localhost:2019" },
            "apps": {
              "http": {
                "servers": {
                  "srv0": {
                    "listen": [":443"],
                    "routes": [
                      {
                        "@id": "edge-basic-auth",
                        "handle": [
                          {
                            "handler": "authentication",
                            "providers": {
                              "http_basic": {
                                "hash": { "algorithm": "bcrypt" },
                                "accounts": [
                                  {
                                    "username": "{env.BASIC_AUTH_USER}",
                                    "password": "{env.BASIC_AUTH_HASH}"
                                  }
                                ]
                              }
                            }
                          }
                        ]
                      }
                    ],
                    "automatic_https": { "disable_certificates": true }
                  }
                }
              },
              "tls": {
                "certificates": {
                  "automate": [$domain, ("*." + $domain)]
                },
                "automation": {
                  "policies": [
                    {
                      "subjects": [$domain, ("*." + $domain)],
                      "issuers": [
                        {
                          "module": "acme",
                          "email": $email,
                          "challenges": {
                            "dns": {
                              "provider": {
                                "name": "cloudflare",
                                "api_token": "{env.CLOUDFLARE_API_TOKEN}"
                              }
                            }
                          }
                        }
                      ]
                    }
                  ]
                }
              }
            }
          }' | sudo tee /etc/caddy/caddy.json >/dev/null
      else
        # No email
        sudo jq -n \
          --arg domain "$DOMAIN" \
          '{
            "admin": { "listen": "localhost:2019" },
            "apps": {
              "http": {
                "servers": {
                  "srv0": {
                    "listen": [":443"],
                    "routes": [
                      {
                        "@id": "edge-basic-auth",
                        "handle": [
                          {
                            "handler": "authentication",
                            "providers": {
                              "http_basic": {
                                "hash": { "algorithm": "bcrypt" },
                                "accounts": [
                                  {
                                    "username": "{env.BASIC_AUTH_USER}",
                                    "password": "{env.BASIC_AUTH_HASH}"
                                  }
                                ]
                              }
                            }
                          }
                        ]
                      }
                    ],
                    "automatic_https": { "disable_certificates": true }
                  }
                }
              },
              "tls": {
                "certificates": {
                  "automate": [$domain, ("*." + $domain)]
                },
                "automation": {
                  "policies": [
                    {
                      "subjects": [$domain, ("*." + $domain)],
                      "issuers": [
                        {
                          "module": "acme",
                          "challenges": {
                            "dns": {
                              "provider": {
                                "name": "cloudflare",
                                "api_token": "{env.CLOUDFLARE_API_TOKEN}"
                              }
                            }
                          }
                        }
                      ]
                    }
                  ]
                }
              }
            }
          }' | sudo tee /etc/caddy/caddy.json >/dev/null
      fi
    else
      # No basic auth
      if [ -n "$ACME_EMAIL" ]; then
        sudo jq -n \
          --arg domain "$DOMAIN" \
          --arg email "$ACME_EMAIL" \
          '{
            "admin": { "listen": "localhost:2019" },
            "apps": {
              "http": {
                "servers": {
                  "srv0": {
                    "listen": [":443"],
                    "routes": [],
                    "automatic_https": { "disable_certificates": true }
                  }
                }
              },
              "tls": {
                "certificates": {
                  "automate": [$domain, ("*." + $domain)]
                },
                "automation": {
                  "policies": [
                    {
                      "subjects": [$domain, ("*." + $domain)],
                      "issuers": [
                        {
                          "module": "acme",
                          "email": $email,
                          "challenges": {
                            "dns": {
                              "provider": {
                                "name": "cloudflare",
                                "api_token": "{env.CLOUDFLARE_API_TOKEN}"
                              }
                            }
                          }
                        }
                      ]
                    }
                  ]
                }
              }
            }
          }' | sudo tee /etc/caddy/caddy.json >/dev/null
      else
        sudo jq -n \
          --arg domain "$DOMAIN" \
          '{
            "admin": { "listen": "localhost:2019" },
            "apps": {
              "http": {
                "servers": {
                  "srv0": {
                    "listen": [":443"],
                    "routes": [],
                    "automatic_https": { "disable_certificates": true }
                  }
                }
              },
              "tls": {
                "certificates": {
                  "automate": [$domain, ("*." + $domain)]
                },
                "automation": {
                  "policies": [
                    {
                      "subjects": [$domain, ("*." + $domain)],
                      "issuers": [
                        {
                          "module": "acme",
                          "challenges": {
                            "dns": {
                              "provider": {
                                "name": "cloudflare",
                                "api_token": "{env.CLOUDFLARE_API_TOKEN}"
                              }
                            }
                          }
                        }
                      ]
                    }
                  ]
                }
              }
            }
          }' | sudo tee /etc/caddy/caddy.json >/dev/null
      fi
    fi

    sudo chmod 0644 /etc/caddy/caddy.json

    log "installing /etc/systemd/system/caddy.service (model B — caddy.json)"
    sudo tee /etc/systemd/system/caddy.service >/dev/null <<EOF
[Unit]
Description=Caddy web server (with caddy-dns/cloudflare for Let's Encrypt DNS-01)
Documentation=https://caddyserver.com/docs/
After=network.target network-online.target
Requires=network-online.target

[Service]
Type=notify
User=caddy
Group=caddy
EnvironmentFile=/etc/caddy/palmux.env
ExecStart=${CADDY_BIN} run --environ --config /etc/caddy/caddy.json
ExecReload=${CADDY_BIN} reload --config /etc/caddy/caddy.json --force
TimeoutStopSec=5s
LimitNOFILE=1048576
PrivateTmp=true
ProtectSystem=full
AmbientCapabilities=CAP_NET_BIND_SERVICE
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

  else
    # Default: single-site Caddyfile (Sfccb3f behavior)
    log "writing /etc/caddy/Caddyfile (domain=${DOMAIN}, basic_auth=$( [ -n "$BASIC_AUTH_USER" ] && echo yes || echo no ))"
    {
      echo "{"
      [ -n "$ACME_EMAIL" ] && echo "    email ${ACME_EMAIL}"
      echo "}"
      echo ""
      echo "${DOMAIN} {"
      if [ -n "$BASIC_AUTH_USER" ]; then
        echo "    basic_auth {"
        echo "        {env.BASIC_AUTH_USER} {env.BASIC_AUTH_HASH}"
        echo "    }"
      fi
      echo "    reverse_proxy 127.0.0.1:8080"
      echo "    tls {"
      echo "        dns cloudflare {env.CLOUDFLARE_API_TOKEN}"
      echo "    }"
      echo "    encode zstd gzip"
      echo "}"
    } | sudo tee /etc/caddy/Caddyfile >/dev/null

    log "installing /etc/systemd/system/caddy.service (ExecStart=${CADDY_BIN})"
    sudo tee /etc/systemd/system/caddy.service >/dev/null <<EOF
[Unit]
Description=Caddy web server (with caddy-dns/cloudflare for Let's Encrypt DNS-01)
Documentation=https://caddyserver.com/docs/
After=network.target network-online.target
Requires=network-online.target

[Service]
Type=notify
User=caddy
Group=caddy
EnvironmentFile=/etc/caddy/palmux.env
ExecStart=${CADDY_BIN} run --environ --config /etc/caddy/Caddyfile
ExecReload=${CADDY_BIN} reload --config /etc/caddy/Caddyfile --force
TimeoutStopSec=5s
LimitNOFILE=1048576
PrivateTmp=true
ProtectSystem=full
AmbientCapabilities=CAP_NET_BIND_SERVICE
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF
  fi

  sudo systemctl daemon-reload
  sudo systemctl enable caddy
  # restart (not reload): systemd's EnvironmentFile is only read at process
  # start, so a `reload` would not pick up rotated CLOUDFLARE_API_TOKEN /
  # BASIC_AUTH_HASH in /etc/caddy/palmux.env. restart is sub-second on Caddy
  # and is the only way to guarantee a secret rotation re-run is effective.
  log "(re)starting Caddy"
  sudo systemctl restart caddy
fi

# --- Portman routing: model B systemd units + config (S85caca) ---------------
#
# Only runs when PORTMAN_ROUTING=1. Creates /etc/portman config, installs
# 4 systemd system units (portman-serve, portman-sync, portman-gc service +
# timer), and runs an initial portman sync to register the palmux2 route.
#
# Troubleshooting — wildcard cert never obtained (palmux2.<base> /
# *.<base> return TLS "internal error", and the SPA loads but its API calls
# fail with "Failed to fetch"):
#   Symptom in the Caddy log: the DNS-01 challenge VALIDATES, but then acme-v02
#   (production) returns "No order for ID" / "No such authorization" /
#   "accountDoesNotExist", repeated "creating new account", and the cert never
#   lands (staging works end-to-end; DNS-01 itself is fine).
#   Root cause: `portman sync` mutating Caddy via the admin API DURING the
#   wildcard's DNS-01 validation window reloads the config and re-inits Caddy's
#   ACME provider before the account/order is persisted, orphaning the order.
#   Prevention: this installer now waits for the wildcard cert before the first
#   portman sync (see the wait loop below), and portman-sync.service is enabled
#   only after that, so a fresh install serializes cert-then-routes.
#   Recovery if a host is already stuck in this state (clears orphaned ACME
#   account state; obtains the cert with no admin-API thrash; existing certs are
#   preserved):
#     sudo systemctl disable --now portman-sync.service
#     sudo systemctl stop caddy
#     sudo rm -rf /var/lib/caddy/.local/share/caddy/acme/acme-v02.api.letsencrypt.org-directory/users
#     sudo systemctl start caddy            # waits/obtains the wildcard cert cleanly
#     # once .../certificates/.../wildcard_.<base>/*.crt exists:
#     sudo systemctl enable portman-sync.service
#     PORTMAN_CONFIG_DIR=/etc/portman portman sync
#   install.sh deliberately does NOT reset ACME accounts automatically — wiping a
#   working account on a healthy host would be destructive.

if [ "$PORTMAN_ROUTING" = "1" ]; then
  log "PORTMAN_ROUTING=1: configuring portman model B routing"

  # Create directories
  sudo install -d -m 0755 /etc/portman
  sudo install -d -m 0755 -o "$USERNAME" -g "$USERNAME" /var/lib/portman
  sudo install -d -m 0755 -o "$USERNAME" -g "$USERNAME" /var/lib/portman/portal

  # Write /etc/portman/config.toml
  log "writing /etc/portman/config.toml"
  sudo tee /etc/portman/config.toml >/dev/null <<EOF
[general]
db_path = "/var/lib/portman/portman.db"

[ports]
range_start = 8200
range_end = 8999
stale_ttl_hours = 24

[proxy]
type = "caddy"
caddy_api = "http://localhost:2019"
domain_suffix = "${DOMAIN}"
host_pattern = "{name}--{worktree}--{repo}"

[dashboard]
enabled = true
host = "${DOMAIN}"
output_dir = "/var/lib/portman/portal"
auto_update = true
serve_addr = "127.0.0.1:8090"
EOF
  sudo chmod 0644 /etc/portman/config.toml

  # Write /etc/portman/services.json using jq for valid JSON
  log "writing /etc/portman/services.json"
  jq -n '{
    "reserved": [
      {"port": 80,   "description": "caddy http"},
      {"port": 443,  "description": "caddy https"},
      {"port": 2019, "description": "caddy admin api"},
      {"port": 8080, "description": "palmux2"},
      {"port": 8090, "description": "portman dashboard"}
    ],
    "permanent": [
      {"name": "palmux2", "port": 8080, "expose": true}
    ]
  }' | sudo tee /etc/portman/services.json >/dev/null
  sudo chmod 0644 /etc/portman/services.json

  # Install portman-serve.service
  log "installing portman-serve.service"
  sudo tee /etc/systemd/system/portman-serve.service >/dev/null <<EOF
[Unit]
Description=portman live dashboard server
After=network.target

[Service]
Type=simple
User=${USERNAME}
Environment=PORTMAN_CONFIG_DIR=/etc/portman
ExecStart=${BIN_DIR}/portman serve --addr 127.0.0.1:8090
Restart=on-failure
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

  # Install portman-sync.service
  log "installing portman-sync.service"
  sudo tee /etc/systemd/system/portman-sync.service >/dev/null <<EOF
[Unit]
Description=portman Caddy route sync
After=caddy.service
BindsTo=caddy.service

[Service]
Type=oneshot
User=${USERNAME}
Environment=PORTMAN_CONFIG_DIR=/etc/portman
ExecStartPre=/bin/sleep 2
ExecStart=${BIN_DIR}/portman sync
RemainAfterExit=yes

[Install]
WantedBy=caddy.service
EOF

  # Install portman-gc.service
  log "installing portman-gc.service"
  sudo tee /etc/systemd/system/portman-gc.service >/dev/null <<EOF
[Unit]
Description=portman garbage collection

[Service]
Type=oneshot
User=${USERNAME}
Environment=PORTMAN_CONFIG_DIR=/etc/portman
ExecStart=${BIN_DIR}/portman gc
EOF

  # Install portman-gc.timer
  log "installing portman-gc.timer"
  sudo tee /etc/systemd/system/portman-gc.timer >/dev/null <<EOF
[Unit]
Description=portman periodic garbage collection

[Timer]
OnCalendar=hourly
Persistent=true

[Install]
WantedBy=timers.target
EOF

  sudo systemctl daemon-reload
  sudo systemctl enable --now portman-serve.service
  sudo systemctl enable portman-sync.service
  sudo systemctl enable --now portman-gc.timer

  # IMPORTANT: wait for Caddy to obtain the *.${DOMAIN} wildcard cert BEFORE the
  # first portman sync. `portman sync` mutates Caddy via the admin API, which
  # triggers a config reload. Doing that during the wildcard's DNS-01 validation
  # window (~tens of seconds) repeatedly re-inits Caddy's ACME provider before
  # the new account/order is persisted, orphaning the order — Let's Encrypt then
  # reports "No order for ID" / "accountDoesNotExist" and the wildcard cert never
  # lands (DNS-01 itself validates fine; staging slips through because it's
  # faster). Serializing cert-then-sync avoids the thrash. Cert issuance happened
  # on the earlier `systemctl restart caddy`, when portman-sync.service was not
  # yet enabled, so this is a clean window to wait on.
  CADDY_CERT_DIR="/var/lib/caddy/.local/share/caddy/certificates"
  log "waiting for Caddy to obtain the *.${DOMAIN} wildcard cert before portman sync (up to 240s)"
  cert_ready=0
  for _i in $(seq 1 48); do
    if sudo find "$CADDY_CERT_DIR" -path "*wildcard_.${DOMAIN}*" -name '*.crt' 2>/dev/null | grep -q .; then
      cert_ready=1
      break
    fi
    sleep 5
  done
  if [ "$cert_ready" = "1" ]; then
    log "wildcard cert obtained — proceeding with portman sync"
  else
    warn "wildcard cert not obtained within timeout; proceeding anyway (Caddy + portman-sync.service keep retrying). If https://palmux2.${DOMAIN}/ shows a TLS error, see the ACME troubleshooting note above."
  fi

  # Initial portman sync to register the palmux2 route in Caddy immediately.
  # Non-fatal: portman-sync.service will reconcile on next caddy (re)start.
  log "running initial portman sync (PORTMAN_CONFIG_DIR=/etc/portman)"
  PORTMAN_CONFIG_DIR=/etc/portman "${BIN_DIR}/portman" sync \
    || warn "initial portman sync failed (caddy may not be ready yet); portman-sync.service will retry on next caddy start"
fi

# --- Incus container runtime (S8478ca — default ON, SKIP_INCUS=1 to opt out) ------
#
# Sets up the host prerequisites for `incus-container` workspaces:
#   1. incus package (apt)
#   2. incus admin init --minimal (idempotent)
#   3. install user added to incus-admin group
#   4. /etc/subuid + /etc/subgid get root:1000:1 (required for raw.idmap unprivileged)
#   5. Docker FORWARD coexistence rule (best-effort, if Docker is present)
#   6. palmux-ws image imported via `palmux runtime install`
#   7. `palmux runtime doctor` run so the user sees green / what's still needed
#
# All steps are idempotent (safe to re-run on update).
#
if [ "${SKIP_INCUS}" != "1" ]; then
  log "=== Incus container runtime setup ==="

  # ── 1. install incus if absent ──────────────────────────────────────────────
  if command -v incus >/dev/null 2>&1; then
    log "incus already installed: $(incus --version 2>/dev/null || echo '?')"
  else
    log "installing incus via apt (Ubuntu 24.04 universe / Debian)"
    if sudo apt-get install -y incus 2>/dev/null; then
      log "incus installed: $(incus --version 2>/dev/null || echo '?')"
    else
      warn "incus not available via apt on this system."
      warn "Install manually: https://linuxcontainers.org/incus/docs/main/installing/"
      warn "Or: sudo apt-get install -y incus  (requires Ubuntu 24.04+ universe or Debian 13+)"
      warn "Skipping remaining Incus setup — run the installer again after installing incus."
      # Skip the rest of the incus block without aborting the whole install.
      SKIP_INCUS=1
    fi
  fi

  if [ "${SKIP_INCUS}" != "1" ]; then

    # ── 2. init the daemon if not already inited ──────────────────────────────
    # Guard: `incus network list` returns non-zero if the daemon has not been
    # initialized (no storage pool / network created yet).
    if incus network list </dev/null >/dev/null 2>&1; then
      log "incus daemon already initialized"
    else
      log "initializing incus daemon (incus admin init --minimal)"
      sudo incus admin init --minimal </dev/null
      log "incus daemon initialized"
    fi

    # ── 3. add install user to incus-admin group ──────────────────────────────
    if id -nG "$USERNAME" 2>/dev/null | tr ' ' '\n' | grep -qx "incus-admin"; then
      log "user ${USERNAME} already in incus-admin group"
    else
      log "adding ${USERNAME} to incus-admin group"
      sudo usermod -aG incus-admin "$USERNAME"
      warn "----------------------------------------------------------------------"
      warn "IMPORTANT: ${USERNAME} was added to the incus-admin group."
      warn "You MUST log out and back in (or run: newgrp incus-admin) for non-sudo"
      warn "incus access to take effect.  The palmux service also needs this group"
      warn "to launch incus-container workspaces — restart it after re-login:"
      warn "  systemctl --user restart palmux2"
      warn "----------------------------------------------------------------------"
    fi

    # ── 4. subuid / subgid: ensure root:1000:1 ────────────────────────────────
    # Required so Incus can map UID 1000 (the workspace user) into an
    # unprivileged container via raw.idmap 'both 1000 1000'.
    _subuid_changed=0
    for _sub_file in /etc/subuid /etc/subgid; do
      if grep -qxF "root:1000:1" "$_sub_file" 2>/dev/null; then
        log "${_sub_file}: root:1000:1 already present"
      else
        log "${_sub_file}: adding root:1000:1"
        echo "root:1000:1" | sudo tee -a "$_sub_file" >/dev/null
        _subuid_changed=1
      fi
    done
    if [ "$_subuid_changed" = "1" ]; then
      log "restarting incus to pick up new subuid/subgid mapping"
      sudo systemctl restart incus
      log "incus restarted"
    fi

    # ── 5. Docker coexistence: allow incusbr0 forwarding ─────────────────────
    # If Docker is active its iptables rules set FORWARD policy=DROP, which
    # blocks incusbr0 outbound traffic (apt/claude cannot reach the internet).
    if ip link show docker0 >/dev/null 2>&1; then
      warn "Docker detected — checking FORWARD coexistence with incusbr0"
      if sudo iptables -C DOCKER-USER -i incusbr0 -j ACCEPT >/dev/null 2>&1; then
        log "iptables DOCKER-USER incusbr0 ACCEPT rule already present"
      else
        log "adding iptables rule: -I DOCKER-USER -i incusbr0 -j ACCEPT"
        sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT || \
          warn "iptables rule failed (DOCKER-USER chain may not exist yet; Docker may not be running)"
        warn "NOTE: this iptables rule is NOT persistent across reboots."
        warn "To make it permanent, install iptables-persistent or add to /etc/rc.local:"
        warn "  iptables -I DOCKER-USER -i incusbr0 -j ACCEPT"
      fi
    fi

    # ── 6. import the palmux-ws image ─────────────────────────────────────────
    # Run as the install user (not root) because the incus image store is
    # per-user-socket (accessed via incus-admin group).
    # The incus-admin group may not be active in the current shell session yet
    # (it was just added above).  We use `sg incus-admin` to activate it for
    # this one command, which works even without re-login.
    #
    # Build the palmux runtime install arguments.
    _runtime_install_args=""
    if [ -n "$PALMUX_WS_IMAGE_FILE" ]; then
      _runtime_install_args="--image-file $(printf '%q' "$PALMUX_WS_IMAGE_FILE")"
    elif [ -n "$PALMUX_WS_IMAGE_URL" ]; then
      _runtime_install_args="--image-url $(printf '%q' "$PALMUX_WS_IMAGE_URL")"
    elif [ "$PALMUX_WS_PRE" = "1" ]; then
      _runtime_install_args="--pre"
    fi

    # Locate the palmux2 binary (home-manager puts it in the Nix profile).
    # The binary installed by home-manager is named palmux2.  The home-manager
    # profile bin dir is typically ~/.nix-profile/bin or
    # /etc/profiles/per-user/<user>/bin — mirror how ghq/tmux are found post-
    # switch.  We search the known profile paths first (PATH may not yet include
    # the hm profile for the current shell invocation) then fall back to PATH.
    _palmux_bin=""
    for _candidate in \
      "${USER_HOME}/.nix-profile/bin/palmux2" \
      "/etc/profiles/per-user/${USERNAME}/bin/palmux2" \
      "${BIN_DIR}/palmux2" \
      "$(command -v palmux2 2>/dev/null || true)"; do
      if [ -n "$_candidate" ] && [ -x "$_candidate" ]; then
        _palmux_bin="$_candidate"
        break
      fi
    done

    if [ -z "$_palmux_bin" ]; then
      warn "palmux2 binary not found on PATH — skipping image import."
      warn "Run manually after install:  palmux2 runtime install"
    else
      log "importing palmux-ws image via: ${_palmux_bin} runtime install ${_runtime_install_args}"
      # sg activates the incus-admin group for this subshell so the group
      # membership added in step 3 is effective without a re-login.
      # shellcheck disable=SC2086
      if sg incus-admin -c "${_palmux_bin} runtime install ${_runtime_install_args}"; then
        log "palmux-ws image import completed"
      else
        _exit=$?
        warn "palmux2 runtime install exited with code ${_exit}."
        warn "This may mean no stable release asset exists yet (RC period)."
        warn "Try:  PALMUX_WS_PRE=1 bash ~/update-palmux2.sh"
        warn "Or:   palmux2 runtime install --pre"
        warn "Or build locally:  bash images/workspace-default/build.sh"
        warn "Incus itself is set up; the image can be added later."
      fi
    fi

    # ── 7. run palmux2 runtime doctor ─────────────────────────────────────────
    log "running palmux2 runtime doctor (host prerequisites check)"
    if [ -n "$_palmux_bin" ]; then
      sg incus-admin -c "${_palmux_bin} runtime doctor" || true
    else
      warn "palmux2 not found — skipping doctor; run manually: palmux2 runtime doctor"
    fi

  fi  # end inner SKIP_INCUS guard (apt failure path)
fi    # end outer SKIP_INCUS guard

# --- linger + service ------------------------------------------------------

if [ "${SKIP_SERVICE:-0}" != "1" ]; then
  if ! loginctl show-user "$USERNAME" 2>/dev/null | grep -q "Linger=yes"; then
    log "enabling user linger (so palmux2 keeps running after logout)"
    sudo loginctl enable-linger "$USERNAME"
  fi

  log "enabling palmux2 user systemd service"
  systemctl --user daemon-reload || true
  systemctl --user enable --now palmux2 || warn "systemctl --user enable failed (may need to reopen shell)"
fi

# --- generate update helper -------------------------------------------------
# Records the (non-secret) options used for this install so future updates are a
# single command: ~/update-palmux2.sh. Secrets are intentionally NOT written
# here — install.sh reads the CF token + basic-auth hash back from
# /etc/caddy/palmux.env on re-run (see "reuse persisted secrets" above). To
# rotate a secret, export CLOUDFLARE_API_TOKEN / BASIC_AUTH_PASSWORD before running.
UPDATE_SCRIPT="${USER_HOME}/update-palmux2.sh"

# Where the helper re-fetches install.sh from: honor an explicit
# PALMUX_INSTALLER_URL, else derive from PALMUX_FLAKE_REF so it tracks the same
# source this install came from.
if [ -n "${PALMUX_INSTALLER_URL:-}" ]; then
  INSTALLER_LINE="curl -fsSL \"${PALMUX_INSTALLER_URL}\" | bash"
else
  case "$PALMUX_FLAKE_REF" in
    github:*\?ref=*)
      _gh="${PALMUX_FLAKE_REF#github:}"; _repo="${_gh%%\?*}"; _ref="${_gh#*ref=}"; _ref="${_ref%%&*}"
      INSTALLER_LINE="curl -fsSL \"https://raw.githubusercontent.com/${_repo}/refs/heads/${_ref}/scripts/install.sh\" | bash"
      ;;
    path:*)
      INSTALLER_LINE="bash \"${PALMUX_FLAKE_REF#path:}/scripts/install.sh\""
      ;;
    github:*/*)
      INSTALLER_LINE="curl -fsSL \"https://raw.githubusercontent.com/${PALMUX_FLAKE_REF#github:}/main/scripts/install.sh\" | bash"
      ;;
    *)
      INSTALLER_LINE="curl -fsSL \"https://raw.githubusercontent.com/tjst-t/palmux2/main/scripts/install.sh\" | bash"
      ;;
  esac
fi

log "writing update helper ${UPDATE_SCRIPT}"
{
  echo '#!/usr/bin/env bash'
  echo '#'
  echo '# Generated by palmux2 install.sh — re-run to update palmux2 + tooling and'
  echo "# re-apply this host's config. Secrets (CF token, basic-auth password) are"
  echo '# NOT stored here; install.sh reuses them from /etc/caddy/palmux.env.'
  echo '# To rotate a secret, export CLOUDFLARE_API_TOKEN / BASIC_AUTH_PASSWORD first.'
  echo '# To pin versions, export PALMUX_VERSION / GHQ_VERSION / GWQ_VERSION / PORTMAN_VERSION.'
  echo 'set -euo pipefail'
  echo "export PROFILE=$(printf '%q' "$PROFILE")"
  echo "export PALMUX_FLAKE_REF=$(printf '%q' "$PALMUX_FLAKE_REF")"
  [ "$PORTMAN_ROUTING" = "1" ] && echo 'export PORTMAN_ROUTING=1'
  [ "$CLAUDE_BYPASS_PERMISSIONS" = "1" ] && echo 'export CLAUDE_BYPASS_PERMISSIONS=1'
  [ -n "$DOMAIN" ] && echo "export DOMAIN=$(printf '%q' "$DOMAIN")"
  [ -n "$ACME_EMAIL" ] && echo "export ACME_EMAIL=$(printf '%q' "$ACME_EMAIL")"
  [ -n "$BASIC_AUTH_USER" ] && echo "export BASIC_AUTH_USER=$(printf '%q' "$BASIC_AUTH_USER")"
  echo "export BASIC_AUTH_BCRYPT_COST=$(printf '%q' "$BASIC_AUTH_BCRYPT_COST")"
  # Incus options (S8478ca)
  [ "$SKIP_INCUS" = "1" ] && echo 'export SKIP_INCUS=1'
  [ "$PALMUX_WS_PRE" = "1" ] && echo 'export PALMUX_WS_PRE=1'
  [ -n "$PALMUX_WS_IMAGE_URL" ] && echo "export PALMUX_WS_IMAGE_URL=$(printf '%q' "$PALMUX_WS_IMAGE_URL")"
  # PALMUX_WS_IMAGE_FILE is a local path; only persist if it still exists at update time.
  [ -n "$PALMUX_WS_IMAGE_FILE" ] && [ -f "$PALMUX_WS_IMAGE_FILE" ] && \
    echo "export PALMUX_WS_IMAGE_FILE=$(printf '%q' "$PALMUX_WS_IMAGE_FILE")"
  echo "$INSTALLER_LINE"
} > "$UPDATE_SCRIPT"
chmod 0755 "$UPDATE_SCRIPT"

# --- summary ---------------------------------------------------------------

cat <<EOM

==> palmux2 installed via Nix (profile=${PROFILE}).

    palmux2  $(palmux2 --version 2>/dev/null || echo '?')
    tmux     $(tmux -V 2>/dev/null || echo '?')
    ghq      $(ghq --version 2>/dev/null | awk '{print $3}' || echo '?')
    gwq      $(gwq version 2>/dev/null | awk 'NR==1 {print $3}' || echo '?')
    portman  $(portman --version 2>/dev/null | awk '{print $NF}' || echo '(skipped)')
    node     $(node --version 2>/dev/null || echo '(skipped)')
    claude   $(claude --version 2>/dev/null | head -1 || echo '(skipped)')
    go       $(go version 2>/dev/null | awk '{print $3}' || echo '?')

==> Next:
$(if [ "$PORTMAN_ROUTING" = "1" ]; then
  echo "    1. palmux2 UI:        https://palmux2.${DOMAIN}/"
  echo "       portman dashboard: https://${DOMAIN}/"
  echo "       expose pattern:    https://<name>--<worktree>--<repo>.${DOMAIN}/"
  if [ -n "$BASIC_AUTH_USER" ]; then
    echo "       (basic auth: user=${BASIC_AUTH_USER})"
  fi
  echo "    2. To expose a service: PORTMAN_CONFIG_DIR=/etc/portman portman exec --name X --expose -- cmd"
elif [ "$CADDY_ENABLED" = "1" ]; then
  echo "    1. Open  https://${DOMAIN}/  (Let's Encrypt cert via Cloudflare DNS-01)"
  if [ -n "$BASIC_AUTH_USER" ]; then
    echo "       (basic auth: user=${BASIC_AUTH_USER})"
  fi
else
  echo "    1. Open  http://$(hostname -I 2>/dev/null | awk '{print $1}'):8080"
fi)
    $(if [ "$PORTMAN_ROUTING" = "1" ]; then echo "3."; else echo "2."; fi) To update later, just run:  ${USER_HOME}/update-palmux2.sh
       (generated with this host's options; reuses secrets from /etc/caddy/palmux.env).
       Nix produces a new generation; failures roll back automatically. Rotate
       secrets by exporting CLOUDFLARE_API_TOKEN / BASIC_AUTH_PASSWORD first.
EOM
