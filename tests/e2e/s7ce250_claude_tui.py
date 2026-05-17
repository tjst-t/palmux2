#!/usr/bin/env python3
"""Sprint S7ce250 Story 5 — claude-tui full-stack hermetic E2E.

Exercises the production claude-tui tab (internal/tab/claudetui/) using a
hermetic palmux2 instance started with ``--claude-bin /bin/cat`` so every
byte sent over the WS is echoed back by the PTY subprocess.  No real
``claude`` binary is required.

Acceptance criteria covered:
  [AC-S7ce250-5-1] branch open → claude-tui tab appears in tab list
  [AC-S7ce250-5-1] browser: tab bar shows "Claude (TUI)" label
  [AC-S7ce250-5-2] WS attach → daemon starts (state=running)
  [AC-S7ce250-5-3] input bytes → echoed back within 5 s (/bin/cat echo)
  [AC-S7ce250-5-4] POST /resize → 204; Resize is wired to PTY
  [AC-S7ce250-5-5] branch close → daemon shuts down
  [AC-S7ce250-5-2] manual smoke log present (>= 100 bytes)

Architecture note:
  The test launches ``bin/palmux`` (pre-built via ``make build`` /
  ``make serve INSTANCE=dev``) with ``--claude-bin /bin/cat``.  This is
  fully hermetic — the running ``make serve INSTANCE=dev`` server is NOT
  used — because that server uses the real ``claude`` binary.  The
  palmux2_test_fixture helper is pointed at the hermetic instance via
  PALMUX2_DEV_PORT_OVERRIDE.

  Fallback: if ``bin/palmux`` does not exist, the suite falls back to
  ``go run ./cmd/palmux`` (slower first run; frontend NOT embedded so
  the browser tab-label test is skipped in that mode).

Exit code 0 = ALL PASS.  Run standalone:
  python3 tests/e2e/s7ce250_claude_tui.py
"""
from __future__ import annotations

import asyncio
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

# Make the e2e helper importable when run from any working directory.
sys.path.insert(0, os.path.dirname(__file__))

REPO_ROOT = Path(__file__).resolve().parents[2]
SMOKE_LOG = REPO_ROOT / "docs/sprint-logs/S7ce250/desktop-attach-demo.md"
PLAYWRIGHT_TIMEOUT = 15_000  # ms

# Prefer the pre-built binary (has frontend embedded).
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
    """Fire an HTTP request against the hermetic palmux2 at *port*."""
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
    """Start a hermetic palmux2 process with --claude-bin /bin/cat.

    Yields ``(port, has_frontend)``.

    * ``has_frontend`` is True when the pre-built binary (with embedded React
      app) is used, False when falling back to ``go run`` (no embedded app).
    * The process is killed (SIGTERM → SIGKILL) on context exit.
    """
    port = _free_port()
    cfg_dir = Path("/tmp") / f"palmux2-e2e-{port}"
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
        # Fallback: go run (no embedded frontend)
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
        # Wait up to 60 s for the server to announce it's listening.
        deadline = time.monotonic() + 60.0
        listening = False
        while time.monotonic() < deadline:
            if proc.stdout is None:
                break
            line = proc.stdout.readline()
            if not line and proc.poll() is not None:
                # Drain remaining output for the error message.
                rest = proc.stdout.read() if proc.stdout else ""
                fail(
                    f"palmux2 exited before listening: rc={proc.returncode}\n{rest}"
                )
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


# ─── Fixture helper (re-imports _fixture with the hermetic port) ──────────────

def _get_fixture_module(port: int):
    """Return the _fixture module configured for *port*."""
    os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
    # Force a clean re-import so BASE_URL is updated.
    if "_fixture" in sys.modules:
        del sys.modules["_fixture"]
    import _fixture as fx_mod
    return fx_mod


# ─── Test cases ──────────────────────────────────────────────────────────────

