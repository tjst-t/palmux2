package worktreewatch

// WatchedDirCountForTest reports how many directories are currently registered
// with the underlying fsnotify watcher. Test-only.
func (w *Watcher) WatchedDirCountForTest() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.rootRefs)
}
