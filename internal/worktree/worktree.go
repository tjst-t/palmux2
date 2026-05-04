// Package worktree reads git worktree state. Read-only: writes (add/remove)
// go through the gwq package.
package worktree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree describes a single git worktree.
type Worktree struct {
	Path       string // absolute
	HEAD       string // commit sha
	Branch     string // branch name (empty for detached HEAD)
	IsPrimary  bool   // holds .git/ as a directory (vs. linked worktrees, which have .git as a file)
	IsLocked   bool
	IsDetached bool
}

// List returns all worktrees for the repository at repoDir using
// `git worktree list --porcelain`.
func List(ctx context.Context, repoDir string) ([]Worktree, error) {
	cmd := exec.CommandContext(ctx, "git", "worktree", "list", "--porcelain")
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git worktree list (%s): %s", repoDir, strings.TrimSpace(stderr.String()))
	}
	worktrees := parsePorcelain(stdout.String())
	for i := range worktrees {
		worktrees[i].IsPrimary = isPrimary(worktrees[i].Path)
	}
	return worktrees, nil
}

// parsePorcelain splits `git worktree list --porcelain` output into records.
// Each record is separated by a blank line and fields are space-separated.
func parsePorcelain(s string) []Worktree {
	var worktrees []Worktree
	var cur Worktree
	flush := func() {
		if cur.Path != "" {
			worktrees = append(worktrees, cur)
		}
		cur = Worktree{}
	}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			flush()
			continue
		}
		k, v, _ := strings.Cut(line, " ")
		switch k {
		case "worktree":
			cur.Path = v
		case "HEAD":
			cur.HEAD = v
		case "branch":
			cur.Branch = strings.TrimPrefix(v, "refs/heads/")
		case "detached":
			cur.IsDetached = true
		case "locked":
			cur.IsLocked = true
		}
	}
	flush()
	return worktrees
}

// isPrimary reports whether the worktree at path holds the canonical .git/
// directory (vs. linked worktrees, where .git is a file pointing at gitdir).
func isPrimary(path string) bool {
	info, err := os.Stat(filepath.Join(path, ".git"))
	if err != nil {
		return false
	}
	return info.IsDir()
}

// Branch is one branch known to git (local or remote).
type Branch struct {
	Name     string // e.g. "feature/new-ui"
	IsRemote bool
	IsHEAD   bool
}

// ListAllBranches returns local + remote branches via `git for-each-ref`.
// Use this for the branch picker (which shows branches that may not yet have
// a worktree).
func ListAllBranches(ctx context.Context, repoDir string) ([]Branch, error) {
	cmd := exec.CommandContext(ctx, "git", "for-each-ref",
		"--format=%(refname:short)\t%(refname)",
		"refs/heads", "refs/remotes")
	cmd.Dir = repoDir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git for-each-ref (%s): %s", repoDir, strings.TrimSpace(stderr.String()))
	}
	var branches []Branch
	for _, line := range strings.Split(stdout.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		short, full, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		// Skip "origin/HEAD" pseudo refs.
		if strings.HasSuffix(short, "/HEAD") {
			continue
		}
		b := Branch{Name: short, IsRemote: strings.HasPrefix(full, "refs/remotes/")}
		branches = append(branches, b)
	}
	return branches, nil
}

// WorktreeStatus describes the unpushed/dirty state of a single worktree for
// the delete-preview endpoint.
type WorktreeStatus struct {
	Path            string   `json:"path"`
	Branch          string   `json:"branch"`
	AheadCommits    []string `json:"aheadCommits"` // short log lines from git log @{u}..HEAD; never nil → []
	UpstreamMissing bool     `json:"upstreamMissing"`
	DirtyFiles      []string `json:"dirtyFiles"`    // M/D/etc entries from git status --porcelain; never nil → []
	UntrackedFiles  []string `json:"untrackedFiles"` // ?? entries; never nil → []
	IsPrimary       bool     `json:"isPrimary"`
}

// HasWarnings reports whether this worktree has any unpushed or dirty state.
func (w WorktreeStatus) HasWarnings() bool {
	return len(w.AheadCommits) > 0 || w.UpstreamMissing || len(w.DirtyFiles) > 0 || len(w.UntrackedFiles) > 0
}

// UnpushedSummary inspects every worktree of the repository at repoPath and
// returns per-worktree status. This is best-effort: individual command errors
// are silently dropped so a missing upstream or detached HEAD does not block
// the preview.
func UnpushedSummary(ctx context.Context, repoPath string) ([]WorktreeStatus, error) {
	wts, err := List(ctx, repoPath)
	if err != nil {
		return nil, fmt.Errorf("worktree.List: %w", err)
	}
	out := make([]WorktreeStatus, 0, len(wts))
	for _, wt := range wts {
		if wt.Branch == "" || wt.IsDetached {
			continue
		}
		st := WorktreeStatus{
			Path:           wt.Path,
			Branch:         wt.Branch,
			IsPrimary:      wt.IsPrimary,
			AheadCommits:   []string{},
			DirtyFiles:     []string{},
			UntrackedFiles: []string{},
		}

		// 1. Check for upstream and ahead commits.
		upstreamTrack := gitForEachRefUpstreamTrack(ctx, wt.Path, wt.Branch)
		if upstreamTrack == "" {
			// No upstream configured at all.
			st.UpstreamMissing = true
			// Collect all local commits as "ahead" for display.
			if commits := gitLogLines(ctx, wt.Path, "HEAD", ""); commits != nil {
				st.AheadCommits = commits
			}
		} else {
			// Upstream exists; get commits ahead of it.
			if commits := gitLogLines(ctx, wt.Path, "@{u}..HEAD", ""); commits != nil {
				st.AheadCommits = commits
			}
		}

		// 2. Get dirty/untracked files from git status --porcelain.
		dirty, untracked := gitStatusPortcelain(ctx, wt.Path)
		if dirty != nil {
			st.DirtyFiles = dirty
		}
		if untracked != nil {
			st.UntrackedFiles = untracked
		}

		out = append(out, st)
	}
	return out, nil
}

// gitForEachRefUpstreamTrack returns the upstream tracking info for a branch,
// e.g. "[ahead 3]" or "". Empty string means no upstream is configured.
func gitForEachRefUpstreamTrack(ctx context.Context, dir, branch string) string {
	cmd := exec.CommandContext(ctx, "git", "for-each-ref",
		"--format=%(upstream:track)", "refs/heads/"+branch)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitLogLines returns short log lines. refRange is e.g. "@{u}..HEAD" or "HEAD".
// An empty range (no upstream) uses "HEAD" with a limit of 20.
func gitLogLines(ctx context.Context, dir, refRange, _ string) []string {
	args := []string{"log", "--oneline"}
	if refRange != "" {
		args = append(args, refRange)
	}
	if refRange == "HEAD" {
		args = append(args, "--max-count=20")
	}
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// gitStatusPortcelain parses `git status --porcelain` and splits entries into
// dirty files (any XY except ??) and untracked files (?? entries).
func gitStatusPortcelain(ctx context.Context, dir string) (dirty, untracked []string) {
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return nil, nil
	}
	for _, line := range strings.Split(string(out), "\n") {
		if len(line) < 4 {
			continue
		}
		xy := line[:2]
		file := strings.TrimSpace(line[3:])
		if xy == "??" {
			untracked = append(untracked, file)
		} else {
			dirty = append(dirty, xy+" "+file)
		}
	}
	return
}
