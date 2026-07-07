package claudetui

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	creackpty "github.com/creack/pty"

	"github.com/tjst-t/palmux2/internal/notify"
	"github.com/tjst-t/palmux2/internal/runtime"
)

// S4d8b1c: when the workspace runtime can build an in-container PTY command
// (incus), claude runs INSIDE the container at these fixed paths.
const (
	// containerClaudeBin is the absolute path of the (host-mounted) claude
	// binary inside the container. A non-login `incus exec` has no ~/.local/bin
	// on PATH, so claude must be invoked by absolute path.
	containerClaudeBin = "/home/ubuntu/.local/bin/claude"
	// containerHookBinPath is where the running palmux binary is bind-mounted
	// inside the container, used as the `<bin> hook` command for in-container
	// claude (the host hookBinPath does not exist in the container).
	containerHookBinPath = "/usr/local/bin/palmux"
)

const (
	// ptyReadBufSize is the read-buffer size for PTY output.
	ptyReadBufSize = 4096

	// gracefulShutdownTimeout is the maximum time to wait for the subprocess to
	// exit after SIGTERM before escalating to SIGKILL.
	gracefulShutdownTimeout = 5 * time.Second
)

// State represents the lifecycle state of a Daemon's subprocess.
type State int32

const (
	// StateIdle means no subprocess has been spawned yet.
	StateIdle State = iota
	// StateRunning means the subprocess is alive and producing output.
	StateRunning
	// StateDead means the subprocess exited unexpectedly; respawnLoop may
	// re-spawn it.
	StateDead
	// StateShutdown means the daemon is shutting down intentionally; no new
	// subprocess will be spawned.
	StateShutdown
)

// String returns a human-readable label for s.
func (s State) String() string {
	switch s {
	case StateIdle:
		return "idle"
	case StateRunning:
		return "running"
	case StateDead:
		return "dead"
	case StateShutdown:
		return "shutdown"
	default:
		return "unknown"
	}
}

// Stats is a point-in-time snapshot of daemon metrics.
type Stats struct {
	// PID is the OS process ID of the subprocess, or 0 if not yet spawned.
	PID int `json:"pid"`
	// RingBytes is the number of bytes currently held in the ring buffer.
	RingBytes int `json:"ring_bytes"`
	// AttachedClients is the number of currently attached WebSocket clients.
	AttachedClients int32 `json:"attached_clients"`
	// Alive is true iff State == StateRunning.
	Alive bool `json:"alive"`
	// State is the string representation of the current daemon state.
	State string `json:"state"`
	// ScrollbackLines is the number of lines in the server-side emulator's
	// scrollback buffer (diagnostic — reflects how much history a fresh attach
	// would replay).
	ScrollbackLines int `json:"scrollback_lines"`
}

