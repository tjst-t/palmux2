package agenttui

import (
	"context"
	"log/slog"
	"os/exec"
	"sync"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/agent"
	"github.com/tjst-t/palmux2/internal/runtime"
)

// fakePathChecker is a runtime.SharedPathChecker whose readiness (and
// optional error) can be controlled per-call, used to exercise
// waitForSharedPaths (Sc4f091-2) without a real incus binary.
type fakePathChecker struct {
	mu       sync.Mutex
	calls    int
	readyAt  int // PathsReady returns true starting from this call number (1-indexed); 0 = never
	fixedErr error
}

func (f *fakePathChecker) PathsReady(_ context.Context, _ []string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fixedErr != nil {
		return false, f.fixedErr
	}
	if f.readyAt != 0 && f.calls >= f.readyAt {
		return true, nil
	}
	return false, nil
}

func (f *fakePathChecker) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// [AC-Sc4f091-2-4] waitForSharedPaths returns as soon as PathsReady reports
// ready, without waiting out the full budget.
func TestWaitForSharedPaths_ReturnsAssoonAsReady(t *testing.T) {
	fc := &fakePathChecker{readyAt: 2} // not ready on the first poll, ready on the second
	start := time.Now()
	waitForSharedPaths(context.Background(), fc, []string{"/home/ubuntu/.codex"}, slog.Default())
	elapsed := time.Since(start)

	if fc.callCount() < 2 {
		t.Errorf("[AC-Sc4f091-2-4] expected at least 2 PathsReady calls, got %d", fc.callCount())
	}
	if elapsed >= sharedPathsReadyBudget {
		t.Errorf("[AC-Sc4f091-2-4] expected early return well under the full budget (%s), took %s", sharedPathsReadyBudget, elapsed)
	}
}

// [AC-Sc4f091-2-4] waitForSharedPaths is fail-open: if PathsReady never
// reports ready (or always errors), it returns anyway once the budget is
// exhausted, rather than blocking indefinitely — the caller must always be
// able to proceed with the spawn.
func TestWaitForSharedPaths_FailsOpenOnTimeout(t *testing.T) {
	fc := &fakePathChecker{readyAt: 0} // never ready
	start := time.Now()
	waitForSharedPaths(context.Background(), fc, []string{"/home/ubuntu/.codex"}, slog.Default())
	elapsed := time.Since(start)

	if elapsed < sharedPathsReadyPoll {
		t.Errorf("[AC-Sc4f091-2-4] expected at least one poll interval to elapse, took %s", elapsed)
	}
	if elapsed > sharedPathsReadyBudget+2*time.Second {
		t.Errorf("[AC-Sc4f091-2-4] expected to fail open at/around the budget (%s), took %s", sharedPathsReadyBudget, elapsed)
	}
}

// [AC-Sc4f091-2-4] A checker that errors on every call is also fail-open —
// waitForSharedPaths must not panic or hang, and must return once the budget
// elapses.
func TestWaitForSharedPaths_FailsOpenOnError(t *testing.T) {
	fc := &fakePathChecker{fixedErr: context.DeadlineExceeded}
	done := make(chan struct{})
	go func() {
		waitForSharedPaths(context.Background(), fc, []string{"/home/ubuntu/.codex"}, slog.Default())
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(sharedPathsReadyBudget + 3*time.Second):
		t.Fatal("[AC-Sc4f091-2-4] waitForSharedPaths did not return in time for an always-erroring checker")
	}
	if fc.callCount() == 0 {
		t.Errorf("[AC-Sc4f091-2-4] expected PathsReady to have been called at least once")
	}
}

// waitForSharedPaths must respect context cancellation and return promptly,
// not wait out the full budget.
func TestWaitForSharedPaths_RespectsContextCancellation(t *testing.T) {
	fc := &fakePathChecker{readyAt: 0} // never ready
	ctx, cancel := context.WithCancel(context.Background())

	start := time.Now()
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	waitForSharedPaths(ctx, fc, []string{"/home/ubuntu/.codex"}, slog.Default())
	elapsed := time.Since(start)

	if elapsed >= sharedPathsReadyBudget {
		t.Errorf("expected cancellation to short-circuit the wait well under the budget (%s), took %s", sharedPathsReadyBudget, elapsed)
	}
}

