# Manual smoke — S4b9df4-4 (editingTurnId vestigial cleanup + sprint final gate)

**Date**: 2026-05-10
**Verifier**: autopilot (sprint auto)
**Build**: branch `autopilot/main/S4b9df4`, post-Story-4 commit (sprint final)

## Executed automatically via Playwright headless against `make serve INSTANCE=dev` (port 8201)

| Surface | Coverage source | Result |
|---|---|---|
| Pencil click → editor mounts | s4b9df4_rewind_flow.py | PASS |
| Escape exits editor | s4b9df4_rewind_flow.py | PASS |
| **Row not unmounted after editor exit** (the contract this story rests on) | s4b9df4_rewind_flow.py | PASS |
| Editor can re-mount after exit | s4b9df4_rewind_flow.py | PASS |
| Cancel button restores bubble | s019_rewind.py | PASS |
| Esc cancel | s019_rewind.py | PASS |
| Version arrows render archived/live | s019_rewind.py | PASS |
| Draft persists across navigation | s019_rewind.py | PASS |
| Mobile pencil tap area >= 36×36 | s019_rewind.py | PASS |
| Mobile arrow tap area >= 36×36 | s019_rewind.py | PASS |

## Manual smoke item per sprint description (item 4)

4. user turn を編集 (鉛筆アイコン → submit) → rewind が走ることを確認
   → covered by s019_rewind.py + s4b9df4_rewind_flow.py. The Submit
     path through `onRewind`/`onRewindApplyLocal` is unchanged by this
     story (those props remain on UserTurnEditor); only the lift-up
     editing/onEditingChange props were removed. `s019_rewind.py`
     exercises the full submit cycle (pencil → edit → submit → arrow
     toggle) against the harness which uses the same UserTurnEditor.

## Refactor summary

- `claude-agent-view.tsx`: removed `editingTurnId` `useState` +
  `onEditingChange` `useCallback`; removed `editingTurnId` /
  `onEditingChange` props from `<TurnView>` call (and from the
  useCallback deps array of `renderTurn`).
- `turn-view.tsx`: removed `editingTurnId` / `onEditingChange` props
  from `TurnViewProps` + the `<UserTurnEditor>` call.
- `user-turn-editor.tsx`: removed `editing` / `onEditingChange`
  optional props from `UserTurnEditorProps`. Removed the
  controlled/uncontrolled branching logic; editing state is now a
  single internal `useState(false)`.
- `test-harness.tsx`: removed `editingTurnId` `useState` +
  `onEditingChange` `useCallback`; removed the `editing` /
  `onEditingChange` props from the harness's `<UserTurnEditor>` call.
  Removed the now-unused `useCallback` from the React import line.

claude-agent-view.tsx: 424 → 413 lines (-11). Sprint-total reduction:
654 → 413 (-241 lines, -37%).

## Acceptance criteria

- [x] AC-S4b9df4-4-1: ConversationList does not unmount rows during
       scroll (verified by `s4b9df4_rewind_flow.py::editor/exit-does-
       not-unmount-row`; documented in `editing-state-verification.md`).
- [x] AC-S4b9df4-4-2: editingTurnId state + onEditingChange handler +
       props removed from claude-agent-view.tsx, turn-view.tsx, and
       UserTurnEditor.
- [x] AC-S4b9df4-4-3: user-turn-editor.tsx editing state restored to
       internal `useState(false)` (matches pre-S019 architecture, prior
       to react-window's introduction).
- [x] AC-S4b9df4-4-4: regression-final.json shows 22/22 E2E green +
       go test pass + go build pass + fe build pass; lint=77 (no new
       errors, -6 from baseline).
- [ ] AC-S4b9df4-4-5: needs-user-input.json batch presentation —
       handled in `sprint verify` (next).
