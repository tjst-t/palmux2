package agenttui

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

	"github.com/tjst-t/palmux2/internal/agent"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/notify"
	"github.com/tjst-t/palmux2/internal/ptyhost"
	"github.com/tjst-t/palmux2/internal/runtime"
)

// S4d8b1c: when the workspace runtime can build an in-container PTY command
// (incus), claude runs INSIDE the container at these fixed paths.
const (
	// containerClaudeBin is the DEFAULT/FALLBACK in-container kill pattern
	// used only where no live Daemon (and therefore no adapter-supplied
	// [agent.SpawnSpec.KillPattern]) is available — orphan GC
	// (ptyhost_discovery.go's GCOrphans, which reaps a ptyhost left behind by
	// a prior palmux2 lifetime with no in-memory Daemon to ask). A live
	// Daemon's own reapContainerClaude calls (teardown / respawnLoop) use
	// d.killPattern instead — see the S0e8afb-2 graft note on that field.
	// S0e8afb-3 generalizes orphan GC too, once ptyhost.StatusFile itself
	// carries the spawning adapter's KillPattern (deferred — see
	// docs/sprint-logs/S0e8afb/verification-S0e8afb-2.md).
	//
	// This is also the literal string [agent.ClaudeAdapter.SpawnSpec] embeds
	// as its own KillPattern (internal/agent/claude.go's own copy of the same
	// constant) — the two are independent literals by design (ADR-0002:
	// ptyhost/agenttui must not need to import agent-specific knowledge), not
	// a shared symbol, so this comment is the enforcement of "keep them in
	// sync" until S0e8afb-3 removes the need entirely.
	containerClaudeBin = "/home/ubuntu/.local/bin/claude"
	// containerHookBinPath is where the running palmux binary is bind-mounted
	// inside the container, used as the `<bin> hook` command for in-container
	// claude (the host hookBinPath does not exist in the container). This is
	// agent-agnostic (S0e8afb-2 design doc: "every adapter's hook command is
	// invoked the same way") — daemon.go resolves it BEFORE building the
	// SpawnIntent, exactly as before the Adapter graft.
	containerHookBinPath = "/usr/local/bin/palmux"
)

const (
	// gracefulShutdownTimeout is the maximum time to wait for the ptyhost to
	// confirm the child has exited after a SHUTDOWN request before Daemon
	// gives up waiting (the ptyhost itself still owns the SIGTERM→SIGKILL
	// escalation — see ptyhost.Server.terminateChild).
	gracefulShutdownTimeout = 5 * time.Second
)

