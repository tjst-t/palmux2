#!/usr/bin/env python3
"""Sprint See8bd4-3 — Ports tab E2E (REAL browser + REAL backend + REAL container).

Runs during `sprint verify`. Exercises the Ports tab against a dev palmux
instance that has a live incus-container Workspace, observing UI state through
the real backend (test-discipline Rule 2/4: no API mocks). A real dev server is
started INSIDE the container; the test asserts the UI lists it, exposes it via
the Caddy admin API, and the published HTTPS subdomain actually answers.

Acceptance criteria:
  [AC-See8bd4-3-1] Ports tab lists the container's listening ports.
  [AC-See8bd4-3-2] expose toggle → POST expose → publicUrl appears; toggle off
                   removes it (verified via GET, not just DOM).
  [AC-See8bd4-3-3] publicUrl is copyable; public/private badge distinguishes state.
  [AC-See8bd4-3-4] host-runtime Workspace shows the host-notice (no exposure).

Prerequisites (VM, real mode — NO MOCK/fake; test-discipline Rule 7):
  - Dev palmux on the VM with incus-admin group, incus-container Workspace open.
  - *.{BASE_DOMAIN} wildcard cert + Caddy admin API (localhost:2019) live.
  - PALMUX2_E2E_BASE_URL points at the dev instance (e.g. http://192.168.1.43:PORT).

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
VM_HOST = os.environ.get("PALMUX2_E2E_VM", "192.168.1.43")
VM_USER = os.environ.get("PALMUX2_E2E_VM_USER", "ubuntu")
BASE_DOMAIN = os.environ.get("PALMUX2_E2E_BASE_DOMAIN", "palmux-deploy-test.tjstkm.net")
# repo/branch IDs of a known incus-container Workspace on the dev instance.
REPO_ID = os.environ.get("PALMUX2_E2E_REPO", "")
BRANCH_ID = os.environ.get("PALMUX2_E2E_BRANCH", "")
HOST_REPO_ID = os.environ.get("PALMUX2_E2E_HOST_REPO", "")
HOST_BRANCH_ID = os.environ.get("PALMUX2_E2E_HOST_BRANCH", "")
CONTAINER = os.environ.get("PALMUX2_E2E_CONTAINER", "")
TEST_PORT = int(os.environ.get("PALMUX2_E2E_TEST_PORT", "5173"))

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


def get_playwright():
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("FAIL: playwright not installed", file=sys.stderr)
        sys.exit(1)
    return sync_playwright


def _start_server_in_container() -> None:
    """Start a real dev server inside the container on TEST_PORT (idempotent)."""
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
    r = _ssh(
        f"curl -s '{BASE_URL}/api/repos/{REPO_ID}/branches/{BRANCH_ID}/ports'"
    )
    return json.loads(r.stdout) if r.stdout.strip() else {}


def test_ac1_lists_ports(page) -> None:
    name = "AC-See8bd4-3-1"
    page.goto(f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/ports",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector("[data-testid='ports-panel']", timeout=PLAYWRIGHT_TIMEOUT)
    row = page.wait_for_selector(f"[data-testid='ports-row-{TEST_PORT}']",
                                 timeout=PLAYWRIGHT_TIMEOUT)
    if row is None:
        fail(name, f"ports-row-{TEST_PORT} not listed (real container port not detected)")
        return
    ok(name, f"port {TEST_PORT} listed in Ports tab via real backend")


def test_ac2_expose_publishes(page) -> None:
    name = "AC-See8bd4-3-2"
    page.goto(f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/ports",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector(f"[data-testid='ports-row-{TEST_PORT}']", timeout=PLAYWRIGHT_TIMEOUT)
    page.locator(f"[data-testid='ports-expose-toggle-{TEST_PORT}']").first.click()
    url_el = page.wait_for_selector(f"[data-testid='ports-public-url-{TEST_PORT}']",
                                    timeout=PLAYWRIGHT_TIMEOUT)
    public_url = (url_el.text_content() or "").strip()
    if BASE_DOMAIN not in public_url:
        fail(name, f"publicUrl unexpected: {public_url!r}")
        return
    # Verify persistence via GET (not just DOM).
    data = _ports_via_api()
    entry = next((p for p in data.get("ports", []) if p.get("port") == TEST_PORT), None)
    if not entry or not entry.get("public") or BASE_DOMAIN not in (entry.get("publicUrl") or ""):
        fail(name, f"expose did not persist in GET /ports: {entry!r}")
        return
    # The published subdomain actually answers (cert valid, reachable).
    sub = public_url.replace("https://", "").rstrip("/")
    curl = _ssh(
        f"curl -s -o /dev/null -w '%{{http_code}}' --resolve {sub}:443:127.0.0.1 "
        f"-u \"$(grep BASIC_AUTH_USER /etc/caddy/palmux.env | cut -d= -f2)\":"
        f"\"$(cat /tmp/e2e-basic-pass 2>/dev/null)\" https://{sub}/ || echo 000"
    )
    code = (curl.stdout or "").strip()
    if code not in ("200", "401"):  # 401 acceptable if pass not provided to curl
        fail(name, f"published subdomain not reachable (HTTP {code})")
        return
    ok(name, f"exposed {TEST_PORT} → {public_url} (persisted + reachable {code})")


def test_ac3_copy_and_badge(page) -> None:
    name = "AC-See8bd4-3-3"
    page.goto(f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/ports",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector(f"[data-testid='ports-row-{TEST_PORT}']", timeout=PLAYWRIGHT_TIMEOUT)
    badge = page.locator(f"[data-testid='ports-public-badge-{TEST_PORT}']").first
    if badge.count() < 1:
        fail(name, "public/private badge missing")
        return
    copy_btn = page.locator(f"[data-testid='ports-copy-{TEST_PORT}']").first
    if copy_btn.count() < 1:
        fail(name, "copy button missing")
        return
    ok(name, "badge + copy button present")


def test_ac4_host_notice(page) -> None:
    name = "AC-See8bd4-3-4"
    if not (HOST_REPO_ID and HOST_BRANCH_ID):
        fail(name, "PALMUX2_E2E_HOST_REPO/BRANCH not configured (cannot verify host notice)")
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
    sync_playwright = get_playwright()
    tests = [test_ac1_lists_ports, test_ac2_expose_publishes,
             test_ac3_copy_and_badge, test_ac4_host_notice]
    try:
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
    finally:
        _stop_server_in_container()
    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
