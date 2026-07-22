package sprint

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
)

// TestTabs_IsSideEffectFree pins the ADR-0012 contract for the provider that
// actually violated it.
//
// Before the split, Tabs' logic lived inside OnBranchOpen, and the Store
// called OnBranchOpen as a query from every recompute path — including the 5 s
// sync loop — while holding its write lock. That meant `startWatcher()` ran
// from there, allocating a real inotify handle under the Store write lock, and
// the watcher it created called back into RecomputeBranchTabs. The Store's own
// comment claimed "recomputeTabs is read-only; it doesn't re-trigger any side
// effects", which was simply untrue.
//
// Tabs() must now be callable an unbounded number of times with no observable
// effect beyond its return value.
func TestTabs_IsSideEffectFree(t *testing.T) {
	dir := t.TempDir()
	docs := filepath.Join(dir, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(docs, "ROADMAP.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}

	p := New(nil)
	branch := &domain.Branch{ID: "b1", RepoID: "r1", Name: "main", WorktreePath: dir}

	for i := 0; i < 100; i++ {
		got, err := p.Tabs(context.Background(), tab.TabsParams{Branch: branch})
		if err != nil {
			t.Fatalf("Tabs #%d: %v", i, err)
		}
		if len(got) != 1 || got[0].Type != TabType {
			t.Fatalf("Tabs #%d = %+v, want exactly one %s tab", i, got, TabType)
		}
	}

	p.mu.Lock()
	watcher, subs := p.watcher, len(p.subs)
	p.mu.Unlock()

	if watcher != nil {
		t.Error("Tabs() started the worktree watcher — that is a side effect and belongs in OnBranchOpen (ADR-0012)")
	}
	if subs != 0 {
		t.Errorf("Tabs() created %d watcher subscription(s); want 0", subs)
	}
}

// TestTabs_IsIdempotentAcrossVisibilityFlip verifies the replacement for the
// removed Conditional() flag: visibility is expressed purely by how many tabs
// the query returns, and it tracks the filesystem on every call.
func TestTabs_IsIdempotentAcrossVisibilityFlip(t *testing.T) {
	dir := t.TempDir()
	docs := filepath.Join(dir, "docs")
	if err := os.MkdirAll(docs, 0o755); err != nil {
		t.Fatal(err)
	}
	roadmap := filepath.Join(docs, "ROADMAP.json")

	p := New(nil)
	branch := &domain.Branch{ID: "b1", RepoID: "r1", Name: "main", WorktreePath: dir}
	count := func() int {
		t.Helper()
		got, err := p.Tabs(context.Background(), tab.TabsParams{Branch: branch})
		if err != nil {
			t.Fatal(err)
		}
		return len(got)
	}

	if n := count(); n != 0 {
		t.Errorf("no ROADMAP.json: want 0 tabs, got %d", n)
	}
	if err := os.WriteFile(roadmap, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Errorf("ROADMAP.json present: want 1 tab, got %d", n)
	}
	if n := count(); n != 1 {
		t.Errorf("repeated call must be idempotent: want 1 tab, got %d", n)
	}
	if err := os.Remove(roadmap); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 0 {
		t.Errorf("ROADMAP.json removed: want 0 tabs, got %d", n)
	}
}

// TestTabs_NilBranchIsSafe — the Store calls Tabs for every provider on every
// recompute, so a defensive nil check is part of the contract.
func TestTabs_NilBranchIsSafe(t *testing.T) {
	p := New(nil)
	got, err := p.Tabs(context.Background(), tab.TabsParams{Branch: nil})
	if err != nil {
		t.Fatalf("Tabs(nil branch): %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want 0 tabs for a nil branch, got %d", len(got))
	}
}
