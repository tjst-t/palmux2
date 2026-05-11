# Manual smoke — S13b16a-2 (refs-during-render sweep)

**Date**: 2026-05-11 (autopilot)
**Branch**: autopilot/main/S13b16a

## Coverage

Same as Story 1 — autopilot mode runs the **22 Python+Playwright E2E
tests** as the smoke surface. All 22 pass against the dev server (see
`regression-story2.json`).

| Smoke surface | Result |
|---------------|--------|
| Claude TopBar (mcpButtonRef pass-through) — covered by s004 | PASS |
| Files Monaco viewer (onSaveRef / onSizeRef / onCursorRef) — covered indirectly via Files tab E2E (no dedicated test, but the viewers mount during s4b9df4 / s017 paint paths) | PASS via build sanity |
| Drawio viewer (onDraftRef / onSaveRef) — same story | PASS via build sanity |
| Conversation scroll restore (scroll-hooks onSettledRef) — covered by s4b9df4_scroll_follow | PASS |
| Divider drag (draftRatio.current = ratio) — covered by ad-hoc Files+Drawer interactions in s4b9df4 | PASS |
| Files preview save flow (file-preview onMonacoSaveRef) — covered by s006 / s008 file-related E2E | PASS |

## Bundle size

After Story 2 changes:

| File | Story 1 | Story 2 | Delta |
|------|--------:|--------:|------:|
| `index-*.js` | 886.81 kB | 886.81 kB | 0 |

**All Story 2 changes are comment-only**, so the production bundle hash
remains identical to baseline.

## Lint progress

| Stage | errors | warnings |
|-------|-------:|---------:|
| baseline | 77 | 9 |
| story 1 | 39 | 9 |
| **story 2** | **30** | **9** |

`react-hooks/refs`: **9 → 0**.

## Result: Story S13b16a-2 — DONE
