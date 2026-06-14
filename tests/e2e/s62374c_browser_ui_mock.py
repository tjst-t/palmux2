#!/usr/bin/env python3
"""Sprint S62374c — Browser tab (MOCK, frontend-only Playwright). noVNC rework.

All API/WS calls are intercepted so this runs against any dev instance without
a real Incus container or VNC connection.

noVNC rework: the URL bar / back / forward / reload / Go / keycapture testids
are removed. Navigation is handled by Chromium's own UI inside the noVNC
viewport. The WS attach endpoint speaks raw RFB binary (aborted in mock).

Acceptance criteria:
  [AC-S62374c-2-4]  Start/Stop button + state badge follow the server state.
  [AC-S62374c-2-7]  State transitions match the diagram (stopped→starting→running).
  [AC-S62374c-2-8]  All required data-testid values are present.
  [AC-S62374c-2-9]  Mock verifies stopped/starting/host states.
  [AC-S62374c-2-10] ↗ Open (browser-popout) present when running; fullscreen
                    route (browser-fullscreen) renders standalone.

Run:  PALMUX2_DEV_PORT=<port> python3 tests/e2e/s62374c_browser_ui_mock.py
"""
from __future__ import annotations

import json
import os
import sys

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8215"
)
BASE_URL = f"http://localhost:{PORT}"
PLAYWRIGHT_TIMEOUT = 15_000

FAKE_REPO   = "demo--repo--ab12"
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
    route.fulfill(status=status, content_type="application/json",
                  body=json.dumps(obj))


def _fake_repo(runtime_kind: str = "incus-container") -> dict:
    """Always include the browser tab in the tabSet (BrowserView decides availability)."""
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
                "tmuxSession": f"_palmux_{FAKE_REPO}_{FAKE_BRANCH}",
                "tabs": [
                    {"id": "claude",    "type": "claude",  "name": "Claude",
                     "protected": True,  "multiple": False,
                     "windowName": "palmux:claude:claude"},
                    {"id": "browser",   "type": "browser", "name": "Browser",
                     "protected": False, "multiple": False, "windowName": ""},
                    {"id": "bash:bash", "type": "bash",    "name": "Bash",
                     "protected": False, "multiple": True,
                     "windowName": "palmux:bash:bash"},
                ],
            },
            "runtime": {
                "kind": runtime_kind,
                "state": "ready",
                "address": "10.146.187.15",
            },
        }],
    }


def _browser_state_payload(state: str = "stopped", cdp_reachable: bool = False,
                           available: bool = True) -> dict:
    return {"state": state, "cdpReachable": cdp_reachable, "available": available}


def _common_mocks(page, *, runtime_kind: str, browser_state: str,
                  available: bool = True) -> None:
    page.route("**/api/runtimes", lambda r: _fulfill(r, {
        "kinds": [
            {"kind": "host",            "available": True},
            {"kind": "incus-container", "available": True},
        ],
    }))
    fake_repo = _fake_repo(runtime_kind=runtime_kind)
    page.route(f"**/api/repos/{FAKE_REPO}", lambda r: _fulfill(r, fake_repo))
    page.route("**/api/repos",              lambda r: _fulfill(r, [fake_repo]))

    # NOTE: Playwright routes are matched in REVERSE registration order (last wins).
    # Register the catch-all FIRST so the specific routes below take priority.

    # Catch-all for browser tab endpoints — abort WS, respond to REST.
    def _browser_catch_all(r):
        req = r.request
        if req.method == "POST" and req.url.endswith("/start"):
            _fulfill(r, {"state": "running"})
        elif req.method == "POST" and req.url.endswith("/stop"):
            _fulfill(r, {"state": "stopped"})
        else:
            r.abort()

    page.route(
        f"**/api/repos/{FAKE_REPO}/branches/{FAKE_BRANCH}/browser/**",
        _browser_catch_all,
    )
    # Register state AFTER catch-all so it takes priority (last registered wins).
    page.route(
        f"**/api/repos/{FAKE_REPO}/branches/{FAKE_BRANCH}/browser/state",
        lambda r: _fulfill(r, _browser_state_payload(
            state=browser_state,
            cdp_reachable=(browser_state == "running"),
            available=available,
        )),
    )


