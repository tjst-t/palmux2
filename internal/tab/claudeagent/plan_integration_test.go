package claudeagent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// TestExitPlanMode_FullRoundTrip drives the ExitPlanMode flow end-to-end:
// stream the tool_use envelope into the session, fire the MCP
// permission_prompt that backs the tool, and assert that AnswerPlanResponse
// resolves the permission with behavior:"allow" + (when the user edited
// the plan) updatedInput.plan.
//
// This is the analogue of TestAskUserQuestion_FullRoundTrip and locks in
// the S001-refine contract: ExitPlanMode no longer routes through the
// generic permission_request UI — there must be NO kind:"permission"
// block in the session for the plan permission, and the kind:"plan"
// block carries the decision instead.
func TestExitPlanMode_FullRoundTrip(t *testing.T) {
	a := newStandaloneAskAgent(t) // re-using helper from ask_integration_test.go
	events, unsub := a.Subscribe()
	defer unsub()

	// 1. Stream-event envelopes that produce a kind:"plan" block.
	processStreamMessage(a.session, parse(t, `{"type":"stream_event","event":{"type":"message_start"}}`))
	startBody := `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_plan_e2e","name":"ExitPlanMode","input":{"plan":"# Plan\n1. Refactor X\n2. Add tests"}}}}`
	for _, ev := range processStreamMessage(a.session, parse(t, startBody)) {
		a.broadcast(ev)
	}
	processStreamMessage(a.session, parse(t, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`))

	// 2. The MCP permission_prompt arrives — RequestPermission blocks
	//    until AnswerPlanResponse is called.
	type respWithErr struct {
		resp permissionResponse
		err  error
	}
	respCh := make(chan respWithErr, 1)
	go func() {
		input := json.RawMessage(`{"plan":"# Plan\n1. Refactor X\n2. Add tests"}`)
		resp, err := a.RequestPermission(context.Background(), "ExitPlanMode", input, "toolu_plan_e2e")
		respCh <- respWithErr{resp, err}
	}()

	// 3. plan.question event must fire (proves the bypass-path is wired).
	permID := waitForPlanQuestion(t, events, 2*time.Second)

	// 4. Snapshot must NOT contain a kind:"permission" block for this
	//    permission_id — that's the exact bug S001-refine fixes.
	pre := a.Snapshot()
	for _, turn := range pre.Turns {
		for _, b := range turn.Blocks {
			if b.Kind == "permission" && b.PermissionID == permID {
				t.Fatalf("kind:\"permission\" block leaked into session for plan permission %s", permID)
			}
		}
	}

	// 5. User clicks Approve with an edited plan and target mode=auto.
	if err := a.AnswerPlanResponse(PlanRespondFrame{
		PermissionID: permID,
		Decision:     "approve",
		TargetMode:   "auto",
		EditedPlan:   "# Edited Plan\n- run only the safe steps",
	}); err != nil {
		t.Fatalf("AnswerPlanResponse: %v", err)
	}

	var got respWithErr
	select {
	case got = <-respCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RequestPermission did not return after AnswerPlanResponse")
	}
	if got.err != nil {
		t.Fatalf("RequestPermission err: %v", got.err)
	}
	if got.resp.Behavior != "allow" {
		t.Fatalf("response behavior = %q, want allow", got.resp.Behavior)
	}
	if !strings.Contains(string(got.resp.UpdatedInput), "Edited Plan") {
		t.Fatalf("updatedInput should contain edited markdown, got %s", got.resp.UpdatedInput)
	}

	// 6. plan.decided event must fan out so cross-tab clients flip too.
	if !waitForPlanDecided(t, events, 1*time.Second, permID, "approved", "auto") {
		t.Fatal("did not observe plan.decided event with decision=approved targetMode=auto")
	}

	// 7. Snapshot reflects the decided state on the kind:"plan" block.
	snap := a.Snapshot()
	var foundPlan bool
	for _, turn := range snap.Turns {
		for _, b := range turn.Blocks {
			if b.Kind != "plan" {
				continue
			}
			if b.PermissionID != permID {
				continue
			}
			if b.PlanDecision != "approved" {
				t.Fatalf("plan block decision = %q, want approved", b.PlanDecision)
			}
			if b.PlanTargetMode != "auto" {
				t.Fatalf("plan block targetMode = %q, want auto", b.PlanTargetMode)
			}
			if !strings.Contains(string(b.Input), "Edited Plan") {
				t.Fatalf("plan block input should hold edited markdown, got %s", b.Input)
			}
			foundPlan = true
		}
	}
	if !foundPlan {
		t.Fatalf("snapshot has no decided plan block: %+v", snap.Turns)
	}
}

// TestExitPlanMode_RejectKeepsPlanMode locks in the deny path: clicking
// Keep planning resolves the CLI permission with behavior:"deny" and a
// "User chose to keep planning" message; the plan block flips to
// planDecision="rejected".
func TestExitPlanMode_RejectKeepsPlanMode(t *testing.T) {
	a := newStandaloneAskAgent(t)
	events, unsub := a.Subscribe()
	defer unsub()

	processStreamMessage(a.session, parse(t, `{"type":"stream_event","event":{"type":"message_start"}}`))
	startBody := `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_plan_rej","name":"ExitPlanMode","input":{"plan":"# Plan\n1. dangerous step"}}}}`
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
		input := json.RawMessage(`{"plan":"# Plan\n1. dangerous step"}`)
		resp, err := a.RequestPermission(context.Background(), "ExitPlanMode", input, "toolu_plan_rej")
		respCh <- respWithErr{resp, err}
	}()

	permID := waitForPlanQuestion(t, events, 2*time.Second)

	if err := a.AnswerPlanResponse(PlanRespondFrame{
		PermissionID: permID,
		Decision:     "reject",
	}); err != nil {
		t.Fatalf("AnswerPlanResponse reject: %v", err)
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
	if got.resp.Behavior != "deny" {
		t.Fatalf("response behavior = %q, want deny", got.resp.Behavior)
	}
	if !strings.Contains(got.resp.Message, "keep planning") {
		t.Fatalf("response message should mention 'keep planning', got %q", got.resp.Message)
	}
}

func waitForPlanQuestion(t *testing.T, events <-chan AgentEvent, timeout time.Duration) string {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				t.Fatal("event channel closed before plan.question arrived")
			}
			if ev.Type != string(EvPlanQuestion) {
				continue
			}
			var p PlanQuestionPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				t.Fatalf("decode plan.question: %v", err)
			}
			if p.PermissionID == "" {
				t.Fatal("plan.question lacks permission_id")
			}
			return p.PermissionID
		case <-deadline.C:
			t.Fatal("timeout waiting for plan.question event")
		}
	}
}

func waitForPlanDecided(t *testing.T, events <-chan AgentEvent, timeout time.Duration, permID, decision, targetMode string) bool {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		select {
		case ev, ok := <-events:
			if !ok {
				return false
			}
			if ev.Type != string(EvPlanDecided) {
				continue
			}
			var p PlanDecidedPayload
			if err := json.Unmarshal(ev.Payload, &p); err != nil {
				continue
			}
			if p.PermissionID != permID {
				continue
			}
			if p.Decision != decision {
				continue
			}
			if targetMode != "" && p.TargetMode != targetMode {
				continue
			}
			return true
		case <-deadline.C:
			return false
		}
	}
}
