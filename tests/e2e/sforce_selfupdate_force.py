#!/usr/bin/env python3
"""Force-update test affordance — full old→new GUI self-update flow at the SAME
real version (no real release required).

This closes the long-standing manual-smoke gap (S6ab0ed MS-1 / Sa8e7d0 backlog):
the complete GUI update chain — badge "更新あり" → "すべてまとめて更新" → REAL
update machinery + palmux2 restart → WS drop → /health reconnect handshake →
"更新しました" toast → badge clears — could only be exercised when a GitHub
release was strictly newer than installed. The env-gated force affordance
(internal/selfupdate/force.go) synthesizes that "update available" at the same
real version and injects the version DELTA (a persisted +force.N suffix) the
handshake needs, so the whole happy path is reachable at one fixed release.

What is REAL here (not mocked): the badge/panel GUI, POST /api/selfupdate/run,
the in-process force state machine (arm → apply → disarm), the real WS drop +
real /health reconnect handshake, and the version-delta logic. What is seamed
(E2E rig only, like S6ab0ed): the box is made "Nix-managed" with a no-op
~/update-palmux2.sh stub, and PALMUX_SELFUPDATE_RESTART_CMD=true neutralizes the
`systemctl --user restart palmux2.service` (the dev binary is a bare subprocess,
not a systemd unit) so the TEST performs the process restart itself — exactly the
pattern s6ab0ed_reconnect_live.py uses. On a real host the restart is real.

Acceptance criteria:
  [AC-FORCE-1] After `palmux update --force-arm`, the GUI badge "更新あり" appears
               at the SAME real version, with the 🧪 forced-test note.
  [AC-FORCE-2] "すべてまとめて更新" runs and the panel switches to progress.
  [AC-FORCE-3] After the (test-performed) restart, the FE detects the advanced
               +force.N version and shows the "更新しました" completion toast.
  [AC-FORCE-4] After completion the badge clears (the forced update disarmed).

Run (manages its own server):  python3 tests/e2e/sforce_selfupdate_force.py
"""
from __future__ import annotations

import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.request
from pathlib import Path

from playwright.sync_api import sync_playwright

REPO = Path(__file__).resolve().parents[2]
BIN = str(REPO / "bin" / "palmux")
PORT = os.environ.get("SFORCE_LIVE_PORT", "8233")
BASE = f"http://localhost:{PORT}"
HOME_STUB = Path.home() / "update-palmux2.sh"
CFG_DIR = "/tmp/sforce-live-cfg"

ENV = dict(
    os.environ,
    PALMUX_SELFUPDATE_FORCE="1",
    # Neutralize the in-binary systemctl restart; the test restarts the process.
    PALMUX_SELFUPDATE_RESTART_CMD="true",
)


def _start() -> subprocess.Popen:
    p = subprocess.Popen(
        [BIN, "--addr", f"0.0.0.0:{PORT}", "--config-dir", CFG_DIR,
         "--tmux-prefix", "_pmx_sforce_"],
        env=ENV, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
        start_new_session=True,
    )
    for _ in range(80):
        try:
            with urllib.request.urlopen(f"{BASE}/api/health", timeout=2) as r:
                if r.status == 200:
                    return p
        except Exception:
            time.sleep(0.25)
    raise RuntimeError("server did not come up")


def _stop(p: subprocess.Popen | None) -> None:
    if p is None:
        return
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


def _arm() -> None:
    """Arm a forced update BEFORE the server starts, so its startup detection
    sees it. Writes the counter file into CFG_DIR via the real CLI path."""
    r = subprocess.run(
        [BIN, "update", "--force-arm", "--config-dir", CFG_DIR],
        env=ENV, capture_output=True, text=True,
    )
    if r.returncode != 0:
        raise RuntimeError(f"force-arm failed: {r.stdout}\n{r.stderr}")
    print("  (armed) " + r.stdout.strip().splitlines()[0])


def main() -> int:
    failures: list[str] = []

    def check(name, cond):
        print(f"[{'PASS' if cond else 'FAIL'}] {name}")
        if not cond:
            failures.append(name)

    # Fresh config dir + Nix-managed stub.
    shutil.rmtree(CFG_DIR, ignore_errors=True)
    os.makedirs(CFG_DIR, exist_ok=True)
    HOME_STUB.write_text("#!/usr/bin/env bash\nsleep 1\nexit 0\n")
    HOME_STUB.chmod(0o755)

    _arm()
    srv = _start()
    srv2 = None
    try:
        # Record the real version the server reports BEFORE the forced run.
        with urllib.request.urlopen(f"{BASE}/api/health", timeout=3) as r:
            import json
            base_version = json.load(r).get("version", "")
        print(f"  base /health version: {base_version!r}")

        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            ctx = browser.new_context()
            # Skip the first-launch onboarding wizard (unconfigured isolated cfg
            # dir → it would overlay and intercept clicks). Gate is sessionStorage
            # 'palmux:onboarding-skipped' (onboarding-wizard.tsx).
            ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
            page = ctx.new_page()
            page.goto(BASE, wait_until="domcontentloaded", timeout=20000)
            page.wait_for_timeout(1500)

            # AC-FORCE-1: badge appears at the same real version + forced note.
            badge_ok = False
            try:
                page.wait_for_selector('[data-testid="update-available-badge"]', timeout=10000)
                badge_ok = True
            except Exception:
                pass
            check("AC-FORCE-1 badge '更新あり' visible after --force-arm", badge_ok)
            page.locator('[data-testid="update-available-badge"]').click()
            forced_note_ok = False
            try:
                page.wait_for_selector('[data-testid="update-forced-note"]', timeout=5000)
                forced_note_ok = True
            except Exception:
                pass
            check("AC-FORCE-1 🧪 forced-test note shown in panel", forced_note_ok)

            # AC-FORCE-2: Nix-managed → Update-all → progress.
            page.wait_for_selector('[data-testid="update-all-btn"]', timeout=8000)
            page.locator('[data-testid="update-all-btn"]').click()
            prog_ok = False
            try:
                page.wait_for_selector('[data-testid="update-progress-badge"]', timeout=8000)
                prog_ok = True
            except Exception:
                pass
            check("AC-FORCE-2 progress state after 'すべてまとめて更新'", prog_ok)

            # The forced run applied the synthetic bump (counter→1, disarmed) and
            # tried the no-op restart seam. Now perform the real process restart:
            # the restarted process reads +force.1 from CFG_DIR and reports it.
            time.sleep(1.5)
            _stop(srv)
            srv = None
            srv2 = _start()

            # AC-FORCE-3: FE detects new version (base+force.1) → completion toast.
            toast_ok = False
            try:
                page.wait_for_selector('[data-testid="update-complete-toast"]', timeout=45000)
                txt = page.locator('[data-testid="update-complete-toast"]').inner_text()
                toast_ok = "force.1" in txt
            except Exception:
                pass
            check("AC-FORCE-3 '更新しました' toast with +force.1 after reconnect", toast_ok)

            # AC-FORCE-4: badge clears (disarmed → no synthetic 'available').
            cleared_ok = False
            try:
                page.wait_for_selector('[data-testid="update-available-badge"]',
                                       state="detached", timeout=10000)
                cleared_ok = True
            except Exception:
                # Also accept: the badge element simply not present.
                cleared_ok = page.locator('[data-testid="update-available-badge"]').count() == 0
            check("AC-FORCE-4 badge cleared after forced update completed", cleared_ok)

            browser.close()
    finally:
        _stop(srv)
        _stop(srv2)
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
