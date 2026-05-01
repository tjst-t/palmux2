package claudeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// writeJSONFile is a small test helper that writes a .json document with
// a trailing newline so the parser hits the same path as a hand-edited file.
func writeJSONFile(t *testing.T, path string, doc any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, append(body, '\n'), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLoadSettingsView_MissingFile(t *testing.T) {
	wt := t.TempDir()
	v, err := loadSettingsView(scopeProject, wt)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if v.Exists {
		t.Fatalf("Exists = true, want false for missing file")
	}
	if want := filepath.Join(wt, ".claude", "settings.json"); v.Path != want {
		t.Fatalf("Path = %q, want %q", v.Path, want)
	}
	if len(v.PermissionsAllow) != 0 {
		t.Fatalf("PermissionsAllow = %v, want empty", v.PermissionsAllow)
	}
}

func TestLoadSettingsView_AllSections(t *testing.T) {
	wt := t.TempDir()
	path := filepath.Join(wt, ".claude", "settings.json")
	writeJSONFile(t, path, map[string]any{
		"permissions": map[string]any{
			"allow": []any{"Bash(ls)", "Read", "Write(/tmp/foo)"},
			"deny":  []any{"Bash(rm -rf *)"},
			"ask":   []any{"WebFetch"},
		},
		"hooks": map[string]any{
			"PreToolUse": []any{
				map[string]any{"matcher": "Bash", "hooks": []any{"echo before"}},
			},
		},
		"model":               "claude-opus-4-7",
		"includeCoAuthoredBy": false,
	})
	v, err := loadSettingsView(scopeProject, wt)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !v.Exists {
		t.Fatalf("Exists = false, want true")
	}
	if v.ParseError != "" {
		t.Fatalf("ParseError = %q, want empty", v.ParseError)
	}
	if got, want := v.PermissionsAllow, []string{"Bash(ls)", "Read", "Write(/tmp/foo)"}; !equalStringSlice(got, want) {
		t.Fatalf("PermissionsAllow = %v, want %v", got, want)
	}
	if got, want := v.PermissionsDeny, []string{"Bash(rm -rf *)"}; !equalStringSlice(got, want) {
		t.Fatalf("PermissionsDeny = %v, want %v", got, want)
	}
	if len(v.Hooks) == 0 {
		t.Fatalf("Hooks empty; want non-empty raw JSON")
	}
	if _, ok := v.Other["model"]; !ok {
		t.Fatalf("Other missing model, got keys: %v", keysOf(v.Other))
	}
	if _, ok := v.Other["includeCoAuthoredBy"]; !ok {
		t.Fatalf("Other missing includeCoAuthoredBy")
	}
	if _, ok := v.Other["permissions(other)"]; !ok {
		t.Fatalf("Other missing permissions(other) bucket for ask")
	}
}

func TestLoadSettingsView_ParseError(t *testing.T) {
	wt := t.TempDir()
	path := filepath.Join(wt, ".claude", "settings.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte("not json {"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	v, err := loadSettingsView(scopeProject, wt)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if !v.Exists {
		t.Fatalf("Exists = false, want true")
	}
	if v.ParseError == "" {
		t.Fatalf("ParseError empty, want populated")
	}
}

func TestLoadSettingsBundle_UserAndProject(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	wt := t.TempDir()
	writeJSONFile(t, filepath.Join(wt, ".claude", "settings.json"), map[string]any{
		"permissions": map[string]any{"allow": []any{"Bash(ls)"}},
	})
	writeJSONFile(t, filepath.Join(home, ".claude", "settings.json"), map[string]any{
		"permissions": map[string]any{"allow": []any{"Read", "WebFetch"}},
	})
	bundle, err := loadSettingsBundle(wt)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got, want := bundle.Project.PermissionsAllow, []string{"Bash(ls)"}; !equalStringSlice(got, want) {
		t.Fatalf("project = %v, want %v", got, want)
	}
	if got, want := bundle.User.PermissionsAllow, []string{"Read", "WebFetch"}; !equalStringSlice(got, want) {
		t.Fatalf("user = %v, want %v", got, want)
	}
}

func TestRemoveFromAllowList_Project(t *testing.T) {
	wt := t.TempDir()
	path := filepath.Join(wt, ".claude", "settings.json")
	writeJSONFile(t, path, map[string]any{
		"permissions": map[string]any{
			"allow": []any{"Bash(ls)", "Read", "Write(/tmp/foo)"},
		},
		"model": "claude-opus-4-7",
	})
	if err := removeFromAllowList(scopeProject, wt, "Read"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	v, err := loadSettingsView(scopeProject, wt)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := v.PermissionsAllow, []string{"Bash(ls)", "Write(/tmp/foo)"}; !equalStringSlice(got, want) {
		t.Fatalf("after remove = %v, want %v", got, want)
	}
	// Other top-level keys untouched.
	if _, ok := v.Other["model"]; !ok {
		t.Fatalf("model key dropped during remove; %v", keysOf(v.Other))
	}
}

func TestRemoveFromAllowList_User(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".claude", "settings.json")
	writeJSONFile(t, path, map[string]any{
		"permissions": map[string]any{"allow": []any{"Read", "WebFetch"}},
	})
	if err := removeFromAllowList(scopeUser, "", "WebFetch"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	v, err := loadSettingsView(scopeUser, "")
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got, want := v.PermissionsAllow, []string{"Read"}; !equalStringSlice(got, want) {
		t.Fatalf("after remove = %v, want %v", got, want)
	}
}

func TestRemoveFromAllowList_Idempotent(t *testing.T) {
	wt := t.TempDir()
	path := filepath.Join(wt, ".claude", "settings.json")
	writeJSONFile(t, path, map[string]any{
		"permissions": map[string]any{"allow": []any{"Read"}},
	})
	if err := removeFromAllowList(scopeProject, wt, "NotPresent"); err != nil {
		t.Fatalf("remove non-existent: %v", err)
	}
	v, _ := loadSettingsView(scopeProject, wt)
	if got, want := v.PermissionsAllow, []string{"Read"}; !equalStringSlice(got, want) {
		t.Fatalf("modified despite no-op: got %v, want %v", got, want)
	}
}

func TestRemoveFromAllowList_MissingFile(t *testing.T) {
	wt := t.TempDir()
	if err := removeFromAllowList(scopeProject, wt, "Read"); err != nil {
		t.Fatalf("remove on missing file: %v", err)
	}
}

func TestRemoveFromAllowList_RejectsEmptyPattern(t *testing.T) {
	wt := t.TempDir()
	if err := removeFromAllowList(scopeProject, wt, ""); err == nil {
		t.Fatalf("want error on empty pattern, got nil")
	}
}

// AddThenRemove rebuilds the round-trip the UI exercises: Always-allow
// adds an entry, then the Settings popup removes it.
func TestAddThenRemove_RoundTrip(t *testing.T) {
	wt := t.TempDir()
	if err := addToProjectAllowList(wt, "Bash(ls)"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := addToProjectAllowList(wt, "Read"); err != nil {
		t.Fatalf("add 2: %v", err)
	}
	if err := removeFromAllowList(scopeProject, wt, "Bash(ls)"); err != nil {
		t.Fatalf("remove: %v", err)
	}
	v, _ := loadSettingsView(scopeProject, wt)
	if got, want := v.PermissionsAllow, []string{"Read"}; !equalStringSlice(got, want) {
		t.Fatalf("round-trip = %v, want %v", got, want)
	}
}

func equalStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func keysOf(m map[string]json.RawMessage) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
