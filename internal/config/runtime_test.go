package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// TestLoadLegacyReposJSONNoRuntimeField covers AC-Sdd4ce1-1-2 / AC-Sdd4ce1-7-1:
// a repos.json that pre-dates Phase A (no `defaultRuntime` field) loads
// successfully and BranchRuntime / DefaultRuntime / ResolveBranchRuntime
// behave as if all entries were host.
//
// [AC-Sdd4ce1-1-2] [AC-Sdd4ce1-7-1]
func TestLoadLegacyReposJSONNoRuntimeField(t *testing.T) {
	dir := t.TempDir()
	// Hand-craft a pre-Phase-A repos.json (just id+ghqPath+userOpenedBranches).
	legacy := `[
		{
			"id": "tjst-t--palmux2--a1b2",
			"ghqPath": "github.com/tjst-t/palmux2",
			"userOpenedBranches": ["main", "feature-x"]
		}
	]`
	if err := os.WriteFile(filepath.Join(dir, "repos.json"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("seed repos.json: %v", err)
	}
	s, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	all := s.All()
	if len(all) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(all))
	}
	if all[0].ID != "tjst-t--palmux2--a1b2" {
		t.Errorf("ID = %q", all[0].ID)
	}
	if all[0].DefaultRuntime != nil {
		t.Errorf("DefaultRuntime = %+v, want nil for legacy file", all[0].DefaultRuntime)
	}
	if all[0].BranchRuntimes != nil {
		t.Errorf("BranchRuntimes = %+v, want nil", all[0].BranchRuntimes)
	}

	// ResolveBranchRuntime walks the priority chain and falls back to host.
	got := s.ResolveBranchRuntime("tjst-t--palmux2--a1b2", "main", runtime.Config{})
	if got.Kind != runtime.KindHost {
		t.Errorf("resolved Kind = %q, want %q (legacy host fallback)", got.Kind, runtime.KindHost)
	}
}