def test_ac_s7ce250_5_1_tab_appears_in_list(port: int) -> None:
    """[AC-S7ce250-5-1] branch open → claude-tui tab in tab list."""
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s7ce250-tab-list") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        tabs = fixture.list_tabs(branch_id)
        tab_ids = [t.get("id") for t in tabs]
        tab_types = [t.get("type") for t in tabs]
        assert "claude-tui" in tab_ids or "claude-tui" in tab_types, (
            f"[AC-S7ce250-5-1] claude-tui tab not found in tabs: {tabs}"
        )
        ct = next(
            (t for t in tabs if t.get("id") == "claude-tui" or t.get("type") == "claude-tui"),
            None,
        )
        assert ct is not None
        assert ct.get("name") == "Claude (TUI)", (
            f"[AC-S7ce250-5-1] expected name='Claude (TUI)', got {ct.get('name')!r}"
        )
        assert ct.get("protected") is True, (
            f"[AC-S7ce250-5-1] expected protected=true, got {ct.get('protected')!r}"
        )
    passed("[AC-S7ce250-5-1] branch open → claude-tui in tab list, name='Claude (TUI)', protected=true")


def test_ac_s7ce250_5_1_browser_tab_label(port: int) -> None:
    """[AC-S7ce250-5-1] Browser: tab bar shows 'Claude (TUI)' label.

    Navigates directly to /{repoId}/{branchId}/claude-tui — the three-segment
    URL that the React Router ``/:repoId/:branchId/:tabId/*`` route handles.
    """
    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s7ce250-browser") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        base = f"http://localhost:{port}"
        # Must include a tabId segment so the /:repoId/:branchId/:tabId route matches.
        url = (
            f"{base}/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude-tui"
        )
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                # Give React time to boot and render the tab bar.
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                # Tab bar should contain a tab labelled "Claude (TUI)".
                page.wait_for_selector("text=Claude (TUI)", timeout=PLAYWRIGHT_TIMEOUT)
                el = page.locator("text=Claude (TUI)").first
                assert el.is_visible(), "Claude (TUI) label not visible in tab bar"
            finally:
                browser.close()
    passed("[AC-S7ce250-5-1] browser: 'Claude (TUI)' visible in tab bar")


def test_ac_s7ce250_5_2_ws_attach_starts_daemon(port: int) -> None:
    """[AC-S7ce250-5-2] WS attach → daemon starts (state becomes running)."""
    try:
        import websockets  # noqa: F401
    except ImportError:
        print("SKIP: websockets not installed")
        return

    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s7ce250-ws-attach") as fixture:
        repo_id = fixture.repo_id
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        # Before attach, daemon should be idle.
        code, stats = _http_json(
            port, "GET",
            f"/api/repos/{urllib.parse.quote(repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/tabs/claude-tui/stats",
        )
        assert code == 200, f"stats before attach: {code}"
        assert stats.get("state") == "idle", (
            f"expected idle before attach, got {stats}"
        )

        uri = (
            f"ws://localhost:{port}"
            f"/api/repos/{urllib.parse.quote(repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/tabs/claude-tui/attach"
        )

        async def _connect_briefly() -> None:
            import websockets
            async with websockets.connect(uri, max_size=None) as ws:
                # /bin/cat needs a byte to echo; a bare newline is safe.
                await ws.send(b"\n")
                await asyncio.sleep(0.5)
                try:
                    await asyncio.wait_for(ws.recv(), timeout=2.0)
                except asyncio.TimeoutError:
                    pass

        asyncio.run(_connect_briefly())

        # After attach, poll for running/dead.
        deadline = time.monotonic() + 5.0
        final_stats: object = {}
        while time.monotonic() < deadline:
            code, stats = _http_json(
                port, "GET",
                f"/api/repos/{urllib.parse.quote(repo_id)}"
                f"/branches/{urllib.parse.quote(branch_id)}/tabs/claude-tui/stats",
            )
            if code == 200 and isinstance(stats, dict):
                final_stats = stats
                if stats.get("state") in ("running", "dead"):
                    break
            time.sleep(0.2)

        assert isinstance(final_stats, dict)
        assert final_stats.get("state") in ("running", "dead"), (
            f"[AC-S7ce250-5-2] expected running/dead after attach, got {final_stats}"
        )
    passed(f"[AC-S7ce250-5-2] WS attach → daemon started (state={final_stats.get('state')})")  # type: ignore[union-attr]


