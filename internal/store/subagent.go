// S021: subagent worktree lifecycle (cleanup + promote).
//
// This file owns the "stale judgement" logic and the actions that follow
// from it (bulk cleanup of stale subagent worktrees, promotion of a
// subagent worktree to the user's standard `my` location).
package store

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/tjst-t/palmux2/internal/domain"
)

// SubagentLockGlob matches the lock files autopilot / claude-skills create
// while a sub-agent run is in progress. The presence of any matching file
// inside `<worktree>/.claude/` exempts the worktree from cleanup.
const SubagentLockGlob = ".claude/autopilot-*.lock"

// SubagentCleanupCandidate describes one subagent worktree that may be
// removed by the cleanup endpoint. The reason fields are included so the
// FE can show the user *why* a given worktree is considered stale.
type SubagentCleanupCandidate struct {
	BranchID      string    `json:"branchId"`
	BranchName    string    `json:"branchName"`
	WorktreePath  string    `json:"worktreePath"`
	LastCommitISO string    `json:"lastCommitIso,omitempty"`
	LastCommit    time.Time `json:"-"`
	AgeDays       int       `json:"ageDays"`
	HasLock       bool      `json:"hasLock"`
	IsPrimary     bool      `json:"isPrimary"`
	Reason        string    `json:"reason"`
}

// SubagentCleanupRemoval is one removal outcome (success or failure).
type SubagentCleanupRemoval struct {
	BranchID     string `json:"branchId"`
	BranchName   string `json:"branchName"`
	WorktreePath string `json:"worktreePath"`
	Error        string `json:"error,omitempty"`
}

// SubagentCleanupResult is the response to POST /worktrees/cleanup-subagent.
type SubagentCleanupResult struct {
	ThresholdDays int                        `json:"thresholdDays"`
	Candidates    []SubagentCleanupCandidate `json:"candidates"`
	Removed       []SubagentCleanupRemoval   `json:"removed,omitempty"`
	Failed        []SubagentCleanupRemoval   `json:"failed,omitempty"`
}

// ListStaleSubagentWorktrees returns the cleanup candidates for one repo:
// every subagent-categorised worktree that has no autopilot lock AND a
// last commit older than `thresholdDays`. The primary worktree is never
// included — it's always the one the user is "in".
//
// The returned slice is sorted by branch name for stable UI rendering.
func (s *Store) ListStaleSubagentWorktrees(ctx context.Context, repoID string, thresholdDays int) ([]SubagentCleanupCandidate, error) {
	if thresholdDays <= 0 {
		thresholdDays = s.deps.Settings.SubagentStaleAfterDays()
	}
	now := time.Now()
	cutoff := now.AddDate(0, 0, -thresholdDays)

	s.mu.Lock()
	repo, ok := s.repos[repoID]
	if !ok {
		s.mu.Unlock()
		return nil, ErrRepoNotFound
	}
	s.applyCategoriesUnlocked(repo)
	branches := make([]*domain.Branch, len(repo.OpenBranches))
	for i, b := range repo.OpenBranches {
		branches[i] = cloneBranch(b)
	}
	s.mu.Unlock()

	var out []SubagentCleanupCandidate
	for _, b := range branches {
		if b.IsPrimary {
			continue
		}
		if b.Category != BranchCategorySubagent {
			continue
		}
		hasLock := worktreeHasAutopilotLock(b.WorktreePath)
		if hasLock {
			continue
		}
		commitTime, err := worktreeLastCommit(ctx, b.WorktreePath)
		if err != nil {
			s.logger.Warn("ListStaleSubagentWorktrees: last-commit",
				"branch", b.Name, "path", b.WorktreePath, "err", err)
			continue
		}
		if commitTime.IsZero() || commitTime.After(cutoff) {
			continue
		}
		ageDays := int(now.Sub(commitTime).Hours() / 24)
		out = append(out, SubagentCleanupCandidate{
			BranchID:      b.ID,
			BranchName:    b.Name,
			WorktreePath:  b.WorktreePath,
			LastCommitISO: commitTime.UTC().Format(time.RFC3339),
			LastCommit:    commitTime,
			AgeDays:       ageDays,
			HasLock:       false,
			IsPrimary:     b.IsPrimary,
			Reason: fmt.Sprintf(
				"no autopilot lock, last commit %d day(s) ago (>= %d)",
				ageDays, thresholdDays,
			),
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].BranchName < out[j].BranchName
	})
	return out, nil
}

