#!/usr/bin/env python3
"""Sprint S7364e3 Story 2 — Workspace image-update UI (REAL server + browser).

Real-mode E2E: a real browser drives the real palmux UI against a real incus
container. Requires a host with BOTH incus AND playwright (open-auth dev
instance). On the SSO deploy VM (where playwright-python can't run) this flow is
validated manually via a remote browser through SSO — see
docs/sprint-logs/S7364e3/decisions.json.

Acceptance criteria:
  [AC-S7364e3-2-1] a stale incus Workspace shows an "update available" badge on
                   the header runtime chip
  [AC-S7364e3-2-2] update action → confirm modal → updating → ready; badge clears
  [AC-S7364e3-2-4] host / fresh incus → no badge

To create a stale fixture: rebuild + re-alias palmux-ws AFTER the workspace
container was created (or temporarily re-alias palmux-ws to an older
fingerprint).

Run:
  PALMUX2_DEV_PORT=8080 python3 tests/e2e/s7364e3_image_update_ui.py
"""
from __future__ import annotations

import os
import sys
import time

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8080"
)
BASE_URL = f"http://localhost:{PORT}"
PLAYWRIGHT_TIMEOUT = 20_000

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def get_playwright():
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("FAIL: playwright not installed", file=sys.stderr)
        sys.exit(1)
    return sync_playwright


def api_get(path: str):
    import json
    import urllib.request
    req = urllib.request.Request(f"{BASE_URL}{path}", headers={"Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=15) as resp:
        return json.loads(resp.read())


def find_stale_incus() -> tuple[str, str, bool] | None:
    """Return (repoId, branchId, stale) of an open incus workspace, preferring a
    stale one."""
    repos = api_get("/api/repos")
    first = None
    for repo in repos:
        for b in (repo.get("openBranches") or []):
            rt = b.get("runtime") or {}
            if rt.get("kind") != "incus-container":
                continue
            entry = (repo["id"], b["id"], bool(rt.get("stale")))
            if first is None:
                first = entry
            if rt.get("stale"):
                return entry
    return first


def main() -> int:
    ws = find_stale_incus()
    if ws is None:
        print("SKIP: no open incus-container Workspace. Real-mode UI test needs "
              "one (ideally stale — re-alias palmux-ws to an older fingerprint).",
              file=sys.stderr)
        return 0
    repo_id, branch_id, stale = ws
    sync_playwright = get_playwright()
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True, args=["--no-sandbox"])
        ctx = browser.new_context(viewport={"width": 1280, "height": 800})
        page = ctx.new_page()
        try:
            page.goto(f"{BASE_URL}/{repo_id}/{branch_id}/claude",
                      timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
            page.wait_for_selector("[data-testid='runtime-chip']", timeout=PLAYWRIGHT_TIMEOUT)

            if not stale:
                # AC-2-4: fresh container shows no badge.
                if page.locator("[data-testid='workspace-update-badge']").count() > 0:
                    fail("AC-S7364e3-2-4", "update badge shown for a non-stale container")
                else:
                    ok("AC-S7364e3-2-4", "fresh incus container shows no update badge")
                return 1 if _FAILED else 0

            # AC-2-1: stale → badge present.
            badge = page.wait_for_selector("[data-testid='workspace-update-badge']",
                                           timeout=PLAYWRIGHT_TIMEOUT)
            if not badge.is_visible():
                fail("AC-S7364e3-2-1", "update badge not visible on stale workspace")
                return 1
            ok("AC-S7364e3-2-1", "stale incus workspace shows update badge")

            # AC-2-2: chip → update action → confirm → regenerate → ready, badge clears.
            page.click("[data-testid='runtime-chip']")
            page.wait_for_selector("[data-testid='runtime-chip-menu']", timeout=PLAYWRIGHT_TIMEOUT)
            page.click("[data-testid='runtime-update-action']")
            page.wait_for_selector("[data-testid='update-container-confirm']", timeout=PLAYWRIGHT_TIMEOUT)
            page.click("[data-testid='update-container-confirm-ok']")

            # Regeneration is slow (probe + recreate); wait for the badge to clear.
            try:
                page.wait_for_selector("[data-testid='workspace-update-badge']",
                                       state="detached", timeout=120_000)
            except Exception:  # noqa: BLE001
                fail("AC-S7364e3-2-2", "update badge did not clear after regenerate")
                return 1
            # Chip returns to ready.
            deadline = time.time() + 30
            state = None
            while time.time() < deadline:
                state = page.locator("[data-testid='runtime-chip']").first.get_attribute("data-runtime-state")
                if state == "ready":
                    break
                time.sleep(2)
            if state != "ready":
                fail("AC-S7364e3-2-2", f"chip not ready after regenerate (state={state})")
                return 1
            ok("AC-S7364e3-2-2", "update flow: action → confirm → regenerate → ready, badge cleared")
        finally:
            ctx.close()
            browser.close()

    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
