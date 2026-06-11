#!/usr/bin/env python3
"""Sprint See8bd4-3 — Ports tab (MOCK, frontend-only Playwright).

All API/WS calls are intercepted so this runs against any dev instance without
a real Incus container. Gates Story completion in `sprint run` (mock tests must
pass before the GUI Story is marked [x]).

Acceptance criteria:
  [AC-See8bd4-3-1] Ports panel lists the container's listening ports (rows with
                   port/proto/process). (populated state)
  [AC-See8bd4-3-4] host-runtime Workspace shows a host-notice instead of rows.
  [AC-See8bd4-3-5] All interactive elements carry data-testid; empty / populated
                   / host-notice states render per the state diagram.
  [AC-See8bd4-3-6] expose API failure shows an inline row error and reverts the
                   toggle (no silent success).

The real-backend acceptance criteria (-3-1..-3-3 against a live container) live
in tests/e2e/see8bd4_ports_ui.py and run during sprint verify.

Run:  PALMUX2_DEV_PORT=<port> python3 tests/e2e/see8bd4_ports_ui_mock.py
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

FAKE_REPO = "demo--repo--ab12"
FAKE_BRANCH = "feature--cd34"
BASE_DOMAIN = "palmux-deploy-test.tjstkm.net"

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


def _fake_repo(kind: str = "incus-container") -> dict:
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
                    {"id": "ports", "type": "ports", "name": "Ports",
                     "protected": False, "multiple": False, "windowName": ""},
                    {"id": "bash:bash", "type": "bash", "name": "Bash",
                     "protected": False, "multiple": True, "windowName": "palmux:bash:bash"},
                ],
            },
            "runtime": {"kind": kind, "state": "ready", "address": "10.146.187.15"},
        }],
    }


def _ports_payload(kind: str, ports: list[dict]) -> dict:
    return {"runtimeKind": kind, "ports": ports}


def _common_mocks(page, *, kind: str, ports: list[dict]) -> None:
    page.route("**/api/runtimes", lambda r: _fulfill(r, {
        "kinds": [
            {"kind": "host", "available": True},
            {"kind": "incus-container", "available": True},
        ],
    }))
    page.route(f"**/api/repos/{FAKE_REPO}", lambda r: _fulfill(r, _fake_repo(kind)))
    page.route("**/api/repos", lambda r: _fulfill(r, [_fake_repo(kind)]))
    page.route(
        f"**/api/repos/{FAKE_REPO}/branches/{FAKE_BRANCH}/ports",
        lambda r: _fulfill(r, _ports_payload(kind, ports)),
    )


def _goto_ports(page) -> None:
    page.goto(f"{BASE_URL}/{FAKE_REPO}/{FAKE_BRANCH}/ports",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector("[data-testid='ports-panel']", timeout=PLAYWRIGHT_TIMEOUT)


# ─── AC-3-5: populated state + data-testid ───────────────────────────────────

def test_ac5_populated_rows(page) -> None:
    name = "AC-See8bd4-3-5/populated"
    ports = [
        {"port": 5173, "proto": "tcp", "bindAddr": "0.0.0.0", "process": "node",
         "localhostOnly": False, "public": False, "publicUrl": ""},
        {"port": 3000, "proto": "tcp", "bindAddr": "127.0.0.1", "process": "python3",
         "localhostOnly": True, "public": False, "publicUrl": ""},
    ]
    _common_mocks(page, kind="incus-container", ports=ports)
    _goto_ports(page)
    for p in (5173, 3000):
        if page.locator(f"[data-testid='ports-row-{p}']").count() < 1:
            fail(name, f"ports-row-{p} not rendered")
            return
        if page.locator(f"[data-testid='ports-expose-toggle-{p}']").count() < 1:
            fail(name, f"ports-expose-toggle-{p} missing (no data-testid)")
            return
    ok(name, "both listening ports rendered with toggles")


# ─── AC-3-1/3-5: empty state ─────────────────────────────────────────────────

def test_ac5_empty_state(page) -> None:
    name = "AC-See8bd4-3-5/empty"
    _common_mocks(page, kind="incus-container", ports=[])
    _goto_ports(page)
    if page.locator("[data-testid='ports-empty']").count() < 1:
        fail(name, "ports-empty not shown when no listening ports")
        return
    ok(name, "empty state shown")


# ─── AC-3-4: host runtime notice ─────────────────────────────────────────────

def test_ac4_host_notice(page) -> None:
    name = "AC-See8bd4-3-4"
    _common_mocks(page, kind="host", ports=[])
    _goto_ports(page)
    if page.locator("[data-testid='ports-host-notice']").count() < 1:
        fail(name, "ports-host-notice not shown for host runtime")
        return
    ok(name, "host-runtime notice shown (no port exposure on host)")


# ─── AC-3-6: expose failure → inline error + toggle revert ───────────────────

def test_ac6_expose_error_inline(page) -> None:
    name = "AC-See8bd4-3-6"
    ports = [{"port": 5173, "proto": "tcp", "bindAddr": "0.0.0.0", "process": "node",
              "localhostOnly": False, "public": False, "publicUrl": ""}]

    def handle(route):
        req = route.request
        if req.method == "POST" and "/ports/5173/expose" in req.url:
            _fulfill(route, {"error": "caddy admin API route add failed"}, status=500)
        else:
            route.continue_()

    page.route("**", handle)
    _common_mocks(page, kind="incus-container", ports=ports)
    _goto_ports(page)

    page.locator("[data-testid='ports-expose-toggle-5173']").first.click()
    err = page.wait_for_selector("[data-testid='ports-row-error-5173']",
                                 timeout=PLAYWRIGHT_TIMEOUT)
    if not (err.text_content() or "").strip():
        fail(name, "no inline error after expose POST 500")
        return
    # toggle should have reverted to off (not public)
    toggle = page.locator("[data-testid='ports-expose-toggle-5173']").first
    if toggle.get_attribute("aria-checked") == "true":
        fail(name, "toggle stayed ON after expose failure (silent-success bug)")
        return
    ok(name, "expose failure surfaced inline + toggle reverted")


def main() -> int:
    sync_playwright = get_playwright()
    tests = [
        test_ac5_populated_rows,
        test_ac5_empty_state,
        test_ac4_host_notice,
        test_ac6_expose_error_inline,
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
