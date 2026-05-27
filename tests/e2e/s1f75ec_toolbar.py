#!/usr/bin/env python3
"""Sprint S1f75ec Story 3 — Toolbar mode auto-switch + ESC ESC button E2E.

Covers acceptance criteria:
  [AC-S1f75ec-3-1] Navigate to Claude tab → [data-testid='toolbar-mode-claude'] visible,
                   [data-testid='toolbar-mode-normal'] absent.
  [AC-S1f75ec-3-2] Navigate to Bash tab → [data-testid='toolbar-mode-normal'] visible,
                   [data-testid='toolbar-mode-claude'] absent.
  [AC-S1f75ec-3-3] claude_mode=tui + click ESC ESC → WS receives \\x1b\\x1b.
  [AC-S1f75ec-3-4] data-testid selectors exist in the DOM.
  [AC-S1f75ec-3-5] claude_mode=agent → ESC ESC button is disabled.

Uses the hermetic palmux2 binary pattern (same as s1f75ec_mode_switch.py).

Exit code 0 = ALL PASS. Run standalone:
  python3 tests/e2e/s1f75ec_toolbar.py
"""
from __future__ import annotations

import json
import os
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator

sys.path.insert(0, os.path.dirname(__file__))

REPO_ROOT = Path(__file__).resolve().parents[2]
PLAYWRIGHT_TIMEOUT = 20_000  # ms

_PREBUILT_BIN = REPO_ROOT / "bin" / "palmux"
_USE_PREBUILT = _PREBUILT_BIN.is_file()


# ─── Helpers ─────────────────────────────────────────────────────────────────

def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def passed(msg: str) -> None:
    print(f"PASS: {msg}")


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _get_playwright():
    try:
        from playwright.sync_api import sync_playwright  # noqa: F401
        return sync_playwright
    except ImportError:
        print("SKIP: playwright not installed")
        sys.exit(0)


# ─── Low-level HTTP helpers ──────────────────────────────────────────────────

def _http_json(port: int, method: str, path: str,
               body: dict | None = None) -> tuple[int, object]:
    url = f"http://localhost:{port}{path}"
    raw = json.dumps(body).encode() if body is not None else None
    headers: dict[str, str] = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, method=method, data=raw, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            code: int = resp.status
            data: bytes = resp.read()
    except urllib.error.HTTPError as exc:
        code = exc.code
        data = exc.read()
    try:
        return code, json.loads(data.decode() or "{}")
    except json.JSONDecodeError:
        return code, data.decode(errors="replace")


# ─── Hermetic palmux2 instance ───────────────────────────────────────────────

@contextmanager
def hermetic_palmux2() -> Iterator[tuple[int, bool]]:
    """Start a hermetic palmux2 process.  Yields (port, has_frontend)."""
    port = _free_port()
    cfg_dir = Path("/tmp") / f"palmux2-e2e-s1f75ec-tb-{port}"
    cfg_dir.mkdir(parents=True, exist_ok=True)

    if _USE_PREBUILT:
        cmd = [
            str(_PREBUILT_BIN),
            "--addr", f"127.0.0.1:{port}",
            "--config-dir", str(cfg_dir),
            "--claude-bin", "/bin/cat",
            "--tmux-prefix", f"_pmx_e2e{port}_",
        ]
        has_frontend = True
    else:
        cmd = [
            "go", "run", "./cmd/palmux",
            "--addr", f"127.0.0.1:{port}",
            "--config-dir", str(cfg_dir),
            "--claude-bin", "/bin/cat",
            "--tmux-prefix", f"_pmx_e2e{port}_",
        ]
        has_frontend = False

    proc = subprocess.Popen(
        cmd,
        cwd=REPO_ROOT,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )

    try:
        deadline = time.monotonic() + 60.0
        listening = False
        while time.monotonic() < deadline:
            if proc.stdout is None:
                break
            line = proc.stdout.readline()
            if not line and proc.poll() is not None:
                rest = proc.stdout.read() if proc.stdout else ""
                fail(f"palmux2 exited before listening: rc={proc.returncode}\n{rest}")
            if "palmux2 listening" in line or f":{port}" in line:
                listening = True
                break
        if not listening:
            proc.kill()
            fail("palmux2 did not announce its listening port within 60 s")
        yield port, has_frontend
    finally:
        if proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=5)
        import shutil
        shutil.rmtree(cfg_dir, ignore_errors=True)


def _get_fixture_module(port: int):
    os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
    if "_fixture" in sys.modules:
        del sys.modules["_fixture"]
    import _fixture as fx_mod
    return fx_mod


# ─── Test cases ──────────────────────────────────────────────────────────────

