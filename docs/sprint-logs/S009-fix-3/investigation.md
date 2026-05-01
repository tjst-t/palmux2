# S009-fix-3 — periodic Bash WS reconnect cycle: investigation log

## Reported symptom

> 数秒使えて、 数秒 Reconnecting、 数秒使えるを繰り返す。
> たぶんチェックしたタイミングでは OK なんだろうね

A Bash terminal tab WS oscillates between "usable" and "Reconnecting…"
on a fixed period. The S009-fix-1 / S009-fix-2 verification suites both
sampled state in short windows and used a generous "drop budget" so
this periodic pattern wasn't surfaced — the user is correct that
single-point checks miss it.

## Improved detection harness

`tests/e2e/s009_fix_periodic_check.py` — three independent watchers
run for `S009_FIX_DURATION_S` seconds (default 180):

1. **WS continuity** — open one Bash attach WS and keep it open the
   whole window. Auto-reconnects after each close so we count drops
   instead of bailing on the first one. Pass = 0 closes.
2. **Marker round-trip** — every 2 s, send a unique `echo` to a fresh
   Bash WS and verify the marker echoes within 3 s. Pass = every send
   completes. (User-observed "Reconnecting" maps to marker_timeout /
   marker_dropped.)
3. **Server log scrape** — tail the dev palmux2 log for
   `sync_tmux: killing zombie session` and recovery events scoped to
   our test branch.

The harness reports the close→close interval distribution at the end
so a periodic pattern is immediately visible (a tight cluster around
the sync_tmux period = smoking gun).

## Reproduction (60s run on `5813c8f` HEAD against dev :8284)

```
Summary: duration=61.6s  ws_closes=12  marker_fails=2  zombie_kills_for_our_branch=0
FAIL: detected reconnect cycle / zombie kill activity
close→close intervals (s): [4.98, 5.02, 4.98, 5.03, 4.95, 5.05, 4.98, 4.99, 5.06, 4.98, 4.95]
```

Twelve `ws_close` events in 60s, **all spaced ~5 s apart with sub-100ms
jitter**. That is exactly `SyncTmuxInterval = 5 * time.Second`.

## Root cause

Two palmux2 processes were running:

- host  (PID 2504507, port 8207) — `cwd=…/palmux2`,
  `--config-dir ./tmp`, binary built post-fix-2
- dev   (PID 2138351, port 8284) — `cwd=…/palmux2`,
  `--config-dir ./tmp`, binary built pre-fix-2 (started 13:21,
  fix-2 committed 13:25)

Both share **the same `_palmux_` session prefix**, the same
`./tmp/repos.json`, and the same tmux server. Each process runs its
own `sync_tmux` loop on a 5s interval against the global tmux state.

The dev binary still contained the pre-fix-2 zombie-kill of group
sessions whose conn IDs it didn't know — so every 5s it was killing
the host's `__grp_{connID}` sessions (and our test harness's). Even
after fix-2 lands everywhere, the structural issue remains: if a user
ever runs two palmux2 processes against the same tmux server, the two
sync_tmux loops race over `_palmux_*` sessions and group sessions,
each treating the other's state as zombie / missing.

The host palmux2 also kept "recovering" the same set of sessions
every 5s — recovery uses `tmux new-session` against an existing
session, which produces `duplicate session: …` and (more importantly)
forces the host into operations that aren't safe to interleave with
the other instance's WS clients.

In short, **the `_palmux_` prefix is shared global state**. Any
multi-instance dev workflow ends up interfering with itself, and
that interference manifests to the user as the periodic Reconnect
loop. The S009-fix-2 `knownConnIDs` patch only narrowed the group-
session zombie-kill case; the base session race remained.

## Fix decision

**Approach 1 — instance isolation by tmux session prefix.**

Add a `--tmux-prefix` CLI flag (default `_palmux_`) and have
`make serve INSTANCE=<name>` automatically pass
`--tmux-prefix=_palmux_<name>_`. The host instance retains the
canonical prefix; the dev instance lives at a disjoint prefix; the
two processes literally cannot see each other's sessions.

This is preferred over Approach 2 (separate tmux server via `tmux -L`)
because it preserves a single tmux server (so the user can `tmux
attach` to either instance without picking a socket) while still
giving us complete isolation, and over Approach 3 (per-instance
filtering layered on top of fix-2) because filtering is a band-aid —
two instances still touch the same name space and any new code path
that forgets to filter brings the bug back.

## Refinement during implementation

A first attempt used `_palmux_<instance>_` (so `dev` → `_palmux_dev_`)
and a strict `IsPalmuxSession` that requires the post-prefix repoID
segment to contain `--`. The strict-parse check was correct but
**didn't help against an unupgraded host**: a host palmux2 process
running pre-fix-3 code is still pure `HasPrefix(name, "_palmux_")`,
which happily matches `_palmux_dev_*` and kills those sessions every
5 s as zombies. The 60 s and 180 s runs both fail in this state.

To make instance isolation work *across versions* (so a user can roll
fix-3 to dev first, get a clean test, and then upgrade host at their
leisure), the dev prefix must not start with `_palmux_` at all. The
shipped Makefile uses `_pmx_<instance>_`:

- pre-fix-3 host: `HasPrefix("_pmx_dev_*", "_palmux_")` → false →
  ignored.
- post-fix-3 host: same `HasPrefix` returns false; even the strict
  `ParseSessionName` rejects (it requires the canonical prefix).
- dev (fix-3): configured prefix is `_pmx_dev_`; only its own sessions
  match.

The strict `ParseSessionName` rule (repoID must contain `--`) is kept
anyway so that *future* instance prefixes that do start with `_palmux_`
(e.g. an installer that doesn't go through the Makefile) are still
rejected by the host. Defence in depth.

## Verification result

After the fix and `_pmx_dev_` prefix:

```
S009-fix-3 periodic check: http://localhost:8285  …  branch=…  duration=180s
  [  15.0s] markers sent=7  echoed=7  ws_closes=0 marker_fails=0 zombie_kills=0
  [  30.0s] markers sent=13 echoed=13 ws_closes=0 marker_fails=0 zombie_kills=0
  …
  [ 180.0s] markers sent=78 echoed=77 ws_closes=0 marker_fails=0 zombie_kills=0
Summary: duration=182.1s  ws_closes=0  marker_fails=0  zombie_kills_for_our_branch=0
PASS: no reconnect cycle detected over the monitoring window
```

3 minutes, 78 markers, 77 echoes (1 final marker raced the deadline),
zero ws_closes, zero marker_fails, zero zombie kills targeting our
branch — vs. the pre-fix run that had a `ws_close` every ~5 s with
sub-100 ms jitter.

Other suites against the same dev port:

- `s009_fix_lifecycle.py` (S009-fix-1): PASS, all 7 cases.
- `s009_fix_lifecycle_v2.py` (S009-fix-2): PASS, all 4 cases —
  case-h reports drops=0 / budget=12.
- `s009_multi_tab.py`: PASS, all acceptance criteria.
- `s008_upload_routes.py`: PASS.
- `s015_worktree_categorization.py`: PASS.

`go test ./...` clean (added two new tests in
`internal/domain/naming_test.go` and one in
`internal/store/store_test.go`).
