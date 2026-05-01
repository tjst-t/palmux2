package files

import (
	"bufio"
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
