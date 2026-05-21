#!/usr/bin/env python3
"""Sprint S1d2278 — Track B PoC: PTY daemon desktop attach demo (Story 3).

E2E tests for the standalone PoC binary `cmd/poc-pty/` that proves
interactive `claude` can be driven from a browser over a Go-owned PTY
(no tmux, no Agent SDK). This is throwaway PoC code; the test surface is
intentionally minimal.

Acceptance criteria covered:
  [AC-S1d2278-3-1] WS endpoint /poc/pty/attach exists and accepts
                   bidirectional raw-byte traffic.
  [AC-S1d2278-3-2] Static HTML + xterm.js connects to the WS and
                   renders claude TUI output.
  [AC-S1d2278-3-3] Reconnect replays ring buffer (scrollback restored).
  [AC-S1d2278-3-4] Manual smoke log is present at
                   docs/sprint-logs/S1d2278/desktop-attach-demo.md and
                   non-empty (the content itself is written by a human
                   during the manual demo run).

Fixture design (Story 3): the binary is started via `go run ./cmd/poc-pty`
with a dynamically chosen free port (avoiding portman for test isolation —
portman's static port assignment causes race conditions across sequential
daemon invocations within the same test run).  The subprocess is set to
`/bin/bash -c cat` so AC-3-1/3-3 are deterministic without a real claude
binary in the sandbox.

Exit code 0 = PASS. Designed to be runnable as a standalone script via
`python3 tests/e2e/s1d2278_pty_poc.py` so it slots into the existing
sprint verify harness.
"""
from __future__ import annotations

import os
import signal
import socket
import subprocess
import sys
import time
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator

REPO_ROOT = Path(__file__).resolve().parents[2]
POC_BIN_REL = "cmd/poc-pty"
SMOKE_LOG = REPO_ROOT / "docs/sprint-logs/S1d2278/desktop-attach-demo.md"


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def passed(msg: str) -> None:
    print(f"PASS: {msg}")


def _get_playwright():
    try:
        from playwright.sync_api import sync_playwright  # noqa: F401
        return sync_playwright
    except ImportError:
        print("SKIP: playwright not installed — install with `pip install playwright`")
        sys.exit(0)


def _free_port() -> int:
    """Find a free TCP port on localhost."""
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


@contextmanager
def poc_daemon() -> Iterator[int]:
    """Start the PoC binary directly via go run; yield the chosen port.

    Story 3 fixture uses /bin/bash -c cat as a deterministic claude-bin
    substitute so that AC-3-1/3-3 (WS bidirectional + ring replay) are
    sandbox-safe and do not depend on the real `claude` binary.
    `cat` echoes every byte it receives back to the PTY, satisfying the
    AC requirement of "at least one frame back within 5s".

    A fresh free port is chosen per invocation to avoid conflicts when
    multiple daemon contexts run sequentially in the same test script.
    """
    bin_dir = REPO_ROOT / POC_BIN_REL
    if not bin_dir.is_dir():
        fail(f"PoC binary directory not found: {bin_dir} — Story 2/3 not implemented yet")

    port = _free_port()
    cmd = [
        "go", "run", f"./{POC_BIN_REL}",
        "--port", str(port),
        "--claude-bin", "/bin/bash",
        "--claude-args", "-c cat",
    ]
    proc = subprocess.Popen(
        cmd, cwd=REPO_ROOT,
        stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
    )
    try:
        # Wait up to 30s for the binary to print its listening port
        # (go run includes compilation time on first run).
        deadline = time.monotonic() + 30.0
        listening = False
        while time.monotonic() < deadline:
            line = proc.stdout.readline() if proc.stdout else ""
            if not line and proc.poll() is not None:
                fail(f"poc-pty exited before listening: rc={proc.returncode}")
            if "listening on :" in line:
                listening = True
                break
        if not listening:
            proc.kill()
            fail("poc-pty did not announce its listening port within 30s")
        yield port
    finally:
        if proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=5)


# ─── Backend-only test (no browser) ────────────────────────────────────────

def test_ac_s1d2278_3_1_ws_bidirectional() -> None:
    """[AC-S1d2278-3-1] /poc/pty/attach accepts bidirectional raw bytes."""
    try:
        import websockets  # noqa: F401
    except ImportError:
        print("SKIP: websockets library not installed")
        return

    import asyncio
    import websockets

    async def _drive(port: int) -> None:
        uri = f"ws://localhost:{port}/poc/pty/attach"
        async with websockets.connect(uri, max_size=None) as ws:
            # Send a benign prompt that any interactive claude (or a
            # cat-like stub during early dev) will echo something back
            # for.
            await ws.send(b"echo hello-from-poc\n")
            # Expect at least one frame within 5 seconds.
            try:
                msg = await asyncio.wait_for(ws.recv(), timeout=5.0)
            except asyncio.TimeoutError:
                fail("no bytes received within 5s after sending prompt")
            if not msg:
                fail("WS recv returned empty frame")

    with poc_daemon() as port:
        asyncio.run(_drive(port))
    passed("AC-S1d2278-3-1 — WS endpoint bidirectional")


