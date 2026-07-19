package agenttui

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

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