// TestLegacyReposJSONStableSerialization covers AC-Sdd4ce1-7-1: loading and
// re-saving a legacy repos.json must NOT introduce surprising new fields
// when the user hasn't touched runtime config. Empty maps stay omitted.
//
// [AC-Sdd4ce1-7-1]
func TestLegacyReposJSONStableSerialization(t *testing.T) {
	dir := t.TempDir()
	legacy := `[{"id":"r1","ghqPath":"github.com/a/r1","userOpenedBranches":["main"]}]`
	if err := os.WriteFile(filepath.Join(dir, "repos.json"), []byte(legacy), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	// Trigger a save by toggling starred (no runtime change).
	if _, err := s.SetStarred("r1", true); err != nil {
		t.Fatalf("SetStarred: %v", err)
	}
	out, err := os.ReadFile(filepath.Join(dir, "repos.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var raw []map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("re-parse: %v", err)
	}
	if _, present := raw[0]["defaultRuntime"]; present {
		t.Errorf("re-saved repos.json contains defaultRuntime field for legacy entry: %s", out)
	}
	if _, present := raw[0]["branchRuntimes"]; present {
		t.Errorf("re-saved repos.json contains branchRuntimes field for legacy entry: %s", out)
	}
}

// TestSetDefaultRuntimePersists covers AC-Sdd4ce1-6-3: per-repo default is
// persisted and survives a reload.
//
// [AC-Sdd4ce1-6-3]
func TestSetDefaultRuntimePersists(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewRepoStore(dir)
	if _, err := s.Add(RepoEntry{ID: "r1", GHQPath: "github.com/a/r1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	cfg := &runtime.Config{
		Kind:    runtime.KindLXDContainer,
		Image:   "ghcr.io/tjst-t/palmux-workspace:default",
		Network: runtime.NetworkPolicy{Mode: "bridged"},
	}
	changed, err := s.SetDefaultRuntime("r1", cfg)
	if err != nil || !changed {
		t.Fatalf("SetDefaultRuntime: changed=%v err=%v", changed, err)
	}
	// Idempotent: re-applying the same config returns changed=false.
	again, err := s.SetDefaultRuntime("r1", cfg)
	if err != nil {
		t.Fatalf("SetDefaultRuntime idempotent: %v", err)
	}
	if again {
		t.Errorf("idempotent SetDefaultRuntime: changed=true, want false")
	}
	// Reload — must round-trip.
	s2, _ := NewRepoStore(dir)
	loaded := s2.DefaultRuntime("r1")
	if loaded == nil || loaded.Kind != runtime.KindLXDContainer || loaded.Image != cfg.Image || loaded.Network.Mode != "bridged" {
		t.Errorf("reload DefaultRuntime = %+v, want %+v", loaded, cfg)
	}
}

// TestSetBranchRuntimePersists covers AC-Sdd4ce1-6-1: per-Workspace override
// is keyed by branch name and persisted across reloads.
//
// [AC-Sdd4ce1-6-1]
func TestSetBranchRuntimePersists(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewRepoStore(dir)
	if _, err := s.Add(RepoEntry{ID: "r1", GHQPath: "github.com/a/r1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	cfg := &runtime.Config{Kind: runtime.KindLXDVM, Image: "ghcr.io/tjst-t/palmux-workspace:gpu"}
	changed, err := s.SetBranchRuntime("r1", "feature-x", cfg)
	if err != nil || !changed {
		t.Fatalf("SetBranchRuntime: changed=%v err=%v", changed, err)
	}
	// Reload.
	s2, _ := NewRepoStore(dir)
	got := s2.BranchRuntime("r1", "feature-x")
	if got == nil || *got != *cfg {
		t.Errorf("reload BranchRuntime = %+v, want %+v", got, cfg)
	}
	// Clear by passing nil.
	if _, err := s2.SetBranchRuntime("r1", "feature-x", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got := s2.BranchRuntime("r1", "feature-x"); got != nil {
		t.Errorf("after clear: BranchRuntime = %+v, want nil", got)
	}
}

// TestResolveBranchRuntimePriority covers the §9.6 priority chain:
//
//	per-Workspace → per-repo → global → host fallback
//
// [AC-Sdd4ce1-6-1]
func TestResolveBranchRuntimePriority(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewRepoStore(dir)
	if _, err := s.Add(RepoEntry{ID: "r1", GHQPath: "github.com/a/r1"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// Layer 4: pure host fallback (everything empty).
	got := s.ResolveBranchRuntime("r1", "main", runtime.Config{})
	if got.Kind != runtime.KindHost {
		t.Errorf("all-empty: Kind = %q, want %q", got.Kind, runtime.KindHost)
	}
	// Layer 3: global default.
	global := runtime.Config{Kind: runtime.KindLXDContainer, Image: "global-img"}
	got = s.ResolveBranchRuntime("r1", "main", global)
	if got.Kind != runtime.KindLXDContainer || got.Image != "global-img" {
		t.Errorf("global: %+v", got)
	}
	// Layer 2: per-repo.
	if _, err := s.SetDefaultRuntime("r1", &runtime.Config{Kind: runtime.KindLXDVM, Image: "repo-img"}); err != nil {
		t.Fatalf("SetDefaultRuntime: %v", err)
	}
	got = s.ResolveBranchRuntime("r1", "main", global)
	if got.Kind != runtime.KindLXDVM {
		t.Errorf("per-repo: Kind = %q, want %q", got.Kind, runtime.KindLXDVM)
	}
	if got.Image != "repo-img" {
		t.Errorf("per-repo: Image = %q, want %q", got.Image, "repo-img")
	}
	// Layer 1: per-Workspace.
	if _, err := s.SetBranchRuntime("r1", "main", &runtime.Config{Kind: runtime.KindLXDRemote, Remote: "gpu-box"}); err != nil {
		t.Fatalf("SetBranchRuntime: %v", err)
	}
	got = s.ResolveBranchRuntime("r1", "main", global)
	if got.Kind != runtime.KindLXDRemote {
		t.Errorf("per-WS: Kind = %q, want %q", got.Kind, runtime.KindLXDRemote)
	}
	if got.Remote != "gpu-box" {
		t.Errorf("per-WS: Remote = %q, want %q", got.Remote, "gpu-box")
	}
	// per-repo Image fills the hole left by per-WS (it didn't set Image).
	if got.Image != "repo-img" {
		t.Errorf("per-WS resolved Image = %q, want per-repo %q (priority hole-fill)", got.Image, "repo-img")
	}
}

// TestSettingsDefaultRuntime covers AC-Sdd4ce1-6-2.
//
// [AC-Sdd4ce1-6-2]
func TestSettingsDefaultRuntime(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewSettingsStore(dir)
	if got := s.DefaultRuntime(); got.Kind != "" {
		t.Errorf("initial DefaultRuntime = %+v, want zero", got)
	}
	cfg := &runtime.Config{
		Kind:  runtime.KindLXDContainer,
		Image: "ghcr.io/tjst-t/palmux-workspace:default",
	}
	if _, err := s.Patch(Settings{DefaultRuntime: cfg}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	// Reload.
	s2, _ := NewSettingsStore(dir)
	got := s2.DefaultRuntime()
	if got.Kind != runtime.KindLXDContainer || got.Image != cfg.Image {
		t.Errorf("reload DefaultRuntime = %+v, want %+v", got, cfg)
	}
	// Clear via {kind:""}.
	if _, err := s2.Patch(Settings{DefaultRuntime: &runtime.Config{}}); err != nil {
		t.Fatalf("clear Patch: %v", err)
	}
	if got := s2.DefaultRuntime(); got.Kind != "" {
		t.Errorf("after clear: %+v", got)
	}
}

// TestSettingsDefaultRuntimeRejectsInvalidKind covers a pure validation
// path: a Patch with an unknown kind must error rather than silently
// persisting bad data.
func TestSettingsDefaultRuntimeRejectsInvalidKind(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewSettingsStore(dir)
	if _, err := s.Patch(Settings{DefaultRuntime: &runtime.Config{Kind: "podman"}}); err == nil {
		t.Errorf("expected error for invalid kind, got nil")
	}
}
