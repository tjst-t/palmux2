package store

// Unit tests for Store.RestartBranchRuntime (S8478ca-refine).
//
// Tests:
//   - same-kind → no-op (no tmux kill, no evict, returns false)
//   - different kind (host → incus) → old session killed + evicted + new session created
//   - branch not open → no-op, returns false
//   - nil registry → no-op, returns false

import (
	"context"
	"io"
	"sync"
	"testing"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tab/bash"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// ── fake runtime ──────────────────────────────────────────────────────────────

type fakeRuntime struct {
	kind      runtime.Kind
	tc        tmux.Client
	stopCalls int
	mu        sync.Mutex
}

func (f *fakeRuntime) Kind() runtime.Kind  { return f.kind }
func (f *fakeRuntime) Config() runtime.Config { return runtime.Config{Kind: f.kind} }
func (f *fakeRuntime) Start(_ context.Context) error { return nil }
func (f *fakeRuntime) Stop(_ context.Context) error {
	f.mu.Lock()
	f.stopCalls++
	f.mu.Unlock()
	return nil
}
func (f *fakeRuntime) Status() runtime.Status { return runtime.Status{State: runtime.StateReady} }
func (f *fakeRuntime) TmuxClient() tmux.Client { return f.tc }
func (f *fakeRuntime) NewTmuxSession(_ context.Context, _ string) error  { return nil }
func (f *fakeRuntime) AttachTmuxSession(_ context.Context, _ string) (io.ReadWriteCloser, error) {
	return nil, nil
}
func (f *fakeRuntime) Exec(_ context.Context, _ []string, _ runtime.ExecOpts) (runtime.ExecResult, error) {
	return runtime.ExecResult{}, nil
}
func (f *fakeRuntime) ListListeningPorts(_ context.Context) ([]runtime.ListeningPort, error) {
	return nil, nil
}
func (f *fakeRuntime) ExposePort(_ context.Context, _ runtime.PortSpec) (runtime.PortMapping, error) {
	return runtime.PortMapping{}, nil
}
func (f *fakeRuntime) UnexposePort(_ context.Context, _ string) error { return nil }

// ── fake registry ─────────────────────────────────────────────────────────────

type fakeRegistry struct {
	mu          sync.Mutex
	runtimes    map[string]runtime.Runtime // key = repoID+"/"+branchID
	evictCalls  map[string]int
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		runtimes:   map[string]runtime.Runtime{},
		evictCalls: map[string]int{},
	}
}

func (r *fakeRegistry) set(repoID, branchID string, rt runtime.Runtime) {
	r.mu.Lock()
	r.runtimes[repoID+"/"+branchID] = rt
	r.mu.Unlock()
}

func (r *fakeRegistry) Get(repoID, branchID string) runtime.Runtime {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runtimes[repoID+"/"+branchID]
}

func (r *fakeRegistry) EvictRuntime(repoID, branchID string) {
	r.mu.Lock()
	r.evictCalls[repoID+"/"+branchID]++
	delete(r.runtimes, repoID+"/"+branchID)
	r.mu.Unlock()
}

