# Palmux2 Installation Guide

## Requirements

- Linux (x86_64 or arm64)
- Go 1.25+
- tmux
- ghq
- gwq (for worktree management)
- git

### Optional — Network Isolation (S034)

Per-worktree network namespace isolation requires:

- `slirp4netns` 1.2.0+ (for outbound connectivity inside isolated worktrees)
- Unprivileged user namespaces enabled (see below)

## Quick Start

```bash
make build
./palmux --config-dir ~/.config/palmux --port 8215
```

## Network Namespace Isolation (Ubuntu 24.04+)

Palmux2 uses Linux network namespaces to isolate per-worktree `localhost`,
preventing port conflicts between concurrent dev servers. This feature requires
**unprivileged user namespaces**, which are restricted by AppArmor on Ubuntu 24.04+.

### Check if restriction is active

```bash
sysctl kernel.apparmor_restrict_unprivileged_userns
# Returns 1 if restricted, 0 if unrestricted
```

### Option 1: Disable globally (simplest)

```bash
sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0
```

To persist across reboots:

```bash
echo 'kernel.apparmor_restrict_unprivileged_userns=0' | \
  sudo tee /etc/sysctl.d/99-palmux-userns.conf
sudo sysctl -p /etc/sysctl.d/99-palmux-userns.conf
```

### Option 2: AppArmor profile for palmux only (more secure)

Create an AppArmor profile that allows palmux to create user namespaces without
affecting other programs:

```bash
sudo tee /etc/apparmor.d/usr.local.bin.palmux <<'EOF'
abi <abi/4.0>,

profile palmux /usr/local/bin/palmux {
  include <abstractions/base>
  userns,
}
EOF

sudo apparmor_parser -r /etc/apparmor.d/usr.local.bin.palmux
```

Adjust the path in the profile to match where `palmux` is installed.

### Verify

After applying one of the options above, restart palmux. If isolation is working
you will see:

```
INFO netns: slirp4netns started ...
```

in the server logs when opening an isolated worktree.

### Graceful degradation

If neither `slirp4netns` nor unprivileged user namespaces are available, palmux
falls back to host networking automatically. Existing `repos.json` settings are
preserved — the runtime simply ignores the isolation flag. You will see:

```
WARN slirp4netns not found in PATH — network isolation disabled
```
or
```
WARN netns: unprivileged userns smoke test failed — network isolation disabled
  hint: run 'sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0' or see docs/INSTALL.md
```

### Installing slirp4netns

```bash
# Debian/Ubuntu
sudo apt install slirp4netns

# Or download from GitHub releases
curl -L https://github.com/rootless-containers/slirp4netns/releases/download/v1.2.1/slirp4netns-x86_64 \
  -o /usr/local/bin/slirp4netns
chmod +x /usr/local/bin/slirp4netns
```

## Caddy Reverse-Proxy Integration (Optional)

When Caddy is installed and enabled in palmux settings, exposed ports receive
stable FQDN + TLS certificates automatically.

### Install Caddy

```bash
# Debian/Ubuntu
sudo apt install -y debian-keyring debian-archive-keyring apt-transport-https curl
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/gpg.key' \
  | sudo gpg --dearmor -o /usr/share/keyrings/caddy-stable-archive-keyring.gpg
curl -1sLf 'https://dl.cloudsmith.io/public/caddy/stable/debian.deb.txt' \
  | sudo tee /etc/apt/sources.list.d/caddy-stable.list
sudo apt update && sudo apt install caddy
```

### Configure in Palmux

1. Open palmux Settings → Network (`/settings/network`)
2. Enable **Caddy integration**
3. Set the **FQDN template** e.g. `{{.branch}}-{{.port}}.dev.example.com`
4. Set the **Snippet file path** (must be `import`ed from your main Caddyfile)
5. Set the **Reload command** e.g. `caddy reload --config ~/.config/caddy/Caddyfile`
6. Save

Your main Caddyfile must include:

```
import /path/to/palmux/snippet.caddyfile
```