// CleanupSubagentWorktrees removes the stale subagent worktrees for the
// repo. When `branchNames` is non-empty only those branches are
// considered (intersected with the staleness check); otherwise every
// stale worktree is targeted. Per-worktree failures are tolerated:
// successful removals proceed, failed ones are returned in `Failed[]`.
//
// Emits a `worktree.cleaned` event with the aggregate result.
func (s *Store) CleanupSubagentWorktrees(ctx context.Context, repoID string, branchNames []string, thresholdDays int) (*SubagentCleanupResult, error) {
	if thresholdDays <= 0 {
		thresholdDays = s.deps.Settings.SubagentStaleAfterDays()
	}
	candidates, err := s.ListStaleSubagentWorktrees(ctx, repoID, thresholdDays)
	if err != nil {
		return nil, err
	}
	allow := map[string]bool{}
	if len(branchNames) > 0 {
		for _, n := range branchNames {
			allow[n] = true
		}
		filtered := candidates[:0]
		for _, c := range candidates {
			if allow[c.BranchName] {
				filtered = append(filtered, c)
			}
		}
		candidates = filtered
	}

	result := &SubagentCleanupResult{
		ThresholdDays: thresholdDays,
		Candidates:    candidates,
	}

	for _, c := range candidates {
		// Drive the existing CloseBranch path so tmux state, repos.json
		// drift, and gwq removal are all consistent.
		if err := s.CloseBranch(ctx, repoID, c.BranchID); err != nil {
			result.Failed = append(result.Failed, SubagentCleanupRemoval{
				BranchID:     c.BranchID,
				BranchName:   c.BranchName,
				WorktreePath: c.WorktreePath,
				Error:        err.Error(),
			})
			continue
		}
		result.Removed = append(result.Removed, SubagentCleanupRemoval{
			BranchID:     c.BranchID,
			BranchName:   c.BranchName,
			WorktreePath: c.WorktreePath,
		})
	}

	s.hub.Publish(Event{
		Type:    EventWorktreeCleaned,
		RepoID:  repoID,
		Payload: result,
	})
	return result, nil
}

// PromoteSubagentBranch moves a subagent-categorised worktree out of its
// auto-detected location (e.g. `.claude/worktrees/<id>`) and into the
// gwq-standard location for the repo, then records the branch as
// user-opened so the Drawer reclassifies it as `my`. The tmux session is
// preserved across the move; only its CWD changes implicitly because
// `git worktree move` rewrites the worktree's git-dir pointer.
//
// Returns the destination path on success.
func (s *Store) PromoteSubagentBranch(ctx context.Context, repoID, branchID string) (string, error) {
	branch, err := s.Branch(repoID, branchID)
	if err != nil {
		return "", err
	}
	if branch.IsPrimary {
		return "", fmt.Errorf("%w: cannot promote the primary worktree", ErrInvalidArg)
	}
	if branch.Category != BranchCategorySubagent {
		return "", fmt.Errorf("%w: branch is not a subagent worktree (category=%s)", ErrInvalidArg, branch.Category)
	}

	repo, err := s.Repo(repoID)
	if err != nil {
		return "", err
	}

	dest, err := gwqStandardPath(ctx, repo, branch.Name)
	if err != nil {
		return "", fmt.Errorf("derive gwq path: %w", err)
	}
	if dest == branch.WorktreePath {
		// Already in the standard location — only need to flip category.
		if err := s.PromoteBranch(ctx, repoID, branch.Name); err != nil {
			return "", err
		}
		return dest, nil
	}

	if _, err := os.Stat(dest); err == nil {
		return "", fmt.Errorf("destination already exists: %s", dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", fmt.Errorf("mkdir dest parent: %w", err)
	}

	// `git worktree move` runs against the repo's primary git-dir to
	// preserve the linked-worktree wiring.
	cmd := exec.CommandContext(ctx, "git", "worktree", "move", branch.WorktreePath, dest)
	cmd.Dir = repo.FullPath
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("git worktree move: %s", strings.TrimSpace(string(out)))
	}
	// `git worktree repair` is a no-op when nothing's broken; we run it
	// defensively in case the post-move inspection finds a stale ref.
	repairCmd := exec.CommandContext(ctx, "git", "worktree", "repair")
	repairCmd.Dir = repo.FullPath
	if out, err := repairCmd.CombinedOutput(); err != nil {
		s.logger.Warn("PromoteSubagentBranch: git worktree repair",
			"err", err, "out", strings.TrimSpace(string(out)))
	}

	// Update in-memory worktree path so subsequent /api/repos snapshots
	// reflect the new location without waiting for the 30s sync ticker.
	s.mu.Lock()
	if r, ok := s.repos[repoID]; ok {
		for _, b := range r.OpenBranches {
			if b.ID == branchID {
				b.WorktreePath = dest
				break
			}
		}
	}
	s.mu.Unlock()

	if err := s.PromoteBranch(ctx, repoID, branch.Name); err != nil {
		// PromoteBranch already published `branch.categoryChanged` for the
		// fail case is moot — this only fails when repos.json is unwritable.
		return "", fmt.Errorf("PromoteBranch: %w", err)
	}

	// Re-apply categories now that the path has changed (the worktree no
	// longer matches the auto-pattern, so its category cannot fall back
	// to `subagent` if the user later demotes it).
	s.mu.Lock()
	if r, ok := s.repos[repoID]; ok {
		s.applyCategoriesUnlocked(r)
	}
	s.mu.Unlock()
	return dest, nil
}

