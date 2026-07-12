package claudeagent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
	"github.com/tjst-t/palmux2/internal/runtime"
)

// S862203-3: Client is a THIN pipe-mode-ptyhost socket client (ADR-0002/
// ADR-0004). It used to own the claude subprocess directly (exec.Cmd +
// stdin/stdout/stderr pipes); it now spawns-or-attaches a `palmux ptyhost
// --mode pipe` process that holds the real subprocess, and pumps stream-json
// NDJSON lines over that socket instead. All parse/MCP/permstate/transcript
// orchestration below this file (normalize.go/permstate.go/transcript.go/
// mcp.go/control.go) is UNCHANGED — replayed lines are fed through the exact
// same dispatch switch that live lines always went through (PD-5: "the
// stream is the source of truth").
//
// argv/env/cwd assembly (hooks --settings, --permission-mode, --plugin-dir,
// the incus PTYCommander wrapper) stays byte-identical to the pre-S862203-3
// exec-direct implementation — it is simply handed to ptyhost as an OPAQUE
// command line instead of being exec'd in this process (see
// [Client.buildOpaqueCommand], mirroring claudetui.spawnWithArgs's incus
// branch).

// S4d8b1c: in-container claude paths (mirror claudetui). When the workspace
// runtime is an incus container, claude runs INSIDE it at these fixed paths.
const (
	containerClaudeBin = "/home/ubuntu/.local/bin/claude"
	containerPluginDir = "/usr/local/share/palmux"
)

// gracefulShutdownTimeout bounds how long ptyhost waits between SIGTERM and
// SIGKILL when we ask it to tear the child down (Close/Shutdown path).
const gracefulShutdownTimeout = 5 * time.Second

// ClientOptions configures one CLI subprocess (now held by a pipe-mode
// ptyhost rather than exec'd directly).
type ClientOptions struct {
	Binary         string // "claude" by default
	Cwd            string // worktree path
	SessionID      string // resume target ("" = new session)
	Model          string // --model
	PermissionMode string // --permission-mode
	Effort         string // --effort (low | medium | high | xhigh | max)
	Fork           bool   // when true, --fork-session: use session_id as base but start a fresh id
	// IncludeHookEvents adds --include-hook-events to the CLI invocation so
	// PreToolUse / PostToolUse / Stop / etc. hooks emit lifecycle envelopes
	// (system/hook_started + system/hook_response) on stdout. Opt-in: the
	// flag is omitted by default to keep stream volume low and to honour
	// "CLI is truth" — Palmux never invents hook activity, only mirrors
	// what the CLI emits.
	IncludeHookEvents bool
	// AddDirs are absolute filesystem paths passed to the CLI as
	// `--add-dir <path>` (repeatable). The flag teaches Claude that
	// these directories are within its allowed scope so tools
	// (Read/Edit/etc.) don't bounce on the worktree boundary.
	AddDirs   []string
	ExtraArgs []string // user-supplied flags from settings.json
	Logger    *slog.Logger

	// S4d8b1c: when non-nil, claude runs INSIDE the workspace's incus container
	// via this ExecCommander (incus exec, plain pipes — stream-json safe). nil →
	// host exec. ContainerEnv carries KEY=VALUE pairs (PALMUX_* for the
	// palmux-browser CLI) injected via incus --env when running in-container.
	ExecCommander runtime.ExecCommander
	ContainerEnv  []string

	// S862203-3: pipe-mode ptyhost identity + persistence wiring.

	// RepoID/BranchID/TabID identify this Client's tab — hashed into the
	// deterministic ptyhost socket/status path (ptyHostSeed) so a
	// freshly-constructed Client after a palmux2 restart recomputes the
	// SAME path a surviving ptyhost is still listening on, and used as the
	// OffsetStore key for replay bookkeeping. Empty is tolerated (tests) —
	// see PalmuxBin's doc comment for what that implies.
	RepoID, BranchID, TabID string
	// PalmuxBin is the palmux binary to re-invoke as `<PalmuxBin> ptyhost
	// --mode pipe ...` (production). Empty (the default for every existing
	// test) falls back to an in-process ptyhost.Server goroutine with its
	// own private, auto-generated run directory — hermetic and immune to
	// cross-test socket collisions regardless of RepoID/BranchID/TabID.
	PalmuxBin string
	// InstancePrefix isolates concurrent palmux instances (host vs
	// INSTANCE=dev rigs) — mirrors domain.PalmuxSessionPrefix.
	InstancePrefix string
	// OffsetStore persists the last fully-processed pipe-mode replay offset
	// per (RepoID, BranchID, TabID) so a reconnect (same-process crash
	// respawn OR a palmux2 restart onto a surviving ptyhost) resumes
	// exactly where it left off with no loss and no duplication. Nil
	// disables persistence — every (re)connect replays from the oldest
	// byte the ptyhost ring still retains (acceptable for tests that don't
	// exercise restart semantics).
	OffsetStore *OffsetStore
	// PtyHostLaunch overrides the launch/attach mechanism (test injection).
	// Nil selects [defaultLaunchPtyHost] when PalmuxBin is set, else
	// [inProcessLaunchPtyHost].
	PtyHostLaunch PtyHostLaunchFunc
	// RunDirOverride pins the ptyhost run directory (test injection — lets
	// two successive Client instances in the SAME test process share a run
	// dir to simulate "palmux2 restarted, new Client object, same
	// surviving ptyhost"). Empty + PalmuxBin=="" auto-generates a private
	// per-Client temp dir (see [autoTestRunDir]).
	RunDirOverride string
	// RingSize is the ptyhost ring buffer capacity in bytes. <=0 uses
	// ptyhost's own default.
	RingSize int
}

// CanUseToolHandler is invoked when the CLI asks permission to run a tool
// via the legacy `can_use_tool` control_request. Modern Claude Code routes
// permission checks through `--permission-prompt-tool` + MCP instead, so
// this handler is rarely (if ever) hit; we keep it as a defence-in-depth
// fallback. The handler must answer with allow/deny; an error → deny.
type CanUseToolHandler func(ctx context.Context, req canUseToolRequest, requestID string) (canUseToolResponse, error)

