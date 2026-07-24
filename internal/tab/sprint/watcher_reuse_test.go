package sprint

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// waitForSub polls until the background subscribe has installed a subscription
// for key (S3f3cb2 made registration async), or the deadline passes.
func waitForSub(t *testing.T, p *Provider, key string, d time.Duration) (watchSub, bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got, ok := p.subs[key]
		p.mu.Unlock()
		if ok {
			return got, true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return watchSub{}, false
}

// TestOnBranchOpen_ReusesSubscriptionForSameWorktree is the regression test for
// the latency defect the user reported as "opening a Bash tab shows black for
// seconds", "adding a tab is slow" and "Reconnecting takes forever".
//
// OnBranchOpen runs from store.ensureBranchSession, which is on the path of BOTH
// tab creation and every WS attach. It used to Unsubscribe + Subscribe the
// worktree watcher unconditionally, and registering inotify watches recursively
// over a real worktree measured ~2.0 s for this provider (~1.7 s for git) —
// i.e. essentially all of the reported latency.
//
// The subscription must now be reused when the worktree behind the branch key
// has not changed.
func TestOnBranchOpen_ReusesSubscriptionForSameWorktree(t *testing.T) {
	dir := t.TempDir()
	p := New(nil)
	branch := &domain.Branch{ID: "b--0001", RepoID: "r--0001", Name: "main", WorktreePath: dir}
	key := branch.RepoID + "/" + branch.ID

	if _, err := p.OnBranchOpen(context.Background(), tab.OpenParams{Branch: branch}); err != nil {
		t.Fatalf("OnBranchOpen #1: %v", err)
	}
	first, ok := waitForSub(t, p, key, 10*time.Second)
	if !ok {
		t.Skip("worktree watcher unavailable in this environment (no inotify)")
	}

	for i := 2; i <= 5; i++ {
		if _, err := p.OnBranchOpen(context.Background(), tab.OpenParams{Branch: branch}); err != nil {
			t.Fatalf("OnBranchOpen #%d: %v", i, err)
		}
	}

	p.mu.Lock()
	got := p.subs[key]
	n := len(p.subs)
	p.mu.Unlock()

	if got.sub != first.sub {
		t.Error("OnBranchOpen re-subscribed the worktree watcher for an unchanged worktree; " +
			"that costs seconds on a large repo and runs on every tab open / WS attach")
	}
	if got.root != dir {
		t.Errorf("subscription root = %q, want %q", got.root, dir)
	}
	if n != 1 {
		t.Errorf("subs map holds %d entries, want 1", n)
	}
}

// TestOnBranchOpen_ResubscribesWhenWorktreeChanged keeps the guard the old
// unconditional Unsubscribe was there for: "branch closed and reopened without
// provider teardown". If the worktree behind the same key changed, the stale
// subscription must be replaced, not reused.
func TestOnBranchOpen_ResubscribesWhenWorktreeChanged(t *testing.T) {
	dirA, dirB := t.TempDir(), t.TempDir()
	p := New(nil)
	key := "r--0001/b--0001"

	branchA := &domain.Branch{ID: "b--0001", RepoID: "r--0001", Name: "main", WorktreePath: dirA}
	if _, err := p.OnBranchOpen(context.Background(), tab.OpenParams{Branch: branchA}); err != nil {
		t.Fatalf("OnBranchOpen A: %v", err)
	}
	first, ok := waitForSub(t, p, key, 10*time.Second)
	if !ok {
		t.Skip("worktree watcher unavailable in this environment (no inotify)")
	}

	branchB := &domain.Branch{ID: "b--0001", RepoID: "r--0001", Name: "main", WorktreePath: dirB}
	if _, err := p.OnBranchOpen(context.Background(), tab.OpenParams{Branch: branchB}); err != nil {
		t.Fatalf("OnBranchOpen B: %v", err)
	}

	var got watchSub
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		got = p.subs[key]
		p.mu.Unlock()
		if got.root == dirB {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if got.sub == first.sub {
		t.Error("worktree changed for this branch key but the stale subscription was reused; " +
			"events would keep firing for the old path")
	}
	if got.root != dirB {
		t.Errorf("subscription root = %q, want the new worktree %q", got.root, dirB)
	}
}

// TestReusedSubscription_StillDeliversEvents is AC-S3f3cb2-1-4: reusing the
// subscription must not cost us the notification it exists for. After several
// OnBranchOpen calls, a docs/ROADMAP.json write must still reach handleEvents,
// which publishes sprint.changed on the store hub.
func TestReusedSubscription_StillDeliversEvents(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfgDir := t.TempDir()
	repoStore, err := config.NewRepoStore(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	settings, err := config.NewSettingsStore(cfgDir)
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.New(store.Deps{
		Tmux: tmux.NewMockClient(), RepoStore: repoStore, Settings: settings,
		Registry: tab.NewRegistry(), GHQRoot: cfgDir,
	})
	if err != nil {
		t.Fatal(err)
	}

	p := New(st)
	branch := &domain.Branch{ID: "b--0001", RepoID: "r--0001", Name: "main", WorktreePath: dir}
	for i := 0; i < 3; i++ {
		if _, err := p.OnBranchOpen(context.Background(), tab.OpenParams{Branch: branch}); err != nil {
			t.Fatalf("OnBranchOpen #%d: %v", i+1, err)
		}
	}
	if _, ok := waitForSub(t, p, "r--0001/b--0001", 10*time.Second); !ok {
		t.Skip("worktree watcher unavailable in this environment (no inotify)")
	}

	ch, unsub := st.Hub().Subscribe()
	defer unsub()

	if err := os.WriteFile(filepath.Join(dir, "docs", "ROADMAP.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	deadline := time.After(15 * time.Second)
	for {
		select {
		case ev := <-ch:
			if ev.Type == EventSprintChanged {
				return // the reused subscription still delivers
			}
		case <-deadline:
			t.Fatal("no sprint.changed within 15s of creating docs/ROADMAP.json — reusing the " +
				"subscription silently lost the notification (AC-S3f3cb2-1-4)")
		}
	}
}

// TestOnBranchOpen_ReturnsWithoutWaitingForRegistration is the point of the
// async change (S3f3cb2): OnBranchOpen sits on ensureBranchSession, which runs
// on every tab creation AND every WS attach. Registering an inotify watch costs
// one syscall per directory and measured ~1.8 s on a real repo, so the call
// must not wait for it.
func TestOnBranchOpen_ReturnsWithoutWaitingForRegistration(t *testing.T) {
	dir := t.TempDir()
	// The tree must be big enough that a SYNCHRONOUS registration would blow
	// the threshold below — otherwise this test passes against the very bug it
	// is supposed to catch. Registration runs at roughly 15 dirs/ms here, so
	// 6000 directories costs ~400 ms synchronously versus a few map operations
	// asynchronously. (An earlier draft used 300 dirs and did NOT fail when the
	// async call was reverted to a synchronous one.)
	const dirs = 6000
	for i := 0; i < dirs; i++ {
		if err := os.MkdirAll(filepath.Join(dir, "docs", strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := New(nil)
	branch := &domain.Branch{ID: "b--0001", RepoID: "r--0001", Name: "main", WorktreePath: dir}

	start := time.Now()
	if _, err := p.OnBranchOpen(context.Background(), tab.OpenParams{Branch: branch}); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)

	if _, ok := waitForSub(t, p, "r--0001/b--0001", 10*time.Second); !ok {
		t.Skip("worktree watcher unavailable in this environment (no inotify)")
	}
	// The registration itself is what we moved off the caller's thread; the
	// synchronous part is just a couple of map operations.
	if elapsed > 150*time.Millisecond {
		t.Errorf("OnBranchOpen blocked for %v — registration must happen in the background", elapsed)
	}
}

// TestOnBranchClose_DuringInFlightSubscribeLeavesNothing pins the hazard the
// async path introduces: a subscription that finishes registering AFTER the
// branch closed must throw itself away, not install a watcher nobody will ever
// unsubscribe. `desired` is what makes that decidable.
func TestOnBranchClose_DuringInFlightSubscribeLeavesNothing(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 300; i++ {
		if err := os.MkdirAll(filepath.Join(dir, "docs", strconv.Itoa(i)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	p := New(nil)
	branch := &domain.Branch{ID: "b--0001", RepoID: "r--0001", Name: "main", WorktreePath: dir}

	if _, err := p.OnBranchOpen(context.Background(), tab.OpenParams{Branch: branch}); err != nil {
		t.Fatal(err)
	}
	// Close immediately — the background subscribe is very likely still running.
	if err := p.OnBranchClose(context.Background(), tab.CloseParams{Branch: branch}); err != nil {
		t.Fatal(err)
	}

	// Give any in-flight registration time to land and discard itself.
	time.Sleep(2 * time.Second)

	p.mu.Lock()
	nSubs, nDesired, nInflight := len(p.subs), len(p.desired), len(p.inflight)
	p.mu.Unlock()

	if nSubs != 0 {
		t.Errorf("a subscription survived OnBranchClose (%d) — an in-flight background "+
			"subscribe installed a watcher for a closed branch", nSubs)
	}
	if nDesired != 0 || nInflight != 0 {
		t.Errorf("bookkeeping leaked after close: desired=%d inflight=%d", nDesired, nInflight)
	}
}
