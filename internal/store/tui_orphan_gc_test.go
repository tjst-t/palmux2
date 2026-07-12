package store

import (
	"context"
	"testing"

	"github.com/tjst-t/palmux2/internal/domain"
)

// fakeTuiOrphanGC is a test double for TuiOrphanGC that records the isLive
// callback it was invoked with (so the test can drive it against known
// (repoID, branchID, tabID) tuples) and returns pre-programmed counts.
type fakeTuiOrphanGC struct {
	calls      int
	lastIsLive func(repoID, branchID, tabID string) bool
	shutdown   int
	cleaned    int
	err        error
}

func (f *fakeTuiOrphanGC) GCOrphans(_ context.Context, isLive func(repoID, branchID, tabID string) bool) (int, int, error) {
	f.calls++
	f.lastIsLive = isLive
	return f.shutdown, f.cleaned, f.err
}

// TestGCTuiOrphans_NoopWithoutGC verifies gcTuiOrphans does nothing (no
// panic, no-op) when SetTuiOrphanGC was never called — every existing store
// test that doesn't wire one must be unaffected by S3f2658-3.
func TestGCTuiOrphans_NoopWithoutGC(t *testing.T) {
	s, _ := newStoreFixture(t)
	s.gcTuiOrphans(context.Background()) // must not panic
}

// TestGCTuiOrphans_CallsWiredGC verifies SetTuiOrphanGC wires gcTuiOrphans to
// call through, and that the isLive callback it passes correctly reflects
// Store.Tab() — the "still referenced" test claudetui.Manager.GCOrphans
// relies on to decide whether an on-disk ptyhost is an orphan.
func TestGCTuiOrphans_CallsWiredGC(t *testing.T) {
	s, _ := newStoreFixture(t)

	repoID := "tjst-t--demo--abcd"
	branchID := injectBranch(t, s, repoID, "/tmp/repo-demo", "main", true)

	// Give the injected branch a claude-tui tab so isTuiTabLive can find it.
	s.mu.Lock()
	repo := s.repos[repoID]
	for _, b := range repo.OpenBranches {
		if b.ID == branchID {
			b.TabSet.Tabs = append(b.TabSet.Tabs, domain.Tab{ID: "claude:claude", Type: "claude-tui"})
		}
	}
	s.mu.Unlock()

	fake := &fakeTuiOrphanGC{shutdown: 2, cleaned: 1}
	s.SetTuiOrphanGC(fake)

	s.gcTuiOrphans(context.Background())

	if fake.calls != 1 {
		t.Fatalf("GCOrphans called %d times, want 1", fake.calls)
	}
	if fake.lastIsLive == nil {
		t.Fatal("gcTuiOrphans did not pass an isLive callback")
	}

	// The real tab must be reported live.
	if !fake.lastIsLive(repoID, branchID, "claude:claude") {
		t.Error("isLive(existing tab) = false, want true")
	}
	// A tabID that was never added to this branch must be reported not-live
	// (this is the case S3f2658-3 orphan GC exists for — tab delete).
	if fake.lastIsLive(repoID, branchID, "claude:claude-2") {
		t.Error("isLive(never-existed tab) = true, want false")
	}
	// A branch that doesn't exist at all (worktree removal / branch close)
	// must also be reported not-live.
	if fake.lastIsLive(repoID, "gone-branch", "claude:claude") {
		t.Error("isLive(gone branch) = true, want false")
	}
	// An unknown repo must also be reported not-live.
	if fake.lastIsLive("gone-repo", branchID, "claude:claude") {
		t.Error("isLive(gone repo) = true, want false")
	}
}

// TestIsTuiTabLive is a focused unit test of the isLive predicate itself.
func TestIsTuiTabLive(t *testing.T) {
	s, _ := newStoreFixture(t)
	repoID := "tjst-t--demo--abcd"
	branchID := injectBranch(t, s, repoID, "/tmp/repo-demo", "main", true)

	if s.isTuiTabLive(repoID, branchID, "claude:claude") {
		t.Error("isTuiTabLive should be false before the tab exists")
	}

	s.mu.Lock()
	repo := s.repos[repoID]
	for _, b := range repo.OpenBranches {
		if b.ID == branchID {
			b.TabSet.Tabs = append(b.TabSet.Tabs, domain.Tab{ID: "claude:claude", Type: "claude-tui"})
		}
	}
	s.mu.Unlock()

	if !s.isTuiTabLive(repoID, branchID, "claude:claude") {
		t.Error("isTuiTabLive should be true once the tab exists")
	}
}
