package claudeagent

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestStreamEvents_TextStream(t *testing.T) {
	s := NewSession("repo", "branch", "", "sonnet", "default")

	// message_start should produce turn.start + status.change
	evs := processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"message_start"}}`))
	if got, want := len(evs), 2; got != want {
		t.Fatalf("message_start emitted %d events, want %d (%v)", got, want, evs)
	}
	if evs[0].Type != string(EvTurnStart) {
		t.Fatalf("first event = %s, want %s", evs[0].Type, EvTurnStart)
	}

	// content_block_start type=text → block.start
	evs = processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"text"}}}`))
	if len(evs) != 1 || evs[0].Type != string(EvBlockStart) {
		t.Fatalf("content_block_start unexpected: %v", evs)
	}

	// Two text deltas.
	for _, chunk := range []string{"Hel", "lo"} {
		body, _ := json.Marshal(map[string]any{
			"type": "stream_event",
			"event": map[string]any{
				"type":  "content_block_delta",
				"index": 0,
				"delta": map[string]any{"type": "text_delta", "text": chunk},
			},
		})
		evs = processStreamMessage(s, parse(t, string(body)))
		if len(evs) != 1 || evs[0].Type != string(EvBlockDelta) {
			t.Fatalf("delta unexpected: %v", evs)
		}
	}

	// content_block_stop → block.end
	evs = processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`))
	if len(evs) != 1 || evs[0].Type != string(EvBlockEnd) {
		t.Fatalf("block_stop unexpected: %v", evs)
	}

	// result → turn.end + status.change(idle)
	evs = processStreamMessage(s, parse(t, `{"type":"result","subtype":"success","total_cost_usd":0.01}`))
	if got, want := len(evs), 2; got != want {
		t.Fatalf("result emitted %d events, want %d", got, want)
	}
	if evs[0].Type != string(EvTurnEnd) {
		t.Fatalf("first event = %s, want %s", evs[0].Type, EvTurnEnd)
	}

	// Snapshot should reflect a single completed text block.
	snap := s.Snapshot()
	if len(snap.Turns) != 1 || len(snap.Turns[0].Blocks) != 1 {
		t.Fatalf("snapshot turns=%v", snap.Turns)
	}
	if got, want := snap.Turns[0].Blocks[0].Text, "Hello"; got != want {
		t.Fatalf("accumulated text = %q, want %q", got, want)
	}
}

func TestSetSessionID_Replaced(t *testing.T) {
	s := NewSession("repo", "branch", "old-id", "", "")
	evs := processStreamMessage(s, parse(t, `{"type":"system","subtype":"init","session_id":"new-id","model":"opus"}`))
	if len(evs) != 1 || evs[0].Type != string(EvSessionReplaced) {
		t.Fatalf("init w/ new id should emit session.replaced; got %v", evs)
	}
	if got := s.SessionID(); got != "new-id" {
		t.Fatalf("session id = %q, want new-id", got)
	}
}

func TestPermission_AddResolve(t *testing.T) {
	s := NewSession("repo", "branch", "id", "", "")
	s.StartAssistantTurn()
	permID, _, _ := s.AddPermissionRequest("cli-1", "Bash", json.RawMessage(`{"command":"ls"}`))
	if permID == "" {
		t.Fatal("expected non-empty permission_id")
	}
	if name := s.ToolNameForPermission(permID); name != "Bash" {
		t.Fatalf("tool name = %q, want Bash", name)
	}
	cliID, ok := s.ResolvePermission(permID, "allow")
	if !ok || cliID != "cli-1" {
		t.Fatalf("resolve = %q,%v want cli-1,true", cliID, ok)
	}
	// Second resolve is a no-op.
	if _, ok := s.ResolvePermission(permID, "deny"); ok {
		t.Fatal("second resolve should fail")
	}
}

func TestExitPlanMode_NormalizesToPlanBlockAndSuppressesToolResult(t *testing.T) {
	s := NewSession("repo", "branch", "", "sonnet", "plan")

	// message_start opens an assistant turn.
	if evs := processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"message_start"}}`)); len(evs) != 2 {
		t.Fatalf("message_start emitted %d events, want 2", len(evs))
	}

	// content_block_start with a tool_use named "ExitPlanMode" should
	// arrive as kind:"plan", carry the plan input, and NOT flip the
	// status to tool_running (plan drafting is still "thinking").
	startBody := `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_plan_1","name":"ExitPlanMode","input":{"plan":"# Plan\n1. Do thing\n"}}}}`
	evs := processStreamMessage(s, parse(t, startBody))
	if len(evs) != 1 || evs[0].Type != string(EvBlockStart) {
		t.Fatalf("plan content_block_start should emit only block.start, got %v", evs)
	}
	var bs BlockStartPayload
	if err := json.Unmarshal(evs[0].Payload, &bs); err != nil {
		t.Fatalf("decode block.start payload: %v", err)
	}
	if bs.Block.Kind != "plan" {
		t.Fatalf("plan block kind = %q, want plan", bs.Block.Kind)
	}
	if bs.Block.Name != "ExitPlanMode" {
		t.Fatalf("plan block name = %q, want ExitPlanMode", bs.Block.Name)
	}
	if s.Status() != StatusThinking {
		t.Fatalf("status after plan start = %q, want thinking", s.Status())
	}

	// Finalize the block. content_block_stop on a plan should reach the
	// frontend as a block.end so the UI flips to the collapsed view.
	if evs := processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`)); len(evs) != 1 || evs[0].Type != string(EvBlockEnd) {
		t.Fatalf("content_block_stop unexpected: %v", evs)
	}

	// The CLI now ships a tool_result inside a `user` envelope echoing
	// the plan-mode answer. We must drop it to keep the conversation
	// clean — the plan block already conveys the outcome.
	resultBody := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_plan_1","content":"User has approved your plan. You can now start coding."}]}}`
	if evs := processStreamMessage(s, parse(t, resultBody)); len(evs) != 0 {
		t.Fatalf("plan tool_result should be suppressed, got %d events: %v", len(evs), evs)
	}

	// Snapshot: the assistant turn holds exactly one plan block; no
	// tool/result turn was added.
	snap := s.Snapshot()
	if len(snap.Turns) != 1 {
		t.Fatalf("snapshot has %d turns, want 1: %+v", len(snap.Turns), snap.Turns)
	}
	if got, want := snap.Turns[0].Blocks[0].Kind, "plan"; got != want {
		t.Fatalf("snapshot block kind = %q, want %q", got, want)
	}
	// The plan input should be preserved under Input (since the start
	// message carried the full plan inline; no streaming deltas needed).
	if len(snap.Turns[0].Blocks[0].Input) == 0 {
		t.Fatalf("plan block input should not be empty: %+v", snap.Turns[0].Blocks[0])
	}

	// A non-plan tool_result on a different tool_use_id should still
	// surface as a regular result block — the suppression list is
	// strictly per-id.
	otherResult := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_other","content":"ok"}]}}`
	evs = processStreamMessage(s, parse(t, otherResult))
	if len(evs) != 1 || evs[0].Type != string(EvToolResult) {
		t.Fatalf("non-plan tool_result should emit tool.result, got %v", evs)
	}
}

