package worktree

import "testing"

func TestParsePorcelain(t *testing.T) {
	in := `worktree /home/u/repo
HEAD abc123
branch refs/heads/main

worktree /home/u/repo-feature
HEAD def456
branch refs/heads/feature/new-ui

worktree /home/u/repo-detached
HEAD 00deadbeef
detached
`
	got := parsePorcelain(in)
	if len(got) != 3 {
		t.Fatalf("expected 3 worktrees, got %d: %+v", len(got), got)
	}
	if got[0].Path != "/home/u/repo" || got[0].Branch != "main" {
		t.Errorf("worktree 0: %+v", got[0])
	}
	if got[1].Branch != "feature/new-ui" {
		t.Errorf("worktree 1 branch: %q", got[1].Branch)
	}
	if !got[2].IsDetached || got[2].Branch != "" {
		t.Errorf("worktree 2 should be detached: %+v", got[2])
	}
}
