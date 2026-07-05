package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Sd44947: helpers for the [workspace] shared_dirs list. A shared dir is a host
// directory bind-mounted into every incus-container Workspace via the
// host-wide `palmux-shared` incus profile. The security invariant (mirrored in
// the GUI warning and DESIGN_PRINCIPLES priority_rule 5) is that only paths
// UNDER $HOME may be shared — an operator cannot accidentally expose /etc or /
// to every container from the deploy tab.

// ExpandSharedDir normalises one shared-dir entry against home:
//   - a leading "~" or "~/" is expanded to home
//   - the result is cleaned to an absolute path
//   - the path MUST resolve to home itself or a descendant of home; anything
//     outside $HOME is rejected with an error (AC-Sd44947-2-3)
//
// It does NOT check for source existence — a non-existent source is a valid
// declaration that is simply skipped when the profile devices are built
// (sources come and go; the operator's intent to share the path is retained).
func ExpandSharedDir(entry, home string) (string, error) {
	raw := strings.TrimSpace(entry)
	if raw == "" {
		return "", fmt.Errorf("shared dir: empty path")
	}
	// ~ expansion.
	if raw == "~" {
		raw = home
	} else if strings.HasPrefix(raw, "~/") {
		raw = filepath.Join(home, raw[2:])
	} else if strings.HasPrefix(raw, "~") {
		// e.g. "~otheruser/..." — we only support the current user's home.
		return "", fmt.Errorf("shared dir %q: only ~/ (current user's home) is supported", entry)
	}

	if !filepath.IsAbs(raw) {
		return "", fmt.Errorf("shared dir %q: must be an absolute path or start with ~/", entry)
	}
	abs := filepath.Clean(raw)

	// $HOME-scope check. Reject anything that is not home itself or under home.
	homeClean := filepath.Clean(home)
	if abs != homeClean {
		rel, err := filepath.Rel(homeClean, abs)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			return "", fmt.Errorf("shared dir %q: paths must be under $HOME (%s)", entry, homeClean)
		}
	}
	return abs, nil
}

// ExpandSharedDirs expands+validates a whole list, returning the absolute paths.
// The first invalid entry aborts with an error naming it (so the GUI can show a
// precise inline message). Duplicates are de-duplicated preserving order.
func ExpandSharedDirs(entries []string, home string) ([]string, error) {
	seen := map[string]bool{}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		abs, err := ExpandSharedDir(e, home)
		if err != nil {
			return nil, err
		}
		if seen[abs] {
			continue
		}
		seen[abs] = true
		out = append(out, abs)
	}
	return out, nil
}
