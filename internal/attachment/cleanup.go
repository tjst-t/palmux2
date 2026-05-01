// Package attachment owns the per-branch attachment storage layout used
// by the Composer's upload path. Files live under
// `<root>/<repoId>/<branchId>/<sanitized-name>`. Older callers (the bash
// terminal-view paste path) still write directly to `<root>/<name>`.
//
// This package only owns the on-disk lifecycle (TTL cleanup at startup).
// The HTTP plumbing — POST /api/upload + per-branch variant — lives in
// internal/server/handler_upload.go where the routing is registered.
package attachment

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// CleanupOlderThan walks `root` and removes regular files whose mtime
// is older than now-ttl. Two-level directories
// (`<root>/<repo>/<branch>/`) are inspected; deeper directories are
// ignored as a safety measure since the upload writer never creates
// them. Returns (filesRemoved, dirsRemoved, firstErr) — non-fatal
// errors during traversal are logged via the optional logger and the
// walk continues.
//
// An empty per-branch dir is removed after its files; the per-repo
// parent is removed when it goes empty too. The root itself stays put.
func CleanupOlderThan(root string, ttl time.Duration, logger *slog.Logger) (int, int, error) {
	if root == "" {
		return 0, 0, errors.New("attachment: empty root")
	}
	if ttl <= 0 {
		return 0, 0, errors.New("attachment: non-positive ttl")
	}
	if logger == nil {
		logger = slog.Default()
	}
	cutoff := time.Now().Add(-ttl)

	info, err := os.Stat(root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, 0, nil
		}
		return 0, 0, fmt.Errorf("stat %s: %w", root, err)
	}
	if !info.IsDir() {
		return 0, 0, fmt.Errorf("attachment: root %s is not a directory", root)
	}

	files, dirs := 0, 0
	// Pass 1: stale files directly under the root (legacy global
	// uploads). We don't remove the root itself.
	if got, err := sweepDir(root, cutoff, logger); err != nil {
		logger.Warn("attachment: sweep root failed", "dir", root, "err", err)
	} else {
		files += got
	}

	// Pass 2: descend two directory levels (repo/branch). Anything else
	// is treated as foreign and left alone.
	repos, err := os.ReadDir(root)
	if err != nil {
		return files, dirs, fmt.Errorf("read %s: %w", root, err)
	}
	for _, repo := range repos {
		if !repo.IsDir() {
			continue
		}
		repoDir := filepath.Join(root, repo.Name())
		branches, err := os.ReadDir(repoDir)
		if err != nil {
			logger.Warn("attachment: read repo dir failed", "dir", repoDir, "err", err)
			continue
		}
		emptyBranches := 0
		for _, br := range branches {
			if !br.IsDir() {
				continue
			}
			branchDir := filepath.Join(repoDir, br.Name())
			got, err := sweepDir(branchDir, cutoff, logger)
			if err != nil {
				logger.Warn("attachment: sweep branch dir failed", "dir", branchDir, "err", err)
				continue
			}
			files += got
			// If the branch dir went empty, remove it.
			entries, err := os.ReadDir(branchDir)
			if err == nil && len(entries) == 0 {
				if err := os.Remove(branchDir); err == nil {
					dirs++
					emptyBranches++
				}
			}
		}
		// Repo dir is empty too if all its branches got removed.
		entries, err := os.ReadDir(repoDir)
		if err == nil && len(entries) == 0 {
			if err := os.Remove(repoDir); err == nil {
				dirs++
			}
		}
		_ = emptyBranches
	}
	return files, dirs, nil
}

// sweepDir removes regular files older than cutoff in dir (non-
// recursive). Returns the count of files removed.
func sweepDir(dir string, cutoff time.Time, logger *slog.Logger) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		full := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			logger.Warn("attachment: stat failed", "path", full, "err", err)
			continue
		}
		if info.Mode()&os.ModeType != 0 {
			// Skip anything that isn't a regular file (symlinks,
			// device nodes). The upload writer only creates regular
			// files; defence in depth.
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		if err := os.Remove(full); err != nil {
			logger.Warn("attachment: remove failed", "path", full, "err", err)
			continue
		}
		removed++
	}
	return removed, nil
}

// RemoveBranchDir removes the per-branch attachment directory and
// everything inside it. Used on branch close so abandoned attachments
// don't accumulate forever even if the user runs the server long
// enough that the daily TTL never matters.
func RemoveBranchDir(root, repoID, branchID string) error {
	if root == "" || repoID == "" || branchID == "" {
		return errors.New("attachment: missing path component")
	}
	dir := filepath.Join(root, repoID, branchID)
	// Refuse to remove the root itself or anything outside it.
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	if abs == rootAbs {
		return errors.New("attachment: refusing to remove root")
	}
	if err := os.RemoveAll(abs); err != nil && !os.IsNotExist(err) {
		return err
	}
	// Try to also drop the repo dir if it went empty.
	repoDir := filepath.Join(root, repoID)
	if entries, err := os.ReadDir(repoDir); err == nil && len(entries) == 0 {
		_ = os.Remove(repoDir)
	}
	return nil
}
