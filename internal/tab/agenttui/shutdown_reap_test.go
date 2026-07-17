package agenttui

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// This file covers Sprint S3f2658 Story 4 (incus in-container survival + reap):
//
//   - AC-S3f2658-4-1's negative half at the unit level: palmux2's own restart
//     path (Detach) must NOT reap a still-referenced in-container claude. (The
//     positive half — the in-container process keeping its pid across a real
//     palmux2 restart — is a REAL-INCUS-only assertion; see
//     docs/sprint-logs/S3f2658/e2e-S3f2658-4.json.)
//   - AC-S3f2658-4-2: every SHUTDOWN trigger (tab close, branch close, orphan
//     GC) must invoke the in-container reap (runtime.ContainerProcessKiller,
//     reusing S52fc2c-4's incus.KillContainerProcesses) with the correct
//     signal + container claude bin pattern.
//
// A fakeContainerKiller stands in for the real incus.incusRuntime so these
// tests exercise the WIRING (which triggers call the reap, with what args)
// without needing a real incus host — the reap primitive itself
// (KillContainerProcesses's pkill-exit-code handling) is already covered by
// internal/runtime/incus/incus_test.go's TestKillContainerProcesses
// [AC-S52fc2c-4-1].

// killCall records one KillContainerProcesses invocation.
type killCall struct{ sig, pattern string }

// fakeContainerKiller is a runtime.PTYCommander + runtime.ContainerProcessKiller
// test double. PTYCommand builds a real (harmless) *exec.Cmd so it can also
// stand in for DaemonConfig.RuntimeResolver in tests that spawn a Daemon;
// KillContainerProcesses just records the call.
type fakeContainerKiller struct {
	mu    sync.Mutex
	calls []killCall
}

func (f *fakeContainerKiller) PTYCommand(ctx context.Context, argv []string, _ runtime.PTYCommandOpts) *exec.Cmd {
	return exec.CommandContext(ctx, argv[0], argv[1:]...)
}

func (f *fakeContainerKiller) KillContainerProcesses(_ context.Context, sig, pattern string) error {
	f.mu.Lock()
	f.calls = append(f.calls, killCall{sig, pattern})
	f.mu.Unlock()
	return nil
}

func (f *fakeContainerKiller) Calls() []killCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]killCall(nil), f.calls...)
}

// assertSingleTermCall fails t unless calls is exactly one TERM call against
// containerClaudeBin — the shape every SHUTDOWN-triggered reap must produce.
func assertSingleTermCall(t *testing.T, ac string, calls []killCall) {
	t.Helper()
	if len(calls) != 1 {
		t.Fatalf("[%s] KillContainerProcesses calls = %d, want 1: %+v", ac, len(calls), calls)
	}
	if calls[0].sig != "TERM" || calls[0].pattern != containerClaudeBin {
		t.Errorf("[%s] KillContainerProcesses(%q, %q), want (TERM, %q)",
			ac, calls[0].sig, calls[0].pattern, containerClaudeBin)
	}
}

