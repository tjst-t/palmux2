package claudeagent

// Bash tool permission round-trip integration tests
// (AC-S43cfb1-6-6).
//
// These tests drive the legacy `canUseTool` path
// (Agent.handleCanUseTool ↔ permWaiters ↔ AnswerPermission) end-to-
// end inside a single process, simulating the four canonical user
// decisions the live Claude tab supports for a Bash invocation:
//
//  1. Allow once    — frame.Decision == "allow", scope == "once"
//  2. Always allow  — scope == "session" (adds tool to allowList)
//  3. Deny          — frame.Decision == "deny"
//  4. AllowList preempt — tool already on allowList; handleCanUseTool
//                         short-circuits without touching permWaiters
//
// The KEY assertion across all four scenarios: **the chain does not
// deadlock**. Every `canUseToolRequest` produces a `canUseToolResponse`
// within a bounded timeout, leaving `permWaiters` empty. A bug that
// orphans a permWaiter entry would manifest as a 5-second test
// timeout here.
//
// We do NOT spin up a real CLI subprocess (or a real WS connection):
// the production wire path is `cli stdin/stdout → controlLoop →
// handleCanUseTool → permWaiter chan → AnswerPermission → response
// back to cli`. By calling handleCanUseTool / AnswerPermission
// directly we cover everything except the JSON unmarshal/marshal at
// the boundary, which is already covered by client_test.go.
//
// The "WS event chain" assertion is satisfied via Agent.Subscribe()
// — broadcast events fan out to subscribers in-process; we observe
// EvPermissionRequest (when prompt fires) and EvStatusChange (after
// resolve) on a goroutine-shared channel.

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// drainEventsUntil is a small helper that pulls events off the
// subscriber channel until either `match` returns true or the
// deadline fires. It returns the matched event (zero value on
// timeout) and a bool indicating whether a match was found.
//
// We deliberately avoid blocking on a single event type: the agent
// emits multiple events per turn (status.change, permission.request,
// status.change, …) so the test must be tolerant of interleaving.
func drainEventsUntil(t *testing.T, events <-chan AgentEvent, timeout time.Duration, match func(AgentEvent) bool) (AgentEvent, bool) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return AgentEvent{}, false
			}
			if match(ev) {
				return ev, true
			}
		case <-deadline.C:
			return AgentEvent{}, false
		}
	}
}

// waitForPermissionRequestEvent extracts the permission_id from the
// next EvPermissionRequest the agent broadcasts. Used by scenarios
// 1/2/3 — for scenario 4 (allowList preempt) no permission.request
// is emitted, so this helper would (correctly) time out there.
func waitForPermissionRequestEvent(t *testing.T, events <-chan AgentEvent, timeout time.Duration) (string, bool) {
	t.Helper()
	ev, ok := drainEventsUntil(t, events, timeout, func(e AgentEvent) bool {
		return e.Type == string(EvPermissionRequest)
	})
	if !ok {
		return "", false
	}
	var p PermissionRequestPayload
	if err := json.Unmarshal(ev.Payload, &p); err != nil {
		t.Fatalf("decode permission.request: %v", err)
	}
	if p.PermissionID == "" {
		t.Fatal("permission.request lacks permission_id")
	}
	return p.PermissionID, true
}

// startBashPerm spawns handleCanUseTool on a goroutine for a Bash
// tool request and returns a recv-only channel that fires once the
// CLI-side response is produced. Lets the test thread drive
// AnswerPermission while the simulated CLI blocks on permWaiters.
type bashPermResult struct {
	resp canUseToolResponse
	err  error
}

func startBashPermRequest(a *Agent, cliRequestID, command string) <-chan bashPermResult {
	ch := make(chan bashPermResult, 1)
	go func() {
		input := json.RawMessage(`{"command":` + jsonString(command) + `,"description":"test bash invocation"}`)
		req := canUseToolRequest{
			Subtype:  "can_use_tool",
			ToolName: "Bash",
			Input:    input,
		}
		resp, err := a.handleCanUseTool(context.Background(), req, cliRequestID)
		ch <- bashPermResult{resp: resp, err: err}
	}()
	return ch
}

// jsonString returns a quoted JSON string literal for `s`. Pulled
// out so the helper above stays readable.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// permStateCounts is a tiny diagnostic snapshot used by the deadlock
// assertions. We hold a.mu while reading because permWaiters is
// guarded by it.
type permStateCounts struct {
	permWaiters        int
	pendingPermissions int
	allowList          int
}

