#!/usr/bin/env python3
"""Sprint S1f75ec Story 3 — Toolbar mock tests.

Mock-style tests for Toolbar auto-switch and ESC ESC button.
Uses Playwright route interception to mock GET /settings and verify
the toolbar responds correctly.

Exit code 0 = ALL PASS / ALL SKIP. Run standalone:
  python3 tests/e2e/s1f75ec_toolbar_mock.py
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
    cfg_dir = Path("/tmp") / f"palmux2-e2e-s1f75ec-tbmock-{port}"
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


# ─── Mock tests ──────────────────────────────────────────────────────────────

def test_mock_toolbar_auto_switches_on_tab_focus(port: int) -> None:
    """Mock: visiting Claude tab auto-switches toolbar to claude mode.

    Uses a real palmux2 process but verifies that navigating between Claude
    and Bash tabs automatically switches the toolbar data-testid attribute.
    """
    if not _USE_PREBUILT:
        print("SKIP: test_mock_toolbar_auto_switches_on_tab_focus (no embedded frontend)")
        return

    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)

    with fx.palmux2_test_fixture("s1f75ec-tbmock-autoswitch") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
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

                # Start on Claude tab.
                page.goto(url_claude, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                page.wait_for_selector("[data-testid='toolbar-mode-claude']", timeout=PLAYWRIGHT_TIMEOUT)
                assert page.locator("[data-testid='toolbar-mode-claude']").count() >= 1, (
                    "[mock auto-switch] Expected toolbar-mode-claude on Claude tab"
                )
                assert page.locator("[data-testid='toolbar-mode-normal']").count() == 0, (
                    "[mock auto-switch] toolbar-mode-normal should not be present on Claude tab"
                )

                # Navigate to Bash tab.
                page.goto(url_bash, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                page.wait_for_selector("[data-testid='toolbar-mode-normal']", timeout=PLAYWRIGHT_TIMEOUT)
                assert page.locator("[data-testid='toolbar-mode-normal']").count() >= 1, (
                    "[mock auto-switch] Expected toolbar-mode-normal on Bash tab"
                )
                assert page.locator("[data-testid='toolbar-mode-claude']").count() == 0, (
                    "[mock auto-switch] toolbar-mode-claude should not be present on Bash tab"
                )
            finally:
                browser.close()

    passed("[mock] Toolbar auto-switches between normal/claude modes on tab navigation")


def test_mock_esc_esc_btn_wired_on_tui(port: int) -> None:
    """Mock: GET /settings returns tui → ESC ESC button is enabled.

    Uses route interception to mock GET /settings for the branch to return
    claude_mode=tui, then verifies the ESC ESC button is enabled.
    """
    if not _USE_PREBUILT:
        print("SKIP: test_mock_esc_esc_btn_wired_on_tui (no embedded frontend)")
        return

    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)

    with fx.palmux2_test_fixture("s1f75ec-tbmock-esc") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        url = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude"
        )
        settings_path = (
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/settings"
        )

        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                context = browser.new_context()
                page = context.new_page()

                # Mock GET /settings to return tui.
                def on_route(route, request):
                    if request.method == "GET" and settings_path in request.url:
                        route.fulfill(
                            status=200,
                            content_type="application/json",
                            body=json.dumps({"claude_mode": "tui"}),
                        )
                    else:
                        route.continue_()

                page.route("**" + settings_path, on_route)

                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                page.wait_for_selector("[data-testid='toolbar-esc-esc-btn']", timeout=PLAYWRIGHT_TIMEOUT)
                btn = page.locator("[data-testid='toolbar-esc-esc-btn']").first
                # With mocked tui settings, button should be enabled.
                assert not btn.is_disabled(), (
                    "[mock esc-esc] ESC ESC button should be enabled when GET /settings returns claude_mode=tui"
                )
            finally:
                browser.close()

    passed("[mock] GET /settings returning tui → ESC ESC button enabled")


def test_mock_esc_esc_btn_disabled_on_agent(port: int) -> None:
    """Mock: GET /settings returns agent → ESC ESC button is disabled."""
    if not _USE_PREBUILT:
        print("SKIP: test_mock_esc_esc_btn_disabled_on_agent (no embedded frontend)")
        return

    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)

    with fx.palmux2_test_fixture("s1f75ec-tbmock-agent") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        url = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude"
        )
        settings_path = (
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/settings"
        )

        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                context = browser.new_context()
                page = context.new_page()

                # Mock GET /settings to return agent.
                def on_route(route, request):
                    if request.method == "GET" and settings_path in request.url:
                        route.fulfill(
                            status=200,
                            content_type="application/json",
                            body=json.dumps({"claude_mode": "agent"}),
                        )
                    else:
                        route.continue_()

                page.route("**" + settings_path, on_route)

                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                page.wait_for_selector("[data-testid='toolbar-esc-esc-btn']", timeout=PLAYWRIGHT_TIMEOUT)
                btn = page.locator("[data-testid='toolbar-esc-esc-btn']").first
                assert btn.is_disabled(), (
                    "[mock esc-esc] ESC ESC button should be disabled when GET /settings returns claude_mode=agent"
                )
            finally:
                browser.close()

    passed("[mock] GET /settings returning agent → ESC ESC button disabled")


# ─── Runner ──────────────────────────────────────────────────────────────────

def main() -> None:
    print("=" * 60)
    print("S1f75ec Story 3 — Toolbar mock tests")
    mode = "pre-built binary" if _USE_PREBUILT else "go run (fallback, no frontend)"
    print(f"Mode: {mode}")
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

    if not _USE_PREBUILT:
        print("SKIP: all mock tests (no embedded frontend in go-run mode)")
        print("ALL SKIP")
        return

    with hermetic_palmux2() as (port, has_frontend):
        print(f"[ok] palmux2 listening on port {port}")
        os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
        _get_fixture_module(port)

        _run("test_mock_toolbar_auto_switches_on_tab_focus",
             lambda: test_mock_toolbar_auto_switches_on_tab_focus(port))
        _run("test_mock_esc_esc_btn_wired_on_tui",
             lambda: test_mock_esc_esc_btn_wired_on_tui(port))
        _run("test_mock_esc_esc_btn_disabled_on_agent",
             lambda: test_mock_esc_esc_btn_disabled_on_agent(port))

    total = passed_count + failed_count
    print()
    print("=" * 60)
    print(f"S1f75ec-3 mock Results: {passed_count}/{total} passed, {skipped_count} skipped")
    if failed_count > 0:
        sys.exit(1)
    print("ALL PASS")


if __name__ == "__main__":
    main()
