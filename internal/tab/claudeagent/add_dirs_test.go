package claudeagent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeBranchResolver returns a fixed worktree path regardless of inputs.
type fakeBranchResolver struct{ root string }

func (f *fakeBranchResolver) WorktreePath(repoID, branchID string) (string, error) {
	return f.root, nil
}

func newTestManager(t *testing.T, worktreeRoot string) *Manager {
	t.Helper()
	return &Manager{
		branches: &fakeBranchResolver{root: worktreeRoot},
	}
}

func TestValidateAddDirs_AcceptsRelativeInsideWorktree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "internal/tab"))

	m := newTestManager(t, root)
	out, err := m.validateAddDirs("repo", "branch", []string{"internal", "internal/tab"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if len(out) != 2 {
		t.Fatalf("expected 2 entries, got %d (%v)", len(out), out)
	}
	if !strings.HasPrefix(out[0], root) || !strings.HasPrefix(out[1], root) {
		t.Fatalf("expected absolute paths under %s, got %v", root, out)
	}
}

func TestValidateAddDirs_RejectsParentTraversal(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	m := newTestManager(t, root)
	cases := []string{
		"../etc/passwd",
		"foo/../../etc",
		"..",
	}
	for _, c := range cases {
		_, err := m.validateAddDirs("repo", "branch", []string{c})
		if err == nil {
			t.Fatalf("expected traversal error for %q, got nil", c)
		}
	}
}

func TestValidateAddDirs_RejectsAbsoluteOutsideWorktree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	other := t.TempDir() // separate temp dir, definitely outside `root`
	m := newTestManager(t, root)
	_, err := m.validateAddDirs("repo", "branch", []string{other})
	if err == nil {
		t.Fatalf("expected outside-worktree error, got nil")
	}
	if !strings.Contains(err.Error(), "outside worktree") {
		t.Fatalf("expected outside-worktree error, got %v", err)
	}
}

func TestValidateAddDirs_AcceptsAbsoluteInsideWorktree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	sub := filepath.Join(root, "vendor/foo")
	mustMkdir(t, sub)
	m := newTestManager(t, root)
	out, err := m.validateAddDirs("repo", "branch", []string{sub})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 || out[0] != sub {
		t.Fatalf("expected [%s], got %v", sub, out)
	}
}

func TestValidateAddDirs_DedupesDuplicates(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, "a"))
	m := newTestManager(t, root)
	out, err := m.validateAddDirs("repo", "branch", []string{"a", "a", "a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 1 {
		t.Fatalf("expected 1 deduped entry, got %v", out)
	}
}

func TestValidateAddDirs_RejectsSymlinkEscape(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	other := t.TempDir()
	link := filepath.Join(root, "escape")
	if err := os.Symlink(other, link); err != nil {
		t.Skipf("symlink creation unsupported: %v", err)
	}
	m := newTestManager(t, root)
	_, err := m.validateAddDirs("repo", "branch", []string{"escape"})
	if err == nil {
		t.Fatalf("expected symlink-escape error, got nil")
	}
}

func TestValidateAddDirs_DropsEmpty(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	m := newTestManager(t, root)
	out, err := m.validateAddDirs("repo", "branch", []string{"", "", ""})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("expected empty slice, got %v", out)
	}
}

func TestMergeAddDirs_GrowsAndDedupes(t *testing.T) {
	t.Parallel()
	a := &Agent{}
	a.addDirs = []string{"/a", "/b"}
	need, merged := a.mergeAddDirs([]string{"/b", "/c"})
	if !need {
		t.Fatalf("expected respawn needed when /c is new, got false")
	}
	if got, want := strings.Join(merged, ","), "/a,/b,/c"; got != want {
		t.Fatalf("merged: got %q want %q", got, want)
	}

	a.addDirs = merged
	need, _ = a.mergeAddDirs([]string{"/a", "/b"})
	if need {
		t.Fatalf("expected no respawn when subset, got true")
	}
}

func mustMkdir(t *testing.T, p string) {
	t.Helper()
	if err := os.MkdirAll(p, 0o755); err != nil {
		t.Fatal(err)
	}
}