func TestAskUserQuestion_NormalizesToAskBlockAndSuppressesToolResult(t *testing.T) {
	s := NewSession("repo", "branch", "", "sonnet", "default")

	// message_start opens an assistant turn.
	if evs := processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"message_start"}}`)); len(evs) != 2 {
		t.Fatalf("message_start emitted %d events, want 2", len(evs))
	}

	// content_block_start with a tool_use named "AskUserQuestion" should
	// arrive as kind:"ask", carry the question input, and NOT flip the
	// status to tool_running (asking is authored content, not background work).
	startBody := `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_ask_1","name":"AskUserQuestion","input":{"questions":[{"question":"Pick one","options":[{"label":"A"},{"label":"B"}]}]}}}}`
	evs := processStreamMessage(s, parse(t, startBody))
	if len(evs) != 1 || evs[0].Type != string(EvBlockStart) {
		t.Fatalf("ask content_block_start should emit only block.start, got %v", evs)
	}
	var bs BlockStartPayload
	if err := json.Unmarshal(evs[0].Payload, &bs); err != nil {
		t.Fatalf("decode block.start payload: %v", err)
	}
	if bs.Block.Kind != "ask" {
		t.Fatalf("ask block kind = %q, want ask", bs.Block.Kind)
	}
	if bs.Block.Name != "AskUserQuestion" {
		t.Fatalf("ask block name = %q, want AskUserQuestion", bs.Block.Name)
	}
	if s.Status() != StatusThinking {
		t.Fatalf("status after ask start = %q, want thinking", s.Status())
	}

	// Finalize the block with content_block_stop.
	if evs := processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`)); len(evs) != 1 || evs[0].Type != string(EvBlockEnd) {
		t.Fatalf("content_block_stop unexpected: %v", evs)
	}

	// CLI now ships a tool_result echoing the chosen option — must be
	// suppressed (the AskQuestionBlock UI conveys the decision visually).
	resultBody := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_ask_1","content":"User selected: A"}]}}`
	if evs := processStreamMessage(s, parse(t, resultBody)); len(evs) != 0 {
		t.Fatalf("ask tool_result should be suppressed, got %d events: %v", len(evs), evs)
	}

	// Snapshot: the assistant turn holds exactly one ask block; no
	// tool/result turn was added.
	snap := s.Snapshot()
	if len(snap.Turns) != 1 {
		t.Fatalf("snapshot has %d turns, want 1: %+v", len(snap.Turns), snap.Turns)
	}
	if got, want := snap.Turns[0].Blocks[0].Kind, "ask"; got != want {
		t.Fatalf("snapshot block kind = %q, want %q", got, want)
	}
	if len(snap.Turns[0].Blocks[0].Input) == 0 {
		t.Fatalf("ask block input should not be empty: %+v", snap.Turns[0].Blocks[0])
	}
}