// MessageHandler is the upstream callback for every (non-control) line the
// CLI emits. The callee receives the raw envelope so it can dispatch on Type.
type MessageHandler func(msg streamMsg)

// Client owns the palmux2-side end of one pipe-mode ptyhost connection. It
// is a thin transport: it spawns-or-attaches, pumps JSON-lines I/O, and
// exposes Send / Interrupt / SetModel / Close/Detach. All stream-json
// normalisation and conversation state lives one layer up (manager.go /
// normalize.go) — UNCHANGED by this story.
type Client struct {
	opts         ClientOptions
	mux          *controlMux
	onMessage    MessageHandler
	onCanUseTool CanUseToolHandler
	mcp          *mcpServer
	logger       *slog.Logger

	ctx    context.Context
	cancel context.CancelFunc

	repoID, branchID, tabID string
	sockPath, statusPath    string
	offsetStore             *OffsetStore
	ringGeneration          string
	// reconnected is true when this Client attached to a ptyhost that was
	// ALREADY listening (a survivor from a prior palmux2 lifetime) rather
	// than one it just launched. EnsureClient uses this to skip re-sending
	// the initialize control_request — see Reconnected's doc comment.
	reconnected bool

	pc *PipeClient // set once at construction; immutable for this Client's lifetime (ADR-0002: respawn = new Client, never re-attach in place)

	writeMu sync.Mutex

	// ackMu guards the replay-offset bookkeeping (advanceAck/beginPending/
	// resolvePending) — see persistSafeFrontierLocked's doc comment for why
	// a still-in-flight async control_request must hold the persisted
	// frontier back (S862203-3 AC-2 part b: a permission outstanding at
	// restart must be replayed, not silently skipped).
	ackMu             sync.Mutex
	highestSeenOffset int64
	// pendingAsync maps the request_id of each control_request still being
	// handled asynchronously to the absolute offset of the first byte of
	// its line (lineStart). The persisted frontier is never advanced past
	// the MIN of these (persistSafeFrontierLocked).
	pendingAsync map[string]int64
	// resolved maps the request_id of each control_request whose
	// control_response has been written back, to its lineStart. Persisted
	// (pruned to the replayable window) so a reconnect can tell an
	// already-answered control_request apart from a genuinely-pending one
	// and NOT re-surface it (S862203-3 review HIGH — parallel/overlapping
	// permissions across a restart).
	resolved map[string]int64
	// replaySuppress is the set of control_request request_ids that were
	// already resolved as of the persisted checkpoint this Client resumed
	// from — loaded ONCE at construction (from the OffsetStore record, only
	// when the persisted offset is actually honoured, i.e. same ring
	// generation) and READ-ONLY thereafter. handleLine consults it to skip
	// re-dispatching (and thus re-surfacing) an already-answered
	// control_request encountered during the reconnect replay. Live traffic
	// never mutates it (new requests get fresh ids), so it needs no lock.
	replaySuppress map[string]struct{}

	// stderrTailMu guards the small rolling buffer used to detect the
	// "No conversation found with session ID" pattern across a stderr
	// chunk boundary (MsgStderrData has no line-reassembly guarantee).
	stderrTailMu sync.Mutex
	stderrTail   string

	teardownOnce sync.Once
	detaching    atomic.Bool
	// runLoopExited is closed exactly once, unconditionally, when the
	// PipeClient.Run goroutine returns for any reason — the "local cleanup
	// is done" signal used by teardown (both Close and Detach wait on it).
	runLoopExited chan struct{}

	// doneCh/exitErr mirror the pre-S862203-3 contract exactly: doneCh
	// closes (and exitErr is set) only when the underlying CHILD PROCESS
	// is confirmed gone — never on a mere local-connection Detach (the
	// child survives that by design). Consumed once by Agent.watchClient.
	doneCh  chan struct{}
	exitErr error

	// invalidResume is set by handleStderr when the CLI emits the "No
	// conversation found with session ID" line — a sign that the --resume
	// target is stale and the Agent should retry without it.
	invalidResumeMu sync.Mutex
	invalidResume   bool
}

