# Sprint S001 — Refine pass (autopilot/S001-refine)

Refine target: complete the ExitPlanMode bypass (the original S001
shipped the PlanBlock but kept routing the underlying permission
request through the generic Allow/Deny UI, producing duplicate UI
under the plan), and redesign the action row to match Claude Code CLI
behaviour with a mode dropdown + Edit dialog.

Branch: `autopilot/S001-refine` (forked from `main`).
Spec source: parent agent's S001-refine prompt.
Authority: docs/VISION.md, docs/DESIGN_PRINCIPLES.md.

## What was broken

S001 added the kind:"plan" block + Approve/Stay-in-plan buttons but did
not bypass `Agent.RequestPermission` for ExitPlanMode. The CLI's
`ExitPlanMode` permission_prompt MCP call therefore took the generic
path, which:

  1. Added a `kind:"permission"` Block to the session, broadcasting
     `permission.request` to the FE — duplicating UI under the plan.
  2. Showed Allow / Allow-for-session / Always-allow / Edit / Deny
     buttons (wrong mental model — the user already has Approve / Edit
     / Keep planning above).

S007 (AskUserQuestion) had already solved the same problem for asks
via `requestAskAnswer`. This refine mirrors that pattern.

## Architectural decisions

### 1. Mirror S007 verbatim, factor out the shared kernel

The S001 / S007 bypasses share a tight kernel: register a
`permission_id` (no UI block), attach it to the existing kind:"plan" /
kind:"ask" block, broadcast a tab-specific `*.question` event, await a
tab-specific `*.respond` frame. We did NOT factor it into a generic
helper this round — DESIGN_PRINCIPLES "既存資産活用 > 新規実装" says
_use_ S007's pattern, not _re-architect_ both. After S001-refine ships
there will be two parallel implementations. Future plumbing (e.g.
`suppressedToolUseIDs` ROADMAP entry) is the right place to merge
them.

### 2. Stop adding kind:"permission" Block in bypass paths

`Session.AddPermissionRequest` was unconditionally appending a
`kind:"permission"` Block to the current turn. Even after the ask
flow's UI fix (the FE used permission_id matching), the SNAPSHOT still
carried a stale "permission" block — visible after reload as
"Decision: allow" under the AskQuestion. We added
`Session.RegisterPendingPermission` (no Block) and switched both
`requestAskAnswer` and the new `requestPlanResponse` to use it. This
also fixes the same drift bug for AskUserQuestion (a quiet bonus the
test `TestAskUserQuestion_FullRoundTrip` keeps in check, which
asserts the snapshot reflects the decided ask block — it would have
also caught a stray permission block).

### 3. Default mode = "auto"; persistence falls out of SetPermissionMode

The mode dropdown defaults to `auto` (per spec). When the user clicks
Approve, the backend wakes the CLI's permission with `behavior:"allow"`
THEN spawns a goroutine that calls `Agent.SetPermissionMode(ctx,
TargetMode)`. SetPermissionMode already persists the mode to
BranchPrefs via `persistPrefs`, so a fresh tab opens in the chosen
mode — no extra plumbing needed. Order matters: the waiter resolves
first, the kill+respawn happens second, so the in-flight CLI gets to
ack its tools/call before being killed.

### 4. Optimistic UI + durable replay

Two concerns: (a) clicking Approve must hide the action row
immediately, (b) reloading the page must keep it hidden. We solve (a)
with a component-local `planDecisions` map (keyed by block id) and
(b) by stamping `planDecision`+`planTargetMode` onto the kind:"plan"
Block server-side via `MarkPlanBlockDecided`, ship it on `plan.decided`,
and replay it from the snapshot. The action row's `decided` prop
reads either source — optimistic wins, but the server's echo makes it
durable.

### 5. Edit dialog uses a per-block overlay (no portals)

The Edit dialog renders inside the PlanBlock's React subtree using a
`position: fixed; inset: 0` overlay. Avoids the `createPortal` tax for
a one-shot UI. textarea is `resize: vertical`, `min-height: 240px`,
monospace font (matches S007's question / Composer's textarea).
Cmd/Ctrl+Enter submits, Esc cancels — keyboard-first parity.

### 6. The verification rule, applied

DESIGN_PRINCIPLES "自律実行の検証ルール" forbids declaring victory on
compile + unit test alone. Spun up `make serve INSTANCE=dev` (port
8241; portman picked it after the 8214/8215 leases became stale).
Wrote `tests/e2e/s001_refine_plan.py` (Playwright + websockets,
modelled on `tests/e2e/s007_ask_question.py`) and ran it against the
dev instance. 14/14 PASS first try.

