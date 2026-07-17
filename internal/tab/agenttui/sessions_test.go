package agenttui

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/agent"
)

// These tests exercise SessionWatcher's generic fsnotify machinery. The
// filename→ID classification is delegated to an idFromPath callback — here
// the built-in Claude adapter's SessionIDFromPath (RFC4122-UUID-named
// .jsonl files), since it's a convenient, realistic classifier. The point of
// these tests is the watcher's directory-watching behavior, not claude's
// naming convention (which has its own tests in internal/agent —
// TestTranscriptDir / TestLatestSessionID / TestSessionIDFromPath in
// internal/agent/claude_test.go, S0e8afb-2 review fix: this package no
// longer owns that logic itself, see sessions.go's doc comments).

// validSessionID is a valid RFC4122 UUID used in watcher tests.
const validSessionID = "12345678-abcd-ef01-2345-6789abcdef01"

func testIDFromPath() SessionIDFromPath {
	return agent.NewClaudeAdapter("claude", nil).SessionIDFromPath
}

// TestSessionWatcher_NewFile writes a new .jsonl to the watched dir and
// asserts an "appeared" event with the correct session_id is delivered.
func TestSessionWatcher_NewFile(t *testing.T) {
	dir := t.TempDir()
	w, err := NewSessionWatcher(dir, testIDFromPath())
	if err != nil {
		t.Fatalf("NewSessionWatcher: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	// Write a new .jsonl after the watcher is running.
	path := filepath.Join(dir, validSessionID+".jsonl")
	writeFile(t, path, `{"type":"user"}`)

	ev := waitForEvent(t, w, 5*time.Second)
	if ev.SessionID != validSessionID {
		t.Errorf("SessionID = %q, want %q", ev.SessionID, validSessionID)
	}
	if ev.EventType != SessionEventAppeared {
		t.Errorf("EventType = %q, want %q", ev.EventType, SessionEventAppeared)
	}
}

// TestSessionWatcher_Modified appends to an existing .jsonl and asserts a
// "modified" event is emitted with the correct session_id.
func TestSessionWatcher_Modified(t *testing.T) {
	dir := t.TempDir()

	// Pre-create the file BEFORE starting the watcher so the first write
	// is unambiguously a modification (not a creation).
	path := filepath.Join(dir, validSessionID+".jsonl")
	writeFile(t, path, `{"type":"user"}`)

	w, err := NewSessionWatcher(dir, testIDFromPath())
	if err != nil {
		t.Fatalf("NewSessionWatcher: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	// Drain any residual events from the initial state.
	time.Sleep(50 * time.Millisecond)
	drainEvents(w)

	// Append a new line — this is a Write event.
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open for append: %v", err)
	}
	if _, err := f.WriteString(`{"type":"assistant"}` + "\n"); err != nil {
		f.Close()
		t.Fatalf("append: %v", err)
	}
	f.Close()

	ev := waitForEvent(t, w, 5*time.Second)
	if ev.SessionID != validSessionID {
		t.Errorf("SessionID = %q, want %q", ev.SessionID, validSessionID)
	}
	if ev.EventType != SessionEventModified {
		t.Errorf("EventType = %q, want %q", ev.EventType, SessionEventModified)
	}
}

// TestSessionWatcher_NonExistentDir verifies that NewSessionWatcher creates the
// directory when it does not exist, rather than returning an error.
func TestSessionWatcher_NonExistentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "auto-created")
	// dir does not exist yet.
	w, err := NewSessionWatcher(dir, testIDFromPath())
	if err != nil {
		t.Fatalf("NewSessionWatcher on non-existent dir: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	if _, serr := os.Stat(dir); serr != nil {
		t.Errorf("directory was not created: %v", serr)
	}
}

// TestSessionWatcher_IgnoresNonUUID verifies that random .txt files and
// non-UUID .jsonl filenames don't emit events (the idFromPath classifier
// rejects them).
func TestSessionWatcher_IgnoresNonUUID(t *testing.T) {
	dir := t.TempDir()
	w, err := NewSessionWatcher(dir, testIDFromPath())
	if err != nil {
		t.Fatalf("NewSessionWatcher: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	writeFile(t, filepath.Join(dir, "ignore-me.txt"), "data")
	writeFile(t, filepath.Join(dir, "not-uuid.jsonl"), "{}")

	// Give a short window — no event should arrive.
	select {
	case ev := <-w.Events():
		t.Errorf("unexpected event: %+v", ev)
	case <-time.After(200 * time.Millisecond):
		// correct — no event
	}
}

// TestSessionWatcher_NilIDFromPath verifies that a nil classifier makes the
// watcher emit no events at all (defensive default — Manager only ever
// constructs a watcher when an adapter implements agent.SessionDiscoverer,
// but the watcher itself must not panic if misused — S0e8afb-2 review fix
// regression guard: this is EXACTLY the "no SessionDiscoverer" case a future
// non-claude Adapter would hit, so a nil callback must be inert, not a
// crash or a false-positive event).
func TestSessionWatcher_NilIDFromPath(t *testing.T) {
	dir := t.TempDir()
	w, err := NewSessionWatcher(dir, nil)
	if err != nil {
		t.Fatalf("NewSessionWatcher: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	writeFile(t, filepath.Join(dir, validSessionID+".jsonl"), `{"type":"user"}`)

	select {
	case ev := <-w.Events():
		t.Errorf("unexpected event with nil idFromPath: %+v", ev)
	case <-time.After(200 * time.Millisecond):
		// correct — no event
	}
}

// ---- helpers -----------------------------------------------------------------

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writeFile(%q): %v", path, err)
	}
}

// waitForEvent blocks until w delivers a SessionEvent or the deadline expires.
func waitForEvent(t *testing.T, w *SessionWatcher, timeout time.Duration) SessionEvent {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case <-deadline:
			t.Fatalf("no SessionEvent received within %v", timeout)
		case ev, ok := <-w.Events():
			if !ok {
				t.Fatal("Events channel closed unexpectedly")
			}
			if ev.SessionID != "" {
				return ev
			}
		}
	}
}

// drainEvents non-blockingly discards all queued events.
func drainEvents(w *SessionWatcher) {
	for {
		select {
		case <-w.Events():
		default:
			return
		}
	}
}