// reapContainerClaude best-effort TERMs any in-container claude process for
// the workspace identified by (repoID, branchID), via the workspace runtime's
// optional runtime.ContainerProcessKiller capability (S52fc2c-4 /
// S3f2658-4). Killing the host-side `incus exec` wrapper (the ptyhost's own
// SIGTERM→SIGKILL escalation of the process IT holds) does not always
// propagate the signal into the container child — see [runtime.
// ContainerProcessKiller]'s doc comment — so every SHUTDOWN trigger (tab
// close, branch close, orphan GC — S3f2658-4 wires ALL THREE through this
// same helper) must ALSO explicitly reap the in-container process.
//
// No-op when resolver is nil, resolves to nil (host runtime / no workspace
// runtime configured), does not implement ContainerProcessKiller, or pattern
// is empty (nothing to target — never pkill with an empty -f pattern, which
// on some platforms/args shapes can match far more than intended). Errors
// from the kill itself are logged at Debug, not returned: pkill exit 1 ("no
// matching process") is the common/expected case (S52fc2c-4
// [AC-S52fc2c-4-1]), and a genuinely unreachable/destroyed container is not
// a failure of the caller's own SHUTDOWN — the child is gone either way.
//
// pattern is the adapter-declared [agent.SpawnSpec.KillPattern] (S0e8afb-2 —
// previously this was unconditionally the hardcoded containerClaudeBin
// constant; callers now pass whichever pattern is available to them, see
// each call site's own comment).
func reapContainerClaude(resolver func(repoID, branchID string) runtime.PTYCommander, repoID, branchID, pattern string, timeout time.Duration, logger *slog.Logger) {
	if resolver == nil || pattern == "" {
		return
	}
	pc := resolver(repoID, branchID)
	if pc == nil {
		return
	}
	kk, ok := pc.(runtime.ContainerProcessKiller)
	if !ok {
		return
	}
	if logger == nil {
		logger = slog.Default()
	}
	kCtx, kCancel := context.WithTimeout(context.Background(), timeout)
	defer kCancel()
	if err := kk.KillContainerProcesses(kCtx, "TERM", pattern); err != nil {
		logger.Debug("agenttui: in-container claude reap (non-fatal)",
			"repo", repoID, "branch", branchID, "err", err)
	}
}

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
	// adapter builds the SpawnSpec (argv/env/kill-pattern) for every spawn —
	// S0e8afb-2 graft: replaces the inline claude arg-builder that used to
	// live directly in this file (hooks.go / claude_args.go's arg assembly).
	// Immutable after NewDaemon; the adapter itself may be internally
	// hot-swappable (agent.Configurable) for its own bin/args — see
	// Manager.SetClaudeBin/SetClaudeArgs.
	adapter agent.Adapter
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
	// runtimeStarter ensures the container is actually started before a
	// spawn uses runtimeResolver's result — see Config.RuntimeStarter's doc
	// comment.
	runtimeStarter func(ctx context.Context, repoID, branchID string)
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

	// stateMu guards conn, pid, sessionID, killPattern, degraded*, and
	// lastCols/lastRows.
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

	// killPattern is the most recent agent.SpawnSpec.KillPattern the adapter
	// returned (S0e8afb-2 graft — mirrors maultiagent's identically-named
	// field). Used by respawnLoop's pre-respawn reap and by Shutdown/teardown
	// to reap a lingering in-container process for THIS live Daemon. Empty
	// (before the first successful spawn) means "nothing to reap yet" —
	// reapContainerClaude no-ops on an empty pattern.
	killPattern string

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
	// Adapter builds the SpawnSpec (argv/env/hook wiring/kill pattern) for
	// every spawn (S0e8afb-2 graft — replaces the inline claude arg-builder
	// that used to live directly in spawnWithArgs). When nil, NewDaemon
	// defaults it to agent.NewClaudeAdapter(ClaudeBin, ClaudeArgs) — this
	// keeps every existing DaemonConfig{ClaudeBin: ..., ClaudeArgs: ...}
	// construction site (this Sprint's own test suite, none of which sets
	// Adapter directly) byte-behavior-identical without a mechanical
	// call-site rewrite. [Manager] always sets Adapter explicitly (see
	// ManagerConfig.Adapter's doc comment) so every daemon it creates shares
	// ONE adapter instance — required for Manager.SetClaudeBin/SetClaudeArgs
	// hot-swap (agent.Configurable) to actually reach already-spawned
	// daemons, not just future ones.
	Adapter agent.Adapter
	// ClaudeBin is the path to the claude binary (default: "claude"). Only
	// consulted when Adapter is nil — see Adapter's doc comment.
	ClaudeBin string
	// ClaudeArgs are additional arguments passed to claude on every spawn.
	// Only consulted when Adapter is nil — see Adapter's doc comment.
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
	// RuntimeStarter is called before every spawn that will use
	// RuntimeResolver's result, ensuring an incus-container runtime is
	// actually running (not just resolved) before an in-container spawn is
	// attempted. RuntimeResolver itself stays side-effect-free — it's also
	// used by orphan-GC's reap path, which must never resurrect a stopped
	// container just to probe it. nil disables this (tests / host-only
	// setups).
	RuntimeStarter func(ctx context.Context, repoID, branchID string)

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
	// which keeps the test suite hermetic and fast. See agenttui/ptyclient.go.
	PalmuxBin string
	// PtyHostLaunch overrides how a ptyhost is spawned/attached-to. Nil (the
	// common case) resolves to DefaultLaunchPtyHost when PalmuxBin
	// is set, or InProcessLaunchPtyHost otherwise. Tests that need
	// fine control over the ptyhost lifecycle (e.g. pre-creating one to test
	// the reconnect/restore path, or a fake HELLO with a mismatched protocol
	// version) set this directly.
	PtyHostLaunch PtyHostLaunchFunc
	// RunDirOverride, when non-empty, replaces ptyhost.RunDir(instancePrefix)
	// as the directory this Daemon's ptyhost socket/status files live in.
	// Tests that want two separately-constructed Daemons to find the SAME
	// surviving ptyhost (simulating a palmux2 restart) set this to a shared
	// directory. Empty + PalmuxBin empty → an automatic per-Daemon-unique
	// temp directory (full test isolation, zero configuration needed).
	RunDirOverride string
	// InstancePrefix (S3f2658-3) overrides domain.PalmuxSessionPrefix as the
	// source of instance isolation for this Daemon's ptyhost run
	// directory/scope-unit name. Empty (every existing caller, including
	// production main.go) falls back to the global domain.PalmuxSessionPrefix
	// — unchanged behavior. Tests that need TWO independently-instance-scoped
	// Managers/Daemons live in the SAME process (where domain.PalmuxSessionPrefix,
	// a package var, cannot hold two values at once) set this directly to
	// simulate "host palmux2" vs "INSTANCE=dev palmux2" without needing two
	// real OS processes for every test (see AC-S3f2658-3-3).
	InstancePrefix string
}

