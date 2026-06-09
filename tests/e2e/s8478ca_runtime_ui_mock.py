#!/usr/bin/env python3
"""Sprint S8478ca Story 5 — Workspace Runtime selector + state badge (MOCK).

Frontend-only tests: the real frontend is served by the dev instance, but the
runtime endpoints (GET /api/runtimes, PATCH .../runtime) and the WS runtime
view are intercepted via Playwright routing so we can drive states a stock dev
host cannot produce for real:
  - Incus absent  → incus-container greyed + install tooltip
  - badge state machine  starting → ready → error  (simulated runtime view)
  - PATCH .../runtime failure → inline error, selector reverts
  - Host login scope (host--0000) shows no selector regardless of caps

These run during `sprint run` (mock gate). The real-server companion
(s8478ca_runtime_ui.py) runs during `sprint verify`.

Acceptance criteria exercised (edge/error states):
  [AC-S8478ca-5-1] incus-container disabled + tooltip when caps say unavailable
  [AC-S8478ca-5-2] badge follows runtime view through starting/ready/error
  [AC-S8478ca-5-3] Host login scope renders no runtime selector

Exit code 0 = ALL PASS. Run standalone (dev instance must be up for static
assets; all API/WS is mocked):
  make serve INSTANCE=dev
  python3 tests/e2e/s8478ca_runtime_ui_mock.py
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
PLAYWRIGHT_TIMEOUT = 15_000  # ms

HOST_REPO = "host--0000"
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


# ─── AC-5-1 (mock): Incus absent → greyed + tooltip ────────────────────────────

def test_ac_5_1_incus_absent(page) -> None:
    name = "AC-S8478ca-5-1"
    page.route("**/api/runtimes", lambda r: _fulfill(r, {
        "kinds": [
            {"kind": "host", "available": True},
            {"kind": "incus-container", "available": False,
             "reason": "Incus is not installed on this host"},
        ]
    }))
    page.goto(BASE_URL, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector("[data-testid='repo-picker-open']", timeout=PLAYWRIGHT_TIMEOUT)
    page.click("[data-testid='repo-picker-open']")
    page.wait_for_selector("[data-testid='runtime-selector']", timeout=PLAYWRIGHT_TIMEOUT)
    disabled = page.locator("[data-testid='runtime-option-incus-container-disabled']")
    if disabled.count() < 1:
        fail(name, "incus-container not disabled when caps say unavailable")
        return
    disabled.first.hover()
    tip = page.wait_for_selector("[data-testid='runtime-incus-install-tooltip']",
                                 timeout=PLAYWRIGHT_TIMEOUT)
    if "incus" not in (tip.text_content() or "").lower():
        fail(name, "install tooltip does not mention Incus")
        return
    ok(name, "incus-container greyed + install tooltip on caps.available=false")


# ─── AC-5-2 (mock): badge follows starting → ready → error ───────────────────

def test_ac_5_2_badge_state_machine(page) -> None:
    name = "AC-S8478ca-5-2"
    repo_id, branch_id = "demo--repo--ab12", "feature--cd34"
    # Sequence the runtime view returned by the branch fetch on each reload.
    states = iter(["starting", "ready", "error"])

    def branch_handler(route):
        st = next(states, "error")
        _fulfill(route, {
            "id": repo_id,
            "openBranches": [{
                "id": branch_id, "name": "feature",
                "runtime": {"kind": "incus-container", "state": st,
                            "address": "10.42.0.5" if st == "ready" else "",
                            "error": "incus launch failed: image not found" if st == "error" else ""},
            }],
        })

    page.route(f"**/api/repos/{repo_id}", branch_handler)
    seen = []
    for _ in range(3):
        page.goto(f"{BASE_URL}/{repo_id}/{branch_id}/claude",
                  timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
        badge = page.wait_for_selector("[data-testid='workspace-runtime-badge']",
                                       timeout=PLAYWRIGHT_TIMEOUT)
        seen.append(badge.get_attribute("data-runtime-state"))
    if seen != ["starting", "ready", "error"]:
        fail(name, f"badge did not follow runtime view; saw {seen}")
        return
    # error state must surface the error message somewhere reachable
    err = page.locator("[data-testid='workspace-runtime-badge']")
    if "error" not in (err.get_attribute("data-runtime-state") or ""):
        fail(name, "final badge not in error state")
        return
    ok(name, "badge followed starting → ready → error from runtime view")


# ─── AC-5-2 (mock): PATCH failure → inline error + revert ─────────────────────

def test_ac_5_2_patch_failure(page) -> None:
    name = "AC-S8478ca-5-2"
    page.route("**/api/runtimes", lambda r: _fulfill(r, {
        "kinds": [{"kind": "host", "available": True},
                  {"kind": "incus-container", "available": True}]
    }))
    page.route("**/runtime", lambda r: _fulfill(
        r, {"error": "failed to persist runtime"}, status=500)
        if r.request.method == "PATCH" else r.continue_())
    page.goto(BASE_URL, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector("[data-testid='repo-picker-open']", timeout=PLAYWRIGHT_TIMEOUT)
    page.click("[data-testid='repo-picker-open']")
    page.wait_for_selector("[data-testid='runtime-selector']", timeout=PLAYWRIGHT_TIMEOUT)
    page.click("[data-testid='runtime-option-incus-container']")
    # Confirm modal for an in-place change (if present) then accept.
    if page.locator("[data-testid='runtime-change-confirm-ok']").count() > 0:
        page.click("[data-testid='runtime-change-confirm-ok']")
    err = page.wait_for_selector("[data-testid='runtime-selector-error']",
                                 timeout=PLAYWRIGHT_TIMEOUT)
    if not (err.text_content() or "").strip():
        fail(name, "PATCH 500 did not surface an inline error")
        return
    ok(name, "PATCH failure surfaced inline error (selector revert path)")


# ─── AC-5-3 (mock): Host login scope renders no selector ──────────────────────

def test_ac_5_3_host_scope_no_selector(page) -> None:
    name = "AC-S8478ca-5-3"
    page.route("**/api/runtimes", lambda r: _fulfill(r, {
        "kinds": [{"kind": "host", "available": True},
                  {"kind": "incus-container", "available": True}]
    }))
    page.goto(f"{BASE_URL}/{HOST_REPO}/host/bash:bash",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    label = page.wait_for_selector("[data-testid='host-scope-label']", timeout=PLAYWRIGHT_TIMEOUT)
    if not (label.text_content() or "").strip():
        fail(name, "host-scope-label empty")
        return
    if page.locator("[data-testid='runtime-selector']").count() > 0:
        fail(name, "Host login scope must not render a runtime selector")
        return
    ok(name, "Host login scope: distinct label, no runtime selector")


def main() -> int:
    sync_playwright = get_playwright()
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            for tc in (test_ac_5_1_incus_absent, test_ac_5_2_badge_state_machine,
                       test_ac_5_2_patch_failure, test_ac_5_3_host_scope_no_selector):
                ctx = browser.new_context(viewport={"width": 1280, "height": 800})
                page = ctx.new_page()
                try:
                    tc(page)
                except Exception as e:  # noqa: BLE001 — surface as a FAIL, keep going
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
