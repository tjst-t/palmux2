package store

// Lock-order regression tests for the tab-set derivation.
//
// PROVENANCE — these tests are RESTORED, not new. They were written on the
// `customize-ws-container` branch as commit 65fc548 ("fix(store): stop
// recomputeTabs from touching the runtime registry under s.mu") and verified
// on real appliance hardware by 380577d. That branch was never merged; it now
// sits 205 commits behind main, so both the fix and its regression net were
// silently lost from the shipping code. ADR-0012 rebuilds the split, and this
// file rebuilds the net so it cannot be lost the same way again.
//
// Background: the reducer used to call tmuxFor (→ RuntimeRegistry.Get(), which
// may Start() an incus container or — when the runtime is not cached and has
// no seeded worktree path — fall back to a worktree-resolver closure that calls
// back into Store.Branch(), i.e. s.mu.Lock() a second time from the SAME
// goroutine) WHILE s.mu WAS HELD. Go's sync.RWMutex is not reentrant, so that
// resolver path deadlocked the process permanently; even without the resolver,
// Start() under the lock could wedge every other API behind a multi-minute
// container launch.
//
// The derivation is split into:
//   - gatherRecomputeWindows: lock-free, may touch RuntimeRegistry, NEVER
//     starts a container (a not-ready incus runtime yields "no windows")
//   - computeTabs: lock-free, pure, calls provider Tabs()
//   - swapTabs: caller holds s.mu, touches nothing but in-memory state
//
// recomputeAndPublish sequences them. These tests verify (a) a not-ready incus
// runtime never gets Start() called by the gather step, (b) a ready one is
// listed normally, and (c) the reentrant-resolver deadlock scenario completes
// instead of hanging, through every startup call site.

import (
	"context"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// trackingRuntime is like fakeRuntime (restart_runtime_test.go) but records
// Start() calls and allows a configurable Status, so tests can assert
// "not-ready incus runtime was never started" precisely.
type trackingRuntime struct {
	kind   runtime.Kind
	tc     tmux.Client
	status runtime.Status

	mu         sync.Mutex
	startCalls int
}

func (f *trackingRuntime) Kind() runtime.Kind     { return f.kind }
func (f *trackingRuntime) Config() runtime.Config { return runtime.Config{Kind: f.kind} }
func (f *trackingRuntime) Start(_ context.Context) error {
	f.mu.Lock()
	f.startCalls++
	f.mu.Unlock()
	return nil
}
func (f *trackingRuntime) Stop(_ context.Context) error                     { return nil }
func (f *trackingRuntime) Status() runtime.Status                           { return f.status }
func (f *trackingRuntime) TmuxClient() tmux.Client                          { return f.tc }
func (f *trackingRuntime) NewTmuxSession(_ context.Context, _ string) error { return nil }
func (f *trackingRuntime) AttachTmuxSession(_ context.Context, _ string) (io.ReadWriteCloser, error) {
	return nil, nil
}
func (f *trackingRuntime) Exec(_ context.Context, _ []string, _ runtime.ExecOpts) (runtime.ExecResult, error) {
	return runtime.ExecResult{}, nil
}
func (f *trackingRuntime) ListListeningPorts(_ context.Context) ([]runtime.ListeningPort, error) {
	return nil, nil
}
func (f *trackingRuntime) ExposePort(_ context.Context, _ runtime.PortSpec) (runtime.PortMapping, error) {
	return runtime.PortMapping{}, nil
}
func (f *trackingRuntime) UnexposePort(_ context.Context, _ string) error { return nil }

func (f *trackingRuntime) startCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.startCalls
}

// TestGatherRecomputeWindows_NotReadyIncusRuntime_NeverStarts verifies that
// gatherRecomputeWindows treats a stopped/evicted/not-yet-started incus
// runtime as "no windows" WITHOUT ever calling Start() — deriving a tab set is
// a read-only reconciliation pass; starting containers is tmuxFor's job, and
// only from call sites that already run outside s.mu.
func TestGatherRecomputeWindows_NotReadyIncusRuntime_NeverStarts(t *testing.T) {
	reg := newFakeRegistry()
	s, _ := newStoreWithRegistry(t, reg)

	repoID := "test-repo--grw1"
	repoDir := t.TempDir()
	branchID := injectBranch(t, s, repoID, repoDir, "main", true)
	sessionName := s.repos[repoID].OpenBranches[0].TabSet.TmuxSession

	rt := &trackingRuntime{
		kind:   runtime.KindIncusContainer,
		tc:     tmux.NewMockClient(),
		status: runtime.Status{State: runtime.StateStopped},
	}
	reg.set(repoID, branchID, rt)

	windows, listFailed := s.gatherRecomputeWindows(context.Background(), repoID, branchID, sessionName)
	if windows != nil {
		t.Errorf("expected nil windows for a not-ready incus runtime, got %+v", windows)
	}
	if !listFailed {
		t.Error("expected listFailed=true for a not-ready incus runtime")
	}
	if got := rt.startCallCount(); got != 0 {
		t.Errorf("gatherRecomputeWindows must never call Start(); got %d calls", got)
	}
}

