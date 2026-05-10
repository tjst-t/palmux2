# editingTurnId lift-up — verification of "row never unmounts"

**Date**: 2026-05-10
**Story**: S4b9df4-4
**Goal**: Confirm ConversationList does not unmount turn rows during
scroll, so that the lift-up of `editingTurnId` to the parent (added
in S019 as a workaround for react-window unmounting rows) is now
vestigial and can be safely removed.

## Method

`tests/e2e/s4b9df4_rewind_flow.py::editor/exit-does-not-unmount-row`
asserts that after opening the user-turn editor (pencil click) and
exiting it (Escape), the row's `data-testid='harness-turn-turn-user-0'`
attribute remains in the DOM (count == 1).

## Result (Story 1 baseline + Story 2/3 final)

```
==> S4b9df4-1 rewind-flow E2E (port 8201)
  [editor/mount-on-pencil-click] OK
  [editor/escape-exits] OK
  [editor/exit-does-not-unmount-row] OK
  [editor/can-re-mount] OK
==> S4b9df4-1 rewind-flow E2E PASSED
```

The row stays mounted. The architectural reason: commit `bed812b
refactor(claude-agent): drop react-window virtualisation, render full
DOM` already eliminated the unmount-on-scroll behavior that the
S019 lift-up was guarding against. With native DOM rendering of every
turn, the row never disappears regardless of scroll position.

## Conclusion

The `editingTurnId` lift-up state is safe to remove from
`claude-agent-view.tsx` and the corresponding `editing` /
`onEditingChange` props from `TurnView` and `UserTurnEditor`. The
internal `editingInternal` state in UserTurnEditor handles the only
state-survival concern (within-mount transitions of the editor open/
close), and the row never unmounts so internal state never gets lost.

## Cross-check: harness behavior matches Claude tab behavior

The test harness in `frontend/src/tabs/claude-agent/test-harness.tsx`
explicitly uses the same `useScrollAutoFollow` + `ConversationList`
combo as the live Claude tab, and the unmount semantics are
identical. The verification result above transfers to the live tab.
