"""S41bdf2 — app card model GUI *mock* test (Playwright + page.route).

Complements the real-backend E2E (s41bdf2_app_cards.py). Covers the state-machine
states that can't be force-triggered against a live dev backend (installing / error
/ shared / 未install→share greyed) plus the add-row inline nixpkgs validation, with
the backend fully mocked so the frontend state machine is exercised deterministically.

  [AC-S41bdf2-3-4] Empty/Populated — GET /api/apps renders the catalog cards
  [AC-S41bdf2-3-4] Installing       — a card in state=installing shows the spinner
  [AC-S41bdf2-3-4] Error            — a failed card shows app-error + retry
  [AC-S41bdf2-3-4] Shared           — an installed+shared card shows both toggles on
  [AC-S41bdf2-2-2] 未install greyed  — share toggle aria-disabled when not installed
  [AC-S41bdf2-3-2] rebuild-boundary — install=要rebuild chip / 共有=hot chip
  [AC-S41bdf2-1-5] add validation   — valid ✓ enables 追加; invalid ⚠ disables it

Run against any dev instance serving the frontend:
    PALMUX2_DEV_PORT=<port> python tests/e2e/s41bdf2_app_cards_mock.py
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
    "nixOSHost": True, "rebuildRunning": True, "home": HOME,
    "apps": [
        _card("infisical", "Infisical", "infisical", "~/.infisical", True, True, "shared"),
        _card("1password-cli", "1Password CLI", "_1password-cli", "~/.config/op", True, False, "installed"),
        _card("gh", "GitHub CLI", "gh", "~/.config/gh", False, False, "available"),
        _card("awscli2", "AWS CLI", "awscli2", "~/.aws", False, False, "installing"),
        _card("terraform", "Terraform", "terraform", "", False, False, "error", custom=True,
              error="nixos-rebuild switch が失敗しました（旧世代を維持）"),
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

        def route_deploy(route):
            route.fulfill(status=200, content_type="application/json", body=json.dumps(DEPLOY_VIEW))

        def route_apps(route):
            route.fulfill(status=200, content_type="application/json", body=json.dumps(APPS_VIEW))

        def route_validate(route):
            pkg = ""
            try:
                pkg = (route.request.post_data_json or {}).get("package", "")
            except Exception:
                pass
            if pkg == "ripgrep":
                body = {"package": pkg, "valid": True, "unavailable": False, "message": f"nixpkgs#{pkg} 解決 OK (ripgrep-14)"}
            else:
                body = {"package": pkg, "valid": False, "unavailable": False, "message": f"nixpkgs に {pkg!r} が見つかりません"}
            route.fulfill(status=200, content_type="application/json", body=json.dumps(body))

        page.route("**/api/deploy", route_deploy)
        page.route("**/api/apps/validate", route_validate)
        page.route("**/api/apps", route_apps)

        page.goto(BASE, wait_until="domcontentloaded")
        page.wait_for_timeout(800)
        open_deploy_tab(page)

        page.wait_for_selector('[data-testid="apps-grid"]', timeout=6000)
        page.locator('[data-testid="app-card-infisical"]').scroll_into_view_if_needed()

        # Populated: five catalog/custom cards.
        check("AC-S41bdf2-3-4 Populated: apps-grid has cards",
              page.locator('[data-testid^="app-card-"]').count() >= 5)

        # rebuild-boundary chips (install=要rebuild, 共有=hot) present on a card.
        inf = page.locator('[data-testid="app-card-infisical"]')
        check("AC-S41bdf2-3-2 rebuild-boundary chips present",
              "要 rebuild" in inf.inner_text() and "hot" in inf.inner_text())

        # Shared: infisical install ON + share ON.
        check("AC-S41bdf2-3-4 Shared: install+share toggles both on",
              page.locator('[data-testid="install-toggle-infisical"]').get_attribute("aria-checked") == "true"
              and page.locator('[data-testid="share-toggle-infisical"]').get_attribute("aria-checked") == "true")

        # 未install greyed: gh share toggle aria-disabled (従属).
        gh_share = page.locator('[data-testid="share-toggle-gh"]')
        check("AC-S41bdf2-2-2 未install: gh share toggle greyed (aria-disabled)",
              gh_share.get_attribute("aria-disabled") == "true"
              and page.locator('[data-testid="install-toggle-gh"]').get_attribute("aria-checked") == "false")

        # Installing: awscli2 shows the spinner/progress.
        check("AC-S41bdf2-3-4 Installing: awscli2 shows install progress",
              page.locator('[data-testid="install-progress-awscli2"]').count() > 0)

        # Error: terraform shows app-error + retry.
        check("AC-S41bdf2-3-4 Error: terraform shows app-error",
              page.locator('[data-testid="app-error-terraform"]').count() > 0
              and page.locator('[data-testid="app-rollback-terraform"]').count() > 0)

        # Add-row validation: invalid ⚠ disables 追加; valid ✓ enables it.
        addinp = page.locator('[data-testid="app-add-input"]')
        addinp.scroll_into_view_if_needed()
        addinp.fill("notarealpkg")
        page.wait_for_timeout(900)
        validity = page.locator('[data-testid="app-add-validity"]')
        check("AC-S41bdf2-1-5 invalid: ⚠ shown + 追加 disabled",
              validity.get_attribute("data-state") == "invalid"
              and page.locator('[data-testid="app-add-btn"]').is_disabled())

        addinp.fill("ripgrep")
        page.wait_for_timeout(900)
        check("AC-S41bdf2-1-5 valid: ✓ shown + 追加 enabled",
              validity.get_attribute("data-state") == "valid"
              and not page.locator('[data-testid="app-add-btn"]').is_disabled())

        browser.close()

    print()
    if failures:
        print(f"FAILED: {len(failures)} check(s): {failures}")
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
