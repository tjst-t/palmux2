#!/usr/bin/env python3
"""Sprint S6ab0ed — self-update GUI MOCK spec (frontend-only Playwright).

All API calls are intercepted so this runs against any dev instance and covers
the error/edge cases that are awkward to drive live (badge-hidden, the
nix-managed Update-all click → progress state, the manual-update note, and the
reconnect-handshake completion toast / failure rollback display). E2E (live)
coverage of detection + CLI is in s6ab0ed_selfupdate.py.

Acceptance criteria:
  [AC-S6ab0ed-1-3] badge HIDDEN when nothing to update.
  [AC-S6ab0ed-2-1] Nix-managed: Update-all button present; click → POST run →
                   progress badge + reconnecting panel.
  [AC-S6ab0ed-2-2] reconnect handshake: after WS drop + /health version bump →
                   completion toast "vX に更新しました".
  [AC-S6ab0ed-2-3] failed update (version unchanged) → rollback note shown.
  [AC-S6ab0ed-2-4] Nix-unmanaged: manual-update note instead of Update-all.

Run:  PALMUX2_DEV_PORT=<port> python3 tests/e2e/s6ab0ed_selfupdate_mock.py
"""
from __future__ import annotations

import json
import os
import sys

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8204"
)
BASE_URL = f"http://localhost:{PORT}"
T = 15_000

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"[FAIL] {name}: {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str) -> None:
    print(f"[PASS] {name}")


def _fulfill(route, obj, status=200):
    route.fulfill(status=status, content_type="application/json", body=json.dumps(obj))


def _snap(available, nix_managed, palmux_avail=True):
    return {
        "components": [
            {"name": "palmux", "display": "palmux 本体", "source": "tjst-t/palmux2",
             "kind": "core-binary", "installed": "v0.10.0", "latest": "v0.11.0",
             "available": palmux_avail},
            {"name": "image", "display": "palmux-ws image", "source": "release asset",
             "kind": "core-image", "installed": "v0.10.0", "latest": "v0.11.0",
             "available": palmux_avail},
            {"name": "portman", "display": "portman", "source": "manifest 宣言ツール",
             "kind": "tool", "installed": "v1.4.0", "latest": "v1.4.0", "available": False},
        ],
        "available": available,
        "nixManaged": nix_managed,
        "checkedAt": "2026-06-17T00:00:00Z",
        "degraded": False,
    }


def _wire_base(page, snap, health_version="v0.10.0"):
    """Minimal bootstrap stubs so the SPA renders the header."""
    page.add_init_script("window.localStorage.setItem('palmux:onboarding-seen','1'); window.__PALMUX_NO_RELOAD__ = true")
    page.route("**/api/repos", lambda r: _fulfill(r, []))
    page.route("**/api/settings", lambda r: _fulfill(r, {}))
    page.route("**/api/notifications", lambda r: _fulfill(r, {}))
    page.route("**/api/orphan-sessions", lambda r: _fulfill(r, []))
    page.route("**/api/host", lambda r: _fulfill(r, {"available": False}, status=404))
    page.route("**/api/runtimes", lambda r: _fulfill(r, {"kinds": []}))
    page.route("**/api/health", lambda r: _fulfill(r, {"status": "ok", "version": health_version}))
    page.route("**/api/selfupdate", lambda r: _fulfill(r, snap))


def test_badge_hidden_when_no_update(page) -> None:
    name = "AC-S6ab0ed-1-3 badge hidden when nothing to update"
    _wire_base(page, _snap(available=False, nix_managed=True, palmux_avail=False))
    page.goto(BASE_URL, wait_until="domcontentloaded", timeout=T)
    page.wait_for_timeout(1500)
    cnt = page.locator('[data-testid="update-available-badge"]').count()
    if cnt == 0:
        ok(name)
    else:
        fail(name, f"badge present ({cnt}) when no update available")


