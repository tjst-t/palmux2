#!/usr/bin/env python3
"""Sprint S1f75ec Story 4 — Activity Inbox claudetui event rendering mock tests.

Tests the frontend rendering logic for claudetui.* notification types using
a mock palmux2 instance (no real claude-tui daemon).  Verifies:
  - The inbox renders data-testid='activity-inbox-event-claudetui' for
    claudetui.permission_prompt and claudetui.task_complete notification types
  - The "Open Claude" button is present on claudetui event rows
  - The data-testid='activity-inbox-event-list' container exists

Uses the hermetic palmux2 binary + REST API injection of fake notifications.

Acceptance criteria covered:
  [AC-S1f75ec-4-5] data-testid='activity-inbox-event-claudetui' in DOM

Exit code 0 = ALL PASS. Run standalone:
  python3 tests/e2e/s1f75ec_inbox_mock.py
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
    """Start a hermetic palmux2 process. Yields (port, has_frontend)."""
    port = _free_port()
    cfg_dir = Path("/tmp") / f"palmux2-inbox-mock-{port}"
    cfg_dir.mkdir(parents=True, exist_ok=True)

    if _USE_PREBUILT:
        cmd = [
            str(_PREBUILT_BIN),
            "--addr", f"127.0.0.1:{port}",
            "--config-dir", str(cfg_dir),
            "--claude-bin", "/bin/cat",
            "--tmux-prefix", f"_pmx_mock{port}_",
        ]
        has_frontend = True
    else:
        cmd = [
            "go", "run", "./cmd/palmux",
            "--addr", f"127.0.0.1:{port}",
            "--config-dir", str(cfg_dir),
            "--claude-bin", "/bin/cat",
            "--tmux-prefix", f"_pmx_mock{port}_",
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


def _inject_notification(port: int, repo_id: str, branch_id: str,
                          notif_type: str, title: str, message: str) -> None:
    """POST a fake notification directly to /api/notify (plain ingest path)."""
    body = {
        "repoId": repo_id,
        "branchId": branch_id,
        "type": notif_type,
        "title": title,
        "message": message,
    }
    code, resp = _http_json(port, "POST", "/api/notify", body)
    assert code in (200, 202), f"POST /api/notify returned {code}: {resp}"


# ─── Test: inbox renders claudetui.permission_prompt ─────────────────────────

def test_inbox_renders_permission_prompt(port: int) -> None:
    """data-testid='activity-inbox-event-claudetui' for permission_prompt type."""
    if not _USE_PREBUILT:
        print("SKIP: test_inbox_renders_permission_prompt (no embedded frontend)")
        return
    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-mock-perm") as fixture:
        repo_id = fixture.repo_id
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        # Inject a fake claudetui.permission_prompt notification.
        _inject_notification(
            port, repo_id, branch_id,
            "claudetui.permission_prompt",
            "claude-tui permission request",
            "Do you want me to read /etc/passwd?",
        )

        base_url = f"http://localhost:{port}"
        url = (
            f"{base_url}/{urllib.parse.quote(repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}/claude"
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

                # Open the Activity Inbox.
                page.click("button[aria-label='Activity inbox']",
                           timeout=PLAYWRIGHT_TIMEOUT)

                # The event list container should be present.
                page.wait_for_selector(
                    "[data-testid='activity-inbox-event-list']",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )

                # A claudetui event row should be present.
                page.wait_for_selector(
                    "[data-testid='activity-inbox-event-claudetui']",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                row = page.locator("[data-testid='activity-inbox-event-claudetui']").first
                assert row.is_visible(), (
                    "[AC-S1f75ec-4-5] claudetui event row not visible"
                )

                # "Open Claude" button should be present.
                open_btn = row.locator("button", has_text="Open Claude")
                assert open_btn.count() >= 1, (
                    "[AC-S1f75ec-4-5] 'Open Claude' button not found on claudetui row"
                )
            finally:
                browser.close()
    passed("[mock] claudetui.permission_prompt renders with data-testid + Open Claude button")


def test_inbox_renders_task_complete(port: int) -> None:
    """data-testid='activity-inbox-event-claudetui' for task_complete type."""
    if not _USE_PREBUILT:
        print("SKIP: test_inbox_renders_task_complete (no embedded frontend)")
        return
    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-mock-bel") as fixture:
        repo_id = fixture.repo_id
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        # Inject a fake claudetui.task_complete notification.
        _inject_notification(
            port, repo_id, branch_id,
            "claudetui.task_complete",
            "claude-tui task complete",
            "Task finished",
        )

        base_url = f"http://localhost:{port}"
        url = (
            f"{base_url}/{urllib.parse.quote(repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}/claude"
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

                page.click("button[aria-label='Activity inbox']",
                           timeout=PLAYWRIGHT_TIMEOUT)

                page.wait_for_selector(
                    "[data-testid='activity-inbox-event-claudetui']",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                row = page.locator("[data-testid='activity-inbox-event-claudetui']").first
                assert row.is_visible(), (
                    "[AC-S1f75ec-4-5] claudetui task_complete row not visible"
                )
            finally:
                browser.close()
    passed("[mock] claudetui.task_complete renders with data-testid='activity-inbox-event-claudetui'")


# ─── Runner ──────────────────────────────────────────────────────────────────

def main() -> None:
    print("=" * 60)
    print("S1f75ec Story 4 — Activity Inbox claudetui rendering mock tests")
    mode = "pre-built binary" if _USE_PREBUILT else "go run (fallback, no frontend)"
    print(f"Mode: {mode}")
    print("Starting hermetic palmux2 with --claude-bin /bin/cat ...")
    print("=" * 60)

    passed_count = 0
    failed_count = 0

    def _run(name: str, fn) -> None:
        nonlocal passed_count, failed_count
        try:
            fn()
            passed_count += 1
        except SystemExit:
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
            _run("test_inbox_renders_permission_prompt",
                 lambda: test_inbox_renders_permission_prompt(port))
            _run("test_inbox_renders_task_complete",
                 lambda: test_inbox_renders_task_complete(port))
        else:
            print("SKIP: test_inbox_renders_permission_prompt (no embedded frontend)")
            print("SKIP: test_inbox_renders_task_complete (no embedded frontend)")

    total = passed_count + failed_count
    print()
    print("=" * 60)
    print(f"S1f75ec Story 4 Mock Results: {passed_count}/{total} passed")
    if failed_count > 0:
        sys.exit(1)
    print("ALL PASS")


if __name__ == "__main__":
    main()