// NewClient spawns-or-attaches the pipe-mode ptyhost holding the CLI and
// starts pumping its stdout in a goroutine. Pass `permission` to wire the
// MCP-based permission prompt — without it the CLI will deny tool calls in
// stream-json mode, since `bypass`/`auto` modes still gate dangerous
// operations through the prompt tool. The caller MUST eventually call
// Close (permanent teardown) or Detach (leave the ptyhost+child running for
// a future reconnect) to release local resources.
func NewClient(ctx context.Context, opts ClientOptions, onMessage MessageHandler, onCanUseTool CanUseToolHandler, permission PermissionRequester) (*Client, error) {
	if opts.Binary == "" {
		opts.Binary = "claude"
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}

	args := []string{
		"--input-format", "stream-json",
		"--output-format", "stream-json",
		"--include-partial-messages",
		"--verbose",
		"--setting-sources", "project,user",
	}
	// Always disable Claude Remote Control for palmux-spawned sessions: they are
	// local-only and must not be steerable remotely. Session-scoped via --settings
	// so the user's ~/.claude and the repo's .claude are untouched.
	args = append(args, "--settings", `{"disableRemoteControl":true}`)
	if permission != nil {
		// Tell the CLI which tool to ask for permission. The SDK server
		// itself is declared via `sdkMcpServers` inside the `initialize`
		// control_request — passing it through --mcp-config too registers
		// the entry twice and triggers a duplicate initialize handshake.
		args = append(args,
			"--permission-prompt-tool", "mcp__"+MCPServerName+"__"+PermissionToolName,
		)
	}
	if opts.SessionID != "" {
		args = append(args, "--resume", opts.SessionID)
		if opts.Fork {
			args = append(args, "--fork-session")
		}
	}
	if opts.Model != "" {
		args = append(args, "--model", opts.Model)
	}
	if opts.PermissionMode != "" {
		args = append(args, "--permission-mode", opts.PermissionMode)
	}
	if opts.Effort != "" {
		args = append(args, "--effort", opts.Effort)
	}
	if opts.IncludeHookEvents {
		args = append(args, "--include-hook-events")
	}
	for _, d := range opts.AddDirs {
		if d == "" {
			continue
		}
		args = append(args, "--add-dir", d)
	}
	args = append(args, opts.ExtraArgs...)

	argv, env, cwd := buildOpaqueCommand(opts, args)

	c := &Client{
		opts:           opts,
		mux:            newControlMux(),
		onMessage:      onMessage,
		onCanUseTool:   onCanUseTool,
		logger:         opts.Logger,
		repoID:         opts.RepoID,
		branchID:       opts.BranchID,
		tabID:          opts.TabID,
		offsetStore:    opts.OffsetStore,
		pendingAsync:   map[string]int64{},
		resolved:       map[string]int64{},
		replaySuppress: map[string]struct{}{},
		runLoopExited:  make(chan struct{}),
		doneCh:         make(chan struct{}),
	}
	if permission != nil {
		c.mcp = newMCPServer(permission)
	}
	// A long-lived, Client-owned ctx (NOT the caller's request ctx) bounds
	// the ptyhost connection — mirrors claudetui's daemonCtx isolation so a
	// short HTTP-request-scoped ctx doesn't kill a long-running turn.
	c.ctx, c.cancel = context.WithCancel(context.Background())

	pc, hello, reconnected, err := c.launchAndAttachPipe(argv, env, cwd)
	if err != nil {
		c.cancel()
		return nil, fmt.Errorf("claudeagent: ptyhost spawn/attach: %w", err)
	}
	c.pc = pc
	c.reconnected = reconnected
	c.ringGeneration = ringGenerationFor(hello)
	// resumeOffsetFor decides the ATTACH offset AND (when it honours a
	// persisted checkpoint of the same ring generation) seeds
	// replaySuppress + resolved from that record's ResolvedControlRequests,
	// so the reconnect replay can skip re-surfacing already-answered
	// control_requests (S862203-3 review HIGH).
	offset := c.resumeOffsetFor(hello)

	go c.runLoop(offset)

	return c, nil
}

// buildOpaqueCommand assembles the FULL, opaque argv/env/cwd handed to
// ptyhost (ADR-0002 — ptyhost interprets none of it), mirroring
// claudetui.spawnWithArgs's incus branch exactly: when opts.ExecCommander
// is set, claude runs INSIDE the workspace's incus container via the
// PTYCommander-shaped *exec.Cmd it builds, whose Path/Args/Env/Dir are
// unpacked into an opaque argv/env/cwd triple (argv = [cmd.Path,
// cmd.Args[1:]...], env = cmd.Env, cwd = cmd.Dir); otherwise it's a host
// spawn with "claude" resolved to an absolute path in PALMUX2's OWN
// process/environment (resolveClaudeBin) so argv[0] resolution is
// deterministic regardless of which process ultimately execs it.
func buildOpaqueCommand(opts ClientOptions, args []string) (argv, env []string, cwd string) {
	// Explicitly capture palmux2's OWN environment (+ TERM) as the opaque
	// Env handed to ptyhost — unlike the pre-S862203-3 exec-direct
	// implementation, ptyhost is normally a SEPARATE detached process (a
	// `systemd-run --user --scope` unit or setsid fallback) that does not
	// automatically inherit palmux2's own env the way an in-process
	// exec.Cmd child would, so this must be forwarded explicitly (mirrors
	// claudetui.spawnWithArgs).
	baseEnv := appendOrReplaceEnv(os.Environ(), "TERM=xterm-256color")

	if opts.ExecCommander != nil {
		cargs := append([]string{"--plugin-dir", containerPluginDir}, args...)
		wrapperArgv := append([]string{containerClaudeBin}, cargs...)
		cenv := append([]string{"TERM=xterm-256color"}, opts.ContainerEnv...)
		cmd := opts.ExecCommander.ExecCommand(context.Background(), wrapperArgv,
			runtime.PTYCommandOpts{Cwd: opts.Cwd, Env: cenv})
		argv = append([]string{cmd.Path}, cmd.Args[1:]...)
		env = cmd.Env
		cwd = cmd.Dir
		return argv, env, cwd
	}

	argv = append([]string{resolveClaudeBin(opts.Binary)}, args...)
	env = baseEnv
	cwd = opts.Cwd
	return argv, env, cwd
}

// resolveClaudeBin resolves a bare command name (no path separator) to an
// absolute path via LookPath in PALMUX2's OWN process/environment, so
// argv[0] resolution is deterministic regardless of which process (this
// one, or a detached `palmux ptyhost`, possibly under a systemd --user
// session with a different PATH) ultimately execs it.
func resolveClaudeBin(bin string) string {
	if bin == "" || strings.ContainsRune(bin, filepath.Separator) {
		return bin
	}
	if resolved, err := exec.LookPath(bin); err == nil {
		return resolved
	}
	return bin
}

// appendOrReplaceEnv either appends "KEY=value" to env or replaces the
// existing entry with a matching key prefix.
func appendOrReplaceEnv(env []string, kv string) []string {
	key := strings.SplitN(kv, "=", 2)[0] + "="
	out := append([]string(nil), env...)
	for i, e := range out {
		if strings.HasPrefix(e, key) {
			out[i] = kv
			return out
		}
	}
	return append(out, kv)
}

// ptyHostSeed is the discovery key used to compute this Client's ptyhost
// socket/status paths — repoID__branchID__tabID. Stable across a palmux2
// restart (a freshly reconstructed Client after restart computes the SAME
// seed, letting launchAndAttachPipe find the surviving ptyhost).
func (c *Client) ptyHostSeed() string {
	return c.repoID + "__" + c.branchID + "__" + c.tabID
}

