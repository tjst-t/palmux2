#!/usr/bin/env python3
"""Sprint S7364e3 Story 2 — Workspace image-update UI (MOCK, frontend-only).

The real frontend is served by the dev instance; the runtime endpoints
(GET /api/runtimes, GET /api/repos[/id], POST .../runtime/regenerate) are
intercepted via Playwright routing so we can drive drift states a stock dev host
cannot produce for real:
  - stale incus container → "update available" badge on chip + drawer
  - update action → confirm modal → progress (updating…) → cleared badge
  - update failure → inline error, container kept (badge stays)
  - host / fresh incus → no badge

Runs during `sprint run` (mock gate). Real-server companion:
s7364e3_image_update_ui.py (sprint verify / real-mode UI validation).

Acceptance criteria (edge/error/state):
  [AC-S7364e3-2-1] stale incus Workspace shows an "update available" badge
  [AC-S7364e3-2-2] update action → confirm → updating→ready progression
  [AC-S7364e3-2-3] update failure → error shown, container kept
  [AC-S7364e3-2-4] host / fresh incus → no badge

Run:  make serve INSTANCE=dev ; PALMUX2_DEV_PORT=<port> \
      python3 tests/e2e/s7364e3_image_update_ui_mock.py
"""
from __future__ import annotations

import json
import os
import sys
import time

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8215"
)
BASE_URL = f"http://localhost:{PORT}"
PLAYWRIGHT_TIMEOUT = 15_000

REPO_ID = "demo--repo--ab12"
BRANCH_ID = "feature--cd34"

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


def _fulfill(route, obj, status=200):
    route.fulfill(status=status, content_type="application/json", body=json.dumps(obj))


def _repo(kind="incus-container", state="ready", stale=False) -> dict:
    return {
        "id": REPO_ID,
        "ghqPath": "demo/repo",
        "fullPath": "/tmp/demo-repo",
        "starred": False,
        "openBranches": [{
            "id": BRANCH_ID,
            "name": "feature",
            "worktreePath": "/tmp/demo-repo",
            "repoId": REPO_ID,
            "isPrimary": True,
            "lastActivity": "2026-01-01T00:00:00Z",
            "tabSet": {
                "tmuxSession": f"_palmux_{REPO_ID}_{BRANCH_ID}",
                "tabs": [
                    {"id": "claude", "type": "claude", "name": "Claude",
                     "protected": True, "multiple": False, "windowName": "palmux:claude:claude"},
                    {"id": "bash:bash", "type": "bash", "name": "Bash",
                     "protected": False, "multiple": True, "windowName": "palmux:bash:bash"},
                ],
            },
            "runtime": {"kind": kind, "state": state, "address": "10.42.0.5", "stale": stale},
        }],
    }


def _wire_common(page, kind="incus-container", stale=True):
    """Route caps + repo list + single repo so the Header renders the chip."""
    page.route("**/api/runtimes", lambda r: _fulfill(r, {
        "kinds": [{"kind": "host", "available": True},
                  {"kind": "incus-container", "available": True}]}))
    page.route("**/api/repos", lambda r: _fulfill(r, [_repo(kind, stale=stale)]))
    page.route(f"**/api/repos/{REPO_ID}", lambda r: _fulfill(r, _repo(kind, stale=stale)))


def _open_chip_menu(page):
    chip = page.wait_for_selector("[data-testid='runtime-chip']", timeout=PLAYWRIGHT_TIMEOUT)
    chip.click()
    page.wait_for_selector("[data-testid='runtime-chip-menu']", timeout=PLAYWRIGHT_TIMEOUT)


# ─── AC-2-1: stale → update badge ─────────────────────────────────────────────

def test_ac1_badge_when_stale(page) -> None:
    name = "AC-S7364e3-2-1"
    _wire_common(page, stale=True)
    page.goto(f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/claude",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    badge = page.wait_for_selector("[data-testid='workspace-update-badge']",
                                   timeout=PLAYWRIGHT_TIMEOUT)
    if not badge.is_visible():
        fail(name, "update badge not visible for stale incus workspace")
        return
    chip = page.locator("[data-testid='runtime-chip']").first
    if chip.get_attribute("data-update-available") != "true":
        fail(name, "runtime chip missing data-update-available=true")
        return
    ok(name, "stale incus workspace shows 'update available' badge on chip")


# ─── AC-2-4: fresh / host → no badge ──────────────────────────────────────────

