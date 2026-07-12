package main

import (
	"net"
	"testing"
	"time"
)

// This file is part of [AC-Sfeed64-1-3]'s defense: it drives the EXACT seam
// run() launches startup ptyhost discovery through (runDiscoveryAsync, defined
// in main.go — which runs claude-tui then claude-agent DiscoverAndRestore plus
// the orphan-GC barrier release) with a deliberately-blocking fn, and asserts
// that a stand-in for the very next thing run() does — ListenAndServe — is
// never delayed by it. This is the general defense: even if a FUTURE reattach
// path (in either package, or a new one) ever wedges the way S3f2658's
// claude-tui replay-Feed did (see
// internal/tab/claudetui/reattach_deadlock_test.go for that concrete,
// realistic-data reproduction), run()'s use of this helper means the whole web
// UI can never 502 as a result — only the discovery pass itself stays
// incomplete (and the orphan-GC barrier stays closed, the safe direction —
// see internal/store TestOrphanGC_DeferredUntilDiscoveryDone), while the lazy
// first-WS-attach path (Daemon.EnsureStarted / Agent.EnsureClient) still
// reattaches to the same surviving ptyhost on demand.
func TestRunDiscoveryAsyncDoesNotBlockCaller(t *testing.T) {
	blockFn := make(chan struct{}) // never closed — fn blocks until the test says so
	fnStarted := make(chan struct{})
	fnReturned := make(chan struct{})

	runDiscoveryAsync(func() {
		close(fnStarted)
		<-blockFn // simulates a wedged reattach (e.g. AC-Sfeed64-1-2's deadlock)
		close(fnReturned)
	})

	// The call above must return immediately — runDiscoveryAsync itself must
	// never block the caller, regardless of how long fn takes.
	select {
	case <-fnStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("fn was never even started — runDiscoveryAsync did not launch it")
	}

	// Stand-in for run()'s ListenAndServe: a REAL listener bind + a trivial
	// accept, run on THIS goroutine (i.e. NOT itself backgrounded) — exactly
	// where ListenAndServe sits in run() relative to the discovery launch.
	// Reaching and completing this block at all, while fn above is STILL
	// blocked on blockFn, is the proof that discovery cannot delay serve.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = ln.Close() }()
	acceptErrCh := make(chan error, 1)
	go func() {
		conn, aerr := ln.Accept()
		if aerr == nil {
			_ = conn.Close()
		}
		acceptErrCh <- aerr
	}()
	dialConn, derr := net.DialTimeout("tcp", ln.Addr().String(), 2*time.Second)
	if derr != nil {
		t.Fatalf("dial the stand-in listener: %v", derr)
	}
	_ = dialConn.Close()
	if aerr := <-acceptErrCh; aerr != nil {
		t.Fatalf("accept on the stand-in listener: %v", aerr)
	}
	t.Log("[AC-Sfeed64-1-3] PASS: stand-in listen/accept completed while fn was still blocked — server startup is not delayed by discovery")

	// fn must STILL be blocked at this point — otherwise this test would be
	// vacuously true (fn finished on its own before we even got to assert
	// anything, never having been a real obstacle to prove past).
	select {
	case <-fnReturned:
		t.Fatal("fn returned before the test released it — this scenario never actually exercised a blocking discovery pass")
	default:
	}

	// Now let the blocked "reattach" finish and confirm the background pass
	// actually runs to completion (it is decoupled from serve's critical path,
	// not abandoned).
	close(blockFn)
	select {
	case <-fnReturned:
	case <-time.After(5 * time.Second):
		t.Fatal("fn never returned after being unblocked — the background pass was abandoned")
	}
}