// ptyHostRunDir resolves this Client's ptyhost run directory: an explicit
// RunDirOverride wins (tests simulating "same box, new process"); else
// production wiring resolves ptyhost.RunDir(InstancePrefix) when PalmuxBin
// is set; else every test gets its own private auto-generated directory.
func (c *Client) ptyHostRunDir() string {
	if c.opts.RunDirOverride != "" {
		return c.opts.RunDirOverride
	}
	if c.opts.PalmuxBin != "" {
		return ptyhost.RunDir(c.opts.InstancePrefix)
	}
	return autoTestRunDir()
}

// ringGenerationFor derives an opaque marker identifying a PARTICULAR
// ptyhost/child instance from its HELLO reply (S862203-3 review finding
// #4). pid+argvHash changes every time a brand new ptyhost/child is
// spawned but stays IDENTICAL across a reconnect to a SURVIVING ptyhost —
// exactly the "same ring or not" signal [Client.resumeOffsetFor] needs.
func ringGenerationFor(hello ptyhost.HelloPayload) string {
	return fmt.Sprintf("%d:%s", hello.Pid, hello.ArgvHash)
}

// resumeOffsetFor decides the ATTACH offset for a freshly-dialed connection
// from the persisted OffsetStore record (if any). -1 means "replay from the
// oldest byte the ring still retains" (fresh session / no usable
// checkpoint).
//
// S862203-3 review finding #4: a persisted LastAckOffset is only meaningful
// for the EXACT ptyhost/child instance it was recorded against — the ring
// is per-process and starts fresh at byte 0 every time a brand new ptyhost
// is spawned. If the current HELLO's generation marker doesn't match the
// persisted record's, that numeric offset refers to bytes that have
// nothing to do with THIS ring (it might even happen to be "valid" — i.e.
// within [0, ring size) — purely by coincidence, which would silently
// splice unrelated replay bytes into the transcript if we attached at it
// blindly). Treat a generation mismatch the same as the overflow
// "lossless restore impossible" case: fall back to -1 rather than clamping
// down to a numerically-valid-but-semantically-wrong offset.
func (c *Client) resumeOffsetFor(hello ptyhost.HelloPayload) int64 {
	if c.offsetStore == nil {
		return -1
	}
	rec, ok := c.offsetStore.Get(c.repoID, c.branchID, c.tabID)
	if !ok {
		return -1
	}
	gen := ringGenerationFor(hello)
	if rec.RingGeneration != "" && rec.RingGeneration != gen {
		c.logger.Info("claudeagent: persisted replay offset belongs to a different ptyhost generation; starting fresh replay",
			"persisted_generation", rec.RingGeneration, "current_generation", gen, "persisted_offset", rec.LastAckOffset)
		// A different generation's resolved-request set is meaningless for
		// THIS ring — leave replaySuppress/resolved empty so nothing is
		// wrongly suppressed on the fresh replay.
		return -1
	}
	// Honouring the persisted checkpoint: seed the already-resolved
	// control_request set so the reconnect replay skips re-surfacing them
	// (S862203-3 review HIGH), and seed the live `resolved` map so
	// subsequent persists retain them until pruned. lineStarts are absolute
	// ptyhost offsets — stable across a reconnect to the SAME generation
	// (which we just confirmed), so they remain comparable to `safe`.
	for id, lineStart := range rec.ResolvedControlRequests {
		c.replaySuppress[id] = struct{}{}
		c.resolved[id] = lineStart
	}
	if n := len(c.replaySuppress); n > 0 {
		c.logger.Info("claudeagent: resuming with already-answered control_requests to suppress on replay",
			"count", n, "resume_offset", rec.LastAckOffset)
	}
	return rec.LastAckOffset
}

// launchAndAttachPipe implements the reconnect-or-spawn decision: it first
// probes the deterministic socket path for a ptyhost that survived a PRIOR
// palmux2 lifetime and attaches to it if found (HELLO only — the ATTACH
// itself, with its offset decided by [Client.resumeOffsetFor], happens
// later in [Client.runLoop] via [PipeClient.Run]); only when nothing is
// listening there does it launch a brand new one.
func (c *Client) launchAndAttachPipe(argv, env []string, cwd string) (pc *PipeClient, hello ptyhost.HelloPayload, reconnected bool, err error) {
	runDir := c.ptyHostRunDir()
	seed := c.ptyHostSeed()
	c.sockPath = ptyhost.SocketPath(runDir, seed)
	c.statusPath = ptyhost.StatusPath(runDir, seed)

	if conn, ok := probeExisting(c.sockPath); ok {
		p := &PipeClient{conn: conn, onLine: c.handleLine, onStderr: c.handleStderr}
		if h, herr := p.Hello(); herr == nil {
			c.logger.Info("claudeagent: attached to surviving ptyhost", "socket", c.sockPath, "pid", h.Pid)
			return p, h, true, nil
		}
		_ = conn.Close()
	}

	launch := c.opts.PtyHostLaunch
	if launch == nil {
		if c.opts.PalmuxBin != "" {
			launch = defaultLaunchPtyHost
		} else {
			launch = inProcessLaunchPtyHost
		}
	}
	req := PtyHostLaunchRequest{
		PalmuxBin:      c.opts.PalmuxBin,
		InstancePrefix: c.opts.InstancePrefix,
		Seed:           seed,
		RepoID:         c.repoID,
		BranchID:       c.branchID,
		TabID:          c.tabID,
		SocketPath:     c.sockPath,
		StatusPath:     c.statusPath,
		Argv:           argv,
		Env:            env,
		Cwd:            cwd,
		RingSize:       c.opts.RingSize,
	}
	if lerr := launch(c.ctx, req); lerr != nil {
		return nil, ptyhost.HelloPayload{}, false, fmt.Errorf("launch: %w", lerr)
	}
	conn, derr := dialFresh(c.ctx, c.sockPath, ptyHostDialTimeout)
	if derr != nil {
		return nil, ptyhost.HelloPayload{}, false, fmt.Errorf("attach: %w", derr)
	}
	p := &PipeClient{conn: conn, onLine: c.handleLine, onStderr: c.handleStderr}
	h, herr := p.Hello()
	if herr != nil {
		_ = conn.Close()
		return nil, ptyhost.HelloPayload{}, false, fmt.Errorf("attach: %w", herr)
	}
	return p, h, false, nil
}

