// Package config persists Palmux's source-of-truth files: repos.json (the
// list of Open repositories) and settings.json (global, shared across
// devices).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// ClaudeMode is the per-branch claude tab implementation selector.
// "agent" = claude-agent (stream-json), "tui" = claude-tui (PTY).
type ClaudeMode string

const (
	ClaudeModeAgent ClaudeMode = "agent"
	ClaudeModeTui   ClaudeMode = "tui"
)

// BranchSettings holds per-branch settings that are persisted in repos.json.
// Keyed by branch ID (not name) because settings are identity-bound to the
// worktree, not to the git head. Outer key = branchId.
//
// Sadf90e (post-S1f75ec rework): the previous BranchSettings.ClaudeMode field
// was a branch-wide flag, which meant every Claude tab in a branch was forced
// into the same mode. That broke the basic UX expectation that two Claude tabs
// in the same workspace can be different modes (one agent, one tui). We now
// store mode per-tab in TabClaudeModes (key = tabID). Missing entries fall
// through to settings.claude.default_mode at tab-add time.
//
// Old `claude_mode` field is intentionally NOT read on load — pre-Sadf90e
// values are discarded as part of the migration the user explicitly approved.
type BranchSettings struct {
	// TabClaudeModes maps Claude tabID → mode. Set at tab-add time via
	// InitTabClaudeMode, mutated by SetTabClaudeMode, cleared by
	// DeleteTabClaudeMode when the tab is removed.
	TabClaudeModes map[string]ClaudeMode `json:"tab_claude_modes,omitempty"`
}

// RepoEntry is one row in repos.json.
//
// UserOpenedBranches (S015) records the branch names the user opened
// explicitly through Palmux (via Drawer "Open Branch…" or via the
// `+ Add to my worktrees` promote action). The Drawer reads this to
// classify branches as `user` (in the slice), `subagent` (path matches
// `autoWorktreePathPatterns`), or `unmanaged` (otherwise). The field is
// `omitempty` so pre-S015 `repos.json` files load unchanged and so the
// JSON stays tidy for repos that have never had branches promoted.
//
// TabOverrides (S020) records per-branch tab customisation (rename + reorder)
// for `Multiple()=true` tab types. Outer key is the branch name (not branch
// ID — branch IDs are slug+hash and stable for a given branch name, but
// keying on the human-readable name keeps the JSON readable and resilient
// to hash collisions on rebuild). Inner key is the tab ID
// (e.g. `bash:dev-server`). `Order` is a per-branch slice of tab IDs
// expressing the user's preferred ordering within each Multiple()=true
// group; tabs not listed fall back to default ordering at the end.
//
// LastActiveBranch (S023) is the most recent branch the user navigated to
// inside this repo. Used by the Drawer to navigate-and-expand a collapsed
// repo on a single click of the header / chevron without forcing the user
// to drill in. Empty / omitted means "no remembered branch" (first-time
// open, or the previous branch was reconciled away). Stored as a branch
// **name** (not ID) so the value survives a hash regeneration of the
// branch ID and stays human-readable in repos.json.
//
// BranchSettingsMap (S1f75ec-2) holds per-branch settings keyed by branchId.
// On load, any existing branch that is missing an entry is back-filled with
// ClaudeMode="agent" (migration-safe default). New branches receive the
// default from global settings (see SettingsStore.ClaudeDefaultMode).
type RepoEntry struct {
	ID                 string                        `json:"id"`
	GHQPath            string                        `json:"ghqPath"`
	Starred            bool                          `json:"starred"`
	UserOpenedBranches []string                      `json:"userOpenedBranches,omitempty"`
	TabOverrides       map[string]BranchTabOverrides `json:"tabOverrides,omitempty"`
	LastActiveBranch   string                        `json:"last_active_branch,omitempty"`
	BranchSettingsMap  map[string]BranchSettings     `json:"branchSettings,omitempty"`
}

// BranchTabOverrides is the per-branch payload of TabOverrides.
type BranchTabOverrides struct {
	// Names maps tabID → user-friendly name. Empty string means "no override".
	Names map[string]string `json:"names,omitempty"`
	// Order is a flat list of tab IDs giving the user's preferred order.
	// IDs from different Multiple()=true groups are allowed (Claude tabs
	// and Bash tabs each cluster within their own group; the server
	// preserves group adjacency when reading this).
	Order []string `json:"order,omitempty"`
}

// RepoStore is the read/write interface for repos.json. All access goes
// through the mutex; writes are atomic via tempfile+rename.
type RepoStore struct {
	path string

	mu      sync.RWMutex
	entries []RepoEntry
}

