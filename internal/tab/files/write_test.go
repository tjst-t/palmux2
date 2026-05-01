package files

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// S011: ComputeETag must be deterministic and short (we shove it into
// HTTP headers). Verify the format is the quoted base64 hash and
// changes when either field changes.
func TestComputeETag(t *testing.T) {
	t1 := time.Unix(1700000000, 0)
	a := ComputeETag(t1, 100)
	if !strings.HasPrefix(a, `"`) || !strings.HasSuffix(a, `"`) {
		t.Fatalf("ETag must be quoted: %q", a)
	}
	if a == ComputeETag(t1, 101) {
		t.Fatalf("size change must change ETag")
	}
	if a == ComputeETag(t1.Add(time.Second), 100) {
		t.Fatalf("mtime change must change ETag")
	}
	if a != ComputeETag(t1, 100) {
		t.Fatalf("same inputs must give same ETag")
	}
}

// S011: WriteFile must atomically replace the file, preserve mode bits,
// and report a fresh ETag.
func TestWriteFile(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, "hello.txt")
	if err := os.WriteFile(abs, []byte("v1"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tagBefore, err := EtagFor(root, "hello.txt")
	if err != nil {
		t.Fatalf("EtagFor: %v", err)
	}
	// sleep 10ms to make sure mtime moves on fast filesystems.
	time.Sleep(10 * time.Millisecond)
	info, etag, err := WriteFile(root, "hello.txt", []byte("v2-longer"))
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if info.Size != int64(len("v2-longer")) {
		t.Fatalf("size: got %d, want %d", info.Size, len("v2-longer"))
	}
	if etag == tagBefore {
		t.Fatalf("etag must change on write: before=%s after=%s", tagBefore, etag)
	}
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if string(got) != "v2-longer" {
		t.Fatalf("content: got %q", string(got))
	}
	st, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("perm leak: got %o, want 0600", st.Mode().Perm())
	}
}

// S011: WriteFile must refuse to create new files. The Files tab is
// "edit existing" only in this sprint.
func TestWriteFileMissing(t *testing.T) {
	root := t.TempDir()
	_, _, err := WriteFile(root, "nope.txt", []byte("x"))
	if err == nil {
		t.Fatalf("expected error on missing target")
	}
	if !os.IsNotExist(err) {
		t.Fatalf("expected ErrNotExist; got %v", err)
	}
}

// S011: WriteFile must refuse traversal/absolute paths.
func TestWriteFilePathSafety(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{"../etc/passwd", "/etc/passwd", "x/../../escape"} {
		_, _, err := WriteFile(root, p, []byte("x"))
		if err == nil {
			t.Errorf("expected error for %q", p)
		}
	}
}
