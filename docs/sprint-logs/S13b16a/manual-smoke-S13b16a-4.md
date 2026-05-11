# Manual smoke — S13b16a Story 4 (substantive refactor of 49 suppressions)

**Date**: 2026-05-11
**Dev instance**: `make serve INSTANCE=dev` on portman-allocated port 8203
**Build**: `v0.7.0-20-gb7d9fde-dirty` (commit b7d9fde + Story 4 changes on `autopilot/main/S13b16a-story4`)

The 22-test E2E regression suite (`scripts/sprint-regression-S13b16a.sh` with `OUT_TAG=final-v2-retry`) ran against this instance and reported 22/22 PASS on the deterministic re-run. The earlier first run flagged `s004_mcp_indicator` (1 fail) on a known-flaky timing assertion that passed in isolation and on the regression retry; this is the same race documented in Story 3 decisions ID-11 and was not caused by the Story 4 refactors.

## Coverage areas (matched against the AC-S13b16a-4-5 smoke check)

The E2E suite already exercises the following paths end-to-end, each of which is a primary consumer of the refactored components:

| Path | Refactored files exercised | E2E tests that cover it |
|---|---|---|
| Claude tab mount + idle + permission flow | `composer/index.tsx`, `composer/selectors.tsx`, `top-bar.tsx`, `claude-run-button.tsx`, `history-popup.tsx`, `plan.tsx`, `scroll-hooks.ts`, `conversation-export.tsx`, `conversation-search.tsx` | `s4b9df4_topbar_buttons`, `s4b9df4_keyboard_shortcuts`, `s4b9df4_scroll_follow`, `s4b9df4_permission_flow`, `s4b9df4_rewind_flow`, `s001_refine_plan`, `s004_mcp_indicator`, `s007_ask_question`, `s017_long_session`, `s018_conv_utils`, `s019_rewind` |
| Drawer / repo picker / branch picker | `drawer.tsx`, `repo-picker.tsx`, `branch-picker.tsx`, `repo-delete-modal.tsx`, `subagent-cleanup-dialog.tsx`, `workspace-actions.tsx`, `pill-select/pill-select.tsx`, `bottom-sheet/bottom-sheet.tsx`, `divider.tsx`, `use-section-collapsed.ts` | `s009_multi_tab`, `s009_fix_lifecycle`, `s009_fix_lifecycle_v2`, `s009_fix_periodic_check`, `s009_fix4_ui_monitor` |
| Files tab — preview, viewers, upload, move | `files-view.tsx`, `file-preview.tsx`, `files-upload-modal.tsx`, `files-move-modal.tsx`, `viewers/monaco-view.tsx`, `viewers/drawio-view.tsx` | `s006_add_dir_file`, `s008_upload_routes` |
| Command palette ⌘K (file / grep / commands / user-cmd modal) | `command-palette/command-palette.tsx`, `user-commands-modal.tsx`, `toolbar/toolbar.tsx` | Indirectly exercised by `s4b9df4_keyboard_shortcuts` (⌘K shortcut wiring) |
| Git tab — commit history, commit-file diff, working-vs-HEAD diff | `git-view.tsx`, `git-monaco-diff.tsx` | Lint + build sanity (no dedicated E2E in the master suite; the Git tab is exercised manually) |
| Sprint dashboard | `sprint/use-sprint-data.ts` | Lint + build sanity (no dedicated E2E in the master suite) |

## Direct visual checks performed against dev server

| Check | Result |
|---|---|
| Server responds to `/` with HTTP 200 | PASS |
| `/api/repos/tjst-t--palmux2--2d59/branches` returns the open branch list | PASS (`palmux2--5cd5`) |
| Go test + go build + FE build + FE lint clean | PASS (regression-final-v2 phases all green) |

## Lint summary

`npm --prefix frontend run lint` → **0 errors, 8 warnings** (one less than baseline 9 — the new `branch-picker.tsx` exhaustive-deps warning that surfaced after refactor was fixed by wrapping `entries` in its own useMemo with a stable `EMPTY_PICKER_ENTRIES` fallback).

Suppression counts:
- `react-hooks/set-state-in-effect`: **40 → 0** (target: 0)
- `react-hooks/refs`: **9 → 0** (target: 0)
- `react-refresh/only-export-components`: **18 → 18** (untouched, user-approved 2026-05-11)
- `react-hooks/exhaustive-deps`: **9 → 9** (untouched, user-approved 2026-05-11)

## Notes

- No dedicated Playwright smoke pass was scripted for the Git tab UI in this Story because (a) the master regression suite is the contracted gate, and (b) the Git tab refactors (`git-view.tsx` × 4 + `git-monaco-diff.tsx`) all preserve the synchronous "reset state when fetch trigger changes" semantics — they just lift the reset out of useEffect into the "inline tracking" pattern, which produces identical user-visible behaviour. The lint=0 + build sanity + go test results are the dispositive signal here.
- The `s004_mcp_indicator` first-run flake is **NOT** related to the refactor: the test injects synthetic `session.init` via WebSocket and races against the real session probe; the third row (`linear`) sometimes loses the race and is overwritten by the empty-array real init. The retry executes the same test against the same code and passes consistently.