// NewRepoStore returns a store rooted at dir/repos.json. The file is loaded
// eagerly so callers see consistent state from the first call.
func NewRepoStore(dir string) (*RepoStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("config: mkdir %s: %w", dir, err)
	}
	s := &RepoStore{path: filepath.Join(dir, "repos.json")}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *RepoStore) load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.entries = nil
			return nil
		}
		return fmt.Errorf("config: read %s: %w", s.path, err)
	}
	var entries []RepoEntry
	if err := json.Unmarshal(b, &entries); err != nil {
		return fmt.Errorf("config: parse %s: %w", s.path, err)
	}
	s.entries = entries
	return nil
}

func (s *RepoStore) save() error {
	b, err := json.MarshalIndent(s.entries, "", "  ")
	if err != nil {
		return fmt.Errorf("config: marshal: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("config: rename %s: %w", s.path, err)
	}
	return nil
}

// All returns a copy of the current entries.
func (s *RepoStore) All() []RepoEntry {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]RepoEntry, len(s.entries))
	copy(out, s.entries)
	return out
}

// Get returns the entry with the given ID, or false if absent.
func (s *RepoStore) Get(id string) (RepoEntry, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ID == id {
			return e, true
		}
	}
	return RepoEntry{}, false
}

// Add inserts (or returns the existing) repo entry. Returns true if newly
// inserted, false if it already existed.
func (s *RepoStore) Add(e RepoEntry) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, existing := range s.entries {
		if existing.ID == e.ID {
			return false, nil
		}
	}
	s.entries = append(s.entries, e)
	sortEntries(s.entries)
	return true, s.save()
}

// Remove deletes the entry with the given ID. Returns true if removed, false
// if absent.
func (s *RepoStore) Remove(id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, e := range s.entries {
		if e.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return false, nil
	}
	s.entries = append(s.entries[:idx], s.entries[idx+1:]...)
	return true, s.save()
}

// AddUserOpenedBranch (S015) records `branchName` as user-opened for the
// given repo. Idempotent — duplicates are dropped silently. Returns
// (added, error): `added` is false when the branch was already in the
// slice. Caller should treat (false, nil) as success.
func (s *RepoStore) AddUserOpenedBranch(repoID, branchName string) (bool, error) {
	if branchName == "" {
		return false, fmt.Errorf("config: AddUserOpenedBranch: empty branch name")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID != repoID {
			continue
		}
		for _, existing := range s.entries[i].UserOpenedBranches {
			if existing == branchName {
				return false, nil
			}
		}
		s.entries[i].UserOpenedBranches = append(s.entries[i].UserOpenedBranches, branchName)
		sort.Strings(s.entries[i].UserOpenedBranches)
		return true, s.save()
	}
	return false, fmt.Errorf("config: AddUserOpenedBranch: repo %q not found", repoID)
}

// RemoveUserOpenedBranch (S015) drops `branchName` from the user-opened
// list. Idempotent. Returns (removed, error).
func (s *RepoStore) RemoveUserOpenedBranch(repoID, branchName string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID != repoID {
			continue
		}
		idx := -1
		for j, existing := range s.entries[i].UserOpenedBranches {
			if existing == branchName {
				idx = j
				break
			}
		}
		if idx < 0 {
			return false, nil
		}
		s.entries[i].UserOpenedBranches = append(
			s.entries[i].UserOpenedBranches[:idx],
			s.entries[i].UserOpenedBranches[idx+1:]...,
		)
		return true, s.save()
	}
	return false, fmt.Errorf("config: RemoveUserOpenedBranch: repo %q not found", repoID)
}

// ReplaceUserOpenedBranches (S015) overwrites the user-opened slice for one
// repo. Used by the startup reconcile to drop entries whose worktree is no
// longer present. The caller has typically already filtered the slice; this
// just commits the result. A nil/empty `branches` clears the field.
func (s *RepoStore) ReplaceUserOpenedBranches(repoID string, branches []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID != repoID {
			continue
		}
		clean := append([]string(nil), branches...)
		sort.Strings(clean)
		s.entries[i].UserOpenedBranches = clean
		return s.save()
	}
	return fmt.Errorf("config: ReplaceUserOpenedBranches: repo %q not found", repoID)
}

// IsUserOpened (S015) reports whether `branchName` is in the user-opened
// slice for the given repo. Cheap read for category derivation.
func (s *RepoStore) IsUserOpened(repoID, branchName string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ID != repoID {
			continue
		}
		for _, b := range e.UserOpenedBranches {
			if b == branchName {
				return true
			}
		}
		return false
	}
	return false
}

// TabName (S020) returns the user-set display name override for the given
// tab, or "" if none is set.
func (s *RepoStore) TabName(repoID, branchName, tabID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ID != repoID {
			continue
		}
		bo, ok := e.TabOverrides[branchName]
		if !ok {
			return ""
		}
		return bo.Names[tabID]
	}
	return ""
}

