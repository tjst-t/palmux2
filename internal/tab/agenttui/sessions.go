package agenttui

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/tjst-t/palmux2/internal/agent"
)

// SessionEventType classifies the filesystem event that triggered a session
// notification.
type SessionEventType string

const (
	// SessionEventAppeared is emitted when a new transcript file is created
	// in the watched directory — i.e. a fresh agent session has started.
	SessionEventAppeared SessionEventType = "appeared"

	// SessionEventModified is emitted when an existing transcript file is
	// written to — i.e. the current session has produced a new turn.
	SessionEventModified SessionEventType = "modified"
)

// SessionEvent carries the session ID that triggered the notification.
type SessionEvent struct {
	SessionID string
	MTime     time.Time
	EventType SessionEventType
}

// SessionIDFromPath reports whether a file path (as delivered by an fsnotify
// event) names a valid session transcript, and if so, its session ID. The
// interpretation of "valid" (file naming convention, ID format) is entirely
// agent-specific — see agent.SessionDiscoverer — so this daemon package
// never parses filenames itself; it just dispatches through the callback an
// adapter supplied.
//
// S0e8afb-2 review fix: this package USED TO own TranscriptDir/
// looksLikeSessionID/LatestSessionID directly (a hardcoded claude-specific
// UUID-.jsonl convention baked into EVERY Manager regardless of which
// Adapter it was configured with — a latent cross-kind data-loss bug for
// S0e8afb-3: two Managers of different kinds sharing one worktree would both
// watch the SAME ~/.claude/projects/<slug> directory and could cross-wire a
// claude session ID into another kind's --resume). That logic moved
// verbatim to agent.ClaudeAdapter (internal/agent/claude.go) as its
// SessionDiscoverer implementation; this package now only ever reaches a
// naming convention through an adapter-supplied callback like this one —
// see Manager.EnsureDaemon's `agent.SessionDiscoverer` type-assertion gate.
type SessionIDFromPath func(path string) (id string, ok bool)

// SessionWatcher watches a transcript directory for new or modified files
// and emits a SessionEvent on each change whose path the adapter-supplied
// idFromPath callback recognises as a session transcript. The directory
// layout and filename→ID parsing are entirely delegated to that callback
// (agent.SessionDiscoverer.SessionIDFromPath) — this package has no
// knowledge of any particular agent's transcript format.
//
// Usage:
//
//	w, err := NewSessionWatcher(transcriptDir, adapter.SessionIDFromPath)
//	// ...
//	for ev := range w.Events() {
//	    // ev.SessionID, ev.EventType
//	}
//	w.Close()
type SessionWatcher struct {
	fsw        *fsnotify.Watcher
	dir        string
	idFromPath SessionIDFromPath
	events     chan SessionEvent
	done       chan struct{}
	closeOnce  sync.Once
}

// NewSessionWatcher creates a watcher for transcriptDir. If the directory
// does not exist yet it is created (with 0o755 permissions) so that
// NewSessionWatcher never fails merely because no session has been recorded
// yet for this worktree. idFromPath classifies each changed file; a nil
// idFromPath makes the watcher emit no events (defensive — callers should
// only construct a SessionWatcher when an adapter implements
// agent.SessionDiscoverer).
func NewSessionWatcher(transcriptDir string, idFromPath SessionIDFromPath) (*SessionWatcher, error) {
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
		fsw:        fsw,
		dir:        transcriptDir,
		idFromPath: idFromPath,
		events:     make(chan SessionEvent, 32),
		done:       make(chan struct{}),
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
	if w.idFromPath == nil {
		return
	}
	sessionID, ok := w.idFromPath(ev.Name)
	if !ok || sessionID == "" {
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

// transcriptExistsFor reports whether td (an agent.SessionDiscoverer's
// TranscriptDir for some worktree) contains a transcript file that sd's own
// SessionIDFromPath resolves to EXACTLY sessionID. This is the
// capability-gated, adapter-agnostic replacement for the old hardcoded
// "<sessionID>.jsonl" existence check (S0e8afb-2 review fix): it never
// assumes a filename extension or naming scheme — it defers entirely to the
// SAME classifier the SessionWatcher itself uses, so "does this ID's
// transcript still exist" and "what ID does this file represent" can never
// disagree.
//
// Used by Manager.EnsureDaemon to gate FIRST-spawn --resume (main's
// palmux2-restart-reattaches-to-prior-conversation feature — see
// DaemonConfig.InitialSessionID's doc comment): a persisted session ID whose
// transcript is gone must never be resumed (claude would error), so the
// first spawn falls back to fresh instead.
func transcriptExistsFor(sd agent.SessionDiscoverer, td, sessionID string) bool {
	if sessionID == "" || sd == nil {
		return false
	}
	entries, err := os.ReadDir(td)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if id, ok := sd.SessionIDFromPath(filepath.Join(td, e.Name())); ok && id == sessionID {
			return true
		}
	}
	return false
}
