package claudetui

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/notify"
	"github.com/tjst-t/palmux2/internal/ptyhost"
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
	// gracefulShutdownTimeout is the maximum time to wait for the ptyhost to
	// confirm the child has exited after a SHUTDOWN request before Daemon
	// gives up waiting (the ptyhost itself still owns the SIGTERM→SIGKILL
	// escalation — see ptyhost.Server.terminateChild).
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
	// PID is the OS process ID of the subprocess (as reported by the
	// ptyhost holding it), or 0 if not yet spawned.
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
	// Degraded is true when the currently-attached ptyhost reported a
	// protocol version this Daemon does not recognize (ADR-0002 §2 — a
	// version-skewed ptyhost is NOT killed, just surfaced as degraded).
	// [AC-S3f2658-2-3]
	Degraded bool `json:"degraded"`
	// DegradedReason is a human-readable explanation, non-empty iff Degraded.
	DegradedReason string `json:"degraded_reason,omitempty"`
}

// Daemon is a thin socket CLIENT of a `palmux ptyhost` process (ADR-0002):
// it no longer owns a PTY directly. It multiplexes the ptyhost's output to
// an arbitrary number of WebSocket clients via a ring buffer / fan-out
// mechanism, exactly as before — only WHERE the bytes come from changed.
//
// # Lifecycle
//
//  1. [NewDaemon] — allocate; no ptyhost/subprocess yet.
//  2. First WS attach calls [Daemon.EnsureStarted], which lazily
//     spawns-or-attaches (priority_rule 4 — lazy spawn; §3 of
//     docs/no-halt-agent-design.md — a surviving ptyhost from a PRIOR
//     palmux2 lifetime is attached to instead of spawning a new one) and
//     starts the respawn loop.
//  3. [Daemon.respawnLoop] monitors the ptyhost connection; on unexpected
//     loss it transitions to [StateDead] and, when a session ID is
//     available, re-spawns a NEW ptyhost with `claude --resume <id>`
//     (ADR-0002: respawn = spawn a new ptyhost, never a re-exec in place).
//  4. [Daemon.Shutdown] is idempotent (sync.Once) and sends the ptyhost a
//     SHUTDOWN request (the ptyhost itself owns SIGTERM→SIGKILL escalation
//     of the child it holds).
//
// # Key invariants
//
//   - The ptyhost is always spawned/attached under [Daemon.daemonCtx], never
//     any HTTP request context, so WS client disconnects do NOT kill it
//     (Fix 7).
//   - Every byte read from the ptyhost socket is also fed to the [Emulator]
//     synchronously, under feedMu, exactly as the old PTY read loop did.
//   - Multi-client role coordination is handled by the embedded
//     [roleCoordinator], unmodified by this migration (ADR-0002 §4 — stays
//     in palmux2).
//   - Argv/env/cwd assembly (hooks, --permission-mode, incus PTYCommander
//     wrapper, --plugin-dir) is UNCHANGED byte-for-byte from before; only the
//     final step (who executes it under a PTY) moved to ptyhost.
type Daemon struct {
	// configuration (immutable after NewDaemon)
	claudeBin  string
	claudeArgs []string
	// permissionMode returns the current claude --permission-mode value (a global
	// setting, default "auto"). A func (not a snapshot) so a settings change is
	// picked up on the next respawn. Never nil after NewDaemon.
	permissionMode func() string
	worktree       string
	resumeOnDeath  bool
	ring           *Ring
	emulator       *Emulator
	logger         *slog.Logger

	// notifyHub is the process-wide hub used to publish Activity Inbox events.
	// claude-tui notifications now arrive via Claude Code hooks (see hooks.go +
	// cmd/palmux/hook.go) rather than terminal screen-scraping; the hub is still
	// referenced here for OSC 52 clipboard events forwarded by the Emulator.
	// May be nil — events are silently discarded in that case.
	notifyHub *notify.Hub
	// repoID, branchID, tabID identify the workspace and tab; injected into the
	// claude subprocess as PALMUX_* env so the hook handler can route
	// notifications back to the originating tab (Sadf90e — tabID distinguishes
	// two Claude-tui tabs in the same workspace). Also folded into the ptyhost
	// discovery seed (repoID__branchID__tabID) — see ptyHostSeed.
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

	// S3f2658-2 (ADR-0002 thin holder): ptyhost wiring.
	// palmuxBin is the palmux binary re-invoked as `<palmuxBin> ptyhost ...`
	// (production spawn path). Empty → this Daemon runs in the automatic
	// in-process ptyhost fallback (unit tests / hermetic local dev — see
	// ptyclient.go).
	palmuxBin string
	// instancePrefix isolates concurrent palmux instances (host vs
	// INSTANCE=dev), mirrored from domain.PalmuxSessionPrefix at construction
	// time.
	instancePrefix string
	// ptyHostLaunch is the injectable seam for starting/ensuring a ptyhost is
	// listening (see [PtyHostLaunchFunc]). Never nil after NewDaemon.
	ptyHostLaunch PtyHostLaunchFunc
	// runDirOverride, when non-empty, replaces ptyhost.RunDir(instancePrefix)
	// as the base directory for this Daemon's socket/status paths. Sourced
	// from DaemonConfig.RunDirOverride, or auto-generated (unique per Daemon)
	// when palmuxBin is empty, so unit tests never collide with each other or
	// with a real running instance on the same host.
	runDirOverride string
	// ptyHostRingSize is the ring buffer capacity requested of the ptyhost
	// side (independent of the palmux2-side Ring size in RingSize).
	ptyHostRingSize int

	// feedMu serializes the readLoop's (ring.Write + emulator.Feed) pair with
	// the attach-time (emulator.RenderSnapshot + ring.Subscribe) pair so a new
	// client's screen-state replay and its live subscription are atomic with
	// respect to incoming ptyhost bytes — no chunk is double-applied or lost at
	// the boundary. Held only for the duration of a single chunk's write+feed.
	feedMu sync.Mutex

	// roles manages multi-client active/viewer assignment (Story 3).
	roles *roleCoordinator

	// daemonCtx is owned by the Daemon and lives until Shutdown().
	// IMPORTANT: all ptyhost launch/attach calls use this context, NOT any
	// per-request HTTP context, so that WS client disconnects do NOT kill the
	// subprocess (Fix 7 — daemonCtx isolation).
	daemonCtx    context.Context
	daemonCancel context.CancelFunc

	// stateMu guards conn, pid, sessionID, degraded*, and lastCols/lastRows.
	stateMu sync.Mutex
	state   atomic.Int32 // holds State; read without lock for lightweight polling

	// connWriteMu serializes writes to conn (multiple goroutines — WS input
	// handlers, Resize, the emulator response drainer, the restore-jiggle —
	// may write concurrently; the wire protocol is not safe for interleaved
	// writes). Always paired with a stateMu-guarded read of conn (see
	// writeFrame) — never held across a conn read.
	connWriteMu sync.Mutex

	conn      net.Conn // socket connection to the current ptyhost (nil before first spawn)
	pid       int      // the CHILD pid as reported by the ptyhost (informational — CurrentStats)
	sessionID string   // set by SetSessionID; used by respawnLoop for --resume

	// degraded / degradedReason surface a ptyhost HELLO protocol-version
	// mismatch (ADR-0002 §2 — the ptyhost is NOT killed; palmux2 just
	// degrades the UI). [AC-S3f2658-2-3]
	degraded       bool
	degradedReason string

	// initialSessionID is the session the FIRST spawn resumes (from DaemonConfig,
	// gated by the Manager on transcript existence). Immutable after construction.
	// initialResumeBad is set when a spawn that resumed initialSessionID died too
	// quickly to be a real session — thereafter respawnLoop stops resuming that
	// specific (bad) id and spawns fresh, so a broken transcript can't tight-loop.
	// spawnedAt / lastResumedSid record the most recent spawn for that judgement.
	// All guarded by stateMu.
	initialSessionID string
	initialResumeBad bool
	spawnedAt        time.Time
	lastResumedSid   string

	// lastCols/lastRows are the most recent client-requested terminal size,
	// recorded by Resize even if it arrives before a subprocess exists. Every
	// spawn (initial, crash respawn, or container-regenerate respawn) re-applies
	// them so a fresh PTY does not fall back to the 80x24 default while the client
	// — whose own dimensions are unchanged — sends no new resize. Without this,
	// claude re-renders its TUI at 80 cols ("narrow width" after Update container).
	// 0 means "never resized" → keep the spawn default. Guarded by stateMu.
	lastCols, lastRows uint16

	// exited is written when the ptyhost connection is lost (see
	// handleConnLost) and replaced on each re-spawn/re-attach. respawnLoop
	// reads the current value under stateMu at the start of each iteration.
	// Nil until the first spawn/attach.
	//
	// exited is a SINGLE-consumer channel — respawnLoop is its only reader.
	// Shutdown() must NOT also select on it: a buffered chan<-error delivers
	// its value to whichever waiting `case` fires first, so two simultaneous
	// receivers (Shutdown and respawnLoop both blocked in a select on the
	// same exited channel) would race for the one value, non-deterministically
	// starving whichever loses — observed as Shutdown() spuriously waiting out
	// its full timeout whenever respawnLoop happened to win the race. Shutdown
	// instead waits on connClosed (below), a close()-based broadcast that both
	// can safely observe.
	exited chan error
	// connClosed is closed (never sent-to) by handleConnLost as a broadcast
	// signal that the current ptyhost connection is confirmed gone — safe for
	// Shutdown() to wait on concurrently with respawnLoop's exited consumption
	// (close() wakes every waiter, unlike a single value send). Replaced on
	// each re-spawn/re-attach alongside exited.
	connClosed chan struct{}

	// spawnMu serialises concurrent EnsureStarted and respawn calls.
	spawnMu sync.Mutex
	spawned bool // true once a ptyhost has been successfully spawned/attached

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
	// PermissionModeFn returns the current claude --permission-mode value (global
	// setting, default "auto"). Read on each spawn so a settings change applies on
	// the next respawn. Nil → no --permission-mode flag is passed.
	PermissionModeFn func() string
	// Worktree is the absolute path the subprocess is spawned in (cmd.Dir).
	// When empty, the subprocess inherits the palmux2 server's cwd — which is
	// almost never the right answer for a per-branch tab. Provider /
	// Manager always pass the branch worktree path.
	Worktree string
	// RingSize is the palmux2-side ring buffer capacity in bytes (0 →
	// DefaultRingSize). Also used as the ptyhost-side ring size unless a
	// different scale is warranted later.
	RingSize int
	// ResumeOnDeath, when true, causes respawnLoop to re-spawn with
	// `--resume <lastSessionID>` after an unexpected subprocess exit.
	// Default is true.
	ResumeOnDeath bool
	// InitialSessionID, when non-empty, makes the FIRST spawn (EnsureStarted)
	// start with `--resume <id>` instead of a fresh session — so a palmux
	// restart (e.g. self-update) or any cold start re-attaches to the previous
	// conversation rather than dropping the user into a blank claude. The
	// Manager only sets this when the session's transcript still exists on disk
	// (stale-resume guard); a fast death of this resumed spawn additionally
	// falls back to a fresh session once (see respawnLoop) so a broken
	// transcript can't tight-loop. Empty → fresh first spawn (unchanged).
	InitialSessionID string
	// Logger is the slog logger to use (nil → slog.Default()).
	Logger *slog.Logger

	// NotifyHub is the notify hub used to publish OSC 52 clipboard events.
	// When nil the Emulator still runs but clipboard events are silently
	// discarded.  Main wires this from the process-wide hub.
	NotifyHub *notify.Hub
	// RepoID, BranchID, TabID identify the workspace and tab. They are
	// stamped onto every [notify.CopyEvent] emitted by the Emulator and
	// injected into the claude subprocess as PALMUX_* env so the Claude Code
	// hook handler can route notifications back to this tab. Also form the
	// ptyhost discovery seed (repoID__branchID__tabID).
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

	// S3f2658-2 (ADR-0002 thin holder): ptyhost wiring.
	//
	// PalmuxBin is the absolute path to the running palmux binary, re-invoked
	// as `<PalmuxBin> ptyhost ...` to spawn the detached process holder
	// (ADR-0003 cgroup-escape spawn — see internal/ptyhost). Production
	// (cmd/palmux/main.go) sets this to the same value as HookBinPath (it is
	// the same binary). Empty (the default for every existing test, which
	// does not set this field) makes the Daemon use an automatic in-process
	// ptyhost fallback instead of spawning a real detached OS process — still
	// the real ptyhost protocol/ring/spawn code, just not process-detached,
	// which keeps the test suite hermetic and fast. See ptyclient.go.
	PalmuxBin string
	// PtyHostLaunch overrides how a ptyhost is spawned/attached-to. Nil (the
	// common case) resolves to defaultLaunchPtyHost when PalmuxBin is set, or
	// inProcessLaunchPtyHost otherwise. Tests that need fine control over the
	// ptyhost lifecycle (e.g. pre-creating one to test the reconnect/restore
	// path, or a fake HELLO with a mismatched protocol version) set this
	// directly.
	PtyHostLaunch PtyHostLaunchFunc
	// RunDirOverride, when non-empty, replaces ptyhost.RunDir(instancePrefix)
	// as the directory this Daemon's ptyhost socket/status files live in.
	// Tests that want two separately-constructed Daemons to find the SAME
	// surviving ptyhost (simulating a palmux2 restart) set this to a shared
	// directory. Empty + PalmuxBin empty → an automatic per-Daemon-unique
	// temp directory (full test isolation, zero configuration needed).
	RunDirOverride string
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
	if cfg.PermissionModeFn == nil {
		cfg.PermissionModeFn = func() string { return "" }
	}
	d := &Daemon{
		claudeBin:            cfg.ClaudeBin,
		claudeArgs:           cfg.ClaudeArgs,
		permissionMode:       cfg.PermissionModeFn,
		worktree:             cfg.Worktree,
		resumeOnDeath:        cfg.ResumeOnDeath,
		initialSessionID:     cfg.InitialSessionID,
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
		palmuxBin:            cfg.PalmuxBin,
		instancePrefix:       domain.PalmuxSessionPrefix,
		ptyHostLaunch:        cfg.PtyHostLaunch,
		runDirOverride:       cfg.RunDirOverride,
		ptyHostRingSize:      cfg.RingSize,
		daemonCtx:            ctx,
		daemonCancel:         cancel,
		sessionIDReady:       make(chan struct{}),
		shutdownCh:           make(chan struct{}),
		roles:                newRoleCoordinator(cfg.Logger),
	}
	if d.ptyHostLaunch == nil {
		if d.palmuxBin != "" {
			d.ptyHostLaunch = defaultLaunchPtyHost
		} else {
			d.ptyHostLaunch = inProcessLaunchPtyHost
			if d.runDirOverride == "" {
				d.runDirOverride = autoTestRunDir()
			}
		}
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
// under feedMu so they line up at exactly one ptyhost-chunk boundary with the
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

// EnsureStarted lazily spawns-or-attaches on the first call.  Subsequent
// calls are no-ops.  Returns an error if the daemon is already in
// [StateShutdown] or if the spawn/attach fails.
//
// The ptyhost is spawned/attached under [Daemon.daemonCtx], not the caller's
// context (Fix 7 — daemonCtx isolation).
//
// On the first successful spawn/attach, EnsureStarted also starts the
// respawn loop goroutine.
func (d *Daemon) EnsureStarted(ctx context.Context) error {
	if State(d.state.Load()) == StateShutdown {
		return fmt.Errorf("claudetui daemon: already shut down")
	}
	d.spawnMu.Lock()
	defer d.spawnMu.Unlock()
	if d.spawned {
		return nil
	}
	// First spawn: resume the previous session when the Manager pre-seeded one
	// (its transcript still exists), so a palmux restart re-attaches to the prior
	// conversation instead of starting blank. Fresh (no --resume) otherwise.
	//
	// Note this is a completely separate concern from a SURVIVING ptyhost
	// (§3 of the design doc): if a ptyhost from a prior palmux2 lifetime is
	// still alive, spawnWithArgs attaches to it directly and these
	// --resume-computed args are simply never used for a spawn (see
	// launchAndAttach).
	initArgs := d.claudeArgs
	if d.initialSessionID != "" {
		initArgs = append(append([]string(nil), d.claudeArgs...), "--resume", d.initialSessionID)
		d.stateMu.Lock()
		d.lastResumedSid = d.initialSessionID
		d.stateMu.Unlock()
		d.logger.Info("claudetui: initial spawn resuming previous session", "session_id", d.initialSessionID)
	}
	if err := d.spawnWithArgs(initArgs); err != nil {
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

// ptyHostSeed is the discovery key used to compute this Daemon's ptyhost
// socket/status paths — repoID__branchID__tabID, per
// docs/no-halt-agent-design.md §3. Stable across respawns AND across a
// palmux2 restart (a freshly reconstructed Daemon after restart computes the
// SAME seed, letting launchAndAttach find the surviving ptyhost).
func (d *Daemon) ptyHostSeed() string {
	return d.repoID + "__" + d.branchID + "__" + d.tabID
}

// ptyHostPaths returns the socket and status file paths for this Daemon's
// ptyhost (deterministic — see ptyHostSeed).
func (d *Daemon) ptyHostPaths() (sockPath, statusPath string) {
	base := d.runDirOverride
	if base == "" {
		base = ptyhost.RunDir(d.instancePrefix)
	}
	seed := d.ptyHostSeed()
	// ptyhost.SocketPath/StatusPath hash the seed to a short, fixed-length
	// filename — a literal repoId__branchId__tabId can exceed the AF_UNIX
	// sun_path length limit (~108 bytes on Linux), especially with longer
	// test/CI-generated repo IDs or a tabId like "claude:claude". See
	// [ptyhost.FileKey]'s doc comment.
	return ptyhost.SocketPath(base, seed), ptyhost.StatusPath(base, seed)
}

// resolveClaudeBin resolves a bare command name (no path separator) to an
// absolute path via LookPath in PALMUX2's OWN process/environment, so
// argv[0] resolution is deterministic regardless of which process (this one,
// or a detached `palmux ptyhost`, possibly under a systemd --user session
// with a different PATH) ultimately execs it. A resolution failure or an
// already-qualified path is returned unchanged — ptyhost will surface the
// same "not found" error itself if it truly doesn't exist.
func resolveClaudeBin(bin string) string {
	if bin == "" || strings.ContainsRune(bin, filepath.Separator) {
		return bin
	}
	if resolved, err := exec.LookPath(bin); err == nil {
		return resolved
	}
	return bin
}

// spawnWithArgs assembles claude's full argv/env/cwd EXACTLY as before
// (hooks, --permission-mode, incus PTYCommander wrapper, --plugin-dir), then
// hands it to [Daemon.launchAndAttach] instead of starting a PTY directly
// (ADR-0002 — ptyhost is claude-agnostic; all this assembly stays here).
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
	} else {
		// Resolve a bare "claude" to an absolute path in PALMUX2's own PATH —
		// see resolveClaudeBin doc comment. In-container argv[0] is already
		// the fixed absolute containerClaudeBin, so this only applies host-side.
		claudeBin = resolveClaudeBin(claudeBin)
	}

	// Claude Code notification hooks + PALMUX_* env (also consumed by the
	// palmux-browser CLI inside the container). containerEnv carries the same
	// KEY=VALUE pairs for the incus --env path.
	var containerEnv []string
	hooksAvailable := hookBin != "" && notifyURL != ""
	// Always inject session-scoped settings via --settings: disableRemoteControl
	// is unconditional (no remote steering of a palmux-spawned session), and the
	// notification hooks are added when a notify endpoint is available. This never
	// touches the user's ~/.claude or the repo's .claude.
	if settings, err := buildClaudeSettings(hookBin, hooksAvailable); err != nil {
		d.logger.Warn("claudetui: failed to build claude settings", "err", err)
	} else {
		args = append([]string{"--settings", settings}, args...)
	}
	if hooksAvailable {
		for _, kv := range hookEnv(notifyURL, d.notifyToken, d.repoID, d.branchID, d.tabID) {
			env = appendOrReplace(env, kv) // host path
			containerEnv = append(containerEnv, kv)
		}
	}
	// Permission mode (global setting, default "auto"). The --permission-mode flag
	// overrides any defaultMode from settings files, so this is authoritative.
	if pm := d.permissionMode(); pm != "" {
		args = append([]string{"--permission-mode", pm}, args...)
	}

	// Build the OPAQUE argv/env/cwd handed to ptyhost (ADR-0002 — ptyhost
	// interprets none of this). daemonCtx (not a request ctx) bounds the
	// LAUNCH call only; the ptyhost itself outlives it by design.
	var argv, spawnEnv []string
	var cwd string
	if inContainer {
		// claude runs INSIDE the container: `incus exec -t <inst> --user … --cwd
		// <worktree> --env … -- /abs/claude <args>`. cwd + container env are
		// delivered through the incus flags baked into Args, not a separate
		// Cwd/Env — see incusRuntime.PTYCommand. The wrapper's own Path (already
		// LookPath-resolved by exec.CommandContext in PALMUX2's process) is used
		// as argv[0] for the same cross-process PATH-determinism reason as
		// resolveClaudeBin above.
		wrapperArgv := append([]string{claudeBin}, args...)
		cenv := append([]string{"TERM=xterm-256color"}, containerEnv...)
		cmd := pc.PTYCommand(d.daemonCtx, wrapperArgv, runtime.PTYCommandOpts{
			Cwd: d.worktree,
			Env: cenv,
		})
		argv = append([]string{cmd.Path}, cmd.Args[1:]...)
		spawnEnv = cmd.Env
		cwd = cmd.Dir
	} else {
		argv = append([]string{claudeBin}, args...)
		spawnEnv = env
		// Run in the branch worktree so `~/.claude/projects/<slug>` resolves to
		// the right repo. Empty cwd means inherit (tests without a worktree).
		cwd = d.worktree
	}

	conn, hello, reconnected, replay, err := d.launchAndAttach(argv, spawnEnv, cwd)
	if err != nil {
		return fmt.Errorf("ptyhost spawn: %w", err)
	}

	degraded := hello.ProtocolVersion != ptyhost.ProtocolVersion
	degradedReason := ""
	if degraded {
		degradedReason = "旧世代の agent ホスト — 再起動で新機能有効"
		d.logger.Warn("claudetui: ptyhost protocol version mismatch — degrading, NOT killing",
			"ptyhost_version", hello.ProtocolVersion, "expected_version", ptyhost.ProtocolVersion)
	}

	// Create fresh exited/connClosed channels for this spawn/attach (see
	// their doc comments on the Daemon struct for why there are two).
	exited := make(chan error, 1)
	connClosed := make(chan struct{})

	d.stateMu.Lock()
	// Close the previous conn before replacing it on respawn — mirrors the
	// old ptmx-close-on-respawn fix (avoids leaking an fd every crash+respawn
	// cycle until Shutdown).
	if d.conn != nil {
		_ = d.conn.Close()
	}
	d.conn = conn
	d.pid = hello.Pid
	d.exited = exited
	d.connClosed = connClosed
	d.degraded = degraded
	d.degradedReason = degradedReason
	d.spawnedAt = time.Now()
	d.state.Store(int32(StateRunning))
	// Snapshot the last client-requested size under the same lock so a resize
	// that raced this spawn is not lost. Applied below, outside the lock.
	cols, rows := d.lastCols, d.lastRows
	d.stateMu.Unlock()

	// Feed whatever the ptyhost already had buffered (the ATTACH replay) into
	// ring+emulator, atomically with the live readLoop about to start —
	// mirrors the old readLoop's per-chunk feedMu boundary. For a genuinely
	// fresh spawn this is normally empty (nothing written yet); for a
	// reconnect to a SURVIVING ptyhost (§5) this is the prior conversation's
	// recent output, replayed from as far back as the ring retained.
	if len(replay) > 0 {
		d.feedMu.Lock()
		if _, werr := d.ring.Write(replay); werr != nil {
			d.logger.Warn("claudetui: ring write (attach replay) error", "err", werr)
		}
		d.emulator.Feed(replay)
		d.feedMu.Unlock()
	}

	d.logger.Info("claudetui: ptyhost spawned/attached",
		"bin", d.claudeBin,
		"args", args,
		"pid", hello.Pid,
		"reconnected", reconnected,
		"degraded", degraded,
	)

	// Background read loop: ptyhost DATA → ring → broadcast. Must start
	// BEFORE the restore jiggle below so the jiggle-triggered repaint isn't
	// dropped on the floor with nobody reading it.
	d.shutdownWg.Add(1)
	go func() {
		defer d.shutdownWg.Done()
		d.readLoop(conn, exited, connClosed)
	}()

	// Background drainer: emulator → ptyhost INPUT (subprocess stdin).
	//
	// The vt.Emulator generates response bytes for some ANSI queries (DA1 / DA2
	// device attributes, cursor position report, etc.) by writing into an
	// internal io.Pipe. If we never drain that pipe its 64KiB buffer fills,
	// at which point the next response Write inside Emulator.Write blocks
	// **while still holding the SafeEmulator writer lock**. That deadlocks every
	// subsequent GridSnapshot caller (each reader-lock attempt waits forever).
	//
	// Forwarding the pipe output to the ptyhost as INPUT frames (subprocess
	// stdin) is the architecturally correct fix: claude asked the terminal a
	// question, the emulator answers, the answer goes back to claude.
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
			n, rerr := d.emulator.Read(buf)
			if rerr != nil {
				return
			}
			if n <= 0 {
				continue
			}
			if werr := d.writeFrame(ptyhost.MsgInput, ptyhost.EncodeInput(buf[:n])); werr != nil {
				return
			}
		}
	}()

	// Re-apply the last known client size to the freshly-attached ptyhost so a
	// respawn (crash recovery, or container regenerate) does not drop back to
	// the 80x24 spawn default. 0 means "never resized" → leave the default.
	// Fixes the "narrow width after Update container" symptom.
	if cols > 0 && rows > 0 {
		if err := d.writeFrame(ptyhost.MsgResize, ptyhost.EncodeResize(cols, rows)); err != nil {
			d.logger.Warn("claudetui: re-apply size on spawn failed", "err", err)
		} else {
			d.emulator.Resize(int(cols), int(rows))
		}
	}

	// §5 screen restore: ONLY for a genuine reconnect to a ptyhost that
	// survived a PRIOR palmux2 lifetime (never for a fresh spawn, and never
	// for an in-life crash-respawn — the old ptyhost is always fully torn
	// down before a crash-respawn's launchAndAttach runs, since "claude 終了
	// = ptyhost 終了" per ADR-0002, so reconnected is only ever true here in
	// the restart-recovery case). Replay was already fed above; this forces
	// claude to fully repaint so the replay's "OK to be temporarily messy"
	// partial history (docs/no-halt-agent-design.md §5) converges to a clean,
	// current screen.
	if reconnected {
		d.restoreScreenJiggle()
	}

	return nil
}

