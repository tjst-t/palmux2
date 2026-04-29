package claudeagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestAskUserQuestion_FullRoundTrip drives the AskUserQuestion flow
// end-to-end at the package level: stream the tool_use envelope into
// the session, fire the MCP permission_prompt that backs the tool, and
// assert that AnswerAskQuestion resolves the permission with the
// CLI-shaped updatedInput. Mirrors the live S007 behaviour at the
// boundary between RequestPermission and the WS handler.
func TestAskUserQuestion_FullRoundTrip(t *testing.T) {
	a := newStandaloneAskAgent(t)

	events, unsub := a.Subscribe()
	defer unsub()

	// 1. Stream-event envelopes that produce a kind:"ask" block in the
	//    session — this is what the CLI emits before invoking the
	//    permission_prompt MCP tool.
	processStreamMessage(a.session, parse(t, `{"type":"stream_event","event":{"type":"message_start"}}`))
	startBody := `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_ask_e2e","name":"AskUserQuestion","input":{"questions":[{"question":"Pick a color","multiSelect":false,"options":[{"label":"red"},{"label":"green"},{"label":"blue"}]}]}}}}`
	for _, ev := range processStreamMessage(a.session, parse(t, startBody)) {
		a.broadcast(ev)
	}
	processStreamMessage(a.session, parse(t, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`))

	// 2. The MCP permission_prompt arrives — RequestPermission blocks
	//    on the user's answer. Run it on a goroutine so the test thread
	//    can drive AnswerAskQuestion.
	type respWithErr struct {
		resp permissionResponse
		err  error
	}
	respCh := make(chan respWithErr, 1)
	go func() {
		input := json.RawMessage(`{"questions":[{"question":"Pick a color","multiSelect":false,"options":[{"label":"red"},{"label":"green"},{"label":"blue"}]}]}`)
		resp, err := a.RequestPermission(context.Background(), "AskUserQuestion", input, "toolu_ask_e2e")
		respCh <- respWithErr{resp, err}
	}()

	// 3. Wait for an ask.question event to fan out — confirms the
	//    block was stamped with a permission_id and the FE would now
	//    enable its action row.
	permID := waitForAskQuestion(t, events, 2*time.Second)

	// 4. User submits "blue".
	if err := a.AnswerAskQuestion(AskRespondFrame{
		PermissionID: permID,
		Answers:      [][]string{{"blue"}},
	}); err != nil {
		t.Fatalf("AnswerAskQuestion: %v", err)
	}

	// 5. Verify the permission response: behavior=allow, updatedInput
	//    carries questionAnswers=[["blue"]].
	var got respWithErr
	select {
	case got = <-respCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RequestPermission did not return after AnswerAskQuestion")
	}
	if got.err != nil {
		t.Fatalf("RequestPermission err: %v", got.err)
	}
	if got.resp.Behavior != "allow" {
		t.Fatalf("response behavior = %q, want allow", got.resp.Behavior)
	}
	if len(got.resp.UpdatedInput) == 0 {
		t.Fatalf("response updatedInput is empty")
	}
	var updated map[string]any
	if err := json.Unmarshal(got.resp.UpdatedInput, &updated); err != nil {
		t.Fatalf("unmarshal updatedInput: %v", err)
	}
	answers, ok := updated["questionAnswers"]
	if !ok {
		t.Fatalf("updatedInput missing questionAnswers key: %v", updated)
	}
	// Round-trip via JSON to verify shape.
	answersJSON, _ := json.Marshal(answers)
	if got, want := string(answersJSON), `[["blue"]]`; got != want {
		t.Fatalf("questionAnswers = %s, want %s", got, want)
	}

	// 6. Verify the ask.decided event fanned out with the user's answer
	//    so other connected clients can flip the block to the decided
	//    view too. (The first ask.question is already drained.)
	if !waitForAskDecided(t, events, 1*time.Second, permID, "blue") {
		t.Fatal("did not observe ask.decided event with answer 'blue'")
	}

	// 7. Snapshot reflects the decided state on the kind:"ask" block.
	snap := a.Snapshot()
	var foundDecided bool
	for _, turn := range snap.Turns {
		for _, b := range turn.Blocks {
			if b.Kind != "ask" {
				continue
			}
			if !b.Done {
				continue
			}
			if len(b.AskAnswers) == 0 {
				continue
			}
			if !strings.Contains(string(b.AskAnswers), "blue") {
				continue
			}
			foundDecided = true
		}
	}
	if !foundDecided {
		t.Fatalf("snapshot has no decided ask block with 'blue': %+v", snap.Turns)
	}
}

// TestAskUserQuestion_MultiSelectRoundTrip checks that multi-select
// questions ship multiple labels per question intact.
func TestAskUserQuestion_MultiSelectRoundTrip(t *testing.T) {
	a := newStandaloneAskAgent(t)
	events, unsub := a.Subscribe()
	defer unsub()

	processStreamMessage(a.session, parse(t, `{"type":"stream_event","event":{"type":"message_start"}}`))
	startBody := `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_ask_multi","name":"AskUserQuestion","input":{"questions":[{"question":"Pick toppings","multiSelect":true,"options":[{"label":"cheese"},{"label":"olives"},{"label":"basil"}]}]}}}}`
	for _, ev := range processStreamMessage(a.session, parse(t, startBody)) {
		a.broadcast(ev)
	}
	processStreamMessage(a.session, parse(t, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`))

	type respWithErr struct {
		resp permissionResponse
		err  error
	}
	respCh := make(chan respWithErr, 1)
	go func() {
		resp, err := a.RequestPermission(
			context.Background(),
			"AskUserQuestion",
			json.RawMessage(`{"questions":[{"question":"Pick toppings","multiSelect":true,"options":[{"label":"cheese"},{"label":"olives"},{"label":"basil"}]}]}`),
			"toolu_ask_multi",
		)
		respCh <- respWithErr{resp, err}
	}()

	permID := waitForAskQuestion(t, events, 2*time.Second)
	if err := a.AnswerAskQuestion(AskRespondFrame{
		PermissionID: permID,
		Answers:      [][]string{{"cheese", "basil"}},
	}); err != nil {
		t.Fatalf("AnswerAskQuestion multi: %v", err)
	}

	var got respWithErr
	select {
	case got = <-respCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RequestPermission did not return")
	}
	if got.err != nil {
		t.Fatalf("err: %v", got.err)
	}
	if !strings.Contains(string(got.resp.UpdatedInput), `"cheese"`) ||
		!strings.Contains(string(got.resp.UpdatedInput), `"basil"`) {
		t.Fatalf("updatedInput should contain cheese and basil, got %s", got.resp.UpdatedInput)
	}
	if strings.Contains(string(got.resp.UpdatedInput), `"olives"`) {
		t.Fatalf("updatedInput should NOT contain olives (unselected): %s", got.resp.UpdatedInput)
	}
}

// newStandaloneAskAgent spins up an Agent with no Client / Manager —
// enough to exercise RequestPermission ↔ AnswerAskQuestion. The Manager
// fields that publishEvent / publishNotification reach into are nil, so
// those calls are no-ops (logged as such in publishEvent itself).
func newStandaloneAskAgent(t *testing.T) *Agent {
	t.Helper()
	deps := agentDeps{
		repoID:   "repo",
		branchID: "branch",
		worktree: "/tmp/fake",
	}
	a := newAgent(deps)
	a.deps.manager = &Manager{} // empty so publishEvent / publishNotification short-circuit
	return a
}

// waitForAskQuestion drains the events channel until an ask.question
// frame appears (or the timeout fires). Returns the contained
// permission_id.
func waitForAskQuestion(t *testing.T, events <-chan AgentEvent, timeout time.Duration) string {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("event channel closed before ask.question arrived")
			}
			if ev.Type != string(EvAskQuestion) {
				continue
			}
			var p AskQuestionPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("decode ask.question: %v", err)
			}
			if p.PermissionID == "" {
				t.Fatal("ask.question lacks permission_id")
			}
			return p.PermissionID
		case <-deadline.C:
			t.Fatal("timeout waiting for ask.question event")
		}
	}
}

// waitForAskDecided drains the events channel until an ask.decided
// event appears for the given permission_id, and asserts that the
// answers contain the given label. Returns false on timeout.
func waitForAskDecided(t *testing.T, events <-chan AgentEvent, timeout time.Duration, permID, expectedLabel string) bool {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return false
			}
			if ev.Type != string(EvAskDecided) {
				continue
			}
			var p AskDecidedPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				continue
			}
			if p.PermissionID != permID {
				continue
			}
			if !strings.Contains(string(p.Answers), expectedLabel) {
				continue
			}
			return true
		case <-deadline.C:
			return false
		}
	}
}
