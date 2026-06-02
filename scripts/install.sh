#!/usr/bin/env bash
#
# Palmux installer / updater for Ubuntu/Debian.
#
# One-liner:
#   curl -fsSL https://raw.githubusercontent.com/tjst-t/palmux2/main/scripts/install.sh | bash
#
# Re-running the same command upgrades to the latest versions.
#
# Environment overrides (all optional):
#   PALMUX_VERSION         palmux release tag, e.g. "v0.8.0" (default: latest)
#   GHQ_VERSION            ghq release tag (default: latest)
#   GWQ_VERSION            gwq release tag (default: latest)
#   PORTMAN_VERSION        port-manager release tag (default: latest)
#   NODE_MAJOR             Node.js major version line (default: 20)
#   SKIP_NODE=1            don't install Node.js and @anthropic-ai/claude-code
#   SKIP_SERVICE=1         don't install the systemd user unit for palmux
#   SKIP_PORTMAN=1         don't install port-manager
#
# HTTPS via Caddy + Cloudflare DNS-01 (opt-in — set BOTH to enable):
#   DOMAIN                 e.g. palmux.example.com
#   CLOUDFLARE_API_TOKEN   scoped CF API token with Zone:DNS:Edit + Zone:Zone:Read
#   ACME_EMAIL             (optional) email for Let's Encrypt notifications
#
# HTTP basic auth at the Caddy edge (opt-in — requires Caddy enabled above):
#   BASIC_AUTH_USER        username
#   BASIC_AUTH_PASSWORD    plaintext password (will be bcrypt-hashed by Caddy)
#
set -euo pipefail

PALMUX_VERSION="${PALMUX_VERSION:-latest}"
GHQ_VERSION="${GHQ_VERSION:-latest}"
GWQ_VERSION="${GWQ_VERSION:-latest}"
PORTMAN_VERSION="${PORTMAN_VERSION:-latest}"
NODE_MAJOR="${NODE_MAJOR:-20}"

DOMAIN="${DOMAIN:-}"
CLOUDFLARE_API_TOKEN="${CLOUDFLARE_API_TOKEN:-}"
ACME_EMAIL="${ACME_EMAIL:-}"
BASIC_AUTH_USER="${BASIC_AUTH_USER:-}"
BASIC_AUTH_PASSWORD="${BASIC_AUTH_PASSWORD:-}"

PALMUX_REPO="tjst-t/palmux2"
GHQ_REPO="x-motemen/ghq"
GWQ_REPO="d-kuro/gwq"
PORTMAN_REPO="tjst-t/port-manager"

INSTALL_CADDY=0
if [ -n "$DOMAIN" ] && [ -n "$CLOUDFLARE_API_TOKEN" ]; then
  INSTALL_CADDY=1
elif [ -n "$DOMAIN" ] || [ -n "$CLOUDFLARE_API_TOKEN" ]; then
  printf '\033[1;31merror:\033[0m DOMAIN and CLOUDFLARE_API_TOKEN must be set together\n' >&2
  exit 1
fi

if { [ -n "$BASIC_AUTH_USER" ] && [ -z "$BASIC_AUTH_PASSWORD" ]; } || \
   { [ -z "$BASIC_AUTH_USER" ] && [ -n "$BASIC_AUTH_PASSWORD" ]; }; then
  printf '\033[1;31merror:\033[0m BASIC_AUTH_USER and BASIC_AUTH_PASSWORD must be set together\n' >&2
  exit 1
fi
if [ -n "$BASIC_AUTH_USER" ] && [ "$INSTALL_CADDY" != "1" ]; then
  printf '\033[1;31merror:\033[0m BASIC_AUTH_* requires DOMAIN + CLOUDFLARE_API_TOKEN (Caddy)\n' >&2
  exit 1
fi

PREFIX="/usr/local"
BIN_DIR="${PREFIX}/bin"
UNIT_DIR="${PREFIX}/lib/systemd/user"

log()  { printf '\033[1;34m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33m==>\033[0m %s\n' "$*" >&2; }
die()  { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

# --- preflight ---------------------------------------------------------------

[ "$(id -u)" -ne 0 ] || die "run as a regular user; the script invokes sudo where needed"
command -v sudo >/dev/null 2>&1 || die "sudo is required"

[ "$(uname -s)" = "Linux" ] || die "unsupported OS: $(uname -s) (Linux only)"

case "$(uname -m)" in
  x86_64|amd64)  PALMUX_ARCH=amd64; GHQ_ARCH=amd64; GWQ_ARCH=x86_64 ;;
  aarch64|arm64) PALMUX_ARCH=arm64; GHQ_ARCH=arm64; GWQ_ARCH=arm64  ;;
  *) die "unsupported arch: $(uname -m)" ;;
