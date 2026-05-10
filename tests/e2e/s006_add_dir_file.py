#!/usr/bin/env python3
"""Sprint S006 — `--add-dir` / `--file` UI E2E test (Playwright + REST).

S4b9df4-0 baseline fix: this test has been **truncated** to the still-
valid REST API hardening checks. The original UI flow (clicking + opens
an attach menu with Add directory / Add file / Upload image submenus)
was **deleted in S008** ("任意ファイルのアップロード添付 — S006 のサーバ側
ピッカー UI を削除する破壊的変更を含む" — see CLAUDE.md). The composer's
+ button now opens the OS file dialog directly; directory attachment is
handled by drag-and-drop. Drag-and-drop / file-input wire-level checks
live in s008_upload_routes.py.

Verifies, against the running dev palmux2 instance, that:

  1. The page loads and the Composer renders the `+` attach button.
  2. The Files API search endpoint enforces traversal protection:
     `?path=../../etc&query=passwd` → 400 (`ErrInvalidPath`).
     `?query=...` with no `path` is fine; we additionally check that
     paths in results never contain `..`.

Exit code 0 = PASS. Anything else = FAIL.
"""

from __future__ import annotations

import asyncio
import json
import os
import sys
import urllib.error
from typing import Any
from urllib.parse import quote

from playwright.async_api import async_playwright

PORT = os.environ.get("PALMUX_DEV_PORT", "8245")
REPO_ID = os.environ.get("S006_REPO_ID", "tjst-t--palmux2--2d59")
BRANCH_ID = os.environ.get("S006_BRANCH_ID", "autopilot--S006--70ed")
BASE_URL = f"http://localhost:{PORT}"

TIMEOUT_S = 12.0


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def passed(msg: str) -> None:
    print(f"PASS: {msg}")


async def http_get_status(url: str) -> tuple[int, str]:
    """Return (status_code, body) without raising on 4xx/5xx."""
    import urllib.request
    req = urllib.request.Request(url, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            return resp.status, resp.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode() if e.fp else ""


async def main() -> None:
    print(f"==> S006 E2E starting (dev port {PORT}, repo {REPO_ID}, branch {BRANCH_ID})")

    # ── REST traversal hardening — run before browser to fail fast.
    base_files = (
        f"{BASE_URL}/api/repos/{quote(REPO_ID)}/branches/{quote(BRANCH_ID)}/files"
    )
    code, body = await http_get_status(f"{base_files}/search?path=../../etc&query=p")
    if code != 400:
        fail(f"expected 400 for path=../../etc, got {code} body={body[:200]}")
    passed("REST search rejects path=../../etc with 400")

    code, body = await http_get_status(f"{base_files}?path=../../etc")
    if code != 400:
        fail(f"expected 400 for listDir path=../../etc, got {code} body={body[:200]}")
    passed("REST listDir rejects path=../../etc with 400")

    # Sanity: a normal search query inside the worktree returns 200 and no
    # result paths contain `..`.
    code, body = await http_get_status(f"{base_files}/search?query=internal")
    if code != 200:
        fail(f"normal search expected 200, got {code}")
    parsed = json.loads(body)
    results = parsed.get("results") or []
    if not results:
        fail("expected at least one result for query=internal in palmux2 worktree")
    for r in results:
        p = r.get("path", "")
        if ".." in p.split("/"):
            fail(f"result path contains traversal: {p}")
    passed(f"REST search returns {len(results)} results for 'internal', none containing '..'")

    # Browser-side: just confirm the composer + button is rendered (the
    # surface S006 originally introduced). Click-to-attach behaviour
    # itself is now covered by s008_upload_routes.py.
    async with async_playwright() as pw:
        browser = await pw.chromium.launch(headless=True)
        ctx = await browser.new_context()
        page = await ctx.new_page()
        page.on("pageerror", lambda err: print(f"[browser pageerror] {err}"))

        url = f"{BASE_URL}/{quote(REPO_ID)}/{quote(BRANCH_ID)}/claude"
        await page.goto(url, wait_until="domcontentloaded")
        try:
            await page.wait_for_selector("textarea", timeout=int(TIMEOUT_S * 1000))
        except Exception:
            html = await page.content()
            print(html[:2000])
            fail("composer textarea did not appear")
        passed("page loaded; composer textarea present")

        plus = page.get_by_test_id("composer-plus-btn")
        if not await plus.is_visible():
            fail("composer-plus-btn not visible")
        passed("composer + button visible (S008-replaced UI surface)")

        await browser.close()

    print("\n==> S006 E2E ALL CHECKS PASSED")


if __name__ == "__main__":
    asyncio.run(main())
