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
		// Hook lifecycle envelopes (only emitted when the CLI was started
		// with --include-hook-events). We render each hook as a single
		// kind:"hook" block that is opened by `hook_started` and closed
		// by the matching `hook_response`. Block lifecycle (start →
		// running → end) mirrors a normal tool_use so the existing
		// turn machinery handles ordering and stamping.
		if msg.Subtype == "hook_started" {
			return processHookStarted(s, msg)
		}
		if msg.Subtype == "hook_response" {
			return processHookResponse(s, msg)
		}
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
		// system/status — used for /compact lifecycle (S018). The first
		// envelope (status="compacting") fires while the CLI is
		// summarising; the matching one (status:null + compact_result)
		// fires when the summary is folded back into the session.
		if msg.Subtype == "status" {
			return processSystemStatus(msg)
		}
		// system/compact_boundary — the marker between pre- and post-
		// compaction history. We mint a synthetic role:"system" turn with
		// a kind:"compact" block so the UI can render a "Compacted: X
		// turns into 1 summary" line where the boundary lands.
		if msg.Subtype == "compact_boundary" {
			return processCompactBoundary(s, msg)
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

// processSystemStatus turns a system/status envelope into compact-lifecycle
// events. We only forward the two compact-related variants — other status
// updates the CLI may emit (none currently observed) are dropped on the
// floor rather than risk emitting a stray spinner.
func processSystemStatus(msg streamMsg) []AgentEvent {
	switch {
	case msg.Status == "compacting":
		ev, err := makeEvent(EvCompactStarted, CompactStartedPayload{})
		if err != nil {
			return nil
		}
		return []AgentEvent{ev}
	case msg.CompactResult != "":
		ev, err := makeEvent(EvCompactFinished, CompactFinishedPayload{Result: msg.CompactResult})
		if err != nil {
			return nil
		}
		return []AgentEvent{ev}
	}
	return nil
}

// processCompactBoundary mints a synthetic role:"system" turn carrying a
// kind:"compact" block. The block records the trigger / pre-token /
// post-token / duration metadata reported by the CLI plus a Palmux-side
// count of how many turns were folded into the summary (counted off the
// session snapshot at the time the boundary lands).
func processCompactBoundary(s *Session, msg streamMsg) []AgentEvent {
	var meta compactMetadata
	if len(msg.CompactMeta) > 0 {
		_ = json.Unmarshal(msg.CompactMeta, &meta)
	}
	turnID, blockID, turns := s.AppendCompactBlock(meta)
	out := []AgentEvent{}
	if ev, err := makeEvent(EvTurnStart, TurnStartPayload{TurnID: turnID, Role: "system"}); err == nil {
		out = append(out, ev)
	}
	if ev, err := makeEvent(EvBlockStart, BlockStartPayload{
		TurnID: turnID,
		Block: Block{
			ID:                blockID,
			Kind:              "compact",
			Done:              true,
			CompactTrigger:    meta.Trigger,
			CompactPreTokens:  meta.PreTokens,
			CompactPostTokens: meta.PostTokens,
			CompactDurationMs: meta.DurationMs,
			CompactTurns:      turns,
		},
	}); err == nil {
		out = append(out, ev)
	}
	return out
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
			Type     string          `json:"type"`
			ID       string          `json:"id,omitempty"`
			Name     string          `json:"name,omitempty"`
			Input    json.RawMessage `json:"input,omitempty"`
			Text     string          `json:"text,omitempty"`
			Thinking string          `json:"thinking,omitempty"`
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
		// AskUserQuestion is a tool_use the CLI ships to ask the user a
		// question with structured options. From the user's perspective
		// it's a question, not a tool, so we re-tag it to kind:"ask" and
		// suppress the matching tool_result (whose textual content just
		// echoes the chosen answer — the AskQuestionBlock UI already
		// communicates that visually). Mirrors the plan path.
		if bs.Type == "tool_use" && isAskQuestionToolName(bs.Name) {
			kind = "ask"
			if bs.ID != "" {
				s.MarkAskToolUse(bs.ID)
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
		// Tool start ⇒ status flips to tool_running. Plan and ask blocks
		// are authored content, not background work, so we leave the
		// status in "thinking" — the agent is still drafting (plan) or
		// awaiting an answer (ask) until it lands a result envelope.
		evs := []AgentEvent{out}
		if bs.Type == "tool_use" && kind != "plan" && kind != "ask" {
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
//
// When the current turn was already populated by stream_event envelopes
// (the common case — modern CLIs emit both partials and the trailing
// `assistant` envelope), this function is a no-op. Running its merge pass
// on top of streamed blocks duplicates ask/plan blocks because the
// finalised content_block_stop already removed entries from openBlocks,
// so the upsert path can't find them and falls through to append.
func processAssistantMessage(s *Session, raw json.RawMessage) []AgentEvent {
	if len(raw) == 0 {
		return nil
	}
	if s.IsCurrentTurnStreamCovered() {
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
			} else if isAskQuestionToolName(b.Name) {
				kind = "ask"
				if b.ID != "" {
					s.MarkAskToolUse(b.ID)
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
			// AskUserQuestion's tool_result echoes the chosen option(s);
			// the AskQuestionBlock UI shows the decision already, so we
			// suppress the redundant "result" line. Same idiom as plan.
			if s.ConsumeAskToolResult(cb.ToolUseID) {
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

// processHookStarted opens (or upserts) a kind:"hook" block for the given
// hook_id. Hook lifecycle events are independent of the assistant turn
// — they fire from the CLI's own machinery — so we stash them as their
// own pseudo-turn ("role":"hook") attached after whatever turn was
// in flight at the time. The frontend renders them inline with the
// surrounding tool_use blocks via simple turn ordering.
//
// hook_started carries only the hook_id / hook_name / hook_event. The
// matching hook_response will fill in stdout/stderr/exit_code.
func processHookStarted(s *Session, msg streamMsg) []AgentEvent {
	if msg.HookID == "" {
		return nil
	}
	turnID, blockID, block := s.OpenHookBlock(msg.HookID, msg.HookEvent, msg.HookName)
	out := []AgentEvent{}
	if ev, err := makeEvent(EvBlockStart, BlockStartPayload{
		TurnID: turnID,
		Block:  block,
	}); err == nil {
		out = append(out, ev)
	}
	_ = blockID
	return out
}

// processHookResponse closes a previously-opened kind:"hook" block by
// stamping its stdout/stderr/exit_code/outcome and flipping Done. If
// the matching `hook_started` was missed (e.g. WS reconnect during
// hook execution) we still render a fully-formed block — better to
// surface a one-shot "completed" block than drop the event.
func processHookResponse(s *Session, msg streamMsg) []AgentEvent {
	if msg.HookID == "" {
		return nil
	}
	turnID, blockID, block, ok := s.CompleteHookBlock(msg.HookID, hookCompletion{
		Event:    msg.HookEvent,
		Name:     msg.HookName,
		Output:   msg.HookOutput,
		Stdout:   msg.HookStdout,
		Stderr:   msg.HookStderr,
		ExitCode: msg.HookExitCode,
		Outcome:  msg.HookOutcome,
		Payload:  msg.HookPayload,
	})
	if !ok {
		return nil
	}
	out := []AgentEvent{}
	if ev, err := makeEvent(EvBlockEnd, BlockEndPayload{
		TurnID:  turnID,
		BlockID: blockID,
	}); err == nil {
		out = append(out, ev)
	}
	// Re-emit a fresh BlockStart so any client that missed the original
	// `hook_started` (e.g. reconnected after the hook fired) still sees
	// the fully-completed hook block. The frontend dedupes by id.
	if ev, err := makeEvent(EvBlockStart, BlockStartPayload{
		TurnID: turnID,
		Block:  block,
	}); err == nil {
		out = append(out, ev)
	}
	return out
}

// hookCompletion bundles the fields we copy off a `hook_response` envelope
// onto the matching kind:"hook" block. Kept private to normalize.go so
// the public Session API doesn't get a parameter explosion.
type hookCompletion struct {
	Event    string
	Name     string
	Output   string
	Stdout   string
	Stderr   string
	ExitCode int
	Outcome  string
	Payload  json.RawMessage
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

// isAskQuestionToolName reports whether the named tool is the CLI's
// AskUserQuestion tool. The canonical name is "AskUserQuestion"; we
// accept a couple of likely variants (snake_case, lowercase) so a
// minor CLI rename doesn't silently regress to the generic tool_use
// + permission_prompt rendering.
func isAskQuestionToolName(name string) bool {
	switch name {
	case "AskUserQuestion", "ask_user_question", "askuserquestion":
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
