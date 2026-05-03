# S011-fix-1 — Markdown file Edit button regression

**Branch**: `autopilot/main/S011-fix-1`
**Date**: 2026-05-01
**Reporter**: user (screenshot of `.md` file in Files tab missing Edit button)

## Problem

The S011 spec said:
> 既存の MD preview は維持。 編集モードへトグル可能 (Monaco で md として開く)

But the original implementation in `frontend/src/tabs/files/file-preview.tsx`
hard-coded `isEditable()` to `false` for the `markdown` viewer kind:

```ts
function isEditable(kind: ViewerKind): boolean {
  return kind === 'monaco' || kind === 'drawio'  // markdown excluded
}
```

with the comment "Markdown stays read-only in S011; the Backlog has follow-ups
for source-mode editing." This contradicted the spec — the rendered MD preview
should stay (view mode) but a toggle into Monaco markdown source-mode editing
should be available.

## Reproduction (pre-fix)

Run `tests/e2e/s011_fix1_md_edit.py` against the dev instance with the
unmodified frontend:

```
FAIL: (1) Edit button missing on .md file (S011 regression)
```

The Edit button assertion is the regression guard — it fails on the original
S011 code and passes after the fix.

## Fix

Three small changes in `frontend/src/tabs/files/file-preview.tsx`:

1. **Mark markdown editable** — `isEditable()` now returns `true` for
   `markdown` (alongside `monaco` and `drawio`).
2. **Mode-conditional rendering** — split the `viewerKind === 'markdown'`
   branch in two:
   - `mode === 'view'` → `<MarkdownView />` (rendered HTML, unchanged from
     S010).
   - `mode === 'edit'` → `<MonacoView language="markdown" mode="edit" />`
     (raw source editing). Save uses the same `PUT /files/raw` + `If-Match`
     path as Monaco/Drawio (S011-1-1 unchanged).
3. **Refresh local body after save** — `doSave` now calls
   `setBody(prev => ({ ...prev, content, size: data.size }))` so toggling
   back to view mode after a save shows the *post-save* rendered MD (the
   stale `body` was tripping E2E test (5)).

The save handler, conflict dialog, dirty store, Ctrl+S, beforeunload guard,
and discard-confirm flow are all unchanged — they keyed on the same
`{repoId, branchId, path}` editor entry and worked unchanged once `markdown`
was added to the editable set.

## VISION scope-out preserved

The MD edit path uses the same `MonacoView` component as source code editing,
so `quickSuggestions: false`, `parameterHints: { enabled: false }`,
`suggestOnTriggerCharacters: false`, `wordBasedSuggestions: 'off'`,
`hover.enabled: false`, `occurrencesHighlight: 'off'` etc. all stay disabled
in markdown edit mode — no IDE-style helpers leak in.

## Tests

New E2E `tests/e2e/s011_fix1_md_edit.py` — 7 assertions:

1. `.md` open → MarkdownView mounted → Edit button visible (regression guard).
2. Click Edit → MonacoView mounts in edit mode with `data-language="markdown"`,
   raw source visible (`#` heading and `-` bullet markers).
3. Type → dirty indicator appears.
4. `Ctrl+S` → dirty cleared, file mutated on disk.
5. Click Done → MarkdownView shows updated rendered MD (post-save content).
6. Discard path: Done while dirty fires confirm dialog, accept → disk pristine.
7. Mobile (414 px) — Edit button reachable + visible inside viewport, Monaco
   mounts on tap.

Existing E2Es re-verified after the fix:
- `tests/e2e/s010_files_preview.py` → 11/11 PASS (no regression in the
  view-mode dispatcher).
- `tests/e2e/s011_text_edit.py` → 9/9 PASS.
- `tests/e2e/s011_drawio_edit.py` → 5/5 PASS.

## Commit

`autopilot/main/S011-fix-1` branched from `c925dbe` (main, S009-fix-4).