// launchAndAttach implements §3's reconnect-or-spawn decision: it first
// probes the deterministic socket path for a ptyhost that survived a PRIOR
// palmux2 lifetime (ADR-0001/0002) and attaches to it if found; only when
// nothing is listening there does it launch a brand new one. Either way it
// performs HELLO + ATTACH(-1) ("replay from the oldest byte still retained")
// before returning, so the caller always gets a live connection plus
// whatever replay bytes the ptyhost already had.
func (d *Daemon) launchAndAttach(argv, env []string, cwd string) (conn net.Conn, hello ptyhost.HelloPayload, reconnected bool, replay []byte, err error) {
	sockPath, statusPath := d.ptyHostPaths()

	if probeConn, ok := probeExisting(sockPath); ok {
		if h, herr := sendHello(probeConn); herr == nil {
			if data, aerr := sendAttach(probeConn, -1); aerr == nil {
				d.logger.Info("claudetui: attached to surviving ptyhost", "socket", sockPath, "pid", h.Pid)
				return probeConn, h, true, data, nil
			}
		}
		// The probe connected but HELLO/ATTACH failed — treat as no survivor
		// (stale/half-dead listener) and fall through to a fresh launch.
		_ = probeConn.Close()
	}

	req := PtyHostLaunchRequest{
		PalmuxBin:      d.palmuxBin,
		InstancePrefix: d.instancePrefix,
		Seed:           d.ptyHostSeed(),
		SocketPath:     sockPath,
		StatusPath:     statusPath,
		Argv:           argv,
		Env:            env,
		Cwd:            cwd,
		RingSize:       d.ptyHostRingSize,
	}
	if lerr := d.ptyHostLaunch(d.daemonCtx, req); lerr != nil {
		return nil, ptyhost.HelloPayload{}, false, nil, fmt.Errorf("launch: %w", lerr)
	}

	freshConn, derr := dialFresh(d.daemonCtx, sockPath, ptyHostDialTimeout)
	if derr != nil {
		return nil, ptyhost.HelloPayload{}, false, nil, fmt.Errorf("attach: %w", derr)
	}
	h, herr := sendHello(freshConn)
	if herr != nil {
		_ = freshConn.Close()
		return nil, ptyhost.HelloPayload{}, false, nil, fmt.Errorf("attach: %w", herr)
	}
	data, aerr := sendAttach(freshConn, -1)
	if aerr != nil {
		_ = freshConn.Close()
		return nil, ptyhost.HelloPayload{}, false, nil, fmt.Errorf("attach: %w", aerr)
	}
	return freshConn, h, false, data, nil
}

