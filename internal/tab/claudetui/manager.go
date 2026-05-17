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
}

// Manager holds one [Daemon] per (repoID, branchID) pair and provides
// lifecycle methods that mirror the claudeagent.Manager pattern so that Story
// 2's Provider implementation can be a thin adapter.
//
// Thread safety: all methods are safe for concurrent use.
type Manager struct {
	cfg    ManagerConfig
	mu     sync.Mutex
	daemons map[string]*Daemon
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
		daemons: make(map[string]*Daemon),
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
	return m.daemons[m.key(repoID, branchID)]
}

// EnsureDaemon returns the existing Daemon for (repoID, branchID) or creates a
// new one.  The subprocess is NOT spawned yet — it starts lazily on the first
// WebSocket attach (priority_rule 4).
func (m *Manager) EnsureDaemon(ctx context.Context, repoID, branchID string) (*Daemon, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := m.key(repoID, branchID)
	if d, ok := m.daemons[k]; ok {
		return d, nil
	}
	d := NewDaemon(DaemonConfig{
		ClaudeBin:     m.cfg.ClaudeBin,
		ClaudeArgs:    m.cfg.ClaudeArgs,
		RingSize:      m.cfg.RingSize,
		ResumeOnDeath: m.cfg.ResumeOnDeath,
		Logger:        m.cfg.Logger.With("repo", repoID, "branch", branchID),
	})
	m.daemons[k] = d
	return d, nil
}

// CloseDaemon shuts down and removes the Daemon for (repoID, branchID).  It is
// a no-op if no daemon exists.  This is called by Story 2's OnBranchClose.
func (m *Manager) CloseDaemon(ctx context.Context, repoID, branchID string) error {
	m.mu.Lock()
	k := m.key(repoID, branchID)
	d, ok := m.daemons[k]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	delete(m.daemons, k)
	m.mu.Unlock()

	d.Shutdown()
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
	daemons := make(map[string]*Daemon, len(m.daemons))
	for k, d := range m.daemons {
		daemons[k] = d
	}
	m.daemons = make(map[string]*Daemon)
	m.mu.Unlock()

	var first error
	for k, d := range daemons {
		d.Shutdown()
		m.cfg.Logger.Info("claudetui: daemon shut down", "key", k)
	}
	return first
}

// Len returns the number of currently managed daemons.
func (m *Manager) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.daemons)
}

// EnsureStarted is a convenience helper: EnsureDaemon then EnsureStarted.
// Useful for testing.
func (m *Manager) EnsureStarted(ctx context.Context, repoID, branchID string) (*Daemon, error) {
	d, err := m.EnsureDaemon(ctx, repoID, branchID)
	if err != nil {
		return nil, fmt.Errorf("claudetui manager: ensure daemon: %w", err)
	}
	if err := d.EnsureStarted(ctx); err != nil {
		return nil, fmt.Errorf("claudetui manager: ensure started: %w", err)
	}
	return d, nil
}
