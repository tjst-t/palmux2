# palmux-workspace:default

Base container image for the palmux Workspace runtime. Published as
`ghcr.io/tjst-t/palmux-workspace:default` (floating tag) and
`ghcr.io/tjst-t/palmux-workspace:default-YYYYMMDD` (date-stamped) by
`.github/workflows/build-image.yml`.

Maintenance contract: weekly cron rebuild + tag-push rebuild.
Target size: ≤ 1 GB.

## What's in here

- `ubuntu:24.04` base
- systemd as PID 1 (so LXD `lxc launch docker:...` boots a real init)
- `claude` (Anthropic Claude Code CLI, installed via `claude.ai/install.sh`)
- `gh` (GitHub CLI, official apt repo)
- `git`, `tmux`, `ansible`, `ansible-lint`
- `tailscale` (official apt repo)
- `python3` + `python3-pip` + `python3-venv` (ansible dep, also useful for scripts)
- `make`, `build-essential`
- `curl`, `gnupg`, `jq`, `rsync`, `openssh-client`, `vim-tiny`, `nano`
- pre-created `ubuntu` user (UID 1000, passwordless sudo) so palmux's
  `raw.idmap "both 1000 1000"` maps cleanly out of the box

## What's NOT in here

- `palmux-agent` — pushed at runtime by palmux (§14.10.6); baking it in
  would create palmux-version / image-version skew
- language toolchains (Node, Go, Rust, Java) — installed per Workspace
  via `mise` / `asdf` / Workspace bootstrap scripts. Pre-baking N
  toolchains at M versions blows past the 1 GB target
- GPU drivers — see `:gpu` variant (deferred until `:default` is stable)

## Build locally (docker, on the test VM)

```bash
# On the test VM (ubuntu@192.168.1.41), or any host with docker:
docker build -t palmux-workspace:local images/workspace-default
docker images palmux-workspace:local --format '{{.Size}}'   # sanity check ≤ 1 GB
```

## Push to GHCR (one-off, manual)

The CI workflow does this automatically on cron + tag push, but for
ad-hoc verification:

```bash
gh auth token | docker login ghcr.io -u tjst-t --password-stdin
docker tag palmux-workspace:local ghcr.io/tjst-t/palmux-workspace:default-test
docker push ghcr.io/tjst-t/palmux-workspace:default-test
```

## Smoke test via LXD (test VM)

```bash
lxc launch docker:ghcr.io/tjst-t/palmux-workspace:default smoke-1
sleep 5
lxc exec smoke-1 -- bash -c 'for c in claude gh git tmux tailscale ansible; do which $c; done'
lxc delete -f smoke-1
```

## Override the image (per Workspace)

In `repos.json`, set `runtime.image` on a Workspace to point at any
LXD-launchable OCI image:

```json
{
  "runtime": {
    "kind": "lxd-container",
    "image": "ubuntu:24.04"            // back to plain ubuntu
  }
}
```

Or any other `ghcr.io/...` image. palmux still pushes `palmux-agent` at
runtime, so as long as the override has `systemd` + libc + coreutils,
it works.

## Bypass entirely

Set `runtime.kind = "host"` to run on the host (legacy behaviour, no
LXD involvement).

See `docs/workspace-runtime.md` for the full user-facing override /
bypass instructions.
