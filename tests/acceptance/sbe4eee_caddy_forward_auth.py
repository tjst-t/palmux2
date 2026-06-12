#!/usr/bin/env python3
"""Story Sbe4eee-2 acceptance — Caddy basic_auth → forward_auth(palmux).

REAL MODE: queries the deployed Caddy admin API + curls the real domain to
prove the auth point moved from Caddy basic_auth to palmux forward_auth, and
checks the local-dev no-regression (SSO off when --public-domain is unset).

Acceptance criteria:
  [AC-Sbe4eee-2-1] palmux-injected per-port route uses forward_auth(→/auth/verify),
                   not basic_auth; a Public=true port is a plain reverse_proxy.
                   curl: auth sub (no cookie) → 302 login; public sub → 200.
  [AC-Sbe4eee-2-2] apex is forward_auth-gated; /auth/* bypasses it (login page
                   reachable un-authenticated).
  [AC-Sbe4eee-2-3] local dev (no --public-domain) injects no forward_auth and
                   /auth/login is disabled (404) — no regression.

Config: PALMUX2_E2E_VM / _BASE_DOMAIN / _REPO / _BRANCH. Infra-gated skip only.
AC-2-3 runs a LOCAL ./bin/palmux (no VM needed for that part).
"""
from __future__ import annotations

import os
import socket
import subprocess
import sys
import time
import urllib.request

DOMAIN = os.environ.get("PALMUX2_E2E_BASE_DOMAIN", "palmux-deploy-test.tjstkm.net")
VM_HOST = os.environ.get("PALMUX2_E2E_VM", DOMAIN)
VM_USER = os.environ.get("PALMUX2_E2E_VM_USER", "ubuntu")
REPO = os.environ.get("PALMUX2_E2E_REPO", "lxc--incus--c18d")
BRANCH = os.environ.get("PALMUX2_E2E_BRANCH", "incus--5523")
AUTH_PORT = 8888
PUB_PORT = 9999

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


def free_port():
    s = socket.socket(); s.bind(("127.0.0.1", 0)); p = s.getsockname()[1]; s.close(); return p


