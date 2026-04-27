package claudeagent

import (
	"encoding/json"
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

func parse(t *testing.T, s string) streamMsg {
	t.Helper()
	var m streamMsg
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return m
}