var _ runtime.SharedPathChecker = (*fakePathChecker)(nil)

// --- Sc4f091-2 review fix regression guard: the readiness wait must not
// serialize concurrent EnsureStarted calls behind spawnMu. ---

// fakeInContainerAdapter is a minimal agent.Adapter + agent.InContainerProvider
// double: just enough for spawnWithArgs/preSpawnWaitForSharedPaths to treat
// this as an in-container agent with shared paths to wait on, without
// depending on any real codex/opencode adapter.
type fakeInContainerAdapter struct {
	paths []string
}

func (a *fakeInContainerAdapter) Kind() agent.Kind    { return agent.Kind("faketest") }
func (a *fakeInContainerAdapter) DisplayName() string { return "FakeTest" }
func (a *fakeInContainerAdapter) Capabilities() agent.Capabilities {
	return agent.Capabilities{InContainer: true}
}
func (a *fakeInContainerAdapter) SpawnSpec(agent.SpawnIntent) (agent.SpawnSpec, error) {
	return agent.SpawnSpec{Argv: []string{"true"}}, nil
}
func (a *fakeInContainerAdapter) ContainerBinary() (string, bool) { return "true", true }
func (a *fakeInContainerAdapter) SharedContainerPaths() []string  { return a.paths }

var (
	_ agent.Adapter             = (*fakeInContainerAdapter)(nil)
	_ agent.InContainerProvider = (*fakeInContainerAdapter)(nil)
)

// slowPathChecker is a runtime.PTYCommander + runtime.SharedPathChecker whose
// PathsReady blocks for `delay` before reporting ready (then a harmless `true`
// PTYCommand actually "spawns") — simulates the pre-spawn readiness wait
// taking real, test-scale time so two concurrent waits can be timed against
// each other.
type slowPathChecker struct {
	delay time.Duration
}

func (s *slowPathChecker) PTYCommand(ctx context.Context, argv []string, _ runtime.PTYCommandOpts) *exec.Cmd {
	return exec.CommandContext(ctx, "true")
}

