#!/usr/bin/env python3
"""Sbe4eee-1 — palmux SSO login flow (real palmux backend, no Caddy).

Starts ./bin/palmux with SSO enabled (--public-domain + BASIC_AUTH_HASH) on a
free port and exercises the login page + cookie mechanics directly against
palmux. The Caddy forward_auth SSO across subdomains is verified separately by
tests/e2e/sbe4eee_sso.py on the deploy VM.

Acceptance criteria:
  [AC-Sbe4eee-1-1] GET /auth/login renders the form; correct password →
                   302 + Set-Cookie palmux_sso (Domain=.<base>, HttpOnly,
                   Secure, SameSite=Lax).
  [AC-Sbe4eee-1-2] wrong password → inline auth-error, no cookie; /auth/verify
                   200 with a valid cookie, 302→login without.
  [AC-Sbe4eee-1-3] remember=on → persistent cookie (Max-Age set);
                   remember=off → session cookie (no Max-Age).
  [AC-Sbe4eee-1-5] /auth/logout clears the cookie (Max-Age=0).

Run:  python3 tests/e2e/sbe4eee_login_mock.py   (builds nothing; needs ./bin/palmux)
"""
from __future__ import annotations

import http.cookies
import os
import signal
import socket
import subprocess
import sys
import time
import urllib.request

PW = "testpass123"
HASH = "$2a$10$LTHPFjQ6k3PQV9C02a0aQeneMtMjQBGk0uXbwdvZIzz1CAL1Gpt5e"
BASE = "test.local"

_FAILED: list[str] = []


def fail(name, msg):
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name, msg=""):
    print(f"  [{name}] {msg or 'OK'}")


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def hreq(method, url, headers=None, data=None):
    """Return (status, headers, body) WITHOUT following redirects."""
    req = urllib.request.Request(url, method=method, data=data, headers=headers or {})

    class NoRedirect(urllib.request.HTTPErrorProcessor):
        def http_response(self, request, response):
            return response
        https_response = http_response

    opener = urllib.request.build_opener(NoRedirect)
    resp = opener.open(req, timeout=10)
    return resp.status, resp.headers, resp.read().decode("utf-8", "replace")


