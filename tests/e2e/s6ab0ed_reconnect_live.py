#!/usr/bin/env python3
"""Sprint S6ab0ed — self-update reconnect handshake LIVE (real backend, real WS drop).

This is the production-mode (Rule 7) test for the self-restart → reconnect →
completion-toast handshake. It manages its OWN palmux dev binary so it can
genuinely RESTART it (dropping the real events WebSocket) and report a NEW
version from /api/health, then assert the FE:
  WS drop → poll /api/health → detect new version → reconnect → completion toast.

Acceptance criteria:
  [AC-S6ab0ed-2-1] Nix-managed Update-all → POST /api/selfupdate/run → progress.
  [AC-S6ab0ed-2-2] after the binary restarts as a new version, the FE shows the
                   "vX に更新しました" completion toast.

Seams (E2E rig only — both override INPUTS, the HTTP/WS paths stay real):
  - PALMUX_SELFUPDATE_FAKE_INSTALLED=v0.9.0  → real GitHub poll yields "update available"
  - PALMUX_FAKE_VERSION=<v>                  → /health reports this version
  - a no-op ~/update-palmux2.sh stub makes the box "Nix-managed" so run succeeds
    (the stub does nothing; the test itself performs the version-bump restart).

Run (manages its own server):  python3 tests/e2e/s6ab0ed_reconnect_live.py
"""
from __future__ import annotations

import os
import signal
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

from playwright.sync_api import sync_playwright

REPO = Path(__file__).resolve().parents[2]
BIN = str(REPO / "bin" / "palmux")
PORT = os.environ.get("S6AB0ED_LIVE_PORT", "8231")
BASE = f"http://localhost:{PORT}"
HOME_STUB = Path.home() / "update-palmux2.sh"
# Isolated, empty config dir so the restarted server comes up fast and does not
# fight the host/dev instance over tmux sessions or repos.json.
CFG_DIR = "/tmp/s6ab0ed-live-cfg"


def _start(version: str) -> subprocess.Popen:
    env = dict(
        os.environ,
        PALMUX_SELFUPDATE_FAKE_INSTALLED="v0.9.0",
        PALMUX_FAKE_VERSION=version,
    )
    p = subprocess.Popen(
        [BIN, "--addr", f"0.0.0.0:{PORT}", "--config-dir", CFG_DIR,
         "--tmux-prefix", "_pmx_s6ablive_"],
        env=env, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        start_new_session=True,
    )
    # wait for health
    for _ in range(60):
        try:
            with urllib.request.urlopen(f"{BASE}/api/health", timeout=2) as r:
                if r.status == 200:
                    return p
        except Exception:
            time.sleep(0.25)
    raise RuntimeError("server did not come up")


def _stop(p: subprocess.Popen) -> None:
    try:
        os.killpg(os.getpgid(p.pid), signal.SIGTERM)
    except Exception:
        try:
            p.terminate()
        except Exception:
            pass
    try:
        p.wait(timeout=5)
    except Exception:
        pass


def main() -> int:
    failures: list[str] = []

    def check(name, cond):
        print(f"[{'PASS' if cond else 'FAIL'}] {name}")
        if not cond:
            failures.append(name)

    os.makedirs(CFG_DIR, exist_ok=True)
    # Make the box "Nix-managed" with a harmless stub so POST run succeeds.
    HOME_STUB.write_text("#!/usr/bin/env bash\nsleep 1\nexit 0\n")
    HOME_STUB.chmod(0o755)

    srv = _start("v0.10.0")
    try:
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            ctx = browser.new_context()
            ctx.add_init_script("window.localStorage.setItem('palmux:onboarding-seen','1'); window.sessionStorage.setItem('palmux:onboarding-skipped','1'); window.__PALMUX_NO_RELOAD__ = true")
            page = ctx.new_page()
            page.goto(BASE, wait_until="domcontentloaded", timeout=20000)
            page.wait_for_timeout(1500)

            # Badge visible → click → Update-all (Nix-managed via stub).
            page.locator('[data-testid="update-available-badge"]').click()
            page.wait_for_selector('[data-testid="update-all-btn"]', timeout=8000)
            check("AC-S6ab0ed-2-1 Update-all button present (Nix-managed via stub)", True)
            page.locator('[data-testid="update-all-btn"]').click()

            # Progress state appears.
            page.wait_for_selector('[data-testid="update-progress-badge"]', timeout=8000)
            check("AC-S6ab0ed-2-1 progress badge after Update-all", True)

            # Now perform the actual "self-restart as a new version": stop the
            # v0.10.0 server (drops the real events WS) and start v0.11.0.
            time.sleep(1.0)
            _stop(srv)
            srv2 = _start("v0.11.0")

            # AC-2-2: the FE should detect the WS drop, poll /health (now
            # v0.11.0 ≠ baseline v0.10.0), reconnect, and show the toast.
            toast_ok = False
            try:
                page.wait_for_selector('[data-testid="update-complete-toast"]', timeout=45000)
                txt = page.locator('[data-testid="update-complete-toast"]').inner_text()
                toast_ok = "v0.11.0" in txt
            except Exception:
                pass
            check("AC-S6ab0ed-2-2 completion toast 'v0.11.0 に更新しました' after reconnect", toast_ok)
            ctx.close()

            # ── AC-2-3: failure (rollback). Restart reporting the SAME version
            # the page started on → the handshake times out → updateFailed →
            # rollback note. A short handshake window keeps this quick.
            _stop(srv2)
            srv_fail = _start("v0.20.0")  # page below will baseline on v0.20.0
            ctx2 = browser.new_context()
            ctx2.add_init_script("window.localStorage.setItem('palmux:onboarding-seen','1'); window.sessionStorage.setItem('palmux:onboarding-skipped','1'); window.__PALMUX_NO_RELOAD__ = true")
            ctx2.add_init_script("window.__PALMUX_UPDATE_TIMEOUT_MS__ = 6000")
            page2 = ctx2.new_page()
            page2.goto(BASE, wait_until="domcontentloaded", timeout=20000)
            page2.wait_for_timeout(1500)
            page2.locator('[data-testid="update-available-badge"]').click()
            page2.wait_for_selector('[data-testid="update-all-btn"]', timeout=8000)
            page2.locator('[data-testid="update-all-btn"]').click()
            page2.wait_for_selector('[data-testid="update-progress-badge"]', timeout=8000)
            # Restart reporting the SAME version → no version advance → failure.
            time.sleep(0.8)
            _stop(srv_fail)
            srv_same = _start("v0.20.0")
            failed_ok = False
            try:
                page2.wait_for_selector('[data-testid="update-failed-note"]', timeout=30000)
                failed_ok = True
            except Exception:
                pass
            check("AC-S6ab0ed-2-3 rollback note shown when version unchanged after restart",
                  failed_ok)
            ctx2.close()

            browser.close()
            _stop(srv_same)
            srv = None  # already stopped
    finally:
        if srv is not None:
            _stop(srv)
        try:
            HOME_STUB.unlink()
        except Exception:
            pass

    print()
    if failures:
        print(f"{len(failures)} FAILED:")
        for f in failures:
            print("  -", f)
        return 1
    print("ALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