### 7. Drift warning

None observed. `make build` clean, `go test ./...` clean, `npm run
build` clean. ROADMAP entry updated.

## E2E results

```
==> S001-refine E2E starting (dev port 8241, branch autopilot--S001-refine--08f1)
PASS: page loaded; composer present
PASS: session.init received via sidecar WS
PASS: plan.respond frame routed (backend rejects fake permId)
PASS: PlanBlock rendered after synthetic inject
PASS: no kind:"permission" UI leaked alongside the PlanBlock          <- the original S001 bug
PASS: mode dropdown lists ['default', 'acceptEdits', 'auto', 'bypassPermissions']
PASS: mode dropdown defaults to 'auto'
PASS: bypassPermissions option carries warning style
PASS: Approve ships plan.respond decision=approve targetMode=auto
PASS: plan-decided shows: 'Approved — switching to auto mode'
PASS: Edit plan… opens the dialog
PASS: Cancel closes the dialog
PASS: Save & approve ships plan.respond with editedPlan
PASS: Keep planning ships plan.respond decision=reject
==> S001-refine E2E PASSED
```

The synthetic inject path mirrors S007's harness — we don't need a
real CLI in the loop because we want to exercise the bypass-path
plumbing. End-to-end with a live CLI (Plan-mode prompt, ExitPlanMode
fired by the agent) is left to manual smoke when the autopilot/S001-refine
branch lands on main; the unit test
`TestExitPlanMode_FullRoundTrip` covers the ⇄ between
RequestPermission and AnswerPlanResponse.

## Files touched

  - internal/tab/claudeagent/events.go (+ EvPlanQuestion / EvPlanDecided,
    PlanQuestion/PlanDecidedPayload, PlanRespondFrame, Block.PlanDecision /
    Block.PlanTargetMode)
  - internal/tab/claudeagent/session.go (+ RegisterPendingPermission,
    Register/Consume/IsPlanPermission, AttachPlanPermission,
    MarkPlanBlockDecided, UpdatePlanBlockText, planPermissions map)
  - internal/tab/claudeagent/manager.go (+ requestPlanResponse,
    AnswerPlanResponse, summarisePlanForNotification,
    decisionLabelForPlan; route ExitPlanMode in RequestPermission;
    requestAskAnswer now uses RegisterPendingPermission)
  - internal/tab/claudeagent/handler.go (+ plan.respond frame; route
    permission.respond addressed at plan permissions)
  - internal/tab/claudeagent/plan_integration_test.go (new) — two
    integration tests (approve + reject) including the no-permission-
    block contract.
  - frontend/src/tabs/claude-agent/types.ts (Block.planDecision /
    planTargetMode)
  - frontend/src/tabs/claude-agent/agent-state.ts (+ pendingPlanByBlock,
    plan.question / plan.decided handlers, stamp/apply helpers,
    findPendingPlansInTurns)
  - frontend/src/tabs/claude-agent/use-agent.ts (+ planRespond)
  - frontend/src/tabs/claude-agent/blocks.tsx (PlanBlock rewritten —
    Approve+mode dropdown, Edit plan…, Keep planning, PlanEditDialog)
  - frontend/src/tabs/claude-agent/blocks.module.css (+ planApproveGroup,
    planModeSelect, planModeWarning, planReject, planEditOverlay /
    planEditCard / planEditArea / planEditActions)
  - frontend/src/tabs/claude-agent/claude-agent-view.tsx
    (planHandlersFor uses pendingPlanByBlock; new findActivePlan +
    findPlanBlockById; PlanHandlersForView extended with modes /
    targetMode / onApprove(mode, edited?) / onReject)
  - tests/e2e/s001_refine_plan.py (new, 14 checks)

## Backlog additions

  - **Generic suppressedToolUseIDs / bypass kernel** (S007 backlog
    already, S001-refine reinforces): the plan & ask paths share
    enough structure that a `Bypass(toolName, toolUseID, input)`
    helper would shrink manager.go by ~120 lines. Not done here
    because doing it well needs a third bypass case (or two) to make
    the abstraction honest.
  - **Real-CLI smoke for plan flow**: a manual run with `--permission-mode
    plan` + a follow-up real prompt would catch CLI-version drift in
    the ExitPlanMode tool name / input shape. ROADMAP "Playwright
    headless E2E ハーネス" still applies.
  - **Unify the warning rendering for bypassPermissions**: the
    Composer's permission mode menu has a similar mode picker; long
    term we'd like one shared mode-select component.