// worktreeHasAutopilotLock returns true when any file matching
// `.claude/autopilot-*.lock` exists under the worktree.
func worktreeHasAutopilotLock(worktreePath string) bool {
	if worktreePath == "" {
		return false
	}
	matches, err := filepath.Glob(filepath.Join(worktreePath, SubagentLockGlob))
	if err != nil {
		return false
	}
	return len(matches) > 0
}

// worktreeLastCommit returns the timestamp of HEAD on the worktree.
// `git log -1 --format=%cI HEAD`. Returns the zero time if the worktree
// has no commits (fresh `git worktree add` with no work).
func worktreeLastCommit(ctx context.Context, worktreePath string) (time.Time, error) {
	if worktreePath == "" {
		return time.Time{}, fmt.Errorf("empty worktree path")
	}
	cmd := exec.CommandContext(ctx, "git", "log", "-1", "--format=%cI", "HEAD")
	cmd.Dir = worktreePath
	out, err := cmd.Output()
	if err != nil {
		// "fatal: your current branch ... does not have any commits yet"
		// is benign; treat as zero time so the candidate is filtered out
		// (we don't want to delete brand-new worktrees that may just have
		// been created).
		return time.Time{}, nil //nolint:nilerr
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse %q: %w", s, err)
	}
	return t, nil
}

// gwqStandardPath returns the path gwq would have used for a fresh
// `gwq add <branch>` against the given repo. We derive it from the
// repo's GHQPath (host/owner/repo) plus gwq's `worktree.basedir` config.
func gwqStandardPath(ctx context.Context, repo *domain.Repository, branchName string) (string, error) {
	basedir, err := gwqBaseDir(ctx)
	if err != nil {
		return "", err
	}
	// gwq's default naming.template is `{{.Host}}/{{.Owner}}/{{.Repository}}/{{.Branch}}`
	// with sanitize_chars `: → -` and `/ → -` applied to the branch.
	parts := strings.Split(repo.GHQPath, "/")
	if len(parts) < 3 {
		return "", fmt.Errorf("unexpected ghqPath %q", repo.GHQPath)
	}
	host := parts[0]
	owner := parts[1]
	repoName := strings.Join(parts[2:], "-")
	branch := strings.NewReplacer(":", "-", "/", "-").Replace(branchName)
	return filepath.Join(basedir, host, owner, repoName, branch), nil
}

// gwqBaseDir returns the configured gwq worktree base directory,
// expanded for ~/. Defaults to `~/worktrees` when gwq is missing.
func gwqBaseDir(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "gwq", "config", "get", "worktree.basedir")
	out, err := cmd.Output()
	var raw string
	if err != nil {
		raw = "~/worktrees"
	} else {
		raw = strings.TrimSpace(string(out))
		if raw == "" {
			raw = "~/worktrees"
		}
	}
	if strings.HasPrefix(raw, "~") {
		home, err := os.UserHomeDir()
		if err == nil {
			raw = filepath.Join(home, strings.TrimPrefix(raw, "~"))
		}
	}
	return raw, nil
}