// TestGatherRecomputeWindows_ReadyIncusRuntime_ListsWindows verifies the
// counterpart: a runtime that is already Ready is listed normally, and still
// never has Start() called by the gather step.
func TestGatherRecomputeWindows_ReadyIncusRuntime_ListsWindows(t *testing.T) {
	reg := newFakeRegistry()
	s, _ := newStoreWithRegistry(t, reg)

	repoID := "test-repo--grw2"
	repoDir := t.TempDir()
	branchID := injectBranch(t, s, repoID, repoDir, "main", true)
	sessionName := s.repos[repoID].OpenBranches[0].TabSet.TmuxSession

	inContainer := tmux.NewMockClient()
	inContainer.SeedSession(sessionName, tmux.Window{Index: 0, Name: "palmux:bash:bash"})

	rt := &trackingRuntime{
		kind:   runtime.KindIncusContainer,
		tc:     inContainer,
		status: runtime.Status{State: runtime.StateReady},
	}
	reg.set(repoID, branchID, rt)

	windows, listFailed := s.gatherRecomputeWindows(context.Background(), repoID, branchID, sessionName)
	if listFailed {
		t.Fatal("expected listFailed=false for a ready incus runtime with a live session")
	}
	if len(windows) != 1 || windows[0].Name != "palmux:bash:bash" {
		t.Errorf("expected the seeded window to be listed, got %+v", windows)
	}
	if got := rt.startCallCount(); got != 0 {
		t.Errorf("gatherRecomputeWindows must never call Start(); got %d calls", got)
	}
}

// reentrantResolverRegistry simulates the real incus.Registry's dangerous
// path: an uncached workspace with no seeded worktree path falls back to a
// resolver that calls back into the Store (Store.Branch → s.mu.Lock()).
// Every call is uncached on purpose, so every Get() re-enters the store.
type reentrantResolverRegistry struct {
	store *Store
}

func (r *reentrantResolverRegistry) Get(repoID, branchID string) runtime.Runtime {
	// Mirrors incus.Registry.Get's worktree-resolver fallback: call back into
	// the store exactly like the real registry does when nothing is cached and
	// no worktree path was seeded. If this runs while the CALLING goroutine
	// already holds s.mu, Store.Branch's s.mu.Lock() blocks forever
	// (sync.RWMutex is not reentrant) — that was the reported deadlock. If the
	// gather step correctly runs lock-free, this is just an ordinary, briefly
	// contended lock acquisition.
	if r.store != nil {
		_, _ = r.store.Branch(repoID, branchID)
	}
	return &trackingRuntime{
		kind:   runtime.KindIncusContainer,
		tc:     tmux.NewMockClient(),
		status: runtime.Status{State: runtime.StateReady},
	}
}

// Kind is the pure query added by ADR-0012. It deliberately does NOT re-enter
// the Store — that is the whole point of having it separate from Get, and it
// is why provider Tabs() implementations may call it.
func (r *reentrantResolverRegistry) Kind(_, _ string) runtime.Kind {
	return runtime.KindIncusContainer
}

// TestPopulateTabs_ReentrantResolver_DoesNotDeadlock reproduces the exact
// mechanism of the reported hang: PopulateTabs runs once at startup for every
// open branch, so every incus workspace's runtime is guaranteed uncached. It
// must gather tmux windows for each branch BEFORE taking s.mu, even when the
// registry's Get() calls back into Store.Branch(). Before the fix, PopulateTabs
// held s.mu.Lock() for the whole loop — a same-goroutine re-lock that hangs
// forever. A hang manifests as the timeout below, so the timeout IS the
// assertion.
func TestPopulateTabs_ReentrantResolver_DoesNotDeadlock(t *testing.T) {
	s, _ := newStoreFixture(t)
	// Wire the reentrant registry AFTER construction (mirrors how main.go
	// wires the real incus.Registry's worktree resolver after the Store
	// exists).
	s.deps.RuntimeRegistry = &reentrantResolverRegistry{store: s}

	repoID := "test-repo--reentrant"
	repoDir := t.TempDir()
	injectBranch(t, s, repoID, repoDir, "main", true)

	done := make(chan struct{})
	go func() {
		s.PopulateTabs(context.Background())
		close(done)
	}()

	select {
	case <-done:
		// returned — no deadlock
	case <-time.After(5 * time.Second):
		t.Fatal("PopulateTabs deadlocked: the derivation must gather tmux windows " +
			"(and touch the RuntimeRegistry) BEFORE taking s.mu, not while holding it")
	}
}

