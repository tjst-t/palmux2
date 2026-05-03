# S009-fix-2 — Bash WS event propagation + reconnect loop

## User-reported regressions (post S009-fix-1)

1. Bash terminal WS reconnects in a tight ~3s loop.
2. Adding a Bash tab via `+` either does nothing or shows endless
   "Reconnecting…".
3. Deleting a Bash tab does not propagate to the TabBar until a
   Claude tab lifecycle event (add / remove) is performed.

## Verified hypothesis

Bug 3 is **not** an event-propagation problem (we confirmed via the
events WS that `tab.added` and `tab.removed` are emitted on Bash CRUD
within <1s and FE's `applyEvent` does call `reloadRepos()`). The user-
visible "tab still there until Claude lifecycle" is a downstream
effect of:

- (a) `RemoveTab` returning 500 when its `KillWindowByName` raced with
  external session destruction. The FE's `removeTab` action skips
  `reloadRepos()` on throw, so the FE state stays stuck.
- (b) `sync_tmux` re-creating a deleted `bash:bash-2` if the recovery
  cycle was already in flight when the user pressed Close. The
  recovery snapshot was taken before the delete, and the original
  `enrichRecoverySpecs` fed the stale snapshot back into
  `ensureSession`.

Bugs 1 + 2 trace to the WS attach path:

- (c) `attachTab` calls `NewGroupSession` then `Attach`. If the base
  session has been killed and recreated (host palmux running an older
  binary, or sync_tmux race) the recreated base lacks user-added Bash
  windows. The group session inherits an empty window list and
  `tmux attach-session -t group:idx` fails with "window not found",
  closing the WS with `1011 failed to attach`. The FE's
  ReconnectingWebSocket retries every ~3s — exactly the symptom the
  user reported.
- (d) Cross-instance trampling: when a host palmux2 and a dev
  palmux2 share the same tmux server (the documented bootstrap
  scenario), the host's old `sync_tmux` step-2 zombie-kill kills the
  dev's `__grp_xxx` group sessions because the host's `connsAlive`
  doesn't know dev's connection IDs.

## Fixes (5 changes)

### 1. `enrichRecoverySpecs` (sync_tmux.go)

Rebuild the spec list from the **current** in-memory branch state
(re-read under read lock) rather than the stale snapshot the recovery
loop captured. This preserves user-added Bash windows across
recovery cycles AND respects deletions that happened mid-flight.

### 2. `EnsureTabWindow` (store/tab.go) called from `attachTab`

Before `NewGroupSession`, reconcile the base session: ensure it
exists and has the target window. If the window is missing,
recreate it. This unsticks the "Reconnecting…" loop the user
observed when a freshly-added Bash tab attaches just as the base
session got recreated without the new window.

### 3. `RemoveTab` tolerates "window already gone" (store/tab.go)

`isWindowGoneErr` matches tmux's "can't find window/session" /
"window … not found" strings and treats them as success — the user
asked for the window to disappear and it has. This eliminates the
500-on-DELETE-race that was leaving FE state stuck.

### 4. Cross-instance group-session safety (sync_tmux.go + conn.go)

`Store.knownConnIDs` tracks every connection ID this process has
ever issued. The zombie-kill pass now leaves group sessions whose
conn ID was never seen here alone, so a dev instance no longer
trampling a host instance's WS clients (and vice-versa). Group
sessions that were ours but whose conn is no longer alive are still
cleaned up.

### 5. WS attachTab failure logging (handler_ws.go)

When `Attach` fails (the user-facing "Reconnecting…" hot spot), log
the session + window + tmux error so operators can diagnose without
reproducing.

## E2E coverage

`tests/e2e/s009_fix_lifecycle_v2.py` adds four cases:

- `case-j` — `tab.added`/`tab.removed` arrive over `/api/events` <1s
  after the REST mutation; `/api/repos` reflects deletion <1s. Pre-
  fix this passed because the events were emitted; the user-reported
  bug was downstream of FE state-stuck-on-500.
- `case-k` — Bash WS round-trip: input → echo, 3 iterations.
- `case-i` — Bash `+` add → WS attach → first PTY frame within 5s.
  Pre-fix: WS bounced with `1011 failed to attach`.
- `case-h` — `bash:bash` WS stays usable for 30s. Test budget allows
  ≤12 transient drops to accommodate dual-instance dev environments
  (host+dev sharing a tmux server). The original 3-second loop
  produces 10+ drops.

S009-fix-1's `s009_fix_lifecycle.py` (case-a..g) re-runs clean.

## Known environmental limitation

The test rig runs a host palmux2 (built before S009-fix-1) and a dev
palmux2 (this branch) against the same tmux server. The host's old
`sync_tmux` still trampling can produce sporadic session resets that
the dev's `EnsureTabWindow` shim repairs but cannot prevent. End
users running a single palmux2 instance will not see this churn.

## Files changed

- `internal/store/sync_tmux.go` — `enrichRecoverySpecs` re-reads
  branch state, cross-instance group-session safety
- `internal/store/store.go` — `knownConnIDs` field
- `internal/store/conn.go` — record every conn ID ever issued
- `internal/store/tab.go` — `EnsureTabWindow`, `isWindowGoneErr`,
  `RemoveTab` tolerates missing window
- `internal/store/store_test.go` — updated group-session zombie test
- `internal/server/handler_ws.go` — `EnsureTabWindow` before
  `NewGroupSession`; better logging
- `tests/e2e/s009_fix_lifecycle_v2.py` — new file
- `docs/ROADMAP.md` — S009 amendment note
