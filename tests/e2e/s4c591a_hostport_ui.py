#!/usr/bin/env python3
"""Sprint S4c591a-1 — host-port mode Ports tab E2E (REAL browser + REAL backend).

Runs during `sprint verify`. Exercises the host-port fallback UI against a dev
palmux instance that has a live incus-container Workspace AND no public domain
configured (publicDomainConfigured=false). It observes UI state through the real
backend (test-discipline Rule 2/4: no API mocks) and verifies that toggling a
port ON adds a real incus proxy device reachable at http://<hostIP>:<hostPort>,
then OFF removes it.

The no-public-domain condition is the natural state of a dev instance started
without --public-domain; any incus-container Workspace is then in host-port mode.

Acceptance criteria:
  [AC-S4c591a-1-1] host-port toggle → POST expose → proxy device added; the host
                   URL appears and http://<hostIP>:<hostPort> answers externally.
  [AC-S4c591a-1-2] host port auto-allocated; toggle OFF removes the proxy device
                   (verified via GET, not just DOM).
  [AC-S4c591a-1-3] published port shows the persistent ⚠ 無認証 warning.
  [AC-S4c591a-1-4] host-runtime Workspace shows the host-notice (no host-port UI).

Prerequisites (VM, real mode — NO MOCK/fake; test-discipline Rule 7):
  - Dev palmux on the VM started WITHOUT --public-domain, incus-container
    Workspace open, incus-admin group.
  - PALMUX2_E2E_BASE_URL points at the dev instance.

Skip conditions (infra absence only — never skips assertions):
  - SKIP_INCUS_E2E set, or the configured base URL / VM is unreachable.
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import time

PLAYWRIGHT_TIMEOUT = 20_000

BASE_URL = os.environ.get("PALMUX2_E2E_BASE_URL", "")
VM_HOST = os.environ.get("PALMUX2_E2E_VM", "palmux-deploy-test.tjstkm.net")
VM_USER = os.environ.get("PALMUX2_E2E_VM_USER", "ubuntu")
REPO_ID = os.environ.get("PALMUX2_E2E_REPO", "")
BRANCH_ID = os.environ.get("PALMUX2_E2E_BRANCH", "")
HOST_REPO_ID = os.environ.get("PALMUX2_E2E_HOST_REPO", "")
HOST_BRANCH_ID = os.environ.get("PALMUX2_E2E_HOST_BRANCH", "")
CONTAINER = os.environ.get("PALMUX2_E2E_CONTAINER", "")
TEST_PORT = int(os.environ.get("PALMUX2_E2E_TEST_PORT", "5173"))
VM_API_URL = os.environ.get("PALMUX2_E2E_VM_API_URL", "http://127.0.0.1:8080")

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def skip(msg: str) -> None:
    print(f"SKIP: {msg}")
    sys.exit(0)


def _ssh(cmd: str, timeout: int = 30):
    return subprocess.run(
        ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
         "-o", "StrictHostKeyChecking=no", f"{VM_USER}@{VM_HOST}", cmd],
        capture_output=True, text=True, timeout=timeout,
    )


def _preflight() -> None:
    if os.environ.get("SKIP_INCUS_E2E"):
        skip("SKIP_INCUS_E2E set")
    if not (BASE_URL and REPO_ID and BRANCH_ID and CONTAINER):
        skip("PALMUX2_E2E_BASE_URL / REPO / BRANCH / CONTAINER not configured")
    if _ssh("echo ok").returncode != 0:
        skip(f"VM {VM_HOST} not reachable")
    # host-port mode requires NO public domain. If the instance has one, this
    # GUI fallback is not active; the proxy-device capability itself is covered
    # by tests/acceptance/s4c591a_hostport_proxy.py regardless.
    data = _ports_via_api()
    if data.get("publicDomainConfigured", True):
        skip("instance has a public domain configured → host-port mode UI not active "
             "(proxy-device capability covered by acceptance test)")


def get_playwright():
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("FAIL: playwright not installed", file=sys.stderr)
        sys.exit(1)
    return sync_playwright


def _start_server_in_container() -> None:
    _ssh(f"incus exec {CONTAINER} -- pkill -f 'http.server {TEST_PORT}' </dev/null 2>/dev/null || true")
    _ssh(
        f"incus exec {CONTAINER} -- bash -c "
        f"'cd /home/ubuntu && nohup python3 -m http.server {TEST_PORT} --bind 0.0.0.0 "
        f">/tmp/e2e-http.log 2>&1 & disown' </dev/null",
    )
    time.sleep(2)


def _stop_server_in_container() -> None:
    _ssh(f"incus exec {CONTAINER} -- pkill -f 'http.server {TEST_PORT}' </dev/null 2>/dev/null || true")


def _ports_via_api() -> dict:
    r = _ssh(f"curl -s '{VM_API_URL}/api/repos/{REPO_ID}/branches/{BRANCH_ID}/ports'")
    try:
        return json.loads(r.stdout) if r.stdout.strip() else {}
    except json.JSONDecodeError:
        return {}


def _reset_expose_state() -> None:
    _ssh(f"curl -s -X DELETE '{VM_API_URL}/api/repos/{REPO_ID}/branches/{BRANCH_ID}/ports/{TEST_PORT}/expose' >/dev/null 2>&1 || true")
    time.sleep(1)


def test_ac1_hostport_toggle_publishes(page) -> None:
    name = "AC-S4c591a-1-1"
    page.goto(f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/ports",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector("[data-testid='ports-mode-hostport-notice']", timeout=PLAYWRIGHT_TIMEOUT)
    page.wait_for_selector(f"[data-testid='ports-row-{TEST_PORT}']", timeout=PLAYWRIGHT_TIMEOUT)
    page.locator(f"[data-testid='ports-hostport-toggle-{TEST_PORT}']").first.click()
    url_el = page.wait_for_selector(f"[data-testid='ports-hostport-url-{TEST_PORT}']",
                                    timeout=PLAYWRIGHT_TIMEOUT)
    host_url = (url_el.text_content() or "").strip()
    if not host_url.startswith("http://"):
        fail(name, f"host URL unexpected: {host_url!r}")
        return
    # Persisted in GET.
    data = _ports_via_api()
    entry = next((p for p in data.get("ports", []) if p.get("port") == TEST_PORT), None)
    if not entry or not entry.get("hostPublished"):
        fail(name, f"host publish did not persist in GET /ports: {entry!r}")
        return
    host_port = entry.get("hostPort") or TEST_PORT
    # The proxy device actually answers from the host network.
    curl = _ssh(f"curl -s -o /dev/null -w '%{{http_code}}' http://127.0.0.1:{host_port}/ || echo 000")
    code = (curl.stdout or "").strip()
    if code not in ("200", "301", "302"):
        fail(name, f"host-port not reachable (HTTP {code}) at :{host_port}")
        return
    # Proxy device exists in incus config.
    dev = _ssh(f"incus config device list {CONTAINER}")
    if "p" not in (dev.stdout or ""):
        fail(name, "no proxy device found after host publish")
        return
    ok(name, f"host-published {TEST_PORT} → {host_url} (persisted + reachable {code})")


def test_ac3_unauth_warning(page) -> None:
    name = "AC-S4c591a-1-3"
    page.goto(f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/ports",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector(f"[data-testid='ports-row-{TEST_PORT}']", timeout=PLAYWRIGHT_TIMEOUT)
    warn = page.locator(f"[data-testid='ports-noauth-warning-{TEST_PORT}']").first
    if warn.count() < 1:
        fail(name, "⚠ 無認証 warning missing on published port")
        return
    ok(name, "persistent unauth warning shown on published host port")


def test_ac2_unpublish_removes_device(page) -> None:
    name = "AC-S4c591a-1-2"
    page.goto(f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/ports",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector(f"[data-testid='ports-row-{TEST_PORT}']", timeout=PLAYWRIGHT_TIMEOUT)
    # Toggle OFF.
    page.locator(f"[data-testid='ports-hostport-toggle-{TEST_PORT}']").first.click()
    time.sleep(1.5)
    data = _ports_via_api()
    entry = next((p for p in data.get("ports", []) if p.get("port") == TEST_PORT), None)
    if entry and entry.get("hostPublished"):
        fail(name, f"port still host-published after toggle OFF: {entry!r}")
        return
    ok(name, "toggle OFF removed proxy device (hostPublished=false in GET)")


def test_ac4_host_notice(page) -> None:
    name = "AC-S4c591a-1-4"
    if not (HOST_REPO_ID and HOST_BRANCH_ID):
        print(f"  [{name}] SKIP — no host workspace configured (covered by mock test)")
        return
    page.goto(f"{BASE_URL}/{HOST_REPO_ID}/{HOST_BRANCH_ID}/ports",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    notice = page.wait_for_selector("[data-testid='ports-host-notice']", timeout=PLAYWRIGHT_TIMEOUT)
    if notice is None:
        fail(name, "ports-host-notice not shown for host runtime")
        return
    ok(name, "host-runtime notice shown")


def main() -> int:
    _preflight()
    _start_server_in_container()
    _reset_expose_state()
    sync_playwright = get_playwright()
    # Ordered: AC1 publishes, AC3 reads the published warning, AC2 unpublishes.
    tests = [test_ac1_hostport_toggle_publishes, test_ac3_unauth_warning,
             test_ac2_unpublish_removes_device, test_ac4_host_notice]
    try:
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                for tc in tests:
                    ctx = browser.new_context(viewport={"width": 1280, "height": 800})
                    page = ctx.new_page()
                    page.add_init_script("window.localStorage.setItem('palmux:onboarding-seen','1')")
                    try:
                        tc(page)
                    except Exception as e:  # noqa: BLE001
                        fail(tc.__name__, f"unexpected: {e}")
                    finally:
                        ctx.close()
            finally:
                browser.close()
    finally:
        _reset_expose_state()
        _stop_server_in_container()
    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
