# Sprint S008 — Autonomous Decisions

> **Sprint**: 任意ファイルのアップロード添付 (Upload Image 拡張)
> **Branch**: `autopilot/main/S008`
> **Mode**: `sprint auto` (autopilot, sub-skill of milestone M1)
> **Run date**: 2026-05-01

## Goal recap

Generalise the existing image-only upload path to **any local file** while
collapsing the three S006 server-side picker actions ("Add directory" /
"Add file" / "Upload image…") into a single "Attach file" action wired
to three input routes — file picker, drag-and-drop, paste. Per-branch
isolation on disk + always-on `--add-dir` of the per-branch dir removes
the per-attachment respawn that S006 needed.

## Planning Decisions

- **Per-branch endpoint vs. global**: Added a new
  `POST /api/repos/{repoId}/branches/{branchId}/upload` route and kept
  the legacy `POST /api/upload` so the bash terminal-view paste flow
  (which doesn't always carry repo/branch context — orphan tabs) still
  works. The Composer always uses the per-branch endpoint; legacy
  `[image: <abspath>]` references in old transcripts still resolve via
  the legacy GET `/api/upload/{name}` because the locator now walks the
  two-level per-branch tree under the configured root.
  Rationale: VISION single-user / mobile-parity lets us avoid
  multi-user scoping; DESIGN_PRINCIPLES "既存資産活用 > 新規実装"
  argued for keeping the legacy GET path resolving so old conversations
  don't break.

- **Attachment dir auto-add via `--add-dir`**: Instead of
  respawning the CLI when each new file lands (the S006 dir-attachment
  pattern), the per-branch attachment directory is included on **every
  CLI spawn** as `--add-dir <root>/<repoId>/<branchId>`. The directory
  is created (`MkdirAll`) before the CLI launches so the flag works
  even on the very first message. Files dropped after the spawn are
  reachable instantly because `--add-dir` allows the whole directory.
  Rationale: DESIGN_PRINCIPLES "lazy spawn > eager spawn" + "明示的 >
  暗黙的" — one stable, predictable argv per branch beats per-message
  argv churn. Avoids the `mergeAddDirs` respawn dance entirely for
  uploads.

- **Removed user-supplied `addDirs` from WS frame**: Per S008 spec, the
  S006 server-side dir picker UI is gone. The frame field is preserved
  on the type for back-compat but the handler now just logs and
  ignores it. `Agent.AddDirs`, `validateAddDirs`,
  `SendUserMessageWithDirs` stay in the codebase per the spec — they
  may be reused later, and the test suite covers them.

- **TTL cleanup at startup, not via cron**: The cleanup pass runs once
  at server startup (`internal/attachment.CleanupOlderThan`). A
  long-running daily ticker is overkill for the use case (single-user,
  most users restart palmux2 a few times a week minimum). Branch
  close also wipes the per-branch dir (`Manager.KillBranch` →
  `os.RemoveAll`). Default TTL = 30 days, configurable via
  `attachmentTtlDays`.

- **MIME signal**: Server resolves MIME via `header.Get("Content-Type")`
  → `mime.TypeByExtension` → `application/octet-stream` fallback. The
  response carries a coarse `kind ∈ {"image","file"}` so the frontend
  doesn't have to re-classify. Frontend still does a
  filename-extension fallback for the upload-time chip kind because
  some platforms don't set `file.type` for clipboard-pasted blobs.

- **Multi-file in one route**: File input gets `multiple` attribute;
  drop and paste both iterate the entire `FileList`. No special handling
  for >1 files — each becomes its own chip and uploads in parallel.

- **`onDrop` on composer-root**: The drop target is the `composerInner`
  wrapper, not the textarea, so dropping anywhere over the input area
  attaches. A blue dashed overlay appears while a file is being
  dragged over the area. `dragenter`/`dragleave` are counted to avoid
  flicker as the cursor crosses inner element boundaries.

- **Backward-compatible settings load**: Legacy `imageUploadDir` JSON
  key is read on load and migrated into `attachmentUploadDir`; the
  Patch endpoint also accepts either key. Subsequent saves write only
  the new key (the legacy field is cleared after read).

## Implementation Decisions

- **`sanitizeBaseName`**: Filenames are sanitised on the server to ASCII
  alnum + `-_.` (anything else collapses to `_`), prefixed with the
  user's chosen base name, and suffixed with `<UTC-timestamp>-<rand4>`.
  This preserves the user-recognisable name in tool output while
  guaranteeing uniqueness when two files with the same name land in the
  same second. Length cap 60 chars before suffix.

- **64 MiB upload cap**: Bumped from 16 MiB so log files / PDFs /
  middleweight CSVs fit. Still bounded so a runaway upload can't fill
  `/tmp`. Configurable via the `uploadMaxBytes` constant for now;
  future-work backlog item: surface as a `attachmentMaxMB` setting.

- **`os.MkdirAll` in `EnsureClient`**: The per-branch attachment dir is
  created before each CLI spawn even though the upload handler also
  calls MkdirAll. Belt-and-braces: a user might create a branch and
  send a message before any upload, in which case `--add-dir` would
  point at a non-existent path. CLI tolerates that today but Read tools
  inside the directory wouldn't see new files until the dir actually
  exists.

- **Removed `+`-button context menu**: Replaced the dropdown with a
  direct click → `<input type=file multiple>`. The single-action UX is
  also more obvious to first-time users. Drag-and-drop and paste cover
  every other affordance.

- **Composer status field on attachment chips**: Added
  `status: 'uploading' | 'ready' | 'error'` to the Attachment type.
  Submission excludes any chip not in `ready`; the Send button is
  disabled while any chip is uploading. Error chips display a `!` glyph
  and a hover tooltip with the server message.

- **Legacy GET `/api/upload/{name}` walks per-branch dirs**: The image
  embed in transcripts (`uploadURLForPath` in `blocks.tsx`) only
  carries the basename, not the full per-branch path. To keep old
  transcripts viewable after S008's directory layout change, the
  fetcher walks two levels under root looking for the basename. We cap
  at two levels (repo/branch) so a malicious symlink can't trick us
  into a deep filesystem walk.

## Review Decisions

- **No new unit tests for the upload handler itself**: The upload
  handler is exercised end-to-end by the Playwright run; adding a
  dedicated Go test would duplicate coverage. The decision in S006
  (test the wire-level frame contract via E2E, test pure functions via
  unit tests) is followed here. The new pure functions —
  `attachment.CleanupOlderThan`, `attachment.RemoveBranchDir` — got
  Go unit tests.

- **Kept `Agent.AddDirs()` accessor**: Tests in
  `internal/tab/claudeagent/add_dirs_test.go` still use it. Removing
  the accessor means rewriting those tests; the field is still useful
  for debugging.

## E2E Verification

**Script**: `tests/e2e/s008_upload_routes.py` (port 8246 dev instance).

**Result (2026-05-01)**:

```
==> S008 E2E starting (dev port 8246, repo tjst-t--palmux2--2d59, branch autopilot--main--S008--6d2f)
PASS: page loaded; composer textarea present
PASS: composer + button (Attach file) visible
PASS: S006 server-side picker UI is fully removed
PASS: Route A (file picker, image): chip ready at /tmp/palmux-uploads/<repo>/<branch>/s008-pixel-...png
PASS: Route A (file picker, text): chip ready at /tmp/palmux-uploads/<repo>/<branch>/s008-note-...txt
PASS: Route A submission: user.message carries [image: ...] + @<abspath>, no addDirs
PASS: Route B (drag-and-drop): image chip ready
PASS: Route C (paste): file chip ready
PASS: upload response envelope contains path/name/originalName/mime/kind/size
PASS: running claude process carries --add-dir /tmp/palmux-uploads/<repo>/<branch> (S008-1-3 wired)
==> S008 E2E ALL CHECKS PASSED
```

The `ps`-based argv check observed a real `claude` subprocess with the
expected `--add-dir` (the dev box has CLI auth set up):

```
claude --input-format stream-json --output-format stream-json
  --include-partial-messages --verbose --setting-sources project,user
  --permission-prompt-tool mcp__palmux__permission_prompt
  --permission-mode auto --effort xhigh
  --add-dir /tmp/palmux-uploads/tjst-t--palmux2--2d59/autopilot--main--S008--6d2f
```

## Backlog additions (autonomous)

- **Surface `attachmentMaxMB` and `attachmentTtlDays` in the Settings
  UI**: Both are wired through the schema but have no UI control yet.
  Today the user has to edit `settings.json` by hand to change them.

- **Periodic cleanup ticker**: The current cleanup runs once at startup.
  For long-running palmux2 instances (months between restarts) the dir
  could accumulate >30-day-old files in the steady state. A 24-hour
  ticker would keep things tidy.

- **Show upload progress for large files**: The chip currently shows a
  binary "uploading…" spinner. A progress bar (XHR/fetch progress
  events) would help when uploading multi-MB CSVs / videos.

- **Drag-and-drop on composer-only sub-area is a leak vector**: If the
  user drags a file onto the conversation pane (not the composer), it
  opens in a new tab. Could swallow that and route to upload too.

## Drift warnings vs VISION / DESIGN_PRINCIPLES

None observed. The design path stayed inside the VISION envelope:
single-user, self-hosted, files stored on the palmux2 host (no
external file API call). DESIGN_PRINCIPLES rules respected:
- 1: CLI が真実 — we don't store metadata Claude can't see; the path
  goes verbatim into the user message.
- 2: タブ間の対称性 — bash terminal paste still works (legacy global
  endpoint).
- 7: 明示的 > 暗黙的 — `--add-dir` is on argv at spawn, not implicit.
- 9: 下書き保持 — composer text draft via localStorage was preserved
  (existing); attachments reset on remount because blob URLs don't
  round-trip safely.

## Files touched

- `internal/config/settings.go` — `imageUploadDir` → `attachmentUploadDir`
  + `attachmentTtlDays` + back-compat migrate.
- `internal/config/repos_test.go` — adjusted assertions.
- `internal/server/handler_upload.go` — full rewrite. Per-branch +
  legacy endpoint, expanded response, sanitised filenames.
- `internal/server/server.go` — new routes registered.
- `internal/tab/claudeagent/manager.go` — `Config.AttachmentDirFn`,
  EnsureClient adds attachment dir, KillBranch removes it.
- `internal/tab/claudeagent/handler.go` — ignores stale user-supplied
  AddDirs[].
- `internal/attachment/cleanup.go` (+ test) — TTL sweep + per-branch
  remove.
- `cmd/palmux/main.go` — startup cleanup + AttachmentDirFn wiring.
- `frontend/src/tabs/claude-agent/composer.tsx` — Attach file button,
  drag-and-drop overlay, status field, three-route upload pipeline,
  send-time routing.
- `frontend/src/tabs/claude-agent/claude-agent-view.module.css` —
  drop overlay + error chip styles, removed PathPicker/AttachMenu CSS.
- `frontend/src/tabs/terminal-view.tsx` — terminal-view paste uses the
  per-branch endpoint when a (repo, branch) is in scope.
- `frontend/src/stores/palmux-store.ts` — extended GlobalSettings
  type.
- `frontend/src/tabs/claude-agent/blocks.tsx` — comment refresh.
- `tests/e2e/s008_upload_routes.py` — three-route Playwright test.
- `docs/ROADMAP.md` — story / tasks marked done.