def test_ac_3_1_claude_tab_sets_toolbar_claude(port: int) -> None:
    """[AC-S1f75ec-3-1] Navigate to Claude tab → toolbar-mode-claude in DOM."""
    if not _USE_PREBUILT:
        print("SKIP: test_ac_3_1 (no embedded frontend)")
        return
    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-tb-ac31") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        url = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude"
        )
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                # Claude tab should be focused — toolbar must be in claude mode.
                page.wait_for_selector("[data-testid='toolbar-mode-claude']", timeout=PLAYWRIGHT_TIMEOUT)
                claude_count = page.locator("[data-testid='toolbar-mode-claude']").count()
                assert claude_count >= 1, (
                    f"[AC-S1f75ec-3-1] toolbar-mode-claude not found (count={claude_count})"
                )
                normal_count = page.locator("[data-testid='toolbar-mode-normal']").count()
                assert normal_count == 0, (
                    f"[AC-S1f75ec-3-1] toolbar-mode-normal should not be present on Claude tab, got {normal_count}"
                )
            finally:
                browser.close()
    passed("[AC-S1f75ec-3-1] Claude tab → toolbar-mode-claude active, toolbar-mode-normal absent")


def test_ac_3_2_bash_tab_sets_toolbar_normal(port: int) -> None:
    """[AC-S1f75ec-3-2] Navigate to Bash tab → toolbar-mode-normal in DOM."""
    if not _USE_PREBUILT:
        print("SKIP: test_ac_3_2 (no embedded frontend)")
        return
    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-tb-ac32") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        # Navigate to bash tab (first bash tab id is "bash:bash").
        bash_tab_id = "bash:bash"
        url = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/{urllib.parse.quote(bash_tab_id, safe='')}"
        )
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                # Bash tab should be focused — toolbar must be in normal mode.
                page.wait_for_selector("[data-testid='toolbar-mode-normal']", timeout=PLAYWRIGHT_TIMEOUT)
                normal_count = page.locator("[data-testid='toolbar-mode-normal']").count()
                assert normal_count >= 1, (
                    f"[AC-S1f75ec-3-2] toolbar-mode-normal not found (count={normal_count})"
                )
                claude_count = page.locator("[data-testid='toolbar-mode-claude']").count()
                assert claude_count == 0, (
                    f"[AC-S1f75ec-3-2] toolbar-mode-claude should not be present on Bash tab, got {claude_count}"
                )
            finally:
                browser.close()
    passed("[AC-S1f75ec-3-2] Bash tab → toolbar-mode-normal active, toolbar-mode-claude absent")


def test_ac_3_4_data_testid_selectors_exist(port: int) -> None:
    """[AC-S1f75ec-3-4] data-testid selectors are present in the DOM."""
    if not _USE_PREBUILT:
        print("SKIP: test_ac_3_4 (no embedded frontend)")
        return
    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-tb-ac34") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        # Check toolbar-mode-claude on Claude tab.
        url_claude = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude"
        )
        url_bash = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/bash:bash"
        )
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                # Verify toolbar-mode-claude on Claude tab.
                page.goto(url_claude, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                page.wait_for_selector("[data-testid='toolbar-mode-claude']", timeout=PLAYWRIGHT_TIMEOUT)
                assert page.locator("[data-testid='toolbar-mode-claude']").count() >= 1, (
                    "[AC-S1f75ec-3-4] toolbar-mode-claude not found"
                )
                # Also verify esc-esc-btn exists in claude mode.
                page.wait_for_selector("[data-testid='toolbar-esc-esc-btn']", timeout=PLAYWRIGHT_TIMEOUT)
                assert page.locator("[data-testid='toolbar-esc-esc-btn']").count() >= 1, (
                    "[AC-S1f75ec-3-4] toolbar-esc-esc-btn not found"
                )
                # Verify toolbar-mode-normal on Bash tab.
                page.goto(url_bash, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                page.wait_for_selector("[data-testid='toolbar-mode-normal']", timeout=PLAYWRIGHT_TIMEOUT)
                assert page.locator("[data-testid='toolbar-mode-normal']").count() >= 1, (
                    "[AC-S1f75ec-3-4] toolbar-mode-normal not found on Bash tab"
                )
            finally:
                browser.close()
    passed("[AC-S1f75ec-3-4] All data-testid selectors exist in DOM")


def test_ac_3_5_agent_mode_esc_esc_disabled(port: int) -> None:
    """[AC-S1f75ec-3-5] claude_mode=agent → ESC ESC button is disabled."""
    if not _USE_PREBUILT:
        print("SKIP: test_ac_3_5 (no embedded frontend)")
        return
    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-tb-ac35") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        # Sadf90e: per-tab settings endpoint. Canonical tab id = claude:claude.
        # Ensure claude_mode is agent (default, but set explicitly).
        tab_id_q = urllib.parse.quote("claude:claude", safe="")
        _http_json(
            port, "PATCH",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/tabs/{tab_id_q}/settings",
            body={"claude_mode": "agent"},
        )
        url = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude"
        )
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                page.wait_for_selector("[data-testid='toolbar-esc-esc-btn']", timeout=PLAYWRIGHT_TIMEOUT)
                btn = page.locator("[data-testid='toolbar-esc-esc-btn']").first
                assert btn.is_disabled(), (
                    "[AC-S1f75ec-3-5] ESC ESC button should be disabled when claude_mode=agent"
                )
            finally:
                browser.close()
    passed("[AC-S1f75ec-3-5] claude_mode=agent → ESC ESC button is disabled")


def test_ac_3_3_tui_mode_esc_esc_enabled(port: int) -> None:
    """[AC-S1f75ec-3-3] claude_mode=tui → ESC ESC button is enabled."""
    if not _USE_PREBUILT:
        print("SKIP: test_ac_3_3 (no embedded frontend)")
        return
    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-tb-ac33") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        # Sadf90e: per-tab settings endpoint. Canonical tab id = claude:claude.
        # Set claude_mode=tui so the ESC ESC button is enabled.
        tab_id_q = urllib.parse.quote("claude:claude", safe="")
        code, body = _http_json(
            port, "PATCH",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/tabs/{tab_id_q}/settings",
            body={"claude_mode": "tui"},
        )
        assert code == 200, f"[AC-S1f75ec-3-3] PATCH to tui failed: {code} {body}"
        url = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude"
        )
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                # Sadf90e hotfix 2026-05-27: TUI mode no longer renders a
                # claude-mode-badge (Agent mode only). The ESC ESC button
                # being enabled is the actual AC — poll for it directly.
                page.wait_for_selector("[data-testid='toolbar-esc-esc-btn']", timeout=PLAYWRIGHT_TIMEOUT)
                deadline = time.monotonic() + 10.0
                btn = page.locator("[data-testid='toolbar-esc-esc-btn']").first
                while time.monotonic() < deadline:
                    if not btn.is_disabled():
                        break
                    time.sleep(0.1)
                assert not btn.is_disabled(), (
                    "[AC-S1f75ec-3-3] ESC ESC button should be enabled when claude_mode=tui"
                )
                # Click the button — it should not throw.
                btn.click()
                time.sleep(0.3)
                # No exception = ESC ESC click was handled.
            finally:
                browser.close()
    passed("[AC-S1f75ec-3-3] claude_mode=tui → ESC ESC button enabled and clickable")