func (s *slowPathChecker) PathsReady(ctx context.Context, _ []string) (bool, error) {
	select {
	case <-time.After(s.delay):
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

var (
	_ runtime.PTYCommander      = (*slowPathChecker)(nil)
	_ runtime.SharedPathChecker = (*slowPathChecker)(nil)
)

// [AC-Sc4f091-2-4] Two DIFFERENT Daemons ("different tabs") calling
// EnsureStarted concurrently each pay their own bounded readiness wait, but
// neither wait blocks the other's spawn — total wall time is close to ONE
// wait's delay, not the sum of both. Before the review fix (readiness wait
// running INSIDE spawnWithArgs while spawnMu was held) this property already
// held trivially for genuinely separate Daemon instances (spawnMu is a
// per-Daemon field, never shared) — this test exists as an explicit,
// permanent regression guard against a future refactor accidentally
// introducing ANY shared/global serialization point for the wait (e.g. a
// package-level mutex, or routing it through a shared Manager lock).
func TestPreSpawnReadinessWait_DifferentTabsDoNotSerialize(t *testing.T) {
	const delay = 300 * time.Millisecond
	adapter := &fakeInContainerAdapter{paths: []string{"/tmp"}}

	newDaemon := func() *Daemon {
		checker := &slowPathChecker{delay: delay}
		return NewDaemon(DaemonConfig{
			Adapter:         adapter,
			RingSize:        1 << 16,
			RuntimeResolver: func(_, _ string) runtime.PTYCommander { return checker },
		})
	}

	d1, d2 := newDaemon(), newDaemon()
	t.Cleanup(func() { d1.Shutdown(); d2.Shutdown() })

	start := time.Now()
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs[0] = d1.EnsureStarted(context.Background()) }()
	go func() { defer wg.Done(); errs[1] = d2.EnsureStarted(context.Background()) }()
	wg.Wait()
	elapsed := time.Since(start)

	for i, err := range errs {
		if err != nil {
			t.Fatalf("[AC-Sc4f091-2-4] daemon %d EnsureStarted: %v", i, err)
		}
	}
	// Serialized (the pre-fix shape) would take ~2*delay; parallel (the
	// fixed shape) takes ~1*delay plus scheduling/spawn overhead. 1.6*delay
	// is comfortably below 2*delay and comfortably above 1*delay+overhead —
	// a wide enough margin to not be flaky on a loaded CI box while still
	// clearly distinguishing the two shapes.
	if elapsed >= delay*16/10 {
		t.Errorf("[AC-Sc4f091-2-4] two different-tab EnsureStarted calls took %s (>= %s) — looks serialized, want close to one %s wait",
			elapsed, delay*16/10, delay)
	}
}

// spawnMuProbeChecker is a runtime.PTYCommander + runtime.SharedPathChecker
// whose PathsReady signals `entered` the instant it's called, then blocks
// until the test closes `release` — giving the test a deterministic window
// (no timing guesswork) to assert, from a SEPARATE goroutine, whether the
// Daemon's own spawnMu is held while the readiness check is in flight.
type spawnMuProbeChecker struct {
	entered chan struct{}
	release chan struct{}
}

func (s *spawnMuProbeChecker) PTYCommand(ctx context.Context, argv []string, _ runtime.PTYCommandOpts) *exec.Cmd {
	return exec.CommandContext(ctx, "true")
}

func (s *spawnMuProbeChecker) PathsReady(ctx context.Context, _ []string) (bool, error) {
	close(s.entered)
	<-s.release
	return true, nil
}

var (
	_ runtime.PTYCommander      = (*spawnMuProbeChecker)(nil)
	_ runtime.SharedPathChecker = (*spawnMuProbeChecker)(nil)
)

// [AC-Sc4f091-2-4] Direct, deterministic proof of the review fix's core
// claim — "the readiness wait must run WITHOUT spawnMu held" — rather than
// inferring it indirectly from wall-clock timing (which, for THIS package's
// exact call graph, turns out NOT to reliably distinguish the two shapes:
// spawnMu is a per-Daemon field so genuinely separate Daemons never
// contended on it either way, and for concurrent calls on the SAME Daemon
// only one real spawn ever happens — guarded by d.spawned — so a second
// caller blocking on the mutex vs. redundantly waiting in parallel costs the
// SAME total wall time either way; verified empirically while writing this
// test by temporarily re-inlining the old wait-under-lock shape and
// confirming a timing-based version of this test did NOT catch it).
//
// This test instead pauses EnsureStarted's spawning goroutine INSIDE
// PathsReady (via the entered/release rendezvous) and, while it is paused
// there, attempts d.spawnMu.TryLock() from the test's own goroutine. Under
// the fixed code (preSpawnWaitForSharedPaths called BEFORE spawnMu.Lock()),
// TryLock must succeed — the mutex is free. Confirmed this fails exactly as
// expected (TryLock reports the mutex held) when temporarily reverting to
// the pre-fix shape (the wait moved back inside spawnWithArgs, called under
// spawnMu) during test development.
func TestPreSpawnWaitForSharedPaths_DoesNotHoldSpawnMu(t *testing.T) {
	adapter := &fakeInContainerAdapter{paths: []string{"/tmp"}}
	checker := &spawnMuProbeChecker{entered: make(chan struct{}), release: make(chan struct{})}
	d := NewDaemon(DaemonConfig{
		Adapter:         adapter,
		RingSize:        1 << 16,
		RuntimeResolver: func(_, _ string) runtime.PTYCommander { return checker },
	})
	t.Cleanup(func() { d.Shutdown() })

	done := make(chan error, 1)
	go func() { done <- d.EnsureStarted(context.Background()) }()

	select {
	case <-checker.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("[AC-Sc4f091-2-4] PathsReady was never entered — spawn path did not reach the readiness check")
	}

	// EnsureStarted's goroutine is now parked inside PathsReady. This is the
	// exact instant the review flagged: is spawnMu held right now?
	locked := d.spawnMu.TryLock()
	if !locked {
		close(checker.release) // unblock the spawn goroutine before failing so the test doesn't also hang
		t.Fatal("[AC-Sc4f091-2-4] spawnMu is held while the shared-paths readiness check is in flight — " +
			"the wait must run OUTSIDE spawnMu so it cannot serialize any other concurrent EnsureStarted/respawn call")
	}
	d.spawnMu.Unlock()

	close(checker.release)
	if err := <-done; err != nil {
		t.Fatalf("[AC-Sc4f091-2-4] EnsureStarted: %v", err)
	}
}
