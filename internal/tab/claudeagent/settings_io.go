package claudeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

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
	if worktree == "" {
		return fmt.Errorf("claudeagent: empty worktree")
	}
	dir := filepath.Join(worktree, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, "settings.json")
	var doc map[string]any
	if b, err := os.ReadFile(path); err == nil {
		if e := json.Unmarshal(b, &doc); e != nil {
			// Don't clobber malformed user JSON. Surface the error.
			return fmt.Errorf("parse %s: %w", path, e)
		}
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