// restoreScreenJiggle sends RESIZE(cols, rows-1) then RESIZE(cols, rows),
// using the emulator's current dimensions, to force claude to fully repaint
// (docs/no-halt-agent-design.md §5's convergence mechanism for a mid-stream
// replay). A short settle window between the two resizes gives claude's
// SIGWINCH handler a chance to react to the shrink before it's undone.
func (d *Daemon) restoreScreenJiggle() {
	g := d.emulator.GridSnapshot()
	cols, rows := uint16(g.Cols), uint16(g.Rows)
	if cols == 0 || rows < 2 {
		return
	}
	if err := d.writeFrame(ptyhost.MsgResize, ptyhost.EncodeResize(cols, rows-1)); err != nil {
		d.logger.Warn("claudetui: screen restore jiggle (shrink) failed", "err", err)
		return
	}
	select {
	case <-time.After(50 * time.Millisecond):
	case <-d.shutdownCh:
		return
	}
	if err := d.writeFrame(ptyhost.MsgResize, ptyhost.EncodeResize(cols, rows)); err != nil {
		d.logger.Warn("claudetui: screen restore jiggle (restore) failed", "err", err)
		return
	}
	d.logger.Info("claudetui: screen restore jiggle sent", "cols", cols, "rows", rows)
}

// writeFrame writes one frame to the current ptyhost connection, serialized
// against other concurrent writers (see connWriteMu doc comment).
func (d *Daemon) writeFrame(t ptyhost.MsgType, payload []byte) error {
	d.stateMu.Lock()
	conn := d.conn
	d.stateMu.Unlock()
	if conn == nil {
		return fmt.Errorf("claudetui daemon: not connected")
	}
	d.connWriteMu.Lock()
	defer d.connWriteMu.Unlock()
	return ptyhost.WriteFrame(conn, t, payload)
}

