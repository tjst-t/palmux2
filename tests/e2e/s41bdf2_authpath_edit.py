"""S41bdf2-4 — auth-folder path edit GUI E2E (real-browser Playwright, real backend).

Drives the real frontend against the real /api/apps backend on a dev instance. An
app's auth folder (authPath) can be corrected after add: install a catalog app, open
the inline edit control on its 認証フォルダ row, and save a new $HOME-scoped path. The
override is persisted (apps.json) and echoed by GET /api/apps as the effective path.
An out-of-$HOME path disables save client-side (server is authoritative too).

  [AC-S41bdf2-4-2] the share row exposes an edit affordance (data-testid) once installed
  [AC-S41bdf2-4-2] editing to a $HOME-scoped path persists an override (GET /api/apps)
  [AC-S41bdf2-4-2] an out-of-$HOME draft disables the save button (client hint)

Run against a dev instance:
    PALMUX2_DEV_PORT=<port> python tests/e2e/s41bdf2_authpath_edit.py
"""
from __future__ import annotations

import json
import os
import sys
import urllib.error
import urllib.request

from playwright.sync_api import sync_playwright

PORT = os.environ.get("PALMUX2_DEV_PORT") or os.environ.get("PALMUX_DEV_PORT") or "8200"
BASE = f"http://localhost:{PORT}"
HOME = os.path.expanduser("~")


def _api(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode() or "{}")


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

    def check(name, cond, extra=""):
        print(f"[{'PASS' if cond else 'FAIL'}] {name}" + (f" — {extra}" if extra and not cond else ""))
        if not cond:
            failures.append(name)

    # Deterministic start: infisical uninstalled + unshared.
    _api("POST", "/api/apps/share", {"id": "infisical", "on": False})
    _api("POST", "/api/apps/uninstall", {"id": "infisical"})
    # Restore the catalog default authPath (drop any prior override) for a clean run.
    _api("POST", "/api/apps/install", {"id": "infisical"})

    with sync_playwright() as p:
        browser = _launch(p)
        page = browser.new_context().new_page()
        page.goto(BASE, wait_until="domcontentloaded")
        page.wait_for_timeout(1000)
        open_deploy_tab(page)

        page.wait_for_selector('[data-testid="apps-grid"]', timeout=6000)
        card = page.locator('[data-testid="app-card-infisical"]')
        card.scroll_into_view_if_needed()

        # [AC-4-2] edit affordance is present once installed.
        edit_btn = page.locator('[data-testid="share-path-edit-infisical"]')
        check("AC-S41bdf2-4-2 edit affordance present on installed card", edit_btn.count() == 1)

        # Open the inline editor.
        edit_btn.click()
        page.wait_for_selector('[data-testid="share-path-input-infisical"]', timeout=4000)
        inp = page.locator('[data-testid="share-path-input-infisical"]')
        save = page.locator('[data-testid="share-path-save-infisical"]')

        # Out-of-$HOME draft disables save (client hint).
        inp.fill("/etc/secret")
        page.wait_for_timeout(150)
        check("AC-S41bdf2-4-2 out-of-$HOME draft disables save", save.is_disabled(),
              extra="save should be disabled for /etc/secret")
        check("AC-S41bdf2-4-2 out-of-$HOME shows scope error",
              page.locator('[data-testid="share-path-error-infisical"]').count() > 0)

        # A valid $HOME-scoped draft enables save.
        new_path = "~/.config/infisical-edited"
        inp.fill(new_path)
        page.wait_for_timeout(150)
        check("AC-S41bdf2-4-2 in-$HOME draft enables save", not save.is_disabled())

        # Save → override persisted, editor closes.
        save.click()
        page.wait_for_function(
            """() => !document.querySelector('[data-testid=share-path-input-infisical]')""",
            timeout=8000,
        )
        st, lv = _api("GET", "/api/apps")
        apps = {a["id"]: a for a in lv.get("apps", [])}
        check("AC-S41bdf2-4-2 override persisted + returned as effective authPath",
              apps.get("infisical", {}).get("authPath") == new_path,
              extra=str(apps.get("infisical", {}).get("authPath")))

        # The card's path row now shows the corrected path.
        card.scroll_into_view_if_needed()
        check("AC-S41bdf2-4-2 card shows corrected path",
              "infisical-edited" in card.inner_text())

        # Cancel path: reopen editor, change, cancel → no change.
        page.locator('[data-testid="share-path-edit-infisical"]').click()
        page.wait_for_selector('[data-testid="share-path-input-infisical"]', timeout=4000)
        page.locator('[data-testid="share-path-input-infisical"]').fill("~/.config/should-not-save")
        page.locator('[data-testid="share-path-cancel-infisical"]').click()
        page.wait_for_timeout(300)
        st, lv = _api("GET", "/api/apps")
        apps = {a["id"]: a for a in lv.get("apps", [])}
        check("AC-S41bdf2-4-2 cancel does not persist",
              apps.get("infisical", {}).get("authPath") == new_path)

        browser.close()

    # cleanup
    _api("POST", "/api/apps/share", {"id": "infisical", "on": False})
    _api("POST", "/api/apps/uninstall", {"id": "infisical"})

    print()
    if failures:
        print(f"FAILED: {len(failures)} check(s): {failures}")
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