// runLoop drives PipeClient.Run for the lifetime of this Client's
// connection. It is started exactly once by NewClient.
func (c *Client) runLoop(offset int64) {
	runErr := c.pc.Run(c.ctx, offset, func(res AttachResult) {
		if res.Overflowed {
			c.logger.Warn("claudeagent: pipe ATTACH overflow — persisted checkpoint was evicted from the ring; replaying from the oldest retained byte instead (a gap may exist)",
				"requested", res.Requested, "start", res.StartOffset)
			if c.offsetStore != nil {
				if err := c.offsetStore.Clear(c.repoID, c.branchID, c.tabID); err != nil {
					c.logger.Warn("claudeagent: clear stale replay offset failed", "err", err)
				}
			}
		}
	})
	c.finishRunLoop(runErr)
}

// finishRunLoop determines whether the connection loss represents a
// genuine child-process exit (closes doneCh/sets exitErr — the pre-
// S862203-3 contract Agent.watchClient consumes) or an intentional Detach
// (child survives; doneCh is deliberately left open since nothing in this
// dying process will ever consume it).
func (c *Client) finishRunLoop(runErr error) {
	defer close(c.runLoopExited)
	c.mux.closeAll()
	if c.detaching.Load() {
		return
	}
	// ptyhost's own connection teardown (handleConn's `case MsgShutdown:
	// ... return`, whose deferred conn.Close() fires immediately) is NOT
	// ordered after the child's actual exit / status-file write — those
	// happen on an independent goroutine (waitChild) racing the SAME
	// SHUTDOWN. So "our connection just closed" does not by itself mean
	// the on-disk status is settled yet; poll briefly for it (bounded by
	// the same grace window ptyhost itself uses for SIGTERM→SIGKILL) so
	// Close()'s "by the time it returns, the process is confirmed dead"
	// contract holds instead of racing a stale Alive=true read.
	waitBudget := gracefulShutdownTimeout + 3*time.Second
	sf, ok := pollExitStatus(c.statusPath, waitBudget)
	var exitErr error
	switch {
	case ok && !sf.Alive && sf.ExitCodeValid && sf.ExitCode != 0:
		exitErr = fmt.Errorf("ptyhost: child exited with code %d", sf.ExitCode)
	case ok:
		// !sf.Alive && !sf.ExitCodeValid, or a clean exit(0): exitErr stays nil.
	case runErr != nil && !errors.Is(runErr, context.Canceled):
		exitErr = fmt.Errorf("ptyhost: connection lost and status unavailable: %w", runErr)
	}
	// The status file settling to Alive=false is written by ptyhost's
	// waitChild goroutine, which races INDEPENDENTLY of the main Run()
	// loop's own teardown (listener close + socket file removal) — the two
	// are not ordered relative to each other beyond both following the
	// same childExited signal. Without this, a caller that launches a
	// BRAND NEW ptyhost at the same deterministic socket path the instant
	// Close()/Done() returns (e.g. Agent.respawnClient, or this story's own
	// cross-generation test) can race a listener socket that is still
	// technically accepting connections for a few more scheduler ticks,
	// mistaking a ptyhost that is ALREADY DEAD-BUT-NOT-YET-TORN-DOWN for a
	// live survivor. Bounded by the same budget as the status poll above.
	if ok {
		waitForSocketGone(c.sockPath, waitBudget)
	}
	c.exitErr = exitErr
	close(c.doneCh)
}

// waitForSocketGone polls until path no longer accepts unix-socket
// connections (or timeout elapses) — the complement of [probeExisting].
func waitForSocketGone(path string, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		conn, ok := probeExisting(path)
		if !ok {
			return
		}
		_ = conn.Close()
		time.Sleep(20 * time.Millisecond)
	}
}

