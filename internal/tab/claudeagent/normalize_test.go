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

func parse(t *testing.T, s string) streamMsg {
	t.Helper()
	var m streamMsg
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return m
}
