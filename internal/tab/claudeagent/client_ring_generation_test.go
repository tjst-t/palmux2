package claudeagent

// S862203-3 task 3 / Wave-2 review finding #4: OffsetStore.RingGeneration
// must actually be wired up and CONSULTED, not just carried as a dead
// field. A persisted LastAckOffset is only meaningful for the EXACT
// ptyhost/child instance it was recorded against — a brand new ptyhost's
// ring starts fresh at byte 0, so a stale numeric offset from a PRIOR
// generation must not be blindly resumed (it could even happen to be
// "valid" — i.e. within the new ring's retained window — purely by
// coincidence, silently splicing unrelated bytes into the transcript).

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
)

// TestResumeOffsetFor_SameGeneration_ResumesPersistedOffset is the negative
// control: when the persisted RingGeneration matches the live ptyhost's
// HELLO, the persisted offset IS resumed (proves the guard isn't
// overzealous — same-generation reconnect still gets exact, lossless
// replay).
func TestResumeOffsetFor_SameGeneration_ResumesPersistedOffset(t *testing.T) {
	store, err := NewOffsetStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewOffsetStore: %v", err)
	}
	hello := ptyhost.HelloPayload{Pid: 4242, ArgvHash: "abc123"}
	gen := ringGenerationFor(hello)
	if err := store.Save("repo", "branch", "claude:claude", 555, gen); err != nil {
		t.Fatalf("Save: %v", err)
	}

	c := &Client{
		offsetStore: store,
		repoID:      "repo",
		branchID:    "branch",
		tabID:       "claude:claude",
		logger:      slog.Default(),
	}
	got := c.resumeOffsetFor(hello)
	if got != 555 {
		t.Fatalf("resumeOffsetFor (same generation) = %d, want 555 (the persisted offset)", got)
	}
}

// TestResumeOffsetFor_DifferentGeneration_IgnoresStaleOffset is the
// positive assertion for finding #4: a persisted offset recorded against a
// DIFFERENT ptyhost/child generation (different pid+argvHash — i.e. a
// brand new ptyhost was spawned, e.g. because the old one genuinely died)
// must NOT be resumed, even though the numeric value alone gives no
// indication of staleness. resumeOffsetFor must fall back to -1 ("replay
// from the oldest byte the NEW ring retains") exactly as it does for a
// genuine ring-overflow.
func TestResumeOffsetFor_DifferentGeneration_IgnoresStaleOffset(t *testing.T) {
	store, err := NewOffsetStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewOffsetStore: %v", err)
	}
	oldGen := ringGenerationFor(ptyhost.HelloPayload{Pid: 1111, ArgvHash: "abc123"})
	// A LARGE offset — deliberately chosen so that, if a caller blindly
	// clamped it against a small fresh ring instead of detecting the
	// generation mismatch, the bug would be "attach at a byte offset that
	// happens to look plausible" rather than an obviously-out-of-range
	// number that some OTHER guard might catch by accident.
	if err := store.Save("repo", "branch", "claude:claude", 123456, oldGen); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// A DIFFERENT ptyhost/child instance: different pid (a brand new spawn
	// — the defining trait of "not the same ring"), argvHash may or may not
	// coincide (real claude respawns of an IDENTICAL argv would still get a
	// new pid every time, which alone changes the generation marker).
	newHello := ptyhost.HelloPayload{Pid: 2222, ArgvHash: "abc123"}

	c := &Client{
		offsetStore: store,
		repoID:      "repo",
		branchID:    "branch",
		tabID:       "claude:claude",
		logger:      slog.Default(),
	}
	got := c.resumeOffsetFor(newHello)
	if got != -1 {
		t.Fatalf("resumeOffsetFor (cross-generation) = %d, want -1 (must NOT resume a stale cross-generation offset)", got)
	}
}

// TestResumeOffsetFor_NoRecordedGeneration_TreatsAsUsable covers records
// persisted before RingGeneration existed (or by a caller that never set
// it) — RingGeneration=="" is NOT treated as a mismatch (there's nothing to
// compare against), so the numeric offset is still honoured. This is a
// deliberate backward-compatibility / conservative-default choice: an
// empty marker means "no generation info available", not "known
// different".
func TestResumeOffsetFor_NoRecordedGeneration_TreatsAsUsable(t *testing.T) {
	store, err := NewOffsetStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewOffsetStore: %v", err)
	}
	if err := store.Save("repo", "branch", "claude:claude", 77, ""); err != nil {
		t.Fatalf("Save: %v", err)
	}
	c := &Client{
		offsetStore: store,
		repoID:      "repo",
		branchID:    "branch",
		tabID:       "claude:claude",
		logger:      slog.Default(),
	}
	got := c.resumeOffsetFor(ptyhost.HelloPayload{Pid: 999, ArgvHash: "whatever"})
	if got != 77 {
		t.Fatalf("resumeOffsetFor (no recorded generation) = %d, want 77", got)
	}
}