// TestRecomputeBranchTabs_ReentrantResolver_DoesNotDeadlock is the same
// regression through the public entry point used by conditional tab providers
// (Sprint / Browser / Ports).
func TestRecomputeBranchTabs_ReentrantResolver_DoesNotDeadlock(t *testing.T) {
	s, _ := newStoreFixture(t)
	s.deps.RuntimeRegistry = &reentrantResolverRegistry{store: s}

	repoID := "test-repo--reentrant2"
	repoDir := t.TempDir()
	branchID := injectBranch(t, s, repoID, repoDir, "main", true)

	done := make(chan struct{})
	go func() {
		_ = s.RecomputeBranchTabs(repoID, branchID)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RecomputeBranchTabs deadlocked via the reentrant registry resolver path")
	}
}

// TestOpenRepo_ReentrantResolver_DoesNotDeadlock covers the third startup call
// site — OpenRepo recomputes tabs for every worktree discovered under a repo at
// Open time, which is the "restore an incus workspace from repos.json on
// process restart" scenario from the original bug report.
func TestOpenRepo_ReentrantResolver_DoesNotDeadlock(t *testing.T) {
	s, _ := newStoreFixture(t)
	s.deps.RuntimeRegistry = &reentrantResolverRegistry{store: s}

	// A directory with no worktrees still exercises the same lock structure:
	// the point is that OpenRepo never re-enters s.mu via the registry while
	// already holding it.
	done := make(chan struct{})
	go func() {
		_, _ = s.OpenRepo(context.Background(), "test/openrepo-reentrant")
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("OpenRepo deadlocked via the reentrant registry resolver path")
	}
}

// TestRecomputeAndPublish_HoldsNoLockDuringDerivation is the direct assertion
// for ADR-0012 AC-2-3, and is stronger than the deadlock tests above: those
// prove the process does not hang, this proves the write lock is genuinely not
// held while the derivation does its I/O.
//
// The assertion must be "a reader acquires s.mu WHILE the derivation is in
// flight", not "a reader eventually acquires s.mu" — the latter is satisfied
// even by the buggy version, because the reader simply waits for the
// derivation to finish and then succeeds. An earlier draft of this test made
// exactly that mistake and passed against a deliberately reintroduced
// s.mu.Lock() around the derivation.
func TestRecomputeAndPublish_HoldsNoLockDuringDerivation(t *testing.T) {
	s, _ := newStoreFixture(t)

	// Buffered so the derivation goroutine never blocks on reporting.
	acquiredDuring := make(chan bool, 1)
	s.deps.RuntimeRegistry = &gatingRegistry{onGet: func() {
		got := make(chan struct{})
		go func() {
			// Actually read shared state under the read lock — this is what a
			// real reader does, and it is what proves s.mu is acquirable while
			// the derivation runs. (An empty RLock/RUnlock pair is both a
			// staticcheck SA2001 hit and a weaker assertion.)
			s.mu.RLock()
			_ = len(s.repos)
			s.mu.RUnlock()
			close(got)
		}()
		select {
		case <-got:
			acquiredDuring <- true
		case <-time.After(1500 * time.Millisecond):
			// Still blocked after 1.5 s ⇒ the caller is holding the write lock
			// across the derivation. (The reader will acquire once the
			// derivation releases; that is precisely what we must NOT accept
			// as a pass.)
			acquiredDuring <- false
		}
	}}

	repoID := "test-repo--nolock"
	repoDir := t.TempDir()
	branchID := injectBranch(t, s, repoID, repoDir, "main", true)

	done := make(chan struct{})
	go func() {
		_ = s.RecomputeBranchTabs(repoID, branchID)
		close(done)
	}()

	select {
	case okDuring := <-acquiredDuring:
		if !okDuring {
			t.Fatal("no reader could acquire s.mu WHILE the tab derivation was running — " +
				"the derivation is holding the write lock across its I/O (ADR-0012 AC-2-3)")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the derivation never reached the runtime registry")
	}

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("RecomputeBranchTabs never completed")
	}
}

// gatingRegistry runs a callback inside Get so a test can observe what the
// caller is holding at that moment.
type gatingRegistry struct{ onGet func() }

func (g *gatingRegistry) Get(_, _ string) runtime.Runtime {
	if g.onGet != nil {
		g.onGet()
	}
	return &trackingRuntime{
		kind:   runtime.KindHost,
		tc:     tmux.NewMockClient(),
		status: runtime.Status{State: runtime.StateReady},
	}
}

func (g *gatingRegistry) Kind(_, _ string) runtime.Kind { return runtime.KindHost }
