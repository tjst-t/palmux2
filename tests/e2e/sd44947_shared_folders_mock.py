"""Sd44947 — shared folders GUI *mock* test (Playwright + page.route).

Complements the real-backend E2E (sd44947_shared_folders.py). AC-Sd44947-2-5
requires a mock test covering the transient / fault states that cannot be
force-triggered against a live backend:

  [AC-Sd44947-2-5] Loading    — GET /api/deploy is delayed → deploy-loading spinner shows
  [AC-Sd44947-2-5] Empty      — GET returns workspace.sharedDirs=[] → empty shared-dirs-list
  [AC-Sd44947-2-5] out-of-home— typing /etc/passwd → inline shared-dir-error (client-side)
  [AC-Sd44947-2-5] SaveError  — POST /api/deploy/apply → 500 → deploy-apply-error banner,
                                values retained

The backend is fully mocked via page.route, so this exercises the frontend state
machine deterministically. Interactive elements are asserted to carry data-testid.

Run against any dev instance that serves the frontend:
    PALMUX2_DEV_PORT=<port> python tests/e2e/sd44947_shared_folders_mock.py
"""
from __future__ import annotations

import json
import os
import sys
import time

from playwright.sync_api import sync_playwright

PORT = os.environ.get("PALMUX2_DEV_PORT") or os.environ.get("PALMUX_DEV_PORT") or "8200"
BASE = f"http://localhost:{PORT}"
HOME = "/home/ubuntu"

DEPLOY_VIEW = {
    "server": {
        "addr": "127.0.0.1:8200", "base_path": "/", "max_connections": 0,
        "tmux_prefix": "_palmux_", "caddy_admin": "http://localhost:2019",
        "claude_bin": "claude", "claude_args": "",
    },
    "public": {"domain": "", "basic_auth_user": "admin"},
    "workspace": {"sharedDirs": [], "home": HOME},
    "secrets": {
        "hasSsoSecret": True, "hasBasicAuthHash": True,
        "hasToken": False, "hasCloudflareToken": False,
    },
    "configured": True,
}

GET_DELAY_S = 1.5  # deploy-loading spinner must be observable within this window


def _launch(p):
    try:
        return p.chromium.launch(
            headless=True, executable_path="/usr/bin/google-chrome",
            args=["--no-sandbox"],
        )
    except Exception:
        return p.chromium.launch(headless=True, args=["--no-sandbox"])


def open_deploy_tab(page) -> None:
    page.keyboard.press("Meta+k")
    try:
        page.wait_for_selector("input", timeout=1500)
    except Exception:
        page.keyboard.press("Control+k")
        page.wait_for_selector("input", timeout=2000)
    inp = page.locator("input").first
    inp.click()
    inp.fill(">settings")
    page.wait_for_timeout(300)
    inp.press("Enter")
    page.wait_for_selector('[data-testid="settings-tabs"]', timeout=5000)
    page.locator('[data-testid="settings-tab-deploy"]').click()


def main() -> int:
    failures: list[str] = []

    def check(name: str, cond: bool) -> None:
        print(f"[{'PASS' if cond else 'FAIL'}] {name}")
        if not cond:
            failures.append(name)

    with sync_playwright() as p:
        browser = _launch(p)
        page = browser.new_context().new_page()

        # --- Mock the deploy API. GET is delayed on the FIRST call so the
        #     Loading spinner is observable; POST /apply returns 500 (SaveError).
        state = {"get_calls": 0}

        def route_deploy(route):
            req = route.request
            if req.method == "POST" and req.url.rstrip("/").endswith("/api/deploy/apply"):
                route.fulfill(
                    status=500, content_type="application/json",
                    body=json.dumps({"error": "incus に接続できません (mocked)"}),
                )
                return
            # GET /api/deploy
            state["get_calls"] += 1
            if state["get_calls"] == 1:
                time.sleep(GET_DELAY_S)  # hold the response → spinner stays up
            route.fulfill(
                status=200, content_type="application/json",
                body=json.dumps(DEPLOY_VIEW),
            )

        page.route("**/api/deploy", route_deploy)
        page.route("**/api/deploy/apply", route_deploy)

        page.goto(BASE, wait_until="domcontentloaded")
        page.wait_for_timeout(1000)

        open_deploy_tab(page)

        # ---- Loading: spinner visible while the (delayed) GET is in flight ----
        try:
            page.wait_for_selector('[data-testid="deploy-loading"]', state="visible", timeout=1200)
            loading_seen = True
        except Exception:
            loading_seen = False
        check("AC-Sd44947-2-5 Loading: deploy-loading spinner shown while GET in flight", loading_seen)

        # ---- Empty: after the GET resolves, an empty shared-dirs-list renders --
        page.wait_for_selector('[data-testid="settings-deploy-panel"]', timeout=5000)
        page.wait_for_selector('[data-testid="shared-dirs-list"]', timeout=5000)
        empty_list = page.locator('[data-testid="shared-dirs-list"]')
        check("AC-Sd44947-2-5 Empty: shared-dirs-list present with no rows",
              empty_list.count() > 0
              and empty_list.locator('[data-testid^="shared-dir-remove-"]').count() == 0)
        warn = page.locator('[data-testid="shared-dirs-warning"]')
        check("AC-Sd44947-2-2 ⚠ exposure warning present", warn.count() > 0 and warn.is_visible())

        # ---- out-of-home (InputError): client-side rejection, no row added -----
        page.locator('[data-testid="shared-dir-input"]').fill("/etc/passwd")
        page.locator('[data-testid="shared-dir-add"]').click()
        page.wait_for_timeout(150)
        err = page.locator('[data-testid="shared-dir-error"]')
        check("AC-Sd44947-2-5 out-of-home: inline shared-dir-error shown",
              err.count() > 0 and err.is_visible() and "$HOME" in err.inner_text())
        check("AC-Sd44947-2-5 out-of-home: list unchanged (no row added)",
              page.locator('[data-testid^="shared-dir-remove-"]').count() == 0)

        # ---- SaveError: add a valid path, Apply → 500 → error banner, value kept
        page.locator('[data-testid="shared-dir-input"]').fill("~/.infisical")
        page.locator('[data-testid="shared-dir-add"]').click()
        page.wait_for_timeout(150)
        check("AC-Sd44947-2-5 valid ~/-path adds a pending row",
              page.locator('[data-testid="shared-dir-remove-0"]').count() > 0)

        page.locator('[data-testid="apply-deploy"]').click()
        try:
            page.wait_for_selector('[data-testid="deploy-apply-error"]', state="visible", timeout=6000)
            save_error_seen = True
            err_text = page.locator('[data-testid="deploy-apply-error"]').inner_text()
        except Exception:
            save_error_seen, err_text = False, ""
        check("AC-Sd44947-2-5 SaveError: deploy-apply-error banner shown on 500",
              save_error_seen and len(err_text) > 0)
        check("AC-Sd44947-2-5 SaveError: pending value retained after failure",
              page.locator('[data-testid="shared-dir-remove-0"]').count() > 0)

        browser.close()

    print()
    if failures:
        print(f"FAILED: {len(failures)} check(s): {failures}")
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