func newStoreWithRegistry(t *testing.T, reg *fakeRegistry) (*Store, *tmux.MockClient) {
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

// ── tests ─────────────────────────────────────────────────────────────────────

// TestRestartBranchRuntime_SameKind verifies that restarting with the same
// kind is a no-op: no KillSession, no EvictRuntime, returns (false, nil).
func TestRestartBranchRuntime_SameKind(t *testing.T) {
	reg := newFakeRegistry()
	s, mockTmux := newStoreWithRegistry(t, reg)

	repoID := "test-repo--a1b2"
	repoDir := t.TempDir()
	// Add repo to RepoStore so SetWorkspaceRuntime can find it.
	if _, err := s.deps.RepoStore.Add(config.RepoEntry{ID: repoID, GHQPath: "test/repo-a1b2"}); err != nil {
		t.Fatalf("RepoStore.Add: %v", err)
	}
	branchID := injectBranch(t, s, repoID, repoDir, "main", true)
	sessionName := s.repos[repoID].OpenBranches[0].TabSet.TmuxSession
	// Seed the session so KillSession doesn't error.
	mockTmux.SeedSession(sessionName)

	// Wire the registry to return a host runtime for this workspace.
	hostRT := &fakeRuntime{kind: runtime.KindHost, tc: mockTmux}
	reg.set(repoID, branchID, hostRT)

	// SetWorkspaceRuntime persists kind=host (same as current).
	if err := s.deps.RepoStore.SetWorkspaceRuntime(repoID, branchID, &runtime.Config{Kind: runtime.KindHost}); err != nil {
		t.Fatalf("SetWorkspaceRuntime: %v", err)
	}

	restarted, err := s.RestartBranchRuntime(context.Background(), repoID, branchID, hostRT)
	if err != nil {
		t.Fatalf("RestartBranchRuntime returned error: %v", err)
	}
	if restarted {
		t.Error("expected no-op (same kind), got restarted=true")
	}
	if reg.evictCalls[repoID+"/"+branchID] != 0 {
		t.Error("EvictRuntime should not be called for same-kind no-op")
	}
	// Session must still exist.
	has, _ := mockTmux.HasSession(context.Background(), sessionName)
	if !has {
		t.Error("session must not be killed on same-kind no-op")
	}
}

// TestRestartBranchRuntime_BranchNotOpen verifies that a call for an unknown
// branchID is a no-op (returns false, nil).
func TestRestartBranchRuntime_BranchNotOpen(t *testing.T) {
	reg := newFakeRegistry()
	s, _ := newStoreWithRegistry(t, reg)

	restarted, err := s.RestartBranchRuntime(context.Background(), "no-repo", "no-branch", &fakeRuntime{kind: runtime.KindHost})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if restarted {
		t.Error("expected no-op for unknown branch, got restarted=true")
	}
}

// TestRestartBranchRuntime_NilRegistry verifies that without a RuntimeRegistry
// the function returns (false, nil) immediately.
func TestRestartBranchRuntime_NilRegistry(t *testing.T) {
	// Use the standard fixture (no RuntimeRegistry).
	s, _ := newStoreFixture(t)

	repoID := "test-repo--0001"
	repoDir := t.TempDir()
	injectBranch(t, s, repoID, repoDir, "main", true)

	restarted, err := s.RestartBranchRuntime(context.Background(), repoID, "anything", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if restarted {
		t.Error("expected no-op with nil registry, got restarted=true")
	}
}

// TestRestartBranchRuntime_KindChange verifies the full migration path:
//
//  1. Old tmux session is killed via the OLD runtime's TmuxClient.
//  2. EvictRuntime is called.
//  3. ensureSession creates a new session (via the new runtime after eviction).
//  4. Returns (true, nil).
//
// We simulate a host→"incus" migration by having the fakeRegistry return a
// host runtime for Get() calls (the "old" cached entry), and then after
// EvictRuntime the next Get() returns nil (no new entry) causing tmuxFor to
// fall back to s.deps.Tmux — which is sufficient to verify ensureSession
// runs without error.
func TestRestartBranchRuntime_KindChange(t *testing.T) {
	reg := newFakeRegistry()
	s, mockTmux := newStoreWithRegistry(t, reg)

	repoID := "test-repo--c3d4"
	repoDir := t.TempDir()
	if _, err := s.deps.RepoStore.Add(config.RepoEntry{ID: repoID, GHQPath: "test/repo-c3d4"}); err != nil {
		t.Fatalf("RepoStore.Add: %v", err)
	}
	branchID := injectBranch(t, s, repoID, repoDir, "main", true)
	sessionName := s.repos[repoID].OpenBranches[0].TabSet.TmuxSession

	// Use a SEPARATE mock tmux for the "old" runtime so we can detect
	// that KillSession was called on the OLD runtime's client.
	oldMock := tmux.NewMockClient()
	oldMock.SeedSession(sessionName)

	hostRT := &fakeRuntime{kind: runtime.KindHost, tc: oldMock}
	reg.set(repoID, branchID, hostRT)

	// Persist the new runtime kind (incus-container) as if the user just PATCHed it.
	if err := s.deps.RepoStore.SetWorkspaceRuntime(repoID, branchID, &runtime.Config{
		Kind: runtime.KindIncusContainer,
	}); err != nil {
		t.Fatalf("SetWorkspaceRuntime: %v", err)
	}
	// After eviction the registry has no entry → tmuxFor falls back to s.deps.Tmux.
	// Seed the session on the main mock so ensureSession can verify HasSession.
	mockTmux.SeedSession(sessionName)

	restarted, err := s.RestartBranchRuntime(context.Background(), repoID, branchID, hostRT)
	if err != nil {
		t.Fatalf("RestartBranchRuntime: %v", err)
	}
	if !restarted {
		t.Error("expected restarted=true for kind change, got false")
	}
	// Old session must have been killed on the OLD tmux client.
	if has, _ := oldMock.HasSession(context.Background(), sessionName); has {
		t.Error("old session should have been killed on the old runtime's TmuxClient")
	}
	// EvictRuntime must have been called once.
	if reg.evictCalls[repoID+"/"+branchID] != 1 {
		t.Errorf("expected 1 EvictRuntime call, got %d", reg.evictCalls[repoID+"/"+branchID])
	}
	// Stop must have been called on the old runtime (it was host kind, so stopCalls=0
	// is correct — Stop is only called for incus-container). Verify accordingly.
	if hostRT.stopCalls != 0 {
		t.Errorf("Stop should NOT be called for host→incus transition (old=host); got %d calls", hostRT.stopCalls)
	}
}

// TestRestartBranchRuntime_IncusStop verifies that when the OLD runtime is
// incus-container, its Stop() is called before eviction.
func TestRestartBranchRuntime_IncusStop(t *testing.T) {
	reg := newFakeRegistry()
	s, mockTmux := newStoreWithRegistry(t, reg)

	repoID := "test-repo--e5f6"
	repoDir := t.TempDir()
	if _, err := s.deps.RepoStore.Add(config.RepoEntry{ID: repoID, GHQPath: "test/repo-e5f6"}); err != nil {
		t.Fatalf("RepoStore.Add: %v", err)
	}
	branchID := injectBranch(t, s, repoID, repoDir, "main", true)
	sessionName := s.repos[repoID].OpenBranches[0].TabSet.TmuxSession
	mockTmux.SeedSession(sessionName)

	// Old runtime is incus-container. We use s.deps.Tmux as its TmuxClient so
	// ensureSession post-evict can still create the session on the host mock.
	incusRT := &fakeRuntime{kind: runtime.KindIncusContainer, tc: mockTmux}
	reg.set(repoID, branchID, incusRT)

	// Persist kind=host (migrating from incus → host).
	if err := s.deps.RepoStore.SetWorkspaceRuntime(repoID, branchID, &runtime.Config{
		Kind: runtime.KindHost,
	}); err != nil {
		t.Fatalf("SetWorkspaceRuntime: %v", err)
	}

	restarted, err := s.RestartBranchRuntime(context.Background(), repoID, branchID, incusRT)
	if err != nil {
		t.Fatalf("RestartBranchRuntime: %v", err)
	}
	if !restarted {
		t.Error("expected restarted=true")
	}
	// Stop must have been called exactly once (old runtime was incus-container).
	if incusRT.stopCalls != 1 {
		t.Errorf("expected 1 Stop call for incus→host migration; got %d", incusRT.stopCalls)
	}
}
