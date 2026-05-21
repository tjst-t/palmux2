package claudetui

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// ManagerConfig holds configuration shared by all daemons in the Manager.
type ManagerConfig struct {
	// ClaudeBin is the path to the claude binary (default: "claude").
	ClaudeBin string
	// ClaudeArgs are additional arguments appended to every daemon spawn.
	ClaudeArgs []string
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
}

// managerEntry bundles a Daemon with its associated SessionWatcher so both
// can be stopped together in CloseDaemon / ShutdownAll.
type managerEntry struct {
	daemon  *Daemon
	watcher *SessionWatcher // may be nil if no worktree was provided
}

// Manager holds one [Daemon] per (repoID, branchID) pair and provides
// lifecycle methods that mirror the claudeagent.Manager pattern so that Story
// 2's Provider implementation can be a thin adapter.
//
// Thread safety: all methods are safe for concurrent use.
type Manager struct {
	cfg     ManagerConfig
	mu      sync.Mutex
	entries map[string]*managerEntry
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

// key builds the map key for (repoID, branchID).
func (m *Manager) key(repoID, branchID string) string {
	return repoID + "/" + branchID
}

// Get returns the Daemon for (repoID, branchID), or nil if none exists.
func (m *Manager) Get(repoID, branchID string) *Daemon {
	m.mu.Lock()
	defer m.mu.Unlock()
	e := m.entries[m.key(repoID, branchID)]
	if e == nil {
		return nil
	}
	return e.daemon
}

// EnsureDaemon returns the existing Daemon for (repoID, branchID) or creates a
// new one.  The subprocess is NOT spawned yet — it starts lazily on the first
// WebSocket attach (priority_rule 4).
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
func (m *Manager) EnsureDaemon(ctx context.Context, repoID, branchID, worktree string) (*Daemon, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(repoID, branchID)
	if e, ok := m.entries[k]; ok {
		return e.daemon, nil
	}

	// Look up the persisted session ID so we can pre-seed the new Daemon.
	var initialSessionID string
	if m.cfg.Store != nil {
		if sid, ok := m.cfg.Store.LoadActive(repoID, branchID); ok {
			initialSessionID = sid
		}
	}

	d := NewDaemon(DaemonConfig{
		ClaudeBin:     m.cfg.ClaudeBin,
		ClaudeArgs:    m.cfg.ClaudeArgs,
		Worktree:      worktree,
		RingSize:      m.cfg.RingSize,
		ResumeOnDeath: m.cfg.ResumeOnDeath,
		Logger:        m.cfg.Logger.With("repo", repoID, "branch", branchID),
	})

	// Pre-seed session ID if we loaded one from the store.  This unblocks
	// respawnLoop immediately so that if the daemon dies on first start
	// (e.g. because claude itself wasn't installed yet), the re-spawn
	// already knows to use --resume.
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
				go m.watcherLoop(repoID, branchID, d, w)
			} else {
				m.cfg.Logger.Warn("claudetui: failed to start session watcher",
					"repo", repoID, "branch", branchID, "err", werr)
			}
		} else {
			m.cfg.Logger.Warn("claudetui: TranscriptDir failed",
				"repo", repoID, "branch", branchID, "err", err)
		}
	}

	m.entries[k] = entry
	return d, nil
}

// watcherLoop runs as a background goroutine and forwards SessionEvents from
// the watcher to the Daemon and SessionStore.
func (m *Manager) watcherLoop(repoID, branchID string, d *Daemon, w *SessionWatcher) {
	for ev := range w.Events() {
		if ev.SessionID == "" {
			continue
		}
		// Forward to the Daemon — this unblocks respawnLoop if it is waiting.
		d.SetSessionID(ev.SessionID)

		// Persist so the next Manager creation (after a server restart) can
		// pre-seed the new Daemon with the same session ID.
		if m.cfg.Store != nil {
			if err := m.cfg.Store.SetActive(repoID, branchID, ev.SessionID); err != nil {
				m.cfg.Logger.Warn("claudetui: failed to persist session ID",
					"repo", repoID, "branch", branchID,
					"session", ev.SessionID, "err", err)
			}
		}
		m.cfg.Logger.Info("claudetui: session ID detected",
			"repo", repoID, "branch", branchID,
			"session", ev.SessionID, "event", ev.EventType)
	}
}

// CloseDaemon shuts down and removes the Daemon for (repoID, branchID).  It is
// a no-op if no daemon exists.  This is called by Story 2's OnBranchClose.
func (m *Manager) CloseDaemon(ctx context.Context, repoID, branchID string) error {
	m.mu.Lock()
	k := m.key(repoID, branchID)
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
	)
	return nil
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
func (m *Manager) EnsureStarted(ctx context.Context, repoID, branchID string) (*Daemon, error) {
	d, err := m.EnsureDaemon(ctx, repoID, branchID, "")
	if err != nil {
		return nil, fmt.Errorf("claudetui manager: ensure daemon: %w", err)
	}
	if err := d.EnsureStarted(ctx); err != nil {
		return nil, fmt.Errorf("claudetui manager: ensure started: %w", err)
	}
	return d, nil
}
