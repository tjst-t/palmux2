package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// ptr returns a pointer to a runtime.Config for test convenience.
func ptr(cfg runtime.Config) *runtime.Config { return &cfg }

// --- AC-S8478ca-3-2: priority chain ---

// TestResolveWorkspaceRuntime_PriorityChain verifies that per-Workspace wins
// over per-repo, which wins over the global default, which wins over host
// fallback.  [AC-S8478ca-3-2]
func TestResolveWorkspaceRuntime_PriorityChain(t *testing.T) {
	dir := t.TempDir()
	rs, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	if _, err := rs.Add(RepoEntry{ID: "repo1", GHQPath: "github.com/a/r1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	incus := runtime.Config{Kind: runtime.KindIncusContainer}
	host := runtime.Config{Kind: runtime.KindHost}

	// Case 1: per-Workspace wins over everything.
	if err := rs.SetWorkspaceRuntime("repo1", "ws1", ptr(incus)); err != nil {
		t.Fatalf("SetWorkspaceRuntime: %v", err)
	}
	if err := rs.SetRepoDefaultRuntime("repo1", ptr(host)); err != nil {
		t.Fatalf("SetRepoDefaultRuntime: %v", err)
	}
	got := rs.ResolveWorkspaceRuntime("repo1", "ws1", host)
	if got.Kind != runtime.KindIncusContainer {
		t.Errorf("[AC-S8478ca-3-2] case1: want incus-container, got %q", got.Kind)
	}

	// Case 2: clear per-Workspace → per-repo wins.
	if err := rs.SetWorkspaceRuntime("repo1", "ws1", nil); err != nil {
		t.Fatalf("SetWorkspaceRuntime(nil): %v", err)
	}
	got = rs.ResolveWorkspaceRuntime("repo1", "ws1", incus)
	if got.Kind != runtime.KindHost {
		t.Errorf("[AC-S8478ca-3-2] case2: want host (per-repo), got %q", got.Kind)
	}

	// Case 3: clear per-repo → global wins.
	if err := rs.SetRepoDefaultRuntime("repo1", nil); err != nil {
		t.Fatalf("SetRepoDefaultRuntime(nil): %v", err)
	}
	got = rs.ResolveWorkspaceRuntime("repo1", "ws1", incus)
	if got.Kind != runtime.KindIncusContainer {
		t.Errorf("[AC-S8478ca-3-2] case3: want incus-container (global), got %q", got.Kind)
	}

	// Case 4: global is zero → host fallback.
	got = rs.ResolveWorkspaceRuntime("repo1", "ws1", runtime.Config{})
	if got.Kind != runtime.KindHost {
		t.Errorf("[AC-S8478ca-3-2] case4: want host (fallback), got %q", got.Kind)
	}
}

// TestResolveWorkspaceRuntime_UnsetEverywhere verifies that an unset chain
// resolves to host.  [AC-S8478ca-3-2]
func TestResolveWorkspaceRuntime_UnsetEverywhere(t *testing.T) {
	dir := t.TempDir()
	rs, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	if _, err := rs.Add(RepoEntry{ID: "repo1", GHQPath: "github.com/a/r1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	// Nothing set — pass zero global.
	got := rs.ResolveWorkspaceRuntime("repo1", "ws1", runtime.Config{})
	if got.Kind != runtime.KindHost {
		t.Errorf("[AC-S8478ca-3-2] unset: want host, got %q", got.Kind)
	}
}

// TestResolveWorkspaceRuntime_UnknownRepo verifies that an unknown repo
// resolves to host (safe default).  [AC-S8478ca-3-2]
func TestResolveWorkspaceRuntime_UnknownRepo(t *testing.T) {
	dir := t.TempDir()
	rs, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	got := rs.ResolveWorkspaceRuntime("unknown", "ws1", runtime.Config{})
	if got.Kind != runtime.KindHost {
		t.Errorf("[AC-S8478ca-3-2] unknown repo: want host, got %q", got.Kind)
	}
}

// --- AC-S8478ca-3-1: persistence round-trip + invalid kind rejection ---

// TestSetWorkspaceRuntime_RoundTrip verifies that the per-Workspace override
// survives save→reload.  [AC-S8478ca-3-1]
func TestSetWorkspaceRuntime_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	rs, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	if _, err := rs.Add(RepoEntry{ID: "repo1", GHQPath: "github.com/a/r1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	want := runtime.Config{Kind: runtime.KindIncusContainer, Image: "ubuntu:24.04"}
	if err := rs.SetWorkspaceRuntime("repo1", "ws1", &want); err != nil {
		t.Fatalf("SetWorkspaceRuntime: %v", err)
	}

	// Reload from disk.
	rs2, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore(reload): %v", err)
	}
	got := rs2.ResolveWorkspaceRuntime("repo1", "ws1", runtime.Config{})
	if got.Kind != want.Kind || got.Image != want.Image {
		t.Errorf("[AC-S8478ca-3-1] round-trip: got %+v, want %+v", got, want)
	}
}

// TestSetRepoDefaultRuntime_RoundTrip verifies that the per-repo default
// survives save→reload.  [AC-S8478ca-3-1]
func TestSetRepoDefaultRuntime_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	rs, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	if _, err := rs.Add(RepoEntry{ID: "repo1", GHQPath: "github.com/a/r1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	want := runtime.Config{Kind: runtime.KindIncusContainer}
	if err := rs.SetRepoDefaultRuntime("repo1", &want); err != nil {
		t.Fatalf("SetRepoDefaultRuntime: %v", err)
	}

	rs2, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore(reload): %v", err)
	}
	got := rs2.ResolveWorkspaceRuntime("repo1", "ws-no-override", runtime.Config{})
	if got.Kind != want.Kind {
		t.Errorf("[AC-S8478ca-3-1] per-repo round-trip: got %q, want %q", got.Kind, want.Kind)
	}
}

// TestSetWorkspaceRuntime_InvalidKind verifies that an invalid kind is
// rejected and not persisted.  [AC-S8478ca-3-1]
func TestSetWorkspaceRuntime_InvalidKind(t *testing.T) {
	dir := t.TempDir()
	rs, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	if _, err := rs.Add(RepoEntry{ID: "repo1", GHQPath: "github.com/a/r1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	bad := runtime.Config{Kind: "docker"}
	if err := rs.SetWorkspaceRuntime("repo1", "ws1", &bad); err == nil {
		t.Error("[AC-S8478ca-3-1] expected error for invalid kind, got nil")
	}

	// Must not persist — reload should return host.
	rs2, _ := NewRepoStore(dir)
	got := rs2.ResolveWorkspaceRuntime("repo1", "ws1", runtime.Config{})
	if got.Kind != runtime.KindHost {
		t.Errorf("[AC-S8478ca-3-1] after invalid set: want host, got %q", got.Kind)
	}
}

// TestSetRepoDefaultRuntime_InvalidKind verifies rejection.  [AC-S8478ca-3-1]
func TestSetRepoDefaultRuntime_InvalidKind(t *testing.T) {
	dir := t.TempDir()
	rs, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	if _, err := rs.Add(RepoEntry{ID: "repo1", GHQPath: "github.com/a/r1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}

	if err := rs.SetRepoDefaultRuntime("repo1", &runtime.Config{Kind: "vm"}); err == nil {
		t.Error("[AC-S8478ca-3-1] expected error for invalid kind, got nil")
	}
}

// TestSettingsStore_DefaultRuntime_RoundTrip verifies that the global
// defaultRuntime field in settings.json survives save→reload.
// [AC-S8478ca-3-1]
func TestSettingsStore_DefaultRuntime_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	ss, err := NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("NewSettingsStore: %v", err)
	}

	// Initially unset.
	if got := ss.DefaultRuntime(); got.Kind != "" {
		t.Errorf("[AC-S8478ca-3-1] initial: want empty, got %q", got.Kind)
	}

	// Set via Patch.
	_, err = ss.Patch(Settings{DefaultRuntime: ptr(runtime.Config{Kind: runtime.KindIncusContainer})})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}

	// Reload.
	ss2, err := NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("NewSettingsStore(reload): %v", err)
	}
	got := ss2.DefaultRuntime()
	if got.Kind != runtime.KindIncusContainer {
		t.Errorf("[AC-S8478ca-3-1] after reload: want incus-container, got %q", got.Kind)
	}
}

// TestSettingsStore_DefaultRuntime_InvalidKind verifies that an invalid kind
// in a PATCH is rejected.  [AC-S8478ca-3-1]
func TestSettingsStore_DefaultRuntime_InvalidKind(t *testing.T) {
	dir := t.TempDir()
	ss, _ := NewSettingsStore(dir)

	_, err := ss.Patch(Settings{DefaultRuntime: ptr(runtime.Config{Kind: "nspawn"})})
	if err == nil {
		t.Error("[AC-S8478ca-3-1] expected error for invalid kind, got nil")
	}

	// Must not persist.
	ss2, _ := NewSettingsStore(dir)
	got := ss2.DefaultRuntime()
	if got.Kind != "" {
		t.Errorf("[AC-S8478ca-3-1] after rejected patch: want empty, got %q", got.Kind)
	}
}

// TestSettingsStore_DefaultRuntime_Clear verifies that PATCH {kind:""} clears
// the field.  [AC-S8478ca-3-1]
func TestSettingsStore_DefaultRuntime_Clear(t *testing.T) {
	dir := t.TempDir()
	ss, _ := NewSettingsStore(dir)

	// Set, then clear.
	if _, err := ss.Patch(Settings{DefaultRuntime: ptr(runtime.Config{Kind: runtime.KindIncusContainer})}); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Pass a non-nil pointer with empty Kind to clear.
	if _, err := ss.Patch(Settings{DefaultRuntime: &runtime.Config{}}); err != nil {
		t.Fatalf("clear: %v", err)
	}

	ss2, _ := NewSettingsStore(dir)
	got := ss2.DefaultRuntime()
	if got.Kind != "" {
		t.Errorf("[AC-S8478ca-3-1] after clear: want empty, got %q", got.Kind)
	}
}

// --- AC-S8478ca-3-3: backward-compat with repos.json without runtime fields ---

// TestBackwardCompat_NoRuntimeField verifies that a repos.json entry written
// WITHOUT runtime fields resolves to host.  [AC-S8478ca-3-3]
func TestBackwardCompat_NoRuntimeField(t *testing.T) {
	dir := t.TempDir()

	// Write a minimal repos.json that has no runtime keys — simulating a
	// file created by a pre-S8478ca palmux version.
	legacy := `[{"id":"r1","ghqPath":"github.com/a/r1","starred":false}]`
	if err := os.WriteFile(filepath.Join(dir, "repos.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	rs, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}

	got := rs.ResolveWorkspaceRuntime("r1", "ws1", runtime.Config{})
	if got.Kind != runtime.KindHost {
		t.Errorf("[AC-S8478ca-3-3] legacy entry: want host, got %q", got.Kind)
	}
}

// TestBackwardCompat_BranchSettingsNoRuntime verifies that a branchSettings
// entry without a runtime field resolves through to host.  [AC-S8478ca-3-3]
func TestBackwardCompat_BranchSettingsNoRuntime(t *testing.T) {
	dir := t.TempDir()

	// repos.json with a branchSettings entry that has no runtime field.
	legacy := `[{
		"id":"r1","ghqPath":"github.com/a/r1","starred":false,
		"branchSettings":{"ws1":{"tab_claude_modes":{"claude":"tui"}}}
	}]`
	if err := os.WriteFile(filepath.Join(dir, "repos.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	rs, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}

	got := rs.ResolveWorkspaceRuntime("r1", "ws1", runtime.Config{})
	if got.Kind != runtime.KindHost {
		t.Errorf("[AC-S8478ca-3-3] legacy branchSettings: want host, got %q", got.Kind)
	}
}

// TestBackwardCompat_SettingsNoRuntime verifies that a settings.json written
// without defaultRuntime resolves to zero (caller falls through to host).
// [AC-S8478ca-3-3]
func TestBackwardCompat_SettingsNoRuntime(t *testing.T) {
	dir := t.TempDir()

	legacy := `{"branchSortOrder":"name","attachmentUploadDir":"/tmp/palmux-uploads/"}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(legacy), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	ss, err := NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("NewSettingsStore: %v", err)
	}

	got := ss.DefaultRuntime()
	if got.Kind != "" {
		t.Errorf("[AC-S8478ca-3-3] legacy settings: want empty, got %q", got.Kind)
	}
}

// TestRepoEntry_RuntimeJSON verifies the JSON round-trip of RepoEntry with
// runtime fields — an integration sanity check.  [AC-S8478ca-3-1]
func TestRepoEntry_RuntimeJSON(t *testing.T) {
	e := RepoEntry{
		ID:             "r1",
		GHQPath:        "github.com/a/r1",
		DefaultRuntime: &runtime.Config{Kind: runtime.KindIncusContainer, Image: "ubuntu:24.04"},
		BranchSettingsMap: map[string]BranchSettings{
			"ws1": {
				Runtime: &runtime.Config{Kind: runtime.KindHost},
			},
		},
	}
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var e2 RepoEntry
	if err := json.Unmarshal(b, &e2); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if e2.DefaultRuntime == nil || e2.DefaultRuntime.Kind != runtime.KindIncusContainer {
		t.Errorf("DefaultRuntime round-trip: got %+v", e2.DefaultRuntime)
	}
	if ws := e2.BranchSettingsMap["ws1"]; ws.Runtime == nil || ws.Runtime.Kind != runtime.KindHost {
		t.Errorf("BranchSettings.Runtime round-trip: got %+v", ws.Runtime)
	}
}