def test_ac4_no_badge_when_fresh(page) -> None:
    name = "AC-S7364e3-2-4"
    _wire_common(page, stale=False)
    page.goto(f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/claude",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector("[data-testid='runtime-chip']", timeout=PLAYWRIGHT_TIMEOUT)
    if page.locator("[data-testid='workspace-update-badge']").count() > 0:
        fail(name, "update badge shown for a fresh (stale=false) container")
        return
    ok(name, "fresh container shows no update badge")


def test_ac4_no_badge_for_host(page) -> None:
    name = "AC-S7364e3-2-4"
    # host runtime with stale=true in payload must STILL not show the badge.
    page.route("**/api/runtimes", lambda r: _fulfill(r, {
        "kinds": [{"kind": "host", "available": True},
                  {"kind": "incus-container", "available": True}]}))
    host_repo = _repo("host", stale=True)
    page.route("**/api/repos", lambda r: _fulfill(r, [host_repo]))
    page.route(f"**/api/repos/{REPO_ID}", lambda r: _fulfill(r, host_repo))
    page.goto(f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/claude",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector("[data-testid='runtime-chip']", timeout=PLAYWRIGHT_TIMEOUT)
    if page.locator("[data-testid='workspace-update-badge']").count() > 0:
        fail(name, "update badge shown for a host runtime (must be incus-only)")
        return
    ok(name, "host runtime never shows update badge (incus-only)")


# ─── AC-2-2: action → confirm → updating → ready ──────────────────────────────

def test_ac2_update_flow_progress(page) -> None:
    name = "AC-S7364e3-2-2"
    # Stateful mock: the container starts stale; after a successful regenerate
    # the backend would report it fresh, so subsequent repo fetches must too
    # (otherwise a re-fetch re-staleness the optimistic clear).
    box = {"stale": True}
    page.route("**/api/runtimes", lambda r: _fulfill(r, {
        "kinds": [{"kind": "host", "available": True},
                  {"kind": "incus-container", "available": True}]}))
    page.route("**/api/repos", lambda r: _fulfill(r, [_repo(stale=box["stale"])]))
    page.route(f"**/api/repos/{REPO_ID}", lambda r: _fulfill(r, _repo(stale=box["stale"])))

    # Regenerate POST: delay so the chip's "updating…" progress is observable,
    # flip the fixture to fresh, then return a FRESH (stale=false) runtime view.
    def regen(route):
        time.sleep(1.2)
        box["stale"] = False
        _fulfill(route, {"ok": True, "regenerated": True,
                         "runtime": {"kind": "incus-container", "state": "ready",
                                     "address": "10.42.0.9", "stale": False}})
    page.route("**/runtime/regenerate", regen)

    page.goto(f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/claude",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    _open_chip_menu(page)
    action = page.wait_for_selector("[data-testid='runtime-update-action']", timeout=PLAYWRIGHT_TIMEOUT)
    action.click()
    # Confirm modal appears and warns about session restart.
    modal = page.wait_for_selector("[data-testid='update-container-confirm']", timeout=PLAYWRIGHT_TIMEOUT)
    if "resume" not in (modal.text_content() or "").lower():
        fail(name, "confirm modal does not mention claude --resume / session restart")
        return
    page.click("[data-testid='update-container-confirm-ok']")

    # During the delayed POST the chip shows the 'updating…' progress (starting).
    chip = page.locator("[data-testid='runtime-chip']").first
    saw_updating = False
    deadline = time.time() + 3
    while time.time() < deadline:
        if chip.get_attribute("data-runtime-state") == "starting":
            saw_updating = True
            break
        time.sleep(0.1)
    if not saw_updating:
        fail(name, "chip never showed 'updating…' (starting) progress during regenerate")
        return

    # After success, the badge clears (stale=false). There are two badges (chip
    # + drawer) sharing the testid, so poll the count to zero rather than a
    # single-element detach.
    cleared = False
    deadline = time.time() + 10
    while time.time() < deadline:
        if page.locator("[data-testid='workspace-update-badge']").count() == 0:
            cleared = True
            break
        time.sleep(0.2)
    if not cleared:
        fail(name, "update badge did not clear after a successful regenerate")
        return
    ok(name, "update: action → confirm → updating → ready, badge cleared")


# ─── AC-2-3: update failure → error, container kept ───────────────────────────

def test_ac3_update_failure_keeps_container(page) -> None:
    name = "AC-S7364e3-2-3"
    _wire_common(page, stale=True)
    # Server returns 200 with updateError (rollback — old container kept).
    page.route("**/runtime/regenerate", lambda r: _fulfill(r, {
        "ok": False, "regenerated": False,
        "updateError": "new image not launchable, kept existing container",
        "runtime": {"kind": "incus-container", "state": "ready",
                    "address": "10.42.0.5", "stale": True}}))

    page.goto(f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/claude",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    _open_chip_menu(page)
    page.click("[data-testid='runtime-update-action']")
    page.wait_for_selector("[data-testid='update-container-confirm']", timeout=PLAYWRIGHT_TIMEOUT)
    page.click("[data-testid='update-container-confirm-ok']")

    err = page.wait_for_selector("[data-testid='update-container-error']", timeout=PLAYWRIGHT_TIMEOUT)
    if not (err.text_content() or "").strip():
        fail(name, "update failure did not surface an inline error")
        return
    # Container kept → badge still present (stale stayed true).
    if page.locator("[data-testid='workspace-update-badge']").count() < 1:
        fail(name, "update badge disappeared on failure (container should be kept)")
        return
    ok(name, "update failure: inline error shown, badge kept (container intact)")


def main() -> int:
    sync_playwright = get_playwright()
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            for tc in (test_ac1_badge_when_stale,
                       test_ac4_no_badge_when_fresh,
                       test_ac4_no_badge_for_host,
                       test_ac2_update_flow_progress,
                       test_ac3_update_failure_keeps_container):
                ctx = browser.new_context(viewport={"width": 1280, "height": 800})
                page = ctx.new_page()
                try:
                    tc(page)
                except Exception as e:  # noqa: BLE001
                    fail(tc.__name__, f"unexpected: {e}")
                finally:
                    ctx.close()
        finally:
            browser.close()

    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
