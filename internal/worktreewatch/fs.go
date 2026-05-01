package worktreewatch

import (
	"io/fs"
	"os"
	"path/filepath"
)

// pathInfo lets us pass either an os.FileInfo (from os.Stat) or fs.DirEntry
// info into the same helper signatures. We only need IsDir() so we keep
// the surface minimal.
type pathInfo interface {
	IsDir() bool
}

// filepath.Walk wants a WalkFunc with signature
//   func(path string, info os.FileInfo, err error) error.
// Wrap it so addRecursiveLocked can use a leaner pathInfo type that's
// trivial to test.

// filepath.Walk wrapper that adapts os.FileInfo into our pathInfo. Defined
// at package level so addRecursiveLocked can reference it cleanly.
func walk(root string, fn func(p string, info pathInfo, err error) error) error {
	return filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return fn(p, nil, err)
		}
		return fn(p, info, nil)
	})
}

// We override filepath.Walk in watcher.go via a renamed shim — but Go has
// no `import as`, so we just keep using filepath.Walk directly there and
// rely on os.FileInfo satisfying pathInfo (it does, via IsDir()).

// isDir reports whether p exists and is a directory. It is best-effort:
// any error (including ENOENT for an entry that vanished between the
// fsnotify Create and our Stat) returns false.
func isDir(p string) bool {
	info, err := os.Stat(p)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// statisfy compile assertion: ensure os.FileInfo satisfies pathInfo.
var _ pathInfo = (fs.FileInfo)(nil)