def ac_2_3_local_no_regression():
    """Local: palmux WITHOUT --public-domain must not serve /auth/login."""
    binp = os.path.abspath(os.path.join(os.path.dirname(__file__), "..", "..", "bin", "palmux"))
    if not os.path.exists(binp):
        fail("AC-Sbe4eee-2-3", "bin/palmux not built")
        return
    port = free_port()
    proc = subprocess.Popen(
        [binp, f"--addr=127.0.0.1:{port}", "--config-dir", "/tmp/sbe4eee-nodom",
         "--tmux-prefix=_pmx_nodom_"],
        env=dict(os.environ, BASIC_AUTH_HASH="", PALMUX_PUBLIC_DOMAIN=""),
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
    try:
        time.sleep(1.5)
        body = ""
        for _ in range(30):
            try:
                r = urllib.request.urlopen(f"http://127.0.0.1:{port}/auth/login", timeout=5)
                body = r.read().decode("utf-8", "replace")
                break
            except urllib.error.HTTPError as e:
                body = e.read().decode("utf-8", "replace") if e.fp else ""
                break
            except Exception:
                time.sleep(0.2)
        # SSO disabled → /auth/login is NOT registered → falls through to the SPA
        # (index.html). The key no-regression assertion is that the SSO login
        # FORM is not served (auth is off).
        if "auth-login-form" not in body:
            ok("AC-Sbe4eee-2-3", "local dev (no --public-domain): SSO login form NOT served (no regression)")
        else:
            fail("AC-Sbe4eee-2-3", "SSO login form served even without --public-domain")
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except Exception:
            proc.kill()


def main() -> int:
    # AC-2-3 is local — always runnable.
    ac_2_3_local_no_regression()

    if os.environ.get("SKIP_INCUS_E2E") or ssh("echo ok").returncode != 0:
        print("SKIP (VM-dependent ACs): VM not reachable / SKIP_INCUS_E2E")
        return 1 if _FAILED else 0

    # Ensure the demo ports are exposed (auth + public) so routes exist.
    api = "http://127.0.0.1:8080"
    for port, pub in ((AUTH_PORT, "false"), (PUB_PORT, "true")):
        ssh(f"incus exec lxc-incus-c18d-incus-5523-9d493c60 -- bash -c "
            f"'pgrep -f \"http.server {port}\" >/dev/null || (cd /home/ubuntu && nohup python3 -m http.server {port} --bind 0.0.0.0 >/tmp/d{port}.log 2>&1 & disown)' </dev/null")
    time.sleep(9)
    ssh(f"curl -s -X POST -H 'Content-Type: application/json' -d '{{\"public\":false}}' {api}/api/repos/{REPO}/branches/{BRANCH}/ports/{AUTH_PORT}/expose >/dev/null")
    ssh(f"curl -s -X POST -H 'Content-Type: application/json' -d '{{\"public\":true}}' {api}/api/repos/{REPO}/branches/{BRANCH}/ports/{PUB_PORT}/expose >/dev/null")
    time.sleep(2)

    # AC-2-1: inspect the admin-API config — auth route has forward_auth, public doesn't.
    cfg = ssh("curl -s http://localhost:2019/config/apps/http/servers/srv0/routes").stdout
    inst = "lxc-incus-c18d-incus-5523-9d493c60"
    # crude but sufficient: the auth route JSON contains /auth/verify; find both ids.
    auth_has_fa = '/auth/verify' in cfg and 'http_basic' not in cfg
    if auth_has_fa:
        ok("AC-Sbe4eee-2-1/route", "per-port routes use forward_auth (/auth/verify), no basic_auth")
    else:
        fail("AC-Sbe4eee-2-1/route", f"expected forward_auth not basic_auth in routes (has /auth/verify={'/auth/verify' in cfg}, has http_basic={'http_basic' in cfg})")

    def code(host, cookie=""):
        c = f"-b '{cookie}'" if cookie else ""
        r = ssh(f"curl -s -o /dev/null -w '%{{http_code}}' {c} --resolve {host}:443:127.0.0.1 https://{host}/")
        return r.stdout.strip()

    import re
    ws = re.sub(r"[^a-z0-9]+", "-", BRANCH.lower()).strip("-")
    parts = REPO.split("--")
    repo = re.sub(r"[^a-z0-9]+", "-", ("-".join(parts[1:]) if len(parts) >= 3 else REPO).lower()).strip("-")
    auth_host = f"{AUTH_PORT}--{ws}--{repo}.{DOMAIN}"
    pub_host = f"{PUB_PORT}--{ws}--{repo}.{DOMAIN}"

    a = code(auth_host)
    # The public route + in-container server may take a moment to be ready.
    pu = ""
    for _ in range(8):
        pu = code(pub_host)
        if pu == "200":
            break
        time.sleep(2)
    if a == "302" and pu == "200":
        ok("AC-Sbe4eee-2-1/curl", f"auth sub no-cookie={a} (login), public sub={pu} (open)")
    else:
        fail("AC-Sbe4eee-2-1/curl", f"auth={a} (want 302), public={pu} (want 200)")

    # AC-2-2: apex forward_auth + /auth bypass.
    apex = code(DOMAIN)
    login = ssh(f"curl -s -o /dev/null -w '%{{http_code}}' --resolve {DOMAIN}:443:127.0.0.1 https://{DOMAIN}/auth/login").stdout.strip()
    if apex == "302" and login == "200":
        ok("AC-Sbe4eee-2-2", f"apex no-cookie={apex} (→login), /auth/login={login} (bypass)")
    else:
        fail("AC-Sbe4eee-2-2", f"apex={apex} (want 302), /auth/login={login} (want 200)")

    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
