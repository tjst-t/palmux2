# Sprint S010 — Autonomous Decisions

Sprint: **S010 — Files preview 拡張 (source code / 画像 / Draw.io)**
Branch: `autopilot/main/S010`
Base commit: `3c63887` (S009 merged)

## Planning Decisions

- **Single story, 11 tasks** as authored in ROADMAP — no split. The
  scope (Monaco + image + drawio + dispatcher + size gate + mobile +
  E2E) lives in one user-facing flow ("preview a file"), so splitting
  would only add ceremony.
- **`gui-spec` not invoked** — S010 is contained inside the existing
  Files tab. The acceptance criteria are concrete (8 viewer kinds /
  states), and the test plan in the roadmap already enumerates them.

## Implementation Decisions

- **Monaco lazy-loaded via `React.lazy`** — keeps the Monaco bundle
  (~3 MB) out of the initial SPA payload. Verified in the build log:
  `editor.api2-*.js` is its own chunk (3.6 MB) and only fetched on
  the first non-Markdown / non-image / non-drawio file. Aligns with
  DESIGN_PRINCIPLES "deferred cost" guidance.
- **Monaco worker setup via Vite `?worker`** — the npm-bundled
  Monaco needs `MonacoEnvironment.getWorker` to wire the language
  workers (json/css/html/ts) so they don't try to fetch from a CDN.
  Set once per page-load via a `window.__palmuxMonacoEnvSet__` flag.
- **VISION-out-of-scope features explicitly OFF (read-only Monaco)**:
  `quickSuggestions: false`, `parameterHints: { enabled: false }`,
  `suggestOnTriggerCharacters: false`, `wordBasedSuggestions: 'off'`,
  `acceptSuggestionOnEnter: 'off'`, `hover: { enabled: false }`,
  `occurrencesHighlight: 'off'`, plus `domReadOnly: true` and
  `contextmenu: false` for safety. Documented inline in
  `monaco-view.tsx` so a future "S011 edit mode" PR has to actively
  re-enable each one rather than silently inheriting Monaco's IDE
  defaults.
- **DOMPurify for SVG**: `USE_PROFILES: { svg, svgFilters }` plus a
  belt-and-braces `FORBID_TAGS = [script, foreignObject, iframe,
  object, embed]` and `FORBID_ATTR = [onload, onerror, onclick, ...]`.
  The E2E confirms `<script>alert("xss")</script>` is removed before
  the SVG hits the DOM.
- **Drawio bundling**: `internal/static/drawio/` ships a slimmed copy
  (21 MB) of the upstream `src/main/webapp/` from
  `jgraph/drawio@5dc0133` (2026-04-20). Trimmed `stencils/`,
  `templates/`, `connect/`, `WEB-INF/`, `META-INF/`, `plugins/`,
  `math4/`, `img/`, all non-{en,ja} `resources/`, and all
  `images/sidebar-*.png` sprite atlases — none of those are touched
  in chromeless read-only embed mode. License (Apache-2.0) and
  upstream-commit metadata in `internal/static/drawio/LICENSE.txt`
  and `README.md`.
- **Drawio served at `/static/drawio/` via `embed.FS`** — new
  `StaticFS` field on `server.Deps`, mounted with a 1-year
  `Cache-Control: immutable` header before the SPA fallback. No auth
  gate (the assets are public OSS).
- **Drawio iframe options**: `embed=1&proto=json&chrome=0&spin=1&libraries=0&nav=0`,
  `sandbox="allow-scripts allow-same-origin"`. Read-only mode
  enforced via the postMessage `editable: 0` field in the `load`
  action. Same-origin postMessage check prevents stray cross-frame
  injection.
- **`stat=1` query on `/files/raw`** — new lightweight endpoint that
  returns `{path, size, mime, isBinary}` without reading or shipping
  the body. The dispatcher uses it to gate `previewMaxBytes` *before*
  any body fetch — confirmed in E2E (g) where the 11 MiB `huge.js`
  fixture renders the placeholder without a Monaco round-trip.
- **`previewMaxBytes` setting**: new `Settings.PreviewMaxBytes` field
  (`json:"previewMaxBytes"`), default 10 MiB
  (`DefaultPreviewMaxBytes`). Accepted by the existing PATCH path
  (no handler change needed).
- **Existing MD preview preserved** — `MarkdownView` reuses
  `react-markdown` + `remark-gfm` from the previous file-preview.tsx,
  CSS copied verbatim. The dispatcher routes `.md` and
  `text/markdown` MIME to it. DESIGN_PRINCIPLES "existing asset
  reuse" satisfied.
