# Palmux v2

Web-based terminal client built around tmux. Run multiple Claude Code agents in
parallel, browse files, and review git diffs from one browser tab — desktop or
mobile.

Palmux is a single Go binary with the frontend embedded. tmux is treated as an
implementation detail: every browser session attaches to a tmux session named
`_palmux_{repoId}_{branchId}`, and each tab maps to a tmux window (`palmux:…`).

## Quick start

```bash
# Required on PATH: tmux, ghq, gwq, git
make build           # produces ./bin/palmux
./bin/palmux --addr :8080 --config-dir ~/.config/palmux
```

Open <http://localhost:8080>. The first repository you Open via the Drawer
must already be cloned under `ghq root`.

### Running with --token

Pass `--token <secret>` to require authentication. First-time visitors hit
`/auth?token=<secret>` to receive a 90-day signed cookie. Hooks and CLI calls
to `/api/notify` use a Bearer header.

## Development

```bash
make dev   # vite dev + Go hot reload, ports leased via portman
```

Both servers come up on ports allocated by [portman](https://github.com/tjst-t/port-manager);
`make ports` prints the actual ports.

## Architecture

See [`docs/original-specs/01-architecture.md`](docs/original-specs/01-architecture.md)
and [`CLAUDE.md`](CLAUDE.md). Highlights:

- **Domain model** lives in `internal/domain` with no external dependencies.
- **Tab modules** are self-contained: implement `tab.Provider`, register in
  `cmd/palmux/main.go`, register a renderer with `frontend/src/lib/tab-registry.ts`.
- **State store** (`internal/store`) is the source of truth. Two ticks: a 5s
  tmux health check and a 30s worktree refresh.
- **WebSocket events** (`/api/events`) fan out from `store.EventHub` to
  every connected browser; clients re-fetch state via REST after a reconnect.

## Notification hooks

On startup Palmux writes `${configDir}/env.${port}` with `PALMUX_URL`,
`PALMUX_TOKEN`, and `PALMUX_PORT`. Hook scripts can source it and POST a
notification:

```bash
source ~/.config/palmux/env.8080
curl -fsS "$PALMUX_URL/api/notify" \
  -H "Authorization: Bearer $PALMUX_TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"tmuxSession\":\"$TMUX_SESSION\",\"type\":\"stop\",\"message\":\"$1\"}"
```

`tmuxSession` is decoded back into `(repoId, branchId)`. The Drawer pulses an
amber dot for the affected branch; the Claude tab shows an unread badge that
auto-clears when the user focuses that tab.

## Recommended tmux configuration

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

## Builds

```bash
make build         # darwin/linux host
make build-linux   # linux amd64
make build-arm     # linux arm64
make test          # Go tests + frontend tests
make lint          # golangci-lint + eslint
```

## Configuration

`~/.config/palmux/settings.json` holds device-shared settings. Schema:

```json
{
  "branchSortOrder": "name",
  "imageUploadDir": "/tmp/palmux-uploads",
  "toolbar": {
    "claude": {
      "rows": [[{"type":"key","key":"/clear","text":"/clear"}]]
    }
  }
}
```

Per-device settings (theme, font size, drawer width, split ratio) live in
`localStorage` under the `palmux:` prefix.

## License

MIT — see [LICENSE](LICENSE) once included. Until then, copyright tjst-t.
