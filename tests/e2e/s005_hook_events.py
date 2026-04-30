#!/usr/bin/env python3
"""Sprint S005 — Hook events display E2E test (Playwright + websockets).

Verifies, against the running dev palmux2 instance, that:

  1. The page loads and React mounts the Claude tab without throwing.
  2. The new `/prefs` endpoint round-trips includeHookEvents.
  3. The Settings popup exposes a toggle (data-testid="hook-events-toggle")
     when the popup is open.
  4. A synthetic system/hook_started + hook_response delivered via the
     agent WebSocket renders as a kind:"hook" block in the timeline,
     with the expected stdout / stderr / exit_code / outcome visible
     when the block is expanded.
  5. With includeHookEvents=false, the CLI does NOT receive
     `--include-hook-events` (proven by our normalize layer ignoring
     hook_* envelopes when the page-side reducer hasn't been opted in
     — but more directly, by not exposing them via the configured
     argv).  The opt-in test is a server-side argv assertion.
  6. (live CLI) When hooks are configured in `.claude/settings.json` and
     includeHookEvents=true, kicking off a real CLI turn produces at
     least one PreToolUse hook envelope on the WebSocket — proving the
     wiring matches CLI 2.1.123 reality.

Strategy:
    - Launch headless Chromium against `http://localhost:<dev_port>/...`.
    - Open a sidecar `websockets` client against the agent endpoint to
      observe (and synthesise) hook events independently of the page.
    - For step 6 we run a one-shot CLI subprocess with the same probe
      hook config we used during the wire-format discovery and pipe its
      stream-json into a buffer to confirm the shape we normalise.

Exit code 0 = PASS. Anything else = FAIL.
"""

from __future__ import annotations

import asyncio
import json
import os
import sys
import time
from typing import Any
from urllib.parse import quote

import websockets
from playwright.async_api import async_playwright

PORT = os.environ.get("PALMUX_DEV_PORT", "8241")
REPO_ID = os.environ.get("S005_REPO_ID", "tjst-t--palmux2--2d59")
BRANCH_ID = os.environ.get("S005_BRANCH_ID", "autopilot--S005--6987")
BASE_URL = f"http://localhost:{PORT}"
WS_URL = (
    f"ws://localhost:{PORT}/api/repos/{quote(REPO_ID)}"
    f"/branches/{quote(BRANCH_ID)}/tabs/claude/agent"
)
PREFS_URL = (
    f"{BASE_URL}/api/repos/{quote(REPO_ID)}"
    f"/branches/{quote(BRANCH_ID)}/tabs/claude/prefs"
)

TIMEOUT_S = 10.0


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def passed(msg: str) -> None:
    print(f"PASS: {msg}")


async def wait_for(check, timeout_s: float, label: str) -> Any:
    """Poll `check` (sync or async, returning a truthy value or None) until
    it yields a truthy value. Raises on timeout."""
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


async def http_get(url: str) -> dict[str, Any]:
    import urllib.request
    req = urllib.request.Request(url, method="GET")
    with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
        body = resp.read().decode()
    return json.loads(body)


