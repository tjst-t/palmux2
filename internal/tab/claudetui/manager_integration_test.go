package claudetui

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestSessionIDDetection spawns fake-claude with --write-session so it writes
// a .jsonl into the watched transcript directory, then verifies that:
//  1. The Manager's SessionWatcher picks up the new session_id.
//  2. The Daemon.SessionID() is updated accordingly.
//  3. The SessionStore persists the value.
//
// The test passes a worktree path to EnsureDaemon; the Manager derives
// transcriptDir via TranscriptDir(worktree).  We compute that same path
// here and pass it to fake-claude via --write-session so both sides agree.
func TestSessionIDDetection(t *testing.T) {
	bin := fakeBin(t)
	ctx := context.Background()

	configDir := t.TempDir()
	// Use t.TempDir() as the worktree; TranscriptDir will compute the real dir.
	worktree := t.TempDir()

	transcriptDir, err := TranscriptDir(worktree)
	if err != nil {
		t.Fatalf("TranscriptDir: %v", err)
	}
	t.Logf("transcript dir: %s", transcriptDir)

	store, err := NewSessionStore(configDir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	const (
		repoID    = "detection-repo"
		branchID  = "detection-branch"
		tabID     = "claude:claude"
		sessionID = "dead1234-dead-dead-dead-dead12345678"
	)

	mgr := NewManager(ManagerConfig{
		ClaudeBin: bin,
		// Tell fake-claude to write a .jsonl with our session ID to the
		// transcript dir that the SessionWatcher is watching.
		ClaudeArgs: []string{
			"--write-session", transcriptDir, sessionID,
		},
		RingSize:      1 << 16,
		ResumeOnDeath: false,
		Store:         store,
	})
	t.Cleanup(func() { mgr.ShutdownAll(ctx) })

	// Pass the worktree path; the Manager derives transcriptDir internally.
	d, err := mgr.EnsureDaemon(ctx, repoID, branchID, tabID, worktree)
	if err != nil {
		t.Fatalf("EnsureDaemon: %v", err)
	}

	// Start the daemon so fake-claude runs and writes the .jsonl.
	if err := d.EnsureStarted(ctx); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}

	// Wait for the SessionWatcher to detect the session ID.
	deadline := time.After(10 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatalf("Daemon.SessionID() still empty after 10s; transcript dir=%s", transcriptDir)
		default:
		}
		if got := d.SessionID(); got == sessionID {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Verify the store was persisted.
	got, ok := store.LoadActive(repoID, branchID, tabID)
	if !ok {
		t.Fatal("SessionStore: LoadActive returned ok=false after detection")
	}
	if got != sessionID {
		t.Errorf("SessionStore session = %q, want %q", got, sessionID)
	}
}

// TestSessionPersistenceAcrossDaemonRestart verifies the full persistence loop:
//  1. A session ID is written to the SessionStore.
//  2. A new Manager is created from the same store.
//  3. EnsureDaemon on the new Manager pre-seeds the Daemon with the stored ID.
//  4. The Daemon re-spawns via --exit-immediately to trigger StateDead.
//  5. respawnLoop re-spawns with --resume <id>, confirmed by ring output.
func TestSessionPersistenceAcrossDaemonRestart(t *testing.T) {
	bin := fakeBin(t)
	ctx := context.Background()
	configDir := t.TempDir()

	const (
		repoID    = "persist-repo"
		branchID  = "persist-branch"
		tabID     = "claude:claude"
		sessionID = "11112222-3333-4444-5555-666677778888"
	)

	// Phase 1: persist a session ID into the store (simulates a prior run).
	store1, err := NewSessionStore(configDir)
	if err != nil {
		t.Fatalf("NewSessionStore (1): %v", err)
	}
	if err := store1.SetActive(repoID, branchID, tabID, sessionID); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	// Phase 2: new Manager + new Store (simulating a server restart).
	store2, err := NewSessionStore(configDir)
	if err != nil {
		t.Fatalf("NewSessionStore (2): %v", err)
	}

	mgr := NewManager(ManagerConfig{
		ClaudeBin: bin,
		// --exit-immediately causes the first subprocess to die at once,
		// triggering the respawnLoop.
		ClaudeArgs:    []string{"--exit-immediately"},
		RingSize:      1 << 16,
		ResumeOnDeath: true, // required for respawnLoop to kick in
		Store:         store2,
	})
	t.Cleanup(func() { mgr.ShutdownAll(ctx) })

	// EnsureDaemon should load the persisted session ID and pre-seed the Daemon.
	d, err := mgr.EnsureDaemon(ctx, repoID, branchID, tabID, "")
	if err != nil {
		t.Fatalf("EnsureDaemon: %v", err)
	}

	// The daemon should already know the session ID (pre-seeded from store).
	if got := d.SessionID(); got != sessionID {
		t.Errorf("pre-seeded SessionID = %q, want %q", got, sessionID)
	}

	// Start: --exit-immediately makes the process die right after printing
	// "fake_claude started".
	if err := d.EnsureStarted(ctx); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}

	// Wait for StateDead.
	waitForState(t, d, StateDead, 10*time.Second)

	// respawnLoop should now re-spawn with --resume <sessionID>.
	waitForState(t, d, StateRunning, 15*time.Second)

	// Confirm ring contains "resume: <sessionID>".
	deadline := time.After(5 * time.Second)
	want := []byte("resume: " + sessionID)
	for {
		select {
		case <-deadline:
			t.Fatalf("ring does not contain %q; ring=%q", want, d.ring.Bytes())
		default:
		}
		if bytes.Contains(d.ring.Bytes(), want) {
			return
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// TestManagerEnsureDaemonWithWorktree verifies that passing a worktree to
// EnsureDaemon does not break the normal Daemon-creation path (e.g. the
// transcript dir doesn't exist yet and gets created).
func TestManagerEnsureDaemonWithWorktree(t *testing.T) {
	ctx := context.Background()
	// Use a dir that doesn't exist yet as the "worktree" — TranscriptDir will
	// derive ~/.claude/projects/... which we don't actually need for this test.
	// Instead, pass an explicit transcript dir via a worktree that resolves to
	// a temp path we own.  The simplest approach: use a temp dir as worktree so
	// TranscriptDir computes a slug under ~/.claude/projects/; the watcher just
	// creates the target dir.
	worktree := t.TempDir()

	mgr := NewManager(ManagerConfig{
		ClaudeBin:     "claude", // not started, lazy
		ResumeOnDeath: false,
	})
	t.Cleanup(func() { mgr.ShutdownAll(ctx) })

	d, err := mgr.EnsureDaemon(ctx, "r", "b", "claude:claude", worktree)
	if err != nil {
		t.Fatalf("EnsureDaemon with worktree: %v", err)
	}
	if d == nil {
		t.Fatal("expected non-nil Daemon")
	}
	// Idempotent: second call returns same daemon.
	d2, err := mgr.EnsureDaemon(ctx, "r", "b", "claude:claude", worktree)
	if err != nil {
		t.Fatalf("second EnsureDaemon: %v", err)
	}
	if d != d2 {
		t.Error("EnsureDaemon should be idempotent — returned different Daemon on second call")
	}

	// Verify the transcript dir was created by the SessionWatcher.
	td, err := TranscriptDir(worktree)
	if err != nil {
		t.Fatalf("TranscriptDir: %v", err)
	}
	if _, serr := os.Stat(td); serr != nil {
		t.Errorf("transcript dir %q should have been created by SessionWatcher: %v", td, serr)
	}

	mgr.CloseDaemon(ctx, "r", "b", "claude:claude")
}

// TestManagerCloseDaemonWithWatcher verifies that CloseDaemon stops the
// SessionWatcher (no goroutine leak).
func TestManagerCloseDaemonWithWatcher(t *testing.T) {
	ctx := context.Background()
	worktree := t.TempDir()

	mgr := NewManager(ManagerConfig{ClaudeBin: "claude", ResumeOnDeath: false})

	_, err := mgr.EnsureDaemon(ctx, "r", "b", "claude:claude", worktree)
	if err != nil {
		t.Fatalf("EnsureDaemon: %v", err)
	}
	// CloseDaemon should not block — watcher.Close() must return promptly.
	done := make(chan struct{})
	go func() {
		mgr.CloseDaemon(ctx, "r", "b", "claude:claude")
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("CloseDaemon with active SessionWatcher timed out")
	}
}

// TestSessionIDDetectionWithExplicitTranscriptDir is a variant of
// TestSessionIDDetection that uses a dedicated temp dir as the transcript
// directory (avoids touching ~/.claude/projects/) by passing the transcript
// dir directly as the "worktree" argument.  SessionWatcher.loop reads from
// the directory itself, so passing the transcript dir as worktree is valid
// for this test.
func TestSessionIDDetectionWithExplicitTranscriptDir(t *testing.T) {
	transcriptDir := t.TempDir()
	const sessionID = "abcdef01-2345-6789-abcd-ef0123456789"

	// Start the watcher directly.
	w, err := NewSessionWatcher(transcriptDir)
	if err != nil {
		t.Fatalf("NewSessionWatcher: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	// Write the .jsonl.
	writeFile(t, filepath.Join(transcriptDir, sessionID+".jsonl"), `{"type":"user"}`)

	// Wait for the event.
	ev := waitForEvent(t, w, 5*time.Second)
	if ev.SessionID != sessionID {
		t.Errorf("SessionID = %q, want %q", ev.SessionID, sessionID)
	}
}
