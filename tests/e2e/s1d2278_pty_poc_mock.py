#!/usr/bin/env python3
"""Sprint S1d2278 — Track B PoC Story 3 mock-only tests.

These tests exercise the static HTML's WS error paths without a running
daemon, using a synthetic WebSocket server that the test owns. They run
during `sprint run` for fast feedback while the real daemon (Story 2)
is still being implemented.

Mock scenarios:
  [MOCK] WS refused        → status testid transitions to Disconnected
  [MOCK] WS closes mid-stream → status testid transitions to Disconnected
                                and Reconnect button becomes visible

The static HTML must implement reconnect_button as a [data-testid]
element so this test can assert visibility transitions.
"""
from __future__ import annotations

import asyncio
import socket
import sys
import threading
import time
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
STATIC_HTML = REPO_ROOT / "cmd/poc-pty/static/index.html"


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
        print("SKIP: playwright not installed")
        sys.exit(0)


def _free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


class _FakeWSServer:
    """Minimal asyncio WS server that closes after a configurable delay."""

    def __init__(self, port: int, behavior: str) -> None:
        self.port = port
        self.behavior = behavior  # "refuse" | "close_after_open"
        self.loop: asyncio.AbstractEventLoop | None = None
        self.thread: threading.Thread | None = None
        self._stop = asyncio.Event()

    def start(self) -> None:
        ready = threading.Event()

        def run() -> None:
            try:
                import websockets
            except ImportError:
                print("SKIP: websockets library not installed for mock tests")
                ready.set()
                return
            self.loop = asyncio.new_event_loop()
            asyncio.set_event_loop(self.loop)

            async def handler(ws):
                if self.behavior == "close_after_open":
                    await asyncio.sleep(0.2)
                    await ws.close(code=1011, reason="mock close")
                else:  # refuse
                    await ws.close(code=1008, reason="mock refuse")

            async def serve():
                if self.behavior == "refuse":
                    # Don't even accept — just bind a TCP socket that
                    # drops connections. websockets.serve always
                    # accepts, so we approximate "refuse" by closing
                    # immediately with 1008.
                    pass
                async with websockets.serve(handler, "127.0.0.1", self.port):
                    ready.set()
                    await self._stop.wait()

            self.loop.run_until_complete(serve())

        self.thread = threading.Thread(target=run, daemon=True)
        self.thread.start()
        if not ready.wait(timeout=5):
            fail("fake WS server did not start within 5s")

    def stop(self) -> None:
        if self.loop is None:
            return
        self.loop.call_soon_threadsafe(self._stop.set)
        if self.thread:
            self.thread.join(timeout=5)


def _mock_test(behavior: str, label: str) -> None:
    if not STATIC_HTML.is_file():
        fail(f"static HTML not found: {STATIC_HTML} — Story 3 not implemented yet")

    sync_playwright = _get_playwright()
    port = _free_port()
    srv = _FakeWSServer(port, behavior)
    srv.start()
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                # Load the static HTML directly via file:// and let it
                # connect to our fake WS endpoint. Implementation note:
                # the HTML must accept ?ws=ws://... as a query-string
                # override so this test can point it at the fake server.
                url = f"file://{STATIC_HTML}?ws=ws://127.0.0.1:{port}/poc/pty/attach"
                page.goto(url, timeout=10_000)
                # Wait for the status testid to settle on "disconnected".
                deadline = time.monotonic() + 5.0
                final_status = ""
                while time.monotonic() < deadline:
                    try:
                        final_status = page.get_by_test_id(
                            "pty-poc-status"
                        ).inner_text(timeout=500)
                    except Exception:
                        time.sleep(0.1)
                        continue
                    if final_status.strip().lower() == "disconnected":
                        break
                    time.sleep(0.1)
                if final_status.strip().lower() != "disconnected":
                    fail(f"[{label}] expected status=disconnected, got {final_status!r}")
                # Reconnect button must appear in disconnected state.
                page.get_by_test_id("pty-poc-reconnect-btn").wait_for(
                    state="visible", timeout=2_000,
                )
            finally:
                browser.close()
    finally:
        srv.stop()
    passed(f"[MOCK] {label}")


def test_mock_ws_refused() -> None:
    """[MOCK] WS refused on open should display Disconnected state."""
    _mock_test("refuse", "ws refused → disconnected + reconnect visible")


def test_mock_ws_closes_mid_stream() -> None:
    """[MOCK] WS close mid-stream should display Disconnected state."""
    _mock_test("close_after_open", "ws closed after open → disconnected + reconnect visible")


def main() -> None:
    test_mock_ws_refused()
    test_mock_ws_closes_mid_stream()
    print("ALL MOCK PASS")


if __name__ == "__main__":
    main()
