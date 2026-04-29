package claudeagent

import (
	"encoding/json"
)

// processStreamMessage takes a raw envelope from the CLI and turns it into
// zero or more Palmux WS events. Mutations to the Session live state happen
// here; the caller is responsible for actually broadcasting the returned
// events.
func processStreamMessage(s *Session, msg streamMsg) []AgentEvent {
	// Stamp the parent_tool_use_id on the session so any new Turn this
	// envelope creates inherits it. The Task tool spawns sub-agents whose
	// envelopes carry the parent Task tool_use_id; the frontend uses this
	// to render those sub-agent turns nested under their parent block.
	// Empty ⇒ top-level conversation; clear after dispatch so unrelated
	// background events don't leak the previous parent.
	s.SetCurrentParentToolUseID(msg.ParentToolUseID)
	defer s.SetCurrentParentToolUseID("")

	switch msg.Type {
	case "system":
		if msg.Subtype == "init" && msg.SessionID != "" {
			replaced, old := s.SetSessionID(msg.SessionID)
			if msg.Model != "" {
				s.SetModel(msg.Model)
			}
			if msg.PermissionMode != "" {
				s.SetPermissionMode(msg.PermissionMode)
			}
			if len(msg.MCPServers) > 0 {
				s.SetMCPServers(msg.MCPServers)
			}
			if replaced {
				ev, err := makeEvent(EvSessionReplaced, SessionReplacedPayload{OldSessionID: old, NewSessionID: msg.SessionID})
				if err == nil {
					return []AgentEvent{ev}
				}
			}
		}
		return nil

	case "stream_event":
		return processStreamEvent(s, msg.Event)

	case "assistant":
		return processAssistantMessage(s, msg.Message)

	case "user":
		return processUserMessage(s, msg.Message)

	case "result":
		s.SetStatus(StatusIdle)
		turnID := s.CloseTurn(msg.TotalCostUSD)
		ev, err := makeEvent(EvTurnEnd, TurnEndPayload{
			TurnID:       turnID,
			IsError:      msg.IsError,
			TotalCostUSD: msg.TotalCostUSD,
			DurationMs:   msg.DurationMs,
			Usage:        msg.Usage,
		})
		if err != nil {
			return nil
		}
		st, _ := makeEvent(EvStatusChange, StatusChangePayload{Status: StatusIdle})
		return []AgentEvent{ev, st}
	}
	return nil
}

