# S009-fix-1 — Tab lifecycle / WS reconnect bug fixes

Emergency fix sprint. Sprint S009 (multi-instance Claude/Bash tabs) shipped
in M1 but the post-S015 refine review surfaced four user-visible
regressions. None were caught by the original `s009_multi_tab.py` E2E
because that test only walks the happy path on a freshly-opened branch
where the tmux session is guaranteed alive.

## Reported bugs (verbatim)

1. **Claude タブを `+` で増やしたあと、消すと、Bash タブもいっしょに消えます**
2. **そのあと Claude タブを `+` で増やすと Bash も復活してきます**
3. **Bash タブが 3 秒ごとぐらいに reconnecting になります**
4. **Bash タブを増やしてもタブが増えなかったり、増えても Reconnecting のままつながりません**

## Reproduction (pre-fix)

```bash
$ curl -s …/tabs                       # baseline
claude:claude  bash:bash  files  git
$ curl -s -XPOST …/tabs -d '{"type":"claude"}'
{"id":"claude:claude-2", …}            # 201
$ curl -s …/tabs                       # ← Bash is gone!
claude:claude  claude:claude-2  files  git
$ curl -s -XDELETE …/tabs/claude:claude-2
204
$ curl -s …/tabs                       # ← Bash still gone
claude:claude  files  git
```

## Root cause

`internal/store/store.go::recomputeTabs` derived the live Bash tab list
from `tmux ListWindows(session)`. When that call failed (which happens
routinely — sync_tmux runs every 5 s and there's a brief gap during the
recovery cycle while the session is being killed/recreated, plus the
two-instance shared `tmp/` setup we use for self-development races every
cycle), the code silently fell through to `windows = nil`, which
collapsed every multi-instance tmux-backed tab type into the empty list.
Every recompute that landed in that gap dropped the Bash tabs from the
snapshot.

For Bug 4 the same fragility hit the *write* path: `pickNextWindowName`
+ `tmux NewWindow` both fail with "can't find session" if the session
was GC'd between AddTab's RLock release and the tmux exec.

## Fix

Two narrow changes in `internal/store/`:

1. **`store.go::recomputeTabs`** — distinguish "ListWindows failed
   transiently" from "session is up with zero windows of this type".
   On failure, fall back to the previously-known tab list of each
   `Multiple()=true && NeedsTmuxWindow()=true` provider. On success
   with empty `byType[type]` we synthesise the canonical instance
   (`bash:bash`) so a fresh-session-zero-windows state still shows the
   user a usable Bash tab.

2. **`tab.go::AddTab`** — call `ensureBranchSession()` (a thin
   `collectOpenSpecs` + `ensureSession` wrapper) before
   `pickNextWindowName`. This is idempotent (`tmux has-session` plus a
   set-difference window pass) and closes the AddTab vs sync_tmux race.

One ergonomic FE addition:

3. **`frontend/src/components/panel.tsx`** — when the URL tab id is the
   bare type slug (`claude` or `bash`) and the branch's tab list has
   the canonical multi-instance form (`claude:claude` or `bash:bash`),
   `replace`-navigate to the canonical id. This was a leftover from
   S009's URL renaming; the s008 E2E and any external bookmark / hook
   notification using the legacy single-tab URL would otherwise land
   on a "Pick a tab" stub.

## Why these are minimal-blast-radius

- `recomputeTabs` already iterated `prevByType` indirectly through
  the in-memory branch state; we just stop discarding it on the
  transient-error path.
- `ensureBranchSession` calls `ensureSession`, which is the same path
  sync_tmux uses. No new tmux primitives.
- The Panel redirect runs only when `decodedTabId` is not in the
  current tabs and matches the bare-type set (closed-set: `claude`,
  `bash`).

## Tests

New: `tests/e2e/s009_fix_lifecycle.py` — 7 cases covering the four
reported bugs plus three regression-coverage cases:

- (a) add Claude → Bash intact
- (b) remove Claude:2 → Bash intact
- (c) re-add Claude → no resurrected Bash
- (d) rapid Bash adds during sync_tmux pressure
- (e) interleaved Claude/Bash add/remove independence
- (f) S009 cap/floor enforcement still in force (Files/Git → 403, last
  Claude → 409, 4th Claude → 409)
- (g) 15-second Bash persistence sample (1 Hz, 0 phantom drops)

Verified all S008-S015 E2E tests pass against the rebuilt dev instance:

```
=== s008_upload_routes ===     ALL CHECKS PASSED
=== s009_multi_tab ===         all S009 acceptance criteria covered
=== s009_fix_lifecycle ===     all S009-fix-1 cases covered
=== s010_files_preview ===     11 assertions OK
=== s011_text_edit ===         9 assertions OK
=== s011_drawio_edit ===       5 assertions OK
=== s012_git_core ===          PASS
=== s013_git_history ===       PASS
=== s014_conflict_rebase ===   PASS
=== s015_worktree_categorization === PASS
```

`go test ./...` clean.

## Decisions

- **D-1 — Preserve previous tab list across failed ListWindows**: the
  alternative was retrying the tmux call inside `recomputeTabs`, but
  that bakes timing assumptions into a function that's also called
  under the write lock from sync_tmux. The fall-back is read-only and
  self-converges within one sync cycle.
- **D-2 — Synthesise canonical bash window when session is up but
  byType[bash] is empty**: this matches what bash.Provider.OnBranchOpen
  guarantees at session creation time. Without this, a freshly opened
  branch would show 0 Bash tabs until ensureSession runs, even though
  the persisted contract is "always at least one Bash".
- **D-3 — `ensureBranchSession` in AddTab not in
  pickNextWindowName**: the right layer is the AddTab handler, since
  it's the only operation that has to *commit* to a new window name.
  pickNextWindowName stays a pure read.
- **D-4 — Legacy URL redirect lives in `Panel`, not router**: the
  router doesn't have access to the live tab list. Doing the redirect
  in Panel keeps the rule next to the consumer (`activeTab`) and uses
  the existing decodedTabId pipeline.

## Out of scope (deferred)

- The two-instance shared `tmp/` race (host palmux2 and dev palmux2
  both managing the same set of `_palmux_*` sessions and killing each
  other's not-yet-tracked recoveries). This is a development-setup
  quirk, not a production bug. Workaround documented in
  `docs/development.md` (use distinct INSTANCE names + separate config
  dirs). Long-term fix is owner-tagging tmux sessions with the
  managing palmux2 PID.
- The 3-second WS reconnect cycle the user observed (Bug 3) was the
  symptom of the same recomputeTabs issue: every dropped Bash tab in
  the FE snapshot tore down the Bash terminal-view → unmount → WS
  close → reconnect. With (a) and (b) holding stable, the WS now
  stays attached. Verified by case-g (15-second 1 Hz sample, 0
  phantom drops).
