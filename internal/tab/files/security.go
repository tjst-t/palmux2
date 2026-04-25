package files

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrInvalidPath is returned when the requested path escapes the worktree
// (via `..`, an absolute path, or a symlink that resolves outside).
var ErrInvalidPath = errors.New("invalid path")

// resolveSafePath joins relPath onto worktreeRoot and ensures the result is
// still inside the root after symlink resolution. Returns the absolute path
// or ErrInvalidPath. relPath of "" or "." is treated as the root.
func resolveSafePath(worktreeRoot, relPath string) (string, error) {
	if worktreeRoot == "" {
		return "", fmt.Errorf("%w: empty worktree root", ErrInvalidPath)
	}
	if relPath == "" || relPath == "." {
		relPath = "."
	}
	if filepath.IsAbs(relPath) {
		return "", fmt.Errorf("%w: absolute path not allowed", ErrInvalidPath)
	}
	cleaned := filepath.Clean(relPath)
	if strings.HasPrefix(cleaned, "..") || strings.Contains(cleaned, "/../") {
		return "", fmt.Errorf("%w: parent traversal", ErrInvalidPath)
	}
	abs := filepath.Join(worktreeRoot, cleaned)
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Path doesn't exist yet (or symlink target missing) — accept the
		// pre-resolution path; callers will get a 404 from the actual op.
		resolved = abs
	}
	rootResolved, err := filepath.EvalSymlinks(worktreeRoot)
	if err != nil {
		rootResolved = worktreeRoot
	}
	if !strings.HasPrefix(resolved+string(filepath.Separator), rootResolved+string(filepath.Separator)) && resolved != rootResolved {
		return "", fmt.Errorf("%w: outside worktree", ErrInvalidPath)
	}
	return abs, nil
}