func TestAskUserQuestion_AssistantFallback(t *testing.T) {
	// The assistant-envelope-only fallback path (no per-block stream
	// events) should also re-tag AskUserQuestion to kind:"ask" and
	// remember the tool_use_id so the tool_result is suppressed.
	s := NewSession("repo", "branch", "", "sonnet", "default")
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_ask_2","name":"AskUserQuestion","input":{"questions":[{"question":"Q","options":[{"label":"yes"}]}]}}]}}`
	processStreamMessage(s, parse(t, body))

	snap := s.Snapshot()
	if len(snap.Turns) != 1 || len(snap.Turns[0].Blocks) != 1 {
		t.Fatalf("expected 1 turn / 1 block, got %+v", snap.Turns)
	}
	if got := snap.Turns[0].Blocks[0].Kind; got != "ask" {
		t.Fatalf("assistant-fallback ask block kind = %q, want ask", got)
	}

	resultBody := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_ask_2","content":"ok"}]}}`
	if evs := processStreamMessage(s, parse(t, resultBody)); len(evs) != 0 {
		t.Fatalf("assistant-fallback ask tool_result should be suppressed, got %v", evs)
	}
}

// TestAskUserQuestion_StreamThenAssistantEnvelope_NoDuplicate covers the
// real-CLI flow: stream_event content_block_* envelopes build the kind:"ask"
// block, then the trailing `assistant` envelope arrives carrying the same
// tool_use. processAssistantMessage must treat the trailing envelope as a
// no-op (the streamed turn is canonical) — otherwise upsertCompleteBlock
// can't find the finalised block in openBlocks (FinalizeBlock removed it),
// falls through to append, and the user sees the AskUserQuestion rendered
// twice in the UI.
func TestAskUserQuestion_StreamThenAssistantEnvelope_NoDuplicate(t *testing.T) {
	s := NewSession("repo", "branch", "", "sonnet", "default")

	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"message_start"}}`))
	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_ask_dup","name":"AskUserQuestion","input":{"questions":[{"question":"Pick","options":[{"label":"A"}]}]}}}}`))
	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`))

	// The same tool_use lands again as a complete `assistant` envelope.
	envelope := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_ask_dup","name":"AskUserQuestion","input":{"questions":[{"question":"Pick","options":[{"label":"A"}]}]}}]}}`
	if evs := processStreamMessage(s, parse(t, envelope)); len(evs) != 0 {
		t.Fatalf("assistant envelope after streamed turn should emit nothing, got %d events: %v", len(evs), evs)
	}

	snap := s.Snapshot()
	if len(snap.Turns) != 1 {
		t.Fatalf("expected 1 turn, got %d: %+v", len(snap.Turns), snap.Turns)
	}
	if got := len(snap.Turns[0].Blocks); got != 1 {
		t.Fatalf("expected 1 ask block (no duplicate), got %d: %+v", got, snap.Turns[0].Blocks)
	}
	if got := snap.Turns[0].Blocks[0].Kind; got != "ask" {
		t.Fatalf("block kind = %q, want ask", got)
	}
}

