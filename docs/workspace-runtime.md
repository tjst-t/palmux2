# Workspace runtime — user guide

Palmux can run each Workspace inside a different *runtime*. The default
runtime depends on what is installed on the host:

| Runtime kind | When it's selected | Isolation |
| --- | --- | --- |
| `host` | LXD is not installed, or the user explicitly requests it | None — processes run on the host |
| `lxd-container` | LXD is installed and `palmux-workspace:default` is reachable | Process / fs / package isolation in an unprivileged LXD container |

> Long-form design rationale: [`docs/workspace-runtime-design.md`](workspace-runtime-design.md). This page is the
> *user-facing* reference — what to put in config and which images
> exist.

## Supported OS

**Ubuntu only** (`linux/amd64`, `linux/arm64`). macOS / Windows / WSL
are not supported. See the design doc §14.7 for why.

## Container images

palmux publishes container images to GHCR. They are public — you don't
need a GitHub login to `lxc launch` them.

| Image | Purpose | Status |
| --- | --- | --- |
| `ghcr.io/tjst-t/palmux-workspace:default` | Floating "latest stable" tag. **This is the default image**; palmux uses it whenever `runtime.image` is unset and `runtime.kind = "lxd-container"`. | Active. Rebuilt weekly + on every `v*` tag of `palmux2`. |
| `ghcr.io/tjst-t/palmux-workspace:default-YYYYMMDD` | Date-stamped tag. Use this if you want to pin the exact image you tested against (so a Monday-morning rebuild can't shift behaviour under you). | Each weekly + tag-push build emits a fresh date-stamped tag. Old date tags are kept indefinitely (cheap on GHCR). |
| `ghcr.io/tjst-t/palmux-workspace:gpu` | (Future) CUDA / NVIDIA driver bundle. | **Not built yet** — deferred until `:default` is stable in production. |
| `ghcr.io/tjst-t/palmux-workspace:minimal` | (Future) base ubuntu + `palmux-agent` deps only, no claude / gh / ansible. | **Not built yet.** |

### What's pre-installed in `:default`

- `claude` (Anthropic Claude Code CLI)
- `gh` (GitHub CLI)
- `git`, `tmux`
- `tailscale`
- `ansible`, `ansible-lint`
- `python3` + `pip` + `venv`, `make`, `build-essential`
- standard ops tools: `curl`, `gnupg`, `jq`, `rsync`, `openssh-client`
- pre-created `ubuntu` user (UID 1000, passwordless sudo) so palmux's
  `raw.idmap "both 1000 1000"` maps cleanly out of the box
- systemd as PID 1 (so `lxc launch docker:...` boots a real init)

### What's NOT pre-installed

- **`palmux-agent`** — palmux pushes the matching agent binary at
  container start. This guarantees the agent always matches the palmux
  version in use, regardless of how old your image is. (See design doc
  §14.10.6.)
- Language toolchains (Node, Go, Rust, Java) — install per-Workspace
  via mise / asdf / your usual bootstrap script. Pre-baking N
  toolchains at M versions blows past the 1 GB image-size budget.

### Image size

Target: **≤ 1 GB**. The CI workflow fails the build if the published
image exceeds this.

## Overriding the image

Per-Workspace override lives in `repos.json`:

```jsonc
{
  "repos": [
    {
      "id": "tjst-t--palmux2--a1b2",
      "branches": [
        {
          "id": "feature--x--7a8b",
          "runtime": {
            "kind": "lxd-container",
            // Anything LXD's `docker:` protocol can pull. Examples:
            "image": "ghcr.io/tjst-t/palmux-workspace:default-20260520",
            // ...or back to plain ubuntu:
            // "image": "ubuntu:24.04",
            // ...or your own image:
            // "image": "ghcr.io/your-org/your-workspace:latest"
            "network": { "mode": "bridged" }
          }
        }
      ]
    }
  ]
}
```

Global default lives in `~/.config/palmux/settings.json`:

```jsonc
{
  "defaultRuntime": {
    "kind": "lxd-container",
    "image": "ghcr.io/tjst-t/palmux-workspace:default"
  }
}
```

### Override requirements

palmux pushes `palmux-agent` (a static Go binary, ≤ 15 MB) into the
container at start time and runs it under systemd. For a custom image
to work end-to-end it needs:

- **systemd** as PID 1 (so palmux can drop a unit file and `systemctl
  start palmux-agent`)
- **glibc** + **coreutils** + **`/bin/sh`** (the agent is a static
  binary so this is mostly satisfied automatically by anything based
  on `ubuntu:*`, `debian:*`, or similar)
- a user with **UID 1000** (palmux pins `raw.idmap "both 1000 1000"`;
  if your image lacks UID 1000 the bind-mounts will be owned by
  `nobody:nogroup` from the container's perspective)

`ubuntu:24.04` straight from Docker Hub satisfies all three.
`alpine:*` does **not** — Alpine uses musl, not glibc.

## Bypassing the runtime entirely

To run a Workspace directly on the host (legacy behaviour, no LXD
involvement):

```jsonc
{
  "runtime": { "kind": "host" }
}
```

This is also the automatic fallback when LXD isn't installed. The
runtime selector in the Workspace settings UI shows `lxd-container` as
greyed out with a tooltip pointing at the LXD install guide; selecting
nothing keeps `host`.

## How palmux launches the image (under the hood)

1. palmux runs `lxc launch docker:ghcr.io/tjst-t/palmux-workspace:default <inst>`.
2. palmux applies `raw.idmap "both 1000 1000"` so the host UID 1000 user
   maps 1:1 into the container.
3. palmux bind-mounts the worktree path and `~/.claude/` (read-write)
   so the in-container `claude` sees the same auth, skills, and memory
   as the host.
4. palmux `lxc file push`es the matching `palmux-agent` binary to
   `/usr/local/bin/palmux-agent` and starts it under systemd. Because
   the binary is pushed each time, agent version and palmux version
   always match.
5. palmux opens an RPC channel over a Unix domain socket and uses the
   agent for `ListListeningPorts`, file ops, and process tracking.
6. Port forwards declared via the `expose_port` MCP tool become
   `lxc config device add <inst> <name> proxy ...` rules.

When the Workspace closes, palmux does `lxc delete --force <inst>`. The
container, including any state inside it, is destroyed. Long-lived
containers are explicitly out of scope (design doc §14.2).

## Maintenance

- Image rebuilds happen automatically (weekly cron) — see
  `.github/workflows/build-image.yml`. You don't need to do anything
  to pick up Ubuntu security updates.
- If you maintain a fork or a custom image, copy
  `images/workspace-default/Dockerfile` as a starting point. The
  `LABEL org.opencontainers.image.*` block makes the image traceable
  back to its source.
- The `:default` floating tag may shift between any two `lxc launch`
  invocations. If you need reproducibility, pin to a `:default-YYYYMMDD`
  tag.

## See also

- [`docs/workspace-runtime-design.md`](workspace-runtime-design.md) — full design doc and decision log
- [`images/workspace-default/Dockerfile`](../images/workspace-default/Dockerfile) — what's actually built
- [`images/workspace-default/README.md`](../images/workspace-default/README.md) — local build / push instructions
- [`.github/workflows/build-image.yml`](../.github/workflows/build-image.yml) — CI workflow