// readLoop pumps DATA frames from the ptyhost connection into the ring
// buffer (which also broadcasts to live subscribers atomically — Fix 3 via
// Ring.Write) and the Emulator, until the connection errors/closes.
func (d *Daemon) readLoop(conn net.Conn, exited chan<- error, connClosed chan<- struct{}) {
	for {
		t, payload, err := ptyhost.ReadFrame(conn)
		if err != nil {
			d.handleConnLost(exited, connClosed)
			return
		}
		if t != ptyhost.MsgData {
			// Unsolicited non-DATA frame (e.g. a STATUS this connection
			// didn't request) — ignore; frozen-minimal protocol has nothing
			// else pushed unprompted besides DATA.
			continue
		}
		_, data, derr := ptyhost.DecodeData(payload)
		if derr != nil {
			d.logger.Warn("claudetui: decode DATA frame error", "err", derr)
			continue
		}
		chunk := make([]byte, len(data))
		copy(chunk, data)
		// feedMu makes (ring.Write + emulator.Feed) atomic w.r.t. a
		// concurrent attach, which takes the same lock around
		// (RenderSnapshot + Subscribe). This guarantees a new client's
		// rendered screen and its live subscription line up exactly at
		// one chunk boundary.
		d.feedMu.Lock()
		if _, werr := d.ring.Write(chunk); werr != nil {
			d.logger.Warn("claudetui: ring write error", "err", werr)
		}
		d.emulator.Feed(chunk)
		d.feedMu.Unlock()
	}
}

