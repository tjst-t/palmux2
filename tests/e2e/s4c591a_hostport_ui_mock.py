#!/usr/bin/env python3
"""Sprint S4c591a-1 — host-port mode Ports tab (MOCK, frontend-only Playwright).

All API/WS calls are intercepted so this runs against any dev instance without
a real Incus container. Gates Story completion in `sprint run` (mock tests must
pass before the GUI Story is marked [x]).

Host-port publishing is the wildcard-DNS-less FALLBACK: when no public domain is
configured (publicDomainConfigured=false), the Ports tab switches to host-port
mode and publishes each port as http://<hostIP>:<port> via an incus proxy
device, UNAUTHENTICATED. The mode is environment-level, not a per-port choice.

Acceptance criteria:
  [AC-S4c591a-1-1] host-port mode notice + toggle shown when no public domain.
  [AC-S4c591a-1-2] auto host-port reassignment surfaces a realloc badge + URL.
  [AC-S4c591a-1-3] persistent ⚠ 無認証 warning on published ports.
  [AC-S4c591a-1-4] host runtime untouched (host-notice, no host-port UI).
  Also: subdomain mode (publicDomainConfigured=true) does NOT show host-port UI.
  Also: expose API failure shows an inline error + reverts the toggle.

Run:  PALMUX2_DEV_PORT=<port> python3 tests/e2e/s4c591a_hostport_ui_mock.py
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
HOST_IP = "192.168.1.40"

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


def _port(port, *, host_published=False, host_port=0, host_url="") -> dict:
    return {
        "port": port, "proto": "tcp", "bindAddr": "0.0.0.0", "process": "node",
        "localhostOnly": False, "public": False, "exposed": False, "publicUrl": "",
        "hostPublished": host_published, "hostPort": host_port, "hostUrl": host_url,
    }


def _ports_payload(kind: str, ports: list[dict], *, public_domain_configured: bool,
                   host_ip: str = "") -> dict:
    return {
        "runtimeKind": kind,
        "ports": ports,
        "publicDomainConfigured": public_domain_configured,
        "hostIP": host_ip,
    }


def _common_mocks(page, *, kind: str, ports: list[dict],
                  public_domain_configured: bool, host_ip: str = "") -> None:
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
        lambda r: _fulfill(r, _ports_payload(
            kind, ports, public_domain_configured=public_domain_configured, host_ip=host_ip)),
    )


def _goto_ports(page) -> None:
    # Suppress the first-launch onboarding wizard (Sa53137) so its full-screen
    # overlay does not intercept toggle clicks. The wizard is gated on this
    # localStorage key; it is unrelated to host-port behaviour under test.
    page.add_init_script("window.localStorage.setItem('palmux:onboarding-seen','1')")
    page.goto(f"{BASE_URL}/{FAKE_REPO}/{FAKE_BRANCH}/ports",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector("[data-testid='ports-panel']", timeout=PLAYWRIGHT_TIMEOUT)


# ─── AC-1-1: host-port mode notice + toggle (no public domain) ────────────────

def test_ac1_hostport_mode_notice_and_toggle(page) -> None:
    name = "AC-S4c591a-1-1"
    ports = [_port(5173), _port(8080)]
    _common_mocks(page, kind="incus-container", ports=ports,
                  public_domain_configured=False, host_ip=HOST_IP)
    _goto_ports(page)
    if page.locator("[data-testid='ports-mode-hostport-notice']").count() < 1:
        fail(name, "ports-mode-hostport-notice not shown in host-port mode")
        return
    for p in (5173, 8080):
        if page.locator(f"[data-testid='ports-hostport-toggle-{p}']").count() < 1:
            fail(name, f"ports-hostport-toggle-{p} missing")
            return
    # Subdomain-mode toggle must NOT appear in host-port mode.
    if page.locator("[data-testid='ports-expose-toggle-5173']").count() > 0:
        fail(name, "subdomain-mode toggle leaked into host-port mode")
        return
    ok(name, "host-port notice + per-port toggles shown (subdomain toggle absent)")


# ─── AC-1-1/1-2/1-3: published port shows URL + unauth warning ────────────────

def test_ac3_published_port_unauth_warning(page) -> None:
    name = "AC-S4c591a-1-3"
    url = f"http://{HOST_IP}:5173"
    ports = [_port(5173, host_published=True, host_port=5173, host_url=url)]
    _common_mocks(page, kind="incus-container", ports=ports,
                  public_domain_configured=False, host_ip=HOST_IP)
    _goto_ports(page)
    if page.locator("[data-testid='ports-noauth-warning-5173']").count() < 1:
        fail(name, "ports-noauth-warning-5173 not shown on published port")
        return
    url_el = page.locator("[data-testid='ports-hostport-url-5173']").first
    if url_el.count() < 1 or HOST_IP not in (url_el.text_content() or ""):
        fail(name, "ports-hostport-url-5173 missing or wrong host URL")
        return
    if page.locator("[data-testid='ports-hostport-copy-5173']").count() < 1:
        fail(name, "ports-hostport-copy-5173 missing")
        return
    ok(name, "published port shows host URL + ⚠ 無認証 + copy")


# ─── AC-1-2: collision auto-reassignment badge ────────────────────────────────

def test_ac2_realloc_badge(page) -> None:
    name = "AC-S4c591a-1-2"
    url = f"http://{HOST_IP}:16006"
    ports = [_port(6006, host_published=True, host_port=16006, host_url=url)]
    _common_mocks(page, kind="incus-container", ports=ports,
                  public_domain_configured=False, host_ip=HOST_IP)
    _goto_ports(page)
    badge = page.locator("[data-testid='ports-hostport-realloc-6006']").first
    if badge.count() < 1:
        fail(name, "ports-hostport-realloc-6006 badge missing for reassigned port")
        return
    url_el = page.locator("[data-testid='ports-hostport-url-6006']").first
    if "16006" not in (url_el.text_content() or ""):
        fail(name, "reassigned URL does not point at host:16006")
        return
    ok(name, "auto-reassignment badge + reassigned URL shown")


# ─── AC-1-1: subdomain mode does NOT show host-port UI ────────────────────────

def test_subdomain_mode_no_hostport(page) -> None:
    name = "AC-S4c591a-1-1/subdomain"
    ports = [_port(5173)]
    _common_mocks(page, kind="incus-container", ports=ports,
                  public_domain_configured=True)
    _goto_ports(page)
    if page.locator("[data-testid='ports-mode-hostport-notice']").count() > 0:
        fail(name, "host-port notice shown even though public domain configured")
        return
    if page.locator("[data-testid='ports-hostport-toggle-5173']").count() > 0:
        fail(name, "host-port toggle shown in subdomain mode")
        return
    # Subdomain-mode toggle present instead.
    if page.locator("[data-testid='ports-expose-toggle-5173']").count() < 1:
        fail(name, "subdomain-mode toggle missing in subdomain mode")
        return
    ok(name, "public domain set → subdomain mode only (no host-port UI)")


# ─── AC-1-4: host runtime untouched ───────────────────────────────────────────

def test_ac4_host_notice(page) -> None:
    name = "AC-S4c591a-1-4"
    _common_mocks(page, kind="host", ports=[], public_domain_configured=False)
    _goto_ports(page)
    if page.locator("[data-testid='ports-host-notice']").count() < 1:
        fail(name, "ports-host-notice not shown for host runtime")
        return
    if page.locator("[data-testid='ports-mode-hostport-notice']").count() > 0:
        fail(name, "host-port notice leaked into host runtime")
        return
    ok(name, "host runtime shows host-notice, no host-port UI")


# ─── expose failure → inline error + toggle revert ───────────────────────────

def test_expose_error_inline(page) -> None:
    name = "AC-S4c591a-1-1/error"
    ports = [_port(5173)]

    def handle(route):
        req = route.request
        if req.method == "POST" and "/ports/5173/expose" in req.url:
            _fulfill(route, {"error": "incus config device add failed"}, status=500)
        else:
            route.continue_()

    page.route("**", handle)
    _common_mocks(page, kind="incus-container", ports=ports,
                  public_domain_configured=False, host_ip=HOST_IP)
    _goto_ports(page)
    page.locator("[data-testid='ports-hostport-toggle-5173']").first.click()
    err = page.wait_for_selector("[data-testid='ports-row-error-5173']",
                                 timeout=PLAYWRIGHT_TIMEOUT)
    if not (err.text_content() or "").strip():
        fail(name, "no inline error after expose POST 500")
        return
    toggle = page.locator("[data-testid='ports-hostport-toggle-5173']").first
    if toggle.get_attribute("aria-checked") == "true":
        fail(name, "toggle stayed ON after expose failure (silent-success bug)")
        return
    ok(name, "host-port expose failure surfaced inline + toggle reverted")


def main() -> int:
    sync_playwright = get_playwright()
    tests = [
        test_ac1_hostport_mode_notice_and_toggle,
        test_ac3_published_port_unauth_warning,
        test_ac2_realloc_badge,
        test_subdomain_mode_no_hostport,
        test_ac4_host_notice,
        test_expose_error_inline,
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