// pollExitStatus polls statusPath until it reports Alive=false (the child
// has genuinely exited and ptyhost has recorded it) or timeout elapses,
// returning the last-read status file and whether it settled to
// Alive=false within the budget.
func pollExitStatus(statusPath string, timeout time.Duration) (ptyhost.StatusFile, bool) {
	deadline := time.Now().Add(timeout)
	var last ptyhost.StatusFile
	for {
		sf, err := ptyhost.ReadStatusFile(statusPath)
		if err == nil {
			last = sf
			if !sf.Alive {
				return sf, true
			}
		}
		if time.Now().After(deadline) {
			return last, false
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Done returns a channel closed once the underlying claude process has
// genuinely exited (never merely detached-and-surviving). ExitErr captures
// the cause; nil for clean exits.
func (c *Client) Done() <-chan struct{} { return c.doneCh }
func (c *Client) ExitErr() error        { return c.exitErr }

// Reconnected reports whether this Client attached to a ptyhost that
// survived a PRIOR palmux2 lifetime, rather than one it just spawned. See
// its use in EnsureClient (skips the initialize control_request on a live
// reconnect).
func (c *Client) Reconnected() bool { return c.reconnected }

// handleLine is the [LineHandler] fed to the [PipeClient]: it re-implements
// the exact dispatch switch the old bufio.Scanner-driven pumpStdout used,
// UNCHANGED in behaviour — control_response routes to the mux,
// control_request forks a handler goroutine, everything else goes to
// onMessage (→ processStreamMessage, one layer up, untouched by this
// story). The only addition is offset bookkeeping (advanceAck/
// beginPending/resolvePending) so a still-pending control_request holds
// back the persisted replay checkpoint — see
// [Client.persistSafeFrontierLocked].
func (c *Client) handleLine(line []byte, endOffset int64) error {
	if len(line) == 0 {
		c.advanceAck(endOffset)
		return nil
	}
	var msg streamMsg
	if err := json.Unmarshal(line, &msg); err != nil {
		c.logger.Warn("claudeagent: bad json line", "err", err, "snippet", truncate(string(line), 200))
		c.advanceAck(endOffset)
		return nil
	}

	switch {
	case msg.Type == "control_response":
		var inner controlResponseInner
		if err := json.Unmarshal(msg.Response, &inner); err != nil {
			c.logger.Warn("claudeagent: malformed control_response", "err", err)
		} else {
			c.mux.resolveResponse(inner)
		}
		c.advanceAck(endOffset)

	case msg.Type == "control_request" && msg.RequestID != "" && len(msg.Request) > 0:
		// The line's own byte range is [lineStart, endOffset). Handling
		// runs asynchronously (a permission decision can legitimately take
		// a long time — the whole point of no-halt-agent) but the ACK/
		// persisted offset must NOT advance past lineStart until it
		// resolves, so a restart in that window replays this exact line
		// again on reconnect rather than silently skipping a pending
		// permission (S862203-3 AC-2 part b).
		lineStart := endOffset - int64(len(line)) - 1
		if _, suppress := c.replaySuppress[msg.RequestID]; suppress {
			// This control_request was ALREADY answered before the restart
			// this Client is resuming from (its control_response was written
			// to the live claude process, which survived — we're reconnected
			// to it). Re-dispatching it would (a) re-surface a spurious
			// duplicate permission prompt in the UI for an already-resolved
			// request and (b) write a duplicate control_response for an
			// already-cleared request_id. Skip both; just advance the ack.
			// The transcript (assistant/tool_use/tool_result envelopes) is
			// carried by SEPARATE `default`-case lines that replay normally —
			// a control_request line itself contributes nothing to the
			// visible transcript. (S862203-3 review HIGH.)
			c.logger.Debug("claudeagent: skipping already-answered control_request on replay",
				"request_id", msg.RequestID)
			c.advanceAck(endOffset)
			break
		}
		c.beginPending(msg.RequestID, lineStart)
		go func() {
			// handleControlRequest writes the control_response back to the
			// CLI; resolvePending then records+persists this request as
			// answered. There is a narrow window BETWEEN those two: if
			// palmux2 crashes after the control_response is written but
			// before resolvePending persists, the reconnect will NOT find
			// this id in the persisted resolved-set and WILL re-surface the
			// (already-answered) permission as a duplicate prompt. This
			// ordering is INTENTIONAL and must NOT be "fixed" by persisting
			// the resolved-set BEFORE writing the response: doing so would
			// invert the failure into the opposite, worse mode — a crash in
			// the inverted window would mark the request answered WITHOUT
			// having told the CLI, permanently hanging the turn on a
			// permission the user can no longer act on. A spurious duplicate
			// prompt (recoverable — the user answers again, or it is a
			// no-op if the CLI already cleared the request_id) is strictly
			// safer than a permanent hang. (S862203-3 review LOW.)
			c.handleControlRequest(msg)
			c.resolvePending(msg.RequestID, lineStart, endOffset)
		}()

	case msg.Type == "control_cancel_request":
		// CLI-side cancellation. We don't track inflight handlers granularly
		// — the caller's ctx will fire a regular timeout. Acknowledge by
		// dropping the message.
		c.advanceAck(endOffset)

	default:
		if c.onMessage != nil {
			c.onMessage(msg)
		}
		c.advanceAck(endOffset)
	}
	return nil
}

// advanceAck records endOffset as fully processed and persists the current
// safe frontier.
func (c *Client) advanceAck(endOffset int64) {
	c.ackMu.Lock()
	defer c.ackMu.Unlock()
	if endOffset > c.highestSeenOffset {
		c.highestSeenOffset = endOffset
	}
	c.persistSafeFrontierLocked()
}

// beginPending marks the control_request `requestID` (whose line starts at
// lineStart) as still being handled asynchronously.
func (c *Client) beginPending(requestID string, lineStart int64) {
	c.ackMu.Lock()
	defer c.ackMu.Unlock()
	c.pendingAsync[requestID] = lineStart
	c.persistSafeFrontierLocked()
}

// resolvePending marks a previously-pending control_request as fully
// handled (its control_response has been written back to the CLI): it moves
// the request from pendingAsync into `resolved` (so a later reconnect that
// replays its line — because an EARLIER request is still holding the
// frontier back — will recognise it as already-answered and NOT re-surface
// it) and re-persists the (now possibly advanced) safe frontier.
func (c *Client) resolvePending(requestID string, lineStart, endOffset int64) {
	c.ackMu.Lock()
	defer c.ackMu.Unlock()
	delete(c.pendingAsync, requestID)
	c.resolved[requestID] = lineStart
	if endOffset > c.highestSeenOffset {
		c.highestSeenOffset = endOffset
	}
	c.persistSafeFrontierLocked()
}

// persistSafeFrontierLocked (ackMu held) persists the largest offset such
// that EVERY byte up to it has been fully processed — i.e. it never
// advances past a still-in-flight async control_request — TOGETHER with the
// set of already-answered control_request ids that fall at or after that
// offset (i.e. could still be replayed). This is the mechanism that makes
// AC-S862203-3-2 part (b) possible: a permission outstanding at the moment
// of a palmux2 restart was never acked past its own line, so ATTACHing at
// the persisted offset on reconnect replays that exact control_request
// again, re-entering the SAME (unchanged) mcp.handle → RequestPermission →
// awaitPermission path and reconstructing the pending-permission UI state
// for free (ADR-0004 PD-5). The resolved-set is what keeps a DIFFERENT,
// already-answered permission (answered while an earlier one stayed pending
// — claude issues parallel tool calls) from ALSO re-surfacing on that same
// replay (S862203-3 review HIGH).
func (c *Client) persistSafeFrontierLocked() {
	if c.offsetStore == nil {
		return
	}
	safe := c.highestSeenOffset
	for _, start := range c.pendingAsync {
		if start < safe {
			safe = start
		}
	}
	// Prune resolved entries strictly before `safe`: a reconnect ATTACHes
	// at `safe`, so anything before it can never be replayed and need not
	// be remembered — keeps the persisted set bounded to the replayable
	// window rather than growing for the whole session.
	for id, start := range c.resolved {
		if start < safe {
			delete(c.resolved, id)
		}
	}
	if err := c.offsetStore.Save(c.repoID, c.branchID, c.tabID, safe, c.ringGeneration, c.resolved); err != nil {
		c.logger.Warn("claudeagent: persist replay offset failed", "err", err)
	}
}

// handleStderr is the [StderrHandler] fed to the [PipeClient]. Stderr is
// diagnostic-only (no offset/replay bookkeeping — ADR-0004 §6): we log each
// chunk and scan a small rolling buffer for the "No conversation found with
// session ID" pattern (which may straddle a chunk boundary).
func (c *Client) handleStderr(_ int64, data []byte) {
	text := string(data)
	c.logger.Info("claude.stderr", "chunk", text)
	const invalidResumePattern = "No conversation found with session ID"
	const tailBudget = 512
	c.stderrTailMu.Lock()
	combined := c.stderrTail + text
	if strings.Contains(combined, invalidResumePattern) {
		c.stderrTailMu.Unlock()
		c.flagInvalidResume()
		c.stderrTailMu.Lock()
	}
	if len(combined) > tailBudget {
		combined = combined[len(combined)-tailBudget:]
	}
	c.stderrTail = combined
	c.stderrTailMu.Unlock()
}

// flagInvalidResume marks this client as having tried to resume a session
// that no longer exists on disk. The Agent's watchClient checks the flag
// after the process exits and clears the persisted active session_id so
// the next spawn starts fresh.
func (c *Client) flagInvalidResume() {
	c.invalidResumeMu.Lock()
	c.invalidResume = true
	c.invalidResumeMu.Unlock()
}

// InvalidResume reports whether stderr matched the "no conversation found"
// pattern at any point during this client's lifetime.
func (c *Client) InvalidResume() bool {
	c.invalidResumeMu.Lock()
	defer c.invalidResumeMu.Unlock()
	return c.invalidResume
}

// handleControlRequest processes CLI-initiated control_requests. UNCHANGED
// from the pre-S862203-3 implementation — only its caller (handleLine)
// changed, from a bufio.Scanner loop to a PipeClient LineHandler.
func (c *Client) handleControlRequest(msg streamMsg) {
	var head struct {
		Subtype string `json:"subtype"`
	}
	_ = json.Unmarshal(msg.Request, &head)
	switch head.Subtype {
	case "mcp_message":
		c.handleMCPMessage(msg)
	case "can_use_tool":
		var req canUseToolRequest
		if err := json.Unmarshal(msg.Request, &req); err != nil {
			c.respondControl(msg.RequestID, canUseToolResponse{
				Subtype: "can_use_tool", Behavior: "deny",
				Message: "malformed canUseTool request",
			})
			return
		}
		if c.onCanUseTool == nil {
			c.respondControl(msg.RequestID, canUseToolResponse{
				Subtype: "can_use_tool", Behavior: "deny",
				Message: "no permission handler configured",
			})
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), controlRequestTimeout)
		defer cancel()
		resp, err := c.onCanUseTool(ctx, req, msg.RequestID)
		if err != nil {
			c.respondControl(msg.RequestID, canUseToolResponse{
				Subtype: "can_use_tool", Behavior: "deny",
				Message: err.Error(),
			})
			return
		}
		resp.Subtype = "can_use_tool"
		c.respondControl(msg.RequestID, resp)
	default:
		c.logger.Warn("claudeagent: unknown control_request subtype", "subtype", head.Subtype)
	}
}

// handleMCPMessage unwraps the JSON-RPC message inside an mcp_message
// control_request, dispatches it to the in-process MCP server, and packages
// the JSON-RPC response back inside an mcp_message control_response so the
// CLI's MCP client picks it up. Notification-style requests yield no
// response. UNCHANGED from the pre-S862203-3 implementation.
func (c *Client) handleMCPMessage(msg streamMsg) {
	if c.mcp == nil {
		c.respondControl(msg.RequestID, map[string]any{
			"subtype": "mcp_message",
			"mcp_response": map[string]any{
				"jsonrpc": "2.0",
				"error":   map[string]any{"code": -32601, "message": "MCP not configured"},
			},
		})
		return
	}
	var inner struct {
		Subtype    string          `json:"subtype"`
		ServerName string          `json:"server_name"`
		Message    json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(msg.Request, &inner); err != nil {
		c.logger.Warn("claudeagent: malformed mcp_message", "err", err)
		return
	}
	if inner.ServerName != "" && inner.ServerName != MCPServerName {
		c.logger.Warn("claudeagent: mcp_message for unknown server", "server", inner.ServerName)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	resp, hasResp := c.mcp.handle(ctx, inner.Message)
	body := map[string]any{}
	if hasResp {
		body["mcp_response"] = resp
	} else {
		body["mcp_response"] = nil
	}
	c.respondControl(msg.RequestID, body)
}

// respondControl writes a success-shaped control_response with the given
// request_id and payload. UNCHANGED from the pre-S862203-3 implementation.
func (c *Client) respondControl(requestID string, body any) {
	frame, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "success",
			"request_id": requestID,
			"response":   body,
		},
	})
	if err != nil {
		c.logger.Warn("claudeagent: marshal control envelope", "err", err)
		return
	}
	if err := c.writeLine(frame); err != nil {
		c.logger.Warn("claudeagent: write control response", "err", err)
	}
}

// respondControlError writes an error-shaped control_response.
func (c *Client) respondControlError(requestID, message string) {
	frame, err := json.Marshal(map[string]any{
		"type": "control_response",
		"response": map[string]any{
			"subtype":    "error",
			"request_id": requestID,
			"error":      message,
		},
	})
	if err != nil {
		c.logger.Warn("claudeagent: marshal control envelope", "err", err)
		return
	}
	if err := c.writeLine(frame); err != nil {
		c.logger.Warn("claudeagent: write control error", "err", err)
	}
}

// writeLine sends one JSON-lines frame to the child's stdin, over the
// pipe-mode ptyhost's INPUT frame (was: a direct write to an exec.Cmd's
// stdin pipe).
func (c *Client) writeLine(data []byte) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if c.pc == nil {
		return errors.New("claudeagent: not connected")
	}
	buf := make([]byte, 0, len(data)+1)
	buf = append(buf, data...)
	buf = append(buf, '\n')
	return c.pc.WriteInput(buf)
}

