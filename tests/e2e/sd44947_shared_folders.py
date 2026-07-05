"""Sd44947 — shared folders (profile-as-mold) GUI E2E (real-browser Playwright).

Drives the real frontend (served by a running palmux2 dev instance) through the
real backend + real incus. Covers Story 2 (GUI + backend):

  [AC-Sd44947-2-1] config → GET /api/deploy returns workspace.sharedDirs as
                   absolute paths; adding + Apply persists it.
  [AC-Sd44947-2-2] deploy tab shows a 共有フォルダ section: list / add / remove /
                   ⚠ warning; Apply rewrites the profile (workspace class).
  [AC-Sd44947-2-3] out-of-$HOME path rejected inline (shared-dir-error), and the
                   API rejects it with 400.
  [AC-Sd44947-2-5] state diagram states (Empty / Adding-Populated / InputError /
                   Removing / Saved) are observable through the real backend;
                   every interactive element carries a data-testid.

Run against a dev instance:
    PALMUX2_DEV_PORT=<port> python tests/e2e/sd44947_shared_folders.py
"""
from __future__ import annotations

import json
import os
import sys
import urllib.request

from playwright.sync_api import sync_playwright

PORT = os.environ.get("PALMUX2_DEV_PORT") or os.environ.get("PALMUX_DEV_PORT") or "8200"
BASE = f"http://localhost:{PORT}"
HOME = os.path.expanduser("~")
E2E_DIR = os.path.join(HOME, ".sd44947-e2e")
E2E_ABS = E2E_DIR  # absolute host path the GUI/API should echo back


def _api(method: str, path: str, body: dict | None = None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(
        BASE + path, data=data, method=method,
        headers={"Content-Type": "application/json"},
    )
    try:
        with urllib.request.urlopen(req, timeout=15) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode() or "{}")


def _launch(p):
    # Prefer the system chrome (headless) with --no-sandbox per the project rig.
    try:
        return p.chromium.launch(
            headless=True, executable_path="/usr/bin/google-chrome",
            args=["--no-sandbox"],
        )
    except Exception:
        return p.chromium.launch(headless=True, args=["--no-sandbox"])


def open_command_palette(page) -> None:
    page.keyboard.press("Meta+k")
    try:
        page.wait_for_selector('input', timeout=1500)
    except Exception:
        page.keyboard.press("Control+k")
        page.wait_for_selector('input', timeout=2000)


def open_deploy_tab(page) -> None:
    open_command_palette(page)
    inp = page.locator('input').first
    inp.click()
    inp.fill(">settings")
    page.wait_for_timeout(300)
    inp.press("Enter")
    page.wait_for_selector('[data-testid="settings-tabs"]', timeout=5000)
    page.locator('[data-testid="settings-tab-deploy"]').click()
    page.wait_for_selector('[data-testid="settings-deploy-panel"]', timeout=5000)
    page.wait_for_timeout(500)


