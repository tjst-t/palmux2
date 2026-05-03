drawio webapp (slim subset)
============================

This directory contains a trimmed copy of the drawio webapp from
https://github.com/jgraph/drawio (`src/main/webapp/`), bundled into palmux2 so
the Files-tab `.drawio` viewer works **without** an external CDN
(VISION: self-hosted, offline-friendly).

Source commit: `5dc0133c688dc28e1990f0b0fb4808732e814f09` (2026-04-20)

License: Apache License 2.0 — full text in `LICENSE.txt`.

What was kept
-------------

- `index.html`, `favicon.ico`
- `js/{bootstrap,main,PreConfig,PostConfig,app.min,extensions.min,shapes-14-6-5.min}.js`
- `js/diagramly/Init.js`
- `js/grapheditor/` (full directory — required by `app.min.js`)
- `mxgraph/` (full directory — graph runtime)
- `styles/` (CSS)
- `resources/dia.txt`, `resources/dia_ja.txt` (English + Japanese i18n only)
- `images/` minus `sidebar-*.png` (sprite atlases for the shape sidebar, which
  the embed/read-only viewer never displays)

What was removed
----------------

- `stencils/`, `templates/`, `connect/`, `WEB-INF/`, `META-INF/`, `plugins/`,
  `math4/`, `img/`, `service-worker*`, `workbox*`, alternative HTML entry points
  (`teams.html`, `dropbox.html`, etc.)
- `resources/dia_*.txt` for languages other than English (`dia.txt`) and
  Japanese (`dia_ja.txt`)
- `images/sidebar-*.png`

These are not needed for **embed mode read-only viewing** of `.drawio` content
passed in via `postMessage({action:'load', xml:…})`. If we later need richer
shape / template support we can copy the missing assets back.

Updating
--------

1. `git clone --depth 1 https://github.com/jgraph/drawio /tmp/drawio-src`
2. Re-run the same `cp` commands documented in `docs/sprint-logs/S010/decisions.json`
   (Sprint S010 implementation log; was `decisions.md` before the S028 JSON
   migration — see `decisions.md.bak` for the original Markdown copy if you
   need the inline rationale text rather than the structured entries).
3. Update the source commit hash above.