esac

if [ -r /etc/os-release ]; then
  # shellcheck disable=SC1091
  . /etc/os-release
  case "${ID:-}-${ID_LIKE:-}" in
    ubuntu-*|debian-*|*-*ubuntu*|*-*debian*) : ;;
    *) warn "non-Debian/Ubuntu distro detected (${ID:-unknown}); apt steps will likely fail" ;;
  esac
fi

# --- apt packages ------------------------------------------------------------

log "installing base apt packages"
sudo apt-get update -qq
sudo apt-get install -y -qq tmux git curl ca-certificates gnupg unzip jq

# --- Node.js + Claude Code CLI ----------------------------------------------

if [ "${SKIP_NODE:-0}" != "1" ]; then
  current_node_major=""
  if command -v node >/dev/null 2>&1; then
    current_node_major=$(node -p 'process.versions.node.split(".")[0]' 2>/dev/null || true)
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

# --- helpers -----------------------------------------------------------------

resolve_tag() {
  local repo="$1" tag="$2"
  if [ "$tag" = "latest" ]; then
    curl -fsSL "https://api.github.com/repos/${repo}/releases/latest" | jq -r .tag_name
  else
    printf '%s\n' "$tag"
  fi
}

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

# --- palmux ------------------------------------------------------------------

PALMUX_TAG=$(resolve_tag "$PALMUX_REPO" "$PALMUX_VERSION")
[ -n "$PALMUX_TAG" ] && [ "$PALMUX_TAG" != "null" ] || die "could not resolve palmux release tag"

installed_palmux=""
command -v palmux >/dev/null 2>&1 && installed_palmux=$(palmux --version 2>/dev/null || true)

if [ "$installed_palmux" = "$PALMUX_TAG" ]; then
  log "palmux ${PALMUX_TAG} already installed"
else
  log "installing palmux ${PALMUX_TAG}"
  curl -fsSL -o "${WORKDIR}/palmux" \
    "https://github.com/${PALMUX_REPO}/releases/download/${PALMUX_TAG}/palmux-linux-${PALMUX_ARCH}"
  chmod +x "${WORKDIR}/palmux"
  sudo install -m 0755 "${WORKDIR}/palmux" "${BIN_DIR}/palmux"
fi

# --- ghq ---------------------------------------------------------------------

GHQ_TAG=$(resolve_tag "$GHQ_REPO" "$GHQ_VERSION")
[ -n "$GHQ_TAG" ] && [ "$GHQ_TAG" != "null" ] || die "could not resolve ghq release tag"

installed_ghq=""
if command -v ghq >/dev/null 2>&1; then
  installed_ghq=$(ghq --version 2>/dev/null | awk '{print $3}' || true)
fi
ghq_want="${GHQ_TAG#v}"
if [ "$installed_ghq" = "$ghq_want" ]; then
  log "ghq ${GHQ_TAG} already installed"
else
  log "installing ghq ${GHQ_TAG}"
  curl -fsSL -o "${WORKDIR}/ghq.zip" \
    "https://github.com/${GHQ_REPO}/releases/download/${GHQ_TAG}/ghq_linux_${GHQ_ARCH}.zip"
  unzip -q -o "${WORKDIR}/ghq.zip" -d "${WORKDIR}/ghq"
  sudo install -m 0755 "${WORKDIR}/ghq/ghq_linux_${GHQ_ARCH}/ghq" "${BIN_DIR}/ghq"
fi

# --- gwq ---------------------------------------------------------------------

GWQ_TAG=$(resolve_tag "$GWQ_REPO" "$GWQ_VERSION")
[ -n "$GWQ_TAG" ] && [ "$GWQ_TAG" != "null" ] || die "could not resolve gwq release tag"

installed_gwq=""
if command -v gwq >/dev/null 2>&1; then
  installed_gwq=$(gwq version 2>/dev/null | awk 'NR==1 {print $3}' || true)
fi
if [ "$installed_gwq" = "$GWQ_TAG" ] || [ "v${installed_gwq}" = "$GWQ_TAG" ]; then
  log "gwq ${GWQ_TAG} already installed"
