# Sprint S011 — Autonomous Decisions

Sprint: **S011 — Files テキスト + Draw.io 編集**
Branch: `autopilot/main/S011`
Base: `main` @ `8b657cb` (S010 merged)
Dev instance: `INSTANCE=dev` on port **8276**

## Planning Decisions

- **No story split.** S011's two stories (text edit, drawio edit) already
  cleanly target a single user-facing behavior each. Split would only add
  ceremony.
- **Edit existing files only.** S011 does not introduce file *creation*
  from the Files tab. Backlog item if requested later. WriteFile refuses
  `os.ErrNotExist` so an accidental new-file PUT fails cleanly.
- **No diff-based 3-way conflict UI.** ROADMAP only requires
  reload / overwrite / cancel. Full diff merge is heavier and lands
  better as a Backlog item once Git Core (S012) introduces the diff
  viewer infra.

## Implementation Decisions

### Backend

- **ETag formula** = base64(sha256(`<unix_nano>.<size>`)[:8]). Short
  (12 chars), fits in `If-Match` headers without escaping, deterministic
  across stat calls. Hashing the full content was rejected — DESIGN
  PRINCIPLES "速い" trumps "perfectly precise" for a single-user editor.
  Risk: if size+mtime collide on two clients writing at the same nano
  the ETag is identical; tolerable for single-user/optimistic-locking.
- **Atomic write** via temp-file-then-rename in the same dir. `O_TMPFILE`
  on Linux would be slightly cleaner but rename(2) already buys us
  crash-safety and is portable to anywhere Go runs.
- **PUT contract**: `If-Match` is **required** — missing → 428
  Precondition Required (per ROADMAP). Mismatch → 412 + `currentEtag`
  in body so the client conflict dialog can drive Reload / Overwrite.
- **Body size cap** = 32 MiB. Hardcoded in `handler.writeFile`; no
  setting yet because the Files tab is human-typed input. Backlog if a
  real use case appears.
- **JSON envelope** rather than raw bytes for the PUT body. Keeps the
  API uniform with the rest of `/api`, and leaves room to add fields
  (`encoding: 'base64'` for binaries) without a breaking change.
- **Same URL** for GET and PUT (`/files/raw?path=…`). Symmetric REST
  surface, easier to reason about than `/files/raw` vs `/files/save`.

### Frontend

- **`useEditorStore` (Zustand)** rather than React component state.
  DESIGN PRINCIPLES "下書きは積極保持" — drafts must survive tab
  switches inside the same branch. Keyed by `{repoId}/{branchId}/{path}`.
  Forgets the entry on unmount only when the buffer is clean.
- **`defaultValue` (uncontrolled) + `key=editorKey::mode`** for Monaco
  rather than fully controlled `value=`. Streaming `value` on every
  keystroke fights Monaco's internal model and causes cursor jumps.
  We lift state via `onChange` only.
- **Conflict dialog as a separate component** (`conflict-dialog.tsx`)
  reusable by both Monaco and Drawio edit paths. ROADMAP S011-2-5
  explicitly asks for reuse.
- **Edit-mode VISION-scope-out disables stay enabled in edit mode.**
  `quickSuggestions: false`, `parameterHints.enabled: false`,
  `suggestOnTriggerCharacters: false`, `wordBasedSuggestions: 'off'`,
  `acceptSuggestionOnEnter: 'off'`, `hover.enabled: false`,
  `occurrencesHighlight: 'off'`. The only difference between READ_ONLY
  and EDIT options is `readOnly` / `domReadOnly` / `contextmenu`.
  Verified by E2E (e) — typing a long identifier prefix does NOT light
  up the suggest widget.
- **drawio mobile gating: `< 900px` disables the Edit button** with a
  tooltip telling the user to open on a wider screen. ROADMAP S011-2-6
  framed this as "視野" — we picked Disable as the safer default since
  drawio's touch palette is genuinely unusable on phone-width screens.
  The text editor stays available on mobile because Monaco's touch
  selection works fine.
