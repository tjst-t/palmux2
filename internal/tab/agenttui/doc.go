// Package agenttui provides the production PTY daemon for agent TUI tabs
// (originally the claude-tui tab — Track B, Sprint A S7ce250; S0e8afb-2's P2
// graft generalized argv/env/hook construction behind [agent.Adapter], and
// this package (formerly "claudetui") was renamed to "agenttui" as part of
// that same graft — S0e8afb-1 moved the ptyhost socket-client seam here
// first, S0e8afb-2 moved Daemon/Manager themselves).
//
// The daemon owns a Go-managed PTY that runs `claude` (or any configured
// binary) as an interactive subprocess.  Multiple WebSocket clients may
// attach concurrently; they all see a full ring-buffer replay followed by
// live PTY output.
//
// # Lifecycle
//
//  1. [NewDaemon] — allocate, configure; no subprocess yet.
//  2. First WS attach calls [Daemon.EnsureStarted], which lazily spawns the
//     subprocess under a PTY (priority_rule 4 — lazy spawn).
//  3. On unexpected subprocess exit, [Daemon.respawnLoop] transitions to
//     [StateDead] and re-spawns using `claude --resume <lastSessionID>` when
//     a session ID is available (Fix 4).
//  4. [Daemon.Shutdown] performs a graceful, idempotent teardown — protected
//     by [sync.Once] and the sole caller of proc.Wait() (Fix 2).
//
// # Key design decisions (S7ce250-1)
//
//   - PTY read loop uses goroutine + channel, never SetReadDeadline (Fix 6).
//   - subprocess is spawned under daemonCtx, not the HTTP request context,
//     so WS client disconnects do NOT kill the process (Fix 7).
//   - [Ring.SnapshotAndSubscribe] is atomic under a single lock, preventing
//     live-byte loss for newly attaching clients (Fix 3).
//   - [ClaudeArgs] implements flag.Value for repeated --claude-arg flags;
//     spaces inside arguments are preserved (Fix 5).
//
// Story 2 will implement the tab.Provider wrapper; Story 3 will wire PTY
// resize from the frontend; Story 4 will add session-ID detection via
// fsnotify.
package agenttui
