package claudeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// CanonicalTabID is the tab id assigned to the first / default Claude tab
// on every branch. New branches and legacy data (pre-S009) resolve to
// this id. Subsequent tabs follow the bash convention:
// `claude:claude-2`, `claude:claude-3`, ...
const CanonicalTabID = "claude:claude"

// CanonicaliseTabID maps legacy / aliased ids to the canonical form.
// Empty string and bare type "claude" both map to `claude:claude` so
// older URL routes keep working. Already-qualified ids pass through.
func CanonicaliseTabID(tabID string) string {
	if tabID == "" || tabID == TabType {
		return CanonicalTabID
	}
	return tabID
}

// tabKey returns the persistence-store key used by Active / BranchPrefs.
// Format: `{repoId}/{branchId}/{tabId}`.
func tabKey(repoID, branchID, tabID string) string {
	return repoID + "/" + branchID + "/" + CanonicaliseTabID(tabID)
}

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
//
// S009: keys for `Active` and `BranchPrefs` are now `(repoID, branchID,
// tabID)` triples written as `{repoId}/{branchId}/{tabId}`. The legacy
// `{repoId}/{branchId}` keys are accepted on load and migrated to the
// canonical `claude:claude` tab so existing users keep their resume
// pointers and prefs across the upgrade.
//
// `BranchTabs` records which Claude tabs exist on each branch so the
// multi-tab layout survives a server restart. The canonical tab id
// `claude:claude` is implicit — every branch has at least one and the
// first lazy spawn auto-creates it.
type PersistedShape struct {
	Sessions    map[string]SessionMeta `json:"sessions"`
	Active      map[string]string      `json:"active"`           // tab-key → session_id
	BranchTabs  map[string][]string    `json:"branchTabs"`       // "{repoId}/{branchId}" → ordered tabIds
	LastInit    *InitInfo              `json:"lastInit,omitempty"`
	BranchPrefs map[string]BranchPrefs `json:"branchPrefs,omitempty"` // tab-key → prefs
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
	s.data.BranchTabs = map[string][]string{}
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
	if s.data.BranchTabs == nil {
		s.data.BranchTabs = map[string][]string{}
	}
	migrateLegacyTabKeys(&s.data)
	return nil
}

// migrateLegacyTabKeys folds pre-S009 `{repoId}/{branchId}` map keys
// (used in Active / BranchPrefs) into the new `{repoId}/{branchId}/claude:claude`
// shape. Ensures upgraded users keep their resume pointer and per-branch
// prefs without manual fixup. Idempotent: keys that already contain the
// tab dimension are left alone.
func migrateLegacyTabKeys(d *PersistedShape) {
	for k, v := range d.Active {
		if strings.Count(k, "/") == 1 { // "repoId/branchId" — no tab dim
			newKey := k + "/" + CanonicalTabID
			if _, exists := d.Active[newKey]; !exists {
				d.Active[newKey] = v
			}
			delete(d.Active, k)
		}
	}
	for k, v := range d.BranchPrefs {
		if strings.Count(k, "/") == 1 {
			newKey := k + "/" + CanonicalTabID
			if _, exists := d.BranchPrefs[newKey]; !exists {
				d.BranchPrefs[newKey] = v
			}
			delete(d.BranchPrefs, k)
		}
	}
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

// ActiveFor returns the session_id Palmux should resume for this tab,
// or "" if the tab has never been touched. tabID may be the legacy
// bare "claude" or empty — both map to the canonical first tab.
func (s *Store) ActiveFor(repoID, branchID, tabID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.Active[tabKey(repoID, branchID, tabID)]
}

// SetActive points the tab at sessionID and upserts a meta record.
func (s *Store) SetActive(repoID, branchID, tabID, sessionID, model string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	s.data.Active[tabKey(repoID, branchID, tabID)] = sessionID
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

// ClearActive forgets the active session_id for the given tab. The
// transcript is left intact on disk under ~/.claude/projects/.
func (s *Store) ClearActive(repoID, branchID, tabID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data.Active, tabKey(repoID, branchID, tabID))
	return s.save()
}

// ──────────── BranchTabs (S009 multi-tab persistence) ────────────────────

// BranchTabs returns the persisted ordered list of Claude tab IDs for a
// branch. Empty result means the branch has never had a Claude tab
// recorded — callers should treat that as "implicit canonical only" and
// auto-seed `CanonicalTabID`.
func (s *Store) BranchTabs(repoID, branchID string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]string(nil), s.data.BranchTabs[branchID2Key(repoID, branchID)]...)
}

// SetBranchTabs persists the ordered tab list for a branch.
func (s *Store) SetBranchTabs(repoID, branchID string, tabIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.BranchTabs == nil {
		s.data.BranchTabs = map[string][]string{}
	}
	if len(tabIDs) == 0 {
		delete(s.data.BranchTabs, branchID2Key(repoID, branchID))
	} else {
		s.data.BranchTabs[branchID2Key(repoID, branchID)] = append([]string(nil), tabIDs...)
	}
	return s.save()
}

// branchID2Key returns the BranchTabs map key (branch-only, no tab).
func branchID2Key(repoID, branchID string) string { return repoID + "/" + branchID }

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

// BranchPrefs returns the persisted overrides for a tab. Missing entry
// yields the zero-value (all empty strings → use defaults). Despite the
// name, since S009 prefs are per-tab; the function name is preserved
// because the public Manager API rooted on it predates multi-tab and
// renaming would churn many callers. Callers pass the tabID explicitly.
func (s *Store) BranchPrefs(repoID, branchID, tabID string) BranchPrefs {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.data.BranchPrefs[tabKey(repoID, branchID, tabID)]
}

// SetBranchPrefs upserts the per-tab overrides. Empty fields are preserved
// as empty (= "follow defaults"), so callers should pass the full
// intended state — partial merges aren't done here.
func (s *Store) SetBranchPrefs(repoID, branchID, tabID string, prefs BranchPrefs) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.data.BranchPrefs == nil {
		s.data.BranchPrefs = map[string]BranchPrefs{}
	}
	s.data.BranchPrefs[tabKey(repoID, branchID, tabID)] = prefs
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

// branchKey is unused after the S009 tab-keyed migration but kept for
// debugging and future helpers that need a branch-only key. Sessions and
// prefs both go through `tabKey` now.
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
