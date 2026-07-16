#!/usr/bin/env python3
"""Sprint S61c9a6, Story 4 — gh onboarding hint E2E.

初回ユーザー向けに「gh (GitHub CLI) は 設定 → デプロイ設定 → アプリ からインストール
できる」ことを Drawer の Host セクションに静的な一行ヒントとして案内する。

Acceptance criteria verified here:

  [AC-S61c9a6-4-1] 初回ログイン後の画面 (Drawer Host セクション) に gh のインストール
                    方法 (アプリカタログ経由) が明記されている
  [AC-S61c9a6-4-3] data-testid="drawer-host-gh-hint" が存在し、DOM 上で到達可能

AC-S61c9a6-4-2 (実機 qcow2 ブートでの導線到達確認) は実機 VM smoke であり、この
headless E2E ではカバーしない (CLAUDE.md の qcow2 ローカル評価手順で別途確認する)。

Runs against: make serve INSTANCE=dev (palmux2 dev instance, default port 8215).

Exit 0 = PASS, else FAIL (prints failing AC to stderr).
"""
from __future__ import annotations

import os
import sys
import urllib.error
import urllib.request

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8215"
)
BASE_URL = f"http://localhost:{PORT}"
PLAYWRIGHT_TIMEOUT = 15_000  # ms

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def get_playwright():
    try:
        from playwright.sync_api import sync_playwright
        return sync_playwright
    except ImportError:
        print("SKIP: playwright not installed "
              "(pip install playwright && playwright install chromium)")
        sys.exit(0)


def scenario1(page) -> None:
    """[AC-S61c9a6-4-1][AC-S61c9a6-4-3] gh onboarding hint reachable in the Drawer
    Host section, mentioning gh + the App Catalog navigation path."""
    page.goto(BASE_URL + "/")
    page.wait_for_selector("body", timeout=PLAYWRIGHT_TIMEOUT)

    # Mobile-collapsed drawer: best-effort open (same fallback as s0c6a1b).
    section = page.locator("[data-testid='drawer-host-section']")
    if section.count() == 0:
        toggle = page.locator("[data-testid='drawer-toggle'], [aria-label*='drawer' i]")
        if toggle.count() > 0:
            toggle.first.click()

    try:
        page.wait_for_selector("[data-testid='drawer-host-section']", timeout=PLAYWRIGHT_TIMEOUT)
        ok("AC-S61c9a6-4-1", "drawer-host-section present")
    except Exception:  # noqa: BLE001
        fail("AC-S61c9a6-4-1", "drawer-host-section not found")
        return

    # Host terminal entry must still be present (pre-existing S0c6a1b surface,
    # sanity-checked here since the hint is placed right next to it).
    if page.locator("[data-testid='drawer-host-terminal']").count() == 0:
        fail("AC-S61c9a6-4-1", "drawer-host-terminal not found (pre-existing S0c6a1b surface)")
        return

    hint = page.locator("[data-testid='drawer-host-gh-hint']")
    try:
        page.wait_for_selector("[data-testid='drawer-host-gh-hint']", timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:  # noqa: BLE001
        fail("AC-S61c9a6-4-3", "drawer-host-gh-hint not found in DOM")
        return

    if not hint.first.is_visible():
        fail("AC-S61c9a6-4-3", "drawer-host-gh-hint exists but is not visible")
        return
    ok("AC-S61c9a6-4-3", "drawer-host-gh-hint present + visible")

    text = hint.first.inner_text()
    if "gh" not in text:
        fail("AC-S61c9a6-4-1", f"hint text does not mention gh: {text!r}")
        return
    if "デプロイ設定" not in text or "アプリ" not in text:
        fail("AC-S61c9a6-4-1", f"hint text does not name the App Catalog nav path: {text!r}")
        return
    ok("AC-S61c9a6-4-1", f"hint mentions gh + App Catalog nav path: {text!r}")

    # Cross-check: the App Catalog is actually reachable at that path, and
    # actually lists a "gh" card, so the copy is not a dangling promise.
    gear = page.locator("[data-testid='header-settings-btn']")
    if gear.count() == 0:
        fail("AC-S61c9a6-4-1", "header-settings-btn not found; cannot verify nav path is real")
        return
    gear.first.click()
    try:
        page.wait_for_selector("[data-testid='settings-tab-deploy']", timeout=PLAYWRIGHT_TIMEOUT)
        page.locator("[data-testid='settings-tab-deploy']").first.click()
        page.wait_for_selector("text=アプリ", timeout=PLAYWRIGHT_TIMEOUT)
        page.wait_for_selector("text=/gh/", timeout=PLAYWRIGHT_TIMEOUT)
        ok("AC-S61c9a6-4-1", "設定 → デプロイ設定 → アプリ actually lists gh (nav path verified real)")
    except Exception:  # noqa: BLE001
        fail("AC-S61c9a6-4-1", "デプロイ設定 → アプリ did not surface a gh entry; hint path is stale")


def main() -> int:
    try:
        req = urllib.request.Request(f"{BASE_URL}/api/repos")
        urllib.request.urlopen(req, timeout=10)
    except urllib.error.URLError as e:
        print(f"FAIL: dev instance not reachable at {BASE_URL}: {e}", file=sys.stderr)
        print("  start it with: make serve INSTANCE=dev", file=sys.stderr)
        return 1

    sync_playwright = get_playwright()
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            ctx = browser.new_context(viewport={"width": 1280, "height": 800})
            # Skip the first-run onboarding wizard overlay (unrelated to this
            # Story) so it doesn't intercept clicks on the settings gear.
            ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
            page = ctx.new_page()
            scenario1(page)
            ctx.close()
        finally:
            browser.close()

    if _FAILED:
        print(f"\nFAILED ACs: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
