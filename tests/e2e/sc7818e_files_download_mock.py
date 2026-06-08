#!/usr/bin/env python3
"""Sprint Sc7818e — Files タブ ダウンロード UI の mock / edge-case test.

real backend を使わず page.route で /files/download をインターセプトし、GUI 側の
分岐ロジック・URL 構築・エラー時の非破壊性を hermetic に検証する。sprint run の
per-Story fast feedback 用 (e2e は sprint verify で real backend に対して走る)。

Covers (GUI edge cases, Story Sc7818e-3):
  [MOCK] 選択 0-1 では batch-download が出ず single download が出る
  [MOCK] N>=2 で batch-download が出て single download は出ない、文言が N を反映
  [MOCK] Download クリックの request URL が単一=path1個 / batch=path反復 で正しい
  [MOCK] download が 404/500 を返しても UI がクラッシュ/白画面にならずメニューが閉じる

このプロジェクトの list/dir API も page.route で固定 fixture を返してレンダリングを
安定させる。download エンドポイントは attachment を fulfill して expect_download を成立
させる (download 属性付き anchor のネイティブ DL を観測)。

Exit 0 = PASS, nonzero = FAIL.
"""
from __future__ import annotations

import asyncio
import json
import sys
import urllib.parse
from pathlib import Path

from playwright.async_api import Page, Route, async_playwright

sys.path.insert(0, str(Path(__file__).parent))
from _fixture import BASE_URL

TIMEOUT = 10_000  # ms

# Stable synthetic IDs — no real repo is opened in mock mode.
REPO_ID = "mock-owner--mock-repo--abcd"
BRANCH_ID = "mock-repo--1234"

# Fixture directory listing the Files tab will render.
DIR_LISTING = {
    "path": "",
    "entries": [
        {"name": "alpha.txt", "path": "alpha.txt", "isDir": False, "size": 5, "modTime": "2026-06-08T00:00:00Z"},
        {"name": "beta.txt", "path": "beta.txt", "isDir": False, "size": 5, "modTime": "2026-06-08T00:00:00Z"},
        {"name": "gamma.txt", "path": "gamma.txt", "isDir": False, "size": 5, "modTime": "2026-06-08T00:00:00Z"},
        {"name": "docs", "path": "docs", "isDir": True, "size": 0, "modTime": "2026-06-08T00:00:00Z"},
    ],
}

_PASS = 0


def ok(msg: str) -> None:
    global _PASS
    _PASS += 1
    print(f"  ok: {msg}")


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def files_prefix() -> str:
    return f"/api/repos/{REPO_ID}/branches/{BRANCH_ID}/files"


async def _route_listing(route: Route) -> None:
    """Fulfill any /files?path=... listing GET with the fixture directory."""
    await route.fulfill(
        status=200,
        content_type="application/json",
        body=json.dumps(DIR_LISTING),
    )


async def install_base_routes(page: Page, *, download_status: int = 200) -> dict:
    """Route the dir listing and the download endpoint. Returns a mutable
    record of the last download request seen (so tests can assert the URL)."""
    seen: dict = {"download_url": None, "download_hits": 0}

    async def on_download(route: Route) -> None:
        seen["download_url"] = route.request.url
        seen["download_hits"] += 1
        if download_status == 200:
            await route.fulfill(
                status=200,
                headers={"Content-Disposition": 'attachment; filename="mock.bin"'},
                content_type="application/octet-stream",
                body=b"MOCKDATA",
            )
        else:
            await route.fulfill(status=download_status, content_type="application/json",
                                body=json.dumps({"error": "mock error"}))

    # Order matters: more specific (download) before the generic listing.
    await page.route(f"**{files_prefix()}/download?**", on_download)
    await page.route(f"**{files_prefix()}?path=**", _route_listing)
    await page.route(f"**{files_prefix()}", _route_listing)
    return seen


