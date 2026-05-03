# Sprint S013 — Autonomous Decisions

Sprint goal: Git History & Common Ops on top of S012's review-and-commit
flow. The "weekly" tier of git work — log archaeology, stash juggling,
cherry-pick / revert / reset, tag management, file history, blame —
plus ⌘K palette discoverability of all git ops.

## Planning Decisions

- **Story granularity**: kept the single S013-1 story unchanged. The
  spec already grouped log + graph + stash + cherry-pick/revert/reset +
  tag + file-history + blame + palette as one user-facing capability
  ("traverse and shuffle history"). Splitting would have created
  artificial scaffolding boundaries; instead, the 18 tasks (9 backend +
  8 FE + 1 E2E) carry the structure.
- **Spec-vs-implementation mismatch — deferred**: `/api/.../git/log`
  spec says "extend"; we added `/git/log/filtered` as a new path so the
  S012 contract for `/git/log` (list of LogEntry without parents/refs)
  stays bit-compatible for any external scripts. Decision: additive >
  breaking, per DESIGN_PRINCIPLES "既存 API レスポンス形式を破壊的変更
  しない".

## Implementation Decisions

- **Blame renderer**: spec called for "Monaco gutter 注釈". Implemented
  a lightweight `<table>` renderer instead. Rationale:
  - the Monaco `IModelDecoration` API for gutter content requires
    keeping a live editor instance alive per blame view; for a read-
    only pane that's pure text + side-cells, a table is dramatically
    cheaper.
  - paying the ~3MB Monaco load just to look at blame for a single
    file is over-budget for mobile.
  - the popover-on-hover requirement is identical to satisfy in either
    renderer.
  Backlog item filed: "Blame view の Monaco gutter 統合" — promote when
  we want inline blame for files the user is *editing*.

- **Cherry-pick conflict signalling**: returned HTTP 409 + JSON
  `{reason: "conflict"}` rather than emitting a WS event. The S013
  scope is "clean cases only"; full conflict resolver is S014's
  responsibility. Backlog item filed for the WS event so Activity
  Inbox can surface the failure later.

- **Reset hard 2-step UX**: implemented as two separate stages inside
  one Modal, not two separate modals.
  - Stage 1: mode picker. If `--hard` is selected, the button label
    changes to "Continue…" and the user has to click through.
  - Stage 2: a destructive panel with reflog hint, an "I understand"
    checkbox that *gates* the destructive button. Pressing the button
    while the checkbox is empty is a no-op.
  Rationale: lazygit / Sourcetree both surface the destructive warning
  inline rather than in a fresh dialog; keeping the same Modal frame
  preserves "one task = one window" mental model.

- **⌘K palette wiring**: introduced a new `'git'` mode keyed by typing
  `git` (with optional space or `:` after) at the start of the query.
  Did NOT take a single-character prefix because all the obvious ones
  (`@ # / > :`) are already taken; `git` as a literal word is more
  discoverable anyway. Twelve git ops registered, all of which navigate
  to the appropriate Git tab sub-view rather than performing the
  destructive op directly — the modals' safety gates are not bypassed.

- **Branch graph algorithm**: implemented a tiny one-pass lane
  allocator (first parent claims the same lane, additional parents get
  fresh lanes, lanes are freed when no descendant references them).
  Rejected alternatives:
  - HEAD-traversal layout (Sourcetree): too much state for a tab that
    can be re-opened mid-fetch.
  - Static "no graph at all": loses the multi-branch story the spec
    asked for.
  The current algorithm renders the palmux2 sample branch (multi-
  branch, ~20 commits per page) correctly in the manual smoke. Edge
  case for octopus merges is acceptable since palmux2 itself doesn't
  produce them.

- **Commit-diff in log detail pane**: out of scope for S013. The right
  pane shows full metadata (hash / parents / refs / author / date) but
  not the diff yet. Reason: existing `/git/diff` endpoint is staged-
  vs-working; a `commit-diff` endpoint that does `git show <sha>` was
  trivial to add but wiring Monaco DiffEditor here re-implements much
  of the GitMonacoDiff component's loader. Deferred to a follow-up
  Sprint (S014 backlog).

