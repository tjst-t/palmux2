package portman

import "testing"

func TestForRepoMatchesProject(t *testing.T) {
	leases := []Lease{
		{Name: "api", Project: "tjst-t/foo", Worktree: "main"},
		{Name: "web", Project: "tjst-t/foo", Worktree: "feature"},
		{Name: "other", Project: "tjst-t/bar", Worktree: "main"},
	}
	got := ForRepo(leases, "github.com/tjst-t/foo", "main")
	if len(got) != 1 || got[0].Name != "api" {
		t.Fatalf("got %+v", got)
	}
}

func TestForRepoAllWorktrees(t *testing.T) {
	leases := []Lease{
		{Name: "api", Project: "tjst-t/foo", Worktree: "main"},
		{Name: "web", Project: "tjst-t/foo", Worktree: "feature"},
	}
	got := ForRepo(leases, "github.com/tjst-t/foo", "")
	if len(got) != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestStripHostPrefix(t *testing.T) {
	cases := map[string]string{
		"github.com/tjst-t/foo": "tjst-t/foo",
		"tjst-t/foo":            "tjst-t/foo",
		"gitlab.com/x/y":        "x/y",
	}
	for in, want := range cases {
		if got := stripHostPrefix(in); got != want {
			t.Errorf("stripHostPrefix(%q) = %q, want %q", in, got, want)
		}
	}
}
