"""S41bdf2-4 — auth-folder path edit *mock* test (Playwright + page.route).

Exercises the inline auth-path editor's input state machine deterministically with a
fully mocked backend: the edit affordance only shows once installed, the edit button
reveals the input + save/cancel, an out-of-$HOME draft disables save and shows the
scope error, an unchanged/empty draft disables save, a valid $HOME draft enables save,
and cancel restores the read-only path row.

  [AC-S41bdf2-4-2] edit affordance present on installed cards; absent when not installed
  [AC-S41bdf2-4-2] out-of-$HOME draft → save disabled + inline scope error
  [AC-S41bdf2-4-2] unchanged draft → save disabled (no-op guard)
  [AC-S41bdf2-4-2] valid $HOME draft → save enabled; cancel restores the path row

Run against any dev instance serving the frontend:
    PALMUX2_DEV_PORT=<port> python tests/e2e/s41bdf2_authpath_edit_mock.py
"""
from __future__ import annotations

import json
import os
import sys

from playwright.sync_api import sync_playwright

PORT = os.environ.get("PALMUX2_DEV_PORT") or os.environ.get("PALMUX_DEV_PORT") or "8200"
BASE = f"http://localhost:{PORT}"
HOME = "/home/ubuntu"

DEPLOY_VIEW = {
    "server": {
        "addr": "127.0.0.1", "base_path": "/", "max_connections": 0,
        "tmux_prefix": "_palmux_", "caddy_admin": "", "claude_bin": "claude", "claude_args": "",
    },
    "public": {"domain": "", "basic_auth_user": "admin"},
    "workspace": {"sharedDirs": [f"{HOME}/.infisical"], "home": HOME},
    "secrets": {"hasSsoSecret": True, "hasBasicAuthHash": True, "hasToken": False, "hasCloudflareToken": False},
    "configured": True, "nixOSHost": True,
}


def _card(cid, display, pkg, auth, installed, shared, state, custom=False, error=""):
    return {
        "id": cid, "display": display, "description": f"{display} desc", "icon": "📦",
        "package": pkg, "authPath": auth, "installed": installed, "shared": shared,
        "custom": custom, "state": state, "error": error,
        "installBoundary": "rebuild", "installReach": "host+containers",
        "shareBoundary": "hot", "shareReach": "containers",
    }


APPS_VIEW = {
    "nixOSHost": True, "rebuildRunning": False, "home": HOME,
    "apps": [
        _card("infisical", "Infisical", "infisical", "~/.infisical", True, True, "shared"),
        _card("gh", "GitHub CLI", "gh", "~/.config/gh", False, False, "available"),
    ],
}


def _launch(p):
    try:
        return p.chromium.launch(headless=True, executable_path="/usr/bin/google-chrome", args=["--no-sandbox"])
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

    def check(name, cond):
        print(f"[{'PASS' if cond else 'FAIL'}] {name}")
        if not cond:
            failures.append(name)

    with sync_playwright() as p:
        browser = _launch(p)
        page = browser.new_context().new_page()

        page.route("**/api/deploy", lambda r: r.fulfill(
            status=200, content_type="application/json", body=json.dumps(DEPLOY_VIEW)))
        page.route("**/api/apps", lambda r: r.fulfill(
            status=200, content_type="application/json", body=json.dumps(APPS_VIEW)))

        page.goto(BASE, wait_until="domcontentloaded")
        page.wait_for_timeout(800)
        open_deploy_tab(page)

        page.wait_for_selector('[data-testid="apps-grid"]', timeout=6000)
        page.locator('[data-testid="app-card-infisical"]').scroll_into_view_if_needed()

        # Edit affordance present on installed infisical, absent on not-installed gh.
        check("AC-S41bdf2-4-2 installed card exposes edit affordance",
              page.locator('[data-testid="share-path-edit-infisical"]').count() == 1)
        check("AC-S41bdf2-4-2 not-installed card has NO edit affordance",
              page.locator('[data-testid="share-path-edit-gh"]').count() == 0)

        # Open the inline editor.
        page.locator('[data-testid="share-path-edit-infisical"]').click()
        page.wait_for_selector('[data-testid="share-path-input-infisical"]', timeout=4000)
        inp = page.locator('[data-testid="share-path-input-infisical"]')
        save = page.locator('[data-testid="share-path-save-infisical"]')

        # Unchanged draft (still ~/.infisical) → save disabled (no-op guard).
        check("AC-S41bdf2-4-2 unchanged draft disables save", save.is_disabled())

        # Out-of-$HOME draft → save disabled + scope error.
        inp.fill("/etc/passwd")
        page.wait_for_timeout(120)
        check("AC-S41bdf2-4-2 out-of-$HOME draft disables save + shows error",
              save.is_disabled() and page.locator('[data-testid="share-path-error-infisical"]').count() > 0)

        # ~otheruser form is rejected too.
        inp.fill("~root/x")
        page.wait_for_timeout(120)
        check("AC-S41bdf2-4-2 ~otheruser draft disables save",
              save.is_disabled() and page.locator('[data-testid="share-path-error-infisical"]').count() > 0)

        # Valid $HOME draft → save enabled + hint (no error).
        inp.fill("~/.config/infisical")
        page.wait_for_timeout(120)
        check("AC-S41bdf2-4-2 valid $HOME draft enables save + hint",
              (not save.is_disabled())
              and page.locator('[data-testid="share-path-hint-infisical"]').count() > 0
              and page.locator('[data-testid="share-path-error-infisical"]').count() == 0)

        # Cancel restores the read-only path row (input gone).
        page.locator('[data-testid="share-path-cancel-infisical"]').click()
        page.wait_for_timeout(200)
        check("AC-S41bdf2-4-2 cancel restores the read-only path row",
              page.locator('[data-testid="share-path-input-infisical"]').count() == 0
              and page.locator('[data-testid="share-path-edit-infisical"]').count() == 1)

        browser.close()

    print()
    if failures:
        print(f"FAILED: {len(failures)} check(s): {failures}")
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
