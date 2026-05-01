// Package worktreewatch is a generic, debounced filesystem watcher built on
// top of fsnotify. It is designed to be used by any palmux2 component that
// needs to react to changes inside a directory tree — currently:
//
//   - internal/tab/git: emit `git.statusChanged` events when files inside
//     the worktree (or the `.git/HEAD` / `.git/refs/` ref pointers) move so
//     the Git tab can refresh its status view automatically (S012-1-6).
//   - internal/sprintdash (S016, planned): emit events when
//     `.claude/autopilot-*.lock` files appear or disappear so the Sprint
//     Dashboard tab can update Active autopilot state.
//
// The package intentionally exposes a *minimal* API:
//
//	w, _ := worktreewatch.New(logger)
//	defer w.Close()
//	sub, _ := w.Subscribe(worktreewatch.Spec{
//	    Roots:    []string{"/path/to/worktree"},
//	    Filter:   filter,            // optional, called per event
//	    Debounce: 1000 * time.Millisecond,
//	    OnEvent:  func(events []Event) { ... }, // dispatched after debounce
//	})
//	defer sub.Unsubscribe()
//
// Subscribers are isolated: each Subscribe call gets its own debounce
// timer and event coalescing buffer. The watcher itself is shared so we
// only allocate one fsnotify Watcher per process even with several
// consumers.
//
// Coalescing semantics: while events keep arriving inside a Spec.Debounce
// window, the timer is reset. When it finally fires, the *full list* of
// raw events seen during that window is delivered as a single OnEvent
// call. Consumers who only care that "something changed" can ignore the
// slice; consumers who need to know which paths moved get the detail.
package worktreewatch

import (
	"errors"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Op mirrors fsnotify.Op so callers don't have to import fsnotify directly.
type Op uint32

const (
	OpCreate Op = 1 << iota
	OpWrite
	OpRemove
	OpRename
	OpChmod
)

// String returns a human-readable, comma-separated list of ops for logging.
func (o Op) String() string {
	parts := make([]string, 0, 5)
	if o&OpCreate != 0 {
		parts = append(parts, "create")
	}
	if o&OpWrite != 0 {
		parts = append(parts, "write")
	}
	if o&OpRemove != 0 {
		parts = append(parts, "remove")
	}
	if o&OpRename != 0 {
		parts = append(parts, "rename")
	}
	if o&OpChmod != 0 {
		parts = append(parts, "chmod")
	}
	if len(parts) == 0 {
		return "noop"
	}
	return strings.Join(parts, ",")
}

// Event is one raw filesystem event seen by a subscriber. Multiple events
// may be coalesced into a single OnEvent call if they arrive within the
// configured debounce window.
type Event struct {
	Path string
	Op   Op
}

func opFromFsnotify(op fsnotify.Op) Op {
	var out Op
	if op&fsnotify.Create != 0 {
		out |= OpCreate
	}
	if op&fsnotify.Write != 0 {
		out |= OpWrite
	}
	if op&fsnotify.Remove != 0 {
		out |= OpRemove
	}
	if op&fsnotify.Rename != 0 {
		out |= OpRename
	}
	if op&fsnotify.Chmod != 0 {
		out |= OpChmod
	}
	return out
}

// Filter accepts an event and returns true if the subscriber wants it.
// Returning false drops the event before it reaches the debounce buffer
// (so unrelated paths don't reset the timer).
type Filter func(ev Event) bool

// Spec describes a single subscription.
type Spec struct {
	// Roots are the directory trees to watch. Each is registered
	// recursively (subdirectories created later are auto-added by the
	// internal sweeper).
	Roots []string
	// Filter is optional. If nil, every event under Roots is delivered.
	Filter Filter
	// Debounce is how long the watcher waits for the burst to settle
	// before calling OnEvent. Defaults to 250ms when zero.
	Debounce time.Duration
	// OnEvent is called from a dedicated goroutine. The slice is owned
	// by the subscriber after the call (the watcher does not retain it).
	OnEvent func(events []Event)
	// MaxBatch caps how many events are buffered before forced delivery.
	// Defaults to 1024.
	MaxBatch int
}

// Subscription is the handle returned by Subscribe.
type Subscription struct {
	id  uint64
	w   *Watcher
	sub *subscriber
}

// Unsubscribe stops delivering events to this subscription. Safe to call
// multiple times.
func (s *Subscription) Unsubscribe() {
	if s == nil || s.w == nil {
		return
	}
	s.w.unsubscribe(s.id)
}

// Watcher is the shared fsnotify watcher with debounced subscriptions.
type Watcher struct {
	mu          sync.Mutex
	fs          *fsnotify.Watcher
	logger      *slog.Logger
	subscribers map[uint64]*subscriber
	nextSubID   uint64
	// rootRefs tracks how many subscribers want a given path watched so
	// Unsubscribe knows when it can safely Remove the underlying fs watch.
	rootRefs map[string]int
	closed   bool
	stopCh   chan struct{}
}

type subscriber struct {
	id       uint64
	roots    []string
	filter   Filter
	debounce time.Duration
	maxBatch int
	onEvent  func(events []Event)

	mu      sync.Mutex
	pending []Event
	timer   *time.Timer
}

// New creates a Watcher. logger may be nil (defaults to slog.Default()).
func New(logger *slog.Logger) (*Watcher, error) {
	if logger == nil {
		logger = slog.Default()
	}
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	w := &Watcher{
		fs:          fsw,
		logger:      logger,
		subscribers: map[uint64]*subscriber{},
		rootRefs:    map[string]int{},
		stopCh:      make(chan struct{}),
	}
	go w.dispatch()
	return w, nil
}

// Close releases all OS watches and stops the dispatcher.
func (w *Watcher) Close() error {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil
	}
	w.closed = true
	close(w.stopCh)
	w.mu.Unlock()
	return w.fs.Close()
}