def _goto_browser(page, *, wait_for: str = "browser-tab-panel") -> None:
    page.goto(
        f"{BASE_URL}/{FAKE_REPO}/{FAKE_BRANCH}/browser",
        timeout=PLAYWRIGHT_TIMEOUT, wait_until="load",
    )
    page.wait_for_selector(f"[data-testid='{wait_for}']",
                           timeout=PLAYWRIGHT_TIMEOUT)


# ─── Test cases ───────────────────────────────────────────────────────────────

def test_ac8_all_testids_present_stopped(page) -> None:
    """[AC-S62374c-2-8] All required data-testid values are present (stopped state).
    noVNC rework: url-input/go/back/forward/reload/keycapture removed.
    """
    name = "AC-S62374c-2-8/stopped"
    _common_mocks(page, runtime_kind="incus-container", browser_state="stopped")
    _goto_browser(page)

    required = [
        "browser-tab-panel",
        "browser-state-badge",
        "browser-stopped",
        "browser-start",
    ]
    for tid in required:
        if page.locator(f"[data-testid='{tid}']").count() < 1:
            fail(name, f"missing data-testid='{tid}' in stopped state")
            return
    ok(name, "all required testids present in stopped state")


def test_ac4_badge_stopped(page) -> None:
    """[AC-S62374c-2-4] state badge shows 'stopped' when browser is stopped."""
    name = "AC-S62374c-2-4/stopped-badge"
    _common_mocks(page, runtime_kind="incus-container", browser_state="stopped")
    _goto_browser(page)

    badge = page.locator("[data-testid='browser-state-badge']").first
    text = badge.text_content() or ""
    if "stopped" not in text.lower():
        fail(name, f"badge text={text!r}, expected 'stopped'")
        return
    ok(name, f"badge shows 'stopped' (got: {text.strip()!r})")


def test_ac4_start_button_visible_when_stopped(page) -> None:
    """[AC-S62374c-2-4] [Start] button visible when stopped."""
    name = "AC-S62374c-2-4/start-button"
    _common_mocks(page, runtime_kind="incus-container", browser_state="stopped")
    _goto_browser(page)

    if page.locator("[data-testid='browser-start']").count() < 1:
        fail(name, "browser-start button not visible when stopped")
        return
    ok(name, "[Start] button visible when stopped")


def test_ac7_stopped_to_starting_transition(page) -> None:
    """[AC-S62374c-2-7] Click Start → badge transitions to 'starting' then 'running'."""
    name = "AC-S62374c-2-7/start-transition"
    _common_mocks(page, runtime_kind="incus-container", browser_state="stopped")
    _goto_browser(page)

    # Override: after clicking Start, POST start returns "running".
    page.route(
        f"**/api/repos/{FAKE_REPO}/branches/{FAKE_BRANCH}/browser/start",
        lambda r: _fulfill(r, {"state": "running"}),
    )
    page.locator("[data-testid='browser-start']").first.click()

    # badge should transition; accept starting or running.
    try:
        page.wait_for_selector("[data-testid='browser-stop']", timeout=8000)
        ok(name, "running state reached: Stop button appeared")
    except Exception:  # noqa: BLE001
        badge_text = (page.locator("[data-testid='browser-state-badge']").first.text_content() or "")
        if "starting" in badge_text.lower() or "running" in badge_text.lower():
            ok(name, f"badge shows transitional state: {badge_text.strip()!r}")
        else:
            fail(name, f"state badge still shows stopped after clicking Start "
                       f"(badge={badge_text!r})")


