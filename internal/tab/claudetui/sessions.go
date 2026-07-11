package claudetui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// TranscriptDir maps a worktree absolute path to the directory where the
// Claude CLI writes per-session .jsonl transcripts.
//
// The algorithm mirrors claudeagent.transcriptDir (read for canonical source):
// replace every '/' and '.' in the absolute path with '-', then join under
// ~/.claude/projects/<slug>. Example:
//
//	/home/ubuntu/ghq/github.com/foo/bar → ~/.claude/projects/-home-ubuntu-ghq-github-com-foo-bar
//
// Pure function — no I/O on worktree itself.
func TranscriptDir(worktree string) (string, error) {
	if worktree == "" {
		return "", errors.New("claudetui: empty worktree")
	}
	abs, err := filepath.Abs(worktree)
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(abs)
	return filepath.Join(home, ".claude", "projects", slug), nil
}

// transcriptExists reports whether the .jsonl transcript for sessionID still
// exists under the worktree's transcript dir. Used to gate first-spawn --resume
// so we never `claude --resume <gone-id>`. Any resolution error → false (treat as
// absent; the caller falls back to a fresh session).
func transcriptExists(worktree, sessionID string) bool {
	if sessionID == "" {
		return false
	}
	dir, err := TranscriptDir(worktree)
	if err != nil {
		return false
	}
	info, err := os.Stat(filepath.Join(dir, sessionID+".jsonl"))
	return err == nil && !info.IsDir()
}

// looksLikeSessionID guards against random non-uuid files in the projects dir.
// Claude Code session IDs are RFC4122 UUIDs.
func looksLikeSessionID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if !((r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')) {
				return false
			}
		}
	}
	return true
}

// LatestSessionID scans transcriptDir for *.jsonl files and returns the
// session_id (filename without .jsonl extension) of the one with the highest
// modification time.  Returns ("", zero, nil) when the directory is empty or
// contains no valid session files.
func LatestSessionID(transcriptDir string) (sessionID string, mtime time.Time, err error) {
	entries, err := os.ReadDir(transcriptDir)
	if err != nil {
		if os.IsNotExist(err) {
			return "", time.Time{}, nil
		}
		return "", time.Time{}, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		id := strings.TrimSuffix(name, ".jsonl")
		if !looksLikeSessionID(id) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		if info.ModTime().After(mtime) {
			mtime = info.ModTime()
			sessionID = id
		}
	}
	return sessionID, mtime, nil
}

// SessionEventType classifies the filesystem event that triggered a session
// notification.
type SessionEventType string

const (
	// SessionEventAppeared is emitted when a new .jsonl file is created in
	// the transcript directory — i.e. a fresh Claude session has started.
	SessionEventAppeared SessionEventType = "appeared"

	// SessionEventModified is emitted when an existing .jsonl file is
	// written to — i.e. the current session has produced a new turn.
	SessionEventModified SessionEventType = "modified"
)

// SessionEvent carries the session ID that triggered the notification.
type SessionEvent struct {
	SessionID string
	MTime     time.Time
	EventType SessionEventType
}

// SessionWatcher watches a transcript directory for new or modified .jsonl
// files and emits a SessionEvent on each change.
//
// Usage:
//
//	w, err := NewSessionWatcher(transcriptDir)
//	// ...
//	for ev := range w.Events() {
//	    // ev.SessionID, ev.EventType
//	}
//	w.Close()
type SessionWatcher struct {
	fsw       *fsnotify.Watcher
	dir       string
	events    chan SessionEvent
	done      chan struct{}
	closeOnce sync.Once
}

// NewSessionWatcher creates a watcher for transcriptDir. If the directory
// does not exist yet it is created (with 0o755 permissions) so that
// NewSessionWatcher never fails merely because no claude session has been
// recorded yet for this worktree.
func NewSessionWatcher(transcriptDir string) (*SessionWatcher, error) {
	if err := os.MkdirAll(transcriptDir, 0o755); err != nil {
		return nil, err
	}

	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	if err := fsw.Add(transcriptDir); err != nil {
		fsw.Close()
		return nil, err
	}

	sw := &SessionWatcher{
		fsw:    fsw,
		dir:    transcriptDir,
		events: make(chan SessionEvent, 32),
		done:   make(chan struct{}),
	}
	go sw.loop()
	return sw, nil
}

// Events returns the channel on which SessionEvents are delivered.  The
// channel is closed when Close() is called.
func (w *SessionWatcher) Events() <-chan SessionEvent {
	return w.events
}

// Close stops the watcher and closes the Events channel.  Idempotent and
// safe to call concurrently — sprint review D1: the previous select+default
// pattern panicked when two goroutines raced into Close.
func (w *SessionWatcher) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
	})
	return w.fsw.Close()
}

func (w *SessionWatcher) loop() {
	defer close(w.events)
	for {
		select {
		case <-w.done:
			return
		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			w.handleFSEvent(ev)
		case _, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			// Ignore individual watcher errors — the next event will
			// succeed or Close will terminate the loop.
		}
	}
}

func (w *SessionWatcher) handleFSEvent(ev fsnotify.Event) {
	name := filepath.Base(ev.Name)
	if !strings.HasSuffix(name, ".jsonl") {
		return
	}
	sessionID := strings.TrimSuffix(name, ".jsonl")
	if !looksLikeSessionID(sessionID) {
		return
	}

	var evType SessionEventType
	if ev.Op&(fsnotify.Create) != 0 {
		evType = SessionEventAppeared
	} else if ev.Op&(fsnotify.Write) != 0 {
		evType = SessionEventModified
	} else {
		// Rename / Remove / Chmod — not relevant for our purposes.
		return
	}

	// Stat the file to get a real mtime.
	var mtime time.Time
	if info, err := os.Stat(ev.Name); err == nil {
		mtime = info.ModTime()
	}

	se := SessionEvent{
		SessionID: sessionID,
		MTime:     mtime,
		EventType: evType,
	}
	select {
	case w.events <- se:
	case <-w.done:
	}
}
