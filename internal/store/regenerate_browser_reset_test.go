package store

// Unit test for the container-regenerate → tab reset wiring (S52fc2c-6).
//
// RegenerateBranchContainer must invoke the optional tab.RuntimeRestartHook on
// every provider after the container is recreated, so a stateful tab (e.g. the
// Browser tab's per-workspace Manager) can drop its connection to the destroyed
// container. This test registers a recording provider that stands in for the
// Browser provider and asserts it was reset for the regenerated branch.
//
// [AC-S52fc2c-6-1] [AC-S52fc2c-6-2]

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tab/bash"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// regenerableRuntime embeds fakeRuntime and adds the ContainerRegenerator
// capability so RegenerateBranchContainer proceeds past its type-assert.
type regenerableRuntime struct {
	*fakeRuntime
	mu         sync.Mutex
	regenCalls int
	regenErr   error
}

func (r *regenerableRuntime) Regenerate(_ context.Context) error {
	r.mu.Lock()
	r.regenCalls++
	r.mu.Unlock()
	return r.regenErr
}

// recordingResetProvider is a terminal-style provider that records which
// branches had their RuntimeRestartHook fired. It stands in for the Browser
// provider so the store→hook wiring can be asserted without an incus container.
type recordingResetProvider struct {
	mu       sync.Mutex
	resetIDs []string
}

func (*recordingResetProvider) Type() string          { return "recording-reset" }
func (*recordingResetProvider) DisplayName() string   { return "Recording Reset" }
func (*recordingResetProvider) Protected() bool       { return false }
func (*recordingResetProvider) Multiple() bool        { return false }
func (*recordingResetProvider) NeedsTmuxWindow() bool { return false }
func (*recordingResetProvider) Conditional() bool     { return false }
func (*recordingResetProvider) Limits(_ tab.SettingsView) tab.InstanceLimits {
	return tab.InstanceLimits{Min: 1, Max: 1}
}
func (*recordingResetProvider) OnBranchOpen(_ context.Context, _ tab.OpenParams) (tab.ProviderResult, error) {
	return tab.ProviderResult{}, nil
}
func (*recordingResetProvider) OnBranchClose(_ context.Context, _ tab.CloseParams) error { return nil }
func (*recordingResetProvider) RegisterRoutes(_ *http.ServeMux, _ string)                {}

// OnBranchRuntimeRestarted implements tab.RuntimeRestartHook.
func (p *recordingResetProvider) OnBranchRuntimeRestarted(_ context.Context, params tab.CloseParams) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if params.Branch != nil {
		p.resetIDs = append(p.resetIDs, params.Branch.ID)
	}
	return nil
}

func (p *recordingResetProvider) recorded() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, len(p.resetIDs))
	copy(out, p.resetIDs)
	return out
}

// newStoreWithProviders builds a store wired to a fake registry and the given
// extra providers (in addition to the fake terminal + bash defaults).
func newStoreWithProviders(t *testing.T, reg *fakeRegistry, extra ...tab.Provider) (*Store, *tmux.MockClient) {
	t.Helper()
	dir := t.TempDir()
	repoStore, err := config.NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	settings, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("NewSettingsStore: %v", err)
	}
	registry := tab.NewRegistry()
	registry.Register(fakeTerminalProvider{})
	registry.Register(bash.New())
	for _, p := range extra {
		registry.Register(p)
	}

	mockTmux := tmux.NewMockClient()
	s, err := New(Deps{
		Tmux:            mockTmux,
		RepoStore:       repoStore,
		Settings:        settings,
		Registry:        registry,
		GHQRoot:         dir,
		RuntimeRegistry: reg,
	})
	if err != nil {
		t.Fatalf("New store: %v", err)
	}
	return s, mockTmux
}

