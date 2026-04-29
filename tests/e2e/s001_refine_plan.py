#!/usr/bin/env python3
"""Sprint S001-refine — ExitPlanMode bypass + redesigned action row.

This test verifies the contract S001-refine was filed to enforce:

  1. The page loads and the Claude tab mounts.
  2. The new `plan.respond` WS frame type is wired into the backend
     (sending one with a fake permission_id produces an error frame).
  3. PlanBlock renders with the new action row when synthetic events
     dispatch a kind:"plan" block + plan.question with a permission_id.
  4. The kind:"permission" duplicate UI does NOT appear under that
     plan block (the original S001 bug — generic permission UI was
     leaking through).
  5. The mode dropdown lists default/auto/acceptEdits/bypassPermissions;
     bypassPermissions has the warning class.
  6. Clicking Approve ships a properly-shaped plan.respond frame with
     the dropdown's current value (default = auto).
  7. Clicking Edit plan… opens the Edit dialog, lets the user type, and
     Save & approve ships plan.respond with editedPlan.
  8. Cancel closes the dialog without sending anything.
  9. Clicking Keep planning ships plan.respond with decision=reject.

Strategy:
    Headless Chromium against the dev palmux2 (port from
    PALMUX_DEV_PORT, default 8241). Synthetic events injected into the
    page WS so we don't need a real CLI.

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

import websockets
from playwright.async_api import async_playwright

PORT = os.environ.get("PALMUX_DEV_PORT", "8241")
REPO_ID = os.environ.get("S001_REPO_ID", "tjst-t--palmux2--2d59")
BRANCH_ID = os.environ.get("S001_BRANCH_ID", "autopilot--S001-refine--08f1")
BASE_URL = f"http://localhost:{PORT}"
WS_URL = (
    f"ws://localhost:{PORT}/api/repos/{quote(REPO_ID)}"
    f"/branches/{quote(BRANCH_ID)}/tabs/claude/agent"
)

TIMEOUT_S = 10.0


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def passed(msg: str) -> None:
    print(f"PASS: {msg}")


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
    print(f"==> S001-refine E2E starting (dev port {PORT}, branch {BRANCH_ID})")
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

        # 1) Navigate to the Claude tab.
        url = f"{BASE_URL}/{quote(REPO_ID)}/{quote(BRANCH_ID)}/claude"
        await page.goto(url, wait_until="domcontentloaded")
        try:
            await page.wait_for_selector("textarea", timeout=int(TIMEOUT_S * 1000))
        except Exception:  # noqa: BLE001
            html = await page.content()
            print(html[:2000])
            fail("composer textarea did not appear")
        passed("page loaded; composer present")

        # 2) Backend route check — fake plan.respond should error.
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
                fail("did not receive session.init")
            if not session_init_ok:
                fail("session.init never observed")
            passed("session.init received via sidecar WS")

            await side.send(
                json.dumps(
                    {
                        "type": "plan.respond",
                        "payload": {
                            "permissionId": "nonexistent-perm-id",
                            "decision": "approve",
                        },
                    }
                )
            )
            err_received = False
            try:
                async with asyncio.timeout(TIMEOUT_S):
                    while not err_received:
                        raw = await side.recv()
                        try:
                            ev = json.loads(raw)
                        except Exception:  # noqa: BLE001
                            continue
                        if ev.get("type") != "error":
                            continue
                        msg = (ev.get("payload") or {}).get("message", "")
                        detail = (ev.get("payload") or {}).get("detail", "")
                        if "Plan answer failed" in msg and (
                            "unknown" in detail or "already-resolved" in detail
                        ):
                            err_received = True
            except asyncio.TimeoutError:
                fail("backend did not return error for fake plan.respond")
            passed("plan.respond frame routed (backend rejects fake permId)")

        # 3) Inject synthetic events: open assistant turn, plan block,
        #    plan.question.  Then assert the PlanBlock + action row
        #    render and NO duplicate kind:"permission" block leaks.
        injected = await page.evaluate(
            """() => {
                const matchAgent = (u) => /\\/tabs\\/claude\\/agent/.test(u);
                const all = (window).__palmuxAllWS || [];
                const ws = all.find((w) => w.readyState === 1 && matchAgent(w.url));
                if (!ws) return false;
                const dispatch = (envelope) => {
                    const ev = new MessageEvent('message', { data: JSON.stringify(envelope) });
                    if (typeof ws.onmessage === 'function') {
                        ws.onmessage(ev);
                    } else {
                        ws.dispatchEvent(ev);
                    }
                };
                const turnId = 'test-turn-' + Math.random().toString(16).slice(2);
                const blockId = 'test-block-' + Math.random().toString(16).slice(2);
                const ts = new Date().toISOString();
                dispatch({
                    type: 'turn.start',
                    ts,
                    payload: { turnId, role: 'assistant' },
                });
                dispatch({
                    type: 'block.start',
                    ts,
                    payload: {
                        turnId,
                        block: {
                            id: blockId,
                            kind: 'plan',
                            index: 0,
                            name: 'ExitPlanMode',
                            input: {
                                plan: '# Refactor plan\\n\\n1. Extract foo helper\\n2. Update callers\\n3. Add tests',
                            },
                            done: true,
                            toolUseId: 'toolu_e2e_plan',
                        },
                    },
                });
                dispatch({
                    type: 'plan.question',
                    ts,
                    payload: {
                        permissionId: 'e2e-plan-perm',
                        blockId,
                        turnId,
                        toolUseId: 'toolu_e2e_plan',
                    },
                });
                return { turnId, blockId };
            }"""
        )
        if not injected:
            fail("could not inject synthetic events")

        try:
            await page.wait_for_selector(
                '[data-testid="plan-block"]',
                state="visible",
                timeout=int(TIMEOUT_S * 1000),
            )
        except Exception:  # noqa: BLE001
            html = await page.content()
            print(html[-3000:])
            fail("plan-block did not render")
        passed("PlanBlock rendered after synthetic inject")

        # 3a) THE LOAD-BEARING ASSERTION: no kind:"permission" block.
        # We verify that no `.permission` (CSS module) panel exists
        # alongside our plan block, by counting elements that match
        # any class containing "permission" within the conversation.
        # The S007 / S001-refine bypass means the only Allow/Deny UI
        # the user should see for ExitPlanMode is the PlanBlock action
        # row — never the generic JSON dump + Allow/Deny buttons.
        permission_count = await page.evaluate(
            """() => {
                // Match any element whose class list includes a class
                // starting with "permission" (CSS Modules hash suffix).
                // We exclude .planActions (which sometimes shares a
                // base name) and .planEditOverlay-style rendered nodes.
                const all = Array.from(document.querySelectorAll('*'));
                const hits = all.filter((el) => {
                    return Array.from(el.classList).some(
                        (cls) =>
                            /^permission(?:[A-Z]|_)/.test(cls) ||
                            cls === 'permission' ||
                            cls.startsWith('permission-')
                    );
                });
                // Also count any permission-block by data-attr / role.
                return {
                    cssHits: hits.length,
                    decisions: Array.from(
                        document.querySelectorAll('[data-permission-id]')
                    ).length,
                };
            }"""
        )
        if permission_count.get("cssHits", 0) > 0:
            fail(
                f"permission-style block leaked into UI: {permission_count}"
                " — generic permission UI should NOT appear for ExitPlanMode"
            )
        passed("no kind:\"permission\" UI leaked alongside the PlanBlock")

        # 4) Mode dropdown lists default/auto/acceptEdits/bypassPermissions.
        select_handle = await page.query_selector('[data-testid="plan-mode-select"]')
        if not select_handle:
            fail("plan-mode-select not found")
        options = await page.evaluate(
            """() => {
                const sel = document.querySelector('[data-testid="plan-mode-select"]');
                if (!sel) return [];
                return Array.from(sel.options).map((o) => o.value);
            }"""
        )
        for needed in ["default", "auto", "acceptEdits", "bypassPermissions"]:
            if needed not in options:
                fail(f"mode dropdown missing option {needed}: {options}")
        passed(f"mode dropdown lists {options}")

        # Default selection: auto (per spec).
        default_value = await page.eval_on_selector(
            '[data-testid="plan-mode-select"]', "(el) => el.value"
        )
        if default_value != "auto":
            fail(f"mode dropdown default = {default_value!r}, want 'auto'")
        passed("mode dropdown defaults to 'auto'")

        # 5) bypassPermissions has the warning class.
        warning_styled = await page.evaluate(
            """() => {
                const sel = document.querySelector('[data-testid="plan-mode-select"]');
                if (!sel) return false;
                sel.value = 'bypassPermissions';
                sel.dispatchEvent(new Event('change', { bubbles: true }));
                // After change the React state updates next tick; check
                // class list on next frame.
                return new Promise((resolve) => {
                    requestAnimationFrame(() => {
                        const cls = Array.from(sel.classList).join(' ');
                        resolve(/Warning|warning/.test(cls));
                    });
                });
            }"""
        )
        if not warning_styled:
            fail("bypassPermissions selection did not pick up the warning class")
        passed("bypassPermissions option carries warning style")

        # Reset selection to 'auto' before next assertions.
        await page.evaluate(
            """() => {
                const sel = document.querySelector('[data-testid="plan-mode-select"]');
                sel.value = 'auto';
                sel.dispatchEvent(new Event('change', { bubbles: true }));
            }"""
        )

        # 6) Clicking Approve ships plan.respond with decision=approve, targetMode=auto.
        sent_frames.clear()
        await page.click('[data-testid="plan-approve"]')
        await wait_for(
            lambda: any(
                f.get("type") == "plan.respond"
                and (f.get("payload") or {}).get("permissionId") == "e2e-plan-perm"
                and (f.get("payload") or {}).get("decision") == "approve"
                and (f.get("payload") or {}).get("targetMode") == "auto"
                for f in sent_frames
            ),
            TIMEOUT_S,
            "plan.respond approve+targetMode=auto",
        )
        passed("Approve ships plan.respond decision=approve targetMode=auto")

        # The action row is now hidden (optimistic decided).  Verify the
        # decided label shows the targetMode.
        try:
            await page.wait_for_selector(
                '[data-testid="plan-decided"]',
                state="visible",
                timeout=int(TIMEOUT_S * 1000),
            )
        except Exception:  # noqa: BLE001
            fail("plan-decided indicator did not appear after Approve")
        decided_text = await page.locator('[data-testid="plan-decided"]').text_content()
        if not decided_text or "auto" not in decided_text:
            fail(f"plan-decided text missing 'auto': {decided_text!r}")
        passed(f"plan-decided shows: {decided_text!r}")

        # 7) Edit dialog test — re-inject a fresh plan block to redo the flow.
        await page.evaluate(
            """() => {
                const matchAgent = (u) => /\\/tabs\\/claude\\/agent/.test(u);
                const all = (window).__palmuxAllWS || [];
                const ws = all.find((w) => w.readyState === 1 && matchAgent(w.url));
                const dispatch = (envelope) => {
                    const ev = new MessageEvent('message', { data: JSON.stringify(envelope) });
                    if (typeof ws.onmessage === 'function') ws.onmessage(ev);
                    else ws.dispatchEvent(ev);
                };
                const turnId = 'edit-turn';
                const blockId = 'edit-block';
                const ts = new Date().toISOString();
                dispatch({
                    type: 'turn.start',
                    ts,
                    payload: { turnId, role: 'assistant' },
                });
                dispatch({
                    type: 'block.start',
                    ts,
                    payload: {
                        turnId,
                        block: {
                            id: blockId,
                            kind: 'plan',
                            index: 0,
                            name: 'ExitPlanMode',
                            input: { plan: '# Initial plan\\n- step A\\n- step B' },
                            done: true,
                            toolUseId: 'toolu_edit_plan',
                        },
                    },
                });
                dispatch({
                    type: 'plan.question',
                    ts,
                    payload: {
                        permissionId: 'e2e-edit-perm',
                        blockId,
                        turnId,
                        toolUseId: 'toolu_edit_plan',
                    },
                });
            }"""
        )

        # Wait for the new active plan-actions row.  There can now be
        # two plan blocks in the DOM (the previous decided one + the
        # new active one) — pick the one whose action row is visible.
        await wait_for(
            lambda: page.locator('[data-testid="plan-actions"]').count(),
            TIMEOUT_S,
            "plan-actions for new plan",
        )
        # Click Edit plan… (last instance — the active one).
        await page.locator('[data-testid="plan-edit"]').last.click()
        try:
            await page.wait_for_selector(
                '[data-testid="plan-edit-dialog"]',
                state="visible",
                timeout=int(TIMEOUT_S * 1000),
            )
        except Exception:  # noqa: BLE001
            fail("plan edit dialog did not open")
        passed("Edit plan… opens the dialog")

        # Cancel test.
        await page.click('[data-testid="plan-edit-cancel"]')
        try:
            await page.wait_for_selector(
                '[data-testid="plan-edit-dialog"]',
                state="detached",
                timeout=int(TIMEOUT_S * 1000),
            )
        except Exception:  # noqa: BLE001
            fail("plan edit dialog did not close on Cancel")
        passed("Cancel closes the dialog")

        # No frame should have been sent for the cancel.
        if any(
            f.get("type") == "plan.respond"
            and (f.get("payload") or {}).get("permissionId") == "e2e-edit-perm"
            for f in sent_frames
        ):
            fail("plan.respond was sent on Cancel — should not be")

        # Re-open and submit edits.
        await page.locator('[data-testid="plan-edit"]').last.click()
        await page.wait_for_selector(
            '[data-testid="plan-edit-dialog"]', state="visible"
        )
        # Replace the textarea content.
        await page.fill(
            '[data-testid="plan-edit-textarea"]',
            "# Edited plan\n- only step A please",
        )
        sent_frames.clear()
        await page.click('[data-testid="plan-edit-submit"]')
        await wait_for(
            lambda: any(
                f.get("type") == "plan.respond"
                and (f.get("payload") or {}).get("permissionId") == "e2e-edit-perm"
                and (f.get("payload") or {}).get("decision") == "approve"
                and "Edited plan" in (f.get("payload") or {}).get("editedPlan", "")
                for f in sent_frames
            ),
            TIMEOUT_S,
            "plan.respond approve with editedPlan",
        )
        passed("Save & approve ships plan.respond with editedPlan")

        # 8) Reject path — re-inject a third plan and click Keep planning.
        await page.evaluate(
            """() => {
                const matchAgent = (u) => /\\/tabs\\/claude\\/agent/.test(u);
                const all = (window).__palmuxAllWS || [];
                const ws = all.find((w) => w.readyState === 1 && matchAgent(w.url));
                const dispatch = (envelope) => {
                    const ev = new MessageEvent('message', { data: JSON.stringify(envelope) });
                    if (typeof ws.onmessage === 'function') ws.onmessage(ev);
                    else ws.dispatchEvent(ev);
                };
                const turnId = 'reject-turn';
                const blockId = 'reject-block';
                const ts = new Date().toISOString();
                dispatch({
                    type: 'turn.start', ts,
                    payload: { turnId, role: 'assistant' },
                });
                dispatch({
                    type: 'block.start', ts,
                    payload: {
                        turnId,
                        block: {
                            id: blockId, kind: 'plan', index: 0,
                            name: 'ExitPlanMode',
                            input: { plan: '# About to do dangerous things' },
                            done: true,
                            toolUseId: 'toolu_reject_plan',
                        },
                    },
                });
                dispatch({
                    type: 'plan.question', ts,
                    payload: {
                        permissionId: 'e2e-reject-perm',
                        blockId, turnId,
                        toolUseId: 'toolu_reject_plan',
                    },
                });
            }"""
        )

        await wait_for(
            lambda: page.locator('[data-testid="plan-reject"]').count(),
            TIMEOUT_S,
            "plan-reject button",
        )
        sent_frames.clear()
        await page.locator('[data-testid="plan-reject"]').last.click()
        await wait_for(
            lambda: any(
                f.get("type") == "plan.respond"
                and (f.get("payload") or {}).get("permissionId") == "e2e-reject-perm"
                and (f.get("payload") or {}).get("decision") == "reject"
                for f in sent_frames
            ),
            TIMEOUT_S,
            "plan.respond decision=reject",
        )
        passed("Keep planning ships plan.respond decision=reject")

        await ctx.close()
        await browser.close()

    print("==> S001-refine E2E PASSED")


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        sys.exit(130)
