# Sprint S015 — Autonomous Decisions

Sprint: **S015 Worktree categorization (my / unmanaged / subagent)**.
Branch: `autopilot/main/S015` (forked from `main` at f688a32).
Started: 2026-05-01.

## Planning Decisions

- **Schema location for `user_opened_branches`** (S015-1-1): added the slice to
  the existing `RepoEntry` struct in `internal/config/repos.go` rather than a
  new file, since it is a per-repo property already keyed by repo ID. The
  `omitempty` tag preserves backward compatibility — old `repos.json` files
  parse unchanged. Guided by DESIGN_PRINCIPLES "既存資産活用".
- **Default `autoWorktreePathPatterns`** (S015-1-2): `[".claude/worktrees/*"]`
  matches the pattern used by claude-skills sub-agents (the only known
  generator today). Patterns are **substring globs** matched against the
  worktree's absolute path, with `*` mapped to `[^/]*` and the prefix
  anchored anywhere within the path so users can write `.claude/worktrees/*`
  without having to know the absolute repo root. This keeps configuration
  values portable across machines (DESIGN_PRINCIPLES "明示的").
- **Promote API verbs** (S015-1-3/4): chose `POST .../promote` and
  `DELETE .../promote` rather than `PATCH /branches/{id}` because the
  underlying state lives outside the branch entity (in the repo's
  `user_opened_branches` slice, not on the worktree). A dedicated endpoint
  keeps the action obvious in network logs.
- **Category field name** (S015-1-5): `category: 'user' | 'unmanaged' | 'subagent'`
  — `user` rather than `my` for spec parity with claude-skills internal
  vocabulary; the FE remaps `user → my` for the section title.
- **WS event name** (S015-1-3/13): `branch.categoryChanged` with payload
  `{ repoId, branchId, category }`. Reuses existing EventHub fan-out.
- **Reconcile policy** (S015-1-7): drop missing `user_opened_branches`
  entries silently at startup. Logged at INFO level. Failure of one repo's
  reconcile does not halt the others (panic-safe per task).
- **Drawer FE structure** (S015-1-8): wrap each repo's branch list with
  three nested sub-sections. Per-repo nesting (rather than three flat
  global sections) preserves the existing repo-grouping UX and avoids a
  fundamental drawer rewrite — DESIGN_PRINCIPLES "既存資産活用".
- **localStorage keys** (S015-1-9): `palmux:drawer.section.<key>.collapsed`
  where `<key>` is one of `my` / `unmanaged` / `subagent`. Single global
  collapse per section (not per repo) keeps the UI predictable across
  sprawling multi-repo setups.
- **Promote FE optimism** (S015-1-12): optimistic update — the entry's
  `category` field is rewritten in the local store immediately and rolled
  back on API failure (toast surfaces the error). Matches the existing
  `closeBranch` / `addTab` patterns elsewhere in palmux-store.

## Implementation Decisions

- **Split `OpenBranch` → `OpenBranch` (explicit) + `OpenBranchAuto` (sync
  loop)**: the original implementation tracked any `OpenBranch` call as
  user-opened, which incorrectly reclassified CLI-created worktrees
  (picked up via `sync_worktree`) as `user`. Solved by routing the sync
  loop through `OpenBranchAuto`, which skips the `userOpenedBranches`
  append. Caller-discriminated semantics rather than a context-flag
  parameter — clearer intent at the call site.
- **Pattern matcher**: implemented as a substring glob (segment-aware,
  `*` = single segment) inside `internal/store/category.go`. Avoided a
  full-fledged `doublestar` dependency since `.claude/worktrees/*` is
  the only known production case and a 60-line substring matcher with
  `path.Match` per segment covers it. Backed by `category_test.go` (7
  cases including custom patterns and edge cases).
- **Categories applied at read time, not on write**: `Repos()`/`Repo()`/
  `Branch()` re-derive `branch.Category` under the write lock before
  returning a snapshot. Avoids stale categories after settings PATCHes
  (no cache invalidation discipline needed) at the cost of one map
  iteration per list call. Acceptable: list calls are rare relative to
  branch reads inside the agent loops.
- **FE optimistic update + WS confirmation**: `promoteBranch` /
  `demoteBranch` flip the local `category` immediately and roll back on
  HTTP error. The server's `branch.categoryChanged` event then
  authoritatively rewrites the category — useful when the optimistic
  guess for demote (`unmanaged`) was wrong (target was actually
  `subagent`).
- **Subagent section default-collapsed only**: `my` and `unmanaged`
  default-expanded matches the spec. Subagent badge shows count even
  when collapsed (`subagent / autopilot (3)`).
- **Always render `my` section** even when empty so the
  `+ Open Branch…` button has a stable home. `unmanaged` and `subagent`
  hide entirely when their bucket is empty (spec said "show when
  branches exist").
- **`+` promote action is a row-level button, not just a context-menu
  item**: spec called for both, so we provide both. The row button is
  the primary affordance (one tap to promote); the context menu also
  exposes "+ Add to my worktrees" (unmanaged) and "Remove from my
  worktrees" (my, when not primary).

## Review Decisions

- **`Branch.Category` is `omitempty`**: pre-S015 clients receive no
  category field (effectively `unmanaged` if they care). The FE remaps
  `undefined → my` to keep legacy behavior usable during a roll-out.
- **Reconcile is best-effort + per-repo isolated**: a single repo with a
  broken `git worktree list` (e.g. corrupted .git) does not block the
  rest. `recover()` traps any panic in the reconcile loop.

## Backlog Additions

- **Subagent → my promotion** (S021 already covers this; verified entry
  exists in roadmap line 1290).
- **Toast component for promote-failure UX**: current FE logs to
  console only when promote fails. Acceptance criterion mentions Toast,
  but no shared Toast primitive exists yet. Tracked for a follow-up
  sprint along with the wider notification UX consolidation.