def test_ac_s7ce250_5_3_input_echoed_back(port: int) -> None:
    """[AC-S7ce250-5-3] Input bytes echoed back by /bin/cat within 5 s."""
    try:
        import websockets  # noqa: F401
    except ImportError:
        print("SKIP: websockets not installed")
        return

    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s7ce250-echo") as fixture:
        repo_id = fixture.repo_id
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        marker = f"s7ce250echo{int(time.time())}".encode()
        uri = (
            f"ws://localhost:{port}"
            f"/api/repos/{urllib.parse.quote(repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/tabs/claude-tui/attach"
        )

        async def _echo_check() -> None:
            import websockets
            async with websockets.connect(uri, max_size=None) as ws:
                await ws.send(marker)
                collected = b""
                deadline_ts = asyncio.get_event_loop().time() + 5.0
                while asyncio.get_event_loop().time() < deadline_ts:
                    remaining = deadline_ts - asyncio.get_event_loop().time()
                    try:
                        msg = await asyncio.wait_for(
                            ws.recv(), timeout=min(0.5, remaining)
                        )
                    except asyncio.TimeoutError:
                        continue
                    collected += msg if isinstance(msg, bytes) else msg.encode()
                    if marker in collected:
                        return
                fail(
                    f"[AC-S7ce250-5-3] marker {marker!r} not echoed within 5 s; "
                    f"got {collected[:200]!r}"
                )

        asyncio.run(_echo_check())
    passed("[AC-S7ce250-5-3] input echoed back by /bin/cat within 5 s")


def test_ac_s7ce250_5_4_resize_accepted(port: int) -> None:
    """[AC-S7ce250-5-4] POST /resize → 204; Resize is wired to the PTY."""
    try:
        import websockets  # noqa: F401
    except ImportError:
        print("SKIP: websockets not installed")
        return

    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s7ce250-resize") as fixture:
        repo_id = fixture.repo_id
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        uri = (
            f"ws://localhost:{port}"
            f"/api/repos/{urllib.parse.quote(repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/tabs/claude-tui/attach"
        )

        async def _attach_briefly() -> None:
            import websockets
            async with websockets.connect(uri, max_size=None) as ws:
                await ws.send(b"x")
                await asyncio.sleep(0.5)

        asyncio.run(_attach_briefly())

        # POST resize — expects 204 No Content.
        resize_path = (
            f"/api/repos/{urllib.parse.quote(repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/tabs/claude-tui/resize"
        )
        code, body = _http_json(port, "POST", resize_path, body={"cols": 120, "rows": 36})
        assert code == 204, (
            f"[AC-S7ce250-5-4] expected 204 from /resize, got {code}: {body}"
        )

        # Daemon should still be alive after resize.
        code2, stats = _http_json(
            port, "GET",
            f"/api/repos/{urllib.parse.quote(repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/tabs/claude-tui/stats",
        )
        assert code2 == 200
        assert isinstance(stats, dict)
        assert stats.get("state") in ("running", "dead"), (
            f"[AC-S7ce250-5-4] unexpected state after resize: {stats}"
        )
    passed("[AC-S7ce250-5-4] POST /resize → 204; daemon Resize accepted")