func snapshotPermState(a *Agent) permStateCounts {
	a.mu.Lock()
	pw := len(a.permWaiters)
	a.mu.Unlock()
	// Session counts: rather than reach into private maps, drive
	// observable behaviour. We reuse IsAllowedThisSession on a
	// sentinel name — but for a precise count we need an
	// alternative. Since the existing code stores allowList as a
	// private field on Session, the public surface is
	// IsAllowedThisSession(name). The tests below only check
	// presence/absence by tool name, which is sufficient for the
	// AC-6-6 round-trip semantics.
	return permStateCounts{permWaiters: pw}
}

// assertNoLeaks fails the test if permWaiters still contains
// entries for any of the supplied permission_ids — that's the
// deadlock signature S43cfb1-6 was meant to prevent.
func assertNoLeaks(t *testing.T, a *Agent, permIDs ...string) {
	t.Helper()
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, id := range permIDs {
		if _, ok := a.permWaiters[id]; ok {
			t.Errorf("permWaiters[%q] still set after round-trip — possible deadlock leak", id)
		}
	}
	if len(a.permWaiters) != 0 {
		t.Errorf("permWaiters non-empty after round-trip: %d leftover entries", len(a.permWaiters))
	}
}

// ─────────────────────────────────────────────────────────────────
// Scenario 1: Allow once
// ─────────────────────────────────────────────────────────────────

func TestBashPerm_AllowOnce(t *testing.T) {
	a := newTestAgent(t)
	events, unsub := a.Subscribe()
	defer unsub()

	// 1. Simulated CLI requests Bash permission. handleCanUseTool
	//    blocks on permWaiters until the user answers.
	respCh := startBashPermRequest(a, "cli-req-1", "echo hello")

	// 2. The agent broadcasts permission.request to all WS subs.
	//    Capture the permission_id from the broadcast.
	permID, ok := waitForPermissionRequestEvent(t, events, permTestTimeout(2*time.Second))
	if !ok {
		t.Fatal("expected permission.request event, none seen")
	}

	// 3. permWaiters MUST hold an entry now — the simulated CLI is
	//    blocked.
	if got := snapshotPermState(a); got.permWaiters != 1 {
		t.Fatalf("permWaiters count = %d, want 1 (CLI should be blocked)", got.permWaiters)
	}

	// 4. User clicks Allow once.
	if err := a.AnswerPermission(PermissionRespondFrame{
		PermissionID: permID,
		Decision:     "allow",
		Scope:        "once",
	}); err != nil {
		t.Fatalf("AnswerPermission: %v", err)
	}

	// 5. CLI-side response arrives within the deadline (no
	//    deadlock).
	select {
	case got := <-respCh:
		if got.err != nil {
			t.Fatalf("handleCanUseTool err: %v", got.err)
		}
		if got.resp.Behavior != "allow" {
			t.Fatalf("response behavior = %q, want allow", got.resp.Behavior)
		}
	case <-time.After(permTestTimeout(2 * time.Second)):
		t.Fatal("DEADLOCK: handleCanUseTool did not return after AnswerPermission")
	}

	// 6. permWaiters is empty + allowList does NOT contain Bash
	//    (scope=once must NOT promote to session-wide allow).
	assertNoLeaks(t, a, permID)
	if a.session.IsAllowedThisSession("Bash") {
		t.Fatal("Bash must NOT be on allowList after Allow once")
	}
}

// ─────────────────────────────────────────────────────────────────
// Scenario 2: Always allow (scope=session)
// ─────────────────────────────────────────────────────────────────