// TestRegenerateBranchContainer_InvokesResetHook verifies the full wiring:
// RegenerateBranchContainer recreates the container (regenerable runtime) and
// then fires RuntimeRestartHook.OnBranchRuntimeRestarted for the regenerated
// branch — the seam the Browser provider hangs its Manager reset on.
func TestRegenerateBranchContainer_InvokesResetHook(t *testing.T) {
	reg := newFakeRegistry()
	rec := &recordingResetProvider{}
	s, mockTmux := newStoreWithProviders(t, reg, rec)

	repoID := "test-repo--b0b0"
	repoDir := t.TempDir()
	if _, err := s.deps.RepoStore.Add(config.RepoEntry{ID: repoID, GHQPath: "test/repo-b0b0"}); err != nil {
		t.Fatalf("RepoStore.Add: %v", err)
	}
	branchID := injectBranch(t, s, repoID, repoDir, "main", true)
	// Derive the session name the same way injectBranch does, without reading
	// s.repos (which would require holding s.mu).
	mockTmux.SeedSession(domain.SessionName(repoID, branchID))

	// Registry returns an incus runtime that supports regeneration.
	rt := &regenerableRuntime{fakeRuntime: &fakeRuntime{kind: runtime.KindIncusContainer, tc: mockTmux}}
	reg.set(repoID, branchID, rt)

	regenerated, err := s.RegenerateBranchContainer(context.Background(), repoID, branchID)
	if err != nil {
		t.Fatalf("RegenerateBranchContainer: %v", err)
	}
	if !regenerated {
		t.Fatal("expected regenerated=true")
	}
	if rt.regenCalls != 1 {
		t.Errorf("expected Regenerate called once, got %d", rt.regenCalls)
	}

	got := rec.recorded()
	if len(got) != 1 || got[0] != branchID {
		t.Errorf("RuntimeRestartHook should fire once for %q, got %v", branchID, got)
	}
}

// TestRegenerateBranchContainer_HostNoOp verifies that for a host runtime (no
// ContainerRegenerator) regeneration is a no-op and the reset hook does NOT
// fire — the host-safety guarantee. [AC-S52fc2c-6-1]
func TestRegenerateBranchContainer_HostNoOp(t *testing.T) {
	reg := newFakeRegistry()
	rec := &recordingResetProvider{}
	s, _ := newStoreWithProviders(t, reg, rec)

	repoID := "test-repo--c1c1"
	repoDir := t.TempDir()
	if _, err := s.deps.RepoStore.Add(config.RepoEntry{ID: repoID, GHQPath: "test/repo-c1c1"}); err != nil {
		t.Fatalf("RepoStore.Add: %v", err)
	}
	branchID := injectBranch(t, s, repoID, repoDir, "main", true)

	// Host runtime: does NOT implement ContainerRegenerator.
	reg.set(repoID, branchID, &fakeRuntime{kind: runtime.KindHost})

	regenerated, err := s.RegenerateBranchContainer(context.Background(), repoID, branchID)
	if err != nil {
		t.Fatalf("RegenerateBranchContainer (host): %v", err)
	}
	if regenerated {
		t.Error("expected regenerated=false for host runtime")
	}
	if got := rec.recorded(); len(got) != 0 {
		t.Errorf("reset hook must NOT fire for host runtime, got %v", got)
	}
}

// TestRegenerateBranchContainer_FailedRegenerateNoReset verifies the
// transactional invariant: when Regenerate fails (e.g. the probe couldn't
// launch the new image), RegenerateBranchContainer returns (false, err) BEFORE
// reaching restartBranchTabDaemons, so the reset hook does NOT fire and the
// stale Manager is left intact (the old container is still the live one).
// (branch.go: early return on regenErr precedes the hook.) [AC-S52fc2c-6-1]
func TestRegenerateBranchContainer_FailedRegenerateNoReset(t *testing.T) {
	reg := newFakeRegistry()
	rec := &recordingResetProvider{}
	s, mockTmux := newStoreWithProviders(t, reg, rec)

	repoID := "test-repo--d2d2"
	repoDir := t.TempDir()
	if _, err := s.deps.RepoStore.Add(config.RepoEntry{ID: repoID, GHQPath: "test/repo-d2d2"}); err != nil {
		t.Fatalf("RepoStore.Add: %v", err)
	}
	branchID := injectBranch(t, s, repoID, repoDir, "main", true)
	mockTmux.SeedSession(domain.SessionName(repoID, branchID))

	// Incus runtime whose Regenerate FAILS (probe couldn't launch the new image).
	rt := &regenerableRuntime{
		fakeRuntime: &fakeRuntime{kind: runtime.KindIncusContainer, tc: mockTmux},
		regenErr:    errors.New("probe failed: image not found"),
	}
	reg.set(repoID, branchID, rt)

	regenerated, err := s.RegenerateBranchContainer(context.Background(), repoID, branchID)
	if err == nil {
		t.Fatal("expected an error when Regenerate fails")
	}
	if regenerated {
		t.Error("expected regenerated=false on Regenerate failure")
	}
	if rt.regenCalls != 1 {
		t.Errorf("expected Regenerate attempted once, got %d", rt.regenCalls)
	}
	// The reset hook must NOT have fired — the old container is still live.
	if got := rec.recorded(); len(got) != 0 {
		t.Errorf("reset hook must NOT fire on failed regenerate, got %v", got)
	}
}
