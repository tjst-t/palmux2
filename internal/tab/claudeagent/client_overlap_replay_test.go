package claudeagent

// S862203-3 review HIGH regression test: parallel/overlapping permissions
// across a restart. Claude issues parallel tool calls, so a LATER permission
// (B) can be ANSWERED while an EARLIER one (A) is still PENDING. The persisted
// replay frontier is a single contiguous offset held back at A, so a
// reconnect's ATTACH replays B's control_request line too — and without the
// resolved-request-id set, B would re-surface as a spurious duplicate prompt
// for an already-answered request. This test drives that exact scenario at
// the handleLine level (no real ptyhost needed — the offset/resolved
// bookkeeping is pure palmux2-side logic) and asserts: on reconnect A IS
// re-surfaced (still genuinely pending) and B is NOT (already answered).

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/ptyhost"
)

// surfacedRecorder is a CanUseToolHandler test double: it records every
// request_id it is asked about (i.e. every control_request that reached the
// "surface a permission to the UI" path) and lets the test hold a specific
// request pending (block) or answer it (return allow) deterministically.
type surfacedRecorder struct {
	mu        sync.Mutex
	surfaced  []string
	arrived   map[string]chan struct{} // closed when RequestPermission for id is entered
	release   map[string]chan struct{} // the handler blocks until this is closed
}

func newSurfacedRecorder(ids ...string) *surfacedRecorder {
	r := &surfacedRecorder{
		arrived: map[string]chan struct{}{},
		release: map[string]chan struct{}{},
	}
	for _, id := range ids {
		r.arrived[id] = make(chan struct{})
		r.release[id] = make(chan struct{})
	}
	return r
}

func (r *surfacedRecorder) handler(_ context.Context, _ canUseToolRequest, requestID string) (canUseToolResponse, error) {
	r.mu.Lock()
	r.surfaced = append(r.surfaced, requestID)
	arrived := r.arrived[requestID]
	release := r.release[requestID]
	r.mu.Unlock()
	if arrived != nil {
		close(arrived)
	}
	if release != nil {
		<-release // block until the test answers this permission
	}
	return canUseToolResponse{Behavior: "allow"}, nil
}

func (r *surfacedRecorder) surfacedIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.surfaced))
	copy(out, r.surfaced)
	return out
}

func (r *surfacedRecorder) waitArrived(t *testing.T, id string, d time.Duration) {
	t.Helper()
	r.mu.Lock()
	ch := r.arrived[id]
	r.mu.Unlock()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatalf("permission for %q never surfaced within %v", id, d)
	}
}

// newBookkeepingOnlyClient builds a Client with JUST the fields
// handleLine/beginPending/resolvePending/persistSafeFrontierLocked/
// advanceAck/handleControlRequest(can_use_tool) touch — NO real ptyhost
// connection. pc is left nil (writeLine tolerates that: the control_response
// write simply fails-and-logs, which is irrelevant to what this test
// asserts — whether a permission is re-SURFACED, not whether its response is
// re-sent). ringGeneration is set from `hello` so a second client resuming
// from the same OffsetStore + same hello honours the checkpoint.
func newBookkeepingOnlyClient(store *OffsetStore, hello ptyhost.HelloPayload, onCanUseTool CanUseToolHandler) *Client {
	c := &Client{
		mux:            newControlMux(),
		onCanUseTool:   onCanUseTool,
		logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		repoID:         "repo",
		branchID:       "branch",
		tabID:          "claude:claude",
		offsetStore:    store,
		ringGeneration: ringGenerationFor(hello),
		pendingAsync:   map[string]int64{},
		resolved:       map[string]int64{},
		replaySuppress: map[string]struct{}{},
		runLoopExited:  make(chan struct{}),
		doneCh:         make(chan struct{}),
	}
	return c
}

// controlRequestLine builds a can_use_tool control_request NDJSON line for
// the given request_id (no trailing newline — handleLine receives the line
// without its '\n').
func controlRequestLine(t *testing.T, requestID string) []byte {
	t.Helper()
	req, err := json.Marshal(map[string]any{
		"subtype":   "can_use_tool",
		"tool_name": "Bash",
		"input":     map[string]any{"command": "echo " + requestID},
	})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	line, err := json.Marshal(streamMsg{Type: "control_request", RequestID: requestID, Request: req})
	if err != nil {
		t.Fatalf("marshal envelope: %v", err)
	}
	return line
}

