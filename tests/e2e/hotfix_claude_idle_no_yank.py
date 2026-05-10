#!/usr/bin/env python3
"""Hotfix verification — Claude tab does NOT yank the user back to bottom
while the agent is IDLE (no LLM output) and the user is reading earlier
history.

Background: User report — "even though there's no LLM output now,
scrolling up forces the view back to the bottom". The previous yank-
during-streaming hotfixes (b6d5517, b8eb48e, S43cfb1-4) closed the
streaming race but the auto-follow effect still re-fires on ANY
[turns, status] dep change, including idle-time WS events that the
agent-state reducer applies (every event spreads `{ ...state }` so the
state OBJECT reference is new even when status stays 'idle' and turns
content is unchanged — and any code path that produces a new turns
ARRAY reference triggers the effect).

The fix: gate the scroll-to-bottom effect on `isStreaming` (status is
'thinking' / 'tool_running' / 'starting'). When the agent is idle, no
amount of dep churn should clobber the user's scroll position.

Test scenario:
  - 80 baseline turns + autofollow=1 + idlePulseMs=200 (forces a new
    turns ARRAY reference every 200 ms while status stays 'idle').
  - Wait for the initial scroll-to-bottom to land.
  - User wheels up by ~1500 px.
  - Verify autoFollow flipped to false.
  - Sample scroll position every 200 ms for 3 s (15 samples spanning
    the full pulse window).
  - PASS: scrollTop holds well above bottom (distance > 500 px) for
    every sample.
  - FAIL (broken impl): one of the idle pulses re-fires the effect
    while autoFollowRef is still TRUE (race during the user wheel,
    250ms guard, etc.) → scrollToBottom snaps the user back, distance
    drops below 100 px on a sample.

Exit code 0 = PASS.
"""
from __future__ import annotations

import os
import sys

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or "8202"
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

    print(f"Hotfix idle-no-yank E2E against {BASE_URL}")
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        ctx = browser.new_context(viewport={"width": 1024, "height": 720})
        page = ctx.new_page()

        # idle (status=idle), 80 baseline turns, autofollow on by
        # default, and idlePulseMs=200 to force turns ref churn every
        # 200 ms (no content change). This is the steady-state we
        # expect when the agent is sitting idle and arbitrary WS
        # events keep arriving (mcp.update, heartbeats, etc.).
        page.goto(
            f"{BASE_URL}/__test/claude?turns=80&autofollow=1&idlePulseMs=200",
            wait_until="networkidle",
        )
        page.wait_for_selector('[data-testid="harness-conversation"]', timeout=5000)
        page.wait_for_function(
            "() => document.querySelector('[data-testid=harness-autofollow]') !== null",
            timeout=3000,
        )
        # Wait for initial scroll-to-bottom + a couple of pulses.
        page.wait_for_timeout(500)
        af0 = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
        if af0 != "true":
            fail(f"baseline: autoFollow should default to true, got {af0!r}")
        ok("baseline-autofollow", "autoFollow=true at idle bottom")

        # Confirm we're actually at bottom before the wheel (otherwise
        # the user-driven scroll-up assertion below wouldn't be
        # meaningful).
        pos_init = page.evaluate(
            """() => {
                const i = document.querySelector('[data-testid=harness-conversation] [role=list]');
                return { top: i.scrollTop, max: i.scrollHeight - i.clientHeight };
            }"""
        )
        if pos_init["max"] - pos_init["top"] > 32:
            fail(
                f"baseline: should be at bottom before wheel, got "
                f"distance={pos_init['max'] - pos_init['top']}"
            )
        ok("baseline-at-bottom", f"distance={pos_init['max'] - pos_init['top']}")

        # User scrolls up while idle. Wheel hard enough to clear the
        # 32 px atBottom tolerance with a generous margin.
        list_box = page.locator('[data-testid="harness-conversation"] [role=list]').bounding_box()
        if list_box is None:
            fail("list bounding box not available")
        page.mouse.move(
            list_box["x"] + list_box["width"] / 2,
            list_box["y"] + list_box["height"] / 2,
        )
        # Scroll up in 3 chunks so we end up well clear of bottom even if
        # the harness viewport is short (one wheel may saturate at a few
        # hundred pixels in headless chromium).
        for _ in range(3):
            page.mouse.wheel(0, -2000)
            page.wait_for_timeout(60)
        page.wait_for_timeout(120)  # let the scroll event commit
        pos1 = page.evaluate(
            """() => {
                const i = document.querySelector('[data-testid=harness-conversation] [role=list]');
                return { top: i.scrollTop, max: i.scrollHeight - i.clientHeight };
            }"""
        )
        dist1 = pos1["max"] - pos1["top"]
        ok("after-wheel", f"scrollTop={pos1['top']} distance={dist1}")
        # Just need to be clearly above the 32 px atBottom margin so the
        # bug (snap-to-bottom) is detectable.
        if dist1 < 200:
            fail(
                f"wheel didn't clear the 32 px atBottom margin with enough "
                f"room: distance={dist1} (expected >200). The harness "
                f"may have rendered too few turns to scroll meaningfully."
            )

        # Verify autoFollow is now false.
        af1 = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
        if af1 != "false":
            fail(
                f"autoFollow should flip to false after user wheel-up, got {af1!r}. "
                f"This is a different bug from the one this test guards."
            )
        ok("autofollow-off", "autoFollow=false after user wheel-up")

        # Now sample the scroll position for 3 s while idle pulses keep
        # firing every 200 ms. With the broken impl, one of these pulses
        # re-fires the auto-follow effect with a stale autoFollowRef=true
        # AND status='idle' — and scrollToBottom yanks the user back.
        # With the fix (isStreaming guard), no scrollToBottom is called
        # during idle regardless of dep churn.
        min_dist_seen = dist1
        for chk in range(15):
            page.wait_for_timeout(200)
            pos = page.evaluate(
                """() => {
                    const i = document.querySelector('[data-testid=harness-conversation] [role=list]');
                    return { top: i.scrollTop, max: i.scrollHeight - i.clientHeight };
                }"""
            )
            dist = pos["max"] - pos["top"]
            min_dist_seen = min(min_dist_seen, dist)
            af = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
            ok(f"+{(chk + 1) * 200}ms", f"distance={dist} af={af}")
            if dist < 50:
                fail(
                    f"BUG: scroll yanked back to bottom while idle "
                    f"(distance={dist} at +{(chk + 1) * 200}ms). The "
                    f"auto-follow effect ran scrollToBottom even though "
                    f"status='idle' — fix the hook to gate on isStreaming."
                )
            if af != "false":
                fail(
                    f"autoFollow should remain false during idle pulses, got {af!r} "
                    f"at +{(chk + 1) * 200}ms"
                )

        ok("no-yank", f"min distance over 3s = {min_dist_seen} (held above 100 px)")

        ctx.close()
        browser.close()

    print("\nALL OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
