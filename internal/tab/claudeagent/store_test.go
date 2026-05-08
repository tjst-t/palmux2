package claudeagent

import (
	"testing"
)

// TestStore_ForgetBranch — KillBranch must wipe every persisted trace
// of a closed branch from sessions.json. A leftover Active resume
// pointer was the smoking gun in the v0.5 resurrection bug: the FE's
// auto-resume path picked it up on the next OpenBranch and triggered
// ensureWorktree → gwq.Add, silently re-creating the deleted worktree.
func TestStore_ForgetBranch(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	const repo = "tjst-t--palmux2--2d59"
	const branchA = "v0_5--176a"
	const branchB = "main--5cd5"

	// Seed: each branch gets a resume pointer (which auto-creates a
	// SessionMeta), a tab list, and prefs. Two SetActive calls per
	// branch with different sessionIDs gives us multiple SessionMeta
	// rows to verify the cleanup walk.
	for _, br := range []string{branchA, branchB} {
		if err := s.SetActive(repo, br, "claude:claude", "session-1-"+br, "claude-opus-4-7"); err != nil {
			t.Fatalf("SetActive 1 %s: %v", br, err)
		}
		if err := s.SetActive(repo, br, "claude:claude-2", "session-2-"+br, "claude-opus-4-7"); err != nil {
			t.Fatalf("SetActive 2 %s: %v", br, err)
		}
		if err := s.SetBranchTabs(repo, br, []string{"claude:claude", "claude:claude-2"}); err != nil {
			t.Fatalf("SetBranchTabs %s: %v", br, err)
		}
		if err := s.SetBranchPrefs(repo, br, "claude:claude", BranchPrefs{Model: "x"}); err != nil {
			t.Fatalf("SetBranchPrefs %s: %v", br, err)
		}
	}

	if err := s.ForgetBranch(repo, branchA); err != nil {
		t.Fatalf("ForgetBranch: %v", err)
	}

	// Branch A must be wiped from every map.
	if id := s.ActiveFor(repo, branchA, "claude:claude"); id != "" {
		t.Errorf("active for closed branch survived: %q", id)
	}
	if id := s.ActiveFor(repo, branchA, "claude:claude-2"); id != "" {
		t.Errorf("active (tab 2) for closed branch survived: %q", id)
	}
	if tabs := s.BranchTabs(repo, branchA); len(tabs) != 0 {
		t.Errorf("branchTabs for closed branch survived: %v", tabs)
	}
	if prefs := s.BranchPrefs(repo, branchA, "claude:claude"); prefs != (BranchPrefs{}) {
		t.Errorf("branchPrefs for closed branch survived: %+v", prefs)
	}
	if metas := s.List(repo, branchA); len(metas) != 0 {
		t.Errorf("SessionMeta for closed branch survived: %d entries", len(metas))
	}

	// Branch B must still be intact — we wiped one branch, not the world.
	if id := s.ActiveFor(repo, branchB, "claude:claude"); id != "session-1-"+branchB {
		t.Errorf("active for unrelated branch was wiped: %q", id)
	}
	if tabs := s.BranchTabs(repo, branchB); len(tabs) != 2 {
		t.Errorf("branchTabs for unrelated branch shrank: %v", tabs)
	}
	if prefs := s.BranchPrefs(repo, branchB, "claude:claude"); prefs.Model != "x" {
		t.Errorf("branchPrefs for unrelated branch was wiped: %+v", prefs)
	}
	if metas := s.List(repo, branchB); len(metas) != 2 {
		t.Errorf("SessionMeta for unrelated branch shrank: %d entries (want 2)", len(metas))
	}

	// Idempotent — double-call must not error.
	if err := s.ForgetBranch(repo, branchA); err != nil {
		t.Fatalf("ForgetBranch second call: %v", err)
	}
}
