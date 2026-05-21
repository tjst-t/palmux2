#!/usr/bin/env python3
"""Sprint S7ce250 Story 3 — claude-tui frontend mock tests.

Exercises the claude-tui tab's UI contract using a static test harness HTML
(frontend/src/tabs/claude-tui/test-harness.html) and synthetic WS / resize
servers.  No running palmux2 server required.

Mock scenarios:
  [MOCK] WS refused        → data-testid=claude-tui-status = "disconnected"
                             data-testid=claude-tui-reconnect-btn visible
  [MOCK] WS close mid-stream → data-testid=claude-tui-status = "disconnected"
                                data-testid=claude-tui-reconnect-btn visible
  [MOCK] resize triggers POST → resize endpoint receives POST with body
                                 {"cols": <int>, "rows": <int>}, both > 0

Exit code 0 = ALL PASS. Non-zero = FAIL.
"""
from __future__ import annotations

import asyncio
import http.server
import json
import socket
import sys
import threading
import time
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
HARNESS_HTML = (
    REPO_ROOT / "frontend" / "src" / "tabs" / "claude-tui" / "test-harness.html"
)


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


# ---------------------------------------------------------------------------
# Fake WebSocket server (asyncio-based, same pattern as s1d2278_pty_poc_mock)
# ---------------------------------------------------------------------------

class _FakeWSServer:
    """Minimal asyncio WS server with configurable behavior."""

    def __init__(self, port: int, behavior: str) -> None:
        self.port = port
        # behavior: "refuse" | "close_after_open" | "send_data"
        self.behavior = behavior
        self.loop: asyncio.AbstractEventLoop | None = None
        self.thread: threading.Thread | None = None
        self._stop = asyncio.Event()

    def start(self) -> None:
        ready = threading.Event()

        def run() -> None:
            try:
                import websockets  # noqa: F401
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
                elif self.behavior == "send_data":
                    await asyncio.sleep(0.1)
                    await ws.send(b"\x1b[2J")  # send some PTY bytes
                    await asyncio.sleep(0.3)
                    await ws.close(code=1011, reason="mock close after send")
                else:  # refuse / immediate close
                    await ws.close(code=1008, reason="mock refuse")

            async def serve():
                import websockets
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


# ---------------------------------------------------------------------------
# Fake resize HTTP server
# ---------------------------------------------------------------------------

class _ResizeCapture:
    """Captures the body of the first POST request it receives."""

    def __init__(self) -> None:
        self.received: dict | None = None
        self._ev = threading.Event()

    def wait(self, timeout: float = 5.0) -> bool:
        return self._ev.wait(timeout=timeout)

    def record(self, body: dict) -> None:
        self.received = body
        self._ev.set()


class _FakeResizeServer:
    def __init__(self, port: int, capture: _ResizeCapture) -> None:
        self.port = port
        self.capture = capture
        self._server: http.server.HTTPServer | None = None
        self._thread: threading.Thread | None = None

    def start(self) -> None:
        capture = self.capture

        class Handler(http.server.BaseHTTPRequestHandler):
            def log_message(self, *_args, **_kwargs) -> None:  # silence access log
                pass

            def do_POST(self):
                length = int(self.headers.get("Content-Length", 0))
                body_bytes = self.rfile.read(length)
                try:
                    data = json.loads(body_bytes.decode())
                    capture.record(data)
                except Exception:
                    pass
                self.send_response(204)
                self.end_headers()

            def do_OPTIONS(self):
                self.send_response(200)
                self.send_header("Access-Control-Allow-Origin", "*")
                self.send_header("Access-Control-Allow-Methods", "POST, OPTIONS")
                self.send_header("Access-Control-Allow-Headers", "Content-Type")
                self.end_headers()

        self._server = http.server.HTTPServer(("127.0.0.1", self.port), Handler)
        self._thread = threading.Thread(target=self._server.serve_forever, daemon=True)
        self._thread.start()

    def stop(self) -> None:
        if self._server:
            self._server.shutdown()


# ---------------------------------------------------------------------------
# Test helpers
# ---------------------------------------------------------------------------

