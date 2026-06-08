#!/usr/bin/env python3
"""Sprint Sc7818e — Files タブ ダウンロード UI の mock / edge-case test.

GUI の分岐ロジック (single vs batch メニュー項目の排他性・文言の選択数反映) と
ダウンロード URL 構築を、Files タブを実描画させて検証する。リポジトリは実 fixture
(palmux2_test_fixture) を使い、一覧は実バックエンドが返す。

設計上の注記 (decisions.json guard 記録参照):
  ダウンロードは native `<a download>` で起動するため、Chromium はそのリクエストを
  ブラウザのダウンロード機構へ直接渡し、page.route / request イベントでは捕捉できない
  (実測で確認)。したがって:
    - download エンドポイントを page.route で差し替えてエラー (500) を擬似注入する
      テストは原理的に不可能。かつ FE は native anchor 任せでレスポンスを JS で観測
      しない設計なので「エラー時の UI 破壊」経路自体が存在しない (= テスト対象なし)。
    - URL 構築の検証は expect_download().value.url (実バックエンド) で行う。
  AC の本検証は e2e (sc7818e_files_download.py) が real backend に対して網羅する。
  本 mock は FE のメニュー分岐ロジックの fast-feedback を担う。

Covers (GUI edge cases, Story Sc7818e-3):
  [MOCK] 選択 0-1 では batch-download が出ず single download が出る
  [MOCK] N>=2 で batch-download が出て single download は出ない、文言が N を反映
  [MOCK] single Download の URL が {prefix}/download?path=<file> (path 1 個)
  [MOCK] batch Download の URL が path= 反復 (選択数ぶん)

Exit 0 = PASS, nonzero = FAIL.
"""
from __future__ import annotations

import asyncio
import sys
import urllib.parse
from pathlib import Path

from playwright.async_api import Page, async_playwright

sys.path.insert(0, str(Path(__file__).parent))
from _fixture import BASE_URL, _http_json, palmux2_test_fixture

TIMEOUT = 12_000  # ms

_PASS = 0


def ok(msg: str) -> None:
    global _PASS
    _PASS += 1
    print(f"  ok: {msg}")


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def files_prefix(rid: str, bid: str) -> str:
    return f"/api/repos/{urllib.parse.quote(rid)}/branches/{urllib.parse.quote(bid)}/files"


def api_create_file(rid: str, bid: str, path: str, content: str) -> None:
    code, data = _http_json("POST", f"{files_prefix(rid, bid)}/create", body={"path": path, "content": content})
    if code not in (200, 201):
        fail(f"seed {path}: {code} {data}")


async def nav(page: Page, rid: str, bid: str) -> None:
    await page.goto(f"{BASE_URL}/{urllib.parse.quote(rid)}/{urllib.parse.quote(bid)}/files")
    await page.wait_for_selector('[data-testid="files-list"]', timeout=TIMEOUT)


async def row(page: Page, name: str):
    return page.locator(f'[data-testid="files-list"] button:has-text("{name}")').first


# ── Tests (desktop width — list pane is always visible so shared context is ok) ──


async def test_single_menu_has_download_no_batch(page: Page, rid: str, bid: str) -> None:
    """[MOCK] selection 0-1: single 'Download' present, batch absent."""
    await nav(page, rid, bid)
    await (await row(page, "alpha.txt")).click(button="right")
    await page.locator('[data-testid="files-context-menu"]').wait_for(timeout=TIMEOUT)
    if not await page.locator('[data-testid="files-ctx-download"]').is_visible():
        fail("[MOCK] single 'Download' item not visible")
    if await page.locator('[data-testid="files-ctx-batch-download"]').count() != 0:
        fail("[MOCK] batch-download leaked into single-selection menu")
    ok("single menu shows Download, no batch-download")
    await page.keyboard.press("Escape")


async def test_batch_menu_count_label(page: Page, rid: str, bid: str) -> None:
    """[MOCK] N>=2: batch 'Download N items…' present, label reflects N, single absent."""
    await nav(page, rid, bid)
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


async def test_single_download_url(page: Page, rid: str, bid: str) -> None:
    """[MOCK] single Download → URL {prefix}/download?path=<file> (1 path)."""
    await nav(page, rid, bid)
    await (await row(page, "beta.txt")).click(button="right")
    await page.locator('[data-testid="files-context-menu"]').wait_for(timeout=TIMEOUT)
    async with page.expect_download() as dl:
        await page.locator('[data-testid="files-ctx-download"]').click()
    url = urllib.parse.unquote((await dl.value).url)
    if "/files/download?" not in url or url.count("path=") != 1 or "beta.txt" not in url:
        fail(f"[MOCK] single download url wrong: {url}")
    ok("single download url correct (1 path=)")


async def test_batch_download_url_repeated_path(page: Page, rid: str, bid: str) -> None:
    """[MOCK] batch Download → ?path= repeated for each selection."""
    await nav(page, rid, bid)
    mod = "Meta" if sys.platform == "darwin" else "Control"
    await (await row(page, "alpha.txt")).click(modifiers=[mod])
    await (await row(page, "beta.txt")).click(modifiers=[mod])
    await (await row(page, "gamma.txt")).click(modifiers=[mod])
    await (await row(page, "gamma.txt")).click(button="right", modifiers=[mod])
    await page.locator('[data-testid="files-context-menu"]').wait_for(timeout=TIMEOUT)
    async with page.expect_download() as dl:
        await page.locator('[data-testid="files-ctx-batch-download"]').click()
    url = urllib.parse.unquote((await dl.value).url)
    if url.count("path=") != 3:
        fail(f"[MOCK] batch url should repeat path= x3: {url}")
    for f in ("alpha.txt", "beta.txt", "gamma.txt"):
        if f not in url:
            fail(f"[MOCK] {f} missing from batch url: {url}")
    ok("batch download url repeats path= per selection")


async def main() -> None:
    with palmux2_test_fixture("sc7818e_mock") as fx:
        rid, bid = fx.repo_id, fx.branch_id
        for name in ("alpha.txt", "beta.txt", "gamma.txt"):
            api_create_file(rid, bid, name, "x\n")

        async with async_playwright() as pw:
            browser = await pw.chromium.launch(headless=True)
            ctx = await browser.new_context(viewport={"width": 1280, "height": 800})
            page = await ctx.new_page()
            page.on("pageerror", lambda e: print(f"  (pageerror) {e}", file=sys.stderr))

            await test_single_menu_has_download_no_batch(page, rid, bid)
            await test_batch_menu_count_label(page, rid, bid)
            await test_single_download_url(page, rid, bid)
            await test_batch_download_url_repeated_path(page, rid, bid)

            await browser.close()
    print(f"\n=== Sc7818e MOCK PASS ({_PASS} checks) ===")


if __name__ == "__main__":
    asyncio.run(main())
