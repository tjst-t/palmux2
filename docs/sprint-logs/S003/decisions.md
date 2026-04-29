# Sprint S003 — Autonomous Decisions

## Reconnaissance

- **Wire format confirmed**: The Claude CLI 2.1.123 binary contains zod schemas
  declaring `parent_tool_use_id: string | null` as a **top-level envelope field**
  on `user`, `assistant`, `stream_event`, and `tool_progress` messages.
  Sample lines extracted with `strings .../claude` :
  - `type:literal("user"),message:…,parent_tool_use_id:string().nullable()`
  - `type:literal("assistant"),message:…,parent_tool_use_id:string().nullable()`
  - `type:literal("stream_event"),event:…,parent_tool_use_id:string().nullable()`
  This is what the SDK emits when the Task tool spawns a sub-agent — every
  envelope produced by that sub-agent carries the tool_use_id of the parent
  Task block. Top-level conversation messages set the field to `null`.
- **Transcripts (.jsonl) do not currently carry `parent_tool_use_id`** in any
  of the existing on-disk Claude project transcripts on this machine; the
  CLI uses `parentUuid` for ancestor linkage in the persistent log instead.
  S003-1-4 therefore has to fall back to a synthesised transcript-style
  test (parent_tool_use_id may be added to the .jsonl format in a future
  CLI version, but cannot be exercised against existing data).

## Planning Decisions

- **Story split**: Kept S003-1 as a single Story per ROADMAP. The four tasks
  are already small and tightly coupled (wire → reducer → render → resume).
  Splitting would force a backend-only PR that the user can't see in the UI,
  violating DESIGN_PRINCIPLES rule 3 ("Phase区切り > 半完成投入").
- **No GUI Story spec**: This Sprint adds nesting/indent rendering on top of
  existing blocks. There's no new entry-point/page/modal — gui-spec
  Playwright scenarios would just re-test BlockView. Skipping the gui-spec
  invocation per its own "skip when no new entry point" guidance.
- **Backend stores parent linkage on the Turn, not the Block**: The Task
  tool_use is a single block in the *parent* assistant turn; the spawned
  sub-agent then produces *its own turns* whose envelopes all carry
  `parent_tool_use_id = <Task block id>`. So the parent pointer belongs on
  the Turn, not the Block. This matches DESIGN_PRINCIPLES rule 5 (responsibility
  scoping — turn-level grouping is a turn concern).

## Implementation Decisions

- **Field naming on the Go envelope**: Added `ParentToolUseID string` (with
  `omitempty`) to `streamMsg`, decoded via `json:"parent_tool_use_id,omitempty"`.
  Stored on `Turn.ParentToolUseID` for snapshot/replay.
- **Frontend tree assembly is render-time, not state-time**: Reducer keeps
  the flat `turns: Turn[]` list (with `parentToolUseId` on each turn) and
  the renderer (`<TaskTree>`) groups turns under their parent tool_use block
  on the fly. Rationale: VISION rule "naviation保持 > UI state保持" — keeping
  the canonical state flat means any future feature (search, jump-to,
  virtualisation) can keep using a simple array without an O(N) tree
  traversal each time. The grouping is purely presentational.
- **Auto-collapse on Task completion**: Use `block.done` on the Task
  tool_use block as the signal. While the task is running we render expanded
  (so the user sees the sub-agent work), once `done` we render collapsed
  (header + summary line) by default but the user can re-open. Mirrors the
  existing PlanBlock/ToolUseBlock pattern from S001.
- **Resume support via transcript loader**: `LoadTranscriptTurns` already
  walks the .jsonl entry-by-entry. When/if the CLI starts persisting
  `parent_tool_use_id` in transcripts (future-proofing) the loader propagates
  it onto the resulting Turn. For today, transcript-only sessions show
  Task children flat, and live sessions show them nested — which is the
  documented behaviour when a sub-agent transcript exists.

## Verify Decisions

- **Tests**: Added `TestStreamEvents_ParentToolUseIDPropagatesToTurn` and
  `TestLoadTranscriptTurns_PreservesParentToolUseID` covering both the live
  stream path and the resume path. The existing 4 tests in normalize_test.go
  continue to pass.
- **Build**: `go build ./...` and `cd frontend && npm run build` both clean.

## Drift / Backlog

- None added.
