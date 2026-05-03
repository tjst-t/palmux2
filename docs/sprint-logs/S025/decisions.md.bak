# Sprint S025 — Autonomous Decisions

E2E test fixture cleanup hygiene. Goal: ensure E2E tests never leave
behind `palmux2-test/` repos in `~/ghq/github.com/` or in the dev
`tmp/repos.json`, and provide a one-shot script for any drift that does
escape.

## Planning Decisions

- **Single Story, 8 tasks**: the Sprint scope was already crisply
  factored (script → bulk delete → helper → refactor → atexit → make
  target → run + verify → S025 E2E). No further story split needed —
  every task contributes to the same user-facing outcome ("E2E never
  litters my drawer").
- **No GUI Story**: no UI surfaces are added or changed. `gui-spec`
  invocation skipped per its own contract (it only runs for GUI work).
  Verification is exclusively API + filesystem.

## Implementation Decisions

- **Cleanup script: Python over shell** — `scripts/cleanup-test-fixtures.py`
  is Python because it must read JSON (`repos.json`) and call HTTP APIs.
  Shell would need `jq` + `curl` + careful array escaping; Python's
  stdlib gives us all of it. Rationale: DESIGN_PRINCIPLES "明示的 >
  暗黙的" — JSON parsing in Python is explicit; jq plumbing is fragile.
- **Helper architecture: `_fixture.py` module + context manager + atexit**
  not `conftest.py` — the existing E2E tests are plain `python3 X.py`
  scripts (not pytest), so we built a stdlib-only helper that:
    1. exposes `make_fixture(sprint)` returning a `Fixture` dataclass
    2. exposes `palmux2_test_fixture(sprint)` context manager
    3. registers each `Fixture` in a `WeakSet` plus an `atexit` hook
       and SIGINT/SIGTERM handlers — so even on Ctrl-C the fixture is
       removed.
  Rationale: DESIGN_PRINCIPLES "既存資産活用" — we kept the existing
  `make_fixture_repo() / fixture_cleanup()` API surface in each test
  and made them thin wrappers over the new helper, so the per-test
  diffs are <30 lines each and call-sites are unchanged.
- **Cleanup channel: API first, file write fallback** — when the dev
  server is reachable the script calls `POST /api/repos/{id}/close` so
  palmux2's tmux/state hooks fire. If the server is down we write
  `repos.json` directly. Both branches are exercised by the script and
  tested.
- **Safety: refuse non-dev config dirs** — script's `looks_like_palmux2_tmp`
  guard requires the config dir to live under the palmux2 repo root,
  unless `--force`. Rationale: DESIGN_PRINCIPLES "責務越境最小" — host
  palmux2's `~/.config/palmux/` MUST never be touched by this script.
- **Dataclass `eq=False`** — `Fixture` lives in a `WeakSet`, which
  requires identity-based hashing. Default dataclass eq=True breaks
  hashability; `eq=False` restores `__hash__`.
- **Signal handler installs on `default_int_handler` too** — Python's
  default SIGINT handler is not `SIG_DFL`; it's `default_int_handler`.
  We treat that as "still default" so the cleanup hook installs cleanly
  on a vanilla test process while still respecting an explicit user
  handler.

## Verification Decisions

- **Pre-existing garbage purged in-place** — script run #1 (real, not
  dry) cleaned 7 stale `palmux2-test--*` IDs and 4 fixture dirs. The
  starting state for the regression run was steady-state-clean.
- **Regression of all 7 existing E2E tests** — each was re-run against
  the running dev instance on port 8215 with the new helper:
  `s015 / s016 / s016_fix1 / s020 / s021 / s023 / s024` all PASS.
  Between every test the invariant `repos.json[palmux2-test]==0 AND
  ghq/palmux2-test/==0 dirs` held.
- **S025 E2E with 4 scenarios** — `tests/e2e/s025_fixture_cleanup.py`
  validates (a) script clears pre-existing leaks, (b) context manager
  cleans on normal exit, (c) context manager cleans on exception
  inside the body, (d) `make e2e-cleanup` works. All PASS.
- **Signal coverage validated** — manual verification: SIGINT-via-os.kill
  → atexit ran → fixture cleaned. SIGTERM-via-os.kill → signal handler
  ran → fixture cleaned. Both leftover-counts == 0.

## Backlog (none added)

No drift discovered during S025. The cleanup script, helper, and Make
target are all single-purpose; no scope-external bugs needed deferring.
