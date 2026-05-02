#!/usr/bin/env python3
"""Mobile E2E — initial bundle size budget (S022-1-5).

Verifies that the gzipped initial bundle (the ``<script type="module">``
entry plus everything Vite emits as ``<link rel="modulepreload">`` in
``index.html``) stays under the 500 KB target. Files / Git / Sprint /
drawio / Mermaid must NOT be in the initial bundle.

Acceptance:
  (a) The total gzipped size of the entry chunk + all preload chunks is
      < 500 KB.
  (b) ``frontend/dist`` was produced (otherwise the test bails with a
      helpful message).
  (c) ``editor.api2`` (Monaco) and ``mermaid`` chunks are NOT among the
      preload set — they must be lazy.

This test does not require Playwright or the dev server — it is a pure
filesystem check that runs against the most recent ``frontend/dist``.

Exit code 0 = PASS. Anything else = FAIL.
"""

from __future__ import annotations

import gzip
import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
DIST = REPO_ROOT / "frontend" / "dist"
INDEX_HTML = DIST / "index.html"
BUDGET_BYTES = 500 * 1024  # 500 KB gzip


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def gzip_size(p: Path) -> int:
    with p.open("rb") as f:
        return len(gzip.compress(f.read()))


def main() -> None:
    print("\n[M002] initial bundle size budget")
    if not INDEX_HTML.is_file():
        fail(
            f"{INDEX_HTML} missing — run `make build` (or `make serve "
            "INSTANCE=dev`) first to produce the dist."
        )

    html = INDEX_HTML.read_text()
    entries: list[str] = []

    # Match <script type="module" ... src="/assets/X.js">.
    for m in re.finditer(
        r'<script\s+type="module"[^>]*src="(/assets/[^"]+\.js)"', html
    ):
        entries.append(m.group(1))
    # Match <link rel="modulepreload" ... href="/assets/X.js">.
    for m in re.finditer(
        r'<link\s+rel="modulepreload"[^>]*href="(/assets/[^"]+\.js)"', html
    ):
        entries.append(m.group(1))

    if not entries:
        fail("no entry / preload chunks found in dist/index.html")

    seen: set[str] = set()
    total = 0
    print(f"  - inspecting {len(entries)} preload chunks")
    for href in entries:
        if href in seen:
            continue
        seen.add(href)
        p = DIST / href.lstrip("/")
        if not p.is_file():
            fail(f"missing chunk file referenced by index.html: {p}")
        size = gzip_size(p)
        total += size
        print(f"     {href:50s}  {size/1024:7.2f} KB gz")
        # Hard guard: large async-only chunks must not show up in the
        # preload set.
        for forbidden in ("editor.api2", "mermaid", "ts.worker", "drawio"):
            if forbidden in href:
                fail(
                    f"{href!r} should be lazy-loaded but appears in the "
                    f"initial preload set"
                )

    print(f"\n  total initial gzip = {total/1024:.2f} KB " f"(budget {BUDGET_BYTES/1024:.0f} KB)")
    if total > BUDGET_BYTES:
        fail(f"initial bundle {total/1024:.2f} KB exceeds budget {BUDGET_BYTES/1024:.0f} KB")
    print("M002 PASS")


if __name__ == "__main__":
    sys.exit(main() or 0)