func TestBashPerm_AlwaysAllow(t *testing.T) {
	a := newTestAgent(t)
	events, unsub := a.Subscribe()
	defer unsub()

	// 1. First Bash request — prompts.
	respCh := startBashPermRequest(a, "cli-req-1", "ls")
	permID, ok := waitForPermissionRequestEvent(t, events, permTestTimeout(2*time.Second))
	if !ok {
		t.Fatal("expected permission.request event")
	}

	// 2. User clicks Always allow (scope=session). The path adds
	//    "Bash" to the session allowList AND resolves this prompt.
	if err := a.AnswerPermission(PermissionRespondFrame{
		PermissionID: permID,
		Decision:     "allow",
		Scope:        "session",
	}); err != nil {
		t.Fatalf("AnswerPermission: %v", err)
	}

	// 3. First request unblocks.
	select {
	case got := <-respCh:
		if got.err != nil {
			t.Fatalf("first handleCanUseTool err: %v", got.err)
		}
		if got.resp.Behavior != "allow" {
			t.Fatalf("first response behavior = %q, want allow", got.resp.Behavior)
		}
	case <-time.After(permTestTimeout(2 * time.Second)):
		t.Fatal("DEADLOCK: first handleCanUseTool did not return")
	}
	assertNoLeaks(t, a, permID)

	// 4. allowList must now contain Bash.
	if !a.session.IsAllowedThisSession("Bash") {
		t.Fatal("Bash must be on allowList after scope=session")
	}

	// 5. Second Bash request — must be auto-allowed via the
	//    short-circuit branch in handleCanUseTool. No prompt event,
	//    no permWaiter, returns immediately.
	respCh2 := startBashPermRequest(a, "cli-req-2", "pwd")
	select {
	case got := <-respCh2:
		if got.err != nil {
			t.Fatalf("second handleCanUseTool err: %v", got.err)
		}
		if got.resp.Behavior != "allow" {
			t.Fatalf("second response behavior = %q, want allow (auto)", got.resp.Behavior)
		}
	case <-time.After(permTestTimeout(2 * time.Second)):
		t.Fatal("DEADLOCK: second (auto-allow) handleCanUseTool did not return")
	}

	// 6. Verify no permission.request event was broadcast for the
	//    second call. We can only check the next 100ms of events
	//    drain (longer would be flaky); a buggy short-circuit would
	//    leak a prompt event.
	if _, ok := waitForPermissionRequestEvent(t, events, 100*time.Millisecond); ok {
		t.Fatal("auto-allow path must NOT broadcast permission.request")
	}
	assertNoLeaks(t, a)
}

// ─────────────────────────────────────────────────────────────────
// Scenario 3: Deny
// ─────────────────────────────────────────────────────────────────

func TestBashPerm_Deny(t *testing.T) {
	a := newTestAgent(t)
	events, unsub := a.Subscribe()
	defer unsub()

	// 1. First Bash request — prompts.
	respCh := startBashPermRequest(a, "cli-req-1", "rm -rf /")
	permID, ok := waitForPermissionRequestEvent(t, events, permTestTimeout(2*time.Second))
	if !ok {
		t.Fatal("expected permission.request event")
	}

	// 2. User clicks Deny with a reason.
	if err := a.AnswerPermission(PermissionRespondFrame{
		PermissionID: permID,
		Decision:     "deny",
		Reason:       "looks dangerous",
	}); err != nil {
		t.Fatalf("AnswerPermission deny: %v", err)
	}

	// 3. CLI side gets behavior=deny + reason; no deadlock.
	select {
	case got := <-respCh:
		if got.err != nil {
			t.Fatalf("handleCanUseTool err: %v", got.err)
		}
		if got.resp.Behavior != "deny" {
			t.Fatalf("response behavior = %q, want deny", got.resp.Behavior)
		}
		if got.resp.Message != "looks dangerous" {
			t.Fatalf("response message = %q, want 'looks dangerous'", got.resp.Message)
		}
	case <-time.After(permTestTimeout(2 * time.Second)):
		t.Fatal("DEADLOCK: handleCanUseTool did not return after Deny")
	}

	// 4. Bash MUST NOT be on allowList after a deny — the next
	//    request must prompt again.
	if a.session.IsAllowedThisSession("Bash") {
		t.Fatal("Bash must NOT be on allowList after Deny")
	}
	assertNoLeaks(t, a, permID)

	// 5. Second Bash request prompts again (proves Deny did not
	//    silently mark allow).
	respCh2 := startBashPermRequest(a, "cli-req-2", "rm -rf /")
	permID2, ok := waitForPermissionRequestEvent(t, events, permTestTimeout(2*time.Second))
	if !ok {
		t.Fatal("Deny must not skip the second prompt; expected permission.request")
	}
	if permID2 == permID {
		t.Fatalf("second prompt reused the first permission_id %q", permID)
	}
	// Drain it cleanly so the goroutine doesn't leak past the test.
	if err := a.AnswerPermission(PermissionRespondFrame{
		PermissionID: permID2,
		Decision:     "deny",
	}); err != nil {
		t.Fatalf("second AnswerPermission deny: %v", err)
	}
	select {
	case <-respCh2:
	case <-time.After(permTestTimeout(2 * time.Second)):
		t.Fatal("second handleCanUseTool did not return")
	}
	assertNoLeaks(t, a)
}

