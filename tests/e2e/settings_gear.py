#!/usr/bin/env python3
"""The header gear button opens the Settings panel, which exposes the Claude
permission-mode field. Verifies the gear→settings→permission wiring.

Exit 0 = PASS / SKIP. Run: python3 tests/e2e/settings_gear.py
"""
from __future__ import annotations
import os, signal, socket, subprocess, sys, time, urllib.parse
from pathlib import Path

sys.path.insert(0, os.path.dirname(__file__))
REPO = Path(__file__).resolve().parents[2]
BIN = REPO / "bin" / "palmux"
TO = 20_000


def free_port():
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0)); return s.getsockname()[1]


def main() -> None:
    print("settings_gear — header gear opens settings + claude permission mode field")
    if not BIN.is_file():
        print("SKIP: no prebuilt binary (make build)"); sys.exit(0)
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("SKIP: playwright not installed"); sys.exit(0)

    port = free_port(); os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
    cfg = Path(f"/tmp/palmux2-gear-{port}"); cfg.mkdir(parents=True, exist_ok=True)
    proc = subprocess.Popen(
        [str(BIN), "--addr", f"127.0.0.1:{port}", "--config-dir", str(cfg),
         "--claude-bin", "/bin/cat", "--tmux-prefix", f"_pmx_gear{port}_"],
        cwd=REPO, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    dl = time.time() + 30
    while time.time() < dl:
        line = proc.stdout.readline() if proc.stdout else ""
        if f":{port}" in line or "listening" in line:
            break
        if proc.poll() is not None:
            print("FAIL: server died"); sys.exit(1)

    failed = 0
    try:
        import _fixture as fx
        with fx.palmux2_test_fixture("gear") as fixture:
            bid = fixture.primary_branch_id(timeout_s=10.0)
            url = (f"http://localhost:{port}/{urllib.parse.quote(fixture.repo_id, safe='')}"
                   f"/{urllib.parse.quote(bid, safe='')}/claude")
            with sync_playwright() as p:
                b = p.chromium.launch(headless=True)
                ctx = b.new_context(viewport={"width": 1280, "height": 800})
                ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                pg = ctx.new_page()
                pg.goto(url, wait_until="load", timeout=TO)
                pg.wait_for_function("document.getElementById('root').innerHTML.length > 100", timeout=TO)
                # Gear button present + opens the settings panel.
                pg.wait_for_selector("[data-testid='header-settings-btn']", timeout=TO)
                pg.click("[data-testid='header-settings-btn']")
                pg.wait_for_selector("[data-testid='field-claudePermissionMode']", timeout=TO)
                # The permission-mode control has a bypass option (button text 'bypass').
                seg = pg.locator("[data-testid='field-claudePermissionMode']")
                if seg.locator("button", has_text="bypass").count() < 1:
                    print("FAIL: bypass option not present in claude permission mode control"); failed += 1
                else:
                    print("PASS: gear opens settings + claude permission mode field (with bypass) present")
                # Update-check button + status present, and clicking it resolves to a
                # concrete status (最新/更新あり — not the pre-check "未確認").
                pg.wait_for_selector("[data-testid='settings-check-updates-btn']", timeout=TO)
                pg.click("[data-testid='settings-check-updates-btn']")
                try:
                    pg.wait_for_function(
                        "!document.querySelector(\"[data-testid='settings-update-status']\")"
                        ".textContent.includes('未確認')",
                        timeout=TO,
                    )
                    print("PASS: update-check button refreshes status")
                except Exception:
                    print("FAIL: update-check button did not update status"); failed += 1
                b.close()
    finally:
        if proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try: proc.wait(timeout=8)
            except subprocess.TimeoutExpired: proc.kill()
        import shutil; shutil.rmtree(cfg, ignore_errors=True)

    print(f"\nsettings_gear: {'ALL PASS' if failed == 0 else 'FAILED'}")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