else
  log "installing gwq ${GWQ_TAG}"
  curl -fsSL -o "${WORKDIR}/gwq.tar.gz" \
    "https://github.com/${GWQ_REPO}/releases/download/${GWQ_TAG}/gwq_Linux_${GWQ_ARCH}.tar.gz"
  tar -xzf "${WORKDIR}/gwq.tar.gz" -C "${WORKDIR}"
  sudo install -m 0755 "${WORKDIR}/gwq" "${BIN_DIR}/gwq"
fi

# --- port-manager ------------------------------------------------------------

if [ "${SKIP_PORTMAN:-0}" != "1" ]; then
  PORTMAN_TAG=$(resolve_tag "$PORTMAN_REPO" "$PORTMAN_VERSION")
  [ -n "$PORTMAN_TAG" ] && [ "$PORTMAN_TAG" != "null" ] || die "could not resolve port-manager release tag"

  installed_portman=""
  command -v portman >/dev/null 2>&1 && installed_portman=$(portman --version 2>/dev/null | awk '{print $NF}' || true)
  portman_want="${PORTMAN_TAG#v}"

  if [ "$installed_portman" = "$portman_want" ] || [ "$installed_portman" = "$PORTMAN_TAG" ]; then
    log "port-manager ${PORTMAN_TAG} already installed"
  else
    log "installing port-manager ${PORTMAN_TAG}"
    curl -fsSL -o "${WORKDIR}/portman" \
      "https://github.com/${PORTMAN_REPO}/releases/download/${PORTMAN_TAG}/port-manager_${portman_want}_linux_${PALMUX_ARCH}"
    chmod +x "${WORKDIR}/portman"
    sudo install -m 0755 "${WORKDIR}/portman" "${BIN_DIR}/portman"
  fi
fi

# --- Caddy + Cloudflare DNS-01 (opt-in) --------------------------------------

