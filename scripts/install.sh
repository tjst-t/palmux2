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
#   GHQ_VERSION            ghq release tag (default: latest)
#   GWQ_VERSION            gwq release tag (default: latest)
#   PORTMAN_VERSION        port-manager release tag (default: latest)
#   NODE_MAJOR             Node.js major version line (default: 20)
#   SKIP_NODE=1            skip Node.js + @anthropic-ai/claude-code install
#   SKIP_SERVICE=1         skip systemctl --user enable palmux2
#
# HTTPS via Caddy + Cloudflare DNS-01 (Story-2 — set BOTH to enable):
#   DOMAIN                 e.g. palmux.example.com
#   CLOUDFLARE_API_TOKEN   scoped CF token (Zone:DNS:Edit + Zone:Zone:Read)
#   ACME_EMAIL             (optional) email for Let's Encrypt notifications
#
# HTTP basic auth at the Caddy edge (Story-2 — requires Caddy):
#   BASIC_AUTH_USER        username
#   BASIC_AUTH_PASSWORD    plaintext password (bcrypt-hashed by Caddy)
#
set -euo pipefail

# --- env / defaults --------------------------------------------------------

PALMUX_VERSION="${PALMUX_VERSION:-}" # currently informational
PROFILE="${PROFILE:-minimal}"
PALMUX_FLAKE_REF="${PALMUX_FLAKE_REF:-github:tjst-t/palmux2}"

GHQ_VERSION="${GHQ_VERSION:-latest}"
GWQ_VERSION="${GWQ_VERSION:-latest}"
PORTMAN_VERSION="${PORTMAN_VERSION:-latest}"
NODE_MAJOR="${NODE_MAJOR:-20}"

DOMAIN="${DOMAIN:-}"
CLOUDFLARE_API_TOKEN="${CLOUDFLARE_API_TOKEN:-}"
ACME_EMAIL="${ACME_EMAIL:-}"
BASIC_AUTH_USER="${BASIC_AUTH_USER:-}"
BASIC_AUTH_PASSWORD="${BASIC_AUTH_PASSWORD:-}"

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

# Story-2 validations (currently warn-only — Story-2 will enforce + wire Caddy)
if [ -n "$DOMAIN" ] || [ -n "$CLOUDFLARE_API_TOKEN" ] || [ -n "$BASIC_AUTH_USER" ] || [ -n "$BASIC_AUTH_PASSWORD" ]; then
  warn "DOMAIN / CLOUDFLARE_API_TOKEN / BASIC_AUTH_* are accepted but Caddy integration lands in Story Sfccb3f-2"
  warn "Story-1 では値は /etc/palmux/flake.nix に書かれるが、 Caddy 設定は生成されない"
fi

USERNAME="$(id -un)"
USER_HOME="$(getent passwd "$USERNAME" | cut -d: -f6)"
HOSTNAME_VALUE="$(hostname)"

log "User: ${USERNAME}, Home: ${USER_HOME}, Host: ${HOSTNAME_VALUE}"
log "Profile: ${PROFILE}, Flake ref: ${PALMUX_FLAKE_REF}"

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

log "generating /etc/palmux/flake.nix"
sudo install -d -m 0755 /etc/palmux

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

sudo tee /etc/palmux/flake.nix >/dev/null <<EOF
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
    profile = "${PROFILE}";${OPTIONAL_ATTRS}
  };
}
EOF

# --- home-manager switch ---------------------------------------------------

log "running home-manager switch (this may take a few minutes on first run)"
nix run \
  --extra-experimental-features 'nix-command flakes' \
  home-manager/master -- \
  switch --flake "/etc/palmux#${USERNAME}" -b backup

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
    1. Open  http://$(hostname -I 2>/dev/null | awk '{print $1}'):8080
    2. To update later, re-run this same one-liner. Nix produces a new
       generation; failures roll back automatically.
EOM
