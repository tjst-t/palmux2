package worktreewatch

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestWatcherBasic(t *testing.T) {
	dir := t.TempDir()
	w, err := New(nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer w.Close()

	var mu sync.Mutex
	var batches [][]Event
	sub, err := w.Subscribe(Spec{
		Roots:    []string{dir},
		Debounce: 80 * time.Millisecond,
		OnEvent: func(events []Event) {
			mu.Lock()
			defer mu.Unlock()
			batches = append(batches, events)
		},
	})
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer sub.Unsubscribe()

	// Give fsnotify a moment to wire up the kernel watch.
	time.Sleep(20 * time.Millisecond)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("y"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Wait for debounce to fire.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(batches)
		mu.Unlock()
		if got > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(batches) == 0 {
		t.Fatalf("expected at least one debounced batch, got 0")
	}
	totalEvents := 0
	for _, b := range batches {
		totalEvents += len(b)
	}
	if totalEvents == 0 {
		t.Fatalf("expected non-empty events; got %v", batches)
	}
}

func TestWatcherFilter(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	w, err := New(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	var mu sync.Mutex
	var seen []Event
	sub, err := w.Subscribe(Spec{
		Roots: []string{dir},
		// Drop events under .git except HEAD/refs.
		Filter: func(ev Event) bool {
			rel, _ := filepath.Rel(dir, ev.Path)
			if rel == ".git" || filepath.Dir(rel) == ".git" || filepath.Base(rel) == "HEAD" {
				return filepath.Base(rel) == "HEAD"
			}
			return true
		},
		Debounce: 50 * time.Millisecond,
		OnEvent: func(events []Event) {
			mu.Lock()
			defer mu.Unlock()
			seen = append(seen, events...)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()
	time.Sleep(20 * time.Millisecond)

	// Write a file under .git/objects (filtered out)
	if err := os.WriteFile(filepath.Join(dir, ".git", "ignored"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write HEAD ref (kept)
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("y"), 0o644); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	defer mu.Unlock()
	for _, ev := range seen {
		base := filepath.Base(ev.Path)
		if base == "ignored" {
			t.Errorf("filter let through %q", ev.Path)
		}
	}
}
