# Sprint S021 — Autonomous Decisions

Sprint: **S021 Subagent worktree lifecycle (cleanup + promote)**.
Branch: `autopilot/main/S021` (forked from `main` at 87068e2).
Started: 2026-05-01.
Dev instance: `:8208` (`make serve INSTANCE=dev`, prefix `_pmx_dev_`).

## Planning Decisions

- **Stale judgement = lock-file absence + last-commit age**. Lock file
  pattern `.claude/autopilot-*.lock` (matches the file `autopilot` skill
  creates while a sub-agent is mid-flight). Last-commit age is queried
  via `git log -1 --format=%cI HEAD` against the worktree path. A
  worktree is "stale" iff (no lock file) AND (last commit older than
  `subagentStaleAfterDays` days). Spec said "lock + N days" so we anchor
  on the spec text.
- **Threshold setting**: `subagentStaleAfterDays` int, default `7`.
  Negative or zero treated as "use default". Live in
  `internal/config/settings.go` as `SubagentStaleAfterDays`. PATCH only
  accepts positive integers.
- **API surface**:
  - `POST /api/repos/{repoId}/worktrees/cleanup-subagent`: body
    `{ "dryRun": bool, "branchNames": []string|nil }`. `dryRun=true`
    returns the list of stale worktrees with reasons; `dryRun=false`
    deletes them via `gwq remove`. When `branchNames` is provided,
    only those branches are considered (the FE narrows after dialog
    review). Response: `{ candidates: [...], removed: [...], failed:
    [{branchName, error}] }`.
  - `POST /api/repos/{repoId}/branches/{branchId}/promote-subagent`:
    moves a subagent worktree to the gwq-standard path
    (`<gwq-root>/<repo-host>/<owner>/<repo>/<branch>` — derived from
    `gwq` itself by reading its config or via `gwq add` semantics) and
    flips the branch into `userOpenedBranches`. We rely on `git
    worktree move` for the move and update the in-memory `WorktreePath`
    afterwards. Response: the updated branch dict.
