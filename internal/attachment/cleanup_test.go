package attachment

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestCleanupOlderThan verifies the two-level traversal: stale files
// in `<root>/<repo>/<branch>/` go away, fresh files survive, and the
// per-branch / per-repo directories collapse when they go empty.
func TestCleanupOlderThan(t *testing.T) {
	root := t.TempDir()

	mustWrite := func(p string, age time.Duration) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
		if age > 0 {
			old := time.Now().Add(-age)
			if err := os.Chtimes(p, old, old); err != nil {
				t.Fatalf("chtimes %s: %v", p, err)
			}
		}
	}

	stale := filepath.Join(root, "repoA", "branch1", "old.txt")
	fresh := filepath.Join(root, "repoA", "branch1", "new.txt")
	emptyBranchStale := filepath.Join(root, "repoB", "branch1", "old2.txt")
	rootLevelStale := filepath.Join(root, "legacy.png")
	rootLevelFresh := filepath.Join(root, "legacy-new.png")

	mustWrite(stale, 60*24*time.Hour)
	mustWrite(fresh, 0)
	mustWrite(emptyBranchStale, 60*24*time.Hour)
	mustWrite(rootLevelStale, 60*24*time.Hour)
	mustWrite(rootLevelFresh, 0)

	files, dirs, err := CleanupOlderThan(root, 30*24*time.Hour, slog.Default())
	if err != nil {
		t.Fatalf("CleanupOlderThan: %v", err)
	}
	// 3 stale files removed (stale, emptyBranchStale, rootLevelStale).
	if files != 3 {
		t.Errorf("files removed = %d, want 3", files)
	}
	// emptyBranchStale's branch dir went empty + repoB went empty → 2 dirs.
	if dirs < 2 {
		t.Errorf("dirs removed = %d, want >=2", dirs)
	}

	// Fresh files must still be there.
	for _, p := range []string{fresh, rootLevelFresh} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to survive, got err: %v", p, err)
		}
	}
	// Stale ones gone.
	for _, p := range []string{stale, emptyBranchStale, rootLevelStale} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("expected %s removed, err=%v", p, err)
		}
	}
	// repoB should be gone.
	if _, err := os.Stat(filepath.Join(root, "repoB")); !os.IsNotExist(err) {
		t.Errorf("expected repoB dir removed, err=%v", err)
	}
	// repoA still has branch1 (fresh file inside).
	if _, err := os.Stat(filepath.Join(root, "repoA", "branch1")); err != nil {
		t.Errorf("expected repoA/branch1 to survive, err=%v", err)
	}
}

func TestCleanupOlderThan_EmptyRoot(t *testing.T) {
	root := t.TempDir()
	files, dirs, err := CleanupOlderThan(root, 24*time.Hour, slog.Default())
	if err != nil {
		t.Fatalf("CleanupOlderThan: %v", err)
	}
	if files != 0 || dirs != 0 {
		t.Errorf("expected (0,0), got (%d,%d)", files, dirs)
	}
}

func TestCleanupOlderThan_MissingRoot(t *testing.T) {
	files, dirs, err := CleanupOlderThan("/tmp/palmux-attachment-cleanup-does-not-exist-zzz", 24*time.Hour, slog.Default())
	if err != nil {
		t.Fatalf("CleanupOlderThan on missing dir: %v", err)
	}
	if files != 0 || dirs != 0 {
		t.Errorf("expected (0,0), got (%d,%d)", files, dirs)
	}
}

func TestRemoveBranchDir(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "repoA", "branch1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := RemoveBranchDir(root, "repoA", "branch1"); err != nil {
		t.Fatalf("RemoveBranchDir: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("branch dir still there, err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "repoA")); !os.IsNotExist(err) {
		t.Errorf("repo dir still there, err=%v", err)
	}
	// Refuse to remove root itself.
	if err := RemoveBranchDir(root, "", ""); err == nil {
		t.Error("expected error on empty repo/branch")
	}
}