def main() -> int:
    binp = os.path.join(os.path.dirname(__file__), "..", "..", "bin", "palmux")
    binp = os.path.abspath(binp)
    if not os.path.exists(binp):
        print("FAIL: bin/palmux not built (run `make build`)", file=sys.stderr)
        return 1

    port = free_port()
    env = dict(os.environ, BASIC_AUTH_USER="ubuntu", BASIC_AUTH_HASH=HASH)
    proc = subprocess.Popen(
        [binp, f"--addr=127.0.0.1:{port}", f"--public-domain={BASE}",
         "--config-dir", "/tmp/sbe4eee-cfg", "--tmux-prefix=_pmx_ssotest_"],
        env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    base = f"http://127.0.0.1:{port}"
    try:
        # wait for listen
        for _ in range(50):
            try:
                hreq("GET", f"{base}/auth/login")
                break
            except Exception:
                time.sleep(0.1)
        else:
            fail("startup", "palmux did not start")
            return 1

        # AC-1-1: login form renders with the testids.
        _, _, body = hreq("GET", f"{base}/auth/login?rd=https://{BASE}/x")
        if all(t in body for t in ('data-testid="auth-login-form"',
                                   'data-testid="auth-password-input"',
                                   'data-testid="auth-remember-checkbox"',
                                   'data-testid="auth-submit"')):
            ok("AC-Sbe4eee-1-1/form", "login form rendered with all testids")
        else:
            fail("AC-Sbe4eee-1-1/form", "login form missing testids")

        # AC-1-2: wrong password → 401 + auth-error, no cookie.
        st, hdrs, body = hreq("POST", f"{base}/auth/login",
                              {"Content-Type": "application/x-www-form-urlencoded"},
                              b"password=wrong&remember=on&rd=https://" + BASE.encode() + b"/")
        if st == 401 and 'data-testid="auth-error"' in body and "Set-Cookie" not in hdrs:
            ok("AC-Sbe4eee-1-2/wrong", "wrong password → 401 + auth-error, no cookie")
        else:
            fail("AC-Sbe4eee-1-2/wrong", f"unexpected: st={st} hasErr={'auth-error' in body} setCookie={'Set-Cookie' in hdrs}")

        # AC-1-1 + 1-3: correct password, remember=on → 302 + persistent cookie.
        st, hdrs, _ = hreq("POST", f"{base}/auth/login",
                           {"Content-Type": "application/x-www-form-urlencoded"},
                           f"password={PW}&remember=on&rd=https://{BASE}/x".encode())
        sc = hdrs.get("Set-Cookie", "")
        c = http.cookies.SimpleCookie()
        c.load(sc)
        morsel = c.get("palmux_sso")
        if st == 302 and morsel and hdrs.get("Location") == f"https://{BASE}/x":
            # Go serializes ".base" → "base" (the legacy leading dot is dropped);
            # per RFC 6265 a Domain attribute already covers subdomains, so SSO
            # works either way.
            attrs_ok = (morsel["domain"] in (BASE, f".{BASE}") and morsel["httponly"]
                        and morsel["secure"] and morsel["samesite"].lower() == "lax"
                        and morsel["max-age"])
            if attrs_ok:
                ok("AC-Sbe4eee-1-1/cookie", f"302 + persistent cookie (Domain={morsel['domain']}, SameSite=Lax, Max-Age set)")
            else:
                fail("AC-Sbe4eee-1-1/cookie", f"cookie attrs wrong: {dict(morsel)}")
            cookie_val = morsel.value
        else:
            fail("AC-Sbe4eee-1-1/cookie", f"login did not 302+cookie: st={st} loc={hdrs.get('Location')}")
            cookie_val = ""

        # AC-1-3: remember=off → session cookie (no Max-Age).
        _, hdrs2, _ = hreq("POST", f"{base}/auth/login",
                           {"Content-Type": "application/x-www-form-urlencoded"},
                           f"password={PW}&rd=https://{BASE}/".encode())
        c2 = http.cookies.SimpleCookie()
        c2.load(hdrs2.get("Set-Cookie", ""))
        m2 = c2.get("palmux_sso")
        if m2 and not m2["max-age"]:
            ok("AC-Sbe4eee-1-3", "remember=off → session cookie (no Max-Age)")
        else:
            fail("AC-Sbe4eee-1-3", f"session cookie should have no Max-Age: {dict(m2) if m2 else None}")

        # AC-1-2: /auth/verify 200 with cookie, 302 without.
        st_no, _, _ = hreq("GET", f"{base}/auth/verify")
        st_yes, vh, _ = hreq("GET", f"{base}/auth/verify",
                             {"Cookie": f"palmux_sso={cookie_val}"})
        if st_no == 302 and st_yes == 200:
            ok("AC-Sbe4eee-1-2/verify", "verify 200 with cookie, 302→login without")
        else:
            fail("AC-Sbe4eee-1-2/verify", f"verify: without={st_no} with={st_yes}")

        # AC-1-5: logout clears the cookie.
        st_l, lh, _ = hreq("GET", f"{base}/auth/logout")
        cl = http.cookies.SimpleCookie()
        cl.load(lh.get("Set-Cookie", ""))
        ml = cl.get("palmux_sso")
        if st_l == 302 and ml is not None and str(ml["max-age"]) == "0":
            ok("AC-Sbe4eee-1-5", "logout clears the cookie (Max-Age=0)")
        else:
            fail("AC-Sbe4eee-1-5", f"logout did not clear cookie: st={st_l} morsel={dict(ml) if ml else None}")

    finally:
        proc.send_signal(signal.SIGTERM)
        try:
            proc.wait(timeout=5)
        except Exception:
            proc.kill()

    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
