package claudetui

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/tjst-t/palmux2/internal/notify"
	"github.com/tjst-t/palmux2/internal/runtime"
)

// ManagerConfig holds configuration shared by all daemons in the Manager.
type ManagerConfig struct {
	// ClaudeBin is the path to the claude binary (default: "claude").
	ClaudeBin string
	// ClaudeArgs are additional arguments appended to every daemon spawn.
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
// call (Sa53137-3 hot apply). Existing daemons keep their binary until respawn.
func (m *Manager) SetClaudeBin(bin string) {
	if bin == "" {
		return
	}
	m.mu.Lock()
	m.cfg.ClaudeBin = bin
	m.mu.Unlock()
}

// SetClaudeArgs hot-swaps the extra args passed to claude on spawn (Sa53137-3).
func (m *Manager) SetClaudeArgs(args []string) {
	m.mu.Lock()
	m.cfg.ClaudeArgs = append([]string(nil), args...)
	m.mu.Unlock()
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
	return &Manager{
		cfg:     cfg,
		entries: make(map[string]*managerEntry),
	}
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

	// Only resume the persisted session on the FIRST spawn when its transcript
	// still exists on disk — otherwise `claude --resume <gone-id>` fails and we'd
	// drop the user into an error/blank claude. When the transcript is gone we
	// leave resumeInitial empty (fresh first spawn). This mirrors claude-agent's
	// transcript-gated resume so a palmux restart re-attaches to the prior
	// conversation without risking a stale-id failure.
	resumeInitial := ""
	if initialSessionID != "" && worktree != "" && transcriptExists(worktree, initialSessionID) {
		resumeInitial = initialSessionID
	}

	d := NewDaemon(DaemonConfig{
		ClaudeBin:            m.cfg.ClaudeBin,
		ClaudeArgs:           m.cfg.ClaudeArgs,
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

	// Start SessionWatcher when a worktree path was provided.
	if worktree != "" {
		td, err := TranscriptDir(worktree)
		if err == nil {
			w, werr := NewSessionWatcher(td)
			if werr == nil {
				entry.watcher = w
				go m.watcherLoop(repoID, branchID, tabID, d, w)
			} else {
				m.cfg.Logger.Warn("claudetui: failed to start session watcher",
					"repo", repoID, "branch", branchID, "tab", tabID, "err", werr)
			}
		} else {
			m.cfg.Logger.Warn("claudetui: TranscriptDir failed",
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
				m.cfg.Logger.Warn("claudetui: failed to persist session ID",
					"repo", repoID, "branch", branchID, "tab", tabID,
					"session", ev.SessionID, "err", err)
			}
		}
		m.cfg.Logger.Info("claudetui: session ID detected",
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
	m.cfg.Logger.Info("claudetui: daemon closed",
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
		m.cfg.Logger.Info("claudetui: branch daemons closed",
			"repo", repoID, "branch", branchID, "count", len(matched))
	}
}

// ShutdownAll shuts down all managed daemons.  Should be called on process
// exit (e.g. from Provider.OnBranchClose for every open branch, or from
// main.go cleanup).
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
		m.cfg.Logger.Info("claudetui: daemon shut down", "key", k)
	}
	return first
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
		return nil, fmt.Errorf("claudetui manager: ensure daemon: %w", err)
	}
	if err := d.EnsureStarted(ctx); err != nil {
		return nil, fmt.Errorf("claudetui manager: ensure started: %w", err)
	}
	return d, nil
}