// TestClient_CrossGeneration_FreshPtyhostDoesNotHangOrCorrupt is the
// end-to-end companion: after the FIRST ptyhost/child is permanently killed
// (Close — genuinely gone, not merely detached) leaving a persisted offset
// behind, a SECOND Client at the SAME identity (same seed → same
// deterministic socket path) launches a BRAND NEW ptyhost/child (a fresh
// generation) and must receive ITS OWN fresh replay starting from its own
// FAKE_NDJSON_START, rather than hanging or misbehaving because the
// persisted offset belonged to a different generation's ring.
func TestClient_CrossGeneration_FreshPtyhostDoesNotHangOrCorrupt(t *testing.T) {
	bin := fakeNDJSONBin(t)
	store, err := NewOffsetStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewOffsetStore: %v", err)
	}
	runDir := t.TempDir()

	// Generation A: emit a handful of lines, let the client fully process
	// and persist an offset well past zero, then permanently kill it
	// (Close, not Detach) so generation A is genuinely gone and its ring
	// will never be reachable again.
	t.Setenv("FAKE_NDJSON_COUNT", "4")
	t.Setenv("FAKE_NDJSON_EXIT_AFTER", "1")
	var muA sync.Mutex
	var seenA int
	cliA, err := NewClient(context.Background(), ClientOptions{
		Binary:         bin,
		Cwd:            t.TempDir(),
		RepoID:         "repo",
		BranchID:       "branch",
		TabID:          "claude:claude",
		RunDirOverride: runDir,
		OffsetStore:    store,
	}, func(streamMsg) { muA.Lock(); seenA++; muA.Unlock() }, nil, nil)
	if err != nil {
		t.Fatalf("NewClient (generation A): %v", err)
	}
	waitFor(t, 10*time.Second, "generation A lines", func() bool {
		muA.Lock()
		defer muA.Unlock()
		return seenA >= 4
	})
	cliA.Close()

	recA, ok := store.Get("repo", "branch", "claude:claude")
	if !ok || recA.LastAckOffset <= 0 {
		t.Fatalf("expected a persisted offset > 0 after generation A, got %+v (ok=%v)", recA, ok)
	}

	// Generation B: a BRAND NEW ptyhost/child at the SAME identity (same
	// seed => same socket path, but A is dead so this launches fresh
	// rather than attaching). Its own ring starts at byte 0 again.
	t.Setenv("FAKE_NDJSON_COUNT", "3")
	t.Setenv("FAKE_NDJSON_START", "0")
	var muB sync.Mutex
	var seqsB []int
	cliB, err := NewClient(context.Background(), ClientOptions{
		Binary:         bin,
		Cwd:            t.TempDir(),
		RepoID:         "repo",
		BranchID:       "branch",
		TabID:          "claude:claude",
		RunDirOverride: runDir,
		OffsetStore:    store,
	}, func(streamMsg) {
		muB.Lock()
		seqsB = append(seqsB, len(seqsB))
		muB.Unlock()
	}, nil, nil)
	if err != nil {
		t.Fatalf("NewClient (generation B): %v", err)
	}
	t.Cleanup(cliB.Close)

	if cliB.Reconnected() {
		t.Fatal("generation B should NOT report Reconnected() — generation A is dead, this must be a fresh spawn")
	}

	// The key correctness property: generation B must actually receive its
	// own fresh output (not hang forever waiting on a bogus ATTACH offset,
	// and not error out) — proving the cross-generation guard let it fall
	// back to -1 instead of stalling on an unusable stale offset.
	waitFor(t, 10*time.Second, "generation B lines", func() bool {
		muB.Lock()
		defer muB.Unlock()
		return len(seqsB) >= 3
	})

	recB, ok := store.Get("repo", "branch", "claude:claude")
	if !ok {
		t.Fatal("expected a persisted offset for generation B")
	}
	if recB.RingGeneration == recA.RingGeneration {
		t.Fatalf("generation B's persisted RingGeneration (%q) must differ from generation A's (%q)", recB.RingGeneration, recA.RingGeneration)
	}
}