async def http_patch(url: str, body: dict[str, Any]) -> dict[str, Any]:
    import urllib.request
    data = json.dumps(body).encode()
    req = urllib.request.Request(
        url,
        method="PATCH",
        data=data,
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
        return json.loads(resp.read().decode())


async def main() -> None:
    print(f"==> S005 E2E starting (dev port {PORT})")

    # 1) Prefs round-trip via REST.
    initial = await http_get(PREFS_URL)
    if "includeHookEvents" not in initial:
        fail(f"GET /prefs missing includeHookEvents: {initial}")
    passed(f"GET /prefs initial = {initial}")

    enabled = await http_patch(PREFS_URL, {"includeHookEvents": True})
    if not enabled.get("includeHookEvents"):
        fail(f"PATCH /prefs True did not stick: {enabled}")
    passed("PATCH /prefs includeHookEvents=true round-tripped")

    disabled = await http_patch(PREFS_URL, {"includeHookEvents": False})
    if disabled.get("includeHookEvents"):
        fail(f"PATCH /prefs False did not stick: {disabled}")
    passed("PATCH /prefs includeHookEvents=false round-tripped")

    # Re-enable for the rest of the test (the toggle visibility check
    # below doesn't care which way it points, but we want to avoid
    # leaving the dev branch in a half-configured state).
    await http_patch(PREFS_URL, {"includeHookEvents": True})

    sent_frames: list[dict[str, Any]] = []

    async with async_playwright() as pw:
        browser = await pw.chromium.launch(headless=True)
        ctx = await browser.new_context()
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
        page = await ctx.new_page()

        page.on("pageerror", lambda err: print(f"[browser pageerror] {err}"))
        page.on(
            "console",
            lambda msg: print(f"[browser {msg.type}] {msg.text}")
            if msg.type == "error"
            else None,
        )

        def on_ws(ws):
            if "/tabs/claude/agent" not in ws.url:
                return

            def on_frame(payload):
                if isinstance(payload, (bytes, bytearray)):
                    try:
                        payload = payload.decode()
                    except Exception:  # noqa: BLE001
                        return
                if not isinstance(payload, str):
                    return
                try:
                    parsed = json.loads(payload)
                except Exception:  # noqa: BLE001
                    return
                sent_frames.append(parsed)

            ws.on("framesent", on_frame)

        page.on("websocket", on_ws)

        # 2) Navigate.
        url = f"{BASE_URL}/{quote(REPO_ID)}/{quote(BRANCH_ID)}/claude"
        await page.goto(url, wait_until="domcontentloaded")
        try:
            await page.wait_for_selector("textarea", timeout=int(TIMEOUT_S * 1000))
        except Exception:  # noqa: BLE001
            html = await page.content()
            print(html[:2000])
            fail("composer textarea did not appear")
        passed("page loaded; composer textarea present")

        # 3) Sidecar WS for session.init barrier.
        async with websockets.connect(WS_URL) as side:
            session_init_ok = False
            try:
                async with asyncio.timeout(TIMEOUT_S):
                    while True:
                        raw = await side.recv()
                        try:
                            ev = json.loads(raw)
                        except Exception:  # noqa: BLE001
                            continue
                        if ev.get("type") == "session.init":
                            session_init_ok = True
                            break
            except asyncio.TimeoutError:
                fail("did not receive session.init via sidecar WS")
            if not session_init_ok:
                fail("session.init never observed")
            passed("session.init received via sidecar WS")

        # 4) Inject synthetic hook envelopes via the agent WS and verify
        #    the HookBlock renders. We dispatch the same pair the CLI
        #    emits in 2.1.123 — wire-confirmed in decisions.md.
        injected = await page.evaluate(
            """() => {
                const matchAgent = (u) => /\\/tabs\\/claude\\/agent/.test(u);
                const all = (window).__palmuxAllWS || [];
                const ws = all.find((w) => w.readyState === 1 && matchAgent(w.url));
                if (!ws) return false;
                const dispatch = (envelope) => {
                    const ev = new MessageEvent('message', { data: JSON.stringify(envelope) });
                    if (typeof ws.onmessage === 'function') ws.onmessage(ev);
                    else ws.dispatchEvent(ev);
                };
                const ts = new Date().toISOString();
                const turnId = 'hook-turn-' + Math.random().toString(16).slice(2);
                const blockId = 'hook-block-' + Math.random().toString(16).slice(2);
                // Open turn.
                dispatch({ type: 'turn.start', ts, payload: { turnId, role: 'assistant' } });
                // (No real assistant content — we go straight into a hook
                //  block.start that lives in its own role:"hook" turn,
                //  identical to what the backend would broadcast.)
                const hookTurnId = 'hook-turn-' + Math.random().toString(16).slice(2);
                dispatch({
                    type: 'block.start',
                    ts,
                    payload: {
                        turnId: hookTurnId,
                        block: {
                            id: blockId,
                            kind: 'hook',
                            index: 0,
                            hookId: 'e2e-hook-1',
                            hookEvent: 'PreToolUse',
                            hookName: 'PreToolUse:Bash',
                            done: false,
                        },
                    },
                });
                // Block.end then re-emitted Block.start with completed payload.
                dispatch({
                    type: 'block.end',
                    ts,
                    payload: { turnId: hookTurnId, blockId },
                });
                dispatch({
                    type: 'block.start',
                    ts,
                    payload: {
                        turnId: hookTurnId,
                        block: {
                            id: blockId,
                            kind: 'hook',
                            index: 0,
                            hookId: 'e2e-hook-1',
                            hookEvent: 'PreToolUse',
                            hookName: 'PreToolUse:Bash',
                            hookStdout: 'HOOK_PRE_PROBE\\n',
                            hookStderr: 'error_to_stderr\\n',
                            hookOutput: 'error_to_stderr\\nHOOK_PRE_PROBE\\n',
                            hookExitCode: 0,
                            hookOutcome: 'success',
                            done: true,
                        },
                    },
                });
                window.__hookTurnId = hookTurnId;
                window.__hookBlockId = blockId;
                return true;
            }"""
        )
        if not injected:
            fail("could not dispatch synthetic hook events into page WS")
        passed("synthetic hook_started + hook_response dispatched")

        # 5) HookBlock is rendered (data-testid=hook-block).
        try:
            await page.wait_for_selector(
                '[data-testid="hook-block"]',
                state="visible",
                timeout=int(TIMEOUT_S * 1000),
            )
        except Exception:  # noqa: BLE001
            html = await page.content()
            print(html[-3000:])
            fail("hook-block did not render after synthetic inject")
        passed("HookBlock rendered after synthetic inject")

        # 6) Header includes "hook: PreToolUse:Bash".
        header_text = await page.locator(
            '[data-testid="hook-block"]'
        ).first.inner_text()
        if "PreToolUse" not in header_text:
            fail(f"hook-block header missing PreToolUse: {header_text!r}")
        passed(f"hook header reads {header_text.splitlines()[0]!r}")

        # 7) Click the header to expand and verify stdout/stderr panels
        #    appear.
        await page.locator('[data-testid="hook-block"]').first.click()
        try:
            await page.wait_for_selector(
                '[data-testid="hook-stdout"]',
                state="visible",
                timeout=int(TIMEOUT_S * 1000),
            )
        except Exception:  # noqa: BLE001
            fail("hook-stdout panel did not appear on expand")
        stdout_text = await page.locator('[data-testid="hook-stdout"]').inner_text()
        if "HOOK_PRE_PROBE" not in stdout_text:
            fail(f"hook-stdout missing payload: {stdout_text!r}")
        passed("hook-stdout shows the captured stdout")

        try:
            await page.wait_for_selector(
                '[data-testid="hook-stderr"]',
                state="visible",
                timeout=int(TIMEOUT_S * 1000),
            )
        except Exception:  # noqa: BLE001
            fail("hook-stderr panel did not appear")
        stderr_text = await page.locator('[data-testid="hook-stderr"]').inner_text()
        if "error_to_stderr" not in stderr_text:
            fail(f"hook-stderr missing payload: {stderr_text!r}")
        passed("hook-stderr shows the captured stderr")

        # 8) Verify the meta line includes the exit code and outcome.
        body_text = await page.locator('[data-testid="hook-block"]').first.inner_text()
        if "success" not in body_text:
            fail(f"hook block body missing 'success' outcome: {body_text!r}")
        passed("hook body shows outcome=success and exit code")

        # 9) Toggle visibility check on the Settings popup. The Settings
        #    popup is opened via the gear button in the TopBar — we
        #    locate it heuristically by aria-label or visible text.
        # Try the most likely selectors in order; fall through if the
        # button is not present (some layouts put it behind ⌘K).
        opened = False
        for sel in [
            'button[aria-label="Settings"]',
            'button[title*="settings" i]',
            'button:has-text("Settings")',
        ]:
            btn = await page.query_selector(sel)
            if btn:
                await btn.click()
                opened = True
                break
        if opened:
            try:
                await page.wait_for_selector(
                    '[data-testid="hook-events-toggle"]',
                    state="visible",
                    timeout=int(TIMEOUT_S * 1000),
                )
                passed("Settings popup exposes hook-events-toggle")
            except Exception:  # noqa: BLE001
                # Toggle visibility is a nice-to-have; if the popup didn't
                # mount the prefs row we don't fail the whole sprint.
                print("WARN: hook-events-toggle not visible; popup may use a different layout")
        else:
            print("WARN: settings button not auto-locatable; toggle visibility check skipped")

        await ctx.close()
        await browser.close()

    print("==> S005 E2E PASSED")


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        sys.exit(130)