// processStreamEvent handles partial-message events. The Anthropic
// content_block_* schema is the lingua franca here.
func processStreamEvent(s *Session, raw json.RawMessage) []AgentEvent {
	if len(raw) == 0 {
		return nil
	}
	var ev streamEvent
	if err := json.Unmarshal(raw, &ev); err != nil {
		return nil
	}
	switch ev.Type {
	case "message_start":
		s.SetStatus(StatusThinking)
		turnID := s.StartAssistantTurn()
		out := []AgentEvent{}
		if e, err := makeEvent(EvTurnStart, TurnStartPayload{
			TurnID:          turnID,
			Role:            "assistant",
			ParentToolUseID: s.CurrentParentToolUseID(),
		}); err == nil {
			out = append(out, e)
		}
		if e, err := makeEvent(EvStatusChange, StatusChangePayload{Status: StatusThinking}); err == nil {
			out = append(out, e)
		}
		return out

	case "content_block_start":
		var bs struct {
			Type string `json:"type"`
			ID string `json:"id,omitempty"`
			Name string `json:"name,omitempty"`
			Input json.RawMessage `json:"input,omitempty"`
			Text string `json:"text,omitempty"`
			Thinking string `json:"thinking,omitempty"`
		}
		_ = json.Unmarshal(ev.Block, &bs)
		kind := blockKindFor(bs.Type)
		// ExitPlanMode is a tool_use the CLI ships when the agent has
		// finished drafting a plan in --permission-mode plan. From the
		// user's perspective it isn't a tool — it's the plan itself —
		// so we re-tag the block as kind:"plan" and remember the
		// tool_use_id so the corresponding tool_result envelope (which
		// the CLI emits whether the user approves or rejects) can be
		// suppressed instead of showing up as a noise "result" line.
		if bs.Type == "tool_use" && isPlanToolName(bs.Name) {
			kind = "plan"
			if bs.ID != "" {
				s.MarkPlanToolUse(bs.ID)
			}
		}
		turnID, blockID, _ := s.OpenBlock(ev.Index, kind)
		switch bs.Type {
		case "tool_use":
			s.SetBlockToolUse(ev.Index, bs.Name, bs.ID, bs.Input)
		}
		out, err := makeEvent(EvBlockStart, BlockStartPayload{
			TurnID: turnID,
			Block: Block{
				ID: blockID, Kind: kind, Index: ev.Index,
				Name:      bs.Name,
				Input:     bs.Input,
				Text:      bs.Text + bs.Thinking,
				ToolUseID: bs.ID,
			},
			ParentToolUseID: s.CurrentParentToolUseID(),
		})
		if err != nil {
			return nil
		}
		// Tool start ⇒ status flips to tool_running. Plan blocks are
		// authored content, not background work, so we leave the status
		// in "thinking" — the agent is still drafting until it lands a
		// result envelope.
		evs := []AgentEvent{out}
		if bs.Type == "tool_use" && kind != "plan" {
			s.SetStatus(StatusToolRunning)
			if st, err := makeEvent(EvStatusChange, StatusChangePayload{Status: StatusToolRunning}); err == nil {
				evs = append(evs, st)
			}
		}
		return evs

	case "content_block_delta":
		var d streamDelta
		_ = json.Unmarshal(ev.Delta, &d)
		switch d.Type {
		case "text_delta":
			turnID, blockID := s.AppendBlockText(ev.Index, "text", d.Text)
			if blockID == "" {
				return nil
			}
			out, err := makeEvent(EvBlockDelta, BlockDeltaPayload{
				TurnID: turnID, BlockID: blockID, Index: ev.Index, Kind: "text", Text: d.Text,
			})
			if err != nil {
				return nil
			}
			return []AgentEvent{out}
		case "thinking_delta":
			turnID, blockID := s.AppendBlockText(ev.Index, "thinking", d.Thinking)
			if blockID == "" {
				return nil
			}
			out, err := makeEvent(EvBlockDelta, BlockDeltaPayload{
				TurnID: turnID, BlockID: blockID, Index: ev.Index, Kind: "thinking", Text: d.Thinking,
			})
			if err != nil {
				return nil
			}
			return []AgentEvent{out}
		case "input_json_delta":
			turnID, blockID, _ := s.AppendToolInputPartial(ev.Index, d.PartialJSON)
			if blockID == "" {
				return nil
			}
			out, err := makeEvent(EvBlockDelta, BlockDeltaPayload{
				TurnID: turnID, BlockID: blockID, Index: ev.Index, Kind: "tool_input", Partial: d.PartialJSON,
			})
			if err != nil {
				return nil
			}
			return []AgentEvent{out}
		}
		return nil

	case "content_block_stop":
		turnID, blockID, finalInput := s.FinalizeBlock(ev.Index)
		if blockID == "" {
			return nil
		}
		out, err := makeEvent(EvBlockEnd, BlockEndPayload{TurnID: turnID, BlockID: blockID, Final: finalInput})
		if err != nil {
			return nil
		}
		return []AgentEvent{out}

	case "message_delta":
		// Not currently surfaced — usage and stop_reason arrive on `result`.
		return nil

	case "message_stop":
		// `result` envelope handles the official turn end.
		return nil
	}
	return nil
}