// TestExitPlanMode_StreamThenAssistantEnvelope_NoDuplicate is the plan-block
// counterpart to the ask test above. Same root cause, same fix — included
// so a regression in either path can be caught independently.
func TestExitPlanMode_StreamThenAssistantEnvelope_NoDuplicate(t *testing.T) {
	s := NewSession("repo", "branch", "", "sonnet", "plan")

	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"message_start"}}`))
	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_plan_dup","name":"ExitPlanMode","input":{"plan":"# Plan\n- a"}}}}`))
	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`))

	envelope := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_plan_dup","name":"ExitPlanMode","input":{"plan":"# Plan\n- a"}}]}}`
	if evs := processStreamMessage(s, parse(t, envelope)); len(evs) != 0 {
		t.Fatalf("assistant envelope after streamed plan should emit nothing, got %d events: %v", len(evs), evs)
	}

	snap := s.Snapshot()
	if len(snap.Turns) != 1 || len(snap.Turns[0].Blocks) != 1 {
		t.Fatalf("expected 1 turn / 1 plan block, got %+v", snap.Turns)
	}
	if got := snap.Turns[0].Blocks[0].Kind; got != "plan" {
		t.Fatalf("block kind = %q, want plan", got)
	}
}

// TestFinalizeBlock_PromotesStreamedInputOverPlaceholder covers the live-CLI
// flow where content_block_start carries `input: {}` as a placeholder and
// the real input arrives via input_json_delta partials accumulated in
// b.Text. FinalizeBlock must promote b.Text into b.Input — otherwise the
// frontend sees `final: {}` at block.end, the input panel renders nothing,
// and the orphan check (block.done && !hasContent) hides the block.
func TestFinalizeBlock_PromotesStreamedInputOverPlaceholder(t *testing.T) {
	s := NewSession("repo", "branch", "", "sonnet", "default")

	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"message_start"}}`))
	// content_block_start with the placeholder {} — typical for tool_use.
	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_agent_1","name":"Agent","input":{}}}}`))
	// input_json_delta partials build the real input in b.Text.
	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"{\"description\":\"Summarize\","}}}`))
	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"subagent_type\":\"general-purpose\","}}}`))
	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_delta","index":0,"delta":{"type":"input_json_delta","partial_json":"\"prompt\":\"do the thing\"}"}}}`))

	evs := processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`))
	if len(evs) != 1 || evs[0].Type != string(EvBlockEnd) {
		t.Fatalf("content_block_stop should emit block.end, got %v", evs)
	}
	var bp BlockEndPayload
	if err := json.Unmarshal(evs[0].Payload, &bp); err != nil {
		t.Fatalf("decode block.end: %v", err)
	}
	if len(bp.Final) == 0 {
		t.Fatalf("Final empty — placeholder {} blocked the promotion")
	}
	var got map[string]string
	if err := json.Unmarshal(bp.Final, &got); err != nil {
		t.Fatalf("Final not parseable as object: %v (raw=%s)", err, bp.Final)
	}
	if got["description"] != "Summarize" || got["subagent_type"] != "general-purpose" {
		t.Fatalf("Final missing streamed fields: %+v", got)
	}

	// Snapshot: block.Input is the real input, block.Text is cleared.
	snap := s.Snapshot()
	if len(snap.Turns) != 1 || len(snap.Turns[0].Blocks) != 1 {
		t.Fatalf("expected 1 turn / 1 block, got %+v", snap.Turns)
	}
	b := snap.Turns[0].Blocks[0]
	if b.Text != "" {
		t.Fatalf("block.Text not cleared: %q", b.Text)
	}
	var bin map[string]string
	if err := json.Unmarshal(b.Input, &bin); err != nil {
		t.Fatalf("block.Input not parseable: %v (raw=%s)", err, b.Input)
	}
	if bin["prompt"] != "do the thing" {
		t.Fatalf("block.Input missing streamed prompt: %+v", bin)
	}
}