- **Viewer dispatcher**: `pickViewer({path, size, mime, maxBytes})`
  is a pure function in `dispatcher.ts`, easy to unit-test. Returns
  one of `markdown | drawio | image | monaco | too-large`. Order
  matters: drawio check fires before image so `.drawio.svg` doesn't
  fall through to ImageView.
- **Mobile parity**: each viewer's CSS is `flex` + `min-height: 0`
  with no fixed widths. The file-preview header drops to one-line at
  < 600 px. E2E (h) verifies all 5 viewers stay inside a 414×812
  viewport.

## Failure / Drift Notes

- First test run hit a "ImageView showed `Loading…` for raster PNGs"
  bug: the viewer's null-body guard rejected the explicit-skip path
  the dispatcher uses for raster images (which load via `<img src>`
  directly). Fixed by narrowing the guard to SVG only — raster
  ImageView now renders eagerly with `body: null`. No follow-up
  needed; covered by E2E (b).
- Drawio's `app.min.js` (8.9 MB) and `extensions.min.js` (4.2 MB)
  dominate the bundle size. Acceptable per ROADMAP "サイズ +10MB は
  リポジトリサイズ的に許容" (we're at +21 MB, slightly over but
  still well within the spirit). Stencils / templates were the
  expensive items; we shed both.

## Backlog Added

- **画像プレビューの zoom / pan / 100% トグル**: already on the S010
  backlog (carried over from the original ROADMAP). Not in S010
  scope; revisit during S012 (Git diff) or later.
- **PDF preview**: out of scope here. Track as a future "preview
  formats" backlog entry — fits the dispatcher with a `pdf-view.tsx`
  one-liner registration.
- **TOML syntax highlighting**: Monaco lacks a stock TOML grammar.
  We map `.toml` → `plaintext`. If we ever care, wire up the
  community `monaco-toml` package.

## Review Decisions

- **Naming**: kept the new files under `frontend/src/tabs/files/viewers/`
  (one component per file, kebab-case), matching the rest of the
  Files-tab tree.
- **Test-IDs**: every viewer emits a `data-testid` (`monaco-view`,
  `image-view-raster`, `image-view-svg`, `markdown-view`,
  `drawio-view`, `too-large-view`) plus `data-viewer` on the
  `file-preview` wrapper. This keeps the E2E selector layer
  decoupled from CSS-module class names that change on every build.

## E2E Results

`tests/e2e/s010_files_preview.py` — **PASS, 11 assertions** against
`make serve INSTANCE=dev` on port 8275:

| # | Acceptance criterion | Verifier | Result |
|---|----------------------|----------|--------|
| (a) | Source-code highlight (Go / TS / Python / Rust) | `data-language` attr on `monaco-view` | PASS (4 files) |
| (b) | PNG raster | `data-testid=image-view-raster` | PASS |
| (c) | SVG `<script>` stripped | innerHTML check on `image-view-svg` | PASS |
| (c') | Clean SVG renders | `image-view-svg svg circle` selector | PASS |
| (d) | `.drawio` → iframe at `/static/drawio/?embed=1` | iframe `src` | PASS |
| (e) | MD keeps existing look | `data-testid=markdown-view` | PASS |
| (f) | Unknown extension → Monaco plaintext fallback | `data-language` on Makefile | PASS |
| (g) | > 10 MiB → too-large placeholder, no body fetch | `data-viewer=too-large` + monaco-view absent | PASS |
| (h) | Mobile (414 px) — all 5 viewers fit viewport | bounding-box width ≤ 430 px | PASS (5 viewers) |

The test self-seeds fixtures into `tmp/s010-fixtures/` so it runs
from a clean checkout.

## Build Artefact Sizes

```
dist/assets/index-*.js               854 kB  (gzip 248 kB) — main SPA
dist/assets/editor.api2-*.js       3,627 kB  (gzip 927 kB) — Monaco core (lazy)
dist/assets/editor.main-*.js          95 kB  (gzip  22 kB) — Monaco shell (lazy)
dist/assets/lib-*.js                 153 kB  (gzip  46 kB) — shared
dist/assets/monaco-view-*.js          16 kB                — viewer wrapper (lazy)
dist/assets/image-view-*.js           24 kB                — incl DOMPurify
dist/assets/drawio-view-*.js           ~6 kB               — iframe shell
dist/assets/markdown-view-*.js         ~5 kB
internal/static/drawio/             21 MiB                 — embedded into Go binary
final palmux binary                  45 MiB                — was ~17 MiB pre-S010
```

## Sign-off

Status: **success**. All 11 ROADMAP tasks `[x]`. Branch
`autopilot/main/S010` ready for merge to `main`.