// processAssistantMessage is the fallback when partial messages are missing
// (some CLI builds have spotty stream_event coverage). We merge the
// completed message into the current turn's block list.
func processAssistantMessage(s *Session, raw json.RawMessage) []AgentEvent {
	if len(raw) == 0 {
		return nil
	}
	var msg chatMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	blocks := make([]contentBlock, 0, len(msg.Content))
	for _, raw := range msg.Content {
		var cb contentBlock
		if err := json.Unmarshal(raw, &cb); err != nil {
			continue
		}
		blocks = append(blocks, cb)
	}
	turnID := s.ApplyAssistantMessage(blocks)

	// Re-emit a snapshot so reconnecting clients (or clients that missed
	// the deltas) see the final state. We piggy-back via session.init style
	// per-block events.
	out := []AgentEvent{}
	for i, b := range blocks {
		switch b.Type {
		case "text":
			ev, err := makeEvent(EvBlockEnd, BlockEndPayload{TurnID: turnID, BlockID: "", Final: rawTextFinal(b.Text)})
			if err == nil {
				_ = i
				out = append(out, ev)
			}
		case "tool_use":
			kind := "tool_use"
			if isPlanToolName(b.Name) {
				kind = "plan"
				if b.ID != "" {
					s.MarkPlanToolUse(b.ID)
				}
			}
			ev, err := makeEvent(EvBlockStart, BlockStartPayload{
				TurnID: turnID,
				Block: Block{
					Kind: kind, Index: i, Name: b.Name, Input: b.Input, Done: true, ToolUseID: b.ID,
				},
				ParentToolUseID: s.CurrentParentToolUseID(),
			})
			if err == nil {
				out = append(out, ev)
			}
		}
	}
	return out
}

// processUserMessage handles tool_result envelopes the CLI ships back to us.
func processUserMessage(s *Session, raw json.RawMessage) []AgentEvent {
	if len(raw) == 0 {
		return nil
	}
	var msg chatMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return nil
	}
	out := []AgentEvent{}
	for _, item := range msg.Content {
		var cb contentBlock
		if err := json.Unmarshal(item, &cb); err != nil {
			continue
		}
		if cb.Type == "tool_result" {
			// ExitPlanMode ships a tool_result that simply echoes the
			// approve/reject decision; the plan block already
			// communicates that visually, so showing the result row is
			// just noise. Drop it on the floor when we have a marker.
			if s.ConsumePlanToolResult(cb.ToolUseID) {
				continue
			}
			text := decodeToolResultContent(cb.Content)
			turnID, _ := s.AppendToolResult(cb.ToolUseID, text, cb.IsError)
			if ev, err := makeEvent(EvToolResult, ToolResultPayload{
				TurnID:    turnID,
				ToolUseID: cb.ToolUseID,
				Output:    text,
				IsError:   cb.IsError,
			}); err == nil {
				out = append(out, ev)
			}
		}
	}
	return out
}

// isPlanToolName reports whether the named tool is the CLI's
// ExitPlanMode tool. The CLI uses the canonical name "ExitPlanMode";
// we accept a couple of obvious variants so a future minor rename
// doesn't silently regress us back to the unstyled tool_use rendering.
func isPlanToolName(name string) bool {
	switch name {
	case "ExitPlanMode", "exit_plan_mode", "exitplanmode":
		return true
	}
	return false
}

// decodeToolResultContent flattens the polymorphic content payload of a
// tool_result block into a single string for display.
func decodeToolResultContent(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err == nil {
			return s
		}
	}
	if raw[0] == '[' {
		var arr []json.RawMessage
		if err := json.Unmarshal(raw, &arr); err == nil {
			var out string
			for _, item := range arr {
				var sub struct {
					Type string `json:"type"`
					Text string `json:"text"`
				}
				if err := json.Unmarshal(item, &sub); err == nil {
					if sub.Type == "text" {
						if out != "" {
							out += "\n"
						}
						out += sub.Text
					}
				}
			}
			return out
		}
	}
	return string(raw)
}

func blockKindFor(t string) string {
	switch t {
	case "text", "thinking", "tool_use":
		return t
	default:
		return t
	}
}

// rawTextFinal builds the "final" payload for an end-of-text block.
func rawTextFinal(text string) json.RawMessage {
	b, _ := json.Marshal(struct {
		Text string `json:"text"`
	}{text})
	return b
}