// SendUserMessage sends a plain text user message into the CLI's stdin.
func (c *Client) SendUserMessage(content string) error {
	body, err := json.Marshal(map[string]any{
		"role":    "user",
		"content": content,
	})
	if err != nil {
		return err
	}
	frame, err := json.Marshal(streamMsg{
		Type:    "user",
		Message: body,
	})
	if err != nil {
		return err
	}
	return c.writeLine(frame)
}

// Initialize sends the initialize control request. Declares the SDK-typed
// MCP servers up-front (just `palmux` for now). The CLI responds with a
// big payload describing its commands / agents / models / account; we
// hand the raw response back to the caller so the manager can extract the
// pieces the UI needs (commands list, model menu, etc).
func (c *Client) Initialize(ctx context.Context) (json.RawMessage, error) {
	req := initializeRequest{Subtype: "initialize"}
	if c.mcp != nil {
		req.SDKMCPServers = []string{MCPServerName}
	}
	return c.controlCall(ctx, req)
}

// Interrupt aborts the in-flight assistant turn.
func (c *Client) Interrupt(ctx context.Context) error {
	_, err := c.controlCall(ctx, interruptRequest{Subtype: "interrupt"})
	return err
}

// SetModel changes the model mid-session.
func (c *Client) SetModel(ctx context.Context, model string) error {
	_, err := c.controlCall(ctx, setModelRequest{Subtype: "set_model", Model: model})
	return err
}

