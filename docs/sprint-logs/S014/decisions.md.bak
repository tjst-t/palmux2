# Sprint S014 — Autonomous Decisions

Sprint goal: **Conflict & Interactive Rebase**. Complete the "難所操作" tier
of git work that S012 (daily) and S013 (weekly) left unaddressed:

- 3-way merge conflict resolution (Tower / Sublime Merge style 3 panes).
- Interactive rebase TODO list with drag-to-reorder + per-line action.
- Submodule init / update / status panel.
- Reflog viewer with "Reset to here" rescue from orphan commits.
- Bisect helper (start / good / bad / skip / reset).

## Planning Decisions

- **Story granularity**: kept the single S014-1 story. All six surfaces
  (conflicts, rebase-todo, submodules, reflog, bisect, plus the merge/
  rebase progress banner) share one cohesive user goal — "complete the
  hard parts of git history work" — and the 16 tasks already carry the
  finer-grained structure.
- **Run on the same Git tab surface as S012/S013**. No new top-level
  tab. We add new sub-views inside the existing `git-view.tsx` switch
  alongside `status / log / branches / stash / tags / file-history /
  blame`.
- **Diff source for 3-way merge**: use `git show :1:path / :2:path /
  :3:path` for base / ours / theirs blobs and the working-tree file for
  the merged result. The working-tree copy is what `git add` ultimately
  records, so editing it directly (rather than building a synthetic
  3-way patch) keeps the contract simple and matches `git mergetool`
  behaviour.
- **Bisect implementation**: shell out to `git bisect` directly. There
  is no porcelain helper for "current step state" so we parse
  `.git/BISECT_LOG` + `git bisect log` to surface progress; fall back
  to the absence of `.git/BISECT_LOG` to mean "not bisecting".

## Implementation Decisions

- **Conflict markers parser**: implemented a small streaming parser
  (no library) that recognises `<<<<<<<`, `|||||||` (diff3 mode),
  `=======`, `>>>>>>>`. Returns one `ConflictHunk` per group with
  ours / base / theirs slices so the FE can render hunks
  side-by-side without re-running git commands per hunk. This avoids
  N+1 git calls for files with many conflicts.

- **Rebase-todo write-back contract**: the `PUT /git/rebase-todo`
  endpoint atomically (a) writes the new TODO file, (b) runs
  `git rebase --continue` to apply it. We chose this over a separate
  "save then continue" flow because the rebase TODO file only exists
  while a rebase is paused; saving without continuing leaves the user
  in an ambiguous state. The handler returns the conflict info if
  `--continue` immediately stops on a conflict, so the FE can chain
  straight into the conflict UI.

- **Interactive rebase via `GIT_SEQUENCE_EDITOR`**: when a user clicks
  "Rebase from here" we shell out to
  `GIT_SEQUENCE_EDITOR=true git rebase -i <onto>`. `true` is a no-op
  editor that accepts the default todo (which is `pick A; pick B; ...`),
  so git pauses *before* doing anything because palmux2 wants to
  intercept the TODO. Then we set the rebase as paused (via the
  presence of `.git/rebase-merge/git-rebase-todo`) and steer the user
  to the FE editor.

  **Update**: simpler — we run
  `GIT_SEQUENCE_EDITOR=":" git rebase -i <onto>` to enter rebase mode
  with all "pick" entries auto-applied (no pause), but for *editing*
  the todo we use `--keep-base` semantics: the FE sends back the new
  todo and we run `git rebase --edit-todo` via stdin. We finally
  settled on letting the FE preview-build the todo from the log range
  client-side, send the edited todo via `POST /git/rebase`, and the
  backend writes the todo and runs `git rebase` with
  `GIT_SEQUENCE_EDITOR=cat` so git uses the file we set.

- **Submodule actions** are intentionally limited to init / update /
  status. `add` / `deinit` are out of scope (they cross repository
  boundaries and need confirmation flows). Filed in backlog if
  demanded.

- **Reflog format**: parse `git reflog --pretty=format:%H %gd %gs` and
  render newest-first with action chips (commit / reset / checkout /
  rebase). "Reset to here" issues `POST /git/reset` with
  `mode: "hard"` and the entry's SHA — reusing the S013 endpoint.

- **Bisect status surfacing**: we expose
  `GET /git/bisect/status` returning `{active, good, bad, currentSha,
  remainingApprox}`. `remainingApprox` comes from
  `git bisect log | grep ^# good\|^# bad` parsing — git itself
  estimates remaining commits via `git bisect visualize` but the
  output is heavy.

## Review Decisions

- **3-way UI on mobile**: the literal 3-pane layout doesn't fit on a
  narrow viewport. The FE collapses to a tab strip (Ours / Theirs /
  Result) on `<900px` (matching the S012 mobile breakpoint). All
  accept buttons remain operable.

- **Drag-and-drop on touch**: used native HTML5 drag for desktop and
  a long-press → drag fallback on touch. Matches the
  DESIGN_PRINCIPLES "mobile parity" rule without a heavyweight
  library.

## Backlog Additions

- Submodule add / deinit (S014 backlog).
- AI-assisted conflict resolution (already in backlog, reaffirmed).
- "Squash similar commits" auto-suggest in interactive rebase
  (S014/S017 candidate).
- Bisect with auto-test script (`git bisect run <cmd>`).
