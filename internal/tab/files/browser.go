package files

import (
	"bufio"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Entry describes one filesystem entry returned by ListDir.
type Entry struct {
	Name    string    `json:"name"`
	Path    string    `json:"path"` // worktree-relative
	IsDir   bool      `json:"isDir"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
	IsLink  bool      `json:"isLink,omitempty"`
}

// ListDir lists the directory at relPath inside worktreeRoot. Returns
// dirs-first, then files, both sorted alphabetically.
func ListDir(worktreeRoot, relPath string) ([]Entry, error) {
	abs, err := resolveSafePath(worktreeRoot, relPath)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]Entry, 0, len(entries))
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		rel, _ := filepath.Rel(worktreeRoot, filepath.Join(abs, e.Name()))
		out = append(out, Entry{
			Name:    e.Name(),
			Path:    filepath.ToSlash(rel),
			IsDir:   e.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
			IsLink:  info.Mode()&os.ModeSymlink != 0,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// FileInfo describes a single readable file.
type FileInfo struct {
	Path     string `json:"path"`
	Size     int64  `json:"size"`
	IsBinary bool   `json:"isBinary"`
	MIME     string `json:"mime,omitempty"`
}

// StatFile returns metadata (path / size / MIME / isBinary) without reading
// the full body. Used by the Files-tab viewer dispatcher (S010) to skip the
// preview round-trip for files above `previewMaxBytes`. We sniff only a
// 512-byte head for MIME detection — that's enough for `looksBinary` and
// `detectMIME` to make their decisions.
func StatFile(worktreeRoot, relPath string) (FileInfo, error) {
	abs, err := resolveSafePath(worktreeRoot, relPath)
	if err != nil {
		return FileInfo{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return FileInfo{}, err
	}
	if st.IsDir() {
		return FileInfo{}, fmt.Errorf("%w: %s is a directory", ErrInvalidPath, relPath)
	}
	info := FileInfo{Path: relPath, Size: st.Size()}
	f, err := os.Open(abs)
	if err != nil {
		return info, err
	}
	defer f.Close()
	head := make([]byte, 512)
	n, err := f.Read(head)
	if err != nil && err != io.EOF {
		return info, err
	}
	head = head[:n]
	info.IsBinary = looksBinary(head)
	info.MIME = detectMIME(relPath, head)
	return info, nil
}

// ReadFile reads up to maxBytes of the file at relPath. Files larger than
// readLimit are flagged isLarge so the UI can show a placeholder.
func ReadFile(worktreeRoot, relPath string, maxBytes int64) ([]byte, FileInfo, error) {
	abs, err := resolveSafePath(worktreeRoot, relPath)
	if err != nil {
		return nil, FileInfo{}, err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return nil, FileInfo{}, err
	}
	if st.IsDir() {
		return nil, FileInfo{}, fmt.Errorf("%w: %s is a directory", ErrInvalidPath, relPath)
	}
	info := FileInfo{Path: relPath, Size: st.Size()}
	f, err := os.Open(abs)
	if err != nil {
		return nil, info, err
	}
	defer f.Close()
	limit := maxBytes
	if limit <= 0 || limit > st.Size() {
		limit = st.Size()
	}
	buf := make([]byte, limit)
	n, err := io.ReadFull(f, buf)
	if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
		return nil, info, err
	}
	buf = buf[:n]
	info.IsBinary = looksBinary(buf)
	info.MIME = detectMIME(relPath, buf)
	return buf, info, nil
}

// SearchEntries lists worktree-relative paths whose basename contains query
// (case-insensitive). Limited to maxResults to keep the UI responsive.
func SearchEntries(worktreeRoot, baseRel, query string, caseSensitive bool, maxResults int) ([]Entry, error) {
	abs, err := resolveSafePath(worktreeRoot, baseRel)
	if err != nil {
		return nil, err
	}
	if !caseSensitive {
		query = strings.ToLower(query)
	}
	var results []Entry
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.Name() == ".git" && d.IsDir() {
			return fs.SkipDir
		}
		name := d.Name()
		hay := name
		if !caseSensitive {
			hay = strings.ToLower(name)
		}
		if !strings.Contains(hay, query) {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(worktreeRoot, p)
		results = append(results, Entry{
			Name:    name,
			Path:    filepath.ToSlash(rel),
			IsDir:   d.IsDir(),
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
		if len(results) >= maxResults {
			return fs.SkipAll
		}
		return nil
	})
	return results, err
}

// GrepHit is one match returned by Grep.
type GrepHit struct {
	Path    string `json:"path"`
	LineNum int    `json:"lineNum"`
	Line    string `json:"line"`
}

// Grep walks the worktree, reading each non-binary file once, and returns
// every line containing pattern (substring match). Stops at maxResults.
func Grep(worktreeRoot, baseRel, pattern string, caseSensitive bool, maxResults int) ([]GrepHit, error) {
	abs, err := resolveSafePath(worktreeRoot, baseRel)
	if err != nil {
		return nil, err
	}
	if pattern == "" {
		return nil, nil
	}
	needle := pattern
	if !caseSensitive {
		needle = strings.ToLower(pattern)
	}
	var hits []GrepHit
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		f, err := os.Open(p)
		if err != nil {
			return nil
		}
		defer f.Close()
		// Skip likely-binary files by sniffing the first 1KiB.
		head := make([]byte, 1024)
		n, _ := f.Read(head)
		if looksBinary(head[:n]) {
			return nil
		}
		// Re-read from start.
		_, _ = f.Seek(0, io.SeekStart)
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 64*1024), 1<<20)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			hay := line
			if !caseSensitive {
				hay = strings.ToLower(line)
			}
			if !strings.Contains(hay, needle) {
				continue
			}
			rel, _ := filepath.Rel(worktreeRoot, p)
			hits = append(hits, GrepHit{Path: filepath.ToSlash(rel), LineNum: lineNum, Line: line})
			if len(hits) >= maxResults {
				return fs.SkipAll
			}
		}
		return nil
	})
	return hits, err
}

// looksBinary heuristically detects binary content (NUL bytes or high
// proportion of non-printable bytes).
func looksBinary(b []byte) bool {
	if len(b) == 0 {
		return false
	}
	nonprintable := 0
	for _, c := range b {
		if c == 0 {
			return true
		}
		// Allow tabs, CR, LF, normal ASCII range, and high bytes (UTF-8).
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			nonprintable++
		}
	}
	return float64(nonprintable)/float64(len(b)) > 0.3
}

// ComputeETag derives a short, opaque, base64 hash from a file's mtime
// (UnixNano) + size. We deliberately avoid hashing the full content so
// that opening a 5 MiB Monaco buffer doesn't pay an extra hash pass —
// mtime + size is good enough as a freshness fingerprint for the
// optimistic-locking flow (S011: PUT requires `If-Match: <etag>`).
//
// The format is `"<hash>"` (with the surrounding quotes) so it can be
// dropped straight into the HTTP `ETag` / `If-Match` headers without
// further escaping. Compares are byte-equal, including the quotes.
func ComputeETag(modTime time.Time, size int64) string {
	h := sha256.Sum256([]byte(fmt.Sprintf("%d.%d", modTime.UnixNano(), size)))
	enc := base64.RawURLEncoding.EncodeToString(h[:8])
	return `"` + enc + `"`
}

// EtagFor returns the current ETag for a worktree-relative file. Returns
// ErrInvalidPath / os errors for anything that can't be stat'd.
func EtagFor(worktreeRoot, relPath string) (string, error) {
	abs, err := resolveSafePath(worktreeRoot, relPath)
	if err != nil {
		return "", err
	}
	st, err := os.Stat(abs)
	if err != nil {
		return "", err
	}
	if st.IsDir() {
		return "", fmt.Errorf("%w: %s is a directory", ErrInvalidPath, relPath)
	}
	return ComputeETag(st.ModTime(), st.Size()), nil
}

// WriteFile replaces the file at relPath atomically and returns the new
// FileInfo + ETag. The write is atomic via a temp-file-then-rename so a
// crash mid-write can never leave a half-written file in the worktree.
//
// The caller is expected to have already validated the `If-Match` ETag
// against `EtagFor` before calling this — WriteFile doesn't re-check
// the precondition (the handler does that, with a small race window
// between EtagFor and rename, which is acceptable for a single-user
// editor — we surface conflicts on the *next* save).
//
// Parent directories are NOT auto-created. Saving must target an
// existing path that the user previously read; the Files tab is not a
// "create new file" surface in S011 (that would arrive in a later
// sprint).
func WriteFile(worktreeRoot, relPath string, content []byte) (FileInfo, string, error) {
	abs, err := resolveSafePath(worktreeRoot, relPath)
	if err != nil {
		return FileInfo{}, "", err
	}
	// Stat existing file to preserve permissions on rename. If the file
	// doesn't exist we refuse — S011 is "edit existing files only".
	st, err := os.Stat(abs)
	if err != nil {
		return FileInfo{}, "", err
	}
	if st.IsDir() {
		return FileInfo{}, "", fmt.Errorf("%w: %s is a directory", ErrInvalidPath, relPath)
	}

	dir := filepath.Dir(abs)
	tmp, err := os.CreateTemp(dir, ".palmux-write-*")
	if err != nil {
		return FileInfo{}, "", err
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = os.Remove(tmpPath)
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		cleanup()
		return FileInfo{}, "", err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return FileInfo{}, "", err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return FileInfo{}, "", err
	}
	if err := os.Chmod(tmpPath, st.Mode().Perm()); err != nil {
		cleanup()
		return FileInfo{}, "", err
	}
	if err := os.Rename(tmpPath, abs); err != nil {
		cleanup()
		return FileInfo{}, "", err
	}

	newSt, err := os.Stat(abs)
	if err != nil {
		return FileInfo{}, "", err
	}
	info := FileInfo{
		Path: relPath,
		Size: newSt.Size(),
	}
	// MIME / isBinary determination uses a fresh head sniff on the new
	// content (the user may have changed the content type — e.g. saving
	// over a `.txt` with binary bytes).
	head := content
	if len(head) > 512 {
		head = head[:512]
	}
	info.IsBinary = looksBinary(head)
	info.MIME = detectMIME(relPath, head)
	return info, ComputeETag(newSt.ModTime(), newSt.Size()), nil
}

// detectMIME picks a MIME type by extension, falling back to "text/plain".
func detectMIME(name string, head []byte) string {
	switch ext := strings.ToLower(filepath.Ext(name)); ext {
	case ".md", ".markdown":
		return "text/markdown"
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".svg":
		return "image/svg+xml"
	case ".webp":
		return "image/webp"
	case ".json":
		return "application/json"
	case ".html", ".htm":
		return "text/html"
	case ".css":
		return "text/css"
	case ".js", ".mjs":
		return "text/javascript"
	case ".ts", ".tsx":
		return "text/typescript"
	case ".go":
		return "text/x-go"
	case ".py":
		return "text/x-python"
	}
	if looksBinary(head) {
		return "application/octet-stream"
	}
	return "text/plain"
}
