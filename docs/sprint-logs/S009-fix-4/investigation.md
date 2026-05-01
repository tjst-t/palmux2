# S009-fix-4 — Investigation

## Hypothesis (user-rejected fix-3 explanation)

> palmux2 host と palmux2 dev も同じ `_palmux_` を見ていて、互いを zombie 扱い。
> S009 で sync_tmux の orphan 検出が より aggressive になった可能性が高い
> (実際、S009-fix-2 / fix-3 で繰り返し触っている領域)

Pre-S009 (`04bfa9b`) was reportedly stable even with shared `_palmux_` prefix.
Post-S009 (`3c63887`) and through fix-1 / fix-2 / fix-3, even a single Bash tab
gets a periodic "Reconnecting…" overlay every 5 s. fix-3's `--tmux-prefix`
isolation is a *defensive* layer — it doesn't fix the underlying aggression.

## Diff archaeology

### sync_tmux.go: pre-S009 (04bfa9b) vs S009-done (3c63887)

```bash
$ git diff 04bfa9b..3c63887 -- internal/store/sync_tmux.go
# (empty — file is byte-identical)
```

So the zombie-kill code was present pre-S009 and S009 didn't touch it.

### sync_tmux.go: 3c63887 → 5813c8f (S009-fix-2)

fix-2 *added* a `knownConnIDs` filter for **group sessions** (`__grp_<connID>`):

```go
// fix-2 added this:
knownConns := make(map[string]bool, len(s.knownConnIDs))
for id := range s.knownConnIDs {
    knownConns[id] = true
}
…
if !knownConns[connID] {
    continue   // peer instance's group — leave alone
}
```

But **base sessions** (`_palmux_<repo>_<branch>` without the group suffix) were
left under the original liberal rule: *anything matching `IsPalmuxSession` and
parsing as `ParseSessionName` that's not in `tracked` gets killed.*

### What changes between S009-fix-2 (5813c8f) and S009-fix-3 (6ffee40)?

fix-3 introduced `--tmux-prefix=<custom>` so dev/host can use disjoint prefixes
(`_palmux_` vs `_pmx_dev_`). The base-session zombie kill code is unchanged —
fix-3 just lets the user *avoid* the collision.

## Why pre-S009 reportedly worked

The same liberal rule existed pre-S009. The bisection-vs-pre-S009 framing in
the user feedback is most plausibly explained by *workload* not *code* — pre-
S009 the user wasn't running the additional palmux2 instances (test runners,
worktree dev instances, scratch instances) that happen during S009-era
exploration. Once two `_palmux_*` instances coexist with disjoint repos.json
contents (instance C's tracked = ∅ vs instance D's tracked = {X, Y}),
instance C's sync_tmux deterministically erases D's sessions every 5 s.

In this session this was confirmed empirically:

* `tmp/palmux.pid` (host palmux2, port 8207, OLD binary, full repos.json)
* `tmp/palmux-dev.pid` (dev palmux2, port 8285, prefix `_pmx_dev_`)
* PID 883055 (palmux2 OLD binary, port 8243, **empty** repos.json, prefix `_palmux_`)
  — this was the silent killer

883055 was running the same `./bin/palmux` from a different config dir as a
left-over test instance. Watching tmux every 2 s showed
`_palmux_tjst-t--palmux2--2d59_autopilot--main--S009-fix-4--dbc1` flicker
(created → killed → created…) on a ~5 s cadence matching `SyncTmuxInterval`.
After `kill 883055` the same session held its `created` timestamp stable
indefinitely.

## Root cause statement

`SyncTmux` step 2 ("Kill zombie Palmux sessions") trusts the `_palmux_*`
prefix as proof that *this* process owns the session. It does not. Any peer
palmux instance sharing the prefix and a tmux server but with an empty or
partial repos.json will deterministically erase the peer's sessions, which
the user sees as the bash terminal flapping into "Reconnecting…" every 5 s.

This was also true pre-S009 — but pre-S009 the user wasn't running multiple
instances, so the bug was latent.

## Proof reproductions captured in this session

### Before fix-4 (with peer-killer in the mix)

3-min Playwright monitor against port 8207, single Bash tab, 36 distinct
"Reconnecting…" overlay appearances (~one every 5 s), all aligned with
WS close on `/tabs/bash:bash/attach`. (Earlier `trace.json`, since
overwritten by the fix verification run; pattern matches WS close cycle of
≈5 s.)

### After killing 883055, before reinstalling fix-4 binaries

Same setup minus 883055: session `created` timestamp stays put for
9 minutes, sync_tmux logs go quiet on the palmux2 line.

### After fix-4 with two fix-4 instances coexisting (port 8290 attacker
with empty repos.json, port 8291 victim with full repos.json, both binaries
patched)

3-min Playwright monitor against 8291 — `ui_reconnect_events=0`. No
"Reconnecting…" overlay observed. WS attach on bash:bash holds open across
the entire 180 s window. Trace stored at `trace.json`.
