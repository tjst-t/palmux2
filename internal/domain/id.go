// Package domain holds the core entity types and ID helpers. It must not
// import any other internal/* package (other than the standard library) so it
// can be used freely from any layer.
package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
)

// hashLen is the length (in hex chars) of the short hash appended to each ID.
// 4 hex chars = 16 bits = ~65k space; combined with the slug it gives
// human-readable IDs with negligible collision risk in practice.
const hashLen = 4

var nonSlugRune = regexp.MustCompile(`[^a-zA-Z0-9._-]`)

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

// BranchSlugID derives a stable URL-safe ID for a branch within a repository.
// Slashes in the branch name become "--" and other unsafe chars are replaced
// with "_". A 4-char hash of repoFullPath:branchName is appended.
func BranchSlugID(repoFullPath, branchName string) string {
	slug := strings.ReplaceAll(branchName, "/", "--")
	slug = sanitizeSlug(slug)
	return slug + "--" + sha256Hex(repoFullPath+":"+branchName, hashLen)
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
