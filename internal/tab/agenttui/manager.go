package agenttui

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/tjst-t/palmux2/internal/agent"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/notify"
	"github.com/tjst-t/palmux2/internal/ptyhost"
	"github.com/tjst-t/palmux2/internal/runtime"
)

// ManagerConfig holds configuration shared by all daemons in the Manager.
type ManagerConfig struct {
	// Adapter builds the SpawnSpec for every Daemon this Manager creates
	// (S0e8afb-2 graft). ALL daemons this Manager creates share the SAME
	// Adapter instance (not one freshly constructed per Daemon) — this is
	// what lets SetClaudeBin/SetClaudeArgs (via agent.Configurable) actually
	// hot-swap already-spawned daemons' bin/args on their next respawn, not
	// just daemons created after the hot-swap.
	//
	// When nil (every existing caller as of this Sprint — production wiring
	// is updated in cmd/palmux/main.go to set this explicitly), NewManager
	// defaults it to agent.NewClaudeAdapter(ClaudeBin, ClaudeArgs) — the
	// built-in claude adapter, preserving pre-graft behavior byte-for-byte.
	// S0e8afb-3 (multiplicity/ownership) is expected to construct one Manager
	// per agent kind, each with its own Adapter from the registry.
	Adapter agent.Adapter
	// ClaudeBin is the path to the claude binary (default: "claude"). Only
	// consulted when Adapter is nil — see Adapter's doc comment.
	ClaudeBin string
	// ClaudeArgs are additional arguments appended to every daemon spawn.
	// Only consulted when Adapter is nil — see Adapter's doc comment.
	ClaudeArgs []string
	// PermissionMode is the claude --permission-mode value (global setting,
	// default "auto"). Hot-swappable via SetPermissionMode; read on each spawn.
	PermissionMode string
	// RingSize is the per-daemon ring buffer capacity in bytes (0 → DefaultRingSize).
	RingSize int
	// ResumeOnDeath controls whether daemons re-spawn with --resume on
	// unexpected subprocess exit.
	ResumeOnDeath bool
	// Logger is used by all daemons (nil → slog.Default()).
	Logger *slog.Logger
	// Store is the optional session-persistence store.  When nil, session IDs
	// are still detected and forwarded to Daemon.SetSessionID but are NOT
	// persisted across process restarts.
	Store *SessionStore
	// NotifyHub is forwarded to each Daemon so that OSC 52 clipboard-write
	// sequences are published to the process-wide notify hub.  May be nil.
	NotifyHub *notify.Hub
	// NotifyURL / NotifyToken / HookBinPath wire the Claude Code notification
	// hooks injected into each claude subprocess (see hooks.go). NotifyURL is
	// the local /api/notify endpoint, NotifyToken the optional auth token, and
	// HookBinPath the absolute path to the palmux binary used as the hook
	// command. Empty NotifyURL or HookBinPath disables hook injection.
	NotifyURL   string
	NotifyToken string
	HookBinPath string

	// S4d8b1c: RuntimeResolver returns a runtime.PTYCommander when the workspace
	// runtime can run claude inside a container (incus), else nil → host exec.
	// NotifyURLInContainer is the bridge-gateway notify URL used for the hook
	// when claude runs in-container.
	RuntimeResolver      func(repoID, branchID string) runtime.PTYCommander
	NotifyURLInContainer string

	// S3f2658-2: PalmuxBin/PtyHostLaunch/RunDirOverride are forwarded verbatim
	// to every Daemon this Manager creates — see DaemonConfig's fields of the
	// same name for the full doc comment.
	PalmuxBin      string
	PtyHostLaunch  PtyHostLaunchFunc
	RunDirOverride string
	// InstancePrefix (S3f2658-3) is forwarded verbatim to every Daemon this
	// Manager creates — see DaemonConfig.InstancePrefix. Empty (production)
	// falls back to the global domain.PalmuxSessionPrefix.
	InstancePrefix string
}

// managerEntry bundles a Daemon with its associated SessionWatcher so both
// can be stopped together in CloseDaemon / ShutdownAll.
type managerEntry struct {
	daemon  *Daemon
	watcher *SessionWatcher // may be nil if no worktree was provided
}