async def nav(page: Page) -> None:
    await page.goto(f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/files")
    await page.wait_for_selector('[data-testid="files-list"]', timeout=TIMEOUT)


async def row(page: Page, name: str):
    return page.locator(f'[data-testid="files-list"] button:has-text("{name}")').first


# ── Tests ───────────────────────────────────────────────────────────────


async def test_single_menu_has_download_no_batch(page: Page) -> None:
    """[MOCK] selection 0-1: single 'Download' present, batch absent."""
    await install_base_routes(page)
    await nav(page)
    await (await row(page, "alpha.txt")).click(button="right")
    await page.locator('[data-testid="files-context-menu"]').wait_for(timeout=TIMEOUT)
    if not await page.locator('[data-testid="files-ctx-download"]').is_visible():
        fail("[MOCK] single 'Download' item not visible")
    if await page.locator('[data-testid="files-ctx-batch-download"]').count() != 0:
        fail("[MOCK] batch-download leaked into single-selection menu")
    ok("single menu shows Download, no batch-download")
    await page.keyboard.press("Escape")


async def test_batch_menu_count_label(page: Page) -> None:
    """[MOCK] N>=2: batch 'Download N items…' present, label reflects N, single absent."""
    await install_base_routes(page)
    await nav(page)
    mod = "Meta" if sys.platform == "darwin" else "Control"
    await (await row(page, "alpha.txt")).click(modifiers=[mod])
    await (await row(page, "beta.txt")).click(modifiers=[mod])
    await (await row(page, "alpha.txt")).click(button="right", modifiers=[mod])
    await page.locator('[data-testid="files-context-menu"]').wait_for(timeout=TIMEOUT)
    batch = page.locator('[data-testid="files-ctx-batch-download"]')
    if not await batch.is_visible():
        fail("[MOCK] batch 'Download N items…' not visible at N=2")
    label = (await batch.inner_text()).strip()
    if "2" not in label:
        fail(f"[MOCK] batch label missing count: {label!r}")
    if await page.locator('[data-testid="files-ctx-download"]').count() != 0:
        fail("[MOCK] single download leaked into batch menu")
    ok(f"batch menu shows count-aware label ({label!r}), no single download")
    await page.keyboard.press("Escape")


async def test_single_download_url(page: Page) -> None:
    """[MOCK] single Download issues request to {prefix}/download?path=<file> (1 path)."""
    seen = await install_base_routes(page)
    await nav(page)
    await (await row(page, "beta.txt")).click(button="right")
    await page.locator('[data-testid="files-context-menu"]').wait_for(timeout=TIMEOUT)
    async with page.expect_download():
        await page.locator('[data-testid="files-ctx-download"]').click()
    url = seen["download_url"] or ""
    dec = urllib.parse.unquote(url)
    if "/files/download?" not in url or dec.count("path=") != 1 or "beta.txt" not in dec:
        fail(f"[MOCK] single download url wrong: {url}")
    ok("single download request url correct (1 path=)")


async def test_batch_download_url_repeated_path(page: Page) -> None:
    """[MOCK] batch Download issues ?path= repeated for each selection."""
    seen = await install_base_routes(page)
    await nav(page)
    mod = "Meta" if sys.platform == "darwin" else "Control"
    await (await row(page, "alpha.txt")).click(modifiers=[mod])
    await (await row(page, "beta.txt")).click(modifiers=[mod])
    await (await row(page, "gamma.txt")).click(modifiers=[mod])
    await (await row(page, "gamma.txt")).click(button="right", modifiers=[mod])
    await page.locator('[data-testid="files-context-menu"]').wait_for(timeout=TIMEOUT)
    async with page.expect_download():
        await page.locator('[data-testid="files-ctx-batch-download"]').click()
    url = seen["download_url"] or ""
    dec = urllib.parse.unquote(url)
    if dec.count("path=") != 3:
        fail(f"[MOCK] batch url should repeat path= x3: {url}")
    for f in ("alpha.txt", "beta.txt", "gamma.txt"):
        if f not in dec:
            fail(f"[MOCK] {f} missing from batch url: {url}")
    ok("batch download url repeats path= per selection")


async def test_download_error_non_breaking(page: Page) -> None:
    """[MOCK] download 500 does not crash the app; menu closes, list still rendered."""
    await install_base_routes(page, download_status=500)
    await nav(page)
    await (await row(page, "alpha.txt")).click(button="right")
    await page.locator('[data-testid="files-context-menu"]').wait_for(timeout=TIMEOUT)
    # native <a download> + 500: browser may or may not fire a download event;
    # the contract here is only that the SPA does not white-screen.
    try:
        async with page.expect_download(timeout=2500):
            await page.locator('[data-testid="files-ctx-download"]').click()
    except Exception:
        # no download event on error is acceptable for native anchor.
        pass
    # Menu should be dismissed and the file list still present (no crash).
    await page.wait_for_timeout(300)
    if await page.locator('[data-testid="files-list"]').count() == 0:
        fail("[MOCK] file list gone after download error (white screen?)")
    if await page.locator('[data-testid="files-context-menu"]').count() != 0:
        fail("[MOCK] context menu stuck open after download click")
    ok("download error is non-breaking (list intact, menu closed)")


async def main() -> None:
    async with async_playwright() as pw:
        browser = await pw.chromium.launch(headless=True)
        ctx = await browser.new_context(viewport={"width": 1280, "height": 800})
        page = await ctx.new_page()
        page.on("pageerror", lambda e: print(f"  (pageerror) {e}", file=sys.stderr))

        await test_single_menu_has_download_no_batch(page)
        await test_batch_menu_count_label(page)
        await test_single_download_url(page)
        await test_batch_download_url_repeated_path(page)
        await test_download_error_non_breaking(page)

        await browser.close()
    print(f"\n=== Sc7818e MOCK PASS ({_PASS} checks) ===")


if __name__ == "__main__":
    asyncio.run(main())
