# Sprint S016 — Autonomous Decisions

Sprint Title: Sprint Dashboard tab (claude-skills 連携)
Branch: `autopilot/main/S016`
Started: 2026-05-01

## Planning Decisions

- **Conditional tab presence pattern**: Sprint provider returns `[]domain.Tab` (length 0 or 1) from `OnBranchOpen` based on `docs/ROADMAP.md` existence. `recomputeTabs` already calls `OnBranchOpen` for non-tmux non-Multiple providers (store.go:331), but currently assumes singleton (always emits one tab). Updated `recomputeTabs` to honor empty result for non-tmux singletons (extends the `Multiple()=true` branch behavior to the singleton case for conditional tabs). Aligns with DESIGN_PRINCIPLES "明示的" (data shape matches reality, not symbolic always-present).
- **Markdown parser choice**: Picked **regex/line-based parser** in pure Go stdlib. Considered `yuin/goldmark` for AST walking but the sprint-runner output has a tightly fixed format (`## スプリント <ID>: <title> [<status>]`, `### ストーリー <ID>: ...`, `- [ ] / - [x]` checkboxes, decisions.md sections). Regex is simpler, has zero new deps, and aligns with DESIGN_PRINCIPLES "既存資産活用". Section-level fail-safe (per task S016-1-8) localises any parse error.
- **Mermaid bundling**: Bundled into the FE as a dynamic import (`import('mermaid')` inside the Dependency Graph component). Lazy keeps initial JS payload small while DOM is ready when graph mounts. CDN rejected per VISION 自前ホスティング.
- **WS event design**: Single `sprint.changed` event with payload `{ files: [paths], scope: 'overview'|'detail'|'decisions'|... }`. Frontend invalidates the matched view's cache and refetches. ETag is the cheap optimization on the GET side.
- **Active autopilot scope**: Limited to current branch's `.claude/autopilot-*.lock`. Cross-branch aggregation deferred to backlog (would require broader watcher fan-out + cross-tab session correlation).
- **Read-only**: No launcher/run-sprint actions in S016 (deferred to backlog). Reduces scope to read paths + filewatch only.

## Implementation Decisions

- **`Conditional()` interface extension**: Added `Conditional() bool` to the
  `tab.Provider` interface (default `false` on existing providers) so the
  Store's `recomputeTabs` knows when a non-Multiple non-tmux provider may
  legitimately return zero tabs. Sprint is the first user. Aligned with
  DESIGN_PRINCIPLES "明示的" — visibility is data, not a side-channel
  comment.
- **`Store.RecomputeBranchTabs(repoID, branchID)` public API**: New
  public entry that takes the lock, runs `recomputeTabs`, diffs the
  resulting `TabSet.Tabs` against the previous list, and publishes
  `tab.added` / `tab.removed` for the differences. Provider's
  worktreewatch callback uses it on every debounced batch so ROADMAP.md
  appearance / disappearance propagates without bespoke per-provider
  diffing logic.
- **fsnotify race fix in `sprintFilter`**: Initially the filter accepted
  only `docs/ROADMAP.md`. When the test harness writes `docs/ROADMAP.md`
  starting from a worktree without `docs/`, the parent dir create races
  the recursive watcher subscribe — by the time `docs/` is added,
  ROADMAP.md may already exist and fsnotify silently misses it. Fix:
  also accept the bare `docs` path so the parent-dir create propagates,
  and recompute fires anyway. Verified by the (a) and (b) E2E flows.
- **Mermaid bundling**: Installed via `npm install mermaid`. Lazy import
  inside `dependency-graph.tsx` (`import('mermaid').then(...)`) — Vite
  splits it into its own chunks so the Files / Git / Claude tabs do not
  pay the cost. Rejected: server-side SVG render — would require
  shipping a JS engine inside the Go binary.
- **WS event payload `{ files, scopes }`**: payload carries both the
  affected paths AND a derived list of dashboard scopes. The FE hook
  matches on scope (cheap, no path parsing duplicated client-side).
  Empty `scopes` is treated as "everything" (defensive fall-through).
- **ETag implementation**: SHA-256 over modtime+size of the source
  file(s), truncated to 16 hex chars. Re-derived on every request — no
  in-memory cache. The cost is one Stat per file per GET, which is
  cheap; the upside is correctness when files mutate concurrently.
  Composite endpoints (overview = ROADMAP.md + .claude/) compose the
  per-component tags through another SHA-256.
- **Active autopilot lock format**: Best-effort JSON parse of `{ pid,
  startedAt }` falling back to file modtime + `0` PID. Spec for the lock
  body is owned by the autopilot skill; we accept the documented shape
  and stay forward-compatible.
- **Decision parser regex tightening**: `- **Title**: body` was the
  intended pattern but real decisions.md docs use both `:` and `：`
  (full-width colon). Regex updated to accept either.
- **Sprint tab order**: TabBar Provider registration order is
  Claude / Files / Git / Sprint / Bash[]. This matches the spec
  ("work → browse → commit → status → terminal") and slots Sprint
  before the multi-instance Bash group.
- **No new Go module dependencies**: chose regex/line-walker over
  `yuin/goldmark` (saves ~200KB binary). The format we parse is fixed
  by sprint-runner so a regex is sufficient.

## Review Decisions

- **Lint compliance**: Moved `statusClass` from `view-header.tsx` to a
  separate `view-helpers.ts` to satisfy the `react-refresh/only-export-
  components` rule. `setState`-in-effect calls in `use-sprint-data`
  carry per-line eslint disables (legitimate "fetch on mount /
  reconnect" pattern documented in the React docs).
- **Lint clean across `src/tabs/sprint`**, type-check clean, FE prod
  build succeeds (Mermaid auto-split into chunks ranging 35–475 KB,
  loaded only on Dependency Graph view mount).
- **All 10 acceptance scenarios pass** via the dedicated E2E
  (`tests/e2e/s016_sprint_dashboard.py`) against dev instance on
  port 8202; full output saved to `docs/sprint-logs/S016/e2e-results.md`.