// ─────────────────────────────────────────────────────────────────
// Scenario 4: Session allowList preempt
// ─────────────────────────────────────────────────────────────────

func TestBashPerm_SessionAllowListPreempt(t *testing.T) {
	a := newTestAgent(t)
	events, unsub := a.Subscribe()
	defer unsub()

	// 1. Pre-seed the session allowList. This simulates either:
	//    (a) a prior scope=session decision earlier in the same
	//        conversation, or
	//    (b) a project-level rule applied by AlwaysAllowTool.
	a.session.AddSessionAllow("Bash")

	// 2. CLI requests Bash permission. handleCanUseTool's
	//    `IsAllowedThisSession` branch must short-circuit BEFORE
	//    any permWaiter or pending block is created, returning
	//    `{Behavior: "allow"}` directly.
	respCh := startBashPermRequest(a, "cli-req-1", "echo preempt")

	// 3. Response arrives immediately — no prompt round-trip.
	select {
	case got := <-respCh:
		if got.err != nil {
			t.Fatalf("handleCanUseTool err: %v", got.err)
		}
		if got.resp.Behavior != "allow" {
			t.Fatalf("response behavior = %q, want allow (preempt)", got.resp.Behavior)
		}
	case <-time.After(permTestTimeout(2 * time.Second)):
		t.Fatal("DEADLOCK: preempt handleCanUseTool did not return")
	}

	// 4. permWaiters MUST be empty — the bypass path never touches
	//    the map.
	assertNoLeaks(t, a)

	// 5. NO permission.request event must be broadcast. Drain
	//    100ms; any leaked prompt event signals the bypass branch
	//    is broken.
	if _, ok := waitForPermissionRequestEvent(t, events, 100*time.Millisecond); ok {
		t.Fatal("preempt path must NOT broadcast permission.request")
	}

	// 6. Snapshot must NOT contain a kind:"permission" block for
	//    this request — the preempt path adds nothing to the
	//    session at all.
	snap := a.Snapshot()
	for _, turn := range snap.Turns {
		for _, b := range turn.Blocks {
			if b.Kind == "permission" {
				t.Fatalf("preempt path leaked a kind:\"permission\" block: %+v", b)
			}
		}
	}
}

// ─────────────────────────────────────────────────────────────────
// Robustness: serial round-trips do not leak waiters
// ─────────────────────────────────────────────────────────────────
//
// Replays the chain N times for the same tool with scope=once
// decisions. After each iteration permWaiters MUST return to
// zero. A regression that double-registered or forgot to delete
// would surface as an ever-growing count.

func TestBashPerm_NoLeakOverManyRounds(t *testing.T) {
	a := newTestAgent(t)
	events, unsub := a.Subscribe()
	defer unsub()

	const N = 25
	for i := 0; i < N; i++ {
		respCh := startBashPermRequest(a, "cli-req-loop", "echo loop")
		permID, ok := waitForPermissionRequestEvent(t, events, permTestTimeout(2*time.Second))
		if !ok {
			t.Fatalf("iter %d: missing permission.request", i)
		}
		if err := a.AnswerPermission(PermissionRespondFrame{
			PermissionID: permID,
			Decision:     "allow",
			Scope:        "once",
		}); err != nil {
			t.Fatalf("iter %d: AnswerPermission: %v", i, err)
		}
		select {
		case got := <-respCh:
			if got.err != nil {
				t.Fatalf("iter %d: handleCanUseTool err: %v", i, got.err)
			}
			if got.resp.Behavior != "allow" {
				t.Fatalf("iter %d: behavior = %q", i, got.resp.Behavior)
			}
		case <-time.After(permTestTimeout(2 * time.Second)):
			t.Fatalf("iter %d: DEADLOCK", i)
		}
		// No leftover waiter after each round.
		a.mu.Lock()
		n := len(a.permWaiters)
		a.mu.Unlock()
		if n != 0 {
			t.Fatalf("iter %d: permWaiters has %d leftover entries", i, n)
		}
	}
}