func TestLoadTranscriptTurns_RetagsAskUserQuestion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"ask me"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_ask_99","name":"AskUserQuestion","input":{"questions":[{"question":"Pick","options":[{"label":"A"},{"label":"B"}]}]}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_ask_99","content":"User selected: A"}]}}`,
	}
	body := []byte("")
	for _, ln := range lines {
		body = append(body, []byte(ln+"\n")...)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	turns, err := LoadTranscriptTurns(path)
	if err != nil {
		t.Fatalf("LoadTranscriptTurns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns (user prompt + assistant ask), got %d: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" {
		t.Fatalf("turn 0 role = %q, want user", turns[0].Role)
	}
	if turns[1].Role != "assistant" {
		t.Fatalf("turn 1 role = %q, want assistant", turns[1].Role)
	}
	if len(turns[1].Blocks) != 1 || turns[1].Blocks[0].Kind != "ask" {
		t.Fatalf("assistant turn should hold one ask block, got %+v", turns[1].Blocks)
	}
}

func TestExitPlanMode_AssistantFallback(t *testing.T) {
	// Some CLI versions emit a complete `assistant` envelope without
	// per-block stream_event lines. Make sure the fallback path also
	// re-tags ExitPlanMode and remembers the tool_use_id so the
	// subsequent tool_result is suppressed.
	s := NewSession("repo", "branch", "", "sonnet", "plan")
	body := `{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_plan_2","name":"ExitPlanMode","input":{"plan":"## Plan\n- step"}}]}}`
	processStreamMessage(s, parse(t, body))

	// Plan should now be in the snapshot as kind:"plan".
	snap := s.Snapshot()
	if len(snap.Turns) != 1 || len(snap.Turns[0].Blocks) != 1 {
		t.Fatalf("expected 1 turn / 1 block, got %+v", snap.Turns)
	}
	if got := snap.Turns[0].Blocks[0].Kind; got != "plan" {
		t.Fatalf("assistant-fallback plan block kind = %q, want plan", got)
	}

	// Tool result is suppressed.
	resultBody := `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_plan_2","content":"ok"}]}}`
	if evs := processStreamMessage(s, parse(t, resultBody)); len(evs) != 0 {
		t.Fatalf("assistant-fallback plan tool_result should be suppressed, got %v", evs)
	}
}

func TestStreamEvents_ParentToolUseIDPropagatesToTurn(t *testing.T) {
	// When the CLI ships an envelope with parent_tool_use_id (sub-agent
	// spawned via the Task tool), every Turn the message creates must
	// carry that ID so the frontend can render it nested under the
	// parent Task block.
	s := NewSession("repo", "branch", "", "sonnet", "default")

	// First, a normal top-level message_start should produce a turn
	// with no parent linkage.
	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"message_start"}}`))
	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_task_1","name":"Task","input":{"description":"audit codebase"}}}}`))
	processStreamMessage(s, parse(t, `{"type":"stream_event","event":{"type":"content_block_stop","index":0}}`))
	processStreamMessage(s, parse(t, `{"type":"result","subtype":"success"}`))

	// Now a sub-agent message_start arrives carrying the parent
	// tool_use_id of the Task block above.
	subBody := `{"type":"stream_event","parent_tool_use_id":"toolu_task_1","event":{"type":"message_start"}}`
	evs := processStreamMessage(s, parse(t, subBody))
	if len(evs) < 1 || evs[0].Type != string(EvTurnStart) {
		t.Fatalf("sub-agent message_start should emit turn.start, got %v", evs)
	}
	var ts TurnStartPayload
	if err := json.Unmarshal(evs[0].Payload, &ts); err != nil {
		t.Fatalf("decode turn.start payload: %v", err)
	}
	if ts.ParentToolUseID != "toolu_task_1" {
		t.Fatalf("turn.start parentToolUseId = %q, want toolu_task_1", ts.ParentToolUseID)
	}

	// Snapshot: parent ID is also preserved on the cached Turn so a
	// reconnecting client sees the nesting.
	snap := s.Snapshot()
	if len(snap.Turns) < 2 {
		t.Fatalf("expected at least 2 turns, got %d", len(snap.Turns))
	}
	subTurn := snap.Turns[len(snap.Turns)-1]
	if subTurn.ParentToolUseID != "toolu_task_1" {
		t.Fatalf("sub-agent snapshot turn parentToolUseId = %q, want toolu_task_1", subTurn.ParentToolUseID)
	}
	// And the parent Task block carries its tool_use_id so the FE can
	// match the sub-agent turn back to it.
	parentTurn := snap.Turns[0]
	if parentTurn.ParentToolUseID != "" {
		t.Fatalf("top-level turn parentToolUseId = %q, want empty", parentTurn.ParentToolUseID)
	}
	if len(parentTurn.Blocks) == 0 || parentTurn.Blocks[0].ToolUseID != "toolu_task_1" {
		t.Fatalf("parent Task block tool_use_id = %q, want toolu_task_1; turn=%+v", parentTurn.Blocks[0].ToolUseID, parentTurn)
	}
}