// TestClient_OverlappingPermissions_OnlyPendingResurfacesAfterRestart is the
// S862203-3 review HIGH regression: A pending + B answered + restart -> on
// reconnect A re-surfaces, B does NOT.
func TestClient_OverlappingPermissions_OnlyPendingResurfacesAfterRestart(t *testing.T) {
	store, err := NewOffsetStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewOffsetStore: %v", err)
	}
	hello := ptyhost.HelloPayload{Pid: 4242, ArgvHash: "gen-abc"}

	// ---- Phase 1: pre-restart. A fires and stays PENDING; B fires and is
	// ANSWERED. ----------------------------------------------------------
	rec1 := newSurfacedRecorder("reqA", "reqB")
	c1 := newBookkeepingOnlyClient(store, hello, rec1.handler)

	lineA := controlRequestLine(t, "reqA")
	lineB := controlRequestLine(t, "reqB")

	// Feed A first, then B, with monotonically increasing absolute offsets
	// exactly as PipeClient.Run would (endOffset = offset just past the
	// line's trailing '\n'). Start A's line at offset 100.
	lineStartA := int64(100)
	endA := lineStartA + int64(len(lineA)) + 1
	lineStartB := endA
	endB := lineStartB + int64(len(lineB)) + 1

	if err := c1.handleLine(lineA, endA); err != nil {
		t.Fatalf("handleLine A: %v", err)
	}
	if err := c1.handleLine(lineB, endB); err != nil {
		t.Fatalf("handleLine B: %v", err)
	}

	// A's handler is blocked (pending). Wait for B to be answered
	// (release B -> its handler returns allow -> resolvePending persists).
	rec1.waitArrived(t, "reqA", 3*time.Second)
	rec1.waitArrived(t, "reqB", 3*time.Second)
	close(rec1.release["reqB"])

	// Wait until B is actually recorded as resolved+persisted (the async
	// resolvePending has run).
	waitFor(t, 3*time.Second, "B persisted as resolved", func() bool {
		r, ok := store.Get("repo", "branch", "claude:claude")
		if !ok {
			return false
		}
		_, has := r.ResolvedControlRequests["reqB"]
		return has
	})

	rec, ok := store.Get("repo", "branch", "claude:claude")
	if !ok {
		t.Fatal("no persisted record after phase 1")
	}
	// Frontier must be held at A's lineStart (A still pending), NOT advanced
	// past it — otherwise A wouldn't replay at all.
	if rec.LastAckOffset != lineStartA {
		t.Fatalf("persisted LastAckOffset = %d, want %d (held at pending A's lineStart)", rec.LastAckOffset, lineStartA)
	}
	if _, has := rec.ResolvedControlRequests["reqB"]; !has {
		t.Fatalf("reqB not in persisted resolved set: %+v", rec.ResolvedControlRequests)
	}
	if _, has := rec.ResolvedControlRequests["reqA"]; has {
		t.Fatalf("reqA must NOT be in the persisted resolved set (it is still pending): %+v", rec.ResolvedControlRequests)
	}
	// NB: reqA's phase-1 handler is deliberately left BLOCKED (pending)
	// through phase 2 — releasing it here would resolve A, advance the
	// persisted frontier past A's line, and collapse the very scenario
	// under test. It (and A's phase-2 handler) are released + drained at
	// the end.

	// ---- Phase 2: restart. New Client resumes from the SAME store + SAME
	// hello (same ring generation), then the reconnect REPLAYS A and B. ----
	rec2 := newSurfacedRecorder("reqA", "reqB")
	c2 := newBookkeepingOnlyClient(store, hello, rec2.handler)
	offset := c2.resumeOffsetFor(hello)
	if offset != lineStartA {
		t.Fatalf("resume offset = %d, want %d (should ATTACH at pending A's line)", offset, lineStartA)
	}
	if _, has := c2.replaySuppress["reqB"]; !has {
		t.Fatalf("reqB should be in replaySuppress after resume; got %v", c2.replaySuppress)
	}
	if _, has := c2.replaySuppress["reqA"]; has {
		t.Fatalf("reqA must NOT be in replaySuppress (it was pending, not answered); got %v", c2.replaySuppress)
	}

	// Replay both lines from the ATTACH offset (A first, then B — the ring
	// order). Their absolute offsets are the SAME as phase 1 (same ring
	// generation, absolute offsets never reused).
	if err := c2.handleLine(lineA, endA); err != nil {
		t.Fatalf("replay handleLine A: %v", err)
	}
	if err := c2.handleLine(lineB, endB); err != nil {
		t.Fatalf("replay handleLine B: %v", err)
	}

	// A must be re-surfaced (it was genuinely still pending across the
	// restart — this is AC-S862203-3-2 part b working).
	rec2.waitArrived(t, "reqA", 3*time.Second)

	// B must NOT be re-surfaced. Give any (erroneous) async dispatch a
	// generous window to prove it doesn't happen.
	deadline := time.Now().Add(1500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if containsStr(rec2.surfacedIDs(), "reqB") {
			t.Fatalf("reqB was RE-SURFACED after restart even though it was already answered pre-restart (S862203-3 review HIGH) — surfaced=%v", rec2.surfacedIDs())
		}
		time.Sleep(50 * time.Millisecond)
	}

	got := rec2.surfacedIDs()
	if !containsStr(got, "reqA") {
		t.Fatalf("reqA was NOT re-surfaced after restart (pending permission lost) — surfaced=%v", got)
	}
	if containsStr(got, "reqB") {
		t.Fatalf("reqB re-surfaced (already-answered duplicate) — surfaced=%v", got)
	}
	t.Logf("observed post-restart re-surfaced set = %v (A re-surfaced, B not) — as required", got)

	// Deterministic teardown: release every still-blocked handler (A in both
	// phases) and WAIT for each client's async control_request goroutines to
	// fully drain (pendingAsync empty) before returning — otherwise a late
	// resolvePending would write to the OffsetStore's temp dir AFTER
	// t.Cleanup's RemoveAll, spuriously failing the test.
	close(rec1.release["reqA"])
	rec2.waitArrived(t, "reqA", 3*time.Second) // ensure A's phase-2 handler entered before releasing
	close(rec2.release["reqA"])
	waitFor(t, 3*time.Second, "phase-1 client drained", func() bool { return c1.pendingCount() == 0 })
	waitFor(t, 3*time.Second, "phase-2 client drained", func() bool { return c2.pendingCount() == 0 })
	_ = fmt.Sprint(offset)
}

// pendingCount reports how many control_requests are still being handled
// asynchronously (test-only drain helper).
func (c *Client) pendingCount() int {
	c.ackMu.Lock()
	defer c.ackMu.Unlock()
	return len(c.pendingAsync)
}

func containsStr(s []string, want string) bool {
	for _, v := range s {
		if v == want {
			return true
		}
	}
	return false
}
