# S009-fix-3 — decisions

## Why prefix isolation, not socket isolation, not deeper filtering

Three approaches were on the table:

1. **Approach 1 — instance isolation by tmux session prefix.**
   Each palmux process picks a per-instance prefix (default `_palmux_`,
   override via `--tmux-prefix`). The `Makefile`'s `INSTANCE=dev`
   target injects `--tmux-prefix=_pmx_dev_` automatically. Two
   palmux processes on the same tmux server can't see each other's
   sessions because their prefix-matching code rejects the foreign
   namespace.

2. **Approach 2 — separate tmux server (`tmux -L palmux-dev`).**
   Add a `socket` field to every `exec.Command` invocation. Total
   isolation. But the user can no longer `tmux attach` to the dev
   instance using the default socket, every existing test fixture
   relies on the default socket, and every code path that calls into
   `internal/tmux` would need socket plumbing.

3. **Approach 3 — extend fix-2's `knownConnIDs` filtering.**
   Track every base session this process has ever created and skip
   anything else, even if the prefix matches. This is what fix-2 did
   for group sessions. Extending to base sessions works in principle
   but the namespace remains shared, and the next code path that walks
   `_palmux_*` (orphan detection, notify lookup, etc.) can re-introduce
   the bug. Complexity increases everywhere.

**Approach 1 chosen** because it solves the bug at its level — the
namespace itself — instead of layering filters on every consumer of
the namespace. It also matches the bootstrapping mental model already
documented in `CLAUDE.md`: dev runs in a separate worktree on a
separate port so it can't trample the host. Adding a separate prefix
extends the same isolation to the tmux session axis.

## Why `_pmx_<instance>_`, not `_palmux_<instance>_`

Originally we picked `_palmux_<instance>_` (so `INSTANCE=dev` →
`_palmux_dev_`). A strict `ParseSessionName` would reject
`_palmux_dev_<repo>_<branch>` from a default-prefix host because
`dev` lacks the `--` slug separator that real Palmux repoIDs always
have.

But that strict rule is only present in fix-3 code. A host palmux2
running pre-fix-3 still uses pure `HasPrefix(name, "_palmux_")`,
which happily matches `_palmux_dev_*` and kills those sessions every
5 s. A user who upgrades only their dev worktree first (the most
common rollout path) wouldn't see any improvement.

`_pmx_` doesn't start with `_palmux_`, so the unupgraded host's
prefix check returns false immediately. The fix takes effect the
moment the user restarts dev with a fix-3 binary, regardless of the
host's version.

The strict-parse rule is kept anyway as defence in depth — if a
future instance prefix accidentally starts with `_palmux_` (e.g.
hand-rolled by a user who didn't read this doc), a fix-3 host still
won't treat it as a peer.

## Why pre-fix-3 verification needed continuous monitoring

The reported pathology is periodic: ~5 s usable, then ~5 s in
"Reconnecting…", on repeat. S009-fix-1 and S009-fix-2 verification
sampled state in narrow windows and used drop budgets that were
generous enough to mask the cycle. The harness in
`tests/e2e/s009_fix_periodic_check.py` runs three independent
watchers for `S009_FIX_DURATION_S` seconds (default 180):

- WS continuity (count every close)
- marker round-trip (one echo every 2 s; record any drop)
- server log scrape (count zombie kills targeting our branch)

and reports the close→close interval distribution at the end. Fail =
any drop; an obvious cluster around 5 s in the intervals output
identifies sync_tmux as the culprit.

The harness is now the canonical regression test for this class of
bug — no ad hoc 30 s check with a "drop budget" survives.

## Migration story

- **Existing installs**: nothing changes. Default prefix is still
  `_palmux_`. Single-instance behaviour is identical.
- **Multi-instance dev**: `make serve INSTANCE=dev` now isolates the
  tmux namespace automatically. No code or workflow change required.
- **Hand-rolled deployments**: pass `--tmux-prefix=…` if running two
  palmux processes against one tmux server. The prefix should not
  start with `_palmux_` for cross-version safety.