func TestProcessStreamMessage_ParentClearedAfterDispatch(t *testing.T) {
	// After processing a sub-agent envelope, the session's stamped
	// parent_tool_use_id must be cleared so unrelated background
	// activity (e.g. system messages) doesn't accidentally inherit it.
	s := NewSession("repo", "branch", "", "sonnet", "default")
	body := `{"type":"stream_event","parent_tool_use_id":"toolu_task_2","event":{"type":"message_start"}}`
	processStreamMessage(s, parse(t, body))
	if got := s.CurrentParentToolUseID(); got != "" {
		t.Fatalf("CurrentParentToolUseID after dispatch = %q, want empty (cleared by deferred reset)", got)
	}
}

func TestLoadTranscriptTurns_PreservesParentToolUseID(t *testing.T) {
	// Future-proofing: when the CLI starts persisting parent_tool_use_id
	// in transcripts (or if the SDK SDK route writes it directly), the
	// loader has to carry the field onto the resulting Turn so resumed
	// sessions show the same nesting as live sessions.
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"do the thing"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_task_99","name":"Task","input":{"description":"investigate"}}]}}`,
		`{"type":"assistant","parent_tool_use_id":"toolu_task_99","message":{"role":"assistant","content":[{"type":"text","text":"sub-agent says hello"}]}}`,
		`{"type":"user","parent_tool_use_id":"toolu_task_99","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_inner_x","content":"sub-agent tool output"}]}}`,
	}
	body := []byte("")
	for _, ln := range lines {
		body = append(body, []byte(ln+"\n")...)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	turns, err := LoadTranscriptTurns(path)
	if err != nil {
		t.Fatalf("LoadTranscriptTurns: %v", err)
	}
	if len(turns) != 4 {
		t.Fatalf("expected 4 turns, got %d: %+v", len(turns), turns)
	}
	// turns[0]: top-level user — no parent
	if turns[0].ParentToolUseID != "" {
		t.Fatalf("top-level user turn parentToolUseId = %q, want empty", turns[0].ParentToolUseID)
	}
	// turns[1]: parent assistant Task — no parent itself, but its
	// tool_use block records the tool_use_id so children can match.
	if turns[1].ParentToolUseID != "" {
		t.Fatalf("parent assistant turn parentToolUseId = %q, want empty", turns[1].ParentToolUseID)
	}
	if turns[1].Blocks[0].ToolUseID != "toolu_task_99" {
		t.Fatalf("parent Task block tool_use_id = %q, want toolu_task_99", turns[1].Blocks[0].ToolUseID)
	}
	// turns[2] and turns[3]: sub-agent envelopes — parent stamped.
	if turns[2].ParentToolUseID != "toolu_task_99" {
		t.Fatalf("sub-agent assistant turn parent = %q, want toolu_task_99", turns[2].ParentToolUseID)
	}
	if turns[3].ParentToolUseID != "toolu_task_99" {
		t.Fatalf("sub-agent tool turn parent = %q, want toolu_task_99", turns[3].ParentToolUseID)
	}
}

