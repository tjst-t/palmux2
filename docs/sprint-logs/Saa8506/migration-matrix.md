# Sprint Saa8506 — migration matrix

Generated 2026-05-11. Story 0 deliverable. The 10 legacy tests listed below
all consume `BRANCH_ID` that hardcodes an *autopilot worktree* deleted by
S1e8d02 / S4b9df4. The plan: convert each one to use
`tests/e2e/_fixture.py :: palmux2_test_fixture()` (and helpers added in
Story 1-1 where needed) so the test creates / opens / cleans up its own
fixture and stops referencing the long-gone `autopilot--*` worktree.

## Section A — `_fixture.py` helpers (post-Saa8506-1-1)

| Helper | Location | Pre/Post behaviour | Solves which dependency |
|---|---|---|---|
| `palmux2_test_fixture(sprint)` | _fixture.py:246 | yields a `Fixture` with `path`, `ghq_path`, `repo_id`; rmtree + `/api/repos/<id>/close` on exit (also on SIGINT/SIGTERM via atexit). | Hermetic ghq repo `+` palmux2 Open Repository registration. |
| `make_fixture(sprint)` | _fixture.py:178 | same as above but no context-manager: caller must `_cleanup()`. | Used by S025 scenarios that intentionally leak. |
| `Fixture._cleanup()` | _fixture.py:158 | idempotent `/api/repos/<id>/close` + `shutil.rmtree(path)`. | Ensures cleanup is exception-safe / double-call-safe. |

**Open branch.** `palmux2_test_fixture()` registers the *repository* but does
not pre-create a worktree — the primary worktree (the ghq path itself) is
auto-opened as a Workspace. New Story-1 helpers (added in Saa8506-1-1) on
top of this base:

| New helper | Behaviour | Used by |
|---|---|---|
| `Fixture.primary_branch_id()` | Polls `/api/repos/{id}/branches`, returns the first `open` branch ID. Retries up to 5 s for the worktree to surface. | All 10 legacy tests. |
| `Fixture.open_claude_tab()` | Idempotent. Confirms the protected `claude` tab is listed by `/api/repos/{id}/branches/{branch}/tabs`. (claude tab is auto-created by tab provider; this helper just synchronises.) | s001 / s004 / s005 / s006 / s007 / s008 / s009* |
| `Fixture.create_bash_tab(name="bash")` | Returns the new tab's ID. | s009_multi_tab / s009_fix_lifecycle / s009_fix_lifecycle_v2 / s009_fix4_ui_monitor (bash tab). |
| `Fixture.create_worktree(branch_name)` | Calls `POST /api/repos/{id}/branches` (gwq add). Returns the new branch ID; cleanup goes through `Fixture._cleanup()` (repo close already cascades to worktrees). | s009_fix_lifecycle*/periodic for "make a second worktree on a hot session". |

## Section B — 10 legacy tests, current state and required fixture