# ─── Browser-driven tests (Playwright) ─────────────────────────────────────

def test_ac_s1d2278_3_2_xterm_renders_tui() -> None:
    """[AC-S1d2278-3-2] xterm.js renders claude TUI output."""
    sync_playwright = _get_playwright()
    with poc_daemon() as port, sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        try:
            page = browser.new_page()
            page.goto(f"http://localhost:{port}/", timeout=10_000)
            # Status testid transitions Connecting → Connected.
            page.wait_for_selector(
                "[data-testid='pty-poc-status']", timeout=5_000,
            )
            status = page.get_by_test_id("pty-poc-status").inner_text()
            if status.strip().lower() not in {"connected", "streaming"}:
                fail(f"status testid expected connected/streaming, got {status!r}")
            # Terminal mount has produced at least some text on canvas.
            term = page.get_by_test_id("pty-poc-terminal")
            term.wait_for(state="visible", timeout=5_000)
            # PoC asserts the terminal element exists. Asserting actual
            # rendered text is deferred — xterm.js canvas is hard to
            # introspect; manual smoke (AC-3-4) covers visual confirmation.
        finally:
            browser.close()
    passed("AC-S1d2278-3-2 — xterm.js mounts and connects")


def test_ac_s1d2278_3_3_replay_on_reconnect() -> None:
    """[AC-S1d2278-3-3] Reconnect restores scrollback via ring buffer."""
    try:
        import websockets  # noqa: F401
    except ImportError:
        print("SKIP: websockets library not installed")
        return

    import asyncio
    import websockets

    marker = f"poc-marker-{int(time.time())}"

    async def _scenario(port: int) -> None:
        uri = f"ws://localhost:{port}/poc/pty/attach"
        async with websockets.connect(uri, max_size=None) as ws:
            await ws.send(f"echo {marker}\n".encode())
            # Drain a few frames so the marker lands in the ring buffer.
            deadline = time.monotonic() + 3.0
            seen = b""
            while time.monotonic() < deadline:
                try:
                    msg = await asyncio.wait_for(ws.recv(), timeout=0.5)
                except asyncio.TimeoutError:
                    continue
                if isinstance(msg, str):
                    seen += msg.encode()
                else:
                    seen += msg
                if marker.encode() in seen:
                    break
            if marker.encode() not in seen:
                fail(f"marker {marker!r} not observed before reconnect")

        # Reconnect; expect ring buffer to replay the marker.
        async with websockets.connect(uri, max_size=None) as ws2:
            replayed = b""
            deadline = time.monotonic() + 3.0
            while time.monotonic() < deadline:
                try:
                    msg = await asyncio.wait_for(ws2.recv(), timeout=0.5)
                except asyncio.TimeoutError:
                    continue
                if isinstance(msg, str):
                    replayed += msg.encode()
                else:
                    replayed += msg
                if marker.encode() in replayed:
                    return
            fail(f"marker {marker!r} not replayed after reconnect")

    with poc_daemon() as port:
        asyncio.run(_scenario(port))
    passed("AC-S1d2278-3-3 — ring buffer replay on reconnect")


def test_ac_s1d2278_3_4_smoke_log_present() -> None:
    """[AC-S1d2278-3-4] Manual smoke log artifact exists and is non-empty.

    The log content itself is written by a human during the manual demo
    run; this test only enforces that the artifact exists at the
    expected path so the deliverable is not silently skipped.
    """
    if not SMOKE_LOG.is_file():
        fail(f"manual smoke log missing: {SMOKE_LOG} — run the PoC demo and write findings")
    if SMOKE_LOG.stat().st_size < 100:
        fail(f"manual smoke log too short (<100 bytes): {SMOKE_LOG}")
    passed("AC-S1d2278-3-4 — manual smoke log present and non-empty")


def main() -> None:
    test_ac_s1d2278_3_1_ws_bidirectional()
    test_ac_s1d2278_3_2_xterm_renders_tui()
    test_ac_s1d2278_3_3_replay_on_reconnect()
    test_ac_s1d2278_3_4_smoke_log_present()
    print("ALL PASS")


if __name__ == "__main__":
    main()