def _load_harness(page, ws_url: str, resize_url: str | None = None) -> None:
    if not HARNESS_HTML.is_file():
        fail(
            f"test harness not found: {HARNESS_HTML} — "
            "claude-tui/test-harness.html must be present"
        )
    params = f"?ws={ws_url}"
    if resize_url:
        params += f"&resize={resize_url}"
    url = f"file://{HARNESS_HTML}{params}"
    page.goto(url, timeout=10_000)


def _wait_for_status(page, expected: str, timeout_s: float = 6.0) -> str:
    deadline = time.monotonic() + timeout_s
    final = ""
    while time.monotonic() < deadline:
        try:
            final = page.get_by_test_id("claude-tui-status").inner_text(timeout=400)
        except Exception:
            time.sleep(0.1)
            continue
        if final.strip().lower() == expected:
            return final
        time.sleep(0.1)
    return final


# ---------------------------------------------------------------------------
# Test cases
# ---------------------------------------------------------------------------

def test_mock_ws_refused() -> None:
    """[MOCK] WS refused → claude-tui-status = disconnected + reconnect visible."""
    sync_playwright = _get_playwright()
    port = _free_port()
    srv = _FakeWSServer(port, "refuse")
    srv.start()
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                _load_harness(page, f"ws://127.0.0.1:{port}/attach")
                final = _wait_for_status(page, "disconnected")
                if final.strip().lower() != "disconnected":
                    fail(
                        f"[MOCK ws-refused] expected status=disconnected, got {final!r}"
                    )
                page.get_by_test_id("claude-tui-reconnect-btn").wait_for(
                    state="visible", timeout=2_000
                )
            finally:
                browser.close()
    finally:
        srv.stop()
    passed("[MOCK] ws refused → disconnected + reconnect visible")


def test_mock_ws_closes_mid_stream() -> None:
    """[MOCK] WS closes mid-stream → claude-tui-status = disconnected."""
    sync_playwright = _get_playwright()
    port = _free_port()
    srv = _FakeWSServer(port, "close_after_open")
    srv.start()
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                _load_harness(page, f"ws://127.0.0.1:{port}/attach")
                final = _wait_for_status(page, "disconnected")
                if final.strip().lower() != "disconnected":
                    fail(
                        f"[MOCK ws-close-mid-stream] expected disconnected, got {final!r}"
                    )
                page.get_by_test_id("claude-tui-reconnect-btn").wait_for(
                    state="visible", timeout=2_000
                )
            finally:
                browser.close()
    finally:
        srv.stop()
    passed("[MOCK] ws closed after open → disconnected + reconnect visible")


def test_mock_resize_triggers_post() -> None:
    """[MOCK] Window resize → POST /resize with {cols, rows} both > 0."""
    sync_playwright = _get_playwright()
    ws_port = _free_port()
    resize_port = _free_port()

    capture = _ResizeCapture()
    ws_srv = _FakeWSServer(ws_port, "refuse")  # WS not needed; we test resize
    resize_srv = _FakeResizeServer(resize_port, capture)
    ws_srv.start()
    resize_srv.start()
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                resize_url = f"http://127.0.0.1:{resize_port}/resize"
                _load_harness(
                    page,
                    f"ws://127.0.0.1:{ws_port}/attach",
                    resize_url=resize_url,
                )
                # The harness POSTs resize on load (WS onopen sends size) and on
                # the ResizeObserver firing.  Wait for capture.
                if not capture.wait(timeout=6.0):
                    fail("[MOCK resize] no POST received within 6s")
                body = capture.received
                if body is None:
                    fail("[MOCK resize] captured body is None")
                if "cols" not in body or "rows" not in body:
                    fail(f"[MOCK resize] missing cols/rows in {body!r}")
                cols = body["cols"]
                rows = body["rows"]
                if not isinstance(cols, int) or cols <= 0:
                    fail(f"[MOCK resize] cols must be a positive int, got {cols!r}")
                if not isinstance(rows, int) or rows <= 0:
                    fail(f"[MOCK resize] rows must be a positive int, got {rows!r}")
            finally:
                browser.close()
    finally:
        ws_srv.stop()
        resize_srv.stop()
    passed(f"[MOCK] resize triggers POST with cols={capture.received.get('cols')} rows={capture.received.get('rows')}")


def main() -> None:
    test_mock_ws_refused()
    test_mock_ws_closes_mid_stream()
    test_mock_resize_triggers_post()
    print("ALL MOCK PASS")


if __name__ == "__main__":
    main()
