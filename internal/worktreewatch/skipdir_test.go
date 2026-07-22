package worktreewatch

import (
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// buildTree creates root/<top>/<i>/<j> so the number of directories is
// predictable: 1 (root) + len(tops) + len(tops)*mid + len(tops)*mid*leaf.
func buildTree(t *testing.T, root string, tops []string, mid, leaf int) int {
	t.Helper()
	n := 1
	for _, top := range tops {
		if err := os.MkdirAll(filepath.Join(root, top), 0o755); err != nil {
			t.Fatal(err)
		}
		n++
		for i := 0; i < mid; i++ {
			mp := filepath.Join(root, top, strconv.Itoa(i))
			if err := os.MkdirAll(mp, 0o755); err != nil {
				t.Fatal(err)
			}
			n++
			for j := 0; j < leaf; j++ {
				if err := os.MkdirAll(filepath.Join(mp, strconv.Itoa(j)), 0o755); err != nil {
					t.Fatal(err)
				}
				n++
			}
		}
	}
	return n
}

// TestSubscribe_SkipDirPrunesRegistration is the mechanism test for S3f3cb2:
// registering a watch costs one inotify syscall per directory, so a subscriber
// that only cares about a narrow subtree must be able to prune the rest.
//
// Measured on a real repo before this existed: the Sprint tab registered
// 27,025 directories when the 26 under docs/ were all it could act on.
func TestSubscribe_SkipDirPrunesRegistration(t *testing.T) {
	root := t.TempDir()
	total := buildTree(t, root, []string{"docs", "node_modules", "src", "vendor"}, 5, 5)

	w, err := New(slog.Default())
	if err != nil {
		t.Skipf("watcher unavailable: %v", err)
	}
	defer w.Close()

	// Baseline: no pruning ⇒ every directory registered.
	subAll, err := w.Subscribe(Spec{Roots: []string{root}, OnEvent: func([]Event) {}})
	if err != nil {
		t.Fatal(err)
	}
	if got := w.WatchedDirCountForTest(); got != total {
		t.Fatalf("no SkipDir: watched %d dirs, want the whole tree (%d)", got, total)
	}
	subAll.Unsubscribe()

	// Pruned: keep only docs/**.
	keepDocs := func(dir string) bool {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			return true
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			return false
		}
		top := rel
		if i := strings.IndexByte(top, '/'); i >= 0 {
			top = top[:i]
		}
		return top != "docs"
	}
	subDocs, err := w.Subscribe(Spec{Roots: []string{root}, SkipDir: keepDocs, OnEvent: func([]Event) {}})
	if err != nil {
		t.Fatal(err)
	}
	defer subDocs.Unsubscribe()

	wantPruned := 1 + 1 + 5 + 5*5 // root + docs + its mids + its leaves
	if got := w.WatchedDirCountForTest(); got != wantPruned {
		t.Errorf("SkipDir did not prune: watched %d dirs, want %d (root + docs subtree only, out of %d)",
			got, wantPruned, total)
	}
}

// TestSubscribe_SkipDirNeverPrunesTheRoot — the root must stay watched even if
// the predicate would reject it, otherwise a subscriber can never notice its
// target subtree being CREATED (the Sprint tab's "repo has no docs/ yet" case).
func TestSubscribe_SkipDirNeverPrunesTheRoot(t *testing.T) {
	root := t.TempDir()
	buildTree(t, root, []string{"a"}, 1, 1)

	w, err := New(slog.Default())
	if err != nil {
		t.Skipf("watcher unavailable: %v", err)
	}
	defer w.Close()

	sub, err := w.Subscribe(Spec{
		Roots:   []string{root},
		SkipDir: func(string) bool { return true }, // reject everything
		OnEvent: func([]Event) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Unsubscribe()

	if got := w.WatchedDirCountForTest(); got != 1 {
		t.Errorf("watched %d dirs, want exactly 1 (the root itself)", got)
	}
}
