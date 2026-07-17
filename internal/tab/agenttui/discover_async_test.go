package agenttui

import (
	"bytes"
	"context"
	"net"
	"strconv"
	"testing"
	"time"
)

// TestDiscoverAndRestoreAsyncWrapDoesNotBlockServeUnderRealisticReplay is
// [AC-Sfeed64-1-3]'s claudetui-specific half: it ties AC-Sfeed64-1-2's
// realistic (>64KiB, real DA1/DA2/CPR query) surviving-ptyhost replay
// directly to the SAME async-wrap idiom cmd/palmux/main.go uses around the
// real, exported [DiscoverAndRestore] — as opposed to
// cmd/palmux/discovery_async_test.go's TestRunDiscoveryAsyncDoesNotBlockCaller,
// which pins the general defensive mechanism (runDiscoveryAsync) with a
// synthetic, test-controlled blocking fn. Together the two tests cover both
// ends: this one proves the REAL production discovery entry point, exercised
// with the REAL production bug's exact triggering data shape, does not stall
// a subsequent serve step when wrapped the way run() wraps it; the other
// proves the wrapping mechanism itself is correct in general.
//
// With the Sfeed64-1 root fix (drainer started before the ATTACH-replay
// Feed) in place, this scenario no longer even hangs — DiscoverAndRestore
// completes fast and this test's stand-in "serve" step was never actually at
// risk. That is the intended, together-defense-in-depth outcome: AC-1 fixes
// the root cause, AC-3 additionally guarantees that even a FUTURE reattach
// bug of this shape (in this package or claudeagent's) could never again
// escalate into a 502'd, un-servable process — see
// reattach_deadlock_test.go's revert-and-rerun demonstration for direct
// proof of what this exact scenario does WITHOUT the AC-1 fix.
func TestDiscoverAndRestoreAsyncWrapDoesNotBlockServeUnderRealisticReplay(t *testing.T) {
	runDir := shortRunDir(t)
	bin := fakeBin(t)

	const queryBurstBytes = 200 * 1024 // matches reattach_deadlock_test.go's AC-Sfeed64-1-2 sizing

	identity := DaemonConfig{
		ClaudeBin:      bin,
		ClaudeArgs:     []string{"--query-burst", strconv.Itoa(queryBurstBytes)},
		RingSize:       1 << 20,
		ResumeOnDeath:  false,
		RepoID:         "discover-async-repo",
		BranchID:       "discover-async-branch",
		TabID:          "claude",
		RunDirOverride: runDir,
	}

	// Leave a surviving ptyhost with a large, query-heavy ring — exactly
	// reattach_deadlock_test.go's setup, but this time [DiscoverAndRestore]
	// itself (not a second hand-built Daemon) will be the one to reattach.
	seed := NewDaemon(identity)
	t.Cleanup(func() { seed.Shutdown() })
	if err := seed.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("seed EnsureStarted: %v", err)
	}
	waitForState(t, seed, StateRunning, 5*time.Second)

	deadline := time.After(60 * time.Second)
	for {
		if bytes.Contains(seed.ring.Bytes(), []byte("QUERY_BURST_DONE")) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("query burst never completed; ring len=%d", len(seed.ring.Bytes()))
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	time.Sleep(50 * time.Millisecond)
	if ringLen := len(seed.ring.Bytes()); ringLen < queryBurstBytes {
		t.Fatalf("ring only has %d bytes, want >= %d", ringLen, queryBurstBytes)
	}
	// Deliberately do NOT shut down `seed` — its ptyhost surviving is the
	// whole point (a real palmux2 restart leaves it running; see
	// docs/no-halt-agent-design.md §3).

	// "palmux2 restarts": a brand-new Manager over the SAME run dir.
	mgr := NewManager(ManagerConfig{ClaudeBin: bin, RunDirOverride: runDir, RingSize: 1 << 20})
	t.Cleanup(func() { _ = mgr.ShutdownAll(context.Background()) })

	// Mirror cmd/palmux/main.go's runDiscoveryAsync wrapping VERBATIM in
	// shape (that helper lives in package main and cannot be imported here,
	// so this reproduces its exact goroutine + result-channel pattern against
	// the real, exported DiscoverAndRestore).
	discoveryDone := make(chan struct{})
	var adopted, cleaned int
	var discoverErr error
	go func() {
		defer close(discoveryDone)
		adopted, cleaned, discoverErr = DiscoverAndRestore(context.Background(), mgr, nil, nil)
	}()

	// Stand-in for run()'s ListenAndServe, run on THIS (the test's main)
	// goroutine — exactly where serve sits relative to the discovery call in
	// run(). A real bind + accept, not backgrounded.
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
	dialConn, derr := net.DialTimeout("tcp", ln.Addr().String(), 3*time.Second)
	if derr != nil {
		t.Fatalf("[AC-Sfeed64-1-3] dial the stand-in listener: %v (server startup appears to have been delayed)", derr)
	}
	_ = dialConn.Close()
	if aerr := <-acceptErrCh; aerr != nil {
		t.Fatalf("accept on the stand-in listener: %v", aerr)
	}
	t.Log("[AC-Sfeed64-1-3] PASS: stand-in listen/accept completed immediately, independent of the real DiscoverAndRestore's progress")

	// The background discovery pass must still complete (with the AC-1 fix,
	// promptly) and successfully re-adopt the surviving ptyhost.
	select {
	case <-discoveryDone:
	case <-time.After(45 * time.Second):
		t.Fatal("DiscoverAndRestore did not complete in the background within 45s")
	}
	if discoverErr != nil {
		t.Fatalf("DiscoverAndRestore: %v", discoverErr)
	}
	if adopted != 1 {
		t.Fatalf("adopted = %d, want 1", adopted)
	}
	if cleaned != 0 {
		t.Fatalf("cleaned = %d, want 0", cleaned)
	}
	d := mgr.Get(identity.RepoID, identity.BranchID, identity.TabID)
	if d == nil {
		t.Fatal("no Daemon adopted for the surviving identity")
	}
	waitForState(t, d, StateRunning, 5*time.Second)
	gotPid, wantPid := d.CurrentStats().PID, seed.CurrentStats().PID
	if gotPid != wantPid {
		t.Fatalf("adopted pid = %d, want %d (the surviving ptyhost's pid) — discovery spawned a NEW ptyhost instead of attaching", gotPid, wantPid)
	}
	t.Logf("[AC-Sfeed64-1-3] PASS: DiscoverAndRestore re-adopted the surviving ptyhost (pid=%d) with its full %d-byte replay in the background, without ever delaying the stand-in serve step above", gotPid, queryBurstBytes)
}
