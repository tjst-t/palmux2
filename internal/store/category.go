package store

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/worktree"
)

// Category constants. These match the JSON values returned by the API
// (`branch.category`) — the FE remaps "user" → "my" in section titles.
const (
	BranchCategoryUser      = "user"
	BranchCategoryUnmanaged = "unmanaged"
	BranchCategorySubagent  = "subagent"
)

// categorize returns the drawer category for a branch given the user-opened
// list and the auto-worktree path patterns. Detection order (per S015 spec):
//
//  1. branch in user_opened_branches → "user"
//  2. worktree path matches any auto pattern → "subagent"
//  3. else → "unmanaged"
//
// `worktreePath` should be absolute. Patterns may use `*` as a single-segment
// wildcard (mapped to `[^/]*`); the pattern is matched against any
// substring of the path so users can write `.claude/worktrees/*` without
// caring about the absolute repo root.
func categorize(branchName, worktreePath string, userOpened, patterns []string) string {
	for _, b := range userOpened {
		if b == branchName {
			return BranchCategoryUser
		}
	}
	if matchesAnyPattern(worktreePath, patterns) {
		return BranchCategorySubagent
	}
	return BranchCategoryUnmanaged
}

// matchesAnyPattern returns true when `path` contains a substring matching
// any of `patterns`. Empty patterns are skipped. `*` is treated as a
// single-segment wildcard.
func matchesAnyPattern(path string, patterns []string) bool {
	if path == "" {
		return false
	}
	// Normalize to forward slashes so the patterns work the same on
	// Windows-derived paths as well (defensive — palmux primarily runs on
	// Linux/macOS but worktree.List might surface either).
	norm := filepath.ToSlash(path)
	for _, p := range patterns {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if matchSubstringGlob(norm, filepath.ToSlash(p)) {
			return true
		}
	}
	return false
}

// matchSubstringGlob reports whether any sub-path of `path` matches the
// glob `pattern`. The match is done segment-by-segment: the pattern is
// expanded against each suffix of `path`'s segments, and `*` is bound to
// a single non-slash segment chunk.
//
// Concretely, `path = "/home/u/repo/.claude/worktrees/foo"` and
// `pattern = ".claude/worktrees/*"` matches because the suffix
// `.claude/worktrees/foo` lines up with the pattern.
func matchSubstringGlob(path, pattern string) bool {
	if pattern == "" {
		return false
	}
	pSegs := strings.Split(pattern, "/")
	if len(pSegs) == 0 {
		return false
	}
	// Skip leading empties from a pattern starting with `/`.
	for len(pSegs) > 0 && pSegs[0] == "" {
		pSegs = pSegs[1:]
	}
	if len(pSegs) == 0 {
		return false
	}
	hSegs := strings.Split(path, "/")
	for start := 0; start+len(pSegs) <= len(hSegs); start++ {
		if matchSegments(hSegs[start:start+len(pSegs)], pSegs) {
			return true
		}
	}
	return false
}

// matchSegments matches pre-split path segments against pattern segments
// where `*` binds a single segment.
func matchSegments(hSegs, pSegs []string) bool {
	if len(hSegs) != len(pSegs) {
		return false
	}
	for i, p := range pSegs {
		if !matchSingleSegment(hSegs[i], p) {
			return false
		}
	}
	return true
}

// matchSingleSegment matches one segment against a glob that may contain
// `*` wildcards (any number of non-slash characters). Uses path.Match
// semantics on a single segment.
func matchSingleSegment(seg, pattern string) bool {
	if pattern == "*" {
		// Cheap & frequent path.
		return true
	}
	ok, err := filepath.Match(pattern, seg)
	if err != nil {
		// Malformed pattern — treat as no-match rather than panicking.
		return false
	}
	return ok
}

