#!/usr/bin/env python3
"""Sbe4eee-3 — single-login SSO across subdomains (REAL browser + REAL Caddy).

Runs Playwright locally against the REAL deployed HTTPS domain (DNS →
deploy VM → Caddy forward_auth → palmux). Cookies are real (correct domain),
so this exercises the genuine cross-subdomain SSO. Container ports are set up
over SSH; the browser drives the login UI and subdomain navigation.

Acceptance criteria:
  [AC-Sbe4eee-3-1] One login at the apex → an auth-protected published
                   subdomain loads WITHOUT re-login (same browser).
  [AC-Sbe4eee-3-2] A fresh (no-cookie) client is redirected to the login page
                   on an auth subdomain; a Public=true subdomain loads with no
                   login.
  [AC-Sbe4eee-3-3] After logout, the apex and auth subdomains redirect to the
                   login page again.

Config (env): PALMUX2_E2E_PASSWORD (required), PALMUX2_E2E_BASE_DOMAIN
  (default palmux-deploy-test.tjstkm.net), PALMUX2_E2E_VM/_REPO/_BRANCH/
  _CONTAINER for the SSH container setup. Infra-gated skip only.
"""
from __future__ import annotations

import os
import subprocess
import sys
import time

DOMAIN = os.environ.get("PALMUX2_E2E_BASE_DOMAIN", "palmux-deploy-test.tjstkm.net")
PASSWORD = os.environ.get("PALMUX2_E2E_PASSWORD", "")
VM_HOST = os.environ.get("PALMUX2_E2E_VM", DOMAIN)
VM_USER = os.environ.get("PALMUX2_E2E_VM_USER", "ubuntu")
REPO = os.environ.get("PALMUX2_E2E_REPO", "lxc--incus--c18d")
BRANCH = os.environ.get("PALMUX2_E2E_BRANCH", "incus--5523")
CONTAINER = os.environ.get("PALMUX2_E2E_CONTAINER", "lxc-incus-c18d-incus-5523-9d493c60")
VM_API = os.environ.get("PALMUX2_E2E_VM_API_URL", "http://127.0.0.1:8080")
AUTH_PORT = 8888
PUB_PORT = 9999
TIMEOUT = 25_000

_FAILED: list[str] = []


def fail(n, m):
    print(f"FAIL: [{n}] {m}", file=sys.stderr)
    _FAILED.append(n)


def ok(n, m=""):
    print(f"  [{n}] {m or 'OK'}")


def skip(m):
    print(f"SKIP: {m}")
    sys.exit(0)


def ssh(cmd, timeout=30):
    return subprocess.run(
        ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
         "-o", "StrictHostKeyChecking=no", f"{VM_USER}@{VM_HOST}", cmd],
        capture_output=True, text=True, timeout=timeout)


def sub(port):
    import re
    parts = REPO.split("--")
    ws = re.sub(r"[^a-z0-9]+", "-", BRANCH.lower()).strip("-")
    repo_src = "-".join(parts[1:]) if len(parts) >= 3 else REPO
    repo = re.sub(r"[^a-z0-9]+", "-", repo_src.lower()).strip("-")
    return f"{port}--{ws}--{repo}.{DOMAIN}"


def setup():
    for p in (AUTH_PORT, PUB_PORT):
        ssh(f"incus exec {CONTAINER} -- bash -c 'pgrep -f \"http.server {p}\" >/dev/null || "
            f"(cd /home/ubuntu && nohup python3 -m http.server {p} --bind 0.0.0.0 >/tmp/d{p}.log 2>&1 & disown)' </dev/null")
    time.sleep(10)  # let the scan see them
    ssh(f"curl -s -X POST -H 'Content-Type: application/json' -d '{{\"public\":false}}' "
        f"{VM_API}/api/repos/{REPO}/branches/{BRANCH}/ports/{AUTH_PORT}/expose >/dev/null")
    ssh(f"curl -s -X POST -H 'Content-Type: application/json' -d '{{\"public\":true}}' "
        f"{VM_API}/api/repos/{REPO}/branches/{BRANCH}/ports/{PUB_PORT}/expose >/dev/null")
    time.sleep(2)


def is_login(page):
    return page.locator("[data-testid='auth-login-form']").count() > 0


def main() -> int:
    if os.environ.get("SKIP_INCUS_E2E"):
        skip("SKIP_INCUS_E2E set")
    if not PASSWORD:
        skip("PALMUX2_E2E_PASSWORD not set")
    if ssh("echo ok").returncode != 0:
        skip(f"VM {VM_HOST} not reachable")
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("FAIL: playwright not installed", file=sys.stderr)
        return 1

    setup()
    apex = f"https://{DOMAIN}/"
    auth_sub = f"https://{sub(AUTH_PORT)}/"
    pub_sub = f"https://{sub(PUB_PORT)}/"

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            # ── AC-3-1: login once, subdomain passes through ──────────────────
            ctx = browser.new_context(ignore_https_errors=True)
            pg = ctx.new_page()
            pg.goto(apex, timeout=TIMEOUT, wait_until="load")
            if not is_login(pg):
                fail("AC-Sbe4eee-3-1", "apex did not redirect to the login page")
            else:
                pg.fill("[data-testid='auth-password-input']", PASSWORD)
                pg.click("[data-testid='auth-submit']")
                pg.wait_for_load_state("load")
                if is_login(pg):
                    fail("AC-Sbe4eee-3-1", "still on login page after submitting the password")
                else:
                    # Now the auth subdomain must load (200) WITHOUT re-login.
                    resp = pg.goto(auth_sub, timeout=TIMEOUT, wait_until="load")
                    if is_login(pg):
                        fail("AC-Sbe4eee-3-1", "auth subdomain forced a re-login (SSO broken)")
                    elif not resp or resp.status != 200:
                        fail("AC-Sbe4eee-3-1", f"auth subdomain returned {resp.status if resp else 'no response'} (not 200)")
                    else:
                        ok("AC-Sbe4eee-3-1", f"single login → {sub(AUTH_PORT)} loaded (200) without re-login")

            # ── AC-3-2: fresh client redirected; public subdomain open ────────
            ctx2 = browser.new_context(ignore_https_errors=True)
            pg2 = ctx2.new_page()
            pg2.goto(auth_sub, timeout=TIMEOUT, wait_until="load")
            redirected = is_login(pg2)
            presp = pg2.goto(pub_sub, timeout=TIMEOUT, wait_until="load")
            pub_open = (not is_login(pg2)) and presp is not None and presp.status == 200
            if redirected and pub_open:
                ok("AC-Sbe4eee-3-2", "no-cookie → login on auth sub; Public=true sub open (200) without login")
            else:
                fail("AC-Sbe4eee-3-2", f"auth-redirected={redirected} public-open-200={pub_open}")

            # ── AC-3-3: logout invalidates everywhere ─────────────────────────
            pg.goto(f"https://{DOMAIN}/auth/logout", timeout=TIMEOUT, wait_until="load")
            pg.goto(apex, timeout=TIMEOUT, wait_until="load")
            apex_login = is_login(pg)
            pg.goto(auth_sub, timeout=TIMEOUT, wait_until="load")
            sub_login = is_login(pg)
            if apex_login and sub_login:
                ok("AC-Sbe4eee-3-3", "after logout, apex + auth subdomain both redirect to login")
            else:
                fail("AC-Sbe4eee-3-3", f"apex-login={apex_login} sub-login={sub_login}")
        finally:
            browser.close()

    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
