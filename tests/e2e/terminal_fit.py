#!/usr/bin/env python3
"""Regression: the claude-tui terminal must FIT its host box — the rendered
`.xterm-screen` must not exceed `.term` (else the last row is clipped, the
"claude-tui bottom cut off" bug). Root cause was padding on the element xterm
opens into, which made FitAddon over-compute rows. Verified across DPRs/heights
where the bug reproduced worst.

Exit 0 = ALL PASS / SKIP. Run: python3 tests/e2e/terminal_fit.py
"""
from __future__ import annotations

import os
import signal
import socket
import subprocess
import sys
import time
import urllib.parse
from pathlib import Path

sys.path.insert(0, os.path.dirname(__file__))

REPO_ROOT = Path(__file__).resolve().parents[2]
PLAYWRIGHT_TIMEOUT = 20_000
_PREBUILT_BIN = REPO_ROOT / "bin" / "palmux"
_USE_PREBUILT = _PREBUILT_BIN.is_file()

# DPR × height combinations where the padding-on-host bug clipped the last row.
_CASES = [(1.0, 1000), (1.25, 800), (1.25, 1080), (1.5, 1000), (2.0, 769)]

_MEAS = """() => {
  const term = document.querySelector("[data-testid='claude-tui-terminal']");
  const screen = term.querySelector('.xterm-screen');
  const cs = getComputedStyle(term);
  const contentH = term.clientHeight - parseFloat(cs.paddingTop) - parseFloat(cs.paddingBottom);
  const screenH = screen ? screen.getBoundingClientRect().height : 0;
  return { overflow: Math.round((screenH - contentH) * 100) / 100 };
}"""


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def main() -> None:
    print("terminal_fit — claude-tui must not overflow its host box")
    if not _USE_PREBUILT:
        print("SKIP: no prebuilt binary (run `make build`)")
        sys.exit(0)
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("SKIP: playwright not installed")
        sys.exit(0)

    port = _free_port()
    os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
    cfg = Path(f"/tmp/palmux2-termfit-{port}")
    cfg.mkdir(parents=True, exist_ok=True)
    proc = subprocess.Popen(
        [str(_PREBUILT_BIN), "--addr", f"127.0.0.1:{port}", "--config-dir", str(cfg),
         "--claude-bin", "/bin/cat", "--tmux-prefix", f"_pmx_tf{port}_"],
        cwd=REPO_ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
    )
    deadline = time.time() + 30
    while time.time() < deadline:
        line = proc.stdout.readline() if proc.stdout else ""
        if f":{port}" in line or "listening" in line:
            break
        if proc.poll() is not None:
            print("FAIL: server died")
            sys.exit(1)

    failed = 0
    try:
        import _fixture as fx
        with fx.palmux2_test_fixture("termfit") as fixture:
            bid = fixture.primary_branch_id(timeout_s=10.0)
            url = (f"http://localhost:{port}/{urllib.parse.quote(fixture.repo_id, safe='')}"
                   f"/{urllib.parse.quote(bid, safe='')}/claude")
            with sync_playwright() as p:
                browser = p.chromium.launch(headless=True)
                for dpr, h in _CASES:
                    ctx = browser.new_context(viewport={"width": 1280, "height": h}, device_scale_factor=dpr)
                    ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                    page = ctx.new_page()
                    try:
                        page.goto(url, wait_until="load", timeout=PLAYWRIGHT_TIMEOUT)
                        page.wait_for_selector("[data-testid='claude-tui-terminal']", timeout=PLAYWRIGHT_TIMEOUT)
                        time.sleep(1.8)
                        overflow = page.evaluate(_MEAS)["overflow"]
                        if overflow > 0.5:
                            print(f"FAIL: dpr={dpr} h={h} → terminal overflows host by {overflow}px (last row clipped)")
                            failed += 1
                        else:
                            print(f"PASS: dpr={dpr} h={h} → fits (overflow {overflow}px)")
                    finally:
                        ctx.close()
                browser.close()
    finally:
        if proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=8)
            except subprocess.TimeoutExpired:
                proc.kill()
        import shutil
        shutil.rmtree(cfg, ignore_errors=True)

    print(f"\nterminal_fit: {'ALL PASS' if failed == 0 else f'{failed} FAILED'}")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
