package claudetui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ---- TranscriptDir -----------------------------------------------------------

// TestTranscriptDir verifies the slug algorithm: '/' and '.' become '-'.
func TestTranscriptDir(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}

	tests := []struct {
		name     string
		worktree string
		wantSlug string // path segment after ~/.claude/projects/
	}{
		{
			name:     "simple unix path",
			worktree: "/home/ubuntu/ghq/github.com/foo/bar",
			wantSlug: "-home-ubuntu-ghq-github-com-foo-bar",
		},
		{
			name:     "dots become dashes",
			worktree: "/home/ubuntu/go/src/github.com/example.org/proj",
			wantSlug: "-home-ubuntu-go-src-github-com-example-org-proj",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := TranscriptDir(tc.worktree)
			if err != nil {
				t.Fatalf("TranscriptDir(%q): %v", tc.worktree, err)
			}
			want := filepath.Join(home, ".claude", "projects", tc.wantSlug)
			if got != want {
				t.Errorf("TranscriptDir(%q) =\n  %q\nwant\n  %q", tc.worktree, got, want)
			}
		})
	}
}

// TestTranscriptDirEmpty verifies that an empty worktree returns an error.
func TestTranscriptDirEmpty(t *testing.T) {
	_, err := TranscriptDir("")
	if err == nil {
		t.Fatal("expected error for empty worktree, got nil")
	}
	if !strings.Contains(err.Error(), "claudetui") {
		t.Errorf("error %q should mention 'claudetui'", err.Error())
	}
}

// ---- LatestSessionID ---------------------------------------------------------

// TestLatestSessionID creates two .jsonl files with different mtimes and
// verifies that LatestSessionID returns the one with the most recent mtime.
func TestLatestSessionID(t *testing.T) {
	dir := t.TempDir()

	older := "11111111-2222-3333-4444-555555555555"
	newer := "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"

	// Write "older" file first.
	writeFile(t, filepath.Join(dir, older+".jsonl"), `{"type":"user"}`)
	time.Sleep(20 * time.Millisecond) // ensure different mtime
	writeFile(t, filepath.Join(dir, newer+".jsonl"), `{"type":"assistant"}`)

	got, mtime, err := LatestSessionID(dir)
	if err != nil {
		t.Fatalf("LatestSessionID: %v", err)
	}
	if got != newer {
		t.Errorf("LatestSessionID = %q, want %q", got, newer)
	}
	if mtime.IsZero() {
		t.Error("mtime should not be zero")
	}
}

// TestLatestSessionIDEmpty verifies behaviour on an empty dir.
func TestLatestSessionIDEmpty(t *testing.T) {
	dir := t.TempDir()
	got, mtime, err := LatestSessionID(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty string", got)
	}
	if !mtime.IsZero() {
		t.Errorf("mtime should be zero, got %v", mtime)
	}
}

// TestLatestSessionIDNonexistentDir verifies that a missing directory returns
// ("", zero, nil) rather than an error.
func TestLatestSessionIDNonexistentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "does-not-exist")
	got, _, err := LatestSessionID(dir)
	if err != nil {
		t.Fatalf("unexpected error for missing dir: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

// TestLatestSessionIDIgnoresNonUUID verifies that non-UUID files are skipped.
func TestLatestSessionIDIgnoresNonUUID(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "not-a-uuid.jsonl"), "{}")
	writeFile(t, filepath.Join(dir, "some-other-file.txt"), "hello")
	got, _, err := LatestSessionID(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty (non-UUID file should be ignored)", got)
	}
}

// ---- SessionWatcher ----------------------------------------------------------

// validSessionID is a valid RFC4122 UUID used in watcher tests.
const validSessionID = "12345678-abcd-ef01-2345-6789abcdef01"

// TestSessionWatcher_NewFile writes a new .jsonl to the watched dir and
// asserts an "appeared" event with the correct session_id is delivered.
func TestSessionWatcher_NewFile(t *testing.T) {
	dir := t.TempDir()
	w, err := NewSessionWatcher(dir)
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

	w, err := NewSessionWatcher(dir)
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
	w, err := NewSessionWatcher(dir)
	if err != nil {
		t.Fatalf("NewSessionWatcher on non-existent dir: %v", err)
	}
	t.Cleanup(func() { w.Close() })

	if _, serr := os.Stat(dir); serr != nil {
		t.Errorf("directory was not created: %v", serr)
	}
}

// TestSessionWatcher_IgnoresNonUUID verifies that random .txt files and
// non-UUID .jsonl filenames don't emit events.
func TestSessionWatcher_IgnoresNonUUID(t *testing.T) {
	dir := t.TempDir()
	w, err := NewSessionWatcher(dir)
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
