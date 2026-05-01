package git

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// initRepo creates a fresh git repo with one initial commit on `main`.
func initRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-b", "main"},
		{"config", "user.email", "test@example.com"},
		{"config", "user.name", "Test"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"add", "seed.txt"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return dir
}

func TestCommit(t *testing.T) {
	repo := initRepo(t)
	if err := os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := Stage(context.Background(), repo, "a.txt"); err != nil {
		t.Fatalf("Stage: %v", err)
	}
	res, err := Commit(context.Background(), repo, CommitOptions{Message: "feat: add a"})
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if res.Hash == "" {
		t.Fatalf("Commit: empty hash")
	}
	if res.Subject != "feat: add a" {
		t.Errorf("Commit subject = %q, want %q", res.Subject, "feat: add a")
	}
}

func TestCommitAmend(t *testing.T) {
	repo := initRepo(t)
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a"), 0o644)
	Stage(context.Background(), repo, "a.txt")
	Commit(context.Background(), repo, CommitOptions{Message: "feat: a"})
	os.WriteFile(filepath.Join(repo, "a.txt"), []byte("a2"), 0o644)
	Stage(context.Background(), repo, "a.txt")
	res, err := Commit(context.Background(), repo, CommitOptions{Amend: true, Message: "feat: a (amended)"})
	if err != nil {
		t.Fatalf("Commit amend: %v", err)
	}
	if res.Subject != "feat: a (amended)" {
		t.Errorf("amend subject = %q", res.Subject)
	}
}

func TestStageLines(t *testing.T) {
	repo := initRepo(t)
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte("a\nb\nc\nd\n"), 0o644)
	Stage(context.Background(), repo, "f.txt")
	Commit(context.Background(), repo, CommitOptions{Message: "init f"})
	// Modify lines 2 and 4 in the working copy.
	os.WriteFile(filepath.Join(repo, "f.txt"), []byte("a\nB\nc\nD\n"), 0o644)
	// Stage only the new line for "B" (i.e. new-file line 2).
	if err := StageLines(context.Background(), repo, "f.txt", []LineRange{{Start: 2, End: 2}}); err != nil {
		t.Fatalf("StageLines: %v", err)
	}
	// `git diff --cached` should now show "B" but not "D".
	cached, _ := RawDiff(context.Background(), repo, DiffStaged, "f.txt")
	if !strings.Contains(cached, "+B") {
		t.Errorf("cached diff missing +B:\n%s", cached)
	}
	if strings.Contains(cached, "+D") {
		t.Errorf("cached diff should not contain +D:\n%s", cached)
	}
}

func TestPushFetchPull(t *testing.T) {
	repo := initRepo(t)
	// Bare remote — playable as "origin" with file:// URL.
	bare := t.TempDir()
	cmd := exec.Command("git", "init", "--bare", bare)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init --bare: %v\n%s", err, out)
	}
	for _, args := range [][]string{
		{"remote", "add", "origin", bare},
	} {
		c := exec.Command("git", args...)
		c.Dir = repo
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	out, err := Push(context.Background(), repo, PushOptions{SetUpstream: true, Branch: "main"})
	if err != nil {
		t.Fatalf("Push: %v\noutput: %s", err, out)
	}
	if _, err := Fetch(context.Background(), repo, FetchOptions{}); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if _, err := Pull(context.Background(), repo, PullOptions{FFOnly: true}); err != nil {
		t.Fatalf("Pull: %v", err)
	}
}

func TestBranchCRUD(t *testing.T) {
	repo := initRepo(t)
	if err := CreateBranch(context.Background(), repo, CreateBranchOptions{Name: "feature/x"}); err != nil {
		t.Fatalf("CreateBranch: %v", err)
	}
	if err := SwitchBranch(context.Background(), repo, "feature/x"); err != nil {
		t.Fatalf("SwitchBranch: %v", err)
	}
	if err := SwitchBranch(context.Background(), repo, "main"); err != nil {
		t.Fatalf("SwitchBranch back: %v", err)
	}
	if err := DeleteBranch(context.Background(), repo, DeleteBranchOptions{Name: "feature/x"}); err != nil {
		t.Fatalf("DeleteBranch: %v", err)
	}
}

func TestAICommitPrompt(t *testing.T) {
	repo := initRepo(t)
	// Nothing staged → error.
	if _, err := AICommitPrompt(context.Background(), repo); err == nil {
		t.Errorf("AICommitPrompt with empty stage: expected error")
	}
	os.WriteFile(filepath.Join(repo, "x.txt"), []byte("x\n"), 0o644)
	Stage(context.Background(), repo, "x.txt")
	prompt, err := AICommitPrompt(context.Background(), repo)
	if err != nil {
		t.Fatalf("AICommitPrompt: %v", err)
	}
	if !strings.Contains(prompt, "x.txt") {
		t.Errorf("prompt missing path:\n%s", prompt)
	}
	if !strings.Contains(prompt, "Conventional-Commits") {
		t.Errorf("prompt missing format guidance")
	}
}