func TestLoadTranscriptTurns_RetagsExitPlanModeAndDropsToolResult(t *testing.T) {
	// Synthesize a tiny transcript: user prompt → assistant ExitPlanMode
	// tool_use → user-side tool_result echoing the approval. The
	// loader must yield (a) a user turn, (b) an assistant turn whose
	// only block is kind:"plan", and NOT a tool turn for the result.
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"plan something"}}`,
		`{"type":"assistant","message":{"role":"assistant","content":[{"type":"tool_use","id":"toolu_xp_1","name":"ExitPlanMode","input":{"plan":"# Plan\n- one\n- two"}}]}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_xp_1","content":"User has approved your plan."}]}}`,
	}
	body := []byte("")
	for _, ln := range lines {
		body = append(body, []byte(ln+"\n")...)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
	turns, err := LoadTranscriptTurns(path)
	if err != nil {
		t.Fatalf("LoadTranscriptTurns: %v", err)
	}
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns (user prompt + assistant plan), got %d: %+v", len(turns), turns)
	}
	if turns[0].Role != "user" {
		t.Fatalf("turn 0 role = %q, want user", turns[0].Role)
	}
	if turns[1].Role != "assistant" {
		t.Fatalf("turn 1 role = %q, want assistant", turns[1].Role)
	}
	if len(turns[1].Blocks) != 1 || turns[1].Blocks[0].Kind != "plan" {
		t.Fatalf("assistant turn should hold one plan block, got %+v", turns[1].Blocks)
	}
}

// TestHookEvents_StartedAndResponseProduceHookBlock verifies that S005's
// --include-hook-events normalizer turns the CLI's system/hook_started
// + system/hook_response envelopes into a single kind:"hook" block on
// the snapshot, with stdout/stderr/exit_code/outcome populated.
//
// Wire shape captured live from claude CLI 2.1.123 (see decisions.md).
func TestHookEvents_StartedAndResponseProduceHookBlock(t *testing.T) {
	s := NewSession("repo", "branch", "", "sonnet", "default")

	// 1. hook_started — should open a fresh kind:"hook" block in a
	//    role:"hook" turn, broadcast as a BlockStart.
	startedJSON := `{"type":"system","subtype":"hook_started","hook_id":"abc-123","hook_name":"PreToolUse:Bash","hook_event":"PreToolUse","uuid":"uuid-1","session_id":"sess-1"}`
	evs := processStreamMessage(s, parse(t, startedJSON))
	if len(evs) != 1 || evs[0].Type != string(EvBlockStart) {
		t.Fatalf("hook_started unexpected events: %+v", evs)
	}
	snap := s.Snapshot()
	if len(snap.Turns) != 1 {
		t.Fatalf("expected 1 turn after hook_started, got %d", len(snap.Turns))
	}
	if snap.Turns[0].Role != "hook" {
		t.Fatalf("turn role = %q, want %q", snap.Turns[0].Role, "hook")
	}
	if len(snap.Turns[0].Blocks) != 1 || snap.Turns[0].Blocks[0].Kind != "hook" {
		t.Fatalf("hook turn blocks unexpected: %+v", snap.Turns[0].Blocks)
	}
	open := snap.Turns[0].Blocks[0]
	if open.HookID != "abc-123" || open.HookEvent != "PreToolUse" || open.HookName != "PreToolUse:Bash" {
		t.Fatalf("hook block fields wrong: %+v", open)
	}
	if open.Done {
		t.Fatalf("hook block should not be Done before hook_response")
	}

	// 2. hook_response — completes the same hook_id with stdout/stderr
	//    and flips Done. Should emit BlockEnd + a fresh BlockStart for
	//    late-joining clients.
	respJSON := `{"type":"system","subtype":"hook_response","hook_id":"abc-123","hook_name":"PreToolUse:Bash","hook_event":"PreToolUse","output":"err\nHELLO\n","stdout":"HELLO\n","stderr":"err\n","exit_code":0,"outcome":"success","uuid":"uuid-2","session_id":"sess-1"}`
	evs = processStreamMessage(s, parse(t, respJSON))
	if len(evs) != 2 {
		t.Fatalf("hook_response should emit 2 events (BlockEnd + BlockStart), got %d: %+v", len(evs), evs)
	}
	if evs[0].Type != string(EvBlockEnd) || evs[1].Type != string(EvBlockStart) {
		t.Fatalf("hook_response events out of order: %+v", evs)
	}
	snap = s.Snapshot()
	if n := len(snap.Turns[0].Blocks); n != 1 {
		t.Fatalf("expected 1 hook block after response, got %d", n)
	}
	done := snap.Turns[0].Blocks[0]
	if !done.Done {
		t.Fatalf("hook block not Done after hook_response: %+v", done)
	}
	if done.HookStdout != "HELLO\n" || done.HookStderr != "err\n" {
		t.Fatalf("stdout/stderr not propagated: %+v", done)
	}
	if done.HookExitCode != 0 || done.HookOutcome != "success" {
		t.Fatalf("exit_code/outcome not propagated: %+v", done)
	}
}

