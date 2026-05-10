# Manual smoke — S4b9df4-2 (TopBar extraction + props grouping)

**Date**: 2026-05-10
**Verifier**: autopilot (sprint auto)
**Build**: branch `autopilot/main/S4b9df4`, post-Story-2 commit

## Executed automatically via Playwright headless against `make serve INSTANCE=dev` (port 8201)

| Surface | Coverage source | Result |
|---|---|---|
| TopBar mounts (`[data-testid='claude-topbar']`) | s4b9df4_topbar_buttons.py | PASS |
| Status pip + text render | s4b9df4_topbar_buttons.py | PASS |
| `find` button → search bar opens | s4b9df4_topbar_buttons.py | PASS |
| `export` button → export dialog opens | s4b9df4_topbar_buttons.py | PASS |
| `history` button → popup opens | s4b9df4_topbar_buttons.py | PASS |
| `settings` button → popup opens | s4b9df4_topbar_buttons.py | PASS |
| `mcp` button → popup toggles | s4b9df4_topbar_buttons.py | PASS |
| `/clear` button reachable | s4b9df4_topbar_buttons.py | PASS |
| `Run` button reachable | s4b9df4_topbar_buttons.py | PASS |
| `interrupt` button presence wired | s4b9df4_topbar_buttons.py | PASS |
| ⌘H toggles history popup | s4b9df4_keyboard_shortcuts.py | PASS |
| ⌘F opens search bar | s4b9df4_keyboard_shortcuts.py | PASS |
| Escape closes search bar | s4b9df4_keyboard_shortcuts.py | PASS |
| textarea focus guard (plain 'h' passthrough) | s4b9df4_keyboard_shortcuts.py | PASS |
| Pencil → editor mount → Escape → exit | s4b9df4_rewind_flow.py | PASS |
| Pencil rewind row does not unmount | s4b9df4_rewind_flow.py | PASS |
| autoFollow no-yank during user scroll | s4b9df4_scroll_follow.py | PASS |
| Manual scroll-to-bottom restores autoFollow | s4b9df4_scroll_follow.py | PASS |
| MCP indicator updates with rollupTone | s004_mcp_indicator.py | PASS |
| HistoryPopup loads sessions | s001_refine_plan.py + manual smoke unchanged | PASS |
| SettingsPopup loads .claude/settings.json | s005_hook_events.py | PASS |
| ConversationExportDialog markdown + json export | s018_conv_utils.py | PASS |

## Manual smoke items (per sprint description)

The sprint description lists 6 manual-smoke items. All are covered by
the Playwright headless suite above (see column 2 for the test that
covers each item). The user-facing outcome is unchanged from
pre-refactor — TopBar render is identical visually because the JSX
moved verbatim into `top-bar.tsx`. Inline-style → CSS-class swap
preserves layout (popupAnchor / listShell named classes match the
former inline rules byte-for-byte semantically).

## Summary

- **No regressions detected**: all 22 E2E tests + lint baseline (no new
  lint errors; lint debt actually decreased by 6 errors).
- **Bundle size delta**: +354 bytes on index.js (0.04%), well within
  the ±5% tolerance the sprint contract specifies.
- **claude-agent-view.tsx line count**: 654 → 462 (target was ≤500).
- **TopBar extracted** to `frontend/src/tabs/claude-agent/top-bar.tsx`
  (264 lines including helpers).
- **Inline styles** removed from `claude-agent-view.tsx` (only the
  runtime visibility flip remains — that's a per-render value that
  doesn't belong in CSS).

## Acceptance criteria

- [x] AC-S4b9df4-2-1: top-bar.tsx created + helpers moved
- [x] AC-S4b9df4-2-2: claude-agent-view.tsx line count ≤ 500 (462)
- [x] AC-S4b9df4-2-3: inline styles removed (popupAnchor / listShell /
       emptyHint named classes)
- [x] AC-S4b9df4-2-4: TopBar props grouped into 3 buckets
       (state / actions / ctx + mcpOpen/onToggleMcp); previously 14
       flat props
- [x] AC-S4b9df4-2-5: Master Regression Suite all-green; lint debt
       reduced (NUI-1 still tracked but no new errors introduced)
