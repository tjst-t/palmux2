// Package gwq wraps the `gwq` worktree-management CLI.
//
// Palmux delegates worktree creation/deletion to gwq so the worktree path
// layout is whatever the user has configured globally — Palmux itself never
// decides where worktrees live.
package gwq

import (
	"context"
	"fmt"
	"os/exec"
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

// Remove deletes a worktree by branch-name pattern. Does NOT delete the
// branch itself (matches gwq default behaviour).
func (c *Client) Remove(ctx context.Context, repoDir, pattern string) error {
	if pattern == "" {
		return fmt.Errorf("gwq.Remove: empty pattern")
	}
	cmd := exec.CommandContext(ctx, c.bin, "remove", pattern)
	cmd.Dir = repoDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("gwq remove %s: %s", pattern, strings.TrimSpace(string(out)))
	}
	return nil
}
