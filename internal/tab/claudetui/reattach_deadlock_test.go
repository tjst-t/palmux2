package claudetui

import (
	"bytes"
	"context"
	"strconv"
	"testing"
	"time"
)

// This file is the Sfeed64-1 regression test for the v0.14.12 (S3f2658)
// reattach startup deadlock — see docs/handoff/reattach-deadlock-handoff.md
// for the full root-cause writeup this test closes the verification gap on.
//
// Bug recap: on reconnect to a SURVIVING ptyhost, [Daemon.spawnWithArgs] fed
// the ATTACH replay into the emulator ([Emulator.Feed]) BEFORE starting the
// background goroutine that drains the emulator's response pipe (the
// goroutine that answers DA1/DA2/cursor-position queries back to claude).
// The vt emulator's response channel is an unbuffered io.Pipe (see
// third_party/charmbracelet-x-vt-racefix/emulator.go): the very first
// response byte written with nobody reading blocks the writer — and it
// blocks HOLDING the SafeEmulator writer lock, which in turn is reached
// while [Daemon.EnsureStarted] still holds spawnMu, so the whole startup
// goroutine wedges and the server never reaches ListenAndServe (S3f2658
// bricked ndev this exact way on v0.14.12's first real reattach).
//
// A FRESH spawn's replay is empty, so this only fires on a reconnect to a
// ptyhost that already has buffered output containing ANSI device queries —
// which is precisely why S3f2658's original verification (tens of bytes of
// toy heartbeat text, see docs/sprint-logs/S3f2658/{survival-S3f2658-1,
// e2e-S3f2658-3}.json) never caught it: that content contained no query
// sequences at all, so the response-generating code path was never
// exercised, regardless of size.
//
// [AC-Sfeed64-1-2] requires REALISTIC data, not a toy poke: this test drives
// a real fake_claude child (via its --query-burst flag, testdata/
// fake_claude.go) that emits >64KiB of scrollback-shaped filler text
// interleaved with real DA1 ("\x1b[c"), DA2 ("\x1b[>c"), and cursor-position
// ("\x1b[6n") query sequences — the exact three sequences
// third_party/charmbracelet-x-vt-racefix/handlers.go answers by writing into
// the emulator's response pipe — before a second, separately-constructed
// Daemon (simulating "palmux2 restarted") reattaches to the still-live
// ptyhost and receives the entire burst as its ATTACH(-1) replay.
func TestReattachSurvivorReplayDoesNotDeadlock(t *testing.T) {
	runDir := shortRunDir(t)
	bin := fakeBin(t)

	// Comfortably over the emulator response pipe's zero-buffering threshold
	// (ANY unread response byte blocks Write — see doc comment above) and
	// over the >64KiB bar [AC-Sfeed64-1-2] sets as the "not a toy" floor —
	// this is the shape of scrollback a real, busy claude-tui session
	// accumulates, not a synthetic few-byte poke.
	const queryBurstBytes = 200 * 1024

	identity := DaemonConfig{
		ClaudeBin:  bin,
		ClaudeArgs: []string{"--query-burst", strconv.Itoa(queryBurstBytes)},
		// 1MiB — comfortably retains the full burst without wrapping, matching
		// the handoff doc's "up to a full ring, ~1MB" description of a real
		// production replay.
		RingSize:       1 << 20,
		ResumeOnDeath:  false,
		RepoID:         "reattach-deadlock-repo",
		BranchID:       "reattach-deadlock-branch",
		TabID:          "claude",
		RunDirOverride: runDir,
	}

	// "Before restart": Daemon A spawns FRESH (empty replay — harmless under
	// both the buggy and fixed ordering) and produces the query-heavy burst
	// as live, ordinary output.
	dA := NewDaemon(identity)
	t.Cleanup(func() { dA.Shutdown() })
	if err := dA.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted A: %v", err)
	}
	waitForState(t, dA, StateRunning, 5*time.Second)

	// Wait for the FULL burst to land in A's ring — not just for the child to
	// start. dA's own live drainer answers the queries as they stream in
	// (the architecturally-correct "live" path, unaffected by this bug), so A
	// reaching this marker proves the whole burst was genuinely produced and
	// relayed through a real ptyhost, not merely spawned.
	deadline := time.After(60 * time.Second)
	for {
		if bytes.Contains(dA.ring.Bytes(), []byte("QUERY_BURST_DONE")) {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("query burst never completed; ring len=%d", len(dA.ring.Bytes()))
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	// Small settle window: dA's ring (palmux2-side) and the ptyhost's own
	// server-side ring (what B's ATTACH(-1) actually replays from) are fed
	// from the same DATA-frame stream but are logically separate buffers.
	time.Sleep(50 * time.Millisecond)

	ringLen := len(dA.ring.Bytes())
	if ringLen < queryBurstBytes {
		t.Fatalf("ring only has %d bytes, want >= %d (query burst) — replay would not be realistic", ringLen, queryBurstBytes)
	}
	t.Logf("query burst landed: %d bytes in ring (>64KiB, AC-Sfeed64-1-2's realistic 'not a toy' floor)", ringLen)

	pidBefore := dA.CurrentStats().PID
	if pidBefore == 0 {
		t.Fatal("A has no pid")
	}

	// "palmux2 restarts": a BRAND NEW Daemon object, same identity + run dir,
	// WITHOUT dA ever being shut down — exactly what a surviving ptyhost from
	// a real palmux2 restart looks like (§3 of docs/no-halt-agent-design.md).
	// dB's launchAndAttach will find dA's live ptyhost and ATTACH(-1),
	// receiving the ENTIRE >64KiB query-heavy burst as replay: the exact
	// condition that deadlocked spawnWithArgs pre-fix.
	dB := NewDaemon(identity)
	t.Cleanup(func() { dB.Shutdown() })

	done := make(chan error, 1)
	go func() {
		done <- dB.EnsureStarted(context.Background())
	}()

	const reattachBound = 45 * time.Second
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("EnsureStarted B: %v", err)
		}
	case <-time.After(reattachBound):
		t.Fatalf("[AC-Sfeed64-1-2] EnsureStarted B did not return within %s — REPRODUCES the v0.14.12 "+
			"reattach startup deadlock: spawnWithArgs's emulator.Feed(replay) blocks writing DA1/DA2/CPR "+
			"responses into the emulator's response pipe with no drainer goroutine running yet, wedging "+
			"EnsureStarted's spawnMu forever — exactly what bricked ndev on v0.14.12's first real reattach "+
			"(see docs/handoff/reattach-deadlock-handoff.md)", reattachBound)
	}

	// The reattach must be to the SAME surviving ptyhost (same pid), not a
	// fresh spawn — otherwise this test would trivially pass without ever
	// exercising the reattach-replay path at all.
	pidAfter := dB.CurrentStats().PID
	if pidAfter != pidBefore {
		t.Fatalf("B spawned a NEW ptyhost (pid %d) instead of reconnecting to A's surviving one (pid %d) — "+
			"this test did not exercise the reattach-replay path", pidAfter, pidBefore)
	}
	waitForState(t, dB, StateRunning, 5*time.Second)
	t.Logf("[AC-Sfeed64-1-2] PASS: reattach to surviving ptyhost (pid=%d) with a %d-byte query-heavy replay "+
		"completed within %s (no deadlock)", pidAfter, ringLen, reattachBound)
}
