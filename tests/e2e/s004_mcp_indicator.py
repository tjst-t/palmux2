#!/usr/bin/env python3
"""Sprint S004 — MCP server status indicator E2E test.

Verifies, against the running dev palmux2 instance, that:

  1. The page loads and the Claude tab mounts.
  2. The TopBar contains an `mcp` indicator button (data-testid =
     "mcp-topbar-btn") with a status pip and a summary label.
  3. With no MCP servers in session.init, the popup opens and shows
     "No MCP servers configured" (the empty-state path that *must*
     not crash the UI in dev environments without MCP).
  4. With three synthetic MCP servers (connected / failed /
     connecting), the TopBar pip rolls up to "err" tone, the summary
     reports "1/3", and clicking the button opens a popup that lists
     all three servers with their per-server status badges.
  5. The popup closes on Escape and on click-outside.

Strategy:
    Headless Chromium against the dev palmux2 (port from
    PALMUX_DEV_PORT, default 8215). The first session.init landing
    naturally over the agent WS gives us the empty-state assertion;
    we then *re-dispatch* a synthetic session.init with mcpServers via
    the page WS' onmessage hook — same pattern as S001 / S007.

Exit code 0 = PASS.
"""

from __future__ import annotations

import asyncio
import json
import os
import sys
import time
from typing import Any
from urllib.parse import quote

from playwright.async_api import async_playwright

PORT = os.environ.get("PALMUX_DEV_PORT", "8215")
REPO_ID = os.environ.get("S004_REPO_ID", "tjst-t--palmux2--2d59")
BRANCH_ID = os.environ.get("S004_BRANCH_ID", "autopilot--S004--6089")
BASE_URL = f"http://localhost:{PORT}"
WS_PATH = (
    f"/api/repos/{quote(REPO_ID)}/branches/{quote(BRANCH_ID)}/tabs/claude/agent"
)

TIMEOUT_S = 15.0


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def passed(msg: str) -> None:
    print(f"PASS: {msg}")


async def popup_gone(page) -> bool:
    return (await page.locator('[data-testid="mcp-popup"]').count()) == 0


async def summary_contains(page, needle: str) -> bool:
    txt = await page.locator('[data-testid="mcp-topbar-summary"]').text_content()
    return bool(txt and needle in txt)


async def wait_for(check, timeout_s: float, label: str) -> Any:
    start = time.monotonic()
    last = None
    while time.monotonic() - start < timeout_s:
        try:
            result = check()
            if asyncio.iscoroutine(result):
                result = await result
            last = result
            if last:
                return last
        except Exception as e:  # noqa: BLE001
            last = e
        await asyncio.sleep(0.08)
    raise TimeoutError(f"timeout waiting for: {label} (last={last!r})")