// handleConnLost is called when the ptyhost connection errors/closes (child
// exit, deliberate SHUTDOWN, or the ptyhost process itself dying). It
// consults the on-disk status file for a definitive exit record — by the
// time a NATURAL child-death connection close is observable, ptyhost has
// already written the final status (its teardown ordering guarantees
// waitChild() completes strictly before the post-exit-linger connection
// close; see internal/ptyhost/server.go) — and forwards the result to
// exited (respawnLoop's single-consumer signal), then closes connClosed
// (Shutdown's multi-consumer-safe broadcast signal — see the Daemon struct's
// doc comments on both fields for why these must be two separate channels).
func (d *Daemon) handleConnLost(exited chan<- error, connClosed chan<- struct{}) {
	var exitErr error
	_, statusPath := d.ptyHostPaths()
	if sf, err := ptyhost.ReadStatusFile(statusPath); err == nil {
		if !sf.Alive && sf.ExitCodeValid && sf.ExitCode != 0 {
			exitErr = fmt.Errorf("ptyhost: child exited with code %d", sf.ExitCode)
		}
		// !sf.Alive && !sf.ExitCodeValid, or a clean exit(0): exitErr stays
		// nil, matching the old "clean exit" (nil waitErr) case.
	} else {
		exitErr = fmt.Errorf("ptyhost: connection lost and status unavailable: %w", err)
	}
	select {
	case exited <- exitErr:
	default:
	}
	close(connClosed)
}

