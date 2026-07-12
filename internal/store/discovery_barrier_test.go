package store

import (
	"context"
	"testing"
)

// TestOrphanGC_DeferredUntilDiscoveryDone is [AC-Sfeed64-1-3]'s barrier test:
// it pins the fix for the HIGH race that making startup DiscoverAndRestore
// async (Sfeed64-1) would otherwise introduce.
//
// The old SYNCHRONOUS discovery calls completed BEFORE Store.Run started the
// 10s scan loop that drives the orphan-GC passes, so GC never ran until
// discovery was done. Async discovery removed that ordering. Because
// DiscoverAndRestore and the two GC passes dial the SAME on-disk ptyhosts
// (claude-tui and claude-agent share one run dir/seed space), a GC tick that
// fired while discovery was still mid-adoption of a surviving orphan socket
// would dial/SHUTDOWN that socket concurrently with discovery — and since the
// ptyhost tolerates only one connection and restored Daemons have
// ResumeOnDeath=true, a clean orphan SHUTDOWN could flap into a respawn/
// re-kill.
//
// Store.ArmDiscoveryBarrier restores the "discovery before GC" ordering. This
// test proves it directly: with the barrier armed (discovery in-flight), a GC
// tick is a complete no-op — GCOrphans, which is what actually dials/SHUTDOWNs
// an orphan socket, is NOT invoked at all (fake.calls stays 0, i.e. the live
// orphan socket is never dialed). Only once the barrier is released (discovery
// done) does the GC pass run and call GCOrphans.
func TestOrphanGC_DeferredUntilDiscoveryDone(t *testing.T) {
	s, _ := newStoreFixture(t)

	fakeTui := &fakeTuiOrphanGC{}
	fakeAgent := &fakeAgentOrphanGC{}
	s.SetTuiOrphanGC(fakeTui)
	s.SetAgentOrphanGC(fakeAgent)

	// Arm the barrier BEFORE any GC tick — mirrors main.go arming it before
	// st.Run starts the scan loop.
	release := s.ArmDiscoveryBarrier()

	// Discovery still in-flight (barrier not released): a GC tick — the exact
	// call runPortScan makes every 10s — must be a complete NO-OP. GCOrphans
	// (which dials/SHUTDOWNs orphan sockets inside scanRunDir) must NOT be
	// invoked, so the live orphan socket is never dialed while discovery is
	// still adopting it.
	s.gcTuiOrphans(context.Background())
	s.gcAgentOrphans(context.Background())
	if fakeTui.calls != 0 || fakeAgent.calls != 0 {
		t.Fatalf("[AC-Sfeed64-1-3] orphan GC dialed sockets while discovery was in-flight: tui GCOrphans calls=%d agent=%d, want 0/0 (barrier must defer the whole pass)",
			fakeTui.calls, fakeAgent.calls)
	}

	// Discovery completes → barrier released.
	release()

	// Now the GC pass must run (GCOrphans invoked) on the next tick.
	s.gcTuiOrphans(context.Background())
	s.gcAgentOrphans(context.Background())
	if fakeTui.calls != 1 || fakeAgent.calls != 1 {
		t.Fatalf("[AC-Sfeed64-1-3] orphan GC did not run after discovery completed: tui GCOrphans calls=%d agent=%d, want 1/1",
			fakeTui.calls, fakeAgent.calls)
	}

	// Release is idempotent (main.go releases it via `defer` regardless of how
	// discovery ended; a second call must not panic on a closed channel).
	release()
	s.gcTuiOrphans(context.Background())
	if fakeTui.calls != 2 {
		t.Fatalf("second release / GC tick misbehaved: tui GCOrphans calls=%d, want 2", fakeTui.calls)
	}
}

// TestOrphanGC_RunsImmediatelyWithoutBarrier verifies the default (no barrier
// armed) leaves orphan GC running immediately — every existing caller/test
// that never calls ArmDiscoveryBarrier (and any deployment that does not
// front-load discovery) is unaffected: discoveryGateOpen returns true for a
// nil channel.
func TestOrphanGC_RunsImmediatelyWithoutBarrier(t *testing.T) {
	s, _ := newStoreFixture(t)
	fakeTui := &fakeTuiOrphanGC{}
	fakeAgent := &fakeAgentOrphanGC{}
	s.SetTuiOrphanGC(fakeTui)
	s.SetAgentOrphanGC(fakeAgent)

	// No ArmDiscoveryBarrier call → nil gate → GC proceeds immediately.
	s.gcTuiOrphans(context.Background())
	s.gcAgentOrphans(context.Background())
	if fakeTui.calls != 1 || fakeAgent.calls != 1 {
		t.Fatalf("orphan GC did not run without a barrier armed: tui=%d agent=%d, want 1/1", fakeTui.calls, fakeAgent.calls)
	}
}
