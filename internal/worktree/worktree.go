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