def main() -> int:
    failures: list[str] = []

    def check(name: str, cond: bool) -> None:
        print(f"[{'PASS' if cond else 'FAIL'}] {name}")
        if not cond:
            failures.append(name)

    os.makedirs(E2E_DIR, exist_ok=True)
    # Start from a clean slate.
    _api("POST", "/api/deploy/apply", {"workspace": {"sharedDirs": []}})

    # ---- Backend AC-2-3: out-of-$HOME → 400 (no browser) --------------------
    code, _ = _api("POST", "/api/deploy/apply", {"workspace": {"sharedDirs": ["/etc/passwd"]}})
    check("AC-Sd44947-2-3 API rejects out-of-$HOME path with 400", code == 400)

    with sync_playwright() as p:
        browser = _launch(p)
        page = browser.new_context().new_page()
        page.goto(BASE, wait_until="domcontentloaded")
        page.wait_for_timeout(1200)

        open_deploy_tab(page)

        # AC-2-2: warning banner + empty list render.
        warn = page.locator('[data-testid="shared-dirs-warning"]')
        check("AC-Sd44947-2-2 ⚠ exposure warning banner visible", warn.count() > 0 and warn.is_visible())
        check("AC-Sd44947-2-5 Empty: shared-dirs-list present", page.locator('[data-testid="shared-dirs-list"]').count() > 0)
        check("AC-Sd44947-2-2 add input + button present",
              page.locator('[data-testid="shared-dir-input"]').count() > 0
              and page.locator('[data-testid="shared-dir-add"]').count() > 0)

        # AC-2-3 InputError: type an out-of-home path → inline error, no row added.
        page.locator('[data-testid="shared-dir-input"]').fill("/etc/passwd")
        page.locator('[data-testid="shared-dir-add"]').click()
        page.wait_for_timeout(200)
        err = page.locator('[data-testid="shared-dir-error"]')
        check("AC-Sd44947-2-3 InputError: inline error for out-of-$HOME path",
              err.count() > 0 and err.is_visible())

        # AC-2-1 Adding→Populated: add a valid ~/-path → a pending row appears.
        page.locator('[data-testid="shared-dir-input"]').fill("~/.sd44947-e2e")
        page.locator('[data-testid="shared-dir-add"]').click()
        page.wait_for_timeout(200)
        list_text = page.locator('[data-testid="shared-dirs-list"]').inner_text()
        check("AC-Sd44947-2-1 Adding: pending row shows absolute host path",
              E2E_ABS in list_text)
        check("AC-Sd44947-2-2 remove button present on row",
              page.locator('[data-testid="shared-dir-remove-0"]').count() > 0)

        # AC-2-1 Apply → Saved: success result + backend GET reflects.
        page.locator('[data-testid="apply-deploy"]').click()
        try:
            page.wait_for_selector('[data-testid="apply-result"]', timeout=6000)
            applied = True
            result_text = page.locator('[data-testid="apply-result"]').inner_text()
        except Exception:
            applied, result_text = False, ""
        check("AC-Sd44947-2-2 Apply returns a classified result", applied and len(result_text) > 0)
        check("AC-Sd44947-2-2 Apply result names the workspace/shared-folder change",
              "共有フォルダ" in result_text or "workspace" in result_text)

        code, view = _api("GET", "/api/deploy")
        got = (view.get("workspace") or {}).get("sharedDirs") or []
        check("AC-Sd44947-2-1 GET /api/deploy reflects the added shared dir (absolute)",
              E2E_ABS in got)

        # AC-2-2 Removing: remove the row → Apply → backend GET shows it gone.
        page.locator('[data-testid="shared-dir-remove-0"]').click()
        page.wait_for_timeout(200)
        page.locator('[data-testid="apply-deploy"]').click()
        page.wait_for_timeout(800)
        code, view2 = _api("GET", "/api/deploy")
        got2 = (view2.get("workspace") or {}).get("sharedDirs") or []
        check("AC-Sd44947-2-2 Removing: Apply removes the shared dir from backend",
              E2E_ABS not in got2)

        # AC-2-5 SaveError: force POST /api/deploy/apply to 500 → error surfaced,
        # the pending row is retained (values not lost).
        page.route("**/api/deploy/apply", lambda route: route.fulfill(
            status=500, content_type="application/json",
            body=json.dumps({"error": "incus unreachable (injected)"})))
        page.locator('[data-testid="shared-dir-input"]').fill("~/.sd44947-e2e")
        page.locator('[data-testid="shared-dir-add"]').click()
        page.wait_for_timeout(150)
        page.locator('[data-testid="apply-deploy"]').click()
        try:
            page.wait_for_selector('[data-testid="deploy-apply-error"]', timeout=4000)
            save_err = True
        except Exception:
            save_err = False
        row_retained = E2E_ABS in page.locator('[data-testid="shared-dirs-list"]').inner_text()
        check("AC-Sd44947-2-5 SaveError: apply 500 surfaces an error, values retained",
              save_err and row_retained)
        page.unroute("**/api/deploy/apply")

        # AC-2-5 Loading: open a fresh page whose GET /api/deploy is delayed, so
        # the deploy panel's loading state (data-testid=deploy-loading) is visible
        # before it populates.
        import time as _t
        page3 = browser.new_context().new_page()

        def _slow_deploy(route):
            _t.sleep(1.2)
            route.continue_()

        page3.route("**/api/deploy", _slow_deploy)
        page3.goto(BASE, wait_until="domcontentloaded")
        page3.wait_for_timeout(1000)
        loading_seen = False
        try:
            open_command_palette(page3)
            inp = page3.locator('input').first
            inp.click(); inp.fill(">settings"); page3.wait_for_timeout(300); inp.press("Enter")
            page3.wait_for_selector('[data-testid="settings-tabs"]', timeout=5000)
            page3.locator('[data-testid="settings-tab-deploy"]').click()
            # The delayed GET keeps the panel in the loading state briefly.
            page3.wait_for_selector('[data-testid="deploy-loading"]', timeout=2500)
            loading_seen = True
        except Exception:
            loading_seen = False
        check("AC-Sd44947-2-5 Loading: deploy panel shows loading state during fetch", loading_seen)
        page3.unroute("**/api/deploy")

        browser.close()

    # Cleanup: reset shared dirs + remove the temp dir.
    _api("POST", "/api/deploy/apply", {"workspace": {"sharedDirs": []}})
    try:
        os.rmdir(E2E_DIR)
    except OSError:
        pass

    print()
    if failures:
        print(f"FAILED: {len(failures)} check(s): {failures}")
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
