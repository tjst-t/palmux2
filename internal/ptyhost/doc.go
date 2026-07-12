// Package ptyhost implements the `palmux ptyhost` subcommand: a thin,
// claude-agnostic process holder that owns a PTY-spawned child process and
// serves a small framed unix-socket protocol so palmux2 can attach, detach,
// and reattach to it across its own restarts.
//
// Design constraints (see docs/no-halt-agent-design.md and
// docs/DESIGN/adr/ADR-0001..0003):
//
//   - ADR-0001: ptyhost is a detached process; palmux2 is a socket client
//     that reconnects after restart.
//   - ADR-0002 (thin holder): ptyhost knows NOTHING about claude. It spawns
//     whatever opaque argv/env/cwd it is given. Respawn, --resume, hook
//     injection, emulator rendering, multi-client coordination, and incus
//     regenerate gating all live in palmux2, not here. Adding capabilities to
//     this package that duplicate palmux2-side orchestration is a violation
//     signal for that ADR — resist the temptation.
//   - ADR-0003 (cgroup escape): launching a ptyhost tries
//     `systemd-run --user --scope --collect` first and falls back to a
//     setsid-detached process when systemd/D-Bus is unavailable. Detection is
//     a runtime probe (try, then fall back), never a static environment
//     flag.
//
// The package has three pieces:
//
//   - ring.go:     Ring, a fixed-capacity byte ring buffer that tracks
//     absolute (never-reused) offsets so a reconnecting client can request
//     replay from any point it last saw, tolerating wrap/eviction.
//   - protocol.go: the frozen-minimal framed wire protocol (HELLO, ATTACH,
//     DATA, INPUT, RESIZE, ACK, STATUS, SHUTDOWN).
//   - server.go:   Server, which spawns the PTY child, feeds its output into
//     the ring, and serves the protocol over a unix socket (one active
//     client connection at a time — palmux2 is the only intended client).
//   - launch.go:   the spawn path that starts a new `palmux ptyhost` process
//     detached from the caller's process/cgroup lifetime.
package ptyhost
