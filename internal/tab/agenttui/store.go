package agenttui

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

// tuiSessionsFileName is the filename for claudetui's own session persistence.
// Separate from claudeagent's sessions.json to keep failure modes isolated
// and avoid tight cross-package coupling.
const tuiSessionsFileName = "claudetui-sessions.json"

// tuiPersistedShape is the on-disk layout for claudetui-sessions.json.
type tuiPersistedShape struct {
	// Active maps "{repoId}/{branchId}" → session_id.
	Active map[string]string `json:"active"`
	// BranchTabs (S0e8afb-3) persists the per-branch generic-agent tab
	// layout, namespaced by kind so a non-claude kind's tabs (e.g.
	// "generic:generic-2") survive a server restart independently of
	// claude's own (claudeagent-owned) tab list. Key:
	// "<kind>/{repoId}/{branchId}" → ordered tabIds. claude never writes
	// here — its tab list lives in claudeagent.Store.BranchTabs (a separate
	// persistence file). Additive field: omitted entirely from the JSON when
	// empty, so an existing claudetui-sessions.json written before this
	// field existed round-trips unchanged.
	BranchTabs map[string][]string `json:"branchTabs,omitempty"`
}

// SessionStore persists claudetui's active session IDs using atomic
// write-via-rename discipline identical to claudeagent.Store.
//
// The file is stored under the same config directory as sessions.json, but
// with filename "claudetui-sessions.json" to keep the two tabs isolated.
type SessionStore struct {
	path string

	mu   sync.RWMutex
	data tuiPersistedShape
}

// NewSessionStore loads (or initialises) claudetui-sessions.json under dir.
// dir is typically the same configDir passed to claudeagent.NewStore.
func NewSessionStore(dir string) (*SessionStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("claudetui store: mkdir %s: %w", dir, err)
	}
	s := &SessionStore{path: filepath.Join(dir, tuiSessionsFileName)}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *SessionStore) load() error {
	s.data.Active = map[string]string{}
	s.data.BranchTabs = map[string][]string{}
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("claudetui store: read %s: %w", s.path, err)
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		// Corruption recovery: log and start fresh.
		s.data.Active = map[string]string{}
		s.data.BranchTabs = map[string][]string{}
		return nil
	}
	if s.data.Active == nil {
		s.data.Active = map[string]string{}
	}
	if s.data.BranchTabs == nil {
		s.data.BranchTabs = map[string][]string{}
	}
	return nil
}

func (s *SessionStore) save() error {
	b, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return fmt.Errorf("claudetui store: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("claudetui store: write tmp: %w", err)
	}
	return os.Rename(tmp, s.path)
}

// storeKey builds the persistence key for (repoID, branchID, tabID). Sadf90e
// added tabID so per-tab Claude(tui) sessions persist independently.
//
// Forward-compatibility note: pre-Sadf90e keys were "{repoID}/{branchID}"
// (no tabID). Those entries are silently ignored on load — re-resume from the
// new key, and an empty tab will start a fresh claude session, which matches
// the user-approved migration behaviour for branch-level claude_mode.
func storeKey(repoID, branchID, tabID string) string {
	return repoID + "/" + branchID + "/" + tabID
}

// LoadActive returns the last-known session ID for (repoID, branchID, tabID),
// or ("", false) if none has been persisted.
func (s *SessionStore) LoadActive(repoID, branchID, tabID string) (sessionID string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, exists := s.data.Active[storeKey(repoID, branchID, tabID)]
	if !exists || v == "" {
		return "", false
	}
	return v, true
}

// SetActive records sessionID as the active session for (repoID, branchID,
// tabID) and persists immediately.
func (s *SessionStore) SetActive(repoID, branchID, tabID, sessionID string) error {
	if sessionID == "" {
		return fmt.Errorf("claudetui store: SetActive: empty sessionID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data.Active[storeKey(repoID, branchID, tabID)] = sessionID
	return s.save()
}

// ClearActive removes the active session pointer for (repoID, branchID, tabID).
func (s *SessionStore) ClearActive(repoID, branchID, tabID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Active, storeKey(repoID, branchID, tabID))
	return s.save()
}

// ──────── BranchTabs (S0e8afb-3: kind-namespaced generic-agent tab layout) ────────

// branchTabsKey builds the BranchTabs map key for one (kind, repoID,
// branchID) triple.
func branchTabsKey(kind, repoID, branchID string) string {
	return kind + "/" + repoID + "/" + branchID
}

// BranchTabs returns the persisted ordered list of tab IDs of the given
// generic agent kind for a branch. Empty result means the branch has never
// had a tab of this kind recorded — callers should treat that as "implicit
// canonical only" and auto-seed "<kind>:<kind>" (mirrors
// claudeagent.Store.BranchTabs).
func (s *SessionStore) BranchTabs(kind, repoID, branchID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.data.BranchTabs[branchTabsKey(kind, repoID, branchID)]...)
}

// SetBranchTabs persists the ordered tab-id list of the given kind for a
// branch. An empty/nil list is persisted explicitly (not deleted — see
// HasBranchTabs) so a branch the user intentionally emptied is
// distinguishable from a fresh branch that was never seeded.
func (s *SessionStore) SetBranchTabs(kind, repoID, branchID string, tabIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.BranchTabs == nil {
		s.data.BranchTabs = map[string][]string{}
	}
	key := branchTabsKey(kind, repoID, branchID)
	s.data.BranchTabs[key] = append([]string{}, tabIDs...)
	return s.save()
}

// HasBranchTabs reports whether a tab list has ever been recorded for this
// (kind, branch) — i.e. the branch has been touched (a tab added or
// removed). A fresh branch returns false so the provider seeds its
// canonical tab; once true, the persisted list (possibly empty) is
// authoritative.
func (s *SessionStore) HasBranchTabs(kind, repoID, branchID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	_, ok := s.data.BranchTabs[branchTabsKey(kind, repoID, branchID)]
	return ok
}