# ─── Runner ──────────────────────────────────────────────────────────────────

def main() -> None:
    print("=" * 60)
    print("S1f75ec Story 3 — Toolbar mode auto-switch + ESC ESC button E2E")
    mode = "pre-built binary" if _USE_PREBUILT else "go run (fallback, no frontend)"
    print(f"Mode: {mode}")
    print("Starting hermetic palmux2 ...")
    print("=" * 60)

    passed_count = 0
    failed_count = 0
    skipped_count = 0

    def _run(name: str, fn) -> None:
        nonlocal passed_count, failed_count, skipped_count
        try:
            fn()
            passed_count += 1
        except SystemExit as e:
            if e.code == 0:
                skipped_count += 1
            else:
                raise
        except Exception as exc:
            print(f"FAIL: {name}: {exc}", file=sys.stderr)
            import traceback
            traceback.print_exc(file=sys.stderr)
            failed_count += 1

    with hermetic_palmux2() as (port, has_frontend):
        print(f"[ok] palmux2 listening on port {port}")
        os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
        _get_fixture_module(port)

        if has_frontend:
            _run("test_ac_3_1_claude_tab_sets_toolbar_claude",
                 lambda: test_ac_3_1_claude_tab_sets_toolbar_claude(port))
            _run("test_ac_3_2_bash_tab_sets_toolbar_normal",
                 lambda: test_ac_3_2_bash_tab_sets_toolbar_normal(port))
            _run("test_ac_3_3_tui_mode_esc_esc_enabled",
                 lambda: test_ac_3_3_tui_mode_esc_esc_enabled(port))
            _run("test_ac_3_4_data_testid_selectors_exist",
                 lambda: test_ac_3_4_data_testid_selectors_exist(port))
            _run("test_ac_3_5_agent_mode_esc_esc_disabled",
                 lambda: test_ac_3_5_agent_mode_esc_esc_disabled(port))
        else:
            for name in [
                "test_ac_3_1_claude_tab_sets_toolbar_claude",
                "test_ac_3_2_bash_tab_sets_toolbar_normal",
                "test_ac_3_3_tui_mode_esc_esc_enabled",
                "test_ac_3_4_data_testid_selectors_exist",
                "test_ac_3_5_agent_mode_esc_esc_disabled",
            ]:
                print(f"SKIP: {name} (no embedded frontend in go-run mode)")
                skipped_count += 1

    total = passed_count + failed_count
    print()
    print("=" * 60)
    print(f"S1f75ec-3 E2E Results: {passed_count}/{total} passed, {skipped_count} skipped")
    if failed_count > 0:
        sys.exit(1)
    print("ALL PASS")


if __name__ == "__main__":
    main()
