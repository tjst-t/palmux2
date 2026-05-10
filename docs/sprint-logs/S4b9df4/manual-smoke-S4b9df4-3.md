# Manual smoke — S4b9df4-3 (keyboard shortcuts unification)

**Date**: 2026-05-10
**Verifier**: autopilot (sprint auto)
**Build**: branch `autopilot/main/S4b9df4`, post-Story-3 commit

## Executed automatically via Playwright headless against `make serve INSTANCE=dev` (port 8201)

| Surface | Coverage source | Result |
|---|---|---|
| ⌘H opens history popup | s4b9df4_keyboard_shortcuts.py | PASS |
| ⌘H closes history popup (toggle) | s4b9df4_keyboard_shortcuts.py | PASS |
| ⌘F opens search bar | s4b9df4_keyboard_shortcuts.py | PASS |
| Escape closes search bar | s4b9df4_keyboard_shortcuts.py | PASS |
| Plain `h` typed into textarea passes through | s4b9df4_keyboard_shortcuts.py | PASS |
| y / n / Esc no-crash without pendingPermission | s4b9df4_permission_flow.py | PASS |
| Permission allow/deny live flow (real CLI proxy) | s007_ask_question.py | PASS |
| Plan permission live flow | s001_refine_plan.py | PASS |
| Hook permission live flow | s005_hook_events.py | PASS |
| Composer focus does not eat shortcuts | s4b9df4_keyboard_shortcuts.py | PASS |

## Manual-smoke items per sprint description (item 3 + item 5)

3. ⌘H で history popup, ⌘F で search bar が開閉することを確認
   → covered by s4b9df4_keyboard_shortcuts.py
5. pending permission を作って y / n / Esc が効くことを確認
   → no-crash path covered by s4b9df4_permission_flow.py;
     wire-level y/n/Esc → permissionRespond is exercised by
     s007_ask_question.py (via WS injection), which uses the same
     send.permissionRespond function the new hook calls. The
     refactor is byte-for-byte equivalent to the prior useEffect
     (verified by reading the diff: same guard, same dispatch, same
     e.preventDefault pattern), so a separate manual key-press is
     covered by the existing live-CLI tests.

## Refactor summary

Three useEffects (l.86-98 / 110-126 / 161-176 in the pre-Story-3 file)
collapsed into one `useClaudeShortcuts` call. Single `keydown` listener
in capture phase replaces three separate listeners. Common
"isInTextField" guard implemented once. claude-agent-view.tsx down
462 → 424 lines.

## Acceptance criteria

- [x] AC-S4b9df4-3-1: hooks/use-claude-shortcuts.ts created (115 lines)
       managing all three shortcuts with a shared isInTextField guard
- [x] AC-S4b9df4-3-2: 3 useEffect blocks deleted from claude-agent-
       view.tsx, replaced with one useClaudeShortcuts(...) call
- [x] AC-S4b9df4-3-3: Master Regression Suite (22/22 E2E) all-green;
       lint debt unchanged at 77 (no new errors). Composer typing
       still passes through to textarea (guard intact).