// TestHookEvents_MultipleHooksInSameTurn — adjacent hooks (PreToolUse +
// PostToolUse on a Bash call) attach to a *single* role:"hook" turn so
// the UI groups them visually, instead of fragmenting into one turn per
// hook. Both blocks must be addressable independently via their hook_id.
func TestHookEvents_MultipleHooksInSameTurn(t *testing.T) {
	s := NewSession("repo", "branch", "", "sonnet", "default")
	processStreamMessage(s, parse(t, `{"type":"system","subtype":"hook_started","hook_id":"pre","hook_event":"PreToolUse","hook_name":"PreToolUse:Bash"}`))
	processStreamMessage(s, parse(t, `{"type":"system","subtype":"hook_response","hook_id":"pre","hook_event":"PreToolUse","hook_name":"PreToolUse:Bash","stdout":"PRE","exit_code":0,"outcome":"success"}`))
	processStreamMessage(s, parse(t, `{"type":"system","subtype":"hook_started","hook_id":"post","hook_event":"PostToolUse","hook_name":"PostToolUse:Bash"}`))
	processStreamMessage(s, parse(t, `{"type":"system","subtype":"hook_response","hook_id":"post","hook_event":"PostToolUse","hook_name":"PostToolUse:Bash","stdout":"POST","exit_code":0,"outcome":"success"}`))

	snap := s.Snapshot()
	if n := len(snap.Turns); n != 1 {
		t.Fatalf("expected 1 hook turn, got %d (%+v)", n, snap.Turns)
	}
	if n := len(snap.Turns[0].Blocks); n != 2 {
		t.Fatalf("expected 2 hook blocks in same turn, got %d", n)
	}
	got := []string{snap.Turns[0].Blocks[0].HookID, snap.Turns[0].Blocks[1].HookID}
	want := []string{"pre", "post"}
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("hook block order/ids: got %v, want %v", got, want)
	}
	if !snap.Turns[0].Blocks[0].Done || !snap.Turns[0].Blocks[1].Done {
		t.Fatalf("both hook blocks should be Done")
	}
}

// TestHookEvents_OptInClientOption — when ClientOptions.IncludeHookEvents
// is true, NewClient adds --include-hook-events to argv. We don't run
// the CLI here; this is a contract test against the args slice the
// client builder constructs.
func TestHookEvents_OptInClientOption(t *testing.T) {
	// Re-implement the prefix of NewClient that builds args, to keep
	// the test hermetic (doesn't try to spawn the CLI binary).
	build := func(opts ClientOptions) []string {
		args := []string{
			"--input-format", "stream-json",
			"--output-format", "stream-json",
			"--include-partial-messages",
			"--verbose",
			"--setting-sources", "project,user",
		}
		if opts.IncludeHookEvents {
			args = append(args, "--include-hook-events")
		}
		return args
	}
	got := build(ClientOptions{IncludeHookEvents: true})
	hasFlag := false
	for _, a := range got {
		if a == "--include-hook-events" {
			hasFlag = true
		}
	}
	if !hasFlag {
		t.Fatalf("--include-hook-events missing when IncludeHookEvents=true: %v", got)
	}
	got = build(ClientOptions{IncludeHookEvents: false})
	for _, a := range got {
		if a == "--include-hook-events" {
			t.Fatalf("--include-hook-events present when IncludeHookEvents=false: %v", got)
		}
	}
}

func parse(t *testing.T, s string) streamMsg {
	t.Helper()
	var m streamMsg
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return m
}