// Daemon owns the PTY subprocess and multiplexes its output to an arbitrary
// number of WebSocket clients via a ring buffer / fan-out mechanism.
//
// # Lifecycle
//
//  1. [NewDaemon] — allocate; no subprocess yet.
//  2. First WS attach calls [Daemon.EnsureStarted], which lazily spawns the
//     subprocess (priority_rule 4 — lazy spawn) and starts the respawn loop.
//  3. [Daemon.respawnLoop] monitors subprocess exit via a channel; on
//     unexpected exit it transitions to [StateDead] and, when a session ID is
//     available, re-spawns with `claude --resume <id>` (Fix 4).
//  4. [Daemon.Shutdown] is idempotent (sync.Once) and is the sole caller of
//     proc.Wait() for the subprocess (Fix 2).
//
// # Key invariants
//
//   - The subprocess is always spawned under daemonCtx, never under any HTTP
//     request context, so WS client disconnects do NOT kill it (Fix 7).
//   - PTY reads use a goroutine + channel pattern; SetReadDeadline is never
//     called on the PTY master fd (Fix 6).
//   - Every byte written to the Ring is also fed to the [Emulator] synchronously.
//     emulator.Feed must be fast (pure state-machine update); it never blocks.
//   - Multi-client role coordination is handled by the embedded [roleCoordinator].
//     Exactly one subscriber is active at a time; others are viewers.  Sending
//     input flips the active role to the sender ("last-typed-wins" — Story 3).
type Daemon struct {
	// configuration (immutable after NewDaemon)
	claudeBin     string
	claudeArgs    []string
	worktree      string
	resumeOnDeath bool
	ring          *Ring
	emulator      *Emulator
	logger        *slog.Logger

	// notifyHub is the process-wide hub used to publish Activity Inbox events.
	// claude-tui notifications now arrive via Claude Code hooks (see hooks.go +
	// cmd/palmux/hook.go) rather than terminal screen-scraping; the hub is still
	// referenced here for OSC 52 clipboard events forwarded by the Emulator.
	// May be nil — events are silently discarded in that case.
	notifyHub *notify.Hub
	// repoID, branchID, tabID identify the workspace and tab; injected into the
	// claude subprocess as PALMUX_* env so the hook handler can route
	// notifications back to the originating tab (Sadf90e — tabID distinguishes
	// two Claude-tui tabs in the same workspace).
	repoID, branchID, tabID string

	// Hook wiring (set by the Manager): the local notify endpoint URL, optional
	// auth token, and the absolute path to the palmux binary used as the Claude
	// Code hook command. When notifyURL or hookBinPath is empty, no hook
	// settings/env are injected (e.g. in tests using fake_claude).
	notifyURL, notifyToken, hookBinPath string

	// S4d8b1c: in-container spawn wiring.
	// runtimeResolver returns a runtime.PTYCommander when the workspace's
	// runtime can run claude INSIDE a container (incus), or nil for host. nil
	// resolver / nil result → host exec (default / tests / host runtime).
	runtimeResolver func(repoID, branchID string) runtime.PTYCommander
	// notifyURLInContainer is the bridge-gateway notify URL used for the hook
	// when claude runs in the container (the plain notifyURL's 127.0.0.1 points
	// at the container itself). Empty → fall back to notifyURL.
	notifyURLInContainer string

	// feedMu serializes the readLoop's (ring.Write + emulator.Feed) pair with
	// the attach-time (emulator.RenderSnapshot + ring.Subscribe) pair so a new
	// client's screen-state replay and its live subscription are atomic with
	// respect to incoming PTY bytes — no chunk is double-applied or lost at the
	// boundary. Held only for the duration of a single chunk's write+feed.
	feedMu sync.Mutex

	// roles manages multi-client active/viewer assignment (Story 3).
	roles *roleCoordinator

	// daemonCtx is owned by the Daemon and lives until Shutdown().
	// IMPORTANT: all subprocess exec.CommandContext calls use this context,
	// NOT any per-request HTTP context, so that WS client disconnects do NOT
	// kill the subprocess (Fix 7 — daemonCtx isolation).
	daemonCtx    context.Context
	daemonCancel context.CancelFunc

	// stateMu guards proc, ptmx, sessionID, exited, and lastCols/lastRows.
	stateMu sync.Mutex
	state   atomic.Int32 // holds State; read without lock for lightweight polling

	proc      *os.Process
	ptmx      *os.File // master side of PTY pair
	sessionID string   // set by SetSessionID; used by respawnLoop for --resume

	// lastCols/lastRows are the most recent client-requested terminal size,
	// recorded by Resize even if it arrives before a subprocess exists. Every
	// spawn (initial, crash respawn, or container-regenerate respawn) re-applies
	// them so a fresh PTY does not fall back to the 80x24 default while the client
	// — whose own dimensions are unchanged — sends no new resize. Without this,
	// claude re-renders its TUI at 80 cols ("narrow width" after Update container).
	// 0 means "never resized" → keep the spawn default. Guarded by stateMu.
	lastCols, lastRows uint16

	// exited is written by the spawn goroutine after cmd.Wait() returns and
	// replaced on each re-spawn.  respawnLoop reads the current value under
	// stateMu at the start of each iteration.  Nil until the first spawn.
	// Fix 2 — this is the sole path for proc.Wait(); Shutdown drains it.
	exited chan error

	// spawnMu serialises concurrent EnsureStarted and respawn calls.
	spawnMu sync.Mutex
	spawned bool // true once a subprocess has been successfully spawned

	// sessionIDReady is closed (once) when the first non-empty session ID is
	// recorded via SetSessionID.  respawnLoop blocks on it before re-spawning.
	sessionIDReady chan struct{}
	sessionIDOnce  sync.Once

	// shutdownOnce ensures Shutdown() is idempotent (Fix 2).
	shutdownOnce sync.Once
	shutdownCh   chan struct{}
	shutdownWg   sync.WaitGroup

	// attachedCount counts currently attached WS clients.
	attachedCount atomic.Int32

	// runtimeWaitNotified guards the one-time "container regenerating" status
	// line printed while gateRespawn waits for the runtime to come back.
	runtimeWaitNotified atomic.Bool
}