// applyCategoriesUnlocked sets `branch.Category` on every branch in `repo`
// using the live RepoStore + Settings. Caller must hold s.mu (write lock
// when mutating, read lock for reads). Pure derivation — no side effects.
//
// S024-1-1: the primary worktree (canonical ghq folder) is always "user"
// regardless of `user_opened_branches`. The drawer treats it as MY so the
// repo always has at least one MY branch — required for the v7 single-line
// MY list and glance-line preview to make sense.
func (s *Store) applyCategoriesUnlocked(repo *domain.Repository) {
	if repo == nil {
		return
	}
	entry, _ := s.deps.RepoStore.Get(repo.ID)
	patterns := s.deps.Settings.AutoWorktreePathPatterns()
	for _, b := range repo.OpenBranches {
		if b.IsPrimary {
			// ghq folder = MY (S024-1-1). Always user, never subagent/unmanaged.
			b.Category = BranchCategoryUser
			continue
		}
		b.Category = categorize(b.Name, b.WorktreePath, entry.UserOpenedBranches, patterns)
	}
}

// applyCategoriesAllUnlocked iterates every Open repo. Caller holds the
// write lock.
func (s *Store) applyCategoriesAllUnlocked() {
	for _, r := range s.repos {
		s.applyCategoriesUnlocked(r)
	}
}

// PromoteBranch (S015) marks `branchName` as user-opened on the given repo
// (idempotent). Returns the new category (always "user" on success). Used
// by the `+ Add to my worktrees` action and by the openBranch hook.
func (s *Store) PromoteBranch(ctx context.Context, repoID, branchName string) error {
	_ = ctx // reserved for future per-call timeouts on save
	if branchName == "" {
		return ErrInvalidArg
	}
	s.mu.Lock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.Unlock()
		return ErrRepoNotFound
	}
	var branchID string
	for _, b := range repo.OpenBranches {
		if b.Name == branchName {
			branchID = b.ID
			break
		}
	}
	s.mu.Unlock()
	if _, err := s.deps.RepoStore.AddUserOpenedBranch(repoID, branchName); err != nil {
		return err
	}
	// Re-apply categories so subsequent reads return the new value.
	s.mu.Lock()
	s.applyCategoriesUnlocked(repo)
	s.mu.Unlock()
	s.hub.Publish(Event{
		Type:     EventBranchCategoryChanged,
		RepoID:   repoID,
		BranchID: branchID,
		Payload:  map[string]string{"category": BranchCategoryUser, "branchName": branchName},
	})
	return nil
}

// DemoteBranch (S015) removes `branchName` from user_opened_branches.
// The new category falls back to "subagent" or "unmanaged" via the
// usual derivation. Idempotent.
func (s *Store) DemoteBranch(ctx context.Context, repoID, branchName string) error {
	_ = ctx
	if branchName == "" {
		return ErrInvalidArg
	}
	s.mu.Lock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.Unlock()
		return ErrRepoNotFound
	}
	var branchID, worktreePath string
	for _, b := range repo.OpenBranches {
		if b.Name == branchName {
			branchID = b.ID
			worktreePath = b.WorktreePath
			break
		}
	}
	s.mu.Unlock()
	if _, err := s.deps.RepoStore.RemoveUserOpenedBranch(repoID, branchName); err != nil {
		return err
	}
	patterns := s.deps.Settings.AutoWorktreePathPatterns()
	cat := categorize(branchName, worktreePath, nil, patterns)
	s.mu.Lock()
	s.applyCategoriesUnlocked(repo)
	s.mu.Unlock()
	s.hub.Publish(Event{
		Type:     EventBranchCategoryChanged,
		RepoID:   repoID,
		BranchID: branchID,
		Payload:  map[string]string{"category": cat, "branchName": branchName},
	})
	return nil
}

