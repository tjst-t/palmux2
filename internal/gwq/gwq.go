// Package gwq wraps the `gwq` worktree-management CLI.
//
// Palmux delegates worktree creation/deletion to gwq so the worktree path
// layout is whatever the user has configured globally — Palmux itself never
// decides where worktrees live.
package gwq

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Client wraps `gwq`.
type Client struct {
	bin string
}

// New returns a Client.
func New() *Client { return &Client{bin: "gwq"} }

// Add creates a worktree for the given branch. If newBranch is true, gwq
// creates the branch (`gwq add -b <name>`); otherwise it expects an existing
// branch (`gwq add <name>`). repoDir must be inside the target git repository
// so gwq picks up the right working directory.
func (c *Client) Add(ctx context.Context, repoDir, branchName string, newBranch bool) error {
	if branchName == "" {
		return fmt.Errorf("gwq.Add: empty branch name")
	}
	args := []string{"add"}
	if newBranch {
		args = append(args, "-b")
	}
	args = append(args, branchName)
	cmd := exec.CommandContext(ctx, c.bin, args...)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gwq add %s: %s", branchName, strings.TrimSpace(string(out)))
	}
	return nil
}

// WorktreeBasedir returns the directory gwq places new worktrees under
// (`gwq config get worktree.basedir`), as an absolute path with a leading `~`
// expanded. Palmux needs it because gwq's default layout puts linked worktrees
// under a base dir (default `~/worktrees`) that is OUTSIDE `~/ghq`: the incus
// runtime bind-mounts only `~/ghq`, so a linked worktree's path does not exist
// inside the container and a Claude/Bash tab opened on it lands at `/` (claude)
// or `~` (bash) — and claude's resume history, keyed by the absolute worktree
// path, is orphaned. Mounting this base dir same-path fixes both. Returns an
// error if gwq is unavailable or worktree.basedir is unset.
func (c *Client) WorktreeBasedir(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, c.bin, "config", "get", "worktree.basedir").Output()
	if err != nil {
		return "", fmt.Errorf("gwq config get worktree.basedir: %w", err)
	}
	dir := strings.TrimSpace(string(out))
	if dir == "" {
		return "", fmt.Errorf("gwq worktree.basedir is unset")
	}
	if dir == "~" || strings.HasPrefix(dir, "~/") {
		home, herr := os.UserHomeDir()
		if herr != nil {
			return "", fmt.Errorf("expand ~ in worktree.basedir: %w", herr)
		}
		dir = filepath.Join(home, strings.TrimPrefix(dir, "~"))
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("gwq worktree.basedir %q is not absolute", dir)
	}
	return dir, nil
}

// Remove deletes a worktree by branch-name pattern. Does NOT delete the
// branch itself (matches gwq default behaviour).
//
// Always passes `-f` so the removal isn't blocked by uncommitted edits or
// untracked files. Without `-f`, gwq aborts with "contains modified or
// untracked files" when ANY untracked file is present — including
// innocuous things like a `bin/` build artefact, a nested `tmp/` from a
// `make serve` instance, or even the user's own scratch files. The
// caller (CloseBranch) only invokes this after the user has explicitly
// confirmed the close in the UI ("tmux session will be killed and its
// worktree removed"), so silently leaving the worktree on disk because
// of a stray file is a worse outcome than the close being permanent.
// Without -f the worktree survived the close and reappeared as
// `unmanaged` on the next sync_worktree tick — the v0.5 resurrection
// bug.
func (c *Client) Remove(ctx context.Context, repoDir, pattern string) error {
	if pattern == "" {
		return fmt.Errorf("gwq.Remove: empty pattern")
	}
	cmd := exec.CommandContext(ctx, c.bin, "remove", "-f", pattern)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gwq remove -f %s: %s", pattern, strings.TrimSpace(string(out)))
	}
	return nil
}
