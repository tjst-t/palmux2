package claudeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestOffsetStore_SaveGet_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewOffsetStore(dir)
	if err != nil {
		t.Fatalf("NewOffsetStore: %v", err)
	}

	if _, ok := s.Get("repo", "branch", "claude:claude"); ok {
		t.Fatal("Get on empty store returned ok=true, want false")
	}

	if err := s.Save("repo", "branch", "claude:claude", 4242, "gen-1"); err != nil {
		t.Fatalf("Save: %v", err)
	}
	rec, ok := s.Get("repo", "branch", "claude:claude")
	if !ok {
		t.Fatal("Get after Save returned ok=false")
	}
	if rec.LastAckOffset != 4242 {
		t.Fatalf("LastAckOffset = %d, want 4242", rec.LastAckOffset)
	}
	if rec.RingGeneration != "gen-1" {
		t.Fatalf("RingGeneration = %q, want %q", rec.RingGeneration, "gen-1")
	}
	if rec.UpdatedAt.IsZero() {
		t.Fatal("UpdatedAt is zero, want set")
	}
}

// TestOffsetStore_SurvivesKill9_AtomicWriteThenRename covers the
// "crash-mid-write must never leave a torn file" requirement: a fresh
// OffsetStore reading the SAME directory after the writer is discarded
// (simulating a palmux2 process that was kill -9'd right after Save
// returned) sees exactly the last successfully-saved value, never a
// partial/corrupt one.
func TestOffsetStore_SurvivesKill9_AtomicWriteThenRename(t *testing.T) {
	dir := t.TempDir()
	s1, err := NewOffsetStore(dir)
	if err != nil {
		t.Fatalf("NewOffsetStore: %v", err)
	}
	for i, off := range []int64{10, 200, 3000, 40000} {
		if err := s1.Save("repo", "branch", "claude:claude", off, ""); err != nil {
			t.Fatalf("Save #%d: %v", i, err)
		}
	}
	// s1 is discarded here WITHOUT any explicit close (OffsetStore has none
	// — every Save already fsync-independent atomic-renamed to disk) —
	// simulating the process dying immediately after the last Save
	// returned.

	// No .tmp file must remain (a leftover .tmp would indicate a non-atomic
	// or interrupted write).
	if _, err := os.Stat(filepath.Join(dir, "agent_offsets.json.tmp")); !os.IsNotExist(err) {
		t.Fatalf("agent_offsets.json.tmp still exists after Save sequence (err=%v) — write is not properly atomic", err)
	}

	s2, err := NewOffsetStore(dir)
	if err != nil {
		t.Fatalf("NewOffsetStore (reload): %v", err)
	}
	rec, ok := s2.Get("repo", "branch", "claude:claude")
	if !ok {
		t.Fatal("reloaded store has no record")
	}
	if rec.LastAckOffset != 40000 {
		t.Fatalf("reloaded LastAckOffset = %d, want 40000 (the last successful Save)", rec.LastAckOffset)
	}

	// The on-disk file itself must be valid, complete JSON (not truncated).
	b, err := os.ReadFile(filepath.Join(dir, "agent_offsets.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var shape offsetPersistedShape
	if err := json.Unmarshal(b, &shape); err != nil {
		t.Fatalf("on-disk agent_offsets.json is not valid JSON: %v\ncontents: %s", err, b)
	}
}

func TestOffsetStore_Clear(t *testing.T) {
	dir := t.TempDir()
	s, err := NewOffsetStore(dir)
	if err != nil {
		t.Fatalf("NewOffsetStore: %v", err)
	}
	if err := s.Save("repo", "branch", "claude:claude", 99, ""); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := s.Clear("repo", "branch", "claude:claude"); err != nil {
		t.Fatalf("Clear: %v", err)
	}
	if _, ok := s.Get("repo", "branch", "claude:claude"); ok {
		t.Fatal("Get after Clear returned ok=true, want false")
	}
	// Clearing an already-absent key must be a harmless no-op.
	if err := s.Clear("repo", "branch", "claude:claude"); err != nil {
		t.Fatalf("Clear (already absent): %v", err)
	}
}

// TestOffsetStore_KeysAreTabScoped_AndCanonicalised covers that distinct
// (repoID, branchID, tabID) triples don't collide, and that the legacy
// bare-"claude" tabID canonicalises the same way tabKey (store.go) does —
// the two stores must agree on tab identity.
func TestOffsetStore_KeysAreTabScoped_AndCanonicalised(t *testing.T) {
	dir := t.TempDir()
	s, err := NewOffsetStore(dir)
	if err != nil {
		t.Fatalf("NewOffsetStore: %v", err)
	}
	if err := s.Save("repoA", "branch1", "claude:claude", 1, ""); err != nil {
		t.Fatalf("Save A: %v", err)
	}
	if err := s.Save("repoA", "branch1", "claude:claude-2", 2, ""); err != nil {
		t.Fatalf("Save B: %v", err)
	}
	if err := s.Save("repoB", "branch1", "claude:claude", 3, ""); err != nil {
		t.Fatalf("Save C: %v", err)
	}

	a, ok := s.Get("repoA", "branch1", "claude:claude")
	if !ok || a.LastAckOffset != 1 {
		t.Fatalf("repoA/branch1/claude:claude = %+v (ok=%v), want offset 1", a, ok)
	}
	b, ok := s.Get("repoA", "branch1", "claude:claude-2")
	if !ok || b.LastAckOffset != 2 {
		t.Fatalf("repoA/branch1/claude:claude-2 = %+v (ok=%v), want offset 2", b, ok)
	}
	c, ok := s.Get("repoB", "branch1", "claude:claude")
	if !ok || c.LastAckOffset != 3 {
		t.Fatalf("repoB/branch1/claude:claude = %+v (ok=%v), want offset 3", c, ok)
	}

	// Legacy bare "claude" / empty tabID must canonicalise to the same key
	// as "claude:claude" (mirrors CanonicaliseTabID / tabKey in store.go).
	legacy, ok := s.Get("repoA", "branch1", "claude")
	if !ok || legacy.LastAckOffset != 1 {
		t.Fatalf("legacy tabID \"claude\" = %+v (ok=%v), want to resolve to offset 1 (same as claude:claude)", legacy, ok)
	}
	empty, ok := s.Get("repoA", "branch1", "")
	if !ok || empty.LastAckOffset != 1 {
		t.Fatalf("empty tabID = %+v (ok=%v), want to resolve to offset 1 (same as claude:claude)", empty, ok)
	}
}
