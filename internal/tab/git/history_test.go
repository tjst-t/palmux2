// Sprint S013 — unit tests for the new history operations.
//
// These tests stand up a fresh fixture repo per case, exercise the
// public API in this package, and assert structural shape (counts,
// fields). Heavier UI / workflow tests live in tests/e2e/s013_*.py.

package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// makeRepo creates a fresh fixture repo with two commits. The closer
// helper removes it.
func makeRepo(t *testing.T) (string, func()) {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) string {
		c := exec.Command("git", args...)
		c.Dir = dir
		c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
		out, err := c.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	run("init", "-b", "main")
	run("config", "user.email", "test@example.com")
	run("config", "user.name", "Test")
	run("config", "commit.gpgsign", "false")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("alpha\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "a.txt")
	run("commit", "-m", "feat: alpha")
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("beta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "b.txt")
	run("commit", "-m", "feat: beta")
	return dir, func() { /* TempDir cleans itself */ }
}

func TestLogFiltered_Basic(t *testing.T) {
	repo, closer := makeRepo(t)
	defer closer()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	entries, err := LogFiltered(ctx, repo, LogFilter{Limit: 50})
	if err != nil {
		t.Fatalf("LogFiltered: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("want 2 commits, got %d", len(entries))
	}
	if !strings.HasPrefix(entries[0].Subject, "feat: beta") {
		t.Errorf("expected beta first, got %q", entries[0].Subject)
	}
	if entries[0].Hash == "" || len(entries[0].Hash) != 40 {
		t.Errorf("bad hash %q", entries[0].Hash)
	}
}

func TestLogFiltered_GrepAndAuthor(t *testing.T) {
	repo, closer := makeRepo(t)
	defer closer()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	got, err := LogFiltered(ctx, repo, LogFilter{Grep: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].Subject, "alpha") {
		t.Fatalf("grep alpha: got %#v", got)
	}
	got, err = LogFiltered(ctx, repo, LogFilter{Author: "Test"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("author=Test should yield 2, got %d", len(got))
	}
}

func TestStashLifecycle(t *testing.T) {
	repo, closer := makeRepo(t)
	defer closer()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// dirty the working tree.
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("alpha-mod\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := StashPush(ctx, repo, StashPushOptions{Message: "wip-test"}); err != nil {
		t.Fatalf("push: %v", err)
	}
	list, err := StashList(ctx, repo)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("want 1 stash, got %d", len(list))
	}
	name := list[0].Name
	diff, err := StashDiff(ctx, repo, name)
	if err != nil {
		t.Fatalf("diff: %v", err)
	}
	if !strings.Contains(diff, "alpha-mod") {
		t.Errorf("stash diff missing change: %s", diff)
	}
	if _, err := StashApply(ctx, repo, name); err != nil {
		t.Fatalf("apply: %v", err)
	}
	// Drop the stash to get to a clean state.
	if err := StashDrop(ctx, repo, name); err != nil {
		t.Fatalf("drop: %v", err)
	}
	list2, _ := StashList(ctx, repo)
	if len(list2) != 0 {
		t.Errorf("want empty, got %d", len(list2))
	}
}

func TestRevertAndReset(t *testing.T) {
	repo, closer := makeRepo(t)
	defer closer()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Get the most recent commit (beta) and revert it.
	entries, _ := LogFiltered(ctx, repo, LogFilter{Limit: 5})
	betaSHA := entries[0].Hash

	if _, err := Revert(ctx, repo, RevertOptions{CommitSHA: betaSHA}); err != nil {
		t.Fatalf("revert: %v", err)
	}
	// After revert we should have 3 commits.
	after, _ := LogFiltered(ctx, repo, LogFilter{Limit: 5})
	if len(after) != 3 {
		t.Errorf("want 3 commits after revert, got %d", len(after))
	}

	// Soft-reset to drop the revert commit. After soft reset HEAD should
	// be back at beta.
	if _, err := Reset(ctx, repo, ResetOptions{CommitSHA: betaSHA, Mode: ResetSoft}); err != nil {
		t.Fatalf("reset soft: %v", err)
	}
	after2, _ := LogFiltered(ctx, repo, LogFilter{Limit: 5})
	if len(after2) != 2 {
		t.Errorf("want 2 commits after soft reset to beta, got %d", len(after2))
	}
}

func TestTagCRUD(t *testing.T) {
	repo, closer := makeRepo(t)
	defer closer()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := CreateTag(ctx, repo, CreateTagOptions{Name: "v0.0.1"}); err != nil {
		t.Fatalf("create lightweight: %v", err)
	}
	if err := CreateTag(ctx, repo, CreateTagOptions{Name: "v0.0.2", Message: "release v2", Annotated: true}); err != nil {
		t.Fatalf("create annotated: %v", err)
	}
	tags, err := TagList(ctx, repo)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(tags) != 2 {
		t.Fatalf("want 2 tags, got %d", len(tags))
	}
	gotAnnotated := false
	for _, t := range tags {
		if t.Name == "v0.0.2" && t.Annotated {
			gotAnnotated = true
		}
	}
	if !gotAnnotated {
		t.Error("annotated v0.0.2 not found")
	}
	if _, err := DeleteTag(ctx, repo, DeleteTagOptions{Name: "v0.0.1"}); err != nil {
		t.Fatalf("delete: %v", err)
	}
	tags2, _ := TagList(ctx, repo)
	if len(tags2) != 1 {
		t.Errorf("want 1 tag after delete, got %d", len(tags2))
	}
}

func TestFileHistoryAndBlame(t *testing.T) {
	repo, closer := makeRepo(t)
	defer closer()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Modify a.txt and commit so it appears in file-history (>1 entry).
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("alpha\nv2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", repo, "add", "a.txt").Run()
	c := exec.Command("git", "commit", "-m", "tweak: a")
	c.Dir = repo
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("commit: %v %s", err, out)
	}

	hist, err := FileHistory(ctx, repo, "a.txt", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(hist) != 2 {
		t.Errorf("want 2 entries for a.txt, got %d", len(hist))
	}

	bl, err := Blame(ctx, repo, "", "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	if len(bl) != 2 {
		t.Errorf("want 2 blame lines, got %d", len(bl))
	}
	if bl[0].Content != "alpha" {
		t.Errorf("blame[0] content %q", bl[0].Content)
	}
	if bl[0].Hash == "" {
		t.Error("blame[0] missing hash")
	}
}

func TestBranchGraphIncludesAllBranches(t *testing.T) {
	repo, closer := makeRepo(t)
	defer closer()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	c := exec.Command("git", "checkout", "-b", "feature/x")
	c.Dir = repo
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("branch x: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "x.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exec.Command("git", "-C", repo, "add", "x.txt").Run()
	c = exec.Command("git", "commit", "-m", "feat: x")
	c.Dir = repo
	c.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := c.CombinedOutput(); err != nil {
		t.Fatalf("commit x: %v %s", err, out)
	}

	g, err := BranchGraph(ctx, repo, 50, true /*all*/)
	if err != nil {
		t.Fatal(err)
	}
	if len(g) < 3 {
		t.Errorf("want >=3 commits across branches, got %d", len(g))
	}
	// At least one entry should have a parent (non-root commits).
	hasParent := false
	for _, e := range g {
		if len(e.Parents) > 0 {
			hasParent = true
			break
		}
	}
	if !hasParent {
		t.Error("graph: no parent edges found")
	}
}