if [ "$INSTALL_CADDY" = "1" ]; then
  log "installing Caddy with caddy-dns/cloudflare plugin (custom build)"
  curl -fsSL -o "${WORKDIR}/caddy" \
    "https://caddyserver.com/api/download?os=linux&arch=${PALMUX_ARCH}&p=github.com%2Fcaddy-dns%2Fcloudflare"
  chmod +x "${WORKDIR}/caddy"
  sudo install -m 0755 "${WORKDIR}/caddy" "${BIN_DIR}/caddy"
  # let Caddy bind 80/443 without running as root
  sudo setcap 'cap_net_bind_service=+ep' "${BIN_DIR}/caddy"

  if ! id -u caddy >/dev/null 2>&1; then
    sudo useradd --system --home /var/lib/caddy --create-home \
      --user-group --shell /usr/sbin/nologin caddy
  fi
  sudo install -d -m 0750 -o caddy -g caddy /etc/caddy
  sudo install -d -m 0750 -o caddy -g caddy /var/lib/caddy

  BASIC_AUTH_HASH=""
  if [ -n "$BASIC_AUTH_USER" ]; then
    log "hashing basic-auth password (bcrypt via caddy hash-password)"
    BASIC_AUTH_HASH=$(/usr/local/bin/caddy hash-password --plaintext "$BASIC_AUTH_PASSWORD")
  fi

  log "writing /etc/caddy/palmux.env (root:caddy, 0640)"
  {
    printf 'CLOUDFLARE_API_TOKEN=%s\n' "$CLOUDFLARE_API_TOKEN"
    if [ -n "$BASIC_AUTH_USER" ]; then
      printf 'BASIC_AUTH_USER=%s\n' "$BASIC_AUTH_USER"
      printf 'BASIC_AUTH_HASH=%s\n' "$BASIC_AUTH_HASH"
    fi
  } | sudo tee /etc/caddy/palmux.env >/dev/null
  sudo chown root:caddy /etc/caddy/palmux.env
  sudo chmod 0640 /etc/caddy/palmux.env
  # remove the legacy filename if an older install left it behind
  sudo rm -f /etc/caddy/cloudflare.env

  log "writing /etc/caddy/Caddyfile (domain=${DOMAIN}, basic_auth=$( [ -n "$BASIC_AUTH_USER" ] && echo yes || echo no ))"
  {
    echo "{"
    if [ -n "$ACME_EMAIL" ]; then
      echo "    email ${ACME_EMAIL}"
    fi
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

  log "installing /etc/systemd/system/caddy.service"
  sudo tee /etc/systemd/system/caddy.service >/dev/null <<'EOF'
[Unit]
Description=Caddy
Documentation=https://caddyserver.com/docs/
After=network.target network-online.target
Requires=network-online.target

[Service]
Type=notify
User=caddy
Group=caddy
EnvironmentFile=/etc/caddy/palmux.env
ExecStart=/usr/local/bin/caddy run --environ --config /etc/caddy/Caddyfile
ExecReload=/usr/local/bin/caddy reload --config /etc/caddy/Caddyfile --force
TimeoutStopSec=5s
LimitNOFILE=1048576
PrivateTmp=true
ProtectSystem=full
AmbientCapabilities=CAP_NET_BIND_SERVICE

[Install]
WantedBy=multi-user.target
EOF

  sudo systemctl daemon-reload
  sudo systemctl enable caddy
  if sudo systemctl is-active --quiet caddy; then
    log "reloading Caddy (already running)"
    sudo systemctl reload caddy || sudo systemctl restart caddy
  else
    log "starting Caddy"
    sudo systemctl start caddy
  fi
fi

# --- systemd user unit for palmux --------------------------------------------

if [ "${SKIP_SERVICE:-0}" != "1" ]; then
  if [ "$INSTALL_CADDY" = "1" ]; then
    PALMUX_BIND="127.0.0.1:8080"
  else
    PALMUX_BIND="0.0.0.0:8080"
  fi

  log "installing systemd user unit at ${UNIT_DIR}/palmux.service (bind=${PALMUX_BIND})"
  sudo install -d -m 0755 "$UNIT_DIR"
  sudo tee "${UNIT_DIR}/palmux.service" >/dev/null <<EOF
[Unit]
Description=Palmux web terminal
After=default.target

[Service]
Type=simple
ExecStart=/usr/local/bin/palmux --addr=${PALMUX_BIND}
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
EOF
  systemctl --user daemon-reload 2>/dev/null || \
    warn "systemctl --user daemon-reload skipped (no user session)"
fi

# --- summary -----------------------------------------------------------------

if [ "$INSTALL_CADDY" = "1" ]; then
  ACCESS_URL="https://${DOMAIN}"
else
  ACCESS_URL="http://<host>:8080"
fi

cat <<EOM

==> Installed:
    palmux  $(palmux --version 2>/dev/null || echo "?")
    ghq     $(ghq --version 2>/dev/null | awk '{print $3}' || echo "?")
    gwq     $(gwq version 2>/dev/null | awk 'NR==1 {print $3}' || echo "?")
    portman $(portman --version 2>/dev/null | awk '{print $NF}' || echo "(skipped)")
    node    $(node --version 2>/dev/null || echo "(skipped)")
    claude  $(claude --version 2>/dev/null | head -1 || echo "(skipped)")
    caddy   $(if [ "$INSTALL_CADDY" = "1" ]; then caddy version 2>/dev/null | awk '{print $1}'; else echo "(not enabled)"; fi)

==> Next steps:
    1. Enable + start palmux:
         systemctl --user enable --now palmux
    2. Keep it running after logout:
         sudo loginctl enable-linger \$USER
    3. Open  ${ACCESS_URL}
EOM

if [ "$INSTALL_CADDY" = "1" ]; then
  if [ -n "$BASIC_AUTH_USER" ]; then
    AUTH_NOTE="basic_auth enabled (user=${BASIC_AUTH_USER})"
  else
    AUTH_NOTE="no basic_auth (pass BASIC_AUTH_USER + BASIC_AUTH_PASSWORD to enable)"
  fi
  cat <<EOM

==> Caddy is serving ${DOMAIN} -> 127.0.0.1:8080 with Let's Encrypt
    (DNS-01 challenge via Cloudflare). Certificates renew automatically.
    Auth at edge: ${AUTH_NOTE}
    To rotate secrets (CF token / basic auth password): re-run this installer
      with the new env vars; Caddy will reload automatically.
    Caddy logs:  journalctl -u caddy -f
EOM
else
  cat <<EOM

    For production, set an auth token:
         systemctl --user edit palmux
       and add:
         [Service]
         ExecStart=
         ExecStart=/usr/local/bin/palmux --addr=0.0.0.0:8080 --token=<your-secret>

    To enable HTTPS later, re-run with DOMAIN=... CLOUDFLARE_API_TOKEN=...
EOM
fi

cat <<EOM

==> To update later, re-run the same one-liner.
EOM