// Subscribe registers a new subscriber. The returned Subscription must be
// Unsubscribed when done — otherwise its goroutine and watches leak.
func (w *Watcher) Subscribe(spec Spec) (*Subscription, error) {
	if spec.OnEvent == nil {
		return nil, errors.New("worktreewatch: Spec.OnEvent must be non-nil")
	}
	if len(spec.Roots) == 0 {
		return nil, errors.New("worktreewatch: Spec.Roots must contain at least one path")
	}
	debounce := spec.Debounce
	if debounce <= 0 {
		debounce = 250 * time.Millisecond
	}
	maxBatch := spec.MaxBatch
	if maxBatch <= 0 {
		maxBatch = 1024
	}
	sub := &subscriber{
		roots:    append([]string(nil), spec.Roots...),
		filter:   spec.Filter,
		debounce: debounce,
		maxBatch: maxBatch,
		onEvent:  spec.OnEvent,
	}
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return nil, errors.New("worktreewatch: closed")
	}
	w.nextSubID++
	id := w.nextSubID
	sub.id = id
	w.subscribers[id] = sub
	for _, root := range sub.roots {
		w.addRecursiveLocked(root)
	}
	w.mu.Unlock()
	return &Subscription{id: id, w: w, sub: sub}, nil
}

func (w *Watcher) unsubscribe(id uint64) {
	w.mu.Lock()
	sub, ok := w.subscribers[id]
	if !ok {
		w.mu.Unlock()
		return
	}
	delete(w.subscribers, id)
	roots := sub.roots
	w.mu.Unlock()

	sub.mu.Lock()
	if sub.timer != nil {
		sub.timer.Stop()
		sub.timer = nil
	}
	sub.pending = nil
	sub.mu.Unlock()

	w.mu.Lock()
	for _, root := range roots {
		w.removeRecursiveLocked(root)
	}
	w.mu.Unlock()
}

