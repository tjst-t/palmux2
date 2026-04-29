package claudeagent

import (
	"encoding/json"
	"sort"
)

// SettingsView is the structured projection of one settings.json that the
// frontend consumes. We split the well-known `permissions` keys out into
// typed string slices so the UI can render them with delete buttons, and
// stash everything else (hooks, env, model, …) under `Other` as raw JSON
// so the UI can show it read-only without us having to mirror the full
// CLI schema.
type SettingsView struct {
	// Path is the absolute on-disk path to the settings.json. Always set
	// (even when Exists is false) so the UI can show "would be created at
	// <path>" hints.
	Path string `json:"path"`

	// Exists is true iff the file is present on disk. A non-existent file
	// is rendered as an empty section in the UI.
	Exists bool `json:"exists"`

	// PermissionsAllow / PermissionsDeny are the parsed string entries.
	// Non-string entries are silently dropped — the schema only uses
	// strings here in practice.
	PermissionsAllow []string `json:"permissionsAllow"`
	PermissionsDeny  []string `json:"permissionsDeny"`

	// Hooks is whatever lived under the `hooks` key, as raw JSON. Read-
	// only in the UI for now (S002 scope is permissions only).
	Hooks json.RawMessage `json:"hooks,omitempty"`

	// Other is every top-level key we didn't categorise above, as raw
	// JSON. Lets the UI surface custom fields without us having to keep
	// a CLI-schema-tracking type list. Sorted by key for stable
	// rendering.
	Other map[string]json.RawMessage `json:"other,omitempty"`

	// ParseError is populated when the file exists but is malformed. The
	// UI surfaces it in place of the parsed sections.
	ParseError string `json:"parseError,omitempty"`
}

// loadSettingsView reads a settings.json (project or user scope) and
// projects it into a SettingsView. It never returns an error for
// "missing file" or "malformed JSON" — both states are encoded in the
// returned view so the UI can render partial state.
func loadSettingsView(scope settingsScope, worktree string) (SettingsView, error) {
	path, _, err := resolveSettingsPath(scope, worktree)
	if err != nil {
		return SettingsView{}, err
	}
	view := SettingsView{Path: path}

	doc, _, err := readSettingsDoc(scope, worktree)
	if err != nil {
		// Distinguish "file doesn't exist" (already returned doc=nil,
		// err=nil) from "file exists but malformed" (returned err with
		// the path embedded).
		view.Exists = true
		view.ParseError = err.Error()
		return view, nil
	}
	if doc == nil {
		return view, nil
	}
	view.Exists = true

	// permissions.{allow,deny}
	if perms, ok := doc["permissions"].(map[string]any); ok {
		view.PermissionsAllow = stringSlice(perms["allow"])
		view.PermissionsDeny = stringSlice(perms["deny"])
		// Anything else under permissions (e.g. ask, additionalDirectories)
		// goes into Other.permissions so the UI can still see it. We
		// remove the well-known keys before re-emitting.
		extras := map[string]any{}
		for k, v := range perms {
			if k == "allow" || k == "deny" {
				continue
			}
			extras[k] = v
		}
		if len(extras) > 0 {
			if b, err := json.Marshal(extras); err == nil {
				if view.Other == nil {
					view.Other = map[string]json.RawMessage{}
				}
				view.Other["permissions(other)"] = b
			}
		}
	}

	// hooks → raw JSON
	if hooks, ok := doc["hooks"]; ok {
		if b, err := json.Marshal(hooks); err == nil {
			view.Hooks = b
		}
	}

	// Everything else top-level
	other := map[string]json.RawMessage{}
	for k, v := range doc {
		if k == "permissions" || k == "hooks" {
			continue
		}
		if b, err := json.Marshal(v); err == nil {
			other[k] = b
		}
	}
	if len(other) > 0 {
		if view.Other == nil {
			view.Other = map[string]json.RawMessage{}
		}
		for k, v := range other {
			view.Other[k] = v
		}
	}
	// Stable iteration: sort the keys so JSON output is deterministic
	// (mostly for tests; UI sorts client-side too).
	if len(view.Other) > 0 {
		keys := make([]string, 0, len(view.Other))
		for k := range view.Other {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		// Rebuild map in sorted insertion order for json.Encode (Go's
		// json package sorts map keys itself, so this is mostly defensive).
		ordered := make(map[string]json.RawMessage, len(view.Other))
		for _, k := range keys {
			ordered[k] = view.Other[k]
		}
		view.Other = ordered
	}

	return view, nil
}

// SettingsBundle is what the REST endpoint returns: project + user views
// side by side. Either may be empty / non-existent independently.
type SettingsBundle struct {
	Project SettingsView `json:"project"`
	User    SettingsView `json:"user"`
}

// loadSettingsBundle is the "give me everything for this branch" call
// used by GET …/tabs/claude/settings.
func loadSettingsBundle(worktree string) (SettingsBundle, error) {
	proj, err := loadSettingsView(scopeProject, worktree)
	if err != nil {
		return SettingsBundle{}, err
	}
	user, err := loadSettingsView(scopeUser, worktree)
	if err != nil {
		return SettingsBundle{}, err
	}
	return SettingsBundle{Project: proj, User: user}, nil
}

// stringSlice coerces an `any` (typically a json-decoded []any) into
// []string, dropping non-string entries. Returns nil for nil input.
func stringSlice(v any) []string {
	arr, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(arr))
	for _, e := range arr {
		if s, ok := e.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