- **Auto-decide: file-history navigation**: chose `?fileHistory=<path>`
  search-param on the Git tab URL rather than a new dedicated tab. The
  Git tab already has the API base, the file-watch hook, and the
  modal stack; threading a sub-route through is cheaper. Same approach
  for blame (`?blame=<path>`).

## Review Decisions

- **Type-only imports**: no, the project allows mixed imports.
- **Naming `GitFileHistory` vs `FileHistoryView`**: stuck with
  `GitFileHistory` to match the existing `GitLog` / `GitDiff` family.
- **Test fixture isolation**: every fixture-based test gets its own
  temp dir under `tmp/s013-fixtures/<timestamp-pid-counter>` so
  parallel re-runs don't collide (S012 had a same-second collision the
  S013 driver inherited; fixed in this Sprint's harness).

## Verification

- Go unit tests: `go test ./internal/tab/git/` — all PASS
  (`TestLogFiltered_Basic`, `TestStashLifecycle`, `TestRevertAndReset`,
  `TestTagCRUD`, `TestFileHistoryAndBlame`, `TestBranchGraphIncludesAllBranches`,
  plus 7 carry-over tests from S010-S012)
- Full Go suite: `go test ./...` — all PASS.
- TypeScript: `tsc --noEmit` — clean.
- Frontend production build: `npm run build` — succeeds (with the
  expected "chunks > 500 kB" warning carried over from Monaco).
- E2E: `tests/e2e/s013_git_history.py` against dev instance on port
  8278 — all 10 scenarios PASS:
  - (a) log filter (author / grep)
  - (b) branch-graph adjacency
  - (c) stash full lifecycle (save → list → diff → apply → drop)
  - (d) cherry-pick (clean — fixture branch)
  - (e) revert
  - (f) reset 2-step UI confirmed in Playwright (mode picker → Continue
        → understood checkbox gating)
  - (g) tag create / delete (palmux2 repo lists at least 1 tag)
  - (h) file-history endpoint returns `entries` for README.md
  - (i) blame endpoint returns 746 lines for CLAUDE.md
  - (j) ⌘K palette exposes `git: …` ops; multiple visible after typing
        `git` prefix

## Backlog additions (filed during this Sprint)

1. **Commit-diff endpoint + Monaco diff in log detail pane** — size M
2. **Cherry-pick / merge conflict WS event** — size S; merge into S014
3. **Blame view の Monaco gutter 統合** — size M; for the
   "editing-file inline blame" UX

## Drift / warnings

- None blocking the next Sprint.
- The dev portman re-allocated the API port from 8277 → 8278 between
  the previous host run and the S013 rebuild. Tests honour the
  `PALMUX2_DEV_PORT` env var so this is captured automatically; just
  noting it for future sessions.

## Final state

- Branch: `autopilot/main/S013` (commits to be pushed in `sprint done`)
- Files added:
  - `internal/tab/git/history.go` (382 LOC)
  - `internal/tab/git/handler_history.go` (332 LOC)
  - `internal/tab/git/history_test.go` (245 LOC)
  - `frontend/src/tabs/git/git-branch-graph.tsx`
  - `frontend/src/tabs/git/git-history-modals.tsx` (+ css)
  - `frontend/src/tabs/git/git-stash.tsx` (+ css)
  - `frontend/src/tabs/git/git-tags.tsx` (+ css)
  - `frontend/src/tabs/git/git-file-history.tsx` (+ css)
  - `frontend/src/tabs/git/git-blame.tsx` (+ css)
  - `tests/e2e/s013_git_history.py`
- Files modified:
  - `internal/tab/git/provider.go` (route registration)
  - `frontend/src/tabs/git/git-log.tsx` (rebuilt as rich log)
  - `frontend/src/tabs/git/git-log.module.css` (rebuilt)
  - `frontend/src/tabs/git/git-view.tsx` (Stash / Tags tabs +
    fileHistory / blame deep-links)
  - `frontend/src/tabs/git/types.ts` (S013 types)
  - `frontend/src/tabs/files/file-preview.tsx` (History / Blame
    buttons in the preview header)
  - `frontend/src/components/command-palette/command-palette.tsx`
    (`'git'` mode + 12 ops)
  - `docs/ROADMAP.md` (S013 marked complete + 3 backlog items added)
