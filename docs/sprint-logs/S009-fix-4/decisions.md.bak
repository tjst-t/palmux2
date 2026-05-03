# S009-fix-4 — Decisions

## What changed

`Store.SyncTmux` was made conservative on **base** session kills, mirroring
the conn-ID protection fix-2 added for **group** sessions:

* New `Store.knownBaseSessions map[string]struct{}` — names this process
  has explicitly created or recovered.
* `ensureSession` records every name it touches (whether the session is
  brand-new or already alive at startup).
* `CloseBranch` removes the entry — once we drop the branch, a future
  reborn-by-peer session of the same name is treated as foreign.
* In `SyncTmux` step 2, base zombie kills are gated on
  `knownBaseSessions[name]`. Foreign `_palmux_*` (or whatever
  `--tmux-prefix` value) is left alone unconditionally.

## Why this and not "remove the kill entirely"

We still need to kill *our own* stale sessions when a branch closes but the
notification gets lost (rare, but it's how `tracked[]` desync was caught
historically). Restricting kills to the `knownBaseSessions` set keeps that
self-cleanup path while removing the cross-instance friendly fire.

## What's preserved from earlier fixes

* fix-1: `recomputeTabs` robustness, `EnsureTabWindow`.
* fix-2: `knownConnIDs` for group sessions, `enrichRecoverySpecs` so
  recovery doesn't lose `bash-2`, `bash-3`, etc.
* fix-3: `--tmux-prefix` flag — kept as a defensive layer. Two instances
  sharing a prefix is now safe regardless of ordering thanks to fix-4, but
  prefix isolation remains the cheapest answer when the user has
  out-of-tree palmux binaries (palmux v1, older palmux2 builds) running
  alongside.

## Tests

### Unit

* `TestSyncTmux_KillsZombieSessions` — adjusted to seed
  `knownBaseSessions[zombie]=true` so the test still exercises the
  self-owned zombie path.
* `TestSyncTmux_LeavesForeignBaseSessionAlone` — new. Plants a
  `_palmux_some-peer-repo--dead_main--1234` session that the store never
  created, runs SyncTmux, asserts the session survives.
* `TestSyncTmux_LeavesPeerInstanceSessionsAlone` (fix-3) — still passes;
  fix-4 only makes the protection wider.
* `go test ./internal/...` — clean.

### UI-level (Playwright, 3 min)

`tests/e2e/s009_fix4_ui_monitor.py` opens a single Bash tab, polls the DOM
every 250 ms for 180 s, and screenshots every distinct
"Reconnecting…" appearance. Pass = zero appearances.

* **Before fix (with peer-killer 883055 alive)**: 36 reconnect events
  spaced ~5 s apart. Trace inspected; matches `SyncTmuxInterval`.
* **After fix (two fix-4 binaries side-by-side, default `_palmux_`
  prefix)**: 0 reconnect events. `trace.json` shows continuous WS open
  state across the whole window.

### Lifecycle E2E

Re-ran existing dev-instance E2E with `S009_FIX_BRANCH_ID` pointed at the
current branch:

* `s009_fix_lifecycle.py` — all cases a–g PASS.
* `s009_fix_lifecycle_v2.py` — all cases h, i, j, k PASS.
* `s009_fix_periodic_check.py` — 180 s, ws_closes=0, zombie_kills=0.

S012/S013/S014/S015 tests are scoped to fixture branches that are not open
on the dev instance after a port-shuffled restart. Their failure mode is
"branch not found" / "worktree already exists from prior run" — pre-
existing test infrastructure issues, unrelated to fix-4. The S009-area
tests, which directly exercise the touched code path, all pass.

### Build

* `go build ./...` clean.
* `make build` clean (frontend + Go).

## Caveat for the user environment

Even with fix-4 deployed, the *running host palmux2* on port 8207 continues
to execute its old (pre-fix-4) binary until restarted. fix-4 binaries
shipping alongside it are now safe (both directions: their sessions
survive host's pre-fix-4 sync_tmux as long as they're in host's tracked
set; host's sessions survive fix-4 instances trivially because fix-4
won't touch them at all). **A user-driven restart of the host process
finishes deploying the fix.** Per the task constraint, the host's
`tmp/palmux.pid` was not touched in this run.