// respawnLoop runs as a background goroutine (started by EnsureStarted).
// It monitors the ptyhost connection by waiting on the exited channel.  On
// unexpected loss, it transitions to StateDead and — if ResumeOnDeath is
// true — waits for a session ID and re-spawns a NEW ptyhost with --resume
// <id> (Fix 4; ADR-0002 — respawn = spawn a new ptyhost, never re-exec in
// place).
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
		diedAt := time.Now()

		// If we are in StateShutdown, the exit was intentional — stop.
		if State(d.state.Load()) == StateShutdown {
			return
		}

		// Unexpected exit → transition to StateDead.
		d.stateMu.Lock()
		pid := d.pid
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
		// Bad-transcript guard: if the process that just died had resumed the
		// pre-seeded initialSessionID and lived only briefly, that transcript is
		// unusable (e.g. deleted/corrupt) — mark it bad so we stop resuming it and
		// spawn fresh instead. Bounds a broken-resume crash loop to one fast fail.
		if d.initialSessionID != "" && d.lastResumedSid == d.initialSessionID &&
			diedAt.Sub(d.spawnedAt) < resumeFallbackWindow {
			d.initialResumeBad = true
			d.logger.Warn("claudetui: resumed session died immediately; restarting fresh",
				"session_id", d.initialSessionID, "lifetime", diedAt.Sub(d.spawnedAt))
		}
		useResume := sid != "" && !(d.initialResumeBad && sid == d.initialSessionID)
		if useResume {
			d.lastResumedSid = sid
		} else {
			d.lastResumedSid = ""
		}
		d.stateMu.Unlock()

		// Build re-spawn args: base args, plus --resume <id> unless the id is the
		// known-bad initial session (then spawn fresh).
		respawnArgs := make([]string, 0, len(d.claudeArgs)+2)
		respawnArgs = append(respawnArgs, d.claudeArgs...)
		if useResume {
			respawnArgs = append(respawnArgs, "--resume", sid)
			d.logger.Info("claudetui: re-spawning with --resume", "session_id", sid, "args", respawnArgs)
		} else {
			d.logger.Info("claudetui: re-spawning fresh (no resume)", "args", respawnArgs)
		}

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