async def main() -> None:
    print(f"==> S004 E2E starting (dev port {PORT})")

    async with async_playwright() as pw:
        browser = await pw.chromium.launch(headless=True)
        ctx = await browser.new_context()
        page = await ctx.new_page()

        page.on("pageerror", lambda err: print(f"[browser pageerror] {err}"))
        page.on(
            "console",
            lambda msg: print(f"[browser {msg.type}] {msg.text}")
            if msg.type == "error"
            else None,
        )

        url = f"{BASE_URL}/{REPO_ID}/{BRANCH_ID}/claude"
        await page.goto(url, wait_until="domcontentloaded")

        # 1. Page boot — TopBar should render the mcp button.
        await wait_for(
            lambda: page.locator('[data-testid="mcp-topbar-btn"]').count(),
            TIMEOUT_S,
            "mcp-topbar-btn appears",
        )
        passed("page loaded; TopBar mcp indicator present")

        # 2. Empty state: dev environment has no MCP servers configured,
        #    so the rollup pip should be 'unknown' (gray) and the
        #    summary should read '—'.
        summary = await page.locator(
            '[data-testid="mcp-topbar-summary"]'
        ).text_content()
        if not summary or "—" not in summary:
            fail(f"expected summary to contain '—' (no servers); got {summary!r}")
        passed(f"empty-state summary correct: {summary!r}")

        tone = await page.locator(
            '[data-testid="mcp-topbar-pip"]'
        ).get_attribute("data-tone")
        if tone != "unknown":
            fail(f"expected pip tone 'unknown' for empty state; got {tone!r}")
        passed("empty-state pip tone = unknown (no MCP servers)")

        # 3. Open popup → empty state list.
        await page.locator('[data-testid="mcp-topbar-btn"]').click()
        await wait_for(
            lambda: page.locator('[data-testid="mcp-popup"]').count(),
            TIMEOUT_S,
            "mcp-popup opens",
        )
        # The popup empty body should explicitly say so.
        empty_text = await page.locator(
            '[data-testid="mcp-popup-empty"]'
        ).text_content()
        if not empty_text or "No MCP servers" not in empty_text:
            fail(
                f"expected popup to show 'No MCP servers configured'; got {empty_text!r}"
            )
        passed("popup empty state renders 'No MCP servers configured'")

        # 4. Esc closes popup.
        await page.keyboard.press("Escape")
        await wait_for(
            lambda: popup_gone(page),
            TIMEOUT_S,
            "popup closes on Escape",
        )
        passed("popup closes on Escape")

        # 5. Inject a synthetic session.init via the page WS onmessage so
        #    the reducer sees three servers. We need the WS proxy in
        #    place before the page mounts, so install it via init script
        #    and reload.
        await ctx.add_init_script(
            """
            (() => {
                const all = (window).__palmuxAllWS = (window).__palmuxAllWS || [];
                const NativeWS = window.WebSocket;
                const proxied = function (...args) {
                    const sock = new NativeWS(...args);
                    all.push(sock);
                    return sock;
                };
                proxied.prototype = NativeWS.prototype;
                proxied.CONNECTING = NativeWS.CONNECTING;
                proxied.OPEN = NativeWS.OPEN;
                proxied.CLOSING = NativeWS.CLOSING;
                proxied.CLOSED = NativeWS.CLOSED;
                window.WebSocket = proxied;
            })();
            """
        )
        await page.goto(url, wait_until="domcontentloaded")
        await wait_for(
            lambda: page.evaluate(
                "() => (window.__palmuxAllWS || []).filter(s => s.url && s.url.includes('/tabs/claude/agent')).length"
            ),
            TIMEOUT_S,
            "agent WS opens after reload",
        )
        await wait_for(
            lambda: page.locator('[data-testid="mcp-topbar-btn"]').count(),
            TIMEOUT_S,
            "mcp-topbar-btn present after reload",
        )

        # Inject the synthetic session.init.
        await page.evaluate(
            """
            () => {
                const sockets = window.__palmuxAllWS || [];
                const ws = sockets.find(s => s.url && s.url.includes('/tabs/claude/agent'));
                if (!ws) {
                    throw new Error('no agent WS found after reload');
                }
                const payload = {
                    sessionId: 'synthetic-s004',
                    branchId: 'autopilot--S004--6089',
                    repoId: 'tjst-t--palmux2--2d59',
                    model: 'claude-opus-4',
                    permissionMode: 'acceptEdits',
                    status: 'idle',
                    turns: [],
                    totalCostUsd: 0,
                    authOk: true,
                    mcpServers: [
                        { name: 'palmux',  status: 'connected' },
                        { name: 'github',  status: 'failed'    },
                        { name: 'linear',  status: 'connecting'},
                    ],
                };
                const ev = new MessageEvent('message', {
                    data: JSON.stringify({
                        type: 'session.init',
                        ts: new Date().toISOString(),
                        payload,
                    }),
                });
                ws.dispatchEvent(ev);
            }
            """
        )

        # Wait for the TopBar summary to update to "1/3" (one connected
        # of three total).
        await wait_for(
            lambda: summary_contains(page, "1/3"),
            TIMEOUT_S,
            "mcp summary populated to 1/3",
        )
        summary2 = await page.locator(
            '[data-testid="mcp-topbar-summary"]'
        ).text_content()
        if not summary2 or "1/3" not in summary2:
            fail(f"expected summary to contain '1/3' (1 connected of 3); got {summary2!r}")
        passed(f"populated-state summary correct: {summary2!r}")

        tone2 = await page.locator(
            '[data-testid="mcp-topbar-pip"]'
        ).get_attribute("data-tone")
        if tone2 != "err":
            fail(f"expected rollup tone 'err' (one failed); got {tone2!r}")
        passed("populated-state rollup tone = err (one failed)")

        # 6. Open popup again — verify the three rows + per-row status
        # badges + dot tones.
        await page.locator('[data-testid="mcp-topbar-btn"]').click()
        await wait_for(
            lambda: page.locator('[data-testid="mcp-popup"]').count(),
            TIMEOUT_S,
            "mcp-popup opens (populated)",
        )

        for name, want_tone in (
            ("palmux", "ok"),
            ("github", "err"),
            ("linear", "warn"),
        ):
            row = page.locator(f'[data-testid="mcp-row-{name}"]')
            count = await row.count()
            if count != 1:
                fail(f"expected 1 row for {name!r}, got {count}")
            dot_tone = await page.locator(
                f'[data-testid="mcp-dot-{name}"]'
            ).get_attribute("data-tone")
            if dot_tone != want_tone:
                fail(f"server {name!r}: expected dot tone {want_tone!r}, got {dot_tone!r}")
            badge = await page.locator(
                f'[data-testid="mcp-status-{name}"]'
            ).text_content()
            if not badge or len(badge.strip()) == 0:
                fail(f"server {name!r}: status badge missing")
            passed(f"row {name!r}: dot tone={dot_tone}, badge={badge!r}")

        # 7. Click outside the popup → it closes.
        await page.locator("body").click(position={"x": 5, "y": 5})
        await wait_for(
            lambda: popup_gone(page),
            TIMEOUT_S,
            "popup closes on click outside",
        )
        passed("popup closes on click outside")

        await ctx.close()
        await browser.close()

    print("==> S004 E2E PASSED")


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except SystemExit:
        raise
    except Exception as e:
        fail(f"unhandled error: {e!r}")
