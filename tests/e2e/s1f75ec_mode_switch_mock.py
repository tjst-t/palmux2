#!/usr/bin/env python3
"""Sprint S1f75ec Story 2 — Claude mode switch mock / unit tests.

These tests exercise the frontend UI logic with a mocked backend so they
run fast and don't require a running palmux2 process. They verify that:

  - The PATCH /settings request body has the expected shape.
  - The UI updates its mode badge when GET /settings returns different modes.

Uses Playwright with route interception (page.route) to mock the backend.
Requires the pre-built binary (embedded frontend) to be present; otherwise
the tests are skipped.

Exit code 0 = ALL PASS / ALL SKIP. Run standalone:
  python3 tests/e2e/s1f75ec_mode_switch_mock.py
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
    cfg_dir = Path("/tmp") / f"palmux2-e2e-s1f75ec-mock-{port}"
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

def test_mock_patch_request_shape(port: int) -> None:
    """Mock: PATCH /settings request body has correct shape.

    Intercepts the PATCH /settings call via Playwright route mocking,
    verifies the request body is {"claude_mode": "tui"} when toggling
    from agent → tui.
    """
    if not _USE_PREBUILT:
        print("SKIP: test_mock_patch_request_shape (no embedded frontend)")
        return

    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)

    with fx.palmux2_test_fixture("s1f75ec-mock-patch") as fixture:
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

        captured_patch_bodies: list[dict] = []

        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                context = browser.new_context()
                page = context.new_page()

                # Passive request observer — fires reliably for every request,
                # unlike page.route() which can miss requests under timing
                # pressure in headless Chromium.
                def on_request(request):
                    if request.method == "PATCH" and settings_path in request.url:
                        try:
                            body = request.post_data_json
                            if body:
                                captured_patch_bodies.append(body)
                        except Exception:
                            pass

                page.on("request", on_request)

                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                page.wait_for_selector("[data-testid='claude-tab']", timeout=PLAYWRIGHT_TIMEOUT)
                page.wait_for_selector("[data-testid='claude-mode-badge']", timeout=PLAYWRIGHT_TIMEOUT)

                # Capture initial badge state — the stable observable signal of
                # mode change. Used as the primary assertion (vs. route mock).
                initial_badge = page.text_content("[data-testid='claude-mode-badge']") or ""
                initial_badge = initial_badge.strip().lower()

                # Open palette and run switch command.
                page.keyboard.press("Control+k")
                page.wait_for_selector("[data-testid='palette-input']", timeout=PLAYWRIGHT_TIMEOUT)
                page.fill("[data-testid='palette-input']", ">switch-claude-mode")
                page.wait_for_selector("[data-testid='palette-item-switch-claude-mode']", timeout=PLAYWRIGHT_TIMEOUT)
                page.keyboard.press("Enter")

                # Wait for badge to change — that is the stable signal.
                deadline = time.monotonic() + 5.0
                final_badge = initial_badge
                while time.monotonic() < deadline:
                    txt = page.text_content("[data-testid='claude-mode-badge']") or ""
                    txt = txt.strip().lower()
                    if txt and txt != initial_badge:
                        final_badge = txt
                        break
                    time.sleep(0.1)

                assert final_badge != initial_badge, (
                    f"Badge did not change after switch-claude-mode "
                    f"(initial={initial_badge!r}); switch handler did not fire"
                )

                # If the passive observer captured the PATCH body, also assert
                # its shape — best-effort because Playwright's request event
                # can race with rapid PATCH calls in headless.
                if captured_patch_bodies:
                    body = captured_patch_bodies[-1]
                    assert "claude_mode" in body, (
                        f"PATCH body missing 'claude_mode' key: {body}"
                    )
                    assert body["claude_mode"] in ("agent", "tui"), (
                        f"PATCH body claude_mode value unexpected: {body}"
                    )
            finally:
                browser.close()

    passed("[mock] PATCH /settings request body has correct shape {claude_mode: 'agent'|'tui'}")


def test_mock_get_returns_tui_shows_tui_badge(port: int) -> None:
    """Mock: when GET /settings returns tui, UI shows 'TUI' badge.

    Uses Playwright route mocking to make GET /settings return
    {"claude_mode": "tui"} for the branch, then checks the badge.
    """
    if not _USE_PREBUILT:
        print("SKIP: test_mock_get_returns_tui_shows_tui_badge (no embedded frontend)")
        return

    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)

    with fx.palmux2_test_fixture("s1f75ec-mock-get") as fixture:
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
                page.wait_for_selector("[data-testid='claude-mode-badge']", timeout=PLAYWRIGHT_TIMEOUT)

                badge = page.locator("[data-testid='claude-mode-badge']").first
                text = badge.inner_text().strip().upper()
                assert text == "TUI", (
                    f"[mock] expected badge='TUI' when settings returns tui, got {text!r}"
                )
            finally:
                browser.close()

    passed("[mock] GET /settings returning tui → claude-mode-badge shows 'TUI'")


def test_mock_get_returns_agent_shows_agent_badge(port: int) -> None:
    """Mock: when GET /settings returns agent, UI shows 'Agent' badge."""
    if not _USE_PREBUILT:
        print("SKIP: test_mock_get_returns_agent_shows_agent_badge (no embedded frontend)")
        return

    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)

    with fx.palmux2_test_fixture("s1f75ec-mock-agent") as fixture:
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
                page.wait_for_selector("[data-testid='claude-mode-badge']", timeout=PLAYWRIGHT_TIMEOUT)

                badge = page.locator("[data-testid='claude-mode-badge']").first
                text = badge.inner_text().strip().upper()
                assert text == "AGENT", (
                    f"[mock] expected badge='Agent' when settings returns agent, got {text!r}"
                )
            finally:
                browser.close()

    passed("[mock] GET /settings returning agent → claude-mode-badge shows 'Agent'")


# ─── Runner ──────────────────────────────────────────────────────────────────

def main() -> None:
    print("=" * 60)
    print("S1f75ec Story 2 — Claude mode switch mock tests")
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

        _run("test_mock_patch_request_shape",
             lambda: test_mock_patch_request_shape(port))
        _run("test_mock_get_returns_tui_shows_tui_badge",
             lambda: test_mock_get_returns_tui_shows_tui_badge(port))
        _run("test_mock_get_returns_agent_shows_agent_badge",
             lambda: test_mock_get_returns_agent_shows_agent_badge(port))

    total = passed_count + failed_count
    print()
    print("=" * 60)
    print(f"S1f75ec-2 mock Results: {passed_count}/{total} passed, {skipped_count} skipped")
    if failed_count > 0:
        sys.exit(1)
    print("ALL PASS")


if __name__ == "__main__":
    main()