// DaemonConfig bundles the options for [NewDaemon].
type DaemonConfig struct {
	// ClaudeBin is the path to the claude binary (default: "claude").
	ClaudeBin string
	// ClaudeArgs are additional arguments passed to claude on every spawn.
	ClaudeArgs []string
	// Worktree is the absolute path the subprocess is spawned in (cmd.Dir).
	// When empty, the subprocess inherits the palmux2 server's cwd — which is
	// almost never the right answer for a per-branch tab. Provider /
	// Manager always pass the branch worktree path.
	Worktree string
	// RingSize is the ring buffer capacity in bytes (0 → DefaultRingSize).
	RingSize int
	// ResumeOnDeath, when true, causes respawnLoop to re-spawn with
	// `--resume <lastSessionID>` after an unexpected subprocess exit.
	// Default is true.
	ResumeOnDeath bool
	// Logger is the slog logger to use (nil → slog.Default()).
	Logger *slog.Logger

	// NotifyHub is the notify hub used to publish OSC 52 clipboard events.
	// When nil the Emulator still runs but clipboard events are silently
	// discarded.  Main wires this from the process-wide hub.
	NotifyHub *notify.Hub
	// RepoID, BranchID, TabID identify the workspace and tab. They are
	// stamped onto every [notify.CopyEvent] emitted by the Emulator and
	// injected into the claude subprocess as PALMUX_* env so the Claude Code
	// hook handler can route notifications back to this tab.
	RepoID   string
	BranchID string
	TabID    string

	// NotifyURL is the local palmux notify endpoint (e.g.
	// http://127.0.0.1:8080/api/notify) the injected hook posts to. NotifyToken
	// is the optional auth token. HookBinPath is the absolute path to the palmux
	// binary used as the hook command. When NotifyURL or HookBinPath is empty,
	// no hook settings/env are injected.
	NotifyURL   string
	NotifyToken string
	HookBinPath string

	// S4d8b1c: RuntimeResolver returns a runtime.PTYCommander when the workspace
	// runtime can run claude inside a container (incus), else nil → host exec.
	// NotifyURLInContainer is the bridge-gateway notify URL for the in-container
	// hook.
	RuntimeResolver      func(repoID, branchID string) runtime.PTYCommander
	NotifyURLInContainer string
}