// TestSpawnWithArgs_IncusWrapperHandedOpaquelyToPtyhost is [AC-S3f2658-4-1]'s
// spawn-side assertion: for an incus-container workspace, palmux2 builds the
// full `incus exec -t ...` argv via the existing runtime.PTYCommander
// (fakePTYRuntime, reused from hooks_test.go's S4d8b1c coverage, stands in
// for incusRuntime.PTYCommand) and hands it OPAQUELY to the ptyhost
// Launcher: the actually-executed process is whatever PTYCommand.Path/Args
// said (here, fakeBin — proving ptyhost itself never special-cases or
// reinterprets anything incus-specific, it just holds whatever process it
// was told to exec — ADR-0002).
func TestSpawnWithArgs_IncusWrapperHandedOpaquelyToPtyhost(t *testing.T) {
	bin := fakeBin(t)
	dump := filepath.Join(t.TempDir(), "invocation.json")
	fakeRT := &fakePTYRuntime{fakeBin: bin}

	d := NewDaemon(DaemonConfig{
		ClaudeBin:     "/nonexistent/host/claude", // must never be used — container path only
		ClaudeArgs:    []string{"--dump-invocation", dump},
		RingSize:      1 << 16,
		ResumeOnDeath: false,
		RepoID:        "repo1", BranchID: "branch1", TabID: "claude",
		Worktree: t.TempDir(),
		RuntimeResolver: func(_, _ string) runtime.PTYCommander {
			return fakeRT
		},
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	// The ptyhost must actually be holding fakeRT's wrapper command (not the
	// unreachable host ClaudeBin) — proof the argv PTYCommand built was what
	// got exec'd, opaquely, through the SAME ptyhost Launcher path host-mode
	// spawns use.
	var raw []byte
	deadline := time.After(5 * time.Second)
	for {
		if b, err := os.ReadFile(dump); err == nil && len(b) > 0 {
			raw = b
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for invocation dump — ptyhost never executed the PTYCommander's argv")
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}
	var rec struct {
		Argv []string `json:"argv"`
	}
	if err := json.Unmarshal(raw, &rec); err != nil {
		t.Fatalf("invocation JSON: %v\n%s", err, raw)
	}
	if !hasArgPair(rec.Argv, "--dump-invocation", dump) {
		t.Errorf("[AC-S3f2658-4-1] ptyhost-executed argv missing --dump-invocation; got: %v", rec.Argv)
	}

	fakeRT.mu.Lock()
	gotArgv0 := ""
	if len(fakeRT.argv) > 0 {
		gotArgv0 = fakeRT.argv[0]
	}
	fakeRT.mu.Unlock()
	if gotArgv0 != containerClaudeBin {
		t.Errorf("[AC-S3f2658-4-1] wrapper argv[0]=%q, want container claude bin %q", gotArgv0, containerClaudeBin)
	}
}

// TestDaemonShutdown_ReapsInContainerClaude is [AC-S3f2658-4-2] at the
// tab-close trigger: Daemon.Shutdown() — the exact call Manager.CloseDaemon
// makes on an explicit tab delete — must invoke the workspace runtime's
// ContainerProcessKiller with TERM + the fixed container claude bin path.
// No spawn is needed: teardown's reap fires unconditionally on
// killPtyhost==true, independent of whether a ptyhost was ever attached
// (matches [Daemon.teardown]'s implementation).
func TestDaemonShutdown_ReapsInContainerClaude(t *testing.T) {
	fk := &fakeContainerKiller{}
	d := NewDaemon(DaemonConfig{
		ClaudeBin: "true",
		RingSize:  1 << 16,
		RepoID:    "repo1", BranchID: "branch1", TabID: "claude",
		RuntimeResolver: func(_, _ string) runtime.PTYCommander { return fk },
	})

	d.Shutdown() // tab-close trigger

	assertSingleTermCall(t, "AC-S3f2658-4-2", fk.Calls())
}

// TestDaemonDetach_DoesNotReap is the negative half of [AC-S3f2658-4-1]:
// palmux2's own restart/process-exit path (Manager.DetachAll -> Daemon.Detach)
// must leave a still-referenced in-container claude running — Detach must NOT
// invoke the reap. Only a genuine SHUTDOWN (driven by an actual tab/branch
// delete or orphan GC) does.
func TestDaemonDetach_DoesNotReap(t *testing.T) {
	fk := &fakeContainerKiller{}
	d := NewDaemon(DaemonConfig{
		ClaudeBin: "true",
		RingSize:  1 << 16,
		RepoID:    "repo1", BranchID: "branch1", TabID: "claude",
		RuntimeResolver: func(_, _ string) runtime.PTYCommander { return fk },
	})

	d.Detach() // palmux2's own restart path — must NOT reap

	if calls := fk.Calls(); len(calls) != 0 {
		t.Fatalf("[AC-S3f2658-4-1] Detach must not reap in-container claude; got calls: %+v", calls)
	}
}

// TestManagerCloseBranchDaemons_ReapsEachTab is [AC-S3f2658-4-2] at the
// branch-close trigger: Provider.OnBranchClose -> Manager.CloseBranchDaemons
// must reap EVERY tab belonging to the closed branch (each Daemon's own
// runtimeResolver, captured at construction, resolved by its OWN
// repoID/branchID) and must NOT touch a different, still-open branch's
// container.
func TestManagerCloseBranchDaemons_ReapsEachTab(t *testing.T) {
	fkClosing := &fakeContainerKiller{}
	fkOther := &fakeContainerKiller{}
	m := NewManager(ManagerConfig{
		ClaudeBin: "true",
		RuntimeResolver: func(_, branchID string) runtime.PTYCommander {
			switch branchID {
			case "b":
				return fkClosing
			case "other":
				return fkOther
			}
			return nil
		},
	})
	ctx := context.Background()
	if _, err := m.EnsureDaemon(ctx, "r", "b", "tab-A", ""); err != nil {
		t.Fatalf("EnsureDaemon tab-A: %v", err)
	}
	if _, err := m.EnsureDaemon(ctx, "r", "b", "tab-B", ""); err != nil {
		t.Fatalf("EnsureDaemon tab-B: %v", err)
	}
	if _, err := m.EnsureDaemon(ctx, "r", "other", defaultTabID, ""); err != nil {
		t.Fatalf("EnsureDaemon other: %v", err)
	}

	m.CloseBranchDaemons(ctx, "r", "b")

	if calls := fkClosing.Calls(); len(calls) != 2 {
		t.Fatalf("[AC-S3f2658-4-2] branch close: KillContainerProcesses calls = %d, want 2 (tab-A + tab-B): %+v",
			len(calls), calls)
	}
	if calls := fkOther.Calls(); len(calls) != 0 {
		t.Fatalf("branch close must not reap a DIFFERENT, still-open branch's container: %+v", calls)
	}
	// The other branch's own daemon must still be closeable normally (not
	// left half-torn-down by the assertion above).
	m.CloseBranchDaemons(ctx, "r", "other")
}

// TestGCOrphans_ReapsInContainerClaude is [AC-S3f2658-4-2] at the orphan-GC
// trigger — the ONE SHUTDOWN path that does NOT go through a Daemon at all
// (GCOrphans dials an orphaned ptyhost's socket directly; see
// [sendOrphanShutdown]'s doc comment), so the reap must be wired explicitly
// into GCOrphans itself rather than inherited from Daemon.teardown. Models
// the case where a tab/branch was deleted while palmux2 was down: the
// ptyhost (and any incus-wrapped claude it holds, per ADR-0001) outlives
// palmux2, and only a LATER GC tick ever observes it as unreferenced — that
// tick must still reap the in-container process, or it would linger forever.
// A referenced sibling ptyhost must be left completely untouched (no reap
// call at all — not even a spurious one).
func TestGCOrphans_ReapsInContainerClaude(t *testing.T) {
	bin := fakeBin(t)
	runDir := t.TempDir()
	fkOrphan := &fakeContainerKiller{}
	fkRef := &fakeContainerKiller{}

	refSock, _, refDone, _ := startRawPtyHost(t, runDir, bin, "gc-repo", "gc-branch-ref", "claude:claude")
	orphSock, _, orphDone, _ := startRawPtyHost(t, runDir, bin, "gc-repo", "gc-branch-orphan", "claude:claude")
	t.Cleanup(func() {
		shutdownRawPtyHost(refSock)
		shutdownRawPtyHost(orphSock)
		for _, d := range []chan struct{}{refDone, orphDone} {
			select {
			case <-d:
			case <-time.After(5 * time.Second):
				t.Log("raw ptyhost did not fully tear down within 5s during cleanup")
			}
		}
	})

	mgr := NewManager(ManagerConfig{
		ClaudeBin:      bin,
		RunDirOverride: runDir,
		RuntimeResolver: func(_, branchID string) runtime.PTYCommander {
			switch branchID {
			case "gc-branch-orphan":
				return fkOrphan
			case "gc-branch-ref":
				return fkRef
			}
			return nil
		},
	})

	isLive := func(repoID, branchID, tabID string) bool {
		return repoID == "gc-repo" && branchID == "gc-branch-ref" && tabID == "claude:claude"
	}

	shutdown, _, err := mgr.GCOrphans(context.Background(), isLive)
	if err != nil {
		t.Fatalf("GCOrphans: %v", err)
	}
	if shutdown != 1 {
		t.Fatalf("shutdown = %d, want 1 (only the orphan)", shutdown)
	}

	assertSingleTermCall(t, "AC-S3f2658-4-2", fkOrphan.Calls())
	if calls := fkRef.Calls(); len(calls) != 0 {
		t.Fatalf("orphan GC must not reap a REFERENCED workspace's container: %+v", calls)
	}

	// [AC-S64c835-2-3] A couple more ticks must not produce any further
	// reap of the REFERENCED entry (it is never dialed at all — skipLive
	// excludes it before any liveness probe, let alone a reap) — that part
	// was already asserted below pre-S64c835-2. What was NOT previously
	// asserted, and is pinned explicitly here: the just-reaped ORPHAN
	// (hostA2/orphSock) can legitimately still show up as "live" on one or
	// more of THESE follow-up ticks too — its own SHUTDOWN's grace-period
	// teardown (terminateChild's SIGTERM→SIGKILL escalation,
	// [gracefulShutdownTimeout]) is async and may not have removed the
	// socket file by the time the next 10s-scan-piggybacked tick runs (see
	// [Manager.GCOrphans]'s doc comment: cleanup is DELIBERATELY deferred,
	// not synchronous). When that happens, reapContainerClaude fires AGAIN
	// for the same already-terminated workspace — this is only safe
	// because KillContainerProcesses's pkill-exit-1 ("no matching
	// process") semantics are idempotent (pinned directly, at the
	// production incus implementation, by
	// internal/runtime/incus.TestKillContainerProcesses_RepeatCallsAreIdempotent).
	// This loop makes that reliance an explicit, checked contract instead
	// of an untested assumption: every call recorded across the follow-up
	// ticks (zero or more — timing-dependent, hence no exact count
	// assertion) must have the exact same well-formed (TERM,
	// containerClaudeBin) shape a single reap produces; anything else would
	// mean a repeat reap call is silently malformed or erroring.
	for i := 0; i < 2; i++ {
		if _, _, err := mgr.GCOrphans(context.Background(), isLive); err != nil {
			t.Fatalf("GCOrphans tick %d: %v", i, err)
		}
	}
	if calls := fkRef.Calls(); len(calls) != 0 {
		t.Fatalf("referenced workspace's container was reaped across follow-up GC ticks: %+v", calls)
	}
	for i, c := range fkOrphan.Calls() {
		if c.sig != "TERM" || c.pattern != containerClaudeBin {
			t.Fatalf("[AC-S64c835-2-3] repeat reap call %d against the already-shutdown orphan was malformed: %+v (want every repeat call to stay (TERM, %q) — pkill-exit-1 idempotency contract)", i, c, containerClaudeBin)
		}
	}
}

// TestReapContainerClaude_RepeatCallsAreSafe is [AC-S64c835-2-3] at the
// claudetui-side helper directly (complementing
// internal/runtime/incus.TestKillContainerProcesses_RepeatCallsAreIdempotent,
// which pins the SAME contract one layer down at the real pkill primitive):
// reapContainerClaude — the palmux2-side call site every SHUTDOWN trigger
// (tab close, branch close, orphan GC) invokes — must tolerate being called
// more than once for the exact same (repoID, branchID) without erroring,
// panicking, or producing a differently-shaped call on the second
// invocation. This is the explicit, checked pin for what was previously
// only an implicit assumption baked into reapContainerClaude's own doc
// comment ("pkill exit 1 ... is the common/expected case").
func TestReapContainerClaude_RepeatCallsAreSafe(t *testing.T) {
	fk := &fakeContainerKiller{}
	resolver := func(_, _ string) runtime.PTYCommander { return fk }

	const repeats = 3
	for i := 0; i < repeats; i++ {
		reapContainerClaude(resolver, "repo1", "branch1", containerClaudeBin, time.Second, nil)
	}

	calls := fk.Calls()
	if len(calls) != repeats {
		t.Fatalf("[AC-S64c835-2-3] reapContainerClaude called %d times, want exactly %d recorded KillContainerProcesses calls", len(calls), repeats)
	}
	for i, c := range calls {
		if c.sig != "TERM" || c.pattern != containerClaudeBin {
			t.Fatalf("[AC-S64c835-2-3] repeat call %d shape drifted: %+v, want (TERM, %q) on every call", i, c, containerClaudeBin)
		}
	}
}