- **`beforeunload` guard** runs from FilePreview's effect; reads
  `hasAnyDirty(state)` so any unsaved file across the app gates the
  unload. Browsers ignore custom messages but still show the native
  prompt when `e.preventDefault()` runs.
- **`window.confirm` for in-app dirty switches** — DESIGN PRINCIPLES
  "明示的 > 暗黙的". Custom dialog could ship later; the native confirm
  is plenty until we have a richer modal stack.

### Drawio

- **Two iframe URLs** rather than reloading via postMessage. Re-mounting
  the iframe with `key=mode` is the simplest way to force drawio into
  the right chrome state, and saves us from chasing drawio's
  init-protocol changes when toggling.
- **`autosave: 1` in edit mode** so drawio emits `event: 'autosave'`
  on every model change. We treat that XML as the latest draft and
  light up the dirty badge.
- **Ctrl+S forwarding via host-window keydown** → posts
  `{action: 'save'}` into drawio. drawio's own save catches the keypress
  when the iframe has focus; this is the safety net for when the user's
  focus is elsewhere on the page.

## Test Strategy

- **Go unit tests** (`internal/tab/files/write_test.go`): ETag stability,
  atomic write + perm preservation, missing-file rejection, traversal
  rejection.
- **Playwright E2E `tests/e2e/s011_text_edit.py`** covers all 7
  acceptance criteria for S011-1 plus the API-level 428/412/200 flow.
- **Playwright E2E `tests/e2e/s011_drawio_edit.py`** covers the
  view→edit URL flip, mobile disable, plus API-level save and conflict
  paths. Drawio's full editor UI is intentionally NOT driven from the
  test — it's flaky in headless Chromium and the postMessage save flow
  funnels into the same PUT endpoint we already exercise.

## Backlog Additions

(none — S011 was tightly scoped, all in-scope work landed)

Suggested follow-ups for later sprints:
- 3-way diff merge inside the conflict dialog (S012 brings diff infra).
- File creation from the Files tab.
- localStorage persistence for the dirty-buffer cache (currently
  in-memory only; survives navigations within the SPA but not page
  reload). Would unlock "tab restored" on hard refresh.
- AI-assisted conflict resolution when Claude tab is open.

## E2E Results

- `go test ./internal/tab/files/...` — PASS (4 tests, 0 failures)
- `tests/e2e/s011_text_edit.py` — **PASS (9 assertions)**
  - api: PUT 428 / 412 / 200 + ETag round-trip
  - (a) Edit → type → Save → file updated
  - (b) Ctrl+S triggers Save
  - (c) confirm dialog on file-switch with dirty buffer
  - (d) 412 → conflict dialog → Reload
  - (d2) 412 → conflict dialog → Overwrite
  - (e) no autocomplete suggestions in edit mode (VISION scope-out)
  - (f) Find/Replace widget opens via Ctrl+F
  - (g) mobile (414 px) Save flow OK
- `tests/e2e/s011_drawio_edit.py` — **PASS (5 assertions)**
  - (api) drawio PUT save round-trip
  - (api) drawio conflict 412
  - (a) drawio default view mode (chrome=0)
  - (b) Edit toggles iframe to chrome=1
  - (c) mobile (< 900 px) drawio Edit button disabled with tooltip
- `tests/e2e/s010_files_preview.py` — **PASS (11 assertions)** — no
  regression from S011 changes.

## Drift Warnings

- Monaco edit mode: VISION-scope-out features stay disabled. **E2E (e)
  asserts the suggest widget never appears** even after typing a long
  identifier prefix in edit mode. If this test ever flakes, audit the
  `EDIT_OPTIONS` block in `monaco-view.tsx` — every IDE-flavored knob
  must stay off.
- Drawio in edit mode runs the full drawio chrome (`chrome=1`). drawio
  itself ships some IDE-flavored UI (XML edit panel, etc.). VISION
  scope-out applies to *Monaco*; for drawio we accept the upstream UI
  surface as-is — it's an embedded webapp, not our editor.
