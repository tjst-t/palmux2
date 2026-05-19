#!/usr/bin/env python3
"""Sprint S0fd64b — mobile chat MVP (Story 4) mock-only tests.

Synthetic fake WS server emits canned grid.init / grid.diff / role frames
so the MobileChatView's state machine and extraction logic can be
exercised without a full palmux2 backend.

Mock scenarios:
  [MOCK] WS refused → status banner shows Disconnected; reconnect btn shown
  [MOCK] grid.init then role=Viewer → input disabled, viewer placeholder
  [MOCK] role flips Active → Viewer mid-stream → input disables live
"""
from __future__ import annotations

import asyncio
import json
import socket
import sys
import threading
import time
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
# The implementation Story 4 must produce a static test harness HTML that
# loads MobileChatView with a configurable WS URL (?ws=...) so this mock
# test can point it at the fake server.
HARNESS = REPO_ROOT / "frontend" / "src" / "tabs" / "claude-tui" / "mobile-test-harness.html"


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


class _FakeGridWS:
    """Fake WS server emitting canned JSON frames per the script."""

    def __init__(self, port: int, script: list[tuple[float, dict]]) -> None:
        self.port = port
        self.script = script  # list of (delay_after_open_seconds, frame_obj)
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
                start = time.monotonic()
                for delay, frame in self.script:
                    sleep_for = max(0, delay - (time.monotonic() - start))
                    if sleep_for > 0:
                        await asyncio.sleep(sleep_for)
                    if frame is None:
                        await ws.close(code=1011, reason="mock mid-stream close")
                        return
                    await ws.send(json.dumps(frame))
                try:
                    async for _ in ws:
                        pass
                except Exception:
                    pass

            async def serve():
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


def _run_mock(script: list[tuple[float, dict]], assertion):
    if not HARNESS.is_file():
        fail(f"test harness not found: {HARNESS} — Story 4 implementation must produce it")

    sync_playwright = _get_playwright()
    port = _free_port()
    srv = _FakeGridWS(port, script)
    srv.start()
    try:
        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            try:
                ctx = browser.new_context(viewport={"width": 375, "height": 667})
                page = ctx.new_page()
                url = f"file://{HARNESS}?ws=ws://127.0.0.1:{port}/"
                page.goto(url, timeout=10_000)
                assertion(page)
            finally:
                browser.close()
    finally:
        srv.stop()


def test_mock_ws_refused() -> None:
    """[MOCK] WS refused → Disconnected + reconnect visible."""
    def check(page):
        # Immediate close before any frame.
        page.wait_for_selector("[data-testid='mobile-chat-reconnect-btn']", timeout=5_000)
    _run_mock([(0.05, None)], check)
    passed("[MOCK] ws refused → reconnect btn visible")


def test_mock_role_viewer_at_init() -> None:
    """[MOCK] init → role=Viewer → input shows viewer CSS restriction.

    Aligned with real MobileChatView contract (sprint-level review D2):
    viewer role uses CSS-only restriction (.textareaViewer class), NOT
    the HTML disabled attribute — so multi-client reclaim flows work
    via Playwright fill(). We verify the CSS class instead.
    """
    def check(page):
        page.wait_for_selector(
            "[data-testid='mobile-chat-role-badge']:has-text('Viewer')",
            timeout=5_000,
        )
        input_el = page.locator("[data-testid='mobile-chat-input']")
        class_attr = input_el.get_attribute("class") or ""
        if "textareaViewer" not in class_attr:
            fail(f"input should have .textareaViewer class for viewer role; got class={class_attr!r}")
    script = [
        (0.05, {"type": "grid.init", "cols": 80, "rows": 24, "cursor": {"x": 0, "y": 0}, "altScreen": False, "rows_data": []}),
        (0.1, {"type": "role", "role": "viewer", "since": int(time.time() * 1000)}),
    ]
    _run_mock(script, check)
    passed("[MOCK] role=Viewer → input disabled at init")


def test_mock_role_active_to_viewer_midstream() -> None:
    """[MOCK] Active → Viewer mid-stream → input disables live."""
    def check(page):
        # First active.
        page.wait_for_selector(
            "[data-testid='mobile-chat-role-badge']:has-text('Active')",
            timeout=5_000,
        )
        # Then flips to viewer.
        page.wait_for_selector(
            "[data-testid='mobile-chat-role-badge']:has-text('Viewer')",
            timeout=5_000,
        )
        input_el = page.locator("[data-testid='mobile-chat-input']")
        class_attr = input_el.get_attribute("class") or ""
        if "textareaViewer" not in class_attr:
            fail(f"input should gain .textareaViewer class when role flips mid-stream; got class={class_attr!r}")
    script = [
        (0.05, {"type": "grid.init", "cols": 80, "rows": 24, "cursor": {"x": 0, "y": 0}, "altScreen": False, "rows_data": []}),
        (0.1, {"type": "role", "role": "active", "since": int(time.time() * 1000)}),
        (0.6, {"type": "role", "role": "viewer", "since": int(time.time() * 1000) + 500}),
    ]
    _run_mock(script, check)
    passed("[MOCK] role flip Active → Viewer mid-stream")


def main() -> None:
    test_mock_ws_refused()
    test_mock_role_viewer_at_init()
    test_mock_role_active_to_viewer_midstream()
    print("ALL MOCK PASS")


if __name__ == "__main__":
    main()