// TabOrder (S020) returns the ordered slice of tab IDs the user has saved
// for the given branch, or nil if none.
func (s *RepoStore) TabOrder(repoID, branchName string) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ID != repoID {
			continue
		}
		bo, ok := e.TabOverrides[branchName]
		if !ok {
			return nil
		}
		out := make([]string, len(bo.Order))
		copy(out, bo.Order)
		return out
	}
	return nil
}

// SetTabName (S020) records or clears a display-name override for one tab.
// Pass empty `name` to delete the override.
func (s *RepoStore) SetTabName(repoID, branchName, tabID, name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID != repoID {
			continue
		}
		if s.entries[i].TabOverrides == nil {
			s.entries[i].TabOverrides = map[string]BranchTabOverrides{}
		}
		bo := s.entries[i].TabOverrides[branchName]
		if name == "" {
			if bo.Names != nil {
				delete(bo.Names, tabID)
				if len(bo.Names) == 0 {
					bo.Names = nil
				}
			}
		} else {
			if bo.Names == nil {
				bo.Names = map[string]string{}
			}
			bo.Names[tabID] = name
		}
		// Drop empty branch entry to keep JSON tidy.
		if bo.Names == nil && len(bo.Order) == 0 {
			delete(s.entries[i].TabOverrides, branchName)
		} else {
			s.entries[i].TabOverrides[branchName] = bo
		}
		if len(s.entries[i].TabOverrides) == 0 {
			s.entries[i].TabOverrides = nil
		}
		return s.save()
	}
	return fmt.Errorf("config: SetTabName: repo %q not found", repoID)
}

// SetTabOrder (S020) records the user's preferred ordering for one branch.
// Pass nil/empty to clear.
func (s *RepoStore) SetTabOrder(repoID, branchName string, order []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID != repoID {
			continue
		}
		if s.entries[i].TabOverrides == nil {
			s.entries[i].TabOverrides = map[string]BranchTabOverrides{}
		}
		bo := s.entries[i].TabOverrides[branchName]
		if len(order) == 0 {
			bo.Order = nil
		} else {
			cp := make([]string, len(order))
			copy(cp, order)
			bo.Order = cp
		}
		if bo.Names == nil && len(bo.Order) == 0 {
			delete(s.entries[i].TabOverrides, branchName)
		} else {
			s.entries[i].TabOverrides[branchName] = bo
		}
		if len(s.entries[i].TabOverrides) == 0 {
			s.entries[i].TabOverrides = nil
		}
		return s.save()
	}
	return fmt.Errorf("config: SetTabOrder: repo %q not found", repoID)
}

// RenameTabIDInOverrides (S020) is called when a Bash window rename causes
// the tab ID to change (`bash:foo` → `bash:bar`). It rewrites both the
// Names key and any Order entries to point at the new ID. No-op if neither
// references the old ID.
func (s *RepoStore) RenameTabIDInOverrides(repoID, branchName, oldID, newID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID != repoID {
			continue
		}
		bo, ok := s.entries[i].TabOverrides[branchName]
		if !ok {
			return nil
		}
		changed := false
		if v, present := bo.Names[oldID]; present {
			delete(bo.Names, oldID)
			if bo.Names == nil {
				bo.Names = map[string]string{}
			}
			bo.Names[newID] = v
			changed = true
		}
		for j, id := range bo.Order {
			if id == oldID {
				bo.Order[j] = newID
				changed = true
			}
		}
		if changed {
			s.entries[i].TabOverrides[branchName] = bo
			return s.save()
		}
		return nil
	}
	return nil
}

// SetLastActiveBranch (S023) records the most-recently-navigated branch for
// the given repo. Pass empty `branchName` to clear. Idempotent. Returns
// (changed, error): `changed` is false when the value was already equal.
func (s *RepoStore) SetLastActiveBranch(repoID, branchName string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID != repoID {
			continue
		}
		if s.entries[i].LastActiveBranch == branchName {
			return false, nil
		}
		s.entries[i].LastActiveBranch = branchName
		return true, s.save()
	}
	return false, fmt.Errorf("config: SetLastActiveBranch: repo %q not found", repoID)
}

// LastActiveBranch (S023) returns the persisted last-active branch name for
// the given repo, or empty string if absent / unknown.
func (s *RepoStore) LastActiveBranch(repoID string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ID == repoID {
			return e.LastActiveBranch
		}
	}
	return ""
}

// SetStarred toggles the starred flag on a repo. Returns false if absent.
func (s *RepoStore) SetStarred(id string, starred bool) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID == id {
			s.entries[i].Starred = starred
			return true, s.save()
		}
	}
	return false, nil
}

func sortEntries(entries []RepoEntry) {
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].GHQPath < entries[j].GHQPath })
}