// SetLastActiveBranch (S023) records `branchName` as the most-recently-
// navigated branch for the given repo. Pass empty `branchName` to clear.
// Idempotent — when the value already equals the request, no event is
// emitted. The branch name is **not** validated against currently-open
// branches: callers (typically the implicit nav hook) may want to record
// a branch that is reachable via worktree but not currently in the
// in-memory snapshot. Reconcile drops stale values at startup.
func (s *Store) SetLastActiveBranch(repoID, branchName string) error {
	changed, err := s.deps.RepoStore.SetLastActiveBranch(repoID, branchName)
	if err != nil {
		return err
	}
	// Mirror onto the in-memory snapshot so subsequent /api/repos calls
	// see the new value without waiting for a hydrate cycle.
	s.mu.Lock()
	if r, ok := s.repos[repoID]; ok {
		r.LastActiveBranch = branchName
	}
	s.mu.Unlock()
	if changed {
		s.hub.Publish(Event{
			Type:    EventBranchLastActiveChanged,
			RepoID:  repoID,
			Payload: map[string]string{"branch": branchName},
		})
	}
	return nil
}

// ReconcileLastActiveBranches (S023) walks every repo's `last_active_branch`
// at startup and clears entries whose worktree no longer exists. Runs in
// the same pass as ReconcileUserOpenedBranches but kept distinct so the
// two reconcilers can be reasoned about independently. Panic-safe.
func (s *Store) ReconcileLastActiveBranches(ctx context.Context) {
	for _, repo := range s.deps.RepoStore.All() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					s.logger.Warn("reconcile: lastActive panic", "repo", repo.ID, "panic", r)
				}
			}()
			if repo.LastActiveBranch == "" {
				return
			}
			full := repo.GHQPath
			if s.ghqRoot != "" {
				full = filepath.Join(s.ghqRoot, repo.GHQPath)
			}
			wts, err := worktree.List(ctx, full)
			if err != nil {
				s.logger.Warn("reconcile: lastActive worktree.List failed", "repo", repo.ID, "err", err)
				return
			}
			for _, wt := range wts {
				if wt.Branch == repo.LastActiveBranch {
					return
				}
			}
			if _, err := s.deps.RepoStore.SetLastActiveBranch(repo.ID, ""); err != nil {
				s.logger.Warn("reconcile: lastActive save failed", "repo", repo.ID, "err", err)
				return
			}
			s.mu.Lock()
			if r, ok := s.repos[repo.ID]; ok {
				r.LastActiveBranch = ""
			}
			s.mu.Unlock()
			s.logger.Info("reconcile: cleared stale last_active_branch",
				"repo", repo.ID, "was", repo.LastActiveBranch)
		}()
	}
}

// ReconcileUserOpenedBranches walks every repo's `user_opened_branches`
// slice at startup and drops entries whose worktree no longer exists on
// disk (e.g. user ran `gwq remove` directly). Panic-safe: a single repo's
// failure does not halt others.
//
// Called from main.go after Store.New() — before the sync loops kick in
// — so the very first /api/repos response reflects the cleaned state.
func (s *Store) ReconcileUserOpenedBranches(ctx context.Context) {
	for _, repo := range s.deps.RepoStore.All() {
		func() {
			defer func() {
				if r := recover(); r != nil {
					s.logger.Warn("reconcile: panic", "repo", repo.ID, "panic", r)
				}
			}()
			if len(repo.UserOpenedBranches) == 0 {
				return
			}
			full := repo.GHQPath
			if s.ghqRoot != "" {
				full = filepath.Join(s.ghqRoot, repo.GHQPath)
			}
			wts, err := worktree.List(ctx, full)
			if err != nil {
				s.logger.Warn("reconcile: worktree.List failed", "repo", repo.ID, "err", err)
				return
			}
			present := map[string]bool{}
			for _, wt := range wts {
				if wt.Branch != "" {
					present[wt.Branch] = true
				}
			}
			kept := make([]string, 0, len(repo.UserOpenedBranches))
			dropped := 0
			for _, b := range repo.UserOpenedBranches {
				if present[b] {
					kept = append(kept, b)
				} else {
					dropped++
				}
			}
			if dropped == 0 {
				return
			}
			if err := s.deps.RepoStore.ReplaceUserOpenedBranches(repo.ID, kept); err != nil {
				s.logger.Warn("reconcile: save failed", "repo", repo.ID, "err", err)
				return
			}
			s.logger.Info("reconcile: dropped stale user_opened_branches",
				"repo", repo.ID, "dropped", dropped, "remaining", len(kept))
		}()
	}
}
