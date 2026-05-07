package domain

import (
	"strings"
	"testing"
)

func TestRepoSlugID(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"github.com/tjst-t/palmux", "tjst-t--palmux--"},
		{"github.com/tjst-t/ansible-nas", "tjst-t--ansible-nas--"},
		{"gitlab.example.com/group/sub/proj", "group--sub--proj--"},
	}
	for _, c := range cases {
		got := RepoSlugID(c.in)
		if len(got) != len(c.want)+hashLen {
			t.Errorf("RepoSlugID(%q) = %q, expected length %d", c.in, got, len(c.want)+hashLen)
		}
		if got[:len(c.want)] != c.want {
			t.Errorf("RepoSlugID(%q) = %q, want prefix %q", c.in, got, c.want)
		}
	}
}

func TestRepoSlugID_Stable(t *testing.T) {
	a := RepoSlugID("github.com/tjst-t/palmux")
	b := RepoSlugID("github.com/tjst-t/palmux")
	if a != b {
		t.Errorf("RepoSlugID not stable: %q vs %q", a, b)
	}
}

func TestRepoSlugID_DifferentInputsCollide(t *testing.T) {
	a := RepoSlugID("github.com/tjst-t/palmux")
	b := RepoSlugID("gitlab.com/tjst-t/palmux")
	// Slugs may match (host stripped), but full IDs must not.
	if a == b {
		t.Errorf("expected different IDs for different hosts, both = %q", a)
	}
}

func TestBranchSlugID(t *testing.T) {
	cases := []struct {
		repo   string
		branch string
		want   string
	}{
		{"/x/y/repo", "main", "main--"},
		{"/x/y/repo", "feature/new-ui", "feature--new-ui--"},
		{"/x/y/repo", "hotfix/v1.2", "hotfix--v1_2--"},
		{"/x/y/palmux2", "v0.5", "v0_5--"},
		{"/x/y/palmux2", "release/2.3", "release--2_3--"},
	}
	for _, c := range cases {
		got := BranchSlugID(c.repo, c.branch)
		if len(got) != len(c.want)+hashLen {
			t.Errorf("BranchSlugID(%q,%q) = %q, expected length %d", c.repo, c.branch, got, len(c.want)+hashLen)
		}
		if got[:len(c.want)] != c.want {
			t.Errorf("BranchSlugID(%q,%q) = %q, want prefix %q", c.repo, c.branch, got, c.want)
		}
	}
}

func TestBranchSlugID_PerRepo(t *testing.T) {
	// Same branch name in different repos must yield different IDs (hash differs).
	a := BranchSlugID("/x/repo-a", "main")
	b := BranchSlugID("/x/repo-b", "main")
	if a == b {
		t.Errorf("expected per-repo branch IDs to differ, both = %q", a)
	}
}

// S1e8d02: WorkspaceSlugIDFromPath must be invariant under branch checkout
// (path is fixed; branch name is irrelevant) and must distinguish primary
// from non-primary worktrees by their basename.
func TestWorkspaceSlugIDFromPath_PathInvariant(t *testing.T) {
	repoFullPath := "/x/y/palmux2"
	// Same primary path under any branch must yield the same ID.
	a := WorkspaceSlugIDFromPath(repoFullPath, true, repoFullPath)
	b := WorkspaceSlugIDFromPath(repoFullPath, true, repoFullPath)
	if a != b {
		t.Errorf("primary path-based ID not stable: %q vs %q", a, b)
	}
	// Slug must be the repo dir basename ("palmux2").
	if !strings.HasPrefix(a, "palmux2--") {
		t.Errorf("primary slug, got %q want prefix palmux2--", a)
	}
}

func TestWorkspaceSlugIDFromPath_PrimaryVsLinked(t *testing.T) {
	repoFullPath := "/ghq/github.com/tjst-t/palmux2"
	primary := WorkspaceSlugIDFromPath(repoFullPath, true, repoFullPath)
	// Linked worktree at sibling path gwq.dir/<repo>/<branch-slug>.
	linked := WorkspaceSlugIDFromPath(
		"/gwq/tjst-t/palmux2/feature-x", false, repoFullPath,
	)
	if primary == linked {
		t.Errorf("primary and linked must differ, both = %q", primary)
	}
	// Linked slug uses worktree dir basename ("feature-x").
	if !strings.HasPrefix(linked, "feature-x--") {
		t.Errorf("linked slug, got %q want prefix feature-x--", linked)
	}
	// Primary slug uses repo dir basename ("palmux2").
	if !strings.HasPrefix(primary, "palmux2--") {
		t.Errorf("primary slug, got %q want prefix palmux2--", primary)
	}
}

func TestWorkspaceSlugIDFromPath_DifferentLinkedSamePath(t *testing.T) {
	// Two non-primary worktrees with same basename but different paths
	// must differ (hash distinguishes them).
	a := WorkspaceSlugIDFromPath("/gwq/repoA/x", false, "/x/repoA")
	b := WorkspaceSlugIDFromPath("/gwq/repoB/x", false, "/x/repoB")
	if a == b {
		t.Errorf("different paths but same basename collided: %q", a)
	}
}

func TestSanitizeSlug(t *testing.T) {
	if got := sanitizeSlug("foo/bar"); got != "foo_bar" {
		t.Errorf("sanitizeSlug = %q, want foo_bar", got)
	}
	if got := sanitizeSlug("ok.name-1_2"); got != "ok_name-1_2" {
		t.Errorf("sanitizeSlug should sanitize '.' for tmux compat, got %q", got)
	}
	if got := sanitizeSlug("v0.5"); got != "v0_5" {
		t.Errorf("sanitizeSlug(v0.5) = %q, want v0_5", got)
	}
}

func TestTabID(t *testing.T) {
	cases := []struct {
		typ, name, want string
	}{
		{"claude", "", "claude"},
		{"files", "", "files"},
		{"bash", "bash", "bash:bash"},
		{"bash", "my-server", "bash:my-server"},
		{"bash", "weird/name", "bash:weird_name"},
	}
	for _, c := range cases {
		if got := TabID(c.typ, c.name); got != c.want {
			t.Errorf("TabID(%q,%q) = %q, want %q", c.typ, c.name, got, c.want)
		}
	}
}