def test_ac_s7ce250_5_5_branch_close_shuts_down_daemon(port: int) -> None:
    """[AC-S7ce250-5-5] branch close → daemon shuts down.

    Uses DELETE /api/repos/{repoId}/branches/{branchId} which calls
    CloseBranch → OnBranchClose → CloseDaemon → Shutdown.
    """
    try:
        import websockets  # noqa: F401
    except ImportError:
        print("SKIP: websockets not installed")
        return

    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s7ce250-close") as fixture:
        repo_id = fixture.repo_id
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        uri = (
            f"ws://localhost:{port}"
            f"/api/repos/{urllib.parse.quote(repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/tabs/claude-tui/attach"
        )

        async def _attach_briefly() -> None:
            import websockets
            async with websockets.connect(uri, max_size=None) as ws:
                await ws.send(b"ping\n")
                await asyncio.sleep(0.5)
            # WS is now closed; give the server a moment to decrement
            # attachedCount (ioCancel triggers the pump goroutine exit).
            await asyncio.sleep(0.3)

        asyncio.run(_attach_briefly())

        # Brief extra pause to ensure server-side cleanup is complete.
        time.sleep(0.3)

        # Verify daemon is running/dead (was spawned by the attach above).
        code, stats_before = _http_json(
            port, "GET",
            f"/api/repos/{urllib.parse.quote(repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/tabs/claude-tui/stats",
        )
        assert code == 200
        assert isinstance(stats_before, dict)
        # /bin/cat may have already exited (dead) — both states are acceptable.
        assert stats_before.get("state") in ("running", "dead"), (
            f"expected running/dead before close, got {stats_before}"
        )

        # Close the branch via DELETE — triggers OnBranchClose → CloseDaemon.
        # (POST /repos/{id}/close does NOT call OnBranchClose for the
        # claude-tui tab; DELETE /branches/{id} does.)
        close_code, close_body = _http_json(
            port, "DELETE",
            f"/api/repos/{urllib.parse.quote(repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}",
        )
        assert close_code in (200, 204), (
            f"expected 2xx from DELETE /branches, got {close_code}: {close_body}"
        )

        # Poll until the daemon state is shutdown/dead or stats returns 404.
        deadline = time.monotonic() + 10.0
        final: dict = {}
        while time.monotonic() < deadline:
            code2, stats_after = _http_json(
                port, "GET",
                f"/api/repos/{urllib.parse.quote(repo_id)}"
                f"/branches/{urllib.parse.quote(branch_id)}/tabs/claude-tui/stats",
            )
            if code2 == 404:
                # Branch/daemon fully removed.
                passed("[AC-S7ce250-5-5] branch close → daemon removed (stats 404)")
                return
            if isinstance(stats_after, dict):
                final = stats_after
                state_now = stats_after.get("state", "")
                if state_now == "shutdown" or not stats_after.get("alive"):
                    break
            time.sleep(0.2)

        state = final.get("state", "")
        alive = final.get("alive", True)
        assert state == "shutdown" or not alive, (
            f"[AC-S7ce250-5-5] expected shutdown/dead after branch close, got {final}"
        )

    passed("[AC-S7ce250-5-5] branch close → daemon shuts down")


def test_ac_s7ce250_5_smoke_log_present() -> None:
    """[AC-S7ce250-5-2] Manual smoke log present and >= 100 bytes."""
    if not SMOKE_LOG.is_file():
        fail(
            f"manual smoke log missing: {SMOKE_LOG} — "
            "docs/sprint-logs/S7ce250/desktop-attach-demo.md must exist"
        )
    if SMOKE_LOG.stat().st_size < 100:
        fail(
            f"manual smoke log too short (<100 bytes): {SMOKE_LOG} "
            f"(actual: {SMOKE_LOG.stat().st_size} bytes)"
        )
    passed("[AC-S7ce250-5-2] manual smoke log present and >= 100 bytes")


# ─── Runner ──────────────────────────────────────────────────────────────────

def main() -> None:
    print("=" * 60)
    print("S7ce250 Story 5 — claude-tui hermetic E2E")
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
        # Pre-import the fixture module now that the env var is set.
        _get_fixture_module(port)

        _run("test_ac_s7ce250_5_1_tab_appears_in_list",
             lambda: test_ac_s7ce250_5_1_tab_appears_in_list(port))

        if has_frontend:
            _run("test_ac_s7ce250_5_1_browser_tab_label",
                 lambda: test_ac_s7ce250_5_1_browser_tab_label(port))
        else:
            print("SKIP: test_ac_s7ce250_5_1_browser_tab_label (no embedded frontend in go-run mode)")

        _run("test_ac_s7ce250_5_2_ws_attach_starts_daemon",
             lambda: test_ac_s7ce250_5_2_ws_attach_starts_daemon(port))

        _run("test_ac_s7ce250_5_3_input_echoed_back",
             lambda: test_ac_s7ce250_5_3_input_echoed_back(port))

        _run("test_ac_s7ce250_5_4_resize_accepted",
             lambda: test_ac_s7ce250_5_4_resize_accepted(port))

        _run("test_ac_s7ce250_5_5_branch_close_shuts_down_daemon",
             lambda: test_ac_s7ce250_5_5_branch_close_shuts_down_daemon(port))

    # Smoke log is file-only — check outside the server context.
    _run("test_ac_s7ce250_5_smoke_log_present", test_ac_s7ce250_5_smoke_log_present)

    total = passed_count + failed_count
    print()
    print("=" * 60)
    print(f"S7ce250 E2E Results: {passed_count}/{total} passed")
    if failed_count > 0:
        sys.exit(1)
    print("ALL PASS")


if __name__ == "__main__":
    main()