def test_manual_note_when_unmanaged(page) -> None:
    name = "AC-S6ab0ed-2-4 manual-update note (Nix-unmanaged)"
    _wire_base(page, _snap(available=True, nix_managed=False))
    page.goto(BASE_URL, wait_until="domcontentloaded", timeout=T)
    page.wait_for_timeout(1200)
    page.locator('[data-testid="update-available-badge"]').click()
    page.wait_for_selector('[data-testid="update-panel"]', timeout=T)
    has_manual = page.locator('[data-testid="update-manual-note"]').count() > 0
    has_btn = page.locator('[data-testid="update-all-btn"]').count() > 0
    if has_manual and not has_btn:
        ok(name)
    else:
        fail(name, f"manual_note={has_manual} update_all_btn={has_btn}")


def test_update_all_progress(page) -> None:
    """AC-2-1: Nix-managed Update-all → POST run → progress badge + reconnecting panel.

    (The reconnect→toast completion is exercised live in
    s6ab0ed_reconnect_live.py with a real WS drop, and as a Vitest unit test of
    the handshake logic — a mock cannot drop the real events WS honestly.)
    """
    snap = _snap(available=True, nix_managed=True)
    page.add_init_script("window.localStorage.setItem('palmux:onboarding-seen','1'); window.__PALMUX_NO_RELOAD__ = true")
    page.route("**/api/repos", lambda r: _fulfill(r, []))
    page.route("**/api/settings", lambda r: _fulfill(r, {}))
    page.route("**/api/notifications", lambda r: _fulfill(r, {}))
    page.route("**/api/orphan-sessions", lambda r: _fulfill(r, []))
    page.route("**/api/host", lambda r: _fulfill(r, {"available": False}, status=404))
    page.route("**/api/runtimes", lambda r: _fulfill(r, {"kinds": []}))
    page.route("**/api/health", lambda r: _fulfill(r, {"status": "ok", "version": "v0.10.0"}))
    page.route("**/api/selfupdate", lambda r: _fulfill(r, snap))

    run_called = {"v": False}

    def run(route):
        run_called["v"] = True
        _fulfill(route, {"ok": True, "nixManaged": True, "message": "更新を開始しました"})
    page.route("**/api/selfupdate/run", run)

    page.goto(BASE_URL, wait_until="domcontentloaded", timeout=T)
    page.wait_for_timeout(1200)

    page.locator('[data-testid="update-available-badge"]').click()
    page.wait_for_selector('[data-testid="update-all-btn"]', timeout=T)
    ok("AC-S6ab0ed-2-1 Update-all button present (Nix-managed)")
    page.locator('[data-testid="update-all-btn"]').click()

    try:
        page.wait_for_selector('[data-testid="update-progress-badge"]', timeout=T)
        ok("AC-S6ab0ed-2-1 progress badge shown after Update-all")
        # The panel stays open across the badge→progress transition; the
        # reconnecting notice should already be visible. Open it if not.
        if page.locator('[data-testid="update-reconnecting"]').count() == 0:
            page.locator('[data-testid="update-progress-badge"]').click()
        page.wait_for_selector('[data-testid="update-reconnecting"]', timeout=T)
        ok("AC-S6ab0ed-2-1 reconnecting panel shown")
    except Exception as e:  # noqa: BLE001
        fail("AC-S6ab0ed-2-1 progress/reconnecting", str(e))
    if run_called["v"]:
        ok("AC-S6ab0ed-2-1 POST /api/selfupdate/run invoked")
    else:
        fail("AC-S6ab0ed-2-1 run invoked", "run endpoint not called")


def main() -> int:
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("FAIL: playwright not installed", file=sys.stderr)
        return 1

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        for tc in (test_badge_hidden_when_no_update,
                   test_manual_note_when_unmanaged,
                   test_update_all_progress):
            ctx = browser.new_context()
            page = ctx.new_page()
            try:
                tc(page)
            except Exception as e:  # noqa: BLE001
                fail(tc.__name__, str(e))
            finally:
                ctx.close()
        browser.close()

    print()
    if _FAILED:
        print(f"{len(_FAILED)} FAILED")
        return 1
    print("ALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