def test_ac9_host_runtime_notice(page) -> None:
    """[AC-S62374c-2-9] browser-host-notice shown when available=false."""
    name = "AC-S62374c-2-9/host-notice"
    # Use host runtime kind + available=False to trigger the host-notice.
    _common_mocks(page, runtime_kind="host", browser_state="stopped", available=False)
    # Override state to return available=false explicitly.
    page.route(
        f"**/api/repos/{FAKE_REPO}/branches/{FAKE_BRANCH}/browser/state",
        lambda r: _fulfill(r, _browser_state_payload(available=False)),
    )
    _goto_browser(page)

    # Wait for available=false to propagate from the async fetchState().
    try:
        page.wait_for_selector("[data-testid='browser-host-notice']",
                               timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:  # noqa: BLE001
        fail(name, "browser-host-notice not shown when available=false")
        return
    ok(name, "browser-host-notice shown when available=false")


def test_ac10_popout_link_present_when_running(page) -> None:
    """[AC-S62374c-2-10] ↗ Open (browser-popout) present when running."""
    name = "AC-S62374c-2-10/popout-link"
    _common_mocks(page, runtime_kind="incus-container", browser_state="running")
    _goto_browser(page)

    # browserState starts as 'stopped', transitions to 'running' on fetchState() poll.
    # Wait for the popout link to appear (it only renders in running state).
    try:
        page.wait_for_selector("[data-testid='browser-popout']",
                               timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:  # noqa: BLE001
        fail(name, "browser-popout not found when running (timed out waiting)")
        return

    popout = page.locator("[data-testid='browser-popout']").first
    href = popout.get_attribute("href") or ""
    if "fullscreen" not in href:
        fail(name, f"browser-popout href={href!r} does not include 'fullscreen'")
        return
    target = popout.get_attribute("target") or ""
    if target != "_blank":
        fail(name, f"browser-popout target={target!r}, expected _blank")
        return
    ok(name, f"browser-popout present with href={href!r} target=_blank")


def test_ac10_fullscreen_route(page) -> None:
    """[AC-S62374c-2-10] Fullscreen route renders browser-fullscreen testid.
    noVNC rework: url-input/go/back controls are removed from fullscreen too.
    """
    name = "AC-S62374c-2-10/fullscreen-route"
    _common_mocks(page, runtime_kind="incus-container", browser_state="running")

    page.goto(
        f"{BASE_URL}/{FAKE_REPO}/{FAKE_BRANCH}/browser?view=fullscreen",
        timeout=PLAYWRIGHT_TIMEOUT, wait_until="load",
    )
    try:
        page.wait_for_selector("[data-testid='browser-fullscreen']",
                               timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:  # noqa: BLE001
        fail(name, "browser-fullscreen testid not found at ?view=fullscreen route")
        return
    # Verify essential controls exist in fullscreen view (noVNC: no url/go/back).
    for tid in ["browser-state-badge"]:
        if page.locator(f"[data-testid='{tid}']").count() < 1:
            fail(name, f"fullscreen missing data-testid='{tid}'")
            return
    ok(name, "browser-fullscreen renders standalone with controls")


def test_ac8_running_testids(page) -> None:
    """[AC-S62374c-2-8] All required testids present in running state.
    noVNC rework: url-input/go/back/forward/reload/keycapture removed.
    browser-viewport is a div (noVNC renders canvas inside it).
    """
    name = "AC-S62374c-2-8/running"
    _common_mocks(page, runtime_kind="incus-container", browser_state="running")
    _goto_browser(page)

    # browserState starts as 'stopped', wait for it to transition to 'running'.
    required_running = [
        "browser-tab-panel",
        "browser-state-badge",
        "browser-viewport",
        "browser-claude-hint",
        "browser-popout",
        "browser-stop",
    ]
    # Wait for browser-stop (only appears in running state).
    try:
        page.wait_for_selector("[data-testid='browser-stop']",
                               timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:  # noqa: BLE001
        fail(name, "browser-stop not found — state may not have transitioned to 'running'")
        return

    for tid in required_running:
        if page.locator(f"[data-testid='{tid}']").count() < 1:
            fail(name, f"missing data-testid='{tid}' in running state")
            return
    ok(name, "all required testids present in running state")


# ─── Runner ───────────────────────────────────────────────────────────────────

def main() -> int:
    sync_playwright = get_playwright()
    tests = [
        test_ac8_all_testids_present_stopped,
        test_ac4_badge_stopped,
        test_ac4_start_button_visible_when_stopped,
        test_ac7_stopped_to_starting_transition,
        test_ac9_host_runtime_notice,
        test_ac10_popout_link_present_when_running,
        test_ac10_fullscreen_route,
        test_ac8_running_testids,
    ]
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            for tc in tests:
                ctx = browser.new_context(viewport={"width": 1280, "height": 800})
                pg = ctx.new_page()
                try:
                    tc(pg)
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