// NewDaemon creates a Daemon from cfg.  No subprocess is spawned yet.
//
// The Emulator is created immediately at default 80×24 (VT-100 default); it
// will be resized when the first WebSocket client calls [Daemon.Resize].
func NewDaemon(cfg DaemonConfig) *Daemon {
	if cfg.ClaudeBin == "" {
		cfg.ClaudeBin = "claude"
	}
	if cfg.RingSize <= 0 {
		cfg.RingSize = DefaultRingSize
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	d := &Daemon{
		claudeBin:            cfg.ClaudeBin,
		claudeArgs:           cfg.ClaudeArgs,
		worktree:             cfg.Worktree,
		resumeOnDeath:        cfg.ResumeOnDeath,
		ring:                 NewRing(cfg.RingSize),
		emulator:             NewEmulator(80, 24, cfg.NotifyHub, cfg.RepoID, cfg.BranchID),
		logger:               cfg.Logger,
		notifyHub:            cfg.NotifyHub,
		repoID:               cfg.RepoID,
		branchID:             cfg.BranchID,
		tabID:                cfg.TabID,
		notifyURL:            cfg.NotifyURL,
		notifyToken:          cfg.NotifyToken,
		hookBinPath:          cfg.HookBinPath,
		runtimeResolver:      cfg.RuntimeResolver,
		notifyURLInContainer: cfg.NotifyURLInContainer,
		daemonCtx:            ctx,
		daemonCancel:         cancel,
		sessionIDReady:       make(chan struct{}),
		shutdownCh:           make(chan struct{}),
		roles:                newRoleCoordinator(cfg.Logger),
	}
	d.state.Store(int32(StateIdle))
	return d
}

// GridSnapshot returns a consistent snapshot of the current visible grid from
// the server-side headless terminal emulator.  It is safe for concurrent use
// and may be called from any goroutine.  Story 2 exposes this via the grid
// WebSocket mode.
func (d *Daemon) GridSnapshot() Grid {
	return d.emulator.GridSnapshot()
}

// RenderSnapshotAndSubscribe atomically (a) captures an ANSI reconstruction of
// the emulator's current screen and (b) registers a live ring subscriber, both
// under feedMu so they line up at exactly one PTY-chunk boundary with the
// readLoop's (ring.Write + emulator.Feed). This is the claude-tui replacement
// for Ring.SnapshotAndSubscribe: the replay sent to a new client is the
// collapsed current screen, not the raw repaint history (which would stack old
// frames into the scrollback as garbage — "scroll up → broken logs"). See
// Emulator.RenderSnapshot.
//
// The caller must call [Ring.Unsubscribe] on the returned [Subscription].
func (d *Daemon) RenderSnapshotAndSubscribe() ([]byte, *Subscription) {
	d.feedMu.Lock()
	defer d.feedMu.Unlock()
	snapshot := d.emulator.RenderSnapshot()
	sub := d.ring.Subscribe()
	return snapshot, sub
}

// EnsureStarted lazily spawns the subprocess on the first call.  Subsequent
// calls are no-ops.  Returns an error if the daemon is already in
// [StateShutdown] or if the spawn fails.
//
// The subprocess is spawned under [Daemon.daemonCtx], not the caller's
// context (Fix 7 — daemonCtx isolation).
//
// On the first successful spawn, EnsureStarted also starts the respawn loop
// goroutine (if ResumeOnDeath is true or if monitoring subprocess exit for
// StateDead transitions is desired).
func (d *Daemon) EnsureStarted(ctx context.Context) error {
	if State(d.state.Load()) == StateShutdown {
		return fmt.Errorf("claudetui daemon: already shut down")
	}
	d.spawnMu.Lock()
	defer d.spawnMu.Unlock()
	if d.spawned {
		return nil
	}
	if err := d.spawnWithArgs(d.claudeArgs); err != nil {
		return fmt.Errorf("claudetui daemon: spawn: %w", err)
	}
	d.spawned = true

	// Start the respawn/monitor loop now that we have a live exited channel.
	// The loop is started under spawnMu to guarantee it sees the correct
	// d.exited value set by spawnWithArgs above.
	d.shutdownWg.Add(1)
	go d.respawnLoop()

	return nil
}

// spawnWithArgs creates the PTY pair, starts the subprocess with args, and
// launches the read and wait goroutines.
//
// MUST be called while holding spawnMu.
func (d *Daemon) spawnWithArgs(args []string) error {
	// Inject Claude Code notification hooks scoped to this subprocess (see
	// hooks.go). The settings travel via --settings so we touch neither the
	// user's ~/.claude nor the repo's .claude; identity + callback URL travel as
	// PALMUX_* env the hook command inherits.
	env := appendOrReplace(os.Environ(), "TERM=xterm-256color")

	// S4d8b1c: resolve the workspace runtime. When it can build an in-container
	// PTY command (incus), claude runs INSIDE the container; otherwise host.
	var pc runtime.PTYCommander
	if d.runtimeResolver != nil {
		pc = d.runtimeResolver(d.repoID, d.branchID)
	}
	inContainer := pc != nil

	// Runtime-aware claude/hook/notify resolution.
	claudeBin := d.claudeBin
	hookBin := d.hookBinPath
	notifyURL := d.notifyURL
	if inContainer {
		claudeBin = containerClaudeBin
		hookBin = containerHookBinPath
		// Inside the container, 127.0.0.1 is the container itself — the host
		// notifyURL is unusable. Use the bridge-gateway URL; if it's unknown
		// (e.g. wildcard --addr / no incus bridge) leave it empty so the hook is
		// skipped entirely rather than injecting a URL that always fails.
		notifyURL = d.notifyURLInContainer
		// The palmux-browser skill plugin only exists inside the container image.
		// --plugin-dir is the correct flag to register it (--add-dir merely grants
		// file access and does NOT load skills).
		args = append([]string{"--plugin-dir", palmuxSkillDir}, args...)
	}

	// Claude Code notification hooks + PALMUX_* env (also consumed by the
	// palmux-browser CLI inside the container). containerEnv carries the same
	// KEY=VALUE pairs for the incus --env path.
	var containerEnv []string
	if hookBin != "" && notifyURL != "" {
		settings, err := buildHookSettings(hookBin)
		if err != nil {
			d.logger.Warn("claudetui: failed to build hook settings; "+
				"notifications disabled for this spawn", "err", err)
		} else {
			args = append([]string{"--settings", settings}, args...)
			for _, kv := range hookEnv(notifyURL, d.notifyToken, d.repoID, d.branchID, d.tabID) {
				env = appendOrReplace(env, kv) // host path
				containerEnv = append(containerEnv, kv)
			}
		}
	}

	// Build the command. daemonCtx (not a request ctx) so Shutdown can cancel
	// while WS disconnects keep the subprocess alive.
	var cmd *exec.Cmd
	if inContainer {
		// claude runs INSIDE the container: `incus exec -t <inst> --user … --cwd
		// <worktree> --env … -- /abs/claude <args>`. cwd + container env are
		// delivered through the incus flags, not cmd.Dir/cmd.Env.
		argv := append([]string{claudeBin}, args...)
		cenv := append([]string{"TERM=xterm-256color"}, containerEnv...)
		cmd = pc.PTYCommand(d.daemonCtx, argv, runtime.PTYCommandOpts{
			Cwd: d.worktree,
			Env: cenv,
		})
	} else {
		cmd = exec.CommandContext(d.daemonCtx, claudeBin, args...)
		// Run in the branch worktree so `~/.claude/projects/<slug>` resolves to
		// the right repo. cmd.Dir == "" means inherit (tests without a worktree).
		if d.worktree != "" {
			cmd.Dir = d.worktree
		}
		cmd.Env = env
	}

	ptmx, err := creackpty.Start(cmd)
	if err != nil {
		return fmt.Errorf("pty start: %w", err)
	}

	// Create a fresh exited channel for this spawn.
	exited := make(chan error, 1)

	d.stateMu.Lock()
	// Close the previous ptmx fd before replacing it on respawn — sprint
	// review F2: without this, every claude crash + respawn cycle leaks one
	// PTY master fd until Shutdown.
	if d.ptmx != nil {
		_ = d.ptmx.Close()
	}
	d.ptmx = ptmx
	d.proc = cmd.Process
	d.exited = exited
	d.state.Store(int32(StateRunning))
	// Snapshot the last client-requested size under the same lock so a resize
	// that raced this spawn is not lost. Applied below, outside the lock.
	cols, rows := d.lastCols, d.lastRows
	d.stateMu.Unlock()

	// Re-apply the last known client size to the freshly-created PTY so a respawn
	// (crash recovery, or container regenerate) does not drop back to the 80x24
	// spawn default. 0 means "never resized" → leave the default. Fixes the
	// "narrow width after Update container" symptom.
	if cols > 0 && rows > 0 {
		if err := creackpty.Setsize(ptmx, &creackpty.Winsize{Rows: rows, Cols: cols}); err != nil {
			d.logger.Warn("claudetui: re-apply size on spawn failed", "err", err)
		} else {
			d.emulator.Resize(int(cols), int(rows))
		}
	}

	d.logger.Info("claudetui: subprocess spawned",
		"bin", d.claudeBin,
		"args", args,
		"pid", cmd.Process.Pid,
	)

	// Background read loop: PTY → ring → broadcast.
	d.shutdownWg.Add(1)
	go func() {
		defer d.shutdownWg.Done()
		d.readLoop(ptmx)
	}()

	// Background drainer: emulator → PTY stdin (subprocess input).
	//
	// The vt.Emulator generates response bytes for some ANSI queries (DA1 / DA2
	// device attributes, cursor position report, etc.) by writing into an
	// internal io.Pipe. If we never drain that pipe its 64KiB buffer fills,
	// at which point the next response Write inside Emulator.Write blocks
	// **while still holding the SafeEmulator writer lock**. That deadlocks every
	// subsequent GridSnapshot caller (each reader-lock attempt waits forever).
	//
	// Forwarding the pipe output to ptmx (the subprocess stdin) is the
	// architecturally correct fix: claude asked the terminal a question, the
	// emulator answers, the answer goes back to claude. claude may or may not
	// care, but the pipe drains and the lock is never held across a blocked
	// write.
	d.shutdownWg.Add(1)
	go func() {
		defer d.shutdownWg.Done()
		buf := make([]byte, 4096)
		for {
			select {
			case <-d.shutdownCh:
				return
			default:
			}
			n, err := d.emulator.Read(buf)
			if err != nil {
				return
			}
			if n <= 0 {
				continue
			}
			if _, werr := ptmx.Write(buf[:n]); werr != nil {
				return
			}
		}
	}()

	// Background wait goroutine: calls cmd.Wait() and sends the result to
	// exited.  This goroutine is the SOLE caller of cmd.Wait() for this
	// subprocess instance (Fix 2 — no double-Wait).  Shutdown() drains exited.
	go func() {
		waitErr := cmd.Wait()
		select {
		case exited <- waitErr:
		default:
		}
		// Do not close exited here; Shutdown drains it and the channel is
		// replaced on respawn.  Closing a replaced channel would be a bug.
	}()

	return nil
}

// readLoop pumps bytes from ptmx into the ring buffer (which also broadcasts
// to live subscribers atomically — Fix 3 via Ring.Write).
//
// Fix 6: goroutine + channel pattern instead of SetReadDeadline, which is
// unreliable on PTY fds on Linux.  Each PTY read is issued in a short-lived
// goroutine so the outer loop can respond to shutdownCh without blocking.
func (d *Daemon) readLoop(ptmx *os.File) {
	type readResult struct {
		n   int
		err error
	}
	buf := make([]byte, ptyReadBufSize)

	for {
		// Check shutdown before each read attempt.
		select {
		case <-d.shutdownCh:
			return
		default:
		}

		// Issue the read in a goroutine so we can select on shutdownCh.
		// The goroutine is short-lived; it sends exactly one result.
		ch := make(chan readResult, 1)
		go func(b []byte) {
			n, err := ptmx.Read(b)
			ch <- readResult{n, err}
		}(buf)

		select {
		case <-d.shutdownCh:
			// ptmx will be closed by Shutdown, unblocking the read goroutine.
			return
		case r := <-ch:
			if r.n > 0 {
				chunk := make([]byte, r.n)
				copy(chunk, buf[:r.n])
				// feedMu makes (ring.Write + emulator.Feed) atomic w.r.t. a
				// concurrent attach, which takes the same lock around
				// (RenderSnapshot + Subscribe). This guarantees a new client's
				// rendered screen and its live subscription line up exactly at
				// one chunk boundary.
				d.feedMu.Lock()
				// Ring.Write broadcasts to subscribers under the ring lock (Fix 3).
				if _, werr := d.ring.Write(chunk); werr != nil {
					d.logger.Warn("claudetui: ring write error", "err", werr)
				}
				// Feed the same bytes to the headless terminal emulator.
				// emulator.Feed is a synchronous state-machine update; it does
				// not block and adds only a small constant overhead per chunk.
				d.emulator.Feed(chunk)
				d.feedMu.Unlock()
			}
			if r.err != nil {
				if r.err != io.EOF {
					d.logger.Debug("claudetui: pty read error", "err", r.err)
				}
				return
			}
		}
	}
}

// respawnLoop runs as a background goroutine (started by EnsureStarted).
// It monitors the subprocess by waiting on the exited channel.  On unexpected
// exit, it transitions to StateDead and — if ResumeOnDeath is true — waits for
// a session ID and re-spawns with --resume <id> (Fix 4).
//
// This replaces the PoC's spawnOnce sync.Once, allowing the daemon to recover
// from subprocess crashes automatically.
func (d *Daemon) respawnLoop() {
	defer d.shutdownWg.Done()
	for {
		// Read the current exited channel under stateMu so we always have the
		// one that corresponds to the most recently spawned process.
		d.stateMu.Lock()
		exited := d.exited
		d.stateMu.Unlock()

		if exited == nil {
			// No subprocess has been spawned yet; shouldn't happen because
			// EnsureStarted starts us after spawn, but guard anyway.
			select {
			case <-d.shutdownCh:
				return
			case <-time.After(50 * time.Millisecond):
				continue
			}
		}

		// Wait for the current subprocess to exit.
		var exitErr error
		select {
		case <-d.shutdownCh:
			return
		case exitErr = <-exited:
		}

		// If we are in StateShutdown, the exit was intentional — stop.
		if State(d.state.Load()) == StateShutdown {
			return
		}

		// Unexpected exit → transition to StateDead.
		d.stateMu.Lock()
		pid := 0
		if d.proc != nil {
			pid = d.proc.Pid
		}
		d.stateMu.Unlock()

		d.state.Store(int32(StateDead))
		d.logger.Warn("claudetui: subprocess died unexpectedly",
			"pid", pid,
			"err", exitErr,
		)

		if !d.resumeOnDeath {
			d.logger.Info("claudetui: resume-on-death disabled; not re-spawning")
			return
		}

		// Wait until a session ID is available before re-spawning, or until
		// shutdown signals us (Fix 4 — respawnLoop waits for SetSessionID).
		select {
		case <-d.shutdownCh:
			return
		case <-d.sessionIDReady:
		}

		d.stateMu.Lock()
		sid := d.sessionID
		d.stateMu.Unlock()

		// Build re-spawn args: base args + --resume <id>.
		respawnArgs := make([]string, 0, len(d.claudeArgs)+2)
		respawnArgs = append(respawnArgs, d.claudeArgs...)
		respawnArgs = append(respawnArgs, "--resume", sid)

		d.logger.Info("claudetui: re-spawning with --resume",
			"session_id", sid,
			"args", respawnArgs,
		)

		// S4d8b1c-fix2: gate the re-spawn so a regenerating incus container
		// (briefly destroyed during Update) doesn't make `incus exec` re-run at
		// full speed and spam "Error: Instance is not running" into the terminal.
		if !d.gateRespawn() {
			return // shutdown signalled while waiting
		}

		// S52fc2c-4: before respawning, kill any lingering in-container claude so
		// only one instance runs at a time. [AC-S52fc2c-4-2]
		if d.runtimeResolver != nil {
			if pc := d.runtimeResolver(d.repoID, d.branchID); pc != nil {
				if kk, ok := pc.(runtime.ContainerProcessKiller); ok {
					kCtx, kCancel := context.WithTimeout(context.Background(), 3*time.Second)
					if err := kk.KillContainerProcesses(kCtx, "TERM", containerClaudeBin); err != nil {
						d.logger.Debug("claudetui: pre-respawn in-container kill (non-fatal)", "err", err)
					}
					kCancel()
				}
			}
		}

		d.spawnMu.Lock()
		spawnErr := d.spawnWithArgs(respawnArgs)
		d.spawnMu.Unlock()

		if spawnErr != nil {
			d.logger.Error("claudetui: respawn failed", "err", spawnErr)
			return
		}
		// Loop continues; will now wait on the new exited channel.
	}
}

// respawnReadyPollInterval is how often gateRespawn re-checks runtime readiness
// while waiting for a regenerating container to come back.
const respawnReadyPollInterval = 1 * time.Second

// runtimeReady reports whether the workspace runtime is ready to exec into.
// Host runtime (nil resolver / nil commander / commander without a Status
// method) is always considered ready. For incus it returns true only when the
// container Status is StateReady — false while the container is being
// regenerated/restarted, which is what lets gateRespawn pause instead of
// re-exec'ing into a destroyed container.
func (d *Daemon) runtimeReady() bool {
	if d.runtimeResolver == nil {
		return true
	}
	pc := d.runtimeResolver(d.repoID, d.branchID)
	if pc == nil {
		return true
	}
	sp, ok := pc.(interface{ Status() runtime.Status })
	if !ok {
		return true
	}
	return sp.Status().State == runtime.StateReady
}

// gateRespawn pauses the respawn loop until it is safe to re-exec, returning
// false if shutdown was signalled while waiting. During an incus container
// regenerate the container is briefly destroyed; re-exec'ing `incus exec` then
// fails instantly with "Instance is not running". We wait (printing one status
// line) until the container is ready again, so no error spam reaches the
// terminal. When the runtime is already ready (host runtime, or a settled
// container) this is a no-op and respawn timing is unchanged.
func (d *Daemon) gateRespawn() bool {
	if d.runtimeReady() {
		return true
	}
	d.announceContainerWait()
	for {
		select {
		case <-d.shutdownCh:
			return false
		case <-time.After(respawnReadyPollInterval):
		}
		if d.runtimeReady() {
			// Re-arm the one-time notice for the next outage.
			d.runtimeWaitNotified.Store(false)
			return true
		}
	}
}

// announceContainerWait writes a single status line to the terminal so the user
// understands why the session paused (vs. the old error spam). Idempotent per
// outage via runtimeWaitNotified.
func (d *Daemon) announceContainerWait() {
	if d.runtimeWaitNotified.Swap(true) {
		return
	}
	msg := []byte("\r\n\x1b[33m[palmux] workspace container is restarting — reconnecting automatically when it's back…\x1b[0m\r\n")
	d.feedMu.Lock()
	_, _ = d.ring.Write(msg)
	d.emulator.Feed(msg)
	d.feedMu.Unlock()
}

// SetSessionID records the session ID for the currently running claude process.
// The first non-empty call unblocks respawnLoop so it can re-spawn with
// --resume on next exit (Fix 4 — Story 4 will call this via fsnotify detection).
func (d *Daemon) SetSessionID(id string) {
	if id == "" {
		return
	}
	d.stateMu.Lock()
	d.sessionID = id
	d.stateMu.Unlock()
	// Signal once that a session ID is now available.
	d.sessionIDOnce.Do(func() {
		close(d.sessionIDReady)
	})
}

// SessionID returns the most recently recorded session ID (may be empty).
func (d *Daemon) SessionID() string {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	return d.sessionID
}

// Worktree returns the branch worktree path the subprocess was spawned in
// (may be empty for tests that did not pass one).
func (d *Daemon) Worktree() string {
	return d.worktree
}

// WriteInput writes bytes to the PTY master (stdin of the subprocess).
func (d *Daemon) WriteInput(ctx context.Context, p []byte) error {
	d.stateMu.Lock()
	ptmx := d.ptmx
	d.stateMu.Unlock()
	if ptmx == nil {
		return fmt.Errorf("claudetui daemon: subprocess not started")
	}
	if _, err := ptmx.Write(p); err != nil {
		return fmt.Errorf("claudetui daemon: pty write: %w", err)
	}
	return nil
}

// Resize propagates a terminal resize to the PTY via SIGWINCH / TIOCSWINSZ
// and keeps the headless [Emulator] grid in sync.
// Fully wired to the frontend in Story 3 via FitAddon events.
func (d *Daemon) Resize(cols, rows uint16) error {
	// Record the requested size under the same lock that guards ptmx, so it is
	// never lost to a race with spawnWithArgs: whichever runs second sees the
	// other's effect. If a subprocess exists, apply now; otherwise the pending
	// spawn will pick lastCols/lastRows up (fixes the "narrow width after Update
	// container" race where the client's reconnect POST /resize arrives before the
	// lazy EnsureStarted spawn has created the PTY).
	d.stateMu.Lock()
	d.lastCols, d.lastRows = cols, rows
	ptmx := d.ptmx
	d.stateMu.Unlock()
	if ptmx == nil {
		// Not started yet — size is remembered and applied on the next spawn.
		return nil
	}
	if err := creackpty.Setsize(ptmx, &creackpty.Winsize{
		Rows: rows,
		Cols: cols,
	}); err != nil {
		return fmt.Errorf("claudetui daemon: pty setsize: %w", err)
	}
	// Keep the emulator dimensions in sync with the PTY so GridSnapshot()
	// reflects the correct terminal size.
	d.emulator.Resize(int(cols), int(rows))
	return nil
}

// CurrentStats returns a non-blocking Stats snapshot.
func (d *Daemon) CurrentStats() Stats {
	st := State(d.state.Load())
	d.stateMu.Lock()
	pid := 0
	if d.proc != nil {
		pid = d.proc.Pid
	}
	d.stateMu.Unlock()
	return Stats{
		PID:             pid,
		RingBytes:       d.ring.Len(),
		AttachedClients: d.attachedCount.Load(),
		Alive:           st == StateRunning,
		State:           st.String(),
		ScrollbackLines: d.emulator.ScrollbackLen(),
	}
}

// Shutdown performs a graceful, idempotent shutdown (Fix 2 — sync.Once guard).
//
//  1. Sets state to StateShutdown.
//  2. Cancels the daemon context (unblocks exec.CommandContext).
//  3. Sends SIGTERM; waits up to gracefulShutdownTimeout; escalates to SIGKILL.
//  4. Drains the exited channel (the sole proc.Wait() path — Fix 2).
//  5. Closes the PTY master fd (unblocks readLoop goroutines).
//  6. Closes shutdownCh (unblocks respawnLoop and readLoop select).
//  7. Waits for all background goroutines via shutdownWg.
//
// Fix 2 guarantee: Shutdown is the sole owner of the proc.Wait() call path via
// the exited channel — the spawn goroutine sends to exited, Shutdown drains it.
// No double-Wait on the same os.Process occurs.
func (d *Daemon) Shutdown() {
	d.shutdownOnce.Do(func() {
		d.state.Store(int32(StateShutdown))
		d.daemonCancel()

		d.stateMu.Lock()
		proc := d.proc
		ptmx := d.ptmx
		exited := d.exited
		d.stateMu.Unlock()

		if proc != nil {
			d.logger.Info("claudetui: sending SIGTERM to subprocess", "pid", proc.Pid)
			_ = proc.Signal(syscall.SIGTERM)

			if exited != nil {
				// Drain the exited channel (the spawn goroutine calls cmd.Wait()).
				select {
				case <-exited:
					// subprocess exited cleanly after SIGTERM
				case <-time.After(gracefulShutdownTimeout):
					d.logger.Warn("claudetui: subprocess did not exit; sending SIGKILL", "pid", proc.Pid)
					_ = proc.Signal(syscall.SIGKILL)
					// Drain after SIGKILL.
					select {
					case <-exited:
					case <-time.After(2 * time.Second):
						d.logger.Error("claudetui: subprocess still alive after SIGKILL; giving up")
					}
				}
			}
		}

		// S52fc2c-4: reap any lingering in-container claude process. Killing the
		// host-side incus exec wrapper does not always propagate SIGTERM into the
		// container child. We attempt an explicit pkill inside the container as
		// a best-effort cleanup. Runs after the host process is dead (or timed out)
		// so we don't race with the live process's own cleanup.
		if d.runtimeResolver != nil {
			if pc := d.runtimeResolver(d.repoID, d.branchID); pc != nil {
				if kk, ok := pc.(runtime.ContainerProcessKiller); ok {
					kCtx, kCancel := context.WithTimeout(context.Background(), 5*time.Second)
					if err := kk.KillContainerProcesses(kCtx, "TERM", containerClaudeBin); err != nil {
						d.logger.Debug("claudetui: in-container claude TERM (non-fatal)", "err", err)
					}
					kCancel()
				}
			}
		}

		// Ensure sessionIDReady is closed so respawnLoop unblocks if it is
		// waiting there — otherwise shutdownWg.Wait could deadlock.
		d.sessionIDOnce.Do(func() { close(d.sessionIDReady) })

		// Closing shutdownCh unblocks respawnLoop and the readLoop select.
		close(d.shutdownCh)

		if ptmx != nil {
			_ = ptmx.Close()
		}

		// Close emulator BEFORE waiting for the wait group. The emulator
		// drainer goroutine blocks in emulator.Read(); closing the underlying
		// io.Pipe makes Read return io.EOF and the drainer exits. Without
		// this, shutdownWg.Wait would deadlock waiting for the drainer.
		d.emulator.Close()

		d.shutdownWg.Wait()
		d.logger.Info("claudetui: daemon shutdown complete")
	})
}

// appendOrReplace either appends "KEY=value" to env or replaces the existing
// entry with a matching key prefix.
func appendOrReplace(env []string, kv string) []string {
	key := strings.SplitN(kv, "=", 2)[0] + "="
	for i, e := range env {
		if strings.HasPrefix(e, key) {
			env[i] = kv
			return env
		}
	}
	return append(env, kv)
}