- **Move strategy**: Use `git worktree move <old> <new>` (a thin shell
  rather than calling `gwq` because gwq doesn't expose a `move`
  subcommand directly). The new path is derived by querying `gwq` for
  its `worktree_dir` setting; failing that, we fall back to
  `<repo-parent>/<branch-as-dir>`. After the move, we need a `git
  worktree repair` only if the post-move path doesn't survive a `git
  worktree list` validation; we do it defensively.
- **WS events**:
  - `worktree.cleaned`: payload `{ repoId, removed: [{branchName, branchId, path}], failed: [...] }`.
    Emitted once per cleanup batch.
  - `branch.categoryChanged`: re-used (already exists from S015) for
    the promote case. Adding a separate `branch.promoted` would be
    duplicate signal.
- **FE structure**:
  - Subagent section header gets a "Clean up" button (small icon next
    to the count badge) that opens a modal listing the stale candidates
    (queried via `dryRun=true`) with per-row checkboxes and delete
    rationale (last commit time + lock-status).
  - Per subagent row: a "→ my" promote button next to the existing
    chip (and inside the context menu as "Promote to my worktrees").
    Clicking opens a confirm dialog (showing the new path it will land
    in) then issues the API call.
- **Partial-failure tolerance**: cleanup API runs each `gwq remove` in
  isolation, collecting per-branch errors into `failed[]` and returning
  200 with the breakdown. The FE surfaces one toast per failure +
  removes successful ones from the dialog. This is the
  DESIGN_PRINCIPLES "明示的" + "安全性" guidance.
- **Reusing existing assets** (DESIGN_PRINCIPLES "既存資産活用"):
  - `Store.PromoteBranch` already exists for the `userOpenedBranches`
    mutation; the new promote-subagent endpoint reuses it after the move
    succeeds.
  - The S015 `branch.categoryChanged` event is reused; no new event for
    promote.
  - `confirmDialog` in `frontend/src/components/context-menu/` is reused
    for the promote confirm.
- **Reconcile**: the existing `ReconcileUserOpenedBranches` handles
  drift caused by the cleanup, but only at startup. To keep in-memory
  state honest, the cleanup handler re-runs the worktree sync for the
  affected repo immediately after gwq remove succeeds.

## Implementation Decisions

- **Reusing `Store.CloseBranch` for cleanup** rather than re-implementing
  the gwq remove + tmux kill + repos.json drift handling: cleanup
  candidates are routed through `s.CloseBranch` which already runs that
  full lifecycle. This caught several edge cases for free (e.g. tmux
  session orphaning, S009-fix-4 known-base-session bookkeeping). Per
  DESIGN_PRINCIPLES "既存資産活用".
- **`git log -1 --format=%cI HEAD` runs against the worktree path**
  (cmd.Dir = worktree). This covers worktrees that have a different
  HEAD branch than the primary repo. A failure (unborn branch, no
  commits) is mapped to "zero time → not stale" so brand-new
  worktrees can't be deleted by accident. Per DESIGN_PRINCIPLES
  "安全性".
- **Promote uses raw `git worktree move`** rather than gwq because gwq
  has no `move` subcommand. After moving, we run `git worktree repair`
  defensively to keep the linked-worktree pointers consistent. The
  in-memory `WorktreePath` is patched directly under the write lock so
  subsequent `/api/repos` snapshots reflect the new location without
  waiting for the 30s sync ticker.
- **Destination derivation uses `gwq config get worktree.basedir`**
  with a `~/worktrees` fallback, plus the default
  `{Host}/{Owner}/{Repository}/{Branch}` template (with `:`/`/`
  sanitisation). The function is in `internal/store/subagent.go` so
  unit tests can reach it.
- **WS event payload is the full `SubagentCleanupResult`** so other
  clients can render the same dialog state (e.g. show which branches
  failed) without re-issuing the cleanup. The existing per-branch
  `branch.closed` events still fire per-removal — the consolidated
  `worktree.cleaned` is informational on top of that.
- **No new `branch.promoted` event**: subsequent dry-run state is
  recovered through the existing `branch.categoryChanged` path. The
  store's `PromoteBranch` call inside `PromoteSubagentBranch` already
  publishes that event.
- **FE store removes branches eagerly on cleanup confirm**: when the
  POST response carries `removed[]`, the local `repos` slice drops
  those branches before any WS round-trip. The Drawer therefore
  refreshes immediately, and the eventual `worktree.cleaned` /
  `branch.closed` events become idempotent no-ops.
- **Per-row promote button uses `↗`** (north-east arrow) rather than
  the `+` reserved for unmanaged → my because the action is
  semantically different (it MOVES files on disk, not just records a
  preference). The context-menu entry "Promote to my worktrees"
  triggers the same path with a confirm dialog showing the
  destination.
- **Cleanup dialog is intentionally not the project's existing
  `confirmDialog`** — we needed a multi-row, partial-failure-aware UI
  with checkboxes and per-row result indicators, which the simple
  confirm/cancel dialog can't represent. The new component lives at
  `frontend/src/components/subagent-cleanup-dialog.tsx` and is reused
  per-repo.

## Review Decisions

- **Promote fails fast when destination already exists** (e.g. left over
  from a previous interrupted run): the handler returns 500 with the
  message "destination already exists: <path>". The user can resolve
  by manually removing the directory and retrying. Adding an
  --override flag was deferred to backlog.
- **`Promoting the primary worktree` is rejected upfront** with
  ErrInvalidArg → 400. The category derivation already excludes the
  primary, so this guard is redundant in practice but defensive.
- **Skipping rows in dry-run for which `git log` itself errors out**
  (corrupted worktree): we log a warning and treat as "not stale" so
  the user must explicitly inspect them. Better than risking an
  unwanted delete.
- **Threshold of 0 days** is treated as "use default" rather than "all
  worktrees stale". A user who *actually* wants to mark every commit
  as stale can set 0.001 — but more likely they want a value > 0.

## Backlog Additions

- **`gwq move` subcommand upstream**: the current implementation
  reaches around gwq for the move because gwq doesn't expose one. If
  gwq adds it later, swap `git worktree move` for `gwq move` to keep
  basedir/template handling consistent.
- **Cleanup with destination override**: when promote fails with
  "destination already exists" the user has to clean up by hand. A
  follow-up could offer a `?force=true` flag or a confirm dialog with
  "remove existing destination first".

## E2E Result

`tests/e2e/s021_subagent_lifecycle.py` against the dev instance on port
:8209 — **all 8 scenarios PASS** in a single run:

1. dry-run lists exactly the stale subagent worktrees (excludes locked
   + fresh worktrees).
2. confirmed cleanup removes the targeted worktrees and the branches
   disappear from `/api/repos`.
3. `promote-subagent` moves a worktree to the gwq-standard path AND
   marks the branch as `userOpenedBranches`. The destination directory
   exists on disk after the call.
4. `subagentStaleAfterDays=1` flips a 2-day-old worktree from
   non-stale → stale.
5. WS `worktree.cleaned` event is broadcast.
6. Cleanup with no candidates returns 200 cleanly.
7. Mobile @media block in `drawer.module.css` includes a `.cleanupBtn`
   entry sized for tap.
8. Playwright drives the Drawer end-to-end: expand subagent section →
   click Cleanup → see candidate dialog → confirm → branches removed
   → `auto/pw-promote` worktree appears with the `↗` promote button.
