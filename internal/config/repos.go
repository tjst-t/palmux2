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

// RepoEntry is one row in repos.json.
//
// UserOpenedBranches (S015) records the branch names the user opened
// explicitly through Palmux (via Drawer "Open Branch…" or via the
// `+ Add to my worktrees` promote action). The Drawer reads this to
// classify branches as `user` (in the slice), `subagent` (path matches
// `autoWorktreePathPatterns`), or `unmanaged` (otherwise). The field is
// `omitempty` so pre-S015 `repos.json` files load unchanged and so the
// JSON stays tidy for repos that have never had branches promoted.
type RepoEntry struct {
	ID                 string   `json:"id"`
	GHQPath            string   `json:"ghqPath"`
	Starred            bool     `json:"starred"`
	UserOpenedBranches []string `json:"userOpenedBranches,omitempty"`
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