// GetTabClaudeMode (Sadf90e) returns the persisted ClaudeMode for the given
// tab. When no entry exists, returns ClaudeModeAgent so callers that race
// with InitTabClaudeMode still get a safe deterministic answer.
func (s *RepoStore) GetTabClaudeMode(repoID, branchID, tabID string) ClaudeMode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ID != repoID {
			continue
		}
		if e.BranchSettingsMap == nil {
			return ClaudeModeAgent
		}
		bs, ok := e.BranchSettingsMap[branchID]
		if !ok || bs.TabClaudeModes == nil {
			return ClaudeModeAgent
		}
		if m, ok := bs.TabClaudeModes[tabID]; ok && (m == ClaudeModeAgent || m == ClaudeModeTui) {
			return m
		}
		return ClaudeModeAgent
	}
	return ClaudeModeAgent
}

// HasTabClaudeMode (Sadf90e) reports whether the tab has an explicit entry.
// Used by GET /tab settings to distinguish "tab not registered yet" (→ 404)
// from "tab registered with mode X" (→ 200 { mode }).
func (s *RepoStore) HasTabClaudeMode(repoID, branchID, tabID string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, e := range s.entries {
		if e.ID != repoID {
			continue
		}
		if e.BranchSettingsMap == nil {
			return false
		}
		bs, ok := e.BranchSettingsMap[branchID]
		if !ok || bs.TabClaudeModes == nil {
			return false
		}
		_, ok = bs.TabClaudeModes[tabID]
		return ok
	}
	return false
}

// SetTabClaudeMode (Sadf90e) persists claude_mode for a specific Claude tab.
// mode must be "agent" or "tui"; any other value returns an error.
func (s *RepoStore) SetTabClaudeMode(repoID, branchID, tabID string, mode ClaudeMode) error {
	if mode != ClaudeModeAgent && mode != ClaudeModeTui {
		return fmt.Errorf("config: SetTabClaudeMode: invalid mode %q (must be agent or tui)", mode)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID != repoID {
			continue
		}
		if s.entries[i].BranchSettingsMap == nil {
			s.entries[i].BranchSettingsMap = map[string]BranchSettings{}
		}
		bs := s.entries[i].BranchSettingsMap[branchID]
		if bs.TabClaudeModes == nil {
			bs.TabClaudeModes = map[string]ClaudeMode{}
		}
		bs.TabClaudeModes[tabID] = mode
		s.entries[i].BranchSettingsMap[branchID] = bs
		return s.save()
	}
	return fmt.Errorf("config: SetTabClaudeMode: repo %q not found", repoID)
}

// InitTabClaudeMode (Sadf90e) ensures the tab has an entry in TabClaudeModes.
// Idempotent — if an entry already exists it is left unchanged. Used at
// tab-add time so the global settings.claude.default_mode value becomes the
// new tab's starting mode without needing an explicit PATCH from the FE.
func (s *RepoStore) InitTabClaudeMode(repoID, branchID, tabID string, defaultMode ClaudeMode) error {
	if defaultMode != ClaudeModeAgent && defaultMode != ClaudeModeTui {
		defaultMode = ClaudeModeAgent
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID != repoID {
			continue
		}
		if s.entries[i].BranchSettingsMap == nil {
			s.entries[i].BranchSettingsMap = map[string]BranchSettings{}
		}
		bs := s.entries[i].BranchSettingsMap[branchID]
		if bs.TabClaudeModes == nil {
			bs.TabClaudeModes = map[string]ClaudeMode{}
		}
		if _, exists := bs.TabClaudeModes[tabID]; exists {
			return nil // already initialised — leave it
		}
		bs.TabClaudeModes[tabID] = defaultMode
		s.entries[i].BranchSettingsMap[branchID] = bs
		return s.save()
	}
	return fmt.Errorf("config: InitTabClaudeMode: repo %q not found", repoID)
}

// DeleteTabClaudeMode (Sadf90e) removes the tab's settings entry. Called from
// the tab-removal path so deleted-tab entries don't accumulate in repos.json.
// No-op when the tab has no entry (e.g. it was created pre-Sadf90e and the
// migration discarded the old branch-wide flag).
func (s *RepoStore) DeleteTabClaudeMode(repoID, branchID, tabID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].ID != repoID {
			continue
		}
		if s.entries[i].BranchSettingsMap == nil {
			return nil
		}
		bs, ok := s.entries[i].BranchSettingsMap[branchID]
		if !ok || bs.TabClaudeModes == nil {
			return nil
		}
		if _, exists := bs.TabClaudeModes[tabID]; !exists {
			return nil
		}
		delete(bs.TabClaudeModes, tabID)
		s.entries[i].BranchSettingsMap[branchID] = bs
		return s.save()
	}
	return nil // repo gone — nothing to clean up
}