// addRecursiveLocked walks `root` once and adds every directory to the
// underlying fsnotify watcher. New subdirectories created later are picked
// up by handleCreate when fsnotify reports them.
//
// Caller must hold w.mu.
func (w *Watcher) addRecursiveLocked(root string) {
	root = filepath.Clean(root)
	w.addOneLocked(root)
	// Walk children. We tolerate per-path errors so a missing subtree
	// doesn't abort the whole watch.
	_ = walk(root, func(p string, info pathInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !info.IsDir() {
			return nil
		}
		w.addOneLocked(p)
		return nil
	})
}

func (w *Watcher) removeRecursiveLocked(root string) {
	root = filepath.Clean(root)
	for p := range w.rootRefs {
		if p == root || strings.HasPrefix(p, root+string(filepath.Separator)) {
			w.removeOneLocked(p)
		}
	}
}

func (w *Watcher) addOneLocked(p string) {
	if p == "" {
		return
	}
	w.rootRefs[p]++
	if w.rootRefs[p] == 1 {
		if err := w.fs.Add(p); err != nil {
			// If the directory disappeared between Walk and Add we
			// just drop the ref — handleEvent will retry on Create.
			w.logger.Debug("worktreewatch: fsnotify Add failed", "path", p, "err", err)
			delete(w.rootRefs, p)
		}
	}
}

func (w *Watcher) removeOneLocked(p string) {
	if w.rootRefs[p] <= 0 {
		return
	}
	w.rootRefs[p]--
	if w.rootRefs[p] <= 0 {
		_ = w.fs.Remove(p)
		delete(w.rootRefs, p)
	}
}

func (w *Watcher) dispatch() {
	for {
		select {
		case <-w.stopCh:
			return
		case ev, ok := <-w.fs.Events:
			if !ok {
				return
			}
			w.handleEvent(ev)
		case err, ok := <-w.fs.Errors:
			if !ok {
				return
			}
			if err != nil {
				w.logger.Debug("worktreewatch: fsnotify error", "err", err)
			}
		}
	}
}

func (w *Watcher) handleEvent(ev fsnotify.Event) {
	out := Event{Path: filepath.Clean(ev.Name), Op: opFromFsnotify(ev.Op)}
	// Auto-add directories created inside an existing watched tree so
	// the recursion is preserved without re-Subscribe.
	if out.Op&OpCreate != 0 {
		if isDir(out.Path) {
			w.mu.Lock()
			w.addOneLocked(out.Path)
			w.mu.Unlock()
		}
	}

	w.mu.Lock()
	subs := make([]*subscriber, 0, len(w.subscribers))
	for _, s := range w.subscribers {
		if eventBelongs(out.Path, s.roots) {
			subs = append(subs, s)
		}
	}
	w.mu.Unlock()

	for _, s := range subs {
		if s.filter != nil && !s.filter(out) {
			continue
		}
		s.queue(out)
	}
}

func (s *subscriber) queue(ev Event) {
	s.mu.Lock()
	s.pending = append(s.pending, ev)
	if len(s.pending) >= s.maxBatch {
		batch := s.pending
		s.pending = nil
		if s.timer != nil {
			s.timer.Stop()
			s.timer = nil
		}
		s.mu.Unlock()
		go s.onEvent(batch)
		return
	}
	if s.timer == nil {
		s.timer = time.AfterFunc(s.debounce, s.flush)
	} else {
		s.timer.Reset(s.debounce)
	}
	s.mu.Unlock()
}

func (s *subscriber) flush() {
	s.mu.Lock()
	batch := s.pending
	s.pending = nil
	s.timer = nil
	s.mu.Unlock()
	if len(batch) == 0 {
		return
	}
	s.onEvent(batch)
}

func eventBelongs(path string, roots []string) bool {
	for _, root := range roots {
		root = filepath.Clean(root)
		if path == root || strings.HasPrefix(path, root+string(filepath.Separator)) {
			return true
		}
	}
	return false
}
