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
type Daemon struct {
	// configuration (immutable after NewDaemon)
	claudeBin     string
	claudeArgs    []string
	resumeOnDeath bool
	ring          *Ring
	logger        *slog.Logger

	// daemonCtx is owned by the Daemon and lives until Shutdown().
	// IMPORTANT: all subprocess exec.CommandContext calls use this context,
	// NOT any per-request HTTP context, so that WS client disconnects do NOT
	// kill the subprocess (Fix 7 — daemonCtx isolation).
	daemonCtx    context.Context
	daemonCancel context.CancelFunc

	// stateMu guards proc, ptmx, sessionID, and exited.
	stateMu sync.Mutex
	state   atomic.Int32 // holds State; read without lock for lightweight polling

	proc      *os.Process
	ptmx      *os.File // master side of PTY pair
	sessionID string   // set by SetSessionID; used by respawnLoop for --resume

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
}

// DaemonConfig bundles the options for [NewDaemon].
type DaemonConfig struct {
	// ClaudeBin is the path to the claude binary (default: "claude").
	ClaudeBin string
	// ClaudeArgs are additional arguments passed to claude on every spawn.
	ClaudeArgs []string
	// RingSize is the ring buffer capacity in bytes (0 → DefaultRingSize).
	RingSize int
	// ResumeOnDeath, when true, causes respawnLoop to re-spawn with
	// `--resume <lastSessionID>` after an unexpected subprocess exit.
	// Default is true.
	ResumeOnDeath bool
	// Logger is the slog logger to use (nil → slog.Default()).
	Logger *slog.Logger
}

// NewDaemon creates a Daemon from cfg.  No subprocess is spawned yet.
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
		claudeBin:      cfg.ClaudeBin,
		claudeArgs:     cfg.ClaudeArgs,
		resumeOnDeath:  cfg.ResumeOnDeath,
		ring:           NewRing(cfg.RingSize),
		logger:         cfg.Logger,
		daemonCtx:      ctx,
		daemonCancel:   cancel,
		sessionIDReady: make(chan struct{}),
		shutdownCh:     make(chan struct{}),
	}
	d.state.Store(int32(StateIdle))
	return d
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
	// exec.CommandContext uses daemonCtx so that cancellation (Shutdown) can
	// terminate the subprocess while keeping it alive across WS disconnects.
	cmd := exec.CommandContext(d.daemonCtx, d.claudeBin, args...)
	// Inherit the full environment so interactive TUIs render correctly.
	cmd.Env = appendOrReplace(os.Environ(), "TERM=xterm-256color")

	ptmx, err := creackpty.Start(cmd)
	if err != nil {
		return fmt.Errorf("pty start: %w", err)
	}

	// Create a fresh exited channel for this spawn.
	exited := make(chan error, 1)

	d.stateMu.Lock()
	d.ptmx = ptmx
	d.proc = cmd.Process
	d.exited = exited
	d.state.Store(int32(StateRunning))
	d.stateMu.Unlock()

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
				// Ring.Write broadcasts to subscribers under the ring lock (Fix 3).
				if _, werr := d.ring.Write(chunk); werr != nil {
					d.logger.Warn("claudetui: ring write error", "err", werr)
				}
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

// Resize propagates a terminal resize to the PTY via SIGWINCH / TIOCSWINSZ.
// Fully wired to the frontend in Story 3 via FitAddon events.
func (d *Daemon) Resize(cols, rows uint16) error {
	d.stateMu.Lock()
	ptmx := d.ptmx
	d.stateMu.Unlock()
	if ptmx == nil {
		return fmt.Errorf("claudetui daemon: subprocess not started")
	}
	if err := creackpty.Setsize(ptmx, &creackpty.Winsize{
		Rows: rows,
		Cols: cols,
	}); err != nil {
		return fmt.Errorf("claudetui daemon: pty setsize: %w", err)
	}
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

		// Ensure sessionIDReady is closed so respawnLoop unblocks if it is
		// waiting there — otherwise shutdownWg.Wait could deadlock.
		d.sessionIDOnce.Do(func() { close(d.sessionIDReady) })

		// Closing shutdownCh unblocks respawnLoop and the readLoop select.
		close(d.shutdownCh)

		if ptmx != nil {
			_ = ptmx.Close()
		}

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
