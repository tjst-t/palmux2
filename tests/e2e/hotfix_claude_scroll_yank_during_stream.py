#!/usr/bin/env python3
"""Hotfix verification — Claude tab does NOT yank the user back to bottom
while the AI is streaming and the user is scrolling up to read history.

Background: claude-agent-view's auto-follow effect runs on every
state.turns / state.status commit. During heavy streaming, block.delta
events arrive every ~50 ms and each one fires the effect. The effect
read autoFollowRef synchronously, but the *user-input* signal that
flips autoFollowRef → false routed through the browser's `scroll`
event — and `scroll` events are deferred until after the next paint.
So a user wheel that fired in the same React batch as a streaming
chunk could lose the race: the effect committed first, read a stale
autoFollowRef=true, and yanked the user back to bottom. Repeated
deltas → repeated yanks → the user could not read history.

This test drives the test harness with `?stream=N&autofollow=1` so a
synthetic streaming loop appends a new turn every N ms while the
user wheel-scrolls up. Pass criteria: after the user scrolls up,
the scroll position must NOT snap back to bottom even though more
turns keep landing.

Exit code 0 = PASS.
"""
from __future__ import annotations

import os
import sys

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or "8215"
)
BASE_URL = f"http://localhost:{PORT}"


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def main() -> int:
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        fail("playwright not installed — run `pip install playwright && playwright install chromium`")

    print(f"Hotfix scroll-yank-during-stream E2E against {BASE_URL}")
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        ctx = browser.new_context(viewport={"width": 1024, "height": 720})
        page = ctx.new_page()

        # 80 baseline turns + 50ms streaming loop + autofollow wired.
        page.goto(
            f"{BASE_URL}/__test/claude?turns=80&autofollow=1&stream=50",
            wait_until="networkidle",
        )
        page.wait_for_selector('[data-testid="harness-conversation"]', timeout=5000)
        page.wait_for_function(
            "() => document.querySelector('[data-testid=harness-autofollow]') !== null",
            timeout=3000,
        )
        # Let the streaming loop tick a few times so we're definitely
        # at bottom + autoFollow=true (default).
        page.wait_for_timeout(400)
        af0 = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
        if af0 != "true":
            fail(f"streaming should keep autoFollow=true at bottom, got {af0!r}")
        ok("baseline-autofollow", "autoFollow=true while streaming at bottom")

        # User scrolls up via wheel WHILE streaming. The race the fix
        # closes: the wheel's `scroll` event fires after the next
        # paint, but the streaming loop is firing the auto-follow
        # effect every 50 ms — without the fix, one of those effect
        # tickets lands between the wheel and its scroll event, sees
        # autoFollowRef stale-true, and snaps scrollTop back to max.
        list_box = page.locator('[data-testid="harness-conversation"] [role=list]').bounding_box()
        if list_box is None:
            fail("list bounding box not available")
        page.mouse.move(
            list_box["x"] + list_box["width"] / 2,
            list_box["y"] + list_box["height"] / 2,
        )
        # One big wheel up — enough to clear the 32 px atBottom
        # tolerance with margin.
        page.mouse.wheel(0, -800)
        # Wait for the wheel-driven scroll to land. Browser `scroll`
        # events are deferred until after the next paint, so reading
        # scrollTop in the same microtask as `mouse.wheel` returns
        # the pre-wheel value.
        page.wait_for_timeout(80)
        pos1 = page.evaluate(
            """() => {
                const i = document.querySelector('[data-testid=harness-conversation] [role=list]');
                return { top: i.scrollTop, max: i.scrollHeight - i.clientHeight };
            }"""
        )
        ok("after-wheel", f"scrollTop={pos1['top']} max={pos1['max']}")
        if pos1["max"] - pos1["top"] < 100:
            fail(f"wheel didn't actually move scrollTop away from bottom: {pos1!r}")

        # Now wait through ~12 streaming ticks (50ms each = 600ms)
        # and verify scrollTop did NOT snap back.
        page.wait_for_timeout(700)
        pos2 = page.evaluate(
            """() => {
                const i = document.querySelector('[data-testid=harness-conversation] [role=list]');
                return { top: i.scrollTop, max: i.scrollHeight - i.clientHeight };
            }"""
        )
        ok("after-stream-window", f"scrollTop={pos2['top']} max={pos2['max']}")

        # The stream keeps growing scrollHeight so `max` grows — but
        # scrollTop should NOT have moved much (the user is reading).
        # We allow a tiny upward drift if anything (none expected).
        # Critical assertion: scrollTop must NOT have snapped to the
        # new max.
        new_dist = pos2["max"] - pos2["top"]
        if new_dist < 100:
            fail(
                f"scrollTop snapped back to bottom while user was reading: "
                f"distance from bottom = {new_dist} px (expected > 100)"
            )
        ok("no-yank", f"distance from bottom held at {new_dist} px (was {pos1['max'] - pos1['top']} px)")

        # autoFollow should also be false now (user-driven scroll up).
        af1 = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
        if af1 != "false":
            fail(f"autoFollow should flip to false on user scroll up, got {af1!r}")
        ok("autofollow-off", "autoFollow=false after user scroll up")

        # Sanity: scrolling back down to bottom re-enables autoFollow
        # and the streamed lines start landing again.
        for _ in range(20):
            page.mouse.wheel(0, 800)
            page.wait_for_timeout(40)
            pos = page.evaluate(
                """() => {
                    const i = document.querySelector('[data-testid=harness-conversation] [role=list]');
                    return { d: i.scrollHeight - i.scrollTop - i.clientHeight };
                }"""
            )
            if pos["d"] < 16:
                break
        page.wait_for_timeout(200)
        af2 = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
        if af2 != "true":
            fail(f"scrolling back to bottom should re-enable autoFollow, got {af2!r}")
        ok("autofollow-back-on", "autoFollow=true after returning to bottom")

        ctx.close()
        browser.close()

    print("\nALL OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
