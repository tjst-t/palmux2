// Package domain holds the core entity types and ID helpers. It must not
// import any other internal/* package (other than the standard library) so it
// can be used freely from any layer.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
)

// hashLen is the length (in hex chars) of the short hash appended to each ID.
// 4 hex chars = 16 bits = ~65k space; combined with the slug it gives
// human-readable IDs with negligible collision risk in practice.
const hashLen = 4

// `.` is excluded so branch/repo names like "v0.5" or "next.js" produce slugs
// that are valid tmux session names (tmux forbids period/colon/space).
var nonSlugRune = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// RepoSlugID converts a ghq-relative path (e.g. "github.com/tjst-t/palmux")
// into a human-readable, URL-safe ID like "tjst-t--palmux--a1b2".
//
// The host segment is dropped; remaining path segments are joined with "--".
// A 4-char SHA256 hash of the original input is appended to break ties.
func RepoSlugID(ghqRelPath string) string {
	parts := strings.Split(strings.Trim(ghqRelPath, "/"), "/")
	var slugParts []string
	if len(parts) > 1 {
		slugParts = parts[1:] // drop host
	} else {
		slugParts = parts
	}
	slug := strings.Join(slugParts, "--")
	slug = sanitizeSlug(slug)
	return slug + "--" + sha256Hex(ghqRelPath, hashLen)
}

// BranchSlugID derives a stable URL-safe ID for a branch within a repository
// using the **legacy** branch-name based scheme (`{branchSlug}--{hash4}`).
//
// S1e8d02: this function is preserved only so the startup migration can
// recompute the pre-S1e8d02 ID a previously persisted entry was filed
// under and rewrite it to the new path-derived [WorkspaceSlugID] form.
// New code MUST NOT call this for ID generation — it would re-introduce
// the in-place-checkout-destroys-everything bug. Use
// [WorkspaceSlugIDFromPath] instead.
func BranchSlugID(repoFullPath, branchName string) string {
	slug := strings.ReplaceAll(branchName, "/", "--")
	slug = sanitizeSlug(slug)
	return slug + "--" + sha256Hex(repoFullPath+":"+branchName, hashLen)
}

// WorkspaceSlugIDFromPath derives a stable, URL-safe ID for a Branch (= the
// dynamic-attribute-on-a-Workspace per the S1e8d02 domain refactor) from
// the **worktree path**, not from the branch name.
//
// Identity rule: same on-disk path → same ID, regardless of which git
// branch happens to be checked out there at any moment. This is what
// keeps `git checkout` from looking like "branch X disappeared + branch
// Y appeared" to sync_worktree, and therefore keeps Claude / tmux / Files
// alive across in-place checkouts (the S1e8d02 incident root-cause).
//
// Slug rules:
//   - primary worktree (`isPrimary == true`): slug = repo dir basename,
//     hash4 = sha256(absolute worktree path)[:4]. Yields stable IDs like
//     `KuraOS--7a8b` regardless of HEAD.
//   - non-primary worktree (gwq-managed linked worktree): slug = the
//     worktree's own dir basename. hash4 still keyed on absolute path so
//     two worktrees that happen to share a basename still differ.
//
// Punctuation in either basename is sanitized the same way [BranchSlugID]
// sanitizes branch names so the result is safe to use as a tmux session
// fragment and a URL path segment.
func WorkspaceSlugIDFromPath(worktreePath string, isPrimary bool, repoFullPath string) string {
	var slug string
	if isPrimary {
		slug = filepath.Base(repoFullPath)
	} else {
		slug = filepath.Base(worktreePath)
	}
	slug = sanitizeSlug(slug)
	return slug + "--" + sha256Hex(worktreePath, hashLen)
}

// TabID returns the tab identifier used in API URLs and the URL bar.
// Examples: "claude", "files", "git", "bash:bash", "bash:my-server".
// For singletons (claude, files, git), pass an empty name.
func TabID(tabType, name string) string {
	if name == "" {
		return tabType
	}
	return tabType + ":" + sanitizeSlug(name)
}

func sanitizeSlug(s string) string {
	return nonSlugRune.ReplaceAllString(s, "_")
}

func sha256Hex(input string, n int) string {
	sum := sha256.Sum256([]byte(input))
	full := hex.EncodeToString(sum[:])
	if n > len(full) {
		n = len(full)
	}
	return full[:n]
}
