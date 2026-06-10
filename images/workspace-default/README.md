# palmux-ws — Workspace Default Base Image

The `palmux-ws` Incus container image is the default base for palmux
`incus-container` workspaces. It is intentionally **minimal and claude-free**.

## What is in the image

| Package      | Purpose |
|---|---|
| `ubuntu 24.04 (noble)` | Base OS — `ubuntu` user at UID 1000 (cloud image default) |
| `tmux`       | Terminal multiplexer used by all workspace tabs |
| `git`        | VCS operations inside the container |
| `curl`       | Downloading resources (mise, npm registries, etc.) |
| `ca-certificates` | TLS verification for outbound HTTPS |
| `python3`    | Used by palmux's in-container localhost relay (`ExposePort`) |

## What is NOT in the image

**`claude` is NOT baked into the image.** It is bind-mounted from the host at
Workspace start time by palmux's incus runtime
(`internal/runtime/incus/incus.go`, the `Start()` bind-mount block).

Specifically, palmux bind-mounts (when present on the host):

| Host path | Container path | Purpose |
|---|---|---|
| `~/.local/share/claude` | same | Versioned native claude ELF binaries |
| `~/.local/bin` | same | `claude` symlink → `versions/<v>` |
| `~/.claude` | same | OAuth credentials, skills, memory |
| `~/.claude.json` | same | Onboarding state |
| `~/ghq` | same | All repositories (cross-repo references) |

This means the in-container `~/.local/bin/claude` resolves to the host's
current version. No re-download, no re-authentication needed when claude
updates on the host.

`/usr/local/bin/claude` is intentionally **not** mounted. It is a
host-specific bash cgroup wrapper (`systemd-run` based) that is not portable
into the container.

## Host prerequisites before using palmux-ws

1. **subuid/subgid** — palmux uses `raw.idmap "both 1000 1000"` so the
   bind-mounted files are owned by the `ubuntu` user inside the container.
   Add this line to **both** `/etc/subuid` and `/etc/subgid`:
   ```
   root:1000:1
   ```
   Then restart incus: `sudo systemctl restart incus`

2. **Docker iptables conflict** — If Docker is installed on the host, its
   `FORWARD DROP` policy will block the incus managed bridge (`incusbr0`) from
   reaching the internet. Fix with:
   ```
   sudo iptables -I DOCKER-USER -i incusbr0 -j ACCEPT
   ```
   Or stop Docker when not needed: `sudo systemctl stop docker`

See `docs/workspace-runtime-design.md §4` for full details.

## Building the image manually

```bash
# From the repo root:
./images/workspace-default/build.sh

# Custom output directory:
OUT_DIR=/tmp/palmux-images ./images/workspace-default/build.sh

# Custom base image (e.g. for arm64):
IMAGE_BASE=images:ubuntu/24.04/arm64 OUT_DIR=./dist ./images/workspace-default/build.sh
```

The script:
1. Launches a temporary `images:ubuntu/24.04` build instance
2. Runs `apt-get install -y tmux git curl ca-certificates python3`
3. Verifies `tmux`, `git`, `python3` are present and `claude` is absent
4. Stops the instance, publishes it, exports a unified Incus tarball to
   `<OUT_DIR>/palmux-ws.tar.gz`
5. Cleans up the build instance and temp alias

## Importing a released tarball

Released tarballs are attached to GitHub Releases as `palmux-ws-<date>.tar.gz`.
The easiest way to import is:

```bash
palmux runtime install
```

Or manually:

```bash
# Download from GitHub Releases:
curl -L -o /tmp/palmux-ws.tar.gz \
  https://github.com/tjst-t/palmux2/releases/latest/download/palmux-ws.tar.gz

# Import:
incus image alias delete palmux-ws </dev/null 2>/dev/null || true
incus image import /tmp/palmux-ws.tar.gz --alias palmux-ws </dev/null
```

## Verifying the image contents

After importing, launch a test instance and check:

```bash
# Launch
incus launch palmux-ws test-check </dev/null

# tmux and git must be present
incus exec test-check </dev/null -- which tmux
incus exec test-check </dev/null -- which git

# claude must NOT be present in the image
incus exec test-check </dev/null -- sh -c 'command -v claude && echo FAIL || echo OK'

# Clean up
incus delete --force test-check </dev/null
```

## CI / Release

The image is rebuilt automatically by
`.github/workflows/build-workspace-image.yml` on:
- `workflow_dispatch` (manual)
- `push` to tags matching `v*`
- Weekly schedule (Monday 02:00 UTC)

The tarball is uploaded as a GitHub Release asset (named
`palmux-ws-<date>.tar.gz` plus a stable `palmux-ws.tar.gz`).