// SetPermissionMode swaps the permission policy.
func (c *Client) SetPermissionMode(ctx context.Context, mode string) error {
	_, err := c.controlCall(ctx, setPermissionModeRequest{Subtype: "set_permission_mode", Mode: mode})
	return err
}

// RegisterSDKMCPServer announces an in-process MCP server to the CLI via
// `mcp_set_servers`. Static `--mcp-config` is necessary but not sufficient
// for SDK-typed servers; the CLI ignores them at tool-resolution time
// unless they're also in the dynamically-managed set.
func (c *Client) RegisterSDKMCPServer(ctx context.Context, name string) error {
	_, err := c.controlCall(ctx, setMCPServersRequest{
		Subtype: "mcp_set_servers",
		Servers: map[string]mcpServerRef{
			name: {Type: "sdk", Name: name},
		},
	})
	return err
}

// Close permanently tears down this Client's claude process: it sends
// SHUTDOWN to the ptyhost (which SIGTERM→(grace)→SIGKILLs the child) and
// waits for local cleanup. Safe to call multiple times, and mutually
// exclusive with Detach (whichever is called first wins — both share the
// same teardown guard).
func (c *Client) Close() {
	c.teardown(true)
}

// Detach disconnects from the ptyhost WITHOUT sending SHUTDOWN — the
// ptyhost (and the claude process/incus-wrapper it holds) is deliberately
// left running so a FUTURE Client (a fresh palmux2 process reconnecting
// after a restart) can attach to it. Use this from palmux2's own
// process-exit path (see Manager.DetachAll), never from an intentional
// tab/branch close (use Close for that).
func (c *Client) Detach() {
	c.teardown(false)
}

func (c *Client) teardown(kill bool) {
	c.teardownOnce.Do(func() {
		// Always released once local cleanup is fully done (pc.Run has
		// already returned by every path below, so this is inert cleanup,
		// never a race with the ctx-cancel-triggered conn.Close in
		// PipeClient.Run's context.AfterFunc).
		defer c.cancel()
		if !kill {
			c.detaching.Store(true)
		}
		if kill && c.pc != nil {
			// Ask ptyhost to terminate the child (SIGTERM→grace→SIGKILL).
			// NOTE: ptyhost's connection teardown on SHUTDOWN happens
			// immediately (handleConn returns and closes its end of the
			// socket right away) and is NOT ordered after the child's actual
			// exit — that races on an independent goroutine. So "our
			// connection just closed" is not itself proof of death; see
			// finishRunLoop's pollExitStatus, which is what actually makes
			// Close()'s "by the time it returns, the process is confirmed
			// dead" contract hold.
			if err := ptyhost.WriteFrame(c.pc.conn, ptyhost.MsgShutdown, ptyhost.EncodeShutdown(ptyhost.ShutdownPayload{
				GraceMillis: int(gracefulShutdownTimeout / time.Millisecond),
			})); err != nil {
				c.logger.Debug("claudeagent: send SHUTDOWN failed (ptyhost likely already gone)", "err", err)
			}
		} else if c.pc != nil {
			_ = c.pc.Close()
		}
		c.cancel()
		<-c.runLoopExited

		if kill {
			// S52fc2c-4: kill any lingering in-container claude process. The
			// host-side incus exec wrapper being terminated does not
			// guarantee the container child dies. Skipped for Detach — a
			// surviving ptyhost's in-container claude must keep running too.
			if c.opts.ExecCommander != nil {
				if kk, ok := c.opts.ExecCommander.(runtime.ContainerProcessKiller); ok {
					kCtx, kCancel := context.WithTimeout(context.Background(), 5*time.Second)
					if err := kk.KillContainerProcesses(kCtx, "TERM", containerClaudeBin); err != nil {
						c.logger.Debug("claudeagent: in-container claude TERM (non-fatal)", "err", err)
					}
					kCancel()
				}
			}
			c.logger.Info("claudeagent: client closed (ptyhost shut down)")
		} else {
			c.logger.Info("claudeagent: client detached (ptyhost left running for a future reconnect)")
		}
	})
}

// currentPipeClientForTest exposes the live connection to whitebox tests
// in this package (e.g. simulating a connection drop). Not used by
// production code.
func (c *Client) currentPipeClientForTest() *PipeClient { return c.pc }

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
