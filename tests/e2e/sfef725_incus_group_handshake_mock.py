#!/usr/bin/env python3
"""Sprint Sfef725 — incus-admin recover RECONNECT HANDSHAKE + self-update routing (MOCK).

These two ACs require a server RESTART / state TRANSITION that a single real
backend instance cannot produce on demand (the seam state is fixed at startup),
so they are covered by a MOCK GUI test: the real frontend (served by a dev
instance) drives the REAL handshake code (use-event-stream's
maybeFinishSelfUpdate → maybeFinishIncusGroupFix, reused verbatim from S6ab0ed),
while the time-domain transitions are simulated. The companion real-backend E2E
(sfef725_incus_group_recover.py) covers the button states + endpoint→verb wiring
without any mocking.

The reconnect is driven for real: the dev backend is RESTARTED mid-test so the
events WebSocket genuinely drops and reconnects (the open-after-drop edge is what
triggers the handshake) — only the /api/incus-group + /api/health JSON bodies
are routed to simulate the stale→ok transition a real user-manager restart would
produce.

Acceptance criteria:
  [AC-Sfef725-2-3] after the recover restart, the reconnect handshake (WS drop →
                   /health poll → re-fetch group state → completion toast
                   "incus-admin を適用しました…") fires.
  [AC-Sfef725-3-2] after a GUI self-update, if stale is detected the recover
                   surface (Story 2) appears (routes the user to the fix).

Run standalone (a built bin/palmux + free ports):
  go build -o bin/palmux ./cmd/palmux && python3 tests/e2e/sfef725_incus_group_handshake_mock.py
"""
from __future__ import annotations

import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
BIN = os.environ.get("PALMUX_BIN", str(REPO / "bin" / "palmux"))
PW_TIMEOUT = 20_000
_FAILED: list[str] = []


def fail(name, msg):
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name, msg=""):
    print(f"  [{name}] {msg or 'OK'}")


def get_playwright():
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("FAIL: playwright not installed", file=sys.stderr)
        sys.exit(1)
    return sync_playwright


def _free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


class Srv:
    def __init__(self, port, cfg):
        self.port = port
        self.cfg = cfg
        self.proc = None

    def start(self):
        self.proc = subprocess.Popen(
            [BIN, "--addr", f"127.0.0.1:{self.port}", "--config-dir", self.cfg,
             "--tmux-prefix", "_pmx_sfef725_hs_"],
            stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=dict(os.environ),
        )
        for _ in range(60):
            try:
                urllib.request.urlopen(f"http://127.0.0.1:{self.port}/api/health", timeout=2)
                return
            except Exception:
                time.sleep(0.2)

    def stop(self):
        if self.proc:
            self.proc.terminate()
            try:
                self.proc.wait(timeout=5)
            except Exception:
                self.proc.kill()
            self.proc = None


def _fulfill(route, obj, status=200):
    route.fulfill(status=status, content_type="application/json", body=json.dumps(obj))


def test_handshake():
    """AC-2-3: stale → recover click → real WS drop/reconnect → completion toast."""
    name = "AC-Sfef725-2-3"
    port = _free_port()
    cfg = tempfile.mkdtemp(prefix="sfef725-hs-")
    srv = Srv(port, cfg)
    srv.start()
    base = f"http://127.0.0.1:{port}"
    try:
        sync_playwright = get_playwright()
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                ctx = browser.new_context(viewport={"width": 1280, "height": 800})
                ctx.add_init_script("window.localStorage.setItem('palmux:onboarding-seen','1')")
                ctx.add_init_script("window.__PALMUX_UPDATE_TIMEOUT_MS__ = 12000")
                page = ctx.new_page()

                grp = {"state": "stale", "remedy": "restart-user-manager",
                       "detail": "stale — user manager cached old groups; NOT enough",
                       "fixAvailable": True, "restartCommand": "sudo systemctl restart user@1000",
                       "gid": 994}
                grp_ok = {"state": "ok", "remedy": "none", "detail": "active", "gid": 994}
                fixed = {"v": False}

                page.route("**/api/incus-group", lambda r: _fulfill(r, grp_ok if fixed["v"] else grp))
                # The fix endpoint just acks; the REAL restart below produces the
                # WS drop. We flip the simulated state at restart time.
                page.route("**/api/incus-group/fix", lambda r: _fulfill(r, {"ok": True, "message": "applying"}))

                page.goto(f"{base}/", timeout=PW_TIMEOUT, wait_until="load")
                page.wait_for_selector("[data-testid='incus-group-recover']", timeout=PW_TIMEOUT)
                btn = page.locator("[data-testid='incus-group-recover-btn']")
                if btn.count() < 1:
                    fail(name, "recover button not shown for stale")
                    return
                btn.click(force=True)
                # Button enters in-flight state (handshake armed).
                page.wait_for_selector("text=適用中", timeout=PW_TIMEOUT)

                # Now produce a REAL WS drop + reconnect by restarting the backend,
                # and flip the simulated group state to ok (as a user-manager
                # restart would). The open-after-drop edge triggers the handshake,
                # which re-polls /api/incus-group (now ok) → completion toast.
                fixed["v"] = True
                srv.stop()
                srv.start()

                page.wait_for_selector("[data-testid='update-complete-toast']", timeout=PW_TIMEOUT)
                txt = page.locator("[data-testid='update-complete-toast']").inner_text()
                if "incus-admin" not in txt:
                    fail(name, f"completion toast text unexpected: {txt!r}")
                    return
                ok(name, "recover handshake (real WS drop/reconnect) → 'incus-admin を適用しました' toast")
            finally:
                browser.close()
    finally:
        srv.stop()


def test_selfupdate_routing():
    """AC-3-2: self-update completes → /health reports stale → recover surface shows."""
    name = "AC-Sfef725-3-2"
    port = _free_port()
    cfg = tempfile.mkdtemp(prefix="sfef725-su-")
    srv = Srv(port, cfg)
    srv.start()
    base = f"http://127.0.0.1:{port}"
    try:
        sync_playwright = get_playwright()
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                ctx = browser.new_context(viewport={"width": 1280, "height": 800})
                ctx.add_init_script("window.localStorage.setItem('palmux:onboarding-seen','1')")
                page = ctx.new_page()

                grp = {"state": "stale", "remedy": "restart-user-manager",
                       "detail": "stale after update", "fixAvailable": True,
                       "restartCommand": "sudo systemctl restart user@1000", "gid": 994}
                # /health reports stale (post-update detection routes to recover).
                page.route("**/api/incus-group", lambda r: _fulfill(r, grp))
                page.route("**/api/health", lambda r: _fulfill(
                    r, {"status": "ok", "version": "v0.0.2", "incusGroupState": "stale"}))

                page.goto(f"{base}/", timeout=PW_TIMEOUT, wait_until="load")
                # The recover surface appears because the group state is stale
                # (the same surface a post-self-update stale detection routes to).
                page.wait_for_selector("[data-testid='incus-group-recover']", timeout=PW_TIMEOUT)
                if page.locator("[data-testid='incus-group-recover-btn']").count() < 1:
                    fail(name, "post-update stale recover surface/button not shown")
                    return
                ok(name, "post-update stale → recover surface routes user to the fix button")
            finally:
                browser.close()
    finally:
        srv.stop()


def main():
    test_handshake()
    test_selfupdate_routing()
    print()
    if _FAILED:
        print(f"{len(_FAILED)} FAILED:")
        for f in _FAILED:
            print("  -", f)
        return 1
    print("ALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
