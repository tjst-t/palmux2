# Sprint Saa8506-1 — Manual smoke (fixture lifecycle)

Run date: 2026-05-11
Operator: autopilot agent (no human-in-the-loop)
Dev rig: `make serve INSTANCE=dev` (port 8205)

## Smoke: 11 hermetic tests, fixture lifecycle observed

Each test was invoked directly (not via the regression script) on the
clean dev rig. Before each, `git worktree list` count and the number of
directories under `~/ghq/github.com/palmux2-test/` were captured.

| Test | Fixture sprint label | Worktree leak after run | palmux2-test dir leak after run | Result |
|---|---|---|---|---|
| s001_refine_plan.py | `s001` | 0 | 0 | PASS — `worktree_clean=yes` |
| s004_mcp_indicator.py | `s004` | 0 | 0 | PASS — `worktree_clean=yes` |
| s005_hook_events.py | `s005` | 0 | 0 | PASS — `worktree_clean=yes` |
| s006_add_dir_file.py | `s006` | 0 | 0 | PASS — `worktree_clean=yes` |
| s007_ask_question.py | `s007` | 0 | 0 | PASS — `worktree_clean=yes` |
| s008_upload_routes.py | `s008` | 0 | 0 | PASS — `worktree_clean=yes` |
| s009_multi_tab.py | `s009-multi` | 0 | 0 | PASS — `worktree_clean=yes` |
| s009_fix_lifecycle.py | `s009-fix` | 0 | 0 | PASS — `worktree_clean=yes` |
| s009_fix_lifecycle_v2.py | `s009-fix2` | 0 | 0 | PASS — `worktree_clean=yes` |
| s009_fix_periodic_check.py | `s009-fix3` | 0 | 0 | PASS — `worktree_clean=yes` |
| s009_fix4_ui_monitor.py | `s009-fix4` | 0 | 0 | PASS — `worktree_clean=yes` |

Procedure for each test (representative — s001):
- pre: `git worktree list | wc -l` = 2; `ls ~/ghq/github.com/palmux2-test/ | wc -l` = 0
- run: `PALMUX2_DEV_PORT=8205 python3 tests/e2e/s001_refine_plan.py`
- observed: test prints `hermetic repo=palmux2-test--s001-... branch=s001-...`
- post: same counts as pre (delta = 0/0)

The hermetic fixture (`palmux2_test_fixture(sprint)` in
`tests/e2e/_fixture.py`) reliably:
1. Creates a fresh ghq path `~/ghq/github.com/palmux2-test/{sprint}-{ts}-{pid}` and `git init -b main` + initial commit + dummy origin.
2. Registers the repo via `POST /api/repos/{id}/open`.
3. Yields the `Fixture` object with `repo_id`, `ghq_path`, `path`.
4. On `__exit__` (or SIGINT/SIGTERM/atexit fallback): `POST /api/repos/{id}/close` + `shutil.rmtree(path)`.
5. New helpers in `_fixture.py` Saa8506-1-1: `primary_branch_id()`, `open_claude_tab()`, `create_bash_tab()`, `wait_for_tab()` — used by the 11 hermetic tests so each one is self-contained.

Across 11 tests there were **zero** worktree leaks and **zero** palmux2-test directory leaks.

worktree_clean=yes