// NewDaemon creates a Daemon from cfg.  No subprocess is spawned yet.
//
// The Emulator is created immediately at default 80×24 (VT-100 default); it
// will be resized when the first WebSocket client calls [Daemon.Resize].
func NewDaemon(cfg DaemonConfig) *Daemon {
	if cfg.Adapter == nil {
		bin := cfg.ClaudeBin
		if bin == "" {
			bin = "claude"
		}
		cfg.Adapter = agent.NewClaudeAdapter(bin, cfg.ClaudeArgs)
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
	instancePrefix := cfg.InstancePrefix
	if instancePrefix == "" {
		instancePrefix = domain.PalmuxSessionPrefix
	}
	d := &Daemon{
		adapter:              cfg.Adapter,
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
		runtimeStarter:       cfg.RuntimeStarter,
		notifyURLInContainer: cfg.NotifyURLInContainer,
		palmuxBin:            cfg.PalmuxBin,
		instancePrefix:       instancePrefix,
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
			d.ptyHostLaunch = DefaultLaunchPtyHost
		} else {
			d.ptyHostLaunch = InProcessLaunchPtyHost
			if d.runDirOverride == "" {
				d.runDirOverride = AutoTestRunDir()
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
//
// Sfeed64-1 [race fix]: held under feedMu, matching every other emulator
// touch (readLoop's Feed, the ATTACH-replay Feed, RenderSnapshotAndSubscribe).
// vt.SafeEmulator.CellAt returns a *uv.Cell POINTER into its own live buffer
// under RLock, but the lock is released the instant CellAt returns — a
// concurrent Feed (write-locked) can then mutate that same cell's fields
// while [cellToGridCell] is still reading them, a data race go test -race
// catches under realistic (fast, high-throughput) traffic such as
// reattach_deadlock_test.go's >64KiB replay + a concurrent live readLoop
// (neither existed before Sfeed64-1's regression test — this race was
// latent and un-triggered by any prior test's much lower throughput).
// feedMu does not protect the SafeEmulator's OWN internal pointer-return API
// in general, but it DOES serialize every palmux2-side Feed call against
// every palmux2-side GridSnapshot call, which is sufficient here: the only
// concurrent mutator of emulator state in this codebase is Feed, and every
// Feed call site already holds feedMu.
func (d *Daemon) GridSnapshot() Grid {
	d.feedMu.Lock()
	defer d.feedMu.Unlock()
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
		return fmt.Errorf("agenttui daemon: already shut down")
	}

	// Sc4f091-2 review fix: peek d.spawned BEFORE doing the (up to
	// sharedPathsReadyBudget) in-container readiness wait, so a call after
	// the daemon is already spawned — the overwhelmingly common case,
	// EnsureStarted is meant to be a near-instant no-op then — never pays
	// that cost. The wait itself runs OUTSIDE spawnMu (preSpawnWaitFor
	// SharedPaths) precisely so a genuine first-spawn's bounded wait can
	// never serialize some OTHER concurrent EnsureStarted/respawn call on
	// this Daemon behind it — previously the wait ran INSIDE spawnWithArgs
	// while spawnMu was held, which is exactly that avoidable regression.
	// The second d.spawned check below (once spawnMu IS held) closes the
	// harmless race where two callers both peek spawned==false and both
	// wait: only the first to actually acquire the lock spawns, the other
	// just returns nil having paid a wasted (but still bounded) wait.
	d.spawnMu.Lock()
	alreadySpawned := d.spawned
	d.spawnMu.Unlock()
	if !alreadySpawned {
		d.preSpawnWaitForSharedPaths()
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
	// still alive, spawnWithArgs attaches to it directly and this
	// --resume-computed session id is simply never used for a spawn (see
	// launchAndAttach).
	//
	// S0e8afb-2 graft: spawnWithArgs now takes (resumeSessionID, isRespawn) —
	// the Adapter (not this call site) appends "--resume <id>" when
	// resumeSessionID is non-empty (see agent.ClaudeAdapter.SpawnSpec), so
	// this is a resumeID/isRespawn CALL, not an argv slice.
	resumeID := ""
	if d.initialSessionID != "" {
		resumeID = d.initialSessionID
		d.stateMu.Lock()
		d.lastResumedSid = d.initialSessionID
		d.stateMu.Unlock()
		d.logger.Info("agenttui: initial spawn resuming previous session", "session_id", d.initialSessionID)
	}
	if err := d.spawnWithArgs(resumeID, false); err != nil {
		return fmt.Errorf("agenttui daemon: spawn: %w", err)
	}
	d.spawned = true

	// Start the respawn/monitor loop now that we have a live exited channel.
	// The loop is started under spawnMu to guarantee it sees the correct
	// d.exited value set by spawnWithArgs above.
	d.shutdownWg.Add(1)
	go d.respawnLoop()

	return nil
}

// ptyHostSeedFor computes the ptyhost discovery seed for a tab. The seed
// determines this Daemon's ptyhost socket/status file paths (it is hashed to
// a short, fixed-length filename — see ptyHostPaths / [ptyhost.FileKey]) and
// must be STABLE across respawns AND across a palmux2 restart, so a freshly
// reconstructed Daemon re-finds its surviving ptyhost (no-halt-agent design
// §3).
//
// Sfa2bab fix — the seed now includes the agent kind for every NON-claude
// kind. Previously the seed was repoID__branchID__tabID with no kind, so two
// Managers of DIFFERENT kinds derived the SAME socket path for the same
// tabID. The claude-tui service Provider's bare /tabs/{tabId}/tui/attach route
// accepts ANY tabId — including a codex/opencode tab's id ("codex:codex") —
// and would create a claude-kind ptyhost at that shared path; the codex/
// opencode Manager's adopt path (launchAndAttach) then latched onto it kind-
// blind, so the codex tab silently ran claude and stuck that way across
// restarts (the seed round-tripped, so re-adoption kept picking the wrong
// process). Appending the kind for non-claude adapters separates their socket
// path from the claude bare route's, so a codex/opencode Daemon can never
// share a ptyhost with a claude one.
//
// claude itself keeps the historical suffix-less seed so already-running
// claude ptyhosts are re-adopted byte-for-byte on the upgrade to this build —
// no forced respawn of every live claude, honouring ADR-0001/0002's "a
// palmux2 restart must not take down running claude". This is sufficient: two
// DIFFERENT non-claude kinds never share a tabId (a codex tab's id is always
// "codex:*", an opencode tab's "opencode:*"), so the only cross-kind path
// collision that ever existed was claude-bare-route vs the owning kind, which
// this closes.
//
// The seed is OPAQUE — it is hashed to a filename and never parsed back into
// its parts (discovery/GC use the ptyhost status file's explicit
// RepoID/BranchID/TabID/AgentKind fields, not a split of Seed — see
// discover.go / ptyhost/server.go), so appending "__"+kind is safe even
// though domain IDs may themselves contain "__".
func ptyHostSeedFor(repoID, branchID, tabID string, kind agent.Kind) string {
	seed := repoID + "__" + branchID + "__" + tabID
	if kind != agent.KindClaude {
		seed += "__" + string(kind)
	}
	return seed
}

// ptyHostSeed is the discovery key used to compute this Daemon's ptyhost
// socket/status paths. See [ptyHostSeedFor] for the format and the Sfa2bab
// cross-kind-collision fix.
func (d *Daemon) ptyHostSeed() string {
	return ptyHostSeedFor(d.repoID, d.branchID, d.tabID, d.adapter.Kind())
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

// sharedPathsReadyBudget/sharedPathsReadyPoll bound waitForSharedPaths (S
// c4f091-2): long enough to ride out one in-flight reconcile tick's
// remove-then-add window (observed to complete well under a second per
// device in this Sprint's live reproduction), short enough that a genuinely
// broken/never-converging profile does not meaningfully delay every agent
// spawn — after the budget it always proceeds anyway (fail-open).
const (
	sharedPathsReadyBudget = 5 * time.Second
	sharedPathsReadyPoll   = 200 * time.Millisecond
)

// waitForSharedPaths polls checker.PathsReady(paths) for up to
// sharedPathsReadyBudget before returning — it NEVER returns an error and
// NEVER blocks past the budget: this is a best-effort pre-flight nudge (see
// runtime.SharedPathChecker's doc comment), not a gate that can fail the
// spawn. Mirrors tests/acceptance/s2b5691_codex_opencode_incontainer.py's own
// wait_for_agent_share helper, now applied on the production spawn path
// instead of only in the test harness.
func waitForSharedPaths(ctx context.Context, checker runtime.SharedPathChecker, paths []string, logger *slog.Logger) {
	deadline := time.Now().Add(sharedPathsReadyBudget)
	attempts := 0
	for {
		attempts++
		ready, err := checker.PathsReady(ctx, paths)
		if err == nil && ready {
			return
		}
		if time.Now().After(deadline) {
			if logger != nil {
				logger.Warn("agenttui: in-container shared paths not confirmed ready before spawn budget — spawning anyway (fail-open)",
					"attempts", attempts, "paths", paths, "err", err)
			}
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(sharedPathsReadyPoll):
		}
	}
}

// spawnWithArgs asks the Adapter (S0e8afb-2 graft) to build a SpawnSpec for a
// fresh spawn (resumeSessionID == "") or a resume (resumeSessionID == the
// session to resume), then hands the resulting argv/env/cwd to
// [Daemon.launchAndAttach] instead of starting a PTY directly (ADR-0002 —
// ptyhost is claude-agnostic; this method is where "who builds the argv" and
// "who executes it" meet).
//
// Everything BELOW the SpawnSpec/argv assembly (launchAndAttach, the
// replay-drainer-before-Feed ordering, readLoop, restoreScreenJiggle) is
// UNCHANGED from before this graft — see docs/agenttui-ptyhost-merge-design.md
// §"P2 — graft seam" risk 1: this ordering is the v0.14.12 reattach-deadlock
// P0 fix and must never be reordered relative to where it already sits.
//
// isRespawn is threaded into the SpawnIntent as agent.SpawnIntent.IsRespawn:
// false from EnsureStarted's first-ever spawn, true from every
// respawnLoop-driven re-spawn (crash recovery or container regenerate).
//
// preSpawnWaitForSharedPaths resolves the in-container runtime and, when the
// adapter needs container-shared paths (codex/opencode's InContainerProvider),
// waits (bounded, fail-open — see waitForSharedPaths) for them to be ready.
//
// MUST be called WITHOUT holding spawnMu (Sc4f091-2 review fix): this can
// take up to sharedPathsReadyBudget, and doing that while holding spawnMu
// would needlessly serialize any OTHER concurrent EnsureStarted/respawnLoop
// call on this SAME Daemon behind an unrelated (from that caller's
// perspective) readiness wait. Both callers (EnsureStarted, respawnLoop) call
// this BEFORE acquiring spawnMu, then acquire it as before to actually spawn.
//
// Safe to call without any lock: d.adapter and d.runtimeResolver are set once
// at construction and never mutated afterward (immutable for the Daemon's
// lifetime — see NewDaemon). Calling d.runtimeStarter here is idempotent
// (starting an already-started runtime is a no-op); spawnWithArgs re-resolves
// the same runtime handle itself (harmless, cheap — a map lookup, not
// repeated I/O) once it actually holds spawnMu, so this function only ever
// ADDS a bounded wait in front of the real spawn, never skips or duplicates
// spawn logic.
func (d *Daemon) preSpawnWaitForSharedPaths() {
	if d.runtimeStarter != nil {
		d.runtimeStarter(d.daemonCtx, d.repoID, d.branchID)
	}
	if d.runtimeResolver == nil {
		return
	}
	pc := d.runtimeResolver(d.repoID, d.branchID)
	if pc == nil {
		return // host runtime (or unresolved) — no shared-profile window to wait on
	}
	icp, ok := d.adapter.(agent.InContainerProvider)
	if !ok {
		return
	}
	paths := icp.SharedContainerPaths()
	if len(paths) == 0 {
		return
	}
	spc, ok := pc.(runtime.SharedPathChecker)
	if !ok {
		return
	}
	waitForSharedPaths(d.daemonCtx, spc, paths, d.logger)
}

// MUST be called while holding spawnMu.
func (d *Daemon) spawnWithArgs(resumeSessionID string, isRespawn bool) error {
	if d.adapter == nil {
		return fmt.Errorf("agenttui daemon: no adapter configured")
	}

	// S4d8b1c: resolve the workspace runtime. When it can build an in-container
	// PTY command (incus), claude runs INSIDE the container; otherwise host.
	// Ensure it's actually started FIRST — resolving alone never starts it
	// (see Config.RuntimeStarter's doc comment; this is the "Instance not
	// found" fix). Uses daemonCtx, not a caller-supplied ctx (Fix 7 daemonCtx
	// isolation — the container's lifecycle must outlive any single request).
	if d.runtimeStarter != nil {
		d.runtimeStarter(d.daemonCtx, d.repoID, d.branchID)
	}
	var pc runtime.PTYCommander
	if d.runtimeResolver != nil {
		pc = d.runtimeResolver(d.repoID, d.branchID)
	}
	inContainer := pc != nil

	// Sc4f091-2: the pre-spawn shared-paths readiness wait used to live HERE,
	// but that ran it while the caller (EnsureStarted/respawnLoop) held
	// spawnMu — an up-to-5s bounded wait is fine for THIS spawn, but holding
	// spawnMu for it needlessly serializes any OTHER concurrent caller of
	// EnsureStarted/respawnLoop on the SAME Daemon behind it (review finding).
	// The wait now happens in preSpawnWaitForSharedPaths, called by BOTH
	// callers BEFORE they acquire spawnMu (see that function's doc comment)
	// — spawnWithArgs itself no longer waits, it only resolves pc/inContainer
	// (needed for argv assembly below) exactly as before.

	// Runtime-aware hook/notify resolution — main's logic, preserved
	// VERBATIM by the S0e8afb-2 graft (design doc: "notify URL/hook-bin の
	// host-vs-container 解決 は main のロジック維持 → SpawnIntent.Hook に渡す").
	// This is agent-agnostic (every adapter's hook command is invoked the
	// same way, just at a different host-vs-container path), so it stays
	// HERE rather than moving into the Adapter.
	hookBin := d.hookBinPath
	notifyURL := d.notifyURL
	if inContainer {
		hookBin = containerHookBinPath
		// Inside the container, 127.0.0.1 is the container itself — the host
		// notifyURL is unusable. Use the bridge-gateway URL; if it's unknown
		// (e.g. wildcard --addr / no incus bridge) leave it empty so the hook is
		// skipped entirely rather than injecting a URL that always fails.
		notifyURL = d.notifyURLInContainer
	}

	intent := agent.SpawnIntent{
		Worktree:        d.worktree,
		ResumeSessionID: resumeSessionID,
		InContainer:     inContainer,
		Hook: agent.HookEnv{
			NotifyURL:   notifyURL,
			Token:       d.notifyToken,
			RepoID:      d.repoID,
			BranchID:    d.branchID,
			TabID:       d.tabID,
			HookBinPath: hookBin,
		},
		PermissionMode: d.permissionMode(),
		IsRespawn:      isRespawn,
	}

	spec, err := d.adapter.SpawnSpec(intent)
	if err != nil {
		return fmt.Errorf("agenttui daemon: build spawn spec: %w", err)
	}
	if len(spec.Argv) == 0 {
		return fmt.Errorf("agenttui daemon: adapter returned empty argv")
	}
	if err := writeFileDrops(spec.PreFiles); err != nil {
		d.logger.Warn("agenttui: pre-spawn file drop failed", "err", err)
	}

	d.stateMu.Lock()
	d.killPattern = spec.KillPattern
	d.stateMu.Unlock()

	// Build the OPAQUE argv/env/cwd handed to ptyhost (ADR-0002 — ptyhost
	// interprets none of this). daemonCtx (not a request ctx) bounds the
	// LAUNCH call only; the ptyhost itself outlives it by design.
	var argv, spawnEnv []string
	var cwd string
	if inContainer {
		// claude runs INSIDE the container: `incus exec -t <inst> --user … --cwd
		// <worktree> --env … -- /abs/claude <args>`. cwd + container env are
		// delivered through the incus flags baked into Args, not a separate
		// Cwd/Env — see incusRuntime.PTYCommand. spec.Argv is already the full
		// [containerClaudeBin, ...args] the adapter built for the in-container
		// case (agent.ClaudeAdapter.SpawnSpec's InContainer branch) — this is
		// the design doc's literal "container: pc.PTYCommand(daemonCtx,
		// spec.Argv, opts)".
		cenv := append([]string{"TERM=xterm-256color"}, spec.Env...)
		cmd := pc.PTYCommand(d.daemonCtx, spec.Argv, runtime.PTYCommandOpts{
			Cwd: d.worktree,
			Env: cenv,
		})
		argv = append([]string{cmd.Path}, cmd.Args[1:]...)
		spawnEnv = cmd.Env
		cwd = cmd.Dir
	} else {
		env := appendOrReplace(os.Environ(), "TERM=xterm-256color")
		for _, kv := range spec.Env {
			env = appendOrReplace(env, kv)
		}
		// Resolve a bare "claude" to an absolute path in PALMUX2's own PATH —
		// see resolveClaudeBin doc comment. This is deliberately applied HERE
		// (agent-agnostic, ptyhost-architecture-specific), not inside the
		// Adapter: the rationale (argv[0] must be resolved in PALMUX2's OWN
		// process, since a detached `palmux ptyhost` — possibly under a
		// systemd --user session with a different PATH — is what ultimately
		// execs it) applies to ANY host-side adapter binary using this
		// ptyhost-based Daemon, not something specific to claude. In-container
		// argv[0] is already the fixed absolute containerClaudeBin (set by the
		// adapter), so this only applies host-side — same as before the graft.
		argv = append([]string{resolveClaudeBin(spec.Argv[0])}, spec.Argv[1:]...)
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
		d.logger.Warn("agenttui: ptyhost protocol version mismatch — degrading, NOT killing",
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

	// Background drainer: emulator → ptyhost INPUT (subprocess stdin).
	//
	// The vt.Emulator generates response bytes for some ANSI queries (DA1 / DA2
	// device attributes, cursor position report, etc.) by writing into an
	// UNBUFFERED internal io.Pipe (Emulator.pw — see
	// third_party/charmbracelet-x-vt-racefix/emulator.go). io.Pipe has ZERO
	// buffering: a single response byte written with nobody reading the pipe
	// blocks the writer immediately — and it blocks inside Emulator.Write
	// **while still holding the SafeEmulator writer lock**. That deadlocks every
	// subsequent GridSnapshot caller (each reader-lock attempt waits forever).
	//
	// CRITICAL ORDERING (v0.14.12 reattach startup-deadlock fix): this drainer
	// MUST start BEFORE the ATTACH-replay Feed below. On a reconnect to a
	// SURVIVING ptyhost the replay can be a full ring of prior output, whose
	// embedded ANSI queries make Feed generate responses that (with no drainer
	// reading the unbuffered pipe yet) block on the very first response byte —
	// so Feed(replay) itself blocks (holding the writer lock), wedging the whole
	// startup goroutine (it runs under EnsureStarted's spawnMu) so the server
	// never reaches ListenAndServe. A fresh spawn has an empty replay, which is
	// why only reconnects deadlocked. (The fix's correctness relies on this
	// unbuffered semantics: there is no buffer to "not fill" — the drainer must
	// simply be running before ANY query-answering Feed.)
	//
	// Responses generated WHILE the replay is fed answer REPLAYED (historical)
	// queries that the real terminal already answered when they first happened;
	// re-sending them to claude would inject spurious input, so we drain-and-
	// DISCARD until replayFed closes, then forward live responses as INPUT frames
	// (the architecturally correct path: claude asks, the emulator answers, the
	// answer goes back to claude).
	replayFed := make(chan struct{})
	d.shutdownWg.Add(1)
	go func() {
		defer d.shutdownWg.Done()
		buf := make([]byte, 4096)
		forwarding := false
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
			if !forwarding {
				select {
				case <-replayFed:
					forwarding = true
				default:
				}
			}
			if !forwarding {
				continue // discard responses to replayed (historical) queries
			}
			if werr := d.writeFrame(ptyhost.MsgInput, ptyhost.EncodeInput(buf[:n])); werr != nil {
				return
			}
		}
	}()

	// Feed whatever the ptyhost already had buffered (the ATTACH replay) into
	// ring+emulator, atomically with the live readLoop about to start —
	// mirrors the old readLoop's per-chunk feedMu boundary. For a genuinely
	// fresh spawn this is normally empty (nothing written yet); for a
	// reconnect to a SURVIVING ptyhost (§5) this is the prior conversation's
	// recent output, replayed from as far back as the ring retained. The
	// drainer started above keeps Emulator.Feed from blocking on a full
	// response pipe (see its CRITICAL ORDERING note).
	if len(replay) > 0 {
		d.feedMu.Lock()
		if _, werr := d.ring.Write(replay); werr != nil {
			d.logger.Warn("agenttui: ring write (attach replay) error", "err", werr)
		}
		d.emulator.Feed(replay)
		d.feedMu.Unlock()
	}
	close(replayFed) // replay drained & applied → forward live responses hereafter

	d.logger.Info("agenttui: ptyhost spawned/attached",
		"argv", spec.Argv,
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

	// Re-apply the last known client size to the freshly-attached ptyhost so a
	// respawn (crash recovery, or container regenerate) does not drop back to
	// the 80x24 spawn default. 0 means "never resized" → leave the default.
	// Fixes the "narrow width after Update container" symptom.
	if cols > 0 && rows > 0 {
		if err := d.writeFrame(ptyhost.MsgResize, ptyhost.EncodeResize(cols, rows)); err != nil {
			d.logger.Warn("agenttui: re-apply size on spawn failed", "err", err)
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

// writeFileDrops writes any agent.FileDrop entries an Adapter declared in a
// SpawnSpec before the subprocess is launched (e.g. a generated config file
// for a user-defined agent). Unused by the built-in Claude adapter today
// (ClaudeAdapter.SpawnSpec never populates PreFiles); a no-op when drops is
// empty. Verbatim behavior from maultiagent's identically-named helper.
func writeFileDrops(drops []agent.FileDrop) error {
	for _, fd := range drops {
		if fd.Path == "" {
			continue
		}
		mode := fd.Mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.MkdirAll(filepath.Dir(fd.Path), 0o755); err != nil {
			return fmt.Errorf("mkdir for file drop %s: %w", fd.Path, err)
		}
		if err := os.WriteFile(fd.Path, fd.Content, mode); err != nil {
			return fmt.Errorf("write file drop %s: %w", fd.Path, err)
		}
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

	if probeConn, ok := ProbeExisting(sockPath); ok {
		if h, herr := SendHello(probeConn); herr == nil {
			if data, aerr := SendAttach(probeConn, -1); aerr == nil {
				d.logger.Info("agenttui: attached to surviving ptyhost", "socket", sockPath, "pid", h.Pid)
				return probeConn, h, true, data, nil
			}
		}
		// The probe connected but HELLO/ATTACH failed — treat as no survivor
		// (stale/half-dead listener) and fall through to a fresh launch.
		_ = probeConn.Close()
	}

	// S0e8afb-3: AgentKind/KillPattern are the per-kind ownership marker +
	// orphan-GC kill pattern, written into the ptyhost status file so a
	// PER-KIND Manager's discovery/GC (discover.go's ScanRunDir) never
	// dials/adopts another kind's ptyhost sharing this same run dir — see
	// ptyhost.Config.AgentKind's doc comment. d.killPattern was already set
	// (under stateMu, by THIS same goroutine, immediately before the
	// spawnWithArgs call that led here) — reading it here without re-taking
	// the lock is safe: no other goroutine writes it, only spawnWithArgs
	// does, and this is the same call chain.
	req := PtyHostLaunchRequest{
		PalmuxBin:      d.palmuxBin,
		InstancePrefix: d.instancePrefix,
		Seed:           d.ptyHostSeed(),
		RepoID:         d.repoID,
		BranchID:       d.branchID,
		TabID:          d.tabID,
		AgentKind:      string(d.adapter.Kind()),
		KillPattern:    d.killPattern,
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

	freshConn, derr := DialFresh(d.daemonCtx, sockPath, PtyHostDialTimeout)
	if derr != nil {
		return nil, ptyhost.HelloPayload{}, false, nil, fmt.Errorf("attach: %w", derr)
	}
	h, herr := SendHello(freshConn)
	if herr != nil {
		_ = freshConn.Close()
		return nil, ptyhost.HelloPayload{}, false, nil, fmt.Errorf("attach: %w", herr)
	}
	data, aerr := SendAttach(freshConn, -1)
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
	// Sfeed64-1 [race fix]: go through d.GridSnapshot() (feedMu-guarded), not
	// d.emulator.GridSnapshot() directly — see that method's doc comment.
	g := d.GridSnapshot()
	cols, rows := uint16(g.Cols), uint16(g.Rows)
	if cols == 0 || rows < 2 {
		return
	}
	if err := d.writeFrame(ptyhost.MsgResize, ptyhost.EncodeResize(cols, rows-1)); err != nil {
		d.logger.Warn("agenttui: screen restore jiggle (shrink) failed", "err", err)
		return
	}
	select {
	case <-time.After(50 * time.Millisecond):
	case <-d.shutdownCh:
		return
	}
	if err := d.writeFrame(ptyhost.MsgResize, ptyhost.EncodeResize(cols, rows)); err != nil {
		d.logger.Warn("agenttui: screen restore jiggle (restore) failed", "err", err)
		return
	}
	d.logger.Info("agenttui: screen restore jiggle sent", "cols", cols, "rows", rows)
}

// writeFrame writes one frame to the current ptyhost connection, serialized
// against other concurrent writers (see connWriteMu doc comment).
func (d *Daemon) writeFrame(t ptyhost.MsgType, payload []byte) error {
	d.stateMu.Lock()
	conn := d.conn
	d.stateMu.Unlock()
	if conn == nil {
		return fmt.Errorf("agenttui daemon: not connected")
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
			d.logger.Warn("agenttui: decode DATA frame error", "err", derr)
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
			d.logger.Warn("agenttui: ring write error", "err", werr)
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
		d.logger.Warn("agenttui: subprocess died unexpectedly",
			"pid", pid,
			"err", exitErr,
		)

		if !d.resumeOnDeath {
			d.logger.Info("agenttui: resume-on-death disabled; not re-spawning")
			return
		}

		// S0e8afb-2 graft: gate the session-ID wait on the Adapter's own
		// Capabilities().Resume (mirrors maultiagent's respawnLoop). This is a
		// no-op for the built-in claude adapter — Capabilities().Resume is
		// always true and ClaudeAdapter also implements SessionDiscoverer, so
		// the code path taken below is IDENTICAL to the pre-graft
		// unconditional "always wait for sessionIDReady" behavior. The gate
		// exists for a future non-resume-capable or discoverer-less adapter
		// (P3+), where blocking forever on a channel nothing will ever close
		// would hang respawnLoop with a dead tab and no error.
		resumeCapable := d.adapter != nil && d.adapter.Capabilities().Resume
		if resumeCapable {
			if _, canDiscoverSession := d.adapter.(agent.SessionDiscoverer); canDiscoverSession {
				// Wait until a session ID is available before re-spawning, or
				// until shutdown signals us (Fix 4 — respawnLoop waits for
				// SetSessionID). Byte-identical to the pre-graft claude path.
				select {
				case <-d.shutdownCh:
					return
				case <-d.sessionIDReady:
				}
			}
			// An adapter with Resume==true but no SessionDiscoverer never
			// closes sessionIDReady — falls through using whatever
			// d.sessionID already holds (empty on first crash), same as
			// maultiagent's respawnLoop.
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
			d.logger.Warn("agenttui: resumed session died immediately; restarting fresh",
				"session_id", d.initialSessionID, "lifetime", diedAt.Sub(d.spawnedAt))
		}
		useResume := resumeCapable && sid != "" && !(d.initialResumeBad && sid == d.initialSessionID)
		if useResume {
			d.lastResumedSid = sid
		} else {
			d.lastResumedSid = ""
			sid = ""
		}
		d.stateMu.Unlock()

		if useResume {
			d.logger.Info("agenttui: re-spawning with --resume", "session_id", sid)
		} else {
			d.logger.Info("agenttui: re-spawning fresh (no resume)")
		}

		// S4d8b1c-fix2: gate the re-spawn so a regenerating incus container
		// (briefly destroyed during Update) doesn't make `incus exec` re-run at
		// full speed and spam "Error: Instance is not running" into the terminal.
		if !d.gateRespawn() {
			return // shutdown signalled while waiting
		}

		// S52fc2c-4: before respawning, kill any lingering in-container claude so
		// only one instance runs at a time. [AC-S52fc2c-4-2] S0e8afb-2 graft:
		// uses the adapter-declared kill pattern from the last built SpawnSpec
		// (equal to the hardcoded containerClaudeBin constant for claude, so
		// byte-identical in practice) rather than the constant directly — see
		// effectiveKillPattern's doc comment.
		reapContainerClaude(d.runtimeResolver, d.repoID, d.branchID, d.effectiveKillPattern(), 3*time.Second, d.logger)

		// Sc4f091-2 review fix: same reasoning as EnsureStarted — wait for
		// in-container shared-path readiness OUTSIDE spawnMu so a respawn's
		// bounded wait can't serialize some unrelated concurrent
		// EnsureStarted/respawn call behind it.
		d.preSpawnWaitForSharedPaths()

		d.spawnMu.Lock()
		spawnErr := d.spawnWithArgs(sid, true)
		d.spawnMu.Unlock()

		if spawnErr != nil {
			d.logger.Error("agenttui: respawn failed", "err", spawnErr)
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

// effectiveKillPattern returns d.killPattern (the most recent
// agent.SpawnSpec.KillPattern, set by spawnWithArgs) if a spawn has
// happened, or the containerClaudeBin fallback constant otherwise. The
// fallback preserves a pre-S0e8afb-2-graft invariant several tests pin
// directly (TestDaemonShutdown_ReapsInContainerClaude,
// TestManagerCloseBranchDaemons_ReapsEachTab): reap must fire — with the
// correct pattern — even for a Daemon whose subprocess was NEVER spawned
// (defense-in-depth: SHUTDOWN-triggered reap is unconditional on
// killPtyhost==true / branch-close, independent of spawn history). For the
// built-in claude adapter this fallback is always byte-identical to
// d.killPattern once a spawn HAS happened (ClaudeAdapter.SpawnSpec always
// returns KillPattern == containerClaudeBin), so this is a pure widening of
// WHEN the correct pattern is used, never a behavior change for claude.
func (d *Daemon) effectiveKillPattern() string {
	d.stateMu.Lock()
	pattern := d.killPattern
	d.stateMu.Unlock()
	if pattern == "" {
		return containerClaudeBin
	}
	return pattern
}

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
		return fmt.Errorf("agenttui daemon: %w", err)
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
		return fmt.Errorf("agenttui daemon: %w", err)
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
			d.logger.Info("agenttui: sending SHUTDOWN to ptyhost")
			if err := d.writeFrame(ptyhost.MsgShutdown, ptyhost.EncodeShutdown(ptyhost.ShutdownPayload{
				GraceMillis: int(gracefulShutdownTimeout / time.Millisecond),
			})); err != nil {
				d.logger.Warn("agenttui: send SHUTDOWN failed (ptyhost likely already gone)", "err", err)
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
					d.logger.Warn("agenttui: ptyhost did not confirm shutdown in time")
				}
			}
		}

		// S52fc2c-4 / S3f2658-4: reap any lingering in-container claude process.
		// Killing the host-side incus exec wrapper does not always propagate
		// SIGTERM into the container child. We attempt an explicit pkill inside
		// the container as a best-effort cleanup. Runs after the ptyhost-side
		// process is confirmed dead/timed-out (above) so we don't race with the
		// live process's own cleanup. Skipped entirely for Detach — a surviving
		// ptyhost's in-container claude must keep running too (AC-S3f2658-4-1:
		// palmux2's own restart must NOT reap a still-referenced session).
		if killPtyhost {
			reapContainerClaude(d.runtimeResolver, d.repoID, d.branchID, d.effectiveKillPattern(), 5*time.Second, d.logger)
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
			d.logger.Info("agenttui: daemon shutdown complete")
		} else {
			d.logger.Info("agenttui: daemon detached (ptyhost left running for a future reconnect)")
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
