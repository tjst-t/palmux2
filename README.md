# Palmux v2

Web-based terminal client built around tmux. Run multiple Claude Code agents
in parallel, browse files, and review git diffs from one browser tab — desktop
or mobile.

Palmux is a single Go binary with the frontend embedded. tmux is treated as an
implementation detail: every browser session attaches to a tmux session named
`_palmux_{repoId}_{branchId}`, and each tab maps to a tmux window
(`palmux:…`).

- **Backend**: Go 1.25, `net/http`, `nhooyr.io/websocket`
- **Frontend**: React 19, TypeScript, Vite, React Router 7, xterm.js, embedded via `embed.FS`
- **Distribution**: a single static binary per architecture

---

## Table of contents

- [Required dependencies](#required-dependencies)
- [Optional dependencies](#optional-dependencies)
- [Install](#install)
- [Run](#run)
- [Authentication and `--token`](#authentication-and---token)
- [Notification hooks](#notification-hooks)
- [Configuration files](#configuration-files)
- [tmux configuration](#tmux-configuration)
- [URL scheme](#url-scheme)
- [Updating](#updating)
- [Running as a service (systemd)](#running-as-a-service-systemd)
- [Reverse proxy](#reverse-proxy)
- [Development](#development)
- [Releases](#releases)
- [License](#license)

---

## Required dependencies

These must be present on `PATH` before Palmux starts; the binary refuses to
launch otherwise.

| Tool | Purpose |
| --- | --- |
| [`tmux`](https://github.com/tmux/tmux) ≥ 3.2 | Owns the actual terminal sessions |
| [`git`](https://git-scm.com/) | Worktree listing, diffs, branch metadata |
| [`ghq`](https://github.com/x-motemen/ghq) | Source-of-truth for what repositories you've Open’d |
| [`gwq`](https://github.com/d-kuro/gwq) | Worktree create/remove (`git worktree add` is never invoked directly) |

## Optional dependencies

| Tool | When you need it |
| --- | --- |
| [`claude`](https://docs.anthropic.com/en/docs/claude-code) (Anthropic Claude Code CLI) | The Claude tab spawns this binary for stream-json IPC. Authenticate it once with `claude auth login` |
| Node.js 22+ and npm | Only for development (`make dev`) or building from source (`make build`). Pre-built release binaries already include the bundled frontend |
| [`portman`](https://github.com/tjst-t/port-manager) | Only when you use the bundled `make serve`/`make dev` recipes — they lease ports through portman and (optionally) register with Caddy |
| [Caddy](https://caddyserver.com/) | Only when you want HTTPS + a friendly hostname; Palmux itself is plain HTTP and doesn't terminate TLS |

---

## Install

### From a release (recommended)

Pre-built Linux binaries are attached to every tag — `linux/amd64` and
`linux/arm64`. They embed the frontend, so the only runtime requirements are
the tools listed in [Required dependencies](#required-dependencies).

```bash
# Pick the latest release: https://github.com/tjst-t/palmux2/releases
ARCH=amd64   # or arm64
VER=v0.1.0
curl -fSL -o ~/.local/bin/palmux \
  "https://github.com/tjst-t/palmux2/releases/download/${VER}/palmux-linux-${ARCH}"
chmod +x ~/.local/bin/palmux
palmux --help
```

### From source

```bash
git clone https://github.com/tjst-t/palmux2 ~/ghq/github.com/tjst-t/palmux2
cd ~/ghq/github.com/tjst-t/palmux2
make build              # ./bin/palmux (host arch)
# or:
make build-linux        # ./bin/palmux-linux-amd64
make build-arm          # ./bin/palmux-linux-arm64
```

---

## Run

```bash
palmux                                # listens on 0.0.0.0:8080
palmux --addr 127.0.0.1:8088          # bind to a specific port
palmux --token <secret>               # require auth
palmux --config-dir ~/.config/palmux  # override the config directory
```

Open the printed URL in your browser. The first time you Open a repository
through the Drawer, it must already be cloned under `ghq root` — Palmux does
not clone things on your behalf, it only manages worktrees of repositories
you've already cloned.

### CLI flags

| Flag | Default | Notes |
| --- | --- | --- |
| `--addr` | `0.0.0.0:8080` | Listen address |
| `--config-dir` | `~/.config/palmux` | Holds `repos.json`, `settings.json`, `sessions.json` |
| `--token` | (empty) | Required token for browser + hook auth (see below) |
| `--base-path` | `/` | Mount under a sub-path, e.g. `/palmux/` |
| `--max-connections` | `0` | Per-branch WS cap. `0` = unlimited |
| `--portman-url` | (empty) | If set, the header shows a link to your portman dashboard |

---

## Authentication and `--token`

Palmux has two auth modes:

- **Open access** (`--token` empty). Anyone with network access can use it.
  A signed cookie is still set so notifications work, but no token is required.
- **Token mode** (`--token <secret>` provided). Browsers must visit
  `/auth?token=<secret>` once; Palmux issues a HMAC-SHA256-signed cookie
  (`palmux_session`, HttpOnly, SameSite=Strict, 90-day TTL) and redirects to
  `/`. CLI calls and notification hooks pass the token in
  `Authorization: Bearer <token>`.

Run with `--token` whenever Palmux is reachable from the network — the cookie
is good for 90 days, and you can rotate the token by restarting the server.

---

## Notification hooks

On startup Palmux writes
`${configDir}/env.${port}` containing the variables your hooks need. Source
the file and `POST` events to `/api/notify`:

```bash
# ~/.config/palmux/env.8080
PALMUX_URL="http://127.0.0.1:8080"
PALMUX_TOKEN="..."          # only set when --token was passed
PALMUX_PORT="8080"
```

```bash
source ~/.config/palmux/env.8080
curl -fsS "$PALMUX_URL/api/notify" \
  -H "Authorization: Bearer $PALMUX_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"tmuxSession":"_palmux_repo_branch","type":"stop","message":"build done"}'
```

`tmuxSession` is decoded back into `(repoId, branchId)`. The Drawer pulses an
amber dot for the affected branch; the Claude tab shows an unread badge that
auto-clears when the user focuses that tab.

---

## Configuration files

All persistent state lives under `--config-dir` (default
`~/.config/palmux/`). They're plain JSON so you can edit by hand, but the
running server holds the source of truth — restart after manual edits.

| File | Purpose |
| --- | --- |
| `repos.json` | Open repositories. Branches are derived from `git worktree list` at runtime |
| `settings.json` | Globally shared settings (Toolbar, Claude defaults, image upload dir) |
| `sessions.json` | Claude tab persistence — last session_id per branch + per-branch model/effort/permission-mode prefs |
| `env.${port}` | Notification-hook env file (regenerated on each start) |

### Example `settings.json`

```jsonc
{
  "branchSortOrder": "name",
  "imageUploadDir": "/tmp/palmux-uploads",
  "toolbar": {
    "claude": {
      "rows": [
        [{ "type": "key", "key": "/clear", "text": "/clear" }]
      ]
    }
  }
}
```

Per-device settings (theme, font size, drawer width, split ratio, IME mode)
live in `localStorage` under the `palmux:` prefix and don't sync between
devices.

### Image upload directory

When you paste an image into the Claude composer, Palmux stores it under
`imageUploadDir` (default `/tmp/palmux-uploads/`). The directory is created on
first paste; clean it up however you like. `/api/upload/{name}` serves these
files back so chat thumbnails work after page reloads.

---

## tmux configuration

Palmux sends arrow-key sequences as `\x1b[A/B/C/D` and uses `\x7f` for
backspace, matching xterm. To make full mouse and 256-colour rendering work
inside the embedded xterm.js, ensure your `~/.tmux.conf` includes:

```tmux
set -g default-terminal "tmux-256color"
set -ag terminal-overrides ",*:Tc,*:RGB"
set -g mouse on
set -g focus-events on
set -g history-limit 50000
```

If you're on macOS without `tmux-256color`, use `screen-256color` instead.

The Palmux server names sessions `_palmux_{repoId}_{branchId}` and windows
`palmux:{type}:{name}` — that prefix is reserved, don't reuse it for your own
sessions.

---

## URL scheme

```
/                                                       # home (drawer with repo list)
/{repoId}/{branchId}/{tabId}                            # main panel
/{repoId}/{branchId}/files/<path>                       # file or directory inside the worktree
/{repoId}/{branchId}/git/{status|diff|log|branches}     # git tab views
/{repoId}/{branchId}/{tabId}?right=<encoded>            # split-panel right side
```

- `repoId` = `{owner}--{repo}--{hash4}`, e.g. `tjst-t--palmux2--2d59`
- `branchId` = `{branch_safe}--{hash4}`, e.g. `main--ad3a`
- `tabId` is `claude` / `files` / `git` / `bash` / `bash:my-server` / …

URLs survive bookmarks; clicking a link to a non-Open branch simply lands you
at the home screen.

---

## Updating

1. Download the new binary from
   <https://github.com/tjst-t/palmux2/releases>.
2. `make serve` if you're using the supplied recipe, or send the running
   process `SIGTERM` and restart it. Existing tmux sessions and Claude
   transcripts persist across restarts — only the server process is replaced.

The Files tab schema, sessions schema, and `settings.json` are forward-
compatible across patch versions; major versions document migrations in
release notes.

---

## Running as a service (systemd)

```ini
# /etc/systemd/system/palmux.service
[Unit]
Description=Palmux v2
After=network.target

[Service]
User=palmux
Environment=PATH=/usr/local/bin:/usr/bin:/bin
ExecStart=/home/palmux/.local/bin/palmux \
  --addr 127.0.0.1:8080 \
  --config-dir /home/palmux/.config/palmux \
  --token %i
Restart=on-failure
RestartSec=2s

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now palmux@SECRETHERE
journalctl -u 'palmux@*' -f
```

The `%i` instance string lets you keep the token out of the unit file; pick a
real secret per host.

---

## Reverse proxy

Palmux speaks plain HTTP and a single WebSocket endpoint per branch. Behind
TLS-terminating proxies, make sure to forward upgrade headers. Caddy example:

```caddyfile
palmux.example.com {
  reverse_proxy 127.0.0.1:8080
}
```

nginx requires the usual `Connection: upgrade` / `Upgrade: $http_upgrade`
headers on `location /api/`. Make sure the proxy doesn't buffer responses.

---

## Development

### Branching tips

Palmux v2 self-hosts: you'll often be running a stable `palmux` binary while
hacking on its source. Don't `make serve` in the host repo while you're
attached to it — restarting kills the very Claude CLI you're talking to. See
[`docs/development.md`](docs/development.md) for the worktree pattern that
sidesteps this.

### Servers

```bash
make dev                  # vite dev + Go hot reload (foreground)
make serve                # production binary, background, auto-kill old PID
make serve-stop           # stop the background instance
make serve-logs           # tail tmp/palmux.log
make {dev,serve,serve-stop,serve-logs} INSTANCE=<name>
                          # run alongside the host instance with separate ports
```

`make serve` writes:

- `tmp/palmux.pid` — the PID of the background process; re-running `make
  serve` SIGTERMs it (then SIGKILL after 5s) and starts fresh
- `tmp/palmux.log` — stdout/stderr
- `tmp/palmux.portman.env` — the leased port

### Build / test / lint

```bash
make build         # host arch
make build-linux   # linux amd64
make build-arm     # linux arm64
make test          # Go tests + `npm test` if defined
make lint          # golangci-lint + eslint
```

### Architecture / specs

- Top-level rules and Claude Code's brief: [`CLAUDE.md`](CLAUDE.md)
- Architecture deep-dive: [`docs/original-specs/01-architecture.md`](docs/original-specs/01-architecture.md)
- Phase-by-phase implementation plan: [`docs/original-specs/03-implementation-plan.md`](docs/original-specs/03-implementation-plan.md)
- Claude tab spec: [`docs/original-specs/05-claude-agent-tab.md`](docs/original-specs/05-claude-agent-tab.md), Phase 2+ roadmap in [`06-claude-tab-roadmap.md`](docs/original-specs/06-claude-tab-roadmap.md)

---

## Releases

`v*` tag pushes trigger
[`.github/workflows/release.yml`](.github/workflows/release.yml). The action
builds both Linux architectures, attaches the binaries, and auto-generates
release notes from the commits since the previous tag.

```bash
git tag v0.2.0
git push origin v0.2.0
# → release at https://github.com/tjst-t/palmux2/releases/tag/v0.2.0
```

---

## License

MIT — see [LICENSE](LICENSE) once included. Until then, copyright tjst-t.