| # | File | Current `BRANCH_ID` hardcode | Wire surfaces used | Fixture needs | Sub-agent |
|---|------|-----------------------------|-------------------|---------------|-----------|
| 1 | tests/e2e/s001_refine_plan.py | `autopilot--S001-refine--08f1` | Claude WS `/tabs/claude/agent` (synthetic frames; no real CLI) | `palmux2_test_fixture` + `primary_branch_id` + `open_claude_tab` | A |
| 2 | tests/e2e/s004_mcp_indicator.py | `autopilot--S004--6089` | Claude WS + synthetic mcp.list frame; Playwright DOM | same as 1 | A |
| 3 | tests/e2e/s005_hook_events.py | `autopilot--S005--6987` | Claude WS, hook event frames (synthetic) | same as 1 | B |
| 4 | tests/e2e/s006_add_dir_file.py | `autopilot--S006--70ed` | REST `/files` + `/files/search` (traversal hardening); composer + button visible | same as 1 (claude tab UI) | C |
| 5 | tests/e2e/s007_ask_question.py | `autopilot--S007--bd65` | Claude WS + AskUserQuestion synthetic frame | same as 1 | B |
| 6 | tests/e2e/s008_upload_routes.py | `autopilot--main--S008--6d2f` | `/api/upload` REST + WS `addDirs` / `@` refs (image paste) | same as 1 + `/tmp/palmux-uploads/...` write path | C |
| 7 | tests/e2e/s009_multi_tab.py | `autopilot--main--S009--e24f` | tabs CRUD; multiple Claude tabs | `palmux2_test_fixture` + `primary_branch_id` + create extra claude/bash tabs | D |
| 8 | tests/e2e/s009_fix_lifecycle.py | env-only; default `tjst-t--palmux2--2d59` first branch | tmux session lifecycle: create / refresh / kill | `palmux2_test_fixture` + `primary_branch_id` + tab creation | D |
| 9 | tests/e2e/s009_fix_lifecycle_v2.py | `autopilot--main--S009-fix-2--544b` | bash tab create + claude tab + session group | `palmux2_test_fixture` + `primary_branch_id` + `create_bash_tab` | E |
| 10 | tests/e2e/s009_fix_periodic_check.py | empty (auto-detect first branch of REPO_ID) | sync_tmux periodic check after foreign kill | `palmux2_test_fixture` + `primary_branch_id` (then manually kill underlying tmux to test sync) | E |
| 11 | tests/e2e/s009_fix4_ui_monitor.py | empty (auto-detect first branch of REPO_ID) | bash tab UI mirroring + reload | `palmux2_test_fixture` + `primary_branch_id` + `create_bash_tab` | E |

> Note: 11 files appear because the Sprint description hedges on
> `s009_fix4_ui_monitor` ("これも入るかも"). Story 0 analysis confirms it
> follows the same hardcode pattern, so it is included — Story 1 hermetics
> all 11.

## Section C — sub-agent split (Story 1 parallel plan)

5 sub-agents, ~2 tests each:

- **A** — s001_refine_plan + s004_mcp_indicator (Claude WS + synthetic frames)
- **B** — s005_hook_events + s007_ask_question (Claude WS + synthetic + Playwright)
- **C** — s006_add_dir_file + s008_upload_routes (REST + upload + composer UI)
- **D** — s009_multi_tab + s009_fix_lifecycle (tabs CRUD + tmux lifecycle)
- **E** — s009_fix_lifecycle_v2 + s009_fix_periodic_check + s009_fix4_ui_monitor (3 — smallest each)

Each sub-agent (or this main agent serially, if parallel proves brittle —
see decisions.json) creates fixtures inside its tests via the helper, so
they cannot interfere with one another.

Note: per Sprint description the parallel sub-agent fanout is *optional*
("並列実行は worktree 並列が可能 ... 検討可"). After surveying the work
(only 11 files, each <300 lines refactor) I will execute Story 1 serially
in the main agent — that avoids worktree race conditions on the same
`make serve INSTANCE=dev` instance and is easier to verify in one pass.
The grouping above is still useful as a logical chunking order.

## Section D — order of operations (Story 1 execution)

1. Saa8506-1-1: add `primary_branch_id` / `open_claude_tab` /
   `create_bash_tab` helpers to `_fixture.py`. Verify by writing a tiny
   smoke `tests/e2e/_smoke_fixture_extras.py` (not in regression — just
   stdout) that exercises each helper, then delete it.
2. Saa8506-1-2 … 1-6: rewrite the 11 files one at a time, running the
   single test after each edit. After each pass, run the full regression
   suite with the env override layer in place (we delete it in Story 3).
3. Saa8506-1-7: full Master Regression Suite + manual smoke (single test
   invoked directly in a clean dev). Log to
   `regression-after-story1.json`.

## Section E — regression contract (recap)

- Master Regression Suite = go test ./internal/tab/claudeagent/... + go
  build ./... + npm fe build + npm fe lint (errors=0) + 22 E2E.
- During Story 1 the 22 E2E count stays at 22 (only the underlying
  fixture mechanic changes).
- Story 2 adds `s006_attach_dnd_wire` → 23.
- Story 3 removes the env override layer from the regression script. The
  hermetic tests must still produce 23/23 green after that.
