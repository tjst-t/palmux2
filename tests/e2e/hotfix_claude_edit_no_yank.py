#!/usr/bin/env python3
"""Hotfix verification — clicking the edit pencil on a past user turn must
NOT yank the conversation to the bottom of the chat.

Background: when auto-follow had previously fired `scrollToBottom('instant')`
during streaming, it set up a 5.2-second `tail()` ResizeObserver on the
list's sentinel element that re-clamped `scrollTop` to `scrollHeight - max`
whenever the scrollHeight changed. The user's scroll-up wheel/touchmove/
mousedown set an `aborted` flag that gated `tail()`, but the abort
listeners were `{ once: true }` — only the first event after scrollToBottom
captured them. If the user then opened the edit editor on a past user
turn, the row's height grew (the editor takes ~200 px more than a bubble),
which fired the ResizeObserver-driven tail() with `aborted` already true
in the active scrollToBottom — but a NEW scrollToBottom (e.g. fired by
the next streaming chunk's auto-follow effect, even if autoFollowRef.current
was already false in some edge cases of the programmatic-re-engage path)
would attach fresh tail() listeners that yanked the user back.

Regression scenario (the bug a user reported on 2026-05-09):
  - active streaming Claude session, autoFollow=true, user at bottom
  - user wheels UP significantly to read history (autoFollow goes false)
  - user clicks edit pencil on a past user turn
  - row grows from bubble height → editor height (~200 px)
  - WITHOUT the fix: scrollTop jumps to current scrollHeight - clientHeight
  - WITH the fix: scrollTop stays where the user was

This is the manual-smoke AC-S43cfb1-2-7's automated equivalent, plus
covers the specific edit-pencil click pattern.

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


def get_pos(page):
    return page.evaluate(
        "() => { const i = document.querySelector('[data-testid=harness-conversation] [role=list]'); "
        "return { top: i.scrollTop, max: i.scrollHeight - i.clientHeight }; }"
    )


def find_visible_edit_button(page):
    return page.evaluate(
        """() => {
            const list = document.querySelector('[data-testid=harness-conversation] [role=list]');
            if (!list) return null;
            const sRect = list.getBoundingClientRect();
            const btns = Array.from(list.querySelectorAll('[data-testid^="rewind-edit-"]'));
            for (const b of btns) {
                const r = b.getBoundingClientRect();
                if (r.top >= sRect.top && r.bottom <= sRect.bottom) {
                    return { id: b.getAttribute('data-testid'), x: r.x + r.width/2, y: r.y + r.height/2 };
                }
            }
            return null;
        }"""
    )


def main() -> int:
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        fail("playwright not installed — run `pip install playwright && playwright install chromium`")

    print(f"Hotfix edit-no-yank E2E against {BASE_URL}")
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        ctx = browser.new_context(viewport={"width": 1024, "height": 720})
        page = ctx.new_page()

        # ---------------------------------------------------------------
        # Scenario A — streaming + autofollow, user wheels up far, clicks
        # edit on a past user turn. This mirrors the report.
        # ---------------------------------------------------------------
        print("\n=== Scenario A: edit during stream after scroll-up ===")
        page.goto(
            f"{BASE_URL}/__test/claude?turns=40&rewind=1&autofollow=1&stream=80",
            wait_until="networkidle",
        )
        page.wait_for_selector('[data-testid="harness-conversation"]', timeout=5000)
        page.wait_for_timeout(600)
        # Force-park at bottom so the auto-follow tail-RO is active.
        page.evaluate(
            "() => { const i = document.querySelector('[data-testid=harness-conversation] [role=list]'); i.scrollTop = i.scrollHeight; }"
        )
        page.wait_for_timeout(400)
        af0 = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
        if af0 != "true":
            fail(f"setup: autoFollow should be true at bottom, got {af0!r}")
        ok("setup-bottom", "autoFollow=true at bottom (tail-RO active for ~5s)")

        # User wheels up ~half the conversation.
        list_box = page.locator('[data-testid="harness-conversation"] [role=list]').bounding_box()
        page.mouse.move(list_box["x"] + list_box["width"]/2, list_box["y"] + list_box["height"]/2)
        for _ in range(10):
            page.mouse.wheel(0, -400)
            page.wait_for_timeout(40)
        page.wait_for_timeout(300)
        pos1 = get_pos(page)
        af1 = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
        if af1 != "false":
            fail(f"setup: autoFollow should flip false on wheel-up, got {af1!r}")
        ok("setup-scrolled-up", f"scrollTop={pos1['top']} dist_from_bottom={pos1['max']-pos1['top']}, autoFollow=false")

        # Find a visible edit button.
        btn = find_visible_edit_button(page)
        if not btn:
            fail("no visible edit pencil after scroll-up")
        ok("found-edit-btn", f"target={btn['id']} at ({btn['x']:.0f},{btn['y']:.0f})")

        # Click + sample scroll position over time.
        scroll_before = pos1
        page.mouse.click(btn["x"], btn["y"])
        # Sample scrollTop several times to catch a transient yank.
        # The bug we are guarding against is a SUDDEN jump to scrollTop=max,
        # so a single sample at +50ms would miss the case where the yank
        # happens at +100ms. Sample every ~100ms for 1.5s.
        delays = [50, 150, 300, 500, 800, 1200, 1700]
        prev = 0
        for d in delays:
            page.wait_for_timeout(d - prev)
            prev = d
            pos = get_pos(page)
            dist = pos['max'] - pos['top']
            af = page.locator('[data-testid="harness-autofollow"]').get_attribute("data-value")
            ok(f"after-edit+{d}ms", f"scrollTop={pos['top']} dist={dist} af={af}")
            # The user was at scroll_before['top']; if scrollTop jumps to
            # near-max (within 100 px of bottom), we've yanked.
            if pos['max'] - pos['top'] < 100:
                fail(
                    f"BUG: clicking edit yanked scrollTop to bottom "
                    f"(was {scroll_before['top']}, now {pos['top']}, max={pos['max']})"
                )

        # Cancel the edit — must also not yank.
        cancel = page.locator('[data-testid="rewind-cancel"]').first
        if cancel.count() == 0:
            fail("rewind-cancel button did not appear (editor may not have opened)")
        cancel.click()
        for d in (50, 200, 500, 1000):
            page.wait_for_timeout(d if d == 50 else d - prev_d if d > 50 else 0)
            prev_d = d
            pos = get_pos(page)
            ok(f"after-cancel+{d}ms", f"scrollTop={pos['top']} dist={pos['max']-pos['top']}")
            if pos['max'] - pos['top'] < 100:
                fail(f"BUG: cancelling edit yanked scrollTop to bottom (now {pos['top']}, max={pos['max']})")

        # ---------------------------------------------------------------
        # Scenario B — same as A but no streaming (regression: should
        # also be no-yank).
        # ---------------------------------------------------------------
        print("\n=== Scenario B: edit on static conversation (no streaming) ===")
        page.goto(
            f"{BASE_URL}/__test/claude?turns=30&rewind=1&autofollow=1",
            wait_until="networkidle",
        )
        page.wait_for_selector('[data-testid="harness-conversation"]', timeout=5000)
        page.wait_for_timeout(400)
        page.evaluate(
            "() => { const i = document.querySelector('[data-testid=harness-conversation] [role=list]'); i.scrollTop = i.scrollHeight; }"
        )
        page.wait_for_timeout(400)
        list_box = page.locator('[data-testid="harness-conversation"] [role=list]').bounding_box()
        page.mouse.move(list_box["x"] + list_box["width"]/2, list_box["y"] + list_box["height"]/2)
        for _ in range(8):
            page.mouse.wheel(0, -400)
            page.wait_for_timeout(40)
        page.wait_for_timeout(300)
        pos = get_pos(page)
        ok("scrolled-up", f"scrollTop={pos['top']} dist={pos['max']-pos['top']}")

        btn = find_visible_edit_button(page)
        if not btn:
            fail("no visible edit pencil")
        scroll_before = pos
        page.mouse.click(btn["x"], btn["y"])
        for d in (50, 200, 500, 1000):
            page.wait_for_timeout(d if d == 50 else d - 200 if d == 200 else 300 if d == 500 else 500)
            pos = get_pos(page)
            ok(f"after-edit+{d}ms", f"scrollTop={pos['top']} dist={pos['max']-pos['top']}")
            if pos['max'] - pos['top'] < 100:
                fail(f"BUG (B): scrollTop yanked to bottom (was {scroll_before['top']}, now {pos['top']}, max={pos['max']})")

        ctx.close()
        browser.close()

    print("\nALL OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
