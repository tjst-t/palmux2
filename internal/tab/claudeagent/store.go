package claudeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// SessionMeta is the per-session record persisted to sessions.json. It does
// NOT hold the conversation transcript (the CLI owns that under
// ~/.claude/projects/) — only the bookkeeping Palmux needs to map branches
// back to session_ids and render the history popup.
type SessionMeta struct {
	ID              string    `json:"id"`
	RepoID          string    `json:"repoId"`
	BranchID        string    `json:"branchId"`
	Title           string    `json:"title,omitempty"`
	Model           string    `json:"model,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
	LastActivityAt  time.Time `json:"lastActivityAt"`
	TurnCount       int       `json:"turnCount"`
	TotalCostUSD    float64   `json:"totalCostUsd"`
	ParentSessionID string    `json:"parentSessionId,omitempty"`
	// Optional, only filled by enriched-list endpoints — read on demand
	// from the CLI's transcript so the History popup can show what each
	// session was about without persisting redundant copies.
	FirstUserMessage     string `json:"firstUserMessage,omitempty"`
	LastUserMessage      string `json:"lastUserMessage,omitempty"`
	LastAssistantSnippet string `json:"lastAssistantSnippet,omitempty"`
}

// PersistedShape is the on-disk JSON layout for sessions.json.
type PersistedShape struct {
	Sessions map[string]SessionMeta `json:"sessions"`
	Active   map[string]string      `json:"active"` // "{repoId}/{branchId}" → session_id
	// LastInit is the most recent CLI initialize response we observed.
	// Cached here so the slash-command popup, model list, and agent list
	// are populated even before the first lazy spawn on a fresh server.
	LastInit *InitInfo `json:"lastInit,omitempty"`
	// BranchPrefs persists user-tweaked model / effort / permission mode
	// per branch. Read on EnsureAgent; written every time the user picks
	// a different value in the composer.
	BranchPrefs map[string]BranchPrefs `json:"branchPrefs,omitempty"`
}

// BranchPrefs is the per-branch overrides for the Claude tab. Empty
// strings fall through to Manager.Config defaults.
type BranchPrefs struct {
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	PermissionMode string `json:"permissionMode,omitempty"`
	// IncludeHookEvents toggles --include-hook-events on the CLI for this
	// branch. Default false (opt-in). When the user flips this in the
	// Settings popup, the agent respawns with the new flag so the CLI
	// starts (or stops) emitting hook lifecycle envelopes.
	IncludeHookEvents bool `json:"includeHookEvents,omitempty"`
}

// Store wraps sessions.json with the same atomic-write discipline as the
// repos/settings stores.
type Store struct {
	path string

	mu   sync.RWMutex
	data PersistedShape
}

// NewStore loads (or initialises) sessions.json under dir.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("claudeagent: mkdir %s: %w", dir, err)
	}
	s := &Store{path: filepath.Join(dir, "sessions.json")}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	s.data.Sessions = map[string]SessionMeta{}
	s.data.Active = map[string]string{}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("claudeagent: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return fmt.Errorf("claudeagent: parse %s: %w", s.path, err)
	}
	if s.data.Sessions == nil {
		s.data.Sessions = map[string]SessionMeta{}
	}
	if s.data.Active == nil {
		s.data.Active = map[string]string{}
	}
	return nil
}

func (s *Store) save() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

// ActiveFor returns the session_id Palmux should resume for this branch,
// or "" if the branch has never been touched.
func (s *Store) ActiveFor(repoID, branchID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Active[branchKey(repoID, branchID)]
}

// SetActive points the branch at sessionID and upserts a meta record.
func (s *Store) SetActive(repoID, branchID, sessionID, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.data.Active[branchKey(repoID, branchID)] = sessionID
	if existing, ok := s.data.Sessions[sessionID]; ok {
		existing.LastActivityAt = now
		if model != "" {
			existing.Model = model
		}
		s.data.Sessions[sessionID] = existing
	} else {
		s.data.Sessions[sessionID] = SessionMeta{
			ID:             sessionID,
			RepoID:         repoID,
			BranchID:       branchID,
			Model:          model,
			CreatedAt:      now,
			LastActivityAt: now,
		}
	}
	return s.save()
}

// ClearActive forgets the active session_id for the given branch. The
// transcript is left intact on disk under ~/.claude/projects/.
func (s *Store) ClearActive(repoID, branchID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Active, branchKey(repoID, branchID))
	return s.save()
}

// UpdateMeta merges a callback's changes into a SessionMeta entry. No-op if
// the session is absent.
func (s *Store) UpdateMeta(sessionID string, fn func(*SessionMeta)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.data.Sessions[sessionID]
	if !ok {
		return nil
	}
	fn(&m)
	s.data.Sessions[sessionID] = m
	return s.save()
}

// Get returns a copy of the meta for sessionID, or zero value + false.
func (s *Store) Get(sessionID string) (SessionMeta, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	m, ok := s.data.Sessions[sessionID]
	return m, ok
}

// List returns sessions filtered by branch (empty branchID = all). Sorted
// most-recent-first by LastActivityAt.
func (s *Store) List(repoID, branchID string) []SessionMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]SessionMeta, 0, len(s.data.Sessions))
	for _, m := range s.data.Sessions {
		if repoID != "" && m.RepoID != repoID {
			continue
		}
		if branchID != "" && m.BranchID != branchID {
			continue
		}
		out = append(out, m)
	}
	sortByActivity(out)
	return out
}

// BranchPrefs returns the persisted overrides for a branch. Missing
// entry yields the zero-value (all empty strings → use defaults).
func (s *Store) BranchPrefs(repoID, branchID string) BranchPrefs {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.BranchPrefs[branchKey(repoID, branchID)]
}

// SetBranchPrefs upserts the per-branch overrides. Empty fields are
// preserved as empty (= "follow defaults"), so callers should pass the
// full intended state — partial merges aren't done here.
func (s *Store) SetBranchPrefs(repoID, branchID string, prefs BranchPrefs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.BranchPrefs == nil {
		s.data.BranchPrefs = map[string]BranchPrefs{}
	}
	s.data.BranchPrefs[branchKey(repoID, branchID)] = prefs
	return s.save()
}

// SetLastInit caches the most recent CLI initialize payload so the next
// agent (after a server restart, before its CLI spawns) can hand it back
// to the UI for the slash-command popup, model list, etc.
func (s *Store) SetLastInit(info InitInfo) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.LastInit = &info
	return s.save()
}

// LastInit returns the cached init info, or zero-value if none yet.
func (s *Store) LastInit() InitInfo {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.data.LastInit == nil {
		return InitInfo{}
	}
	return *s.data.LastInit
}

// Delete drops the session record and any active pointer that referenced it.
func (s *Store) Delete(sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Sessions, sessionID)
	for k, v := range s.data.Active {
		if v == sessionID {
			delete(s.data.Active, k)
		}
	}
	return s.save()
}

func branchKey(repoID, branchID string) string { return repoID + "/" + branchID }

func sortByActivity(xs []SessionMeta) {
	// hand-rolled insertion sort is fine here; lists are tiny.
	for i := 1; i < len(xs); i++ {
		j := i
		for j > 0 && xs[j].LastActivityAt.After(xs[j-1].LastActivityAt) {
			xs[j], xs[j-1] = xs[j-1], xs[j]
			j--
		}
	}
}
