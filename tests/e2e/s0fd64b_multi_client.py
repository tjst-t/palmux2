#!/usr/bin/env python3
"""Sprint S0fd64b Story 3 — Multi-client role coordination hermetic E2E.

Exercises the active/viewer role assignment when N WebSocket clients attach
to the same (repo, branch) claude-tui endpoint.  Uses a hermetic palmux2
instance started with ``--claude-bin /bin/cat``.

Acceptance criteria covered:
  [AC-S0fd64b-3-1] single client → role=active
  [AC-S0fd64b-3-2] second client → role=viewer, first stays active
  [AC-S0fd64b-3-3] viewer sends input → both receive role event (last-typed-wins)
  [AC-S0fd64b-3-4] active disconnects → viewer receives role=active

Exit code 0 = ALL PASS.  Run standalone:
  python3 tests/e2e/s0fd64b_multi_client.py
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

sys.path.insert(0, os.path.dirname(__file__))

REPO_ROOT = Path(__file__).resolve().parents[2]

_PREBUILT_BIN = REPO_ROOT / "bin" / "palmux"
_USE_PREBUILT = _PREBUILT_BIN.is_file()


# ─── Helpers ──────────────────────────────────────────────────────────────────

def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def passed(msg: str) -> None:
    print(f"PASS: {msg}")


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


# ─── Low-level HTTP helpers ────────────────────────────────────────────────────

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


# ─── Hermetic palmux2 fixture ──────────────────────────────────────────────────

@contextmanager
def hermetic_palmux2() -> Iterator[tuple[int, bool]]:
    """Start a hermetic palmux2 with --claude-bin /bin/cat.

    Yields (port, has_frontend).
    """
    port = _free_port()
    cfg_dir = Path("/tmp") / f"palmux2-s0fd64b3-{port}"
    cfg_dir.mkdir(parents=True, exist_ok=True)

    if _USE_PREBUILT:
        cmd = [
            str(_PREBUILT_BIN),
            "--addr", f"127.0.0.1:{port}",
            "--config-dir", str(cfg_dir),
            "--claude-bin", "/bin/cat",
            "--tmux-prefix", f"_pmx_s0fd64b3_{port}_",
        ]
        has_frontend = True
    else:
        cmd = [
            "go", "run", "./cmd/palmux",
            "--addr", f"127.0.0.1:{port}",
            "--config-dir", str(cfg_dir),
            "--claude-bin", "/bin/cat",
            "--tmux-prefix", f"_pmx_s0fd64b3_{port}_",
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


# ─── WS helpers ───────────────────────────────────────────────────────────────

def _attach_uri(port: int, repo_id: str, branch_id: str, mode: str = "") -> str:
    # Sadf90e: per-tab tui endpoint, canonical tab is claude:claude.
    tab_id_q = urllib.parse.quote("claude:claude", safe="")
    base = (
        f"ws://localhost:{port}"
        f"/api/repos/{urllib.parse.quote(repo_id)}"
        f"/branches/{urllib.parse.quote(branch_id)}/tabs/{tab_id_q}/tui/attach"
    )
    if mode:
        base += f"?mode={mode}"
    return base


async def _collect_role(ws, timeout: float = 5.0) -> dict | None:
    """Read frames until a {type:"role"} event arrives or timeout."""
    deadline = asyncio.get_event_loop().time() + timeout
    while asyncio.get_event_loop().time() < deadline:
        remaining = deadline - asyncio.get_event_loop().time()
        if remaining <= 0:
            break
        try:
            msg = await asyncio.wait_for(ws.recv(), timeout=min(0.5, remaining))
        except asyncio.TimeoutError:
            continue
        if isinstance(msg, bytes):
            continue  # PTY bytes — skip
        try:
            obj = json.loads(msg)
        except json.JSONDecodeError:
            continue
        if obj.get("type") == "role":
            return obj
    return None


# ─── Test cases ───────────────────────────────────────────────────────────────

def test_single_client_active(port: int) -> None:
    """[AC-S0fd64b-3-1] Single client → role=active within 5 s."""
    try:
        import websockets  # noqa: F401
    except ImportError:
        print("SKIP: websockets not installed")
        return

    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s0fd64b3-single") as fixture:
        repo_id = fixture.repo_id
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        async def _run() -> dict | None:
            import websockets
            uri = _attach_uri(port, repo_id, branch_id)
            async with websockets.connect(uri, max_size=None) as ws:
                return await _collect_role(ws, timeout=5.0)

        ev = asyncio.run(_run())
        assert ev is not None, "[AC-S0fd64b-3-1] no role event received"
        assert ev.get("role") == "active", (
            f"[AC-S0fd64b-3-1] expected role=active, got {ev!r}"
        )
        assert isinstance(ev.get("since"), int) and ev["since"] > 0, (
            f"[AC-S0fd64b-3-1] 'since' must be a positive int, got {ev.get('since')!r}"
        )
    passed("[AC-S0fd64b-3-1] single client → role=active")


def test_second_client_viewer(port: int) -> None:
    """[AC-S0fd64b-3-2] First client is active; second is viewer."""
    try:
        import websockets  # noqa: F401
    except ImportError:
        print("SKIP: websockets not installed")
        return

    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s0fd64b3-viewer") as fixture:
        repo_id = fixture.repo_id
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        async def _run() -> tuple[dict | None, dict | None]:
            import websockets
            uri = _attach_uri(port, repo_id, branch_id)
            async with websockets.connect(uri, max_size=None) as ws_a:
                ev_a = await _collect_role(ws_a, timeout=5.0)
                # Open a second connection while the first is still open.
                async with websockets.connect(uri, max_size=None) as ws_b:
                    ev_b = await _collect_role(ws_b, timeout=5.0)
                    return ev_a, ev_b

        ev_a, ev_b = asyncio.run(_run())
        assert ev_a is not None, "[AC-S0fd64b-3-2] client A: no role event"
        assert ev_b is not None, "[AC-S0fd64b-3-2] client B: no role event"
        assert ev_a["role"] == "active", (
            f"[AC-S0fd64b-3-2] client A expected active, got {ev_a['role']!r}"
        )
        assert ev_b["role"] == "viewer", (
            f"[AC-S0fd64b-3-2] client B expected viewer, got {ev_b['role']!r}"
        )
    passed("[AC-S0fd64b-3-2] second client → role=viewer; first stays active")


def test_last_typed_wins(port: int) -> None:
    """[AC-S0fd64b-3-3] Viewer sends input → both receive role transition."""
    try:
        import websockets  # noqa: F401
    except ImportError:
        print("SKIP: websockets not installed")
        return

    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s0fd64b3-ltw") as fixture:
        repo_id = fixture.repo_id
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        async def _run() -> tuple[dict | None, dict | None]:
            import websockets
            uri = _attach_uri(port, repo_id, branch_id)
            async with websockets.connect(uri, max_size=None) as ws_a:
                await _collect_role(ws_a, timeout=5.0)  # consume initial active
                async with websockets.connect(uri, max_size=None) as ws_b:
                    await _collect_role(ws_b, timeout=5.0)  # consume initial viewer

                    # Client B (viewer) sends input — should trigger role transfer.
                    await ws_b.send(b"hello\n")

                    # Collect the next role events on both connections.
                    ev_a, ev_b = await asyncio.gather(
                        _collect_role(ws_a, timeout=3.0),
                        _collect_role(ws_b, timeout=3.0),
                    )
                    return ev_a, ev_b

        ev_a, ev_b = asyncio.run(_run())
        assert ev_a is not None, (
            "[AC-S0fd64b-3-3] client A: no role event after B sends input"
        )
        assert ev_b is not None, (
            "[AC-S0fd64b-3-3] client B: no role event after sending input"
        )
        assert ev_a["role"] == "viewer", (
            f"[AC-S0fd64b-3-3] client A expected viewer after B sends, got {ev_a['role']!r}"
        )
        assert ev_b["role"] == "active", (
            f"[AC-S0fd64b-3-3] client B expected active after sending, got {ev_b['role']!r}"
        )
    passed(
        "[AC-S0fd64b-3-3] viewer sends input → both clients get role transition "
        "(A→viewer, B→active)"
    )


def test_active_disconnect_promotes_viewer(port: int) -> None:
    """[AC-S0fd64b-3-4] Active disconnects → remaining viewer becomes active."""
    try:
        import websockets  # noqa: F401
    except ImportError:
        print("SKIP: websockets not installed")
        return

    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s0fd64b3-promote") as fixture:
        repo_id = fixture.repo_id
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        async def _run() -> dict | None:
            import websockets
            uri = _attach_uri(port, repo_id, branch_id)
            # Open connections manually (not as context managers) so we can close
            # ws_a while keeping ws_b alive.
            ws_a = await websockets.connect(uri, max_size=None)
            ws_b = await websockets.connect(uri, max_size=None)
            try:
                ev_a = await _collect_role(ws_a, timeout=5.0)
                assert ev_a is not None and ev_a["role"] == "active", (
                    f"ws_a expected active, got {ev_a!r}"
                )
                ev_b = await _collect_role(ws_b, timeout=5.0)
                assert ev_b is not None and ev_b["role"] == "viewer", (
                    f"ws_b expected viewer, got {ev_b!r}"
                )
                # Close the active client (ws_a).
                await ws_a.close()
                ws_a = None
                # ws_b should now receive role=active.
                return await _collect_role(ws_b, timeout=3.0)
            finally:
                if ws_a is not None:
                    await ws_a.close()
                await ws_b.close()

        ev_b = asyncio.run(_run())
        assert ev_b is not None, (
            "[AC-S0fd64b-3-4] client B: no role event after active disconnects"
        )
        assert ev_b["role"] == "active", (
            f"[AC-S0fd64b-3-4] expected B to become active, got {ev_b['role']!r}"
        )
    passed("[AC-S0fd64b-3-4] active disconnects → viewer promoted to active")


def test_raw_mode_role_event(port: int) -> None:
    """[AC-S0fd64b-3-1] Raw mode also delivers role events as text frames."""
    try:
        import websockets  # noqa: F401
    except ImportError:
        print("SKIP: websockets not installed")
        return

    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s0fd64b3-raw-role") as fixture:
        repo_id = fixture.repo_id
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        async def _run() -> dict | None:
            import websockets
            # Raw mode (no ?mode=grid).
            uri = _attach_uri(port, repo_id, branch_id)
            async with websockets.connect(uri, max_size=None) as ws:
                return await _collect_role(ws, timeout=5.0)

        ev = asyncio.run(_run())
        assert ev is not None, (
            "[AC-S0fd64b-3-1] raw mode: no role event received"
        )
        assert ev.get("role") == "active", (
            f"[AC-S0fd64b-3-1] raw mode: expected role=active, got {ev!r}"
        )
    passed("[AC-S0fd64b-3-1] raw mode delivers role events as text frames")


# ─── Runner ───────────────────────────────────────────────────────────────────

def main() -> None:
    print("=" * 60)
    print("S0fd64b Story 3 — Multi-client role coordination hermetic E2E")
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

    with hermetic_palmux2() as (port, _has_frontend):
        print(f"[ok] palmux2 listening on port {port}")
        os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
        _get_fixture_module(port)

        _run(
            "test_single_client_active",
            lambda: test_single_client_active(port),
        )
        _run(
            "test_second_client_viewer",
            lambda: test_second_client_viewer(port),
        )
        _run(
            "test_last_typed_wins",
            lambda: test_last_typed_wins(port),
        )
        _run(
            "test_active_disconnect_promotes_viewer",
            lambda: test_active_disconnect_promotes_viewer(port),
        )
        _run(
            "test_raw_mode_role_event",
            lambda: test_raw_mode_role_event(port),
        )

    total = passed_count + failed_count
    print()
    print("=" * 60)
    print(f"S0fd64b-3 Multi-client Results: {passed_count}/{total} passed")
    if failed_count > 0:
        sys.exit(1)
    print("ALL PASS")


if __name__ == "__main__":
    main()
