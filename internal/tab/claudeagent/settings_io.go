package claudeagent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// settingsScope identifies which `.claude/settings.json` we are talking about.
//
//   - scopeProject lives at `<worktree>/.claude/settings.json` and is the
//     scope we write to from `Always allow`.
//   - scopeUser lives at `~/.claude/settings.json` and is the user-global
//     scope. We never *write* to user implicitly — only via the explicit
//     Settings popup with a user-confirm step.
type settingsScope string

const (
	scopeProject settingsScope = "project"
	scopeUser    settingsScope = "user"
)

// resolveSettingsPath returns the absolute path of a settings.json for the
// given scope, plus the parent directory (for MkdirAll on first write).
//
// `worktree` is required for project scope and ignored for user scope.
//
// User scope honours `os.UserHomeDir()` (which respects $HOME on Unix), so
// tests can isolate writes via `t.Setenv("HOME", tmp)`.
func resolveSettingsPath(scope settingsScope, worktree string) (path, dir string, err error) {
	switch scope {
	case scopeProject:
		if worktree == "" {
			return "", "", errors.New("claudeagent: empty worktree")
		}
		dir = filepath.Join(worktree, ".claude")
	case scopeUser:
		home, e := os.UserHomeDir()
		if e != nil {
			return "", "", e
		}
		dir = filepath.Join(home, ".claude")
	default:
		return "", "", fmt.Errorf("claudeagent: unknown settings scope %q", scope)
	}
	return filepath.Join(dir, "settings.json"), dir, nil
}

// readSettingsDoc reads `<scope>/settings.json` and returns the parsed
// document. A missing file is *not* an error — we return (nil, nil) so the
// caller can treat it as an empty doc.
func readSettingsDoc(scope settingsScope, worktree string) (map[string]any, string, error) {
	path, _, err := resolveSettingsPath(scope, worktree)
	if err != nil {
		return nil, "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, path, nil
		}
		return nil, path, err
	}
	if len(b) == 0 {
		return nil, path, nil
	}
	var doc map[string]any
	if err := json.Unmarshal(b, &doc); err != nil {
		return nil, path, fmt.Errorf("parse %s: %w", path, err)
	}
	return doc, path, nil
}

// writeSettingsDoc atomically replaces `<scope>/settings.json` with the
// given document. The directory is created if necessary. The document is
// marshalled with 2-space indent for human editing, mirroring
// addToProjectAllowList.
func writeSettingsDoc(scope settingsScope, worktree string, doc map[string]any) error {
	path, dir, err := resolveSettingsPath(scope, worktree)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// removeFromAllowList removes a single entry (exact-match) from
// `permissions.allow` of the given scope. If the file does not exist or
// the entry is not present the call is a no-op (returns nil) — this is
// idempotent on purpose so the UI can be optimistic.
//
// `permissions.allow` is set to an empty array if the last entry is
// removed, rather than dropping the key, because the CLI distinguishes
// "explicit empty" (deny everything not user-prompted) from "not set".
// Empty array is the safer state.
func removeFromAllowList(scope settingsScope, worktree, pattern string) error {
	if pattern == "" {
		return errors.New("claudeagent: empty pattern")
	}
	doc, _, err := readSettingsDoc(scope, worktree)
	if err != nil {
		return err
	}
	if doc == nil {
		return nil
	}
	perms, _ := doc["permissions"].(map[string]any)
	if perms == nil {
		return nil
	}
	allowAny, ok := perms["allow"]
	if !ok {
		return nil
	}
	arr, ok := allowAny.([]any)
	if !ok {
		return nil
	}
	out := make([]any, 0, len(arr))
	removed := false
	for _, v := range arr {
		if s, ok := v.(string); ok && s == pattern {
			removed = true
			continue
		}
		out = append(out, v)
	}
	if !removed {
		return nil
	}
	perms["allow"] = out
	doc["permissions"] = perms
	return writeSettingsDoc(scope, worktree, doc)
}

// addToProjectAllowList appends the given pattern to a worktree-scoped
// `.claude/settings.json`'s `permissions.allow` array, idempotently.
//
// The CLI also reads user-scope (`~/.claude/settings.json`) and local
// (`.claude/settings.local.json`) — we always write to project for two
// reasons: (a) it's discoverable next to the worktree, (b) it's the
// least-invasive scope that survives across machines via git.
//
// If the file or directory doesn't exist, they are created.
//
// The merged settings JSON is written back with 2-space indent for human
// editing.
func addToProjectAllowList(worktree, pattern string) error {
	doc, _, err := readSettingsDoc(scopeProject, worktree)
	if err != nil {
		return err
	}
	if doc == nil {
		doc = map[string]any{}
	}
	perms, _ := doc["permissions"].(map[string]any)
	if perms == nil {
		perms = map[string]any{}
		doc["permissions"] = perms
	}
	allowAny := perms["allow"]
	allow := []string{}
	if arr, ok := allowAny.([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				allow = append(allow, s)
			}
		}
	}
	for _, existing := range allow {
		if existing == pattern {
			return nil // already present
		}
	}
	allow = append(allow, pattern)
	// Re-marshal as []any so json.Marshal honours the []string type.
	out := make([]any, len(allow))
	for i, s := range allow {
		out[i] = s
	}
	perms["allow"] = out
	return writeSettingsDoc(scopeProject, worktree, doc)
}
