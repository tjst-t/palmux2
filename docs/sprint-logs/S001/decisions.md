# Sprint S001 — Autonomous Decisions

Sprint title: Plan モード UI
Branch: `autopilot/S001`
Authority docs: VISION.md, DESIGN_PRINCIPLES.md
Spec source: docs/ROADMAP.md (S001)

## Planning Decisions

- **No story split**: S001-1 (rendering) and S001-2 (action) are already
  decomposed cleanly along the seam "data shape → user-facing behaviour".
  No further split needed (DESIGN_PRINCIPLES: "one story = one user-facing
  behavior").
- **Where to translate ExitPlanMode**: chose backend `normalize.go` (and
  the transcript replay path) over the frontend. Rationale: the roadmap
  task S001-1-1 explicitly says "stream-json の ExitPlanMode 入力を
  normalize 段階で kind: \"plan\" ブロックに変換". Doing it once on the
  server keeps the wire schema honest and prevents tool-result blocks
  from accidentally pairing with the renamed `plan` block (the CLI ships
  a tool_result for ExitPlanMode that we suppress for the same reason).
  Aligned with DESIGN_PRINCIPLES "1. CLI が真実 > Palmux が真実" — we
  preserve the original payload (the markdown plan) verbatim, just
  re-tagging the block kind so the UI can render it correctly. Note: the
  prior `kind: "todo"` block was reserved at the type layer but never
  actually emitted; we follow the same pattern and add `kind: "plan"`.
- **GUI spec**: skipped formal `gui-spec` invocation in autonomous mode
  because (a) Palmux2 has no Playwright harness wired beyond manual
  smoke tests today (`docs/development.md` and CLAUDE.md state the test
  policy is "Vitest for stores/lib + manual E2E"), and (b) the host
  Palmux2 runs in foreground from the parent agent and the bootstrap
  contract forbids restarting it. Verification falls back to: Go unit
  tests in `internal/tab/claudeagent/normalize_test.go`, `make build`
  passing, and a logical walkthrough of the Plan flow.
- **No new test framework introduced**: would violate "既存資産活用 > 新規実装"
  in DESIGN_PRINCIPLES if we added Playwright just for this sprint.
  Logged in Backlog suggestion below.

## Implementation Decisions

### S001-1: ExitPlanMode → kind:"plan" block

- **Server-side block conversion**: in `normalize.go`'s
  `processStreamEvent` handler, when a `content_block_start` arrives with
  `type:"tool_use"` and `name:"ExitPlanMode"`, emit a `Block` with
  `Kind:"plan"` instead of `Kind:"tool_use"`. The plan markdown lives in
  `Input.plan` per the CLI's tool schema; we keep `Input` as-is so the
  frontend can read it.
- **Streaming during partial input**: the CLI may stream the plan via
  `input_json_delta` chunks before sending `content_block_stop`. The
  existing `AppendToolInputPartial` path on Session keeps accumulating
  partial JSON in `Block.Text`; that already works for tool_use blocks
  and continues to work for plan blocks because we only change the
  `Kind` label, not the storage.
- **Suppress matching tool_result**: when ExitPlanMode is permitted to
  exit plan mode the CLI also emits a tool_result for it; the
  `tool_result` envelope renders as a separate "result" block which is
  noise. We suppress those by name lookup. Concrete approach: track the
  ExitPlanMode tool_use_id when we re-tag the block, and have
  `processUserMessage` skip tool_result entries whose `ToolUseID`
  matches. Implemented as a per-Session set under the same mu.
- **Resume / transcript replay**: in `assistantTurnFromTranscript` we
  apply the same rename (tool_use name=="ExitPlanMode" → kind="plan").
  The corresponding tool_result in the next user turn is dropped at
  load time (mirroring the live-stream suppression). Satisfies S001-1-3.

### S001-1: PlanBlock UI

- **Markdown via existing react-markdown + remarkGfm**: same renderer
  as `TextBlock` so Markdown quality matches the rest of the
  conversation (acceptance "可読性は通常の assistant 出力と同等以上").
  Re-uses `splitTextWithAttachments` is unnecessary — plans don't
  contain `[image: …]` markers.
- **Default-collapsed when block is `done`, expanded while streaming**:
  same idiom as `ToolUseBlock`, so long plans don't dominate scroll
  but in-flight planning is visible (matches "折りたたみ可能で長い計画でも
  会話全体を圧迫しない"). Header shows a one-line "Plan" label + first
  line of the plan as preview; chevron indicates state.
- **Visual distinction**: PlanBlock uses a panel with a 2px
  `--color-accent` left border + faint accent-tinted background so it
  stands out from prose without screaming. Borrows the styling axis
  from the existing ToolUse panel + Permission panel.

### S001-2: Approve / Reject buttons

- **Action set**: "Approve & Run" (primary, accent), "Stay in plan"
  (secondary, neutral). Roadmap also lists "Reject" — interpreted as
  "Stay in plan" since rejecting a plan mid-CLI doesn't have a clean
  CLI counterpart; staying in plan mode lets the user iterate. We do
  not send a separate "reject" message because Plan mode is opt-in;
  just keeping the user in plan mode is the natural reject path.
- **Mode resolution on Approve**: pull the target mode from the
  persisted `BranchPrefs.PermissionMode` if non-empty AND not "plan",
  otherwise fall back to the CLI default `acceptEdits`. (Reading the
  CLI-detected default would require an extra round-trip to /api/claude/modes
  for the "default" string; we already have `modes.default` cached on
  the frontend so we use that.) Logic lives in the frontend: we have
  `state.permissionMode` ("plan" right now), the `modes.default`
  computed from /api/claude/modes, and we send `permission_mode.set`
  with the resolved value. The backend respawns the CLI with the new
  flag, and a fresh session.init confirms the mode change.
- **Optimistic UI**: after sending the WS frame we mark the block as
  `decision: "approved"` locally and hide the action row. The next
  session.init or status.change will not re-flip because the block is
  cached in turns.
- **One-time use**: if a plan block is reached via transcript replay
  (resume), the `decision` field on the persisted Block is
  unset/empty, but to avoid offering Approve for a long-past plan we
  only show the buttons when `state.permissionMode === "plan"` AND
  the block is not already decided AND it is the most-recent plan
  block in the turn list. (Frontend predicate.)

### Build / verification

- **Backend**: added unit test `TestPlanBlock_NormalizeAndSuppress`
  exercising the ExitPlanMode rename + tool_result suppression in a
  single-stream walkthrough.
- **Frontend**: ran `npm run build` per the bootstrap contract — no
  Go restart, no `make serve`. The host Palmux2 keeps running.

## Backlog additions

- **Add Playwright headless E2E for Claude tab**: would have made
  S001-2-3 a real automated check. Sized M.
- **Surface CLI default permission mode in `/api/claude/modes` response
  with explicit `defaultForApprove` field**: today the FE has to
  reverse-engineer "what mode should I switch to when leaving plan?".
  Sized S.

## Review Decisions

- **No naming surprises**: `kind: "plan"` matches the verb the user
  sees in the spec; `PlanBlock` follows the existing `*Block` family.
- **No CSS module key conflicts** — added `.plan*` keys in
  `blocks.module.css` alongside the others.