// resumeFallbackWindow bounds the "resumed session died too fast to be real"
// check. `claude --resume <bad-id>` prints an error and exits within a second or
// two; a genuinely-resumed session runs far longer before any unexpected crash.
// A spawn that resumed the pre-seeded initialSessionID and died inside this
// window is treated as a bad transcript → the next spawn drops --resume and
// starts fresh (respawnLoop), so a broken transcript can't tight-loop.
const resumeFallbackWindow = 8 * time.Second

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

// WriteInput writes bytes to the ptyhost as an INPUT frame (subprocess stdin).
func (d *Daemon) WriteInput(ctx context.Context, p []byte) error {
	if err := d.writeFrame(ptyhost.MsgInput, ptyhost.EncodeInput(p)); err != nil {
		return fmt.Errorf("claudetui daemon: %w", err)
	}
	return nil
}

// Resize propagates a terminal resize to the ptyhost (which applies it via
// SIGWINCH / TIOCSWINSZ to the PTY it holds) and keeps the headless
// [Emulator] grid in sync.
// Fully wired to the frontend in Story 3 via FitAddon events.
func (d *Daemon) Resize(cols, rows uint16) error {
	// Record the requested size under the same lock that guards conn, so it is
	// never lost to a race with spawnWithArgs: whichever runs second sees the
	// other's effect. If a ptyhost is attached, apply now; otherwise the pending
	// spawn will pick lastCols/lastRows up (fixes the "narrow width after Update
	// container" race where the client's reconnect POST /resize arrives before the
	// lazy EnsureStarted spawn has created the PTY).
	d.stateMu.Lock()
	d.lastCols, d.lastRows = cols, rows
	conn := d.conn
	d.stateMu.Unlock()
	if conn == nil {
		// Not started yet — size is remembered and applied on the next spawn.
		return nil
	}
	if err := d.writeFrame(ptyhost.MsgResize, ptyhost.EncodeResize(cols, rows)); err != nil {
		return fmt.Errorf("claudetui daemon: %w", err)
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
	pid := d.pid
	degraded := d.degraded
	degradedReason := d.degradedReason
	d.stateMu.Unlock()
	return Stats{
		PID:             pid,
		RingBytes:       d.ring.Len(),
		AttachedClients: d.attachedCount.Load(),
		Alive:           st == StateRunning,
		State:           st.String(),
		ScrollbackLines: d.emulator.ScrollbackLen(),
		Degraded:        degraded,
		DegradedReason:  degradedReason,
	}
}

// Shutdown performs a graceful, idempotent TEARDOWN (Fix 2 — sync.Once
// guard): the ptyhost holding this daemon's claude process is told to
// terminate its child and exit. Use this for an INTENTIONAL end-of-life —
// tab/branch close (Manager.CloseDaemon / CloseBranchDaemons) — where the
// user is deliberately ending the session.
//
// Do NOT call Shutdown from a palmux2 PROCESS-exit path (SIGTERM,
// self-update restart, `systemctl --user restart`) — that would defeat the
// entire point of ADR-0001/0002's ptyhost survival by killing every running
// claude on every restart. Use [Daemon.Detach] there instead.
//
//  1. Sets state to StateShutdown.
//  2. Cancels the daemon context (unblocks any in-flight launch/attach call).
//  3. Sends a SHUTDOWN request to the ptyhost (which owns SIGTERM→SIGKILL
//     escalation of the child it holds — ADR-0002).
//  4. Waits (bounded) for the connection loss to be observed via the exited
//     channel.
//  5. Closes the socket connection (unblocks readLoop's blocking read, in
//     case the ptyhost side hasn't already closed it).
//  6. Closes shutdownCh (unblocks respawnLoop and the emulator drainer).
//  7. Waits for all background goroutines via shutdownWg.
func (d *Daemon) Shutdown() {
	d.teardown(true)
}

// Detach disconnects from the ptyhost WITHOUT sending SHUTDOWN and WITHOUT
// reaping any in-container claude process — the ptyhost (and the claude
// process/incus-wrapper it holds) is deliberately left running so a FUTURE
// palmux2 process can reconnect to it (§3 of
// docs/no-halt-agent-design.md — this is the mechanism that makes a palmux2
// restart NOT kill the running claude). Use this from palmux2's own
// process-exit path (cmd/palmux/main.go's `<-ctx.Done()` handler covers both
// SIGTERM/SIGINT and a deliberate stop — ADR-0002 draws no distinction; both
// must leave surviving ptyhosts alone).
//
// Detach shares Shutdown's idempotent (sync.Once) guard and teardown
// mechanics (closing the LOCAL socket connection + goroutines) — it is the
// SAME local cleanup, just without telling the remote ptyhost to die.
func (d *Daemon) Detach() {
	d.teardown(false)
}

// teardown is the shared implementation behind Shutdown (killPtyhost=true)
// and Detach (killPtyhost=false). See both doc comments for when to use
// which.
func (d *Daemon) teardown(killPtyhost bool) {
	d.shutdownOnce.Do(func() {
		d.state.Store(int32(StateShutdown))
		d.daemonCancel()

		d.stateMu.Lock()
		conn := d.conn
		connClosed := d.connClosed
		d.stateMu.Unlock()

		if conn != nil && killPtyhost {
			d.logger.Info("claudetui: sending SHUTDOWN to ptyhost")
			if err := d.writeFrame(ptyhost.MsgShutdown, ptyhost.EncodeShutdown(ptyhost.ShutdownPayload{
				GraceMillis: int(gracefulShutdownTimeout / time.Millisecond),
			})); err != nil {
				d.logger.Warn("claudetui: send SHUTDOWN failed (ptyhost likely already gone)", "err", err)
			}
			if connClosed != nil {
				// Wait on connClosed (a close()-based broadcast), NOT exited —
				// respawnLoop is ALSO concurrently blocked waiting to receive
				// from exited at this point (it only learns to stop once it
				// observes StateShutdown, which happens after it wakes), so a
				// second simultaneous receiver on that single-value channel
				// would race respawnLoop for the one value and could starve
				// indefinitely (this exact race previously made Shutdown spin
				// its full timeout whenever respawnLoop won). See the struct
				// doc comments on exited/connClosed.
				select {
				case <-connClosed:
					// ptyhost confirmed (connection lost, status consulted)
				case <-time.After(gracefulShutdownTimeout + 3*time.Second):
					d.logger.Warn("claudetui: ptyhost did not confirm shutdown in time")
				}
			}
		}

		// S52fc2c-4: reap any lingering in-container claude process. Killing the
		// host-side incus exec wrapper does not always propagate SIGTERM into the
		// container child. We attempt an explicit pkill inside the container as
		// a best-effort cleanup. Runs after the ptyhost-side process is confirmed
		// dead/timed-out (above) so we don't race with the live process's own
		// cleanup. Skipped entirely for Detach — a surviving ptyhost's
		// in-container claude must keep running too.
		if killPtyhost && d.runtimeResolver != nil {
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

		// Closing shutdownCh unblocks respawnLoop, the readLoop-adjacent
		// jiggle wait, and the emulator drainer.
		close(d.shutdownCh)

		if conn != nil {
			// Always close OUR local connection — for Detach this simply
			// disconnects (the ptyhost keeps its child alive regardless; a
			// future palmux2 reconnects via launchAndAttach's survivor probe).
			_ = conn.Close()
		}

		// Close emulator BEFORE waiting for the wait group. The emulator
		// drainer goroutine blocks in emulator.Read(); closing the underlying
		// io.Pipe makes Read return io.EOF and the drainer exits. Without
		// this, shutdownWg.Wait would deadlock waiting for the drainer.
		d.emulator.Close()

		d.shutdownWg.Wait()
		if killPtyhost {
			d.logger.Info("claudetui: daemon shutdown complete")
		} else {
			d.logger.Info("claudetui: daemon detached (ptyhost left running for a future reconnect)")
		}
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
