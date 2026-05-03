# Sprint S012 — Autonomous Decisions

Sprint: **S012 — Git Core (review-and-commit flow)**
Branch: `autopilot/main/S012`
Dev instance port: 8277 (rebuilt from 8276 via `make serve INSTANCE=dev`)

## Planning Decisions

- **Provider-owned watcher** (not store-owned). The shared
  `*worktreewatch.Watcher` is created lazily inside the Git provider
  on the first `OnBranchOpen` so unit tests that don't use the
  provider don't pay the fsnotify cost. S016 will create its own
  Watcher instance for autopilot-lock detection — the API is shared,
  the *instance* is per-feature so subscriptions cannot leak across
  domains. Rationale: VISION emphasises "speed first / no surprise
  side effects" and DESIGN_PRINCIPLES "small, composable
  abstractions". A god-watcher subscribed to every directory by
  default would invert that.

- **`worktreewatch` filter signature**: pure function `func(Event) bool`
  rather than a list of include/exclude globs. Globs felt under-
  specified (which engine? case-sensitivity?), and the Git tab needs
  custom logic anyway (`.git/HEAD` allow-list while ignoring
  `.git/objects`). One callback wins on flexibility *and* test ergonomics.

- **Coalesced batch delivery** (`OnEvent([]Event)`) rather than
  per-event callbacks. The Git tab only needs "something moved";
  S016 needs the path. Delivering the slice satisfies both
  consumers without splitting the API.

## Implementation Decisions

- **Credential failure detection** uses stderr substring matching
  (`could not read username`, `authentication failed`,
  `permission denied (publickey)`, etc.). Originally considered
  reading `git --no-pager` exit codes, but the codes alone don't
  distinguish "remote rejected non-fast-forward" from "no
  credentials". String matching against `GIT_TERMINAL_PROMPT=0`
  output is brittle but simple, and the failure path (FE shows
  the raw stderr in a dialog) is forgiving when the heuristic
  misses.

- **AI commit message** *just* writes the prompt to the Claude
  composer's `localStorage` draft key
  (`palmux:claude-draft:{repoId}/{branchId}`) and dispatches a
  `palmux:composer-prefill` `CustomEvent`. The composer subscribes
  via the same draft-key effect it already uses for tab/branch
  switches. **No new WS frame** needed — chosen because (a) the
  composer is a FE-only state machine, so a server-side frame would
  be redundant, and (b) `localStorage` survives reload, so a Claude
  tab opened *after* clicking the AI button still picks up the
  prompt. Trade-off: the user has to actually open the Claude tab
  for the prefill to take effect — surfaced via the redirect
  `navigate(/{repoId}/{branchId}/{tabId})` so this is invisible.

- **Composer prefill listener wiring deferred**. The current
  composer.tsx already re-loads the draft when `draftKey` changes;
  navigating to the Claude tab triggers the mount which re-reads
  `localStorage`. So the `CustomEvent` is fired but no listener is
  added in this sprint — the draft-key reload covers the visible
  cases (cold open, branch switch, after AI button). If a power user
  has the Claude tab already focused when they click AI, they'll
  need a manual refocus. Logged as backlog: **`composer.prefill
  hot-reload listener`**.

- **Force-push 2-step confirm** uses two separate `confirmDialog.ask`
  calls rather than a custom 2-step dialog. Reuses the existing
  primitive at zero cost. UX-wise the second dialog is a "are you
  REALLY sure" pattern familiar from GitKraken / Sublime Merge.

- **Git Show endpoint** added (`GET /git/show?ref=...&path=...`)
  instead of fetching pre-image lines from the diff parser.
  Rationale: Monaco DiffEditor wants two complete file bodies;
  reconstructing them from hunks would require accumulating
  context lines from `--unified=99999`, which can be huge. `git
  show HEAD:path` is the canonical way and adds one route.

- **Stage-lines algorithm**: filter the *existing* unified diff
  (kept additions inside the line range, dropped additions outside,
  preserved context everywhere) rather than building a fresh patch
  from scratch. Re-uses `BuildHunkPatch`'s output format and the
  existing `git apply --cached --whitespace=nowarn` pipeline.
  Validated by `TestStageLines`.

- **`unidiff-zero=false` regression fix**. The pre-S012 hunk-staging
  path passed `--unidiff-zero=false` which `git apply` rejects as
  "option `unidiff-zero' takes no value". Removed the flag entirely
  — the default is what we wanted anyway.

- **DiffEditor language detection** reuses
  `monacoLanguageFor(path)` from S010 so the same syntax-highlight
  set handles diffs and read-only previews.

- **No `git daemon` in E2E**. Uses a local bare repo + `file://`
  remote. Avoids opening a TCP port in the test runner and works
  on any machine without daemon perms.

## Review Decisions

- **`git apply --whitespace=nowarn` retained** even after removing
  the bad `--unidiff-zero=false` flag — a few hunks our parser
  emits have trailing whitespace inconsistencies which `git apply`
  would otherwise refuse.

- **No remote ref bookkeeping inside `BranchEntry`**. Could surface
  `ahead/behind` counts so the FE shows "ahead 2" badges, but
  shipping the simpler list first matches DESIGN_PRINCIPLES "ship
  the minimum, expand on real demand". Logged as backlog:
  **`branch ahead/behind counters`**.

## Backlog Additions

1. **composer.prefill hot-reload listener** — when a user clicks AI
   commit message *while* a Claude tab is already focused, the
   prefill goes to localStorage but the live composer state doesn't
   refresh. Fixed by listening for `palmux:composer-prefill` in
   composer.tsx.

2. **branch ahead/behind counters** — `git for-each-ref
   --format=%(upstream:track)` returns `[ahead 2, behind 1]`
   substrings; surfacing them on `BranchEntry` plus a small badge
   in `git-branches.tsx` would close the "do I need to push?" gap.

3. **Force-with-lease lease ref** — current implementation uses
   bare `--force-with-lease` which checks the remote's current tip
   against what git has locally. A safer "lease against the
   ref I last fetched" mode (`--force-with-lease=ref:expect`)
   should be exposed when the FE knows the upstream sha.

4. **Conflict resolution UI** — the Conflicts section currently shows
   filenames but no merge-conflict-helper UI. Belongs to S014 per
   the roadmap; flagged here so we don't forget that S012 leaves
   conflicts unhandled.

5. **Magit ranged hunks** — `s` and `u` operate on whole files; a
   future enhancement could let the user step into a hunk with
   arrow keys and stage just that hunk via `s`. Mentioned in
   ROADMAP S013 already.

## E2E Results

`tests/e2e/s012_git_core.py` PASS — full output in
`docs/sprint-logs/S012/e2e.log`. Covers acceptance criteria
(a)–(j) directly or via fixture-repo equivalence:

  - (a) filewatch / `git.statusChanged` republish → status reflects
        new file within ~200ms (well inside the 1s SLA)
  - (b/c) hunk + line-range staging — exercised via `TestStageLines`
        + fixture sanity check
  - (d) commit normal / amend — direct + via REST
  - (e) push / pull / fetch — local bare remote
  - (f) AI commit prompt API — 200 with prompt or 400 if nothing
        staged
  - (g) `--force-with-lease` round-trip
  - (h) branch CRUD
  - (i) UI smoke (Sync bar buttons, Commit form, AI button visible)
  - (j) Magit-style `c` focuses the commit textarea

Go test suite (`go test ./...`) green; FE TypeScript compile
(`npx tsc --noEmit`) clean.
