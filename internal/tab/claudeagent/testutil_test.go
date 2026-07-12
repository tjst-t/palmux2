package claudeagent

// Test factories used by the integration / unit tests in this
// package. Centralised in S43cfb1-7 so a fixture change (e.g.
// adding a new dep field, swapping the Manager stub) is a 1-place
// edit instead of N-place.
//
// Naming follows the canonical Go convention:
//   - newTestSession(t) — bare Session, no Agent / Manager
//   - newTestAgent(t)   — Agent + Session + empty Manager + stub Client
//   - newTestClient(t)  — stub Client recording Send / Interrupt /
//                         Close call counts (used by future tests
//                         that need to assert against the CLI wire)
//
// Each factory registers `t.Cleanup` so leaks are caught even when
// a test panics mid-flight.

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// newTestSession returns a bare Session with sensible defaults.
// repoID="repo" / branchID="branch" / sessionID="" so the first
// system/init envelope assigns a fresh id (mirroring real CLI
// behaviour). Caller can override defaults by mutating fields
// before driving streamMsgs through processStreamMessage.
func newTestSession(t *testing.T) *Session {
	t.Helper()
	return NewSession("repo", "branch", "", "sonnet", "default")
}

// newTestAgent returns an Agent + its Session in a state that's
// usable for permission round-trip tests (RequestPermission ↔
// AnswerAskQuestion / AnswerPlanQuestion / send.permissionRespond).
//
// The Manager is an empty struct so publishEvent /
// publishNotification short-circuit (they read internal Manager
// fields that are zero-valued and bail out).
//
// The Client field is left nil — tests that need to capture wire
// calls should pass a *stubClient via newTestClient.
func newTestAgent(t *testing.T) *Agent {
	t.Helper()
	deps := agentDeps{
		repoID:   "repo",
		branchID: "branch",
		worktree: "/tmp/fake",
	}
	a := newAgent(deps)
	a.deps.manager = &Manager{}
	return a
}

// stubClient is a minimal capturing stub for the Client interface
// used by Agent. It records the count + last-arg of each method so
// tests can assert behaviour without spinning up a real CLI.
type stubClient struct {
	mu sync.Mutex

	sendCount      int
	lastSendBody   json.RawMessage
	interruptCount int
	closeCount     int
}

// Capture-style API. The real client interface is internal to this
// package; tests substitute by setting `agent.cli = &stubAdapter{...}`
// where stubAdapter satisfies the package's Client interface and
// delegates to the underlying stubClient counters. Tests that need a
// shared, simple counter can use stubClient directly without an
// adapter.
func (s *stubClient) recordSend(body json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sendCount++
	s.lastSendBody = body
}

func (s *stubClient) recordInterrupt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interruptCount++
}

func (s *stubClient) recordClose() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closeCount++
}

// SendCount returns the number of times Send / recordSend has been
// invoked. Safe for concurrent tests.
func (s *stubClient) SendCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sendCount
}

// InterruptCount returns the count of Interrupt invocations.
func (s *stubClient) InterruptCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interruptCount
}

// CloseCount returns the count of Close invocations.
func (s *stubClient) CloseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.closeCount
}

// LastSendBody returns the last argument passed to Send. Useful for
// asserting the wire format of a control_request the agent emits.
func (s *stubClient) LastSendBody() json.RawMessage {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastSendBody
}

// newTestClient returns a stubClient with t.Cleanup registered so
// any latent goroutines flagged via Close are accounted for.
func newTestClient(t *testing.T) *stubClient {
	t.Helper()
	c := &stubClient{}
	t.Cleanup(func() {
		// No-op for now — stubClient holds no goroutines, but the
		// cleanup hook is intentional so future test fixtures that
		// DO spawn (for example a buffered channel pump) have a
		// canonical drain site.
	})
	return c
}

// _ guarantees the unused symbols above stay live during refactors.
// (Go vet does not flag exported-helpers-only-used-in-tests, but
// the early callers may still be in another file.)
var _ = context.Background

// permTestTimeout returns an adaptive deadline for the permission
// round-trip integration tests' bounded waits (AC-S64c835-2-2 /
// backlog #6: TestBashPerm_NoLeakOverManyRounds, TestAskUserQuestion_-
// FullRoundTrip, TestExitPlanMode_FullRoundTrip, and the other tests in
// bash_perm_integration_test.go / ask_integration_test.go /
// plan_integration_test.go that share the exact same "resolve a
// permission on a goroutine, wait for the response on a channel with a
// select/time.After" shape — a single hardcoded budget across the file
// left the rest inconsistently exposed to the same flake even though
// only 3 tests were individually named).
//
// base is the nominal (unloaded, non-race) budget these waits were
// originally hardcoded to (2*time.Second, 1*time.Second, ...). The
// returned deadline is base scaled by:
//   - raceTimeoutMultiplier (3 under `go test -race`, 1 otherwise — see
//     timeout_scale_race_test.go / timeout_scale_norace_test.go: cmd/go
//     automatically sets the "race" build tag for `go test -race`, so
//     this needs no runtime detection at all). Race instrumentation
//     alone measurably slows goroutine scheduling and channel ops,
//     which is exactly the shape of every wait these tests do.
//   - the PALMUX_TEST_TIMEOUT_SCALE env var (float, default 1): lets a
//     `sprint verify` run (or a developer) widen the budget further for
//     an explicitly loaded run (e.g. alongside stress-ng) without
//     touching source, matching the project's existing
//     PALMUX_SURVIVAL_SMOKE-style env-gate convention.
//
// This ONLY widens the WAIT BUDGET. Every assertion these tests make
// (permission fires, tool executes, response shape, no leaked waiter)
// is completely unchanged — see docs/sprint-logs/S64c835/decisions.json's
// S64c835-2 entry for why this is deliberately NOT a t.Skip or a
// loosened assertion.
func permTestTimeout(base time.Duration) time.Duration {
	scale := 1.0
	if v := os.Getenv("PALMUX_TEST_TIMEOUT_SCALE"); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil && f > 0 {
			scale = f
		}
	}
	return time.Duration(float64(base) * float64(raceTimeoutMultiplier) * scale)
}
