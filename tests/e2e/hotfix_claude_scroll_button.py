#!/usr/bin/env python3
"""Hotfix verification — Claude tab "scroll to latest" button reappears
when user scrolls up, AND auto-follow stays on while AI streams to a
user who is at the bottom.

Background: ConversationList's scroll listener was attached via
useEffect(_, [onScroll]) which only ran once on mount. At that point
react-window's container DOM was still in useState(null) and the
imperative API getter returned null, so the listener never bound.
autoFollow stayed at its initial true → the conditional button render
in claude-agent-view.tsx never triggered.

A second issue surfaced once the listener attached: programmatic
scrolls (our own scrollToBottom during streaming, scroll-restore on
session load) fire scroll events too, and react-window's scrollToRow
can land a few pixels short of the absolute bottom because it scrolls
against estimated row heights. The 32px tolerance check would then
flip autoFollow off, and the next streaming chunk would bail out of
auto-follow. Fix: tag scroll events with isUserDriven (true only when
a wheel/touchmove/keydown fired within 250ms) and only let user-driven
scrolls flip autoFollow off; programmatic scrolls can only re-enable
it.

This test drives the test-harness with ?autofollow=1 (which mirrors
the same onScroll → autoFollow → button wiring as the real Claude
tab) and checks:

  1. autoFollow defaults to true at mount (scroll listener attached
     but doesn't fire).
  2. A programmatic scroll-to-top (no user input) does NOT flip
     autoFollow off — that's how streaming behaves.
  3. A user-driven scroll-up (wheel) DOES flip autoFollow off and
     reveals the button.
  4. A user-driven scroll-down to the bottom flips it back on.

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

    print(f"Hotfix scroll-button E2E against {BASE_URL}")
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        ctx = browser.new_context(viewport={"width": 1024, "height": 720})
        page = ctx.new_page()

        page.goto(f"{BASE_URL}/__test/claude?turns=80&autofollow=1", wait_until="networkidle")
        page.wait_for_selector('[data-testid="harness-conversation"]', timeout=5000)
        page.wait_for_function(
            "() => document.querySelector('[data-testid=harness-autofollow]') !== null",
            timeout=3000,
        )

        # Wait one tick for the listener to install + any startup
        # programmatic scroll to settle.
        page.wait_for_timeout(200)

        # 1. autoFollow should default to true. Listener attached but
        #    didn't fire (we don't fire onScroll on attach because the
        #    initial scrollTop=0 is meaningless for new sessions where
        #    the parent will immediately scrollToBottom).
        af = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
        ok("initial-autofollow", f"value={af}")
        if af != "true":
            fail(f"autoFollow should default to true at mount, got {af!r}. "
                 f"If it's false, the 'fire once on attach' regressed in.")
        if page.locator('[data-testid="harness-scroll-to-bottom"]').count() != 0:
            fail("scroll-to-latest button visible while autoFollow=true")
        ok("initial-button-hidden", "button correctly absent while autoFollow=true")

        # 2. Programmatic scroll-to-top (simulating scroll-restore or
        #    other code-driven scrolls) must NOT flip autoFollow off.
        #    This is the streaming case: react-window's scrollToBottom
        #    sometimes lands a few pixels short, and we don't want
        #    that to break auto-follow.
        page.evaluate(
            """() => {
                const inner = document.querySelector('[data-testid=harness-conversation] [role=list]');
                inner.scrollTo({top: 100, behavior: 'instant'});
            }"""
        )
        page.wait_for_timeout(150)
        af2 = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
        ok("after-programmatic-scroll", f"value={af2}")
        if af2 != "true":
            fail(f"programmatic scroll-up should NOT flip autoFollow off, got {af2!r}")

        # 3. User-driven scroll-up via wheel must flip autoFollow off
        #    and reveal the button. We hover the inner list and use
        #    Playwright's mouse.wheel which dispatches a real wheel
        #    event — the harness's scroll listener detects this as
        #    user-driven (within 250ms of a wheel event).
        list_box = page.locator('[data-testid="harness-conversation"] [role=list]').bounding_box()
        if list_box is None:
            fail("list bounding box not available")
        page.mouse.move(list_box["x"] + list_box["width"] / 2, list_box["y"] + list_box["height"] / 2)
        # Negative deltaY = scroll up. Use a big delta to make sure
        # we move > 32px above bottom even if we started near it.
        page.mouse.wheel(0, -500)
        page.wait_for_timeout(200)
        af3 = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
        ok("after-user-wheel-up", f"value={af3}")
        if af3 != "false":
            fail(f"user wheel-up should flip autoFollow to false, got {af3!r}")
        if page.locator('[data-testid="harness-scroll-to-bottom"]').count() == 0:
            fail("button missing after user wheel-up")
        ok("button-visible-after-user-scroll", "button appears after real user scroll-up")

        # 4. Wheel back down all the way (in increments to keep
        #    triggering wheel events) and we should re-enable
        #    autoFollow + hide the button. mouse.wheel scrolls a
        #    browser-decided fraction of the delta, so we ensure we
        #    actually reach the bottom by chaining wheels until the
        #    scroll position is at the bottom.
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
        af4 = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
        ok("after-user-wheel-down", f"value={af4} dist={pos['d']}")
        if af4 != "true":
            fail(f"user wheel-down to bottom should re-enable autoFollow, got {af4!r} (dist={pos['d']})")
        if page.locator('[data-testid="harness-scroll-to-bottom"]').count() != 0:
            fail("button still visible after returning to bottom")
        ok("button-hidden-after-return", "button hidden after returning to bottom")

        ctx.close()
        browser.close()

    print("\nALL OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