// Manager holds one [Daemon] per (repoID, branchID, tabID) tuple. Sadf90e
// switched the key from per-branch to per-tab so two Claude tabs in the same
// workspace can hold independent claude processes.
//
// Thread safety: all methods are safe for concurrent use.
type Manager struct {
	cfg     ManagerConfig
	mu      sync.Mutex
	entries map[string]*managerEntry
}

// SetClaudeBin hot-swaps the claude binary used for daemons spawned after this
// call (Sa53137-3 hot apply). S0e8afb-2 graft: this now ALSO reaches
// already-spawned daemons on their next respawn — every Daemon this Manager
// creates shares the same cfg.Adapter instance (see ManagerConfig.Adapter's
// doc comment), and agent.ClaudeAdapter.SetBin mutates that shared instance
// (agent.Configurable), so a live daemon's respawnLoop picks up the new
// binary the next time it calls spawnWithArgs. No-op when the configured
// Adapter does not implement agent.Configurable (a future non-claude kind
// might not).
func (m *Manager) SetClaudeBin(bin string) {
	if bin == "" {
		return
	}
	m.mu.Lock()
	m.cfg.ClaudeBin = bin
	adapter := m.cfg.Adapter
	m.mu.Unlock()
	if configurable, ok := adapter.(agent.Configurable); ok {
		configurable.SetBin(bin)
	}
}

// SetClaudeArgs hot-swaps the extra args passed to claude on spawn
// (Sa53137-3). See SetClaudeBin's doc comment for the S0e8afb-2 shared-
// adapter hot-swap mechanism.
func (m *Manager) SetClaudeArgs(args []string) {
	m.mu.Lock()
	m.cfg.ClaudeArgs = append([]string(nil), args...)
	adapter := m.cfg.Adapter
	m.mu.Unlock()
	if configurable, ok := adapter.(agent.Configurable); ok {
		configurable.SetArgs(args)
	}
}

// SetPermissionMode hot-swaps the claude --permission-mode value. Existing
// daemons pick it up on their next respawn (the value is read via a getter).
func (m *Manager) SetPermissionMode(mode string) {
	m.mu.Lock()
	m.cfg.PermissionMode = mode
	m.mu.Unlock()
}

// permissionModeGetter returns a closure the daemon calls at spawn time to read
// the current permission mode under the manager lock.
func (m *Manager) permissionModeGetter() func() string {
	return func() string {
		m.mu.Lock()
		defer m.mu.Unlock()
		return m.cfg.PermissionMode
	}
}

