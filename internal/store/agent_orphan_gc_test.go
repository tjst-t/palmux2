package store

import (
	"context"
	"testing"

	"github.com/tjst-t/palmux2/internal/domain"
)

// fakeAgentOrphanGC is a test double for AgentOrphanGC — mirrors
// fakeTuiOrphanGC (tui_orphan_gc_test.go) exactly, since [Store.gcAgentOrphans]
// wires the SAME shape of interface for the Claude AGENT tab's pipe-mode
// ptyhosts (S64c835-3, claudetui parity).
type fakeAgentOrphanGC struct {
	calls      int
	lastIsLive func(repoID, branchID, tabID string) bool
	shutdown   int
	cleaned    int
	err        error
}

func (f *fakeAgentOrphanGC) GCOrphans(_ context.Context, isLive func(repoID, branchID, tabID string) bool) (int, int, error) {
	f.calls++
	f.lastIsLive = isLive
	return f.shutdown, f.cleaned, f.err
}

// TestGCAgentOrphans_NoopWithoutGC verifies gcAgentOrphans does nothing (no
// panic, no-op) when SetAgentOrphanGC was never called — every existing
// store test that doesn't wire one must be unaffected by S64c835-3.
func TestGCAgentOrphans_NoopWithoutGC(t *testing.T) {
	s, _ := newStoreFixture(t)
	s.gcAgentOrphans(context.Background()) // must not panic
}

// TestGCAgentOrphans_CallsWiredGC verifies SetAgentOrphanGC wires
// gcAgentOrphans to call through, and that the isLive callback it passes
// correctly reflects Store.Tab() — the "still referenced" test
// claudeagent.Manager.GCOrphans relies on to decide whether an on-disk
// ptyhost is an orphan.
func TestGCAgentOrphans_CallsWiredGC(t *testing.T) {
	s, _ := newStoreFixture(t)

	repoID := "tjst-t--demo--abcd"
	branchID := injectBranch(t, s, repoID, "/tmp/repo-demo", "main", true)

	// Give the injected branch a claude AGENT tab so isTuiTabLive (reused,
	// see gcAgentOrphans's doc comment) can find it.
	s.mu.Lock()
	repo := s.repos[repoID]
	for _, b := range repo.OpenBranches {
		if b.ID == branchID {
			b.TabSet.Tabs = append(b.TabSet.Tabs, domain.Tab{ID: "claude:claude", Type: "claude"})
		}
	}
	s.mu.Unlock()

	fake := &fakeAgentOrphanGC{shutdown: 2, cleaned: 1}
	s.SetAgentOrphanGC(fake)

	s.gcAgentOrphans(context.Background())

	if fake.calls != 1 {
		t.Fatalf("GCOrphans called %d times, want 1", fake.calls)
	}
	if fake.lastIsLive == nil {
		t.Fatal("gcAgentOrphans did not pass an isLive callback")
	}

	// The real tab must be reported live.
	if !fake.lastIsLive(repoID, branchID, "claude:claude") {
		t.Error("isLive(existing tab) = false, want true")
	}
	// A tabID that was never added to this branch must be reported not-live
	// (this is the case S64c835-3 orphan GC exists for — tab delete).
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

// TestGCAgentOrphans_TuiAndAgentGC_BothWired verifies both orphan reapers
// (claude-tui's and claude-agent's) are invoked independently on the same
// scan tick when both are wired — the actual production wiring
// (runPortScan calls gcTuiOrphans then gcAgentOrphans on every 10s tick).
func TestGCAgentOrphans_TuiAndAgentGC_BothWired(t *testing.T) {
	s, _ := newStoreFixture(t)

	fakeTui := &fakeTuiOrphanGC{}
	fakeAgent := &fakeAgentOrphanGC{}
	s.SetTuiOrphanGC(fakeTui)
	s.SetAgentOrphanGC(fakeAgent)

	s.gcTuiOrphans(context.Background())
	s.gcAgentOrphans(context.Background())

	if fakeTui.calls != 1 {
		t.Errorf("tui GCOrphans called %d times, want 1", fakeTui.calls)
	}
	if fakeAgent.calls != 1 {
		t.Errorf("agent GCOrphans called %d times, want 1", fakeAgent.calls)
	}
}
