#!/usr/bin/env python3
"""Hotfix verification — autoFollow is STRICTLY user-owned.

Background: the previous logic in onListScroll re-engaged autoFollow on
ANY scroll event landing within 32 px of the bottom, even programmatic
ones (our own scrollToBottom, react-window's layout adjustment after a
row's height change). That broke the "I scrolled up; AI keeps yanking
me" invariant in subtle cases — e.g. when a streaming chunk's
auto-follow effect fires `scrollToBottom('instant')` once before the
user's scroll-up wheel registered, and the resulting programmatic
scroll event tagged isUserDriven=false but lands within the 32 px
threshold, the OLD code re-engaged autoFollow even though the user
had clearly intended to scroll up.

The fix: programmatic scrolls (isUserDriven=false) must NEVER toggle
autoFollow. Only wheel/touchmove/keydown/mousedown-driven scrolls do.

Scenario: user scrolls up via wheel, autoFollow=false. Streaming
continues. Wait long enough for many programmatic scroll events from
react-window's height-change layout passes to fire. autoFollow MUST
remain false the entire time.

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
        fail("playwright not installed")

    print(f"Hotfix autofollow-strict E2E against {BASE_URL}")
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        ctx = browser.new_context(viewport={"width": 1024, "height": 720})
        page = ctx.new_page()
        # Aggressive streaming + autofollow + 80 turns to keep
        # scrollHeight changing constantly.
        page.goto(
            f"{BASE_URL}/__test/claude?turns=80&autofollow=1&stream=30",
            wait_until="networkidle",
        )
        page.wait_for_selector('[data-testid="harness-conversation"]', timeout=5000)
        page.wait_for_timeout(400)

        list_box = page.locator('[data-testid="harness-conversation"] [role=list]').bounding_box()
        page.mouse.move(list_box["x"] + list_box["width"]/2, list_box["y"] + list_box["height"]/2)

        # User scrolls up enough that we're clearly NOT at bottom.
        page.mouse.wheel(0, -1200)
        page.wait_for_timeout(100)
        page.mouse.wheel(0, -1200)
        page.wait_for_timeout(100)
        page.wait_for_timeout(200)
        af = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
        if af != "false":
            fail(f"setup: autoFollow should be false after wheel-up, got {af!r}")
        ok("setup-up", "autoFollow=false after wheel-up")

        # Now wait through HEAVY streaming for 3 seconds. During this
        # window, react-window will fire many programmatic scroll
        # events as row heights settle. With the OLD code, ANY of them
        # landing within 32 px of bottom would re-engage autoFollow.
        # With the FIX, none should.
        for chk in range(6):
            page.wait_for_timeout(500)
            af = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
            pos = page.evaluate(
                "() => { const i = document.querySelector('[data-testid=harness-conversation] [role=list]'); "
                "return { top: i.scrollTop, max: i.scrollHeight - i.clientHeight }; }"
            )
            dist = pos['max'] - pos['top']
            ok(f"+{(chk+1)*500}ms", f"scrollTop={pos['top']} dist={dist} af={af}")
            if af != "false":
                fail(
                    f"BUG: autoFollow re-engaged programmatically (af={af!r} at +{(chk+1)*500}ms). "
                    f"User had wheeled up; only a USER scroll back to bottom should re-enable autoFollow."
                )
            if dist < 100:
                fail(
                    f"sanity: scroll position drifted to bottom (dist={dist}) without user input — "
                    f"a different bug from the one this test guards"
                )

        # NOTE: A "wheel back to bottom must re-engage autoFollow"
        # assertion is intentionally omitted from this test. With
        # very fast streaming (stream=30), wheel events struggle to
        # outrun scrollHeight growth, so the test is timing-fragile
        # in CI. The recovery path is covered by the existing
        # `hotfix_claude_scroll_button.py` (steps 3+4) and
        # `hotfix_claude_scroll_yank_during_stream.py` (final
        # autofollow-back-on step) with stream=50, which leaves
        # enough headroom for the wheel to land at-bottom in a
        # single user-driven scroll event.

        ctx.close()
        browser.close()

    print("\nALL OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