// NewManager creates a Manager.  cfg.ClaudeBin defaults to "claude".
func NewManager(cfg ManagerConfig) *Manager {
	if cfg.ClaudeBin == "" {
		cfg.ClaudeBin = "claude"
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Adapter == nil {
		// S0e8afb-2 graft default — see ManagerConfig.Adapter's doc comment.
		cfg.Adapter = agent.NewClaudeAdapter(cfg.ClaudeBin, cfg.ClaudeArgs)
	}
	return &Manager{
		cfg:     cfg,
		entries: make(map[string]*managerEntry),
	}
}

// Kind returns the agent kind this Manager's daemons spawn (S0e8afb-2 graft —
// always "claude" until S0e8afb-3 wires one Manager per registry kind).
// Exposed for the eventual per-kind-manager plumbing and for diagnostics.
func (m *Manager) Kind() agent.Kind {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.Adapter == nil {
		return agent.KindClaude
	}
	return m.cfg.Adapter.Kind()
}

// key builds the map key for (repoID, branchID, tabID). Sadf90e: tabID is
// included so multiple Claude tabs on the same branch get distinct entries.
func (m *Manager) key(repoID, branchID, tabID string) string {
	return repoID + "/" + branchID + "/" + tabID
}

// Get returns the Daemon for (repoID, branchID, tabID), or nil if none exists.
func (m *Manager) Get(repoID, branchID, tabID string) *Daemon {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[m.key(repoID, branchID, tabID)]
	if e == nil {
		return nil
	}
	return e.daemon
}

// EnsureDaemon returns the existing Daemon for (repoID, branchID, tabID) or
// creates a new one. The subprocess is NOT spawned yet — it starts lazily on
// the first WebSocket attach (priority_rule 4).
//
// worktree is the absolute path to the branch worktree. When non-empty,
// EnsureDaemon:
//   - Looks up the persisted session ID (if any) from the SessionStore and
//     pre-populates InitialSessionID in the new Daemon so the first respawn
//     uses --resume <id>.
//   - Starts a SessionWatcher on the transcript directory; when a new .jsonl
//     appears or is modified, the Manager calls Daemon.SetSessionID and
//     persists to the SessionStore.
//
// Passing an empty worktree silently skips session detection (useful in tests
// that don't need the fsnotify machinery).
func (m *Manager) EnsureDaemon(ctx context.Context, repoID, branchID, tabID, worktree string) (*Daemon, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(repoID, branchID, tabID)
	if e, ok := m.entries[k]; ok {
		return e.daemon, nil
	}

	// Look up the persisted session ID so we can pre-seed the new Daemon.
	// SessionStore is keyed by (repoID, branchID, tabID) since Sadf90e —
	// see store.go for the layout.
	var initialSessionID string
	if m.cfg.Store != nil {
		if sid, ok := m.cfg.Store.LoadActive(repoID, branchID, tabID); ok {
			initialSessionID = sid
		}
	}

	// S0e8afb-2 review fix: resolve the configured Adapter's SessionDiscoverer
	// capability ONCE, up front — both the resume-gate below and the
	// SessionWatcher startup further down must use the SAME adapter-supplied
	// transcript layout, never a hardcoded claude-specific one, or two
	// Managers of different kinds sharing one worktree could cross-wire a
	// session ID from one kind into another's --resume (the exact bug this
	// fix closes — see sessions.go's SessionIDFromPath doc comment).
	sessionDiscoverer, canDiscoverSessions := m.cfg.Adapter.(agent.SessionDiscoverer)

	// Only resume the persisted session on the FIRST spawn when its transcript
	// still exists on disk — otherwise `claude --resume <gone-id>` fails and we'd
	// drop the user into an error/blank claude. When the transcript is gone we
	// leave resumeInitial empty (fresh first spawn). This mirrors claude-agent's
	// transcript-gated resume so a palmux restart re-attaches to the prior
	// conversation without risking a stale-id failure. Skipped entirely when
	// the Adapter doesn't support session discovery — there is nothing to
	// gate against, so the first spawn is fresh (matches the pre-graft
	// behavior for adapters that never had this capability at all).
	resumeInitial := ""
	if initialSessionID != "" && worktree != "" && canDiscoverSessions {
		if td, err := sessionDiscoverer.TranscriptDir(worktree); err == nil {
			if transcriptExistsFor(sessionDiscoverer, td, initialSessionID) {
				resumeInitial = initialSessionID
			}
		}
	}

	d := NewDaemon(DaemonConfig{
		Adapter:              m.cfg.Adapter,
		PermissionModeFn:     m.permissionModeGetter(),
		Worktree:             worktree,
		RingSize:             m.cfg.RingSize,
		ResumeOnDeath:        m.cfg.ResumeOnDeath,
		InitialSessionID:     resumeInitial,
		Logger:               m.cfg.Logger.With("repo", repoID, "branch", branchID, "tab", tabID),
		NotifyHub:            m.cfg.NotifyHub,
		RepoID:               repoID,
		BranchID:             branchID,
		TabID:                tabID,
		NotifyURL:            m.cfg.NotifyURL,
		NotifyToken:          m.cfg.NotifyToken,
		HookBinPath:          m.cfg.HookBinPath,
		RuntimeResolver:      m.cfg.RuntimeResolver,
		NotifyURLInContainer: m.cfg.NotifyURLInContainer,
		PalmuxBin:            m.cfg.PalmuxBin,
		PtyHostLaunch:        m.cfg.PtyHostLaunch,
		RunDirOverride:       m.cfg.RunDirOverride,
		InstancePrefix:       m.cfg.InstancePrefix,
	})

	// Pre-seed session ID if we loaded one from the store. This unblocks
	// respawnLoop immediately so that if the daemon dies on first start, the
	// re-spawn already knows to use --resume. Kept unconditional (independent of
	// the transcript-gated resumeInitial above): in the normal case the fresh
	// first spawn creates a new session and the watcher overrides this id before
	// any respawn; this pre-seed only matters if the first spawn dies before that.
	if initialSessionID != "" {
		d.SetSessionID(initialSessionID)
	}

	entry := &managerEntry{daemon: d}

	// Start SessionWatcher when a worktree path was provided AND the
	// configured Adapter implements agent.SessionDiscoverer (S0e8afb-2
	// review fix — design doc §manager.go: "SessionWatcher-when-
	// SessionDiscoverer gate"). An adapter without this capability has no
	// well-defined transcript directory/naming scheme to watch at all — a
	// Manager for such a kind simply never detects sessions by fsnotify
	// (Capabilities().Resume should also be false for such an adapter, per
	// agent.Adapter's contract).
	if worktree != "" && canDiscoverSessions {
		td, err := sessionDiscoverer.TranscriptDir(worktree)
		if err == nil {
			w, werr := NewSessionWatcher(td, sessionDiscoverer.SessionIDFromPath)
			if werr == nil {
				entry.watcher = w
				go m.watcherLoop(repoID, branchID, tabID, d, w)
			} else {
				m.cfg.Logger.Warn("agenttui: failed to start session watcher",
					"repo", repoID, "branch", branchID, "tab", tabID, "err", werr)
			}
		} else {
			m.cfg.Logger.Warn("agenttui: adapter TranscriptDir failed",
				"repo", repoID, "branch", branchID, "tab", tabID, "err", err)
		}
	}

	m.entries[k] = entry
	return d, nil
}

// watcherLoop runs as a background goroutine and forwards SessionEvents from
// the watcher to the Daemon and SessionStore.
func (m *Manager) watcherLoop(repoID, branchID, tabID string, d *Daemon, w *SessionWatcher) {
	for ev := range w.Events() {
		if ev.SessionID == "" {
			continue
		}
		// Forward to the Daemon — this unblocks respawnLoop if it is waiting.
		d.SetSessionID(ev.SessionID)

		// Persist so the next Manager creation (after a server restart) can
		// pre-seed the new Daemon with the same session ID.
		if m.cfg.Store != nil {
			if err := m.cfg.Store.SetActive(repoID, branchID, tabID, ev.SessionID); err != nil {
				m.cfg.Logger.Warn("agenttui: failed to persist session ID",
					"repo", repoID, "branch", branchID, "tab", tabID,
					"session", ev.SessionID, "err", err)
			}
		}
		m.cfg.Logger.Info("agenttui: session ID detected",
			"repo", repoID, "branch", branchID, "tab", tabID,
			"session", ev.SessionID, "event", ev.EventType)
	}
}

// CloseDaemon shuts down and removes the Daemon for (repoID, branchID, tabID).
// No-op if no daemon exists. Called from the tab-removal handler so a deleted
// Claude(tui) tab does not leave a zombie process / watcher behind.
func (m *Manager) CloseDaemon(ctx context.Context, repoID, branchID, tabID string) error {
	m.mu.Lock()
	k := m.key(repoID, branchID, tabID)
	e, ok := m.entries[k]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	delete(m.entries, k)
	m.mu.Unlock()

	if e.watcher != nil {
		e.watcher.Close()
	}
	e.daemon.Shutdown()
	m.cfg.Logger.Info("agenttui: daemon closed",
		"repo", repoID,
		"branch", branchID,
		"tab", tabID,
	)
	return nil
}

// CloseBranchDaemons (Sadf90e) shuts down every Daemon belonging to the given
// branch. Called from Provider.OnBranchClose because the provider no longer
// owns the canonical tab list and instead asks the Manager to garbage-collect
// everything for the closing branch. Errors from individual daemons are
// logged, not propagated.
func (m *Manager) CloseBranchDaemons(ctx context.Context, repoID, branchID string) {
	prefix := repoID + "/" + branchID + "/"
	m.mu.Lock()
	var matched []*managerEntry
	for k, e := range m.entries {
		if len(k) > len(prefix) && k[:len(prefix)] == prefix {
			matched = append(matched, e)
			delete(m.entries, k)
		}
	}
	m.mu.Unlock()

	for _, e := range matched {
		if e.watcher != nil {
			e.watcher.Close()
		}
		e.daemon.Shutdown()
	}
	if len(matched) > 0 {
		m.cfg.Logger.Info("agenttui: branch daemons closed",
			"repo", repoID, "branch", branchID, "count", len(matched))
	}
}

// ShutdownAll shuts down (kills every ptyhost) all managed daemons. Callers
// intending a genuine, permanent teardown of every session use this — e.g. a
// test harness tearing down its fixtures. Do NOT call this from palmux2's own
// process-exit path (SIGTERM / self-update restart): that would kill every
// running claude on every restart, defeating ADR-0001/0002's entire purpose.
// See [Manager.DetachAll] for that case.
func (m *Manager) ShutdownAll(ctx context.Context) error {
	m.mu.Lock()
	entries := make(map[string]*managerEntry, len(m.entries))
	for k, e := range m.entries {
		entries[k] = e
	}
	m.entries = make(map[string]*managerEntry)
	m.mu.Unlock()

	var first error
	for k, e := range entries {
		if e.watcher != nil {
			e.watcher.Close()
		}
		e.daemon.Shutdown()
		m.cfg.Logger.Info("agenttui: daemon shut down", "key", k)
	}
	return first
}

// DetachAll disconnects from every managed daemon's ptyhost WITHOUT killing
// any of them (see [Daemon.Detach]) — the correct call for palmux2's own
// process-exit path (cmd/palmux/main.go's SIGTERM/SIGINT handler), so a
// palmux2 restart does not take down every running claude. Session watchers
// ARE stopped (they are palmux2-side fsnotify goroutines with nothing to do
// with ptyhost survival); a freshly (re)started palmux2 recreates them on the
// next EnsureDaemon.
func (m *Manager) DetachAll(ctx context.Context) error {
	m.mu.Lock()
	entries := make(map[string]*managerEntry, len(m.entries))
	for k, e := range m.entries {
		entries[k] = e
	}
	m.entries = make(map[string]*managerEntry)
	m.mu.Unlock()

	for k, e := range entries {
		if e.watcher != nil {
			e.watcher.Close()
		}
		e.daemon.Detach()
		m.cfg.Logger.Info("agenttui: daemon detached (ptyhost left running)", "key", k)
	}
	return nil
}

// RunDir (S3f2658-3) returns the directory this Manager's Daemons place
// ptyhost sockets/status files in — the SAME computation [Daemon.ptyHostPaths]
// uses (cfg.RunDirOverride if set, else ptyhost.RunDir(instancePrefix)) — so a
// discovery/GC pass driven from outside a specific Daemon (see discover.go)
// scans the exact directory this Manager's daemons actually use.
//
// NOTE: this is only meaningful when every Daemon this Manager creates
// resolves to the SAME directory, which requires either cfg.PalmuxBin (the
// production case: ptyhost.RunDir(instancePrefix) is process-wide, not
// per-Daemon) or an explicit cfg.RunDirOverride. The automatic in-process
// test fallback (PalmuxBin=="" && RunDirOverride=="") gives each Daemon its
// OWN unique temp directory for hermetic test isolation — discovery/GC
// against THIS Manager's RunDir() would see none of them, by design; tests
// exercising discovery/GC set RunDirOverride explicitly.
func (m *Manager) RunDir() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.cfg.RunDirOverride != "" {
		return m.cfg.RunDirOverride
	}
	prefix := m.cfg.InstancePrefix
	if prefix == "" {
		prefix = domain.PalmuxSessionPrefix
	}
	return ptyhost.RunDir(prefix)
}

// Len returns the number of currently managed daemons.
func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.entries)
}

// EnsureStarted is a convenience helper: EnsureDaemon then EnsureStarted.
// Useful for testing.  Pass empty worktree to skip session detection.
func (m *Manager) EnsureStarted(ctx context.Context, repoID, branchID, tabID string) (*Daemon, error) {
	d, err := m.EnsureDaemon(ctx, repoID, branchID, tabID, "")
	if err != nil {
		return nil, fmt.Errorf("agenttui manager: ensure daemon: %w", err)
	}
	if err := d.EnsureStarted(ctx); err != nil {
		return nil, fmt.Errorf("agenttui manager: ensure started: %w", err)
	}
	return d, nil
}
