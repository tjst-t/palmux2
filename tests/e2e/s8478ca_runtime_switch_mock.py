#!/usr/bin/env python3
"""Sprint S8478ca-refine — in-place runtime switch via header chip (MOCK).

Frontend-only Playwright tests for the header runtime-chip popover.
All API/WS calls are intercepted so the test runs against any dev instance
without needing a real Incus installation.

Acceptance criteria:
  [AC-refine-1] Header runtime-chip is clickable for a non-host workspace;
                clicking opens a popover menu (data-testid=runtime-chip-menu)
                listing host / incus-container options.
  [AC-refine-2] Selecting a DIFFERENT kind from the menu opens the
                RuntimeChangeConfirm modal
                (data-testid=runtime-change-confirm).
  [AC-refine-3] Confirming fires PATCH .../runtime with {kind: ...}.
  [AC-refine-4] Cancelling the modal closes it without firing a PATCH.
  [AC-refine-5] PATCH failure surfaces an inline error
                (data-testid=runtime-selector-error) in the chip menu area.
  [AC-refine-6] incus-container option is disabled when GET /api/runtimes
                reports incus unavailable.
  [AC-refine-7] Host login scope (host--0000) chip is non-interactive
                (no runtime-chip-menu).

Exit code 0 = ALL PASS. Requires a running dev instance:
  make serve INSTANCE=dev
  python3 tests/e2e/s8478ca_runtime_switch_mock.py
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
PLAYWRIGHT_TIMEOUT = 15_000  # ms

HOST_REPO = "host--0000"
FAKE_REPO = "demo--repo--ab12"
FAKE_BRANCH = "feature--cd34"

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


def _fake_repo_response(kind: str = "host", state: str = "ready") -> dict:
    return {
        "id": FAKE_REPO,
        "ghqPath": "demo/repo",
        "fullPath": "/tmp/demo-repo",
        "starred": False,
        "openBranches": [{
            "id": FAKE_BRANCH,
            "name": "feature",
            "worktreePath": "/tmp/demo-repo",
            "repoId": FAKE_REPO,
            "isPrimary": True,
            "lastActivity": "2026-01-01T00:00:00Z",
            "tabSet": {
                "tmuxSession": "_palmux_demo--repo--ab12_feature--cd34",
                "tabs": [
                    {"id": "claude", "type": "claude", "name": "Claude",
                     "protected": True, "multiple": False, "windowName": "palmux:claude:claude"},
                    {"id": "bash:bash", "type": "bash", "name": "Bash",
                     "protected": False, "multiple": True, "windowName": "palmux:bash:bash"},
                ],
            },
            "runtime": {"kind": kind, "state": state, "address": "localhost"},
        }],
    }


def _navigate_to_workspace(page) -> None:
    """Navigate to the fake workspace URL."""
    page.goto(
        f"{BASE_URL}/{FAKE_REPO}/{FAKE_BRANCH}/claude",
        timeout=PLAYWRIGHT_TIMEOUT,
        wait_until="load",
    )
    # Wait for the runtime chip to be present
    page.wait_for_selector("[data-testid='runtime-chip']", timeout=PLAYWRIGHT_TIMEOUT)


def _mock_apis(page, *, incus_available: bool = True, kind: str = "host") -> None:
    """Intercept all the APIs needed for the header chip to work."""
    page.route("**/api/runtimes", lambda r: _fulfill(r, {
        "kinds": [
            {"kind": "host", "available": True},
            {"kind": "incus-container", "available": incus_available,
             "reason": "" if incus_available else "Incus is not installed on this host"},
        ]
    }))
    page.route(f"**/api/repos/{FAKE_REPO}", lambda r: _fulfill(r, _fake_repo_response(kind=kind)))
    page.route("**/api/repos", lambda r: (
        _fulfill(r, [_fake_repo_response(kind=kind)])
        if r.request.method == "GET" else r.continue_()
    ))


# ─── AC-refine-1: chip click opens menu ────────────────────────────────────────

def test_ac_refine_1_chip_opens_menu(page) -> None:
    name = "AC-refine-1"
    _mock_apis(page, kind="host")
    _navigate_to_workspace(page)

    chip = page.locator("[data-testid='runtime-chip']")
    if chip.count() < 1:
        fail(name, "runtime-chip not found in header")
        return
    chip.first.click()
    menu = page.wait_for_selector("[data-testid='runtime-chip-menu']",
                                  timeout=PLAYWRIGHT_TIMEOUT)
    if not menu:
        fail(name, "runtime-chip-menu did not appear after click")
        return
    # Menu should offer host option at minimum
    if page.locator("[data-testid='runtime-option-host']").count() < 1:
        fail(name, "runtime-option-host not present in menu")
        return
    ok(name, "runtime-chip opened menu with runtime options")


# ─── AC-refine-2: select different kind → confirm modal ───────────────────────

def test_ac_refine_2_select_opens_confirm(page) -> None:
    name = "AC-refine-2"
    _mock_apis(page, kind="host", incus_available=True)
    _navigate_to_workspace(page)

    page.locator("[data-testid='runtime-chip']").first.click()
    page.wait_for_selector("[data-testid='runtime-chip-menu']", timeout=PLAYWRIGHT_TIMEOUT)
    # Click incus-container (different from current kind=host)
    page.locator("[data-testid='runtime-option-incus-container']").first.click()
    confirm = page.wait_for_selector("[data-testid='runtime-change-confirm']",
                                     timeout=PLAYWRIGHT_TIMEOUT)
    if not confirm:
        fail(name, "RuntimeChangeConfirm modal did not appear")
        return
    # Verify confirm-ok button text mentions restart / new kind
    ok_btn = page.locator("[data-testid='runtime-change-confirm-ok']")
    if ok_btn.count() < 1:
        fail(name, "runtime-change-confirm-ok button missing")
        return
    ok_text = (ok_btn.first.text_content() or "").lower()
    if "restart" not in ok_text and "change" not in ok_text and "incus" not in ok_text:
        fail(name, f"confirm-ok button text unexpected: {ok_text!r}")
        return
    ok(name, "selecting different kind opened RuntimeChangeConfirm modal")


# ─── AC-refine-3: confirm fires PATCH ──────────────────────────────────────────

def test_ac_refine_3_confirm_fires_patch(page) -> None:
    name = "AC-refine-3"
    # Register the broad catch-all FIRST so that the more-specific routes added
    # by _mock_apis() (registered after) take priority in Playwright's route
    # matching (latest-registered route wins for a given request).
    patched_body: list[dict] = []

    def capture_patch(route):
        if route.request.method == "PATCH" and "runtime" in route.request.url:
            try:
                patched_body.append(json.loads(route.request.post_data or "{}"))
            except Exception:
                pass
            _fulfill(route, {"ok": True, "restarted": True,
                              "runtime": {"kind": "incus-container", "state": "ready",
                                          "address": "10.42.0.1"}})
        else:
            route.continue_()

    page.route("**", capture_patch)
    _mock_apis(page, kind="host", incus_available=True)
    _navigate_to_workspace(page)

    page.locator("[data-testid='runtime-chip']").first.click()
    page.wait_for_selector("[data-testid='runtime-chip-menu']", timeout=PLAYWRIGHT_TIMEOUT)
    page.locator("[data-testid='runtime-option-incus-container']").first.click()
    page.wait_for_selector("[data-testid='runtime-change-confirm']", timeout=PLAYWRIGHT_TIMEOUT)
    page.locator("[data-testid='runtime-change-confirm-ok']").first.click()

    # Wait a moment for the PATCH to fire
    deadline = time.time() + 3.0
    while time.time() < deadline and not patched_body:
        time.sleep(0.1)

    if not patched_body:
        fail(name, "PATCH not fired after confirm-ok click")
        return
    if patched_body[0].get("kind") != "incus-container":
        fail(name, f"PATCH body unexpected: {patched_body[0]!r}")
        return
    ok(name, f"PATCH fired with correct body: {patched_body[0]!r}")


# ─── AC-refine-4: cancel closes modal without PATCH ───────────────────────────

def test_ac_refine_4_cancel_no_patch(page) -> None:
    name = "AC-refine-4"
    # Broad catch-all registered FIRST; specific API mocks registered after
    # (later = higher priority in Playwright route matching).
    patch_count = [0]

    def count_patch(route):
        if route.request.method == "PATCH" and "runtime" in route.request.url:
            patch_count[0] += 1
        route.continue_()

    page.route("**", count_patch)
    _mock_apis(page, kind="host", incus_available=True)
    _navigate_to_workspace(page)

    page.locator("[data-testid='runtime-chip']").first.click()
    page.wait_for_selector("[data-testid='runtime-chip-menu']", timeout=PLAYWRIGHT_TIMEOUT)
    page.locator("[data-testid='runtime-option-incus-container']").first.click()
    page.wait_for_selector("[data-testid='runtime-change-confirm']", timeout=PLAYWRIGHT_TIMEOUT)
    page.locator("[data-testid='runtime-change-confirm-cancel']").first.click()

    # Modal should be gone
    time.sleep(0.3)
    if page.locator("[data-testid='runtime-change-confirm']").count() > 0:
        fail(name, "confirm modal still present after cancel")
        return
    if patch_count[0] > 0:
        fail(name, f"PATCH fired despite cancel: {patch_count[0]} calls")
        return
    ok(name, "cancel closed modal without firing PATCH")


# ─── AC-refine-5: PATCH failure shows inline error ─────────────────────────────

def test_ac_refine_5_patch_failure_inline_error(page) -> None:
    name = "AC-refine-5"
    # Broad catch-all registered FIRST; specific API mocks registered after
    # (later = higher priority in Playwright route matching).

    def handle(route):
        if route.request.method == "PATCH" and "runtime" in route.request.url:
            _fulfill(route, {"error": "failed to start incus container"}, status=500)
        else:
            route.continue_()

    page.route("**", handle)
    _mock_apis(page, kind="host", incus_available=True)
    _navigate_to_workspace(page)

    page.locator("[data-testid='runtime-chip']").first.click()
    page.wait_for_selector("[data-testid='runtime-chip-menu']", timeout=PLAYWRIGHT_TIMEOUT)
    page.locator("[data-testid='runtime-option-incus-container']").first.click()
    page.wait_for_selector("[data-testid='runtime-change-confirm']", timeout=PLAYWRIGHT_TIMEOUT)
    page.locator("[data-testid='runtime-change-confirm-ok']").first.click()

    err = page.wait_for_selector("[data-testid='runtime-selector-error']",
                                 timeout=PLAYWRIGHT_TIMEOUT)
    err_text = (err.text_content() or "").strip()
    if not err_text:
        fail(name, "inline error not shown after PATCH 500")
        return
    ok(name, f"inline error shown after PATCH failure: {err_text!r}")


# ─── AC-refine-5b: HTTP 200 + restartError (silent rollback) shows inline error ─

def test_ac_refine_5b_restart_error_inline_error(page) -> None:
    """[AC-refine-5] A switch that PERSISTED but failed to restart in-place
    returns HTTP 200 with {restarted:false, restartError:"..."} and rolls the
    workspace back to its previous runtime. This must NOT look like success:
    the FE must surface the restartError as an inline error, exactly like the
    500 path. Regression guard for the silent-rollback bug (incus-admin perm
    failure → switch quietly fell back to host with no user-visible error)."""
    name = "AC-refine-5b"

    def handle(route):
        if route.request.method == "PATCH" and "runtime" in route.request.url:
            # 200, but the in-place restart failed and rolled back to host.
            _fulfill(route, {
                "ok": True,
                "restarted": False,
                "restartError": "incus init palmux-ws: permission denied talking to incus daemon",
                "runtime": {"kind": "host", "state": "ready", "address": "localhost"},
            }, status=200)
        else:
            route.continue_()

    page.route("**", handle)
    _mock_apis(page, kind="host", incus_available=True)
    _navigate_to_workspace(page)

    page.locator("[data-testid='runtime-chip']").first.click()
    page.wait_for_selector("[data-testid='runtime-chip-menu']", timeout=PLAYWRIGHT_TIMEOUT)
    page.locator("[data-testid='runtime-option-incus-container']").first.click()
    page.wait_for_selector("[data-testid='runtime-change-confirm']", timeout=PLAYWRIGHT_TIMEOUT)
    page.locator("[data-testid='runtime-change-confirm-ok']").first.click()

    err = page.wait_for_selector("[data-testid='runtime-selector-error']",
                                 timeout=PLAYWRIGHT_TIMEOUT)
    err_text = (err.text_content() or "").strip()
    if not err_text:
        fail(name, "inline error not shown after 200+restartError (silent rollback)")
        return
    if "permission denied" not in err_text and "kept on its previous runtime" not in err_text:
        fail(name, f"error text does not convey the failure: {err_text!r}")
        return
    ok(name, f"inline error shown after silent-rollback 200: {err_text!r}")


# ─── AC-refine-6: incus unavailable → option disabled ────────────────────────

def test_ac_refine_6_incus_unavailable(page) -> None:
    name = "AC-refine-6"
    _mock_apis(page, kind="host", incus_available=False)
    _navigate_to_workspace(page)

    page.locator("[data-testid='runtime-chip']").first.click()
    page.wait_for_selector("[data-testid='runtime-chip-menu']", timeout=PLAYWRIGHT_TIMEOUT)

    # The incus-container option should exist but be disabled
    incus_btn = page.locator("[data-testid='runtime-option-incus-container']")
    if incus_btn.count() < 1:
        fail(name, "runtime-option-incus-container not in menu")
        return
    disabled = incus_btn.first.get_attribute("disabled")
    if disabled is None:
        fail(name, "incus-container option not disabled when incus unavailable")
        return
    ok(name, "incus-container disabled in chip menu when Incus unavailable")


# ─── AC-refine-7: Host login scope chip is non-interactive ────────────────────

def test_ac_refine_7_host_scope_no_chip(page) -> None:
    name = "AC-refine-7"
    page.route("**/api/runtimes", lambda r: _fulfill(r, {
        "kinds": [
            {"kind": "host", "available": True},
            {"kind": "incus-container", "available": True},
        ]
    }))
    page.goto(
        f"{BASE_URL}/{HOST_REPO}/host/bash:bash",
        timeout=PLAYWRIGHT_TIMEOUT,
        wait_until="load",
    )
    page.wait_for_selector("[data-testid='host-scope-label']", timeout=PLAYWRIGHT_TIMEOUT)
    # Host scope must not show an interactive runtime chip
    if page.locator("[data-testid='runtime-chip']").count() > 0:
        fail(name, "Host login scope must not show a runtime-chip")
        return
    if page.locator("[data-testid='runtime-chip-menu']").count() > 0:
        fail(name, "Host login scope must not show a runtime-chip-menu")
        return
    ok(name, "Host login scope: no interactive runtime chip")


def main() -> int:
    sync_playwright = get_playwright()
    tests = [
        test_ac_refine_1_chip_opens_menu,
        test_ac_refine_2_select_opens_confirm,
        test_ac_refine_3_confirm_fires_patch,
        test_ac_refine_4_cancel_no_patch,
        test_ac_refine_5_patch_failure_inline_error,
        test_ac_refine_5b_restart_error_inline_error,
        test_ac_refine_6_incus_unavailable,
        test_ac_refine_7_host_scope_no_chip,
    ]
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            for tc in tests:
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
