"""S41bdf2 — app card model GUI E2E (real-browser Playwright, real backend).

Drives the real frontend served by a running dev instance through the real
/api/apps backend. On a plain dev instance (non-NixOS) install persists intent and
the card advances available → installed (no rebuild); the share toggle writes the
SAME [workspace].shared_dirs source. The transient installing/error states are
covered by the mock test (s41bdf2_app_cards_mock.py).

  [AC-S41bdf2-3-1] 設定 GUI has an アプリ section: 1アプリ=1カード with install +
                   share toggles; every interactive element has data-testid.
  [AC-S41bdf2-3-2] rebuild-boundary shown: install=要rebuild chip / 共有=hot chip.
  [AC-S41bdf2-2-2] share toggle greyed (aria-disabled) until installed (従属).
  [AC-S41bdf2-1-1] clicking install advances the card to installed through the real backend.
  [AC-S41bdf2-2-1] clicking share writes the auth path to shared_dirs (GET /api/deploy).

Run against a dev instance:
    PALMUX2_DEV_PORT=<port> python tests/e2e/s41bdf2_app_cards.py
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

    # Reset test state via the API so the run is deterministic.
    _api("POST", "/api/apps/uninstall", {"id": "infisical"})
    _api("POST", "/api/apps/share", {"id": "infisical", "on": False})

    with sync_playwright() as p:
        browser = _launch(p)
        page = browser.new_context().new_page()
        page.goto(BASE, wait_until="domcontentloaded")
        page.wait_for_timeout(1000)
        open_deploy_tab(page)

        # [AC-3-1] apps section present with catalog cards.
        page.wait_for_selector('[data-testid="apps-grid"]', timeout=6000)
        card = page.locator('[data-testid="app-card-infisical"]')
        card.scroll_into_view_if_needed()
        check("AC-S41bdf2-3-1 apps section: infisical card present", card.count() == 1)
        check("AC-S41bdf2-3-1 install + share toggles carry data-testid",
              page.locator('[data-testid="install-toggle-infisical"]').count() == 1
              and page.locator('[data-testid="share-toggle-infisical"]').count() == 1)

        # [AC-3-2] rebuild-boundary chips.
        check("AC-S41bdf2-3-2 rebuild-boundary chips (要 rebuild / hot)",
              "要 rebuild" in card.inner_text() and "hot" in card.inner_text())

        # [AC-2-2] share greyed until installed (従属).
        share = page.locator('[data-testid="share-toggle-infisical"]')
        check("AC-S41bdf2-2-2 share toggle greyed before install",
              share.get_attribute("aria-disabled") == "true"
              and page.locator('[data-testid="install-toggle-infisical"]').get_attribute("aria-checked") == "false")

        # [AC-1-1] click install → card advances to installed (real backend).
        page.locator('[data-testid="install-toggle-infisical"]').click()
        page.wait_for_function(
            """() => { const t = document.querySelector('[data-testid=install-toggle-infisical]');
                       return t && t.getAttribute('aria-checked') === 'true'; }""",
            timeout=8000,
        )
        st, lv = _api("GET", "/api/apps")
        apps = {a["id"]: a for a in lv.get("apps", [])}
        check("AC-S41bdf2-1-1 install advanced card to installed (backend)",
              apps.get("infisical", {}).get("installed") is True)

        # share toggle now enabled.
        check("AC-S41bdf2-2-2 share toggle enabled after install",
              page.locator('[data-testid="share-toggle-infisical"]').get_attribute("aria-disabled") != "true")

        # [AC-2-1] click share → auth path written to shared_dirs single source.
        page.locator('[data-testid="share-toggle-infisical"]').click()
        page.wait_for_function(
            """() => { const t = document.querySelector('[data-testid=share-toggle-infisical]');
                       return t && t.getAttribute('aria-checked') === 'true'; }""",
            timeout=8000,
        )
        st, dv = _api("GET", "/api/deploy")
        want = os.path.join(HOME, ".infisical")
        check("AC-S41bdf2-2-1 share wrote auth path to shared_dirs (deploy single source)",
              want in dv.get("workspace", {}).get("sharedDirs", []),
              extra=str(dv.get("workspace", {}).get("sharedDirs", [])))

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
