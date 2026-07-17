package agenttui

import (
	"log/slog"
	"sync"
	"testing"
)

// TestRoleCoordinatorConcurrentSubscribeUnsubscribe is a regression test for
// AC-S64c835-1-2: roleCoordinator.OnSubscribe/OnUnsubscribe used to read
// len(rc.subs) *after* releasing rc.mu for their "subscriber joined/left"
// debug log lines, racing a concurrent OnUnsubscribe's write to rc.subs
// (`rc.subs = newSubs`) under the lock. `go test -race` reliably caught this
// with many concurrent subscribers, exactly the multi-client scenario this
// coordinator exists for (see role.go's doc comment).
//
// The fix captures the count under rc.mu before unlocking (role.go's
// OnSubscribe/OnUnsubscribe). This test does not assert on any particular
// role assignment outcome — TestSecondClientIsViewer /
// TestActiveTransferOnDisconnect already cover those semantics — it exists
// purely to hammer the concurrent-access pattern under `-race`.
func TestRoleCoordinatorConcurrentSubscribeUnsubscribe(t *testing.T) {
	rc := newRoleCoordinator(slog.Default())

	const workers = 32
	const roundsPerWorker = 50

	var wg sync.WaitGroup
	wg.Add(workers)
	for w := 0; w < workers; w++ {
		go func(id int64) {
			defer wg.Done()
			for r := 0; r < roundsPerWorker; r++ {
				sub := newSubscriber(0) // id assigned by OnSubscribe
				_ = rc.OnSubscribe(sub)
				// Interleave a TakeActive call so the broadcast path (which
				// also reads rc.subs under lock) gets exercised concurrently
				// too.
				rc.TakeActive(sub)
				rc.OnUnsubscribe(sub)
			}
		}(int64(w))
	}
	wg.Wait()

	// After every worker has subscribed and unsubscribed the same number of
	// times, no subscribers should remain.
	rc.mu.Lock()
	remaining := len(rc.subs)
	rc.mu.Unlock()
	if remaining != 0 {
		t.Errorf("remaining subscribers = %d, want 0", remaining)
	}
}
