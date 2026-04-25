package domain

import "testing"

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
		{"/x/y/repo", "hotfix/v1.2", "hotfix--v1.2--"},
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

func TestSanitizeSlug(t *testing.T) {
	if got := sanitizeSlug("foo/bar"); got != "foo_bar" {
		t.Errorf("sanitizeSlug = %q, want foo_bar", got)
	}
	if got := sanitizeSlug("ok.name-1_2"); got != "ok.name-1_2" {
		t.Errorf("sanitizeSlug should leave allowed chars alone, got %q", got)
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
