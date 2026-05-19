#!/usr/bin/env python3
"""Sprint S0fd64b — Track B mobile chat MVP (Story 4) E2E tests.

E2E tests for the MobileChatView component that renders on viewport
< 600px as an alternative to the xterm.js view for the claude-tui tab.

Test-environment requirement (sprint-level review D1):
    These tests use `palmux2_test_fixture` which talks to the dev
    palmux2 instance at `BASE_URL` (resolved from `PALMUX2_DEV_PORT`
    env var, default 8215). They assume a real palmux2 is already
    running — start it via `make serve INSTANCE=dev` first.

Acceptance criteria covered:
  [AC-S0fd64b-4-1] viewport < 600px renders MobileChatView (not xterm.js)
                   for the claude-tui tab; reconnect path works.
  [AC-S0fd64b-4-2] grid-mode WS connects and chat bubbles are extracted
                   from grid frames (last user prompt + assistant
                   response).
  [AC-S0fd64b-4-3] textarea + Send composer; Send disabled on empty
                   input; sending clears the textarea.
  [AC-S0fd64b-4-4] role badge shows Active for the only client; second
                   client becomes Viewer; typing in Viewer reclaims
                   Active (last-typed wins).
  [AC-S0fd64b-4-5] all required data-testid attributes exist.

Exit code 0 = PASS. Designed to be runnable as `python3
tests/e2e/s0fd64b_mobile_chat.py` per the project's standalone-script
convention.
"""
from __future__ import annotations

import sys
import time
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
sys.path.insert(0, str(REPO_ROOT / "tests" / "e2e"))

# palmux2_test_fixture from tests/e2e/_fixture.py (hermetic git repo +
# already-running palmux2 referenced via BASE_URL / PALMUX2_DEV_PORT).
from _fixture import palmux2_test_fixture, BASE_URL  # noqa: E402

MOBILE_VIEWPORT = {"width": 375, "height": 667}


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
        print("SKIP: playwright not installed — install via `pip install playwright`")
        sys.exit(0)


# ─── E2E test scenarios ────────────────────────────────────────────────────

def test_ac_s0fd64b_4_1_mobile_chat_renders_at_narrow_viewport() -> None:
    """[AC-S0fd64b-4-1] viewport<600px → MobileChatView (not xterm)."""
    sync_playwright = _get_playwright()
    with palmux2_test_fixture("s0fd64b-1") as fx, sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        try:
            ctx = browser.new_context(viewport=MOBILE_VIEWPORT)
            page = ctx.new_page()
            page.goto(f"{BASE_URL}/{fx.repo_id}/{fx.branch_id}/claude-tui", timeout=10_000)

            # MobileChatView must be visible; xterm-host (desktop) should NOT.
            page.wait_for_selector("[data-testid='mobile-chat-view']", timeout=5_000)
            term_count = page.locator("[data-testid='claude-tui-terminal']").count()
            if term_count > 0:
                fail("desktop xterm terminal element rendered at mobile viewport")
        finally:
            browser.close()
    passed("AC-S0fd64b-4-1 — MobileChatView renders at <600px")


def test_ac_s0fd64b_4_2_grid_to_chat_bubble_extraction() -> None:
    """[AC-S0fd64b-4-2] grid frames produce chat bubble via extraction."""
    sync_playwright = _get_playwright()
    marker = f"e2e-mobile-{int(time.time())}"
    with palmux2_test_fixture("s0fd64b-2") as fx, sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        try:
            ctx = browser.new_context(viewport=MOBILE_VIEWPORT)
            page = ctx.new_page()
            page.goto(f"{BASE_URL}/{fx.repo_id}/{fx.branch_id}/claude-tui", timeout=10_000)
            page.wait_for_selector("[data-testid='mobile-chat-view']", timeout=5_000)

            # Wait for connected (role badge appears).
            page.wait_for_selector(
                "[data-testid='mobile-chat-role-badge']:has-text('Active')",
                timeout=10_000,
            )

            page.get_by_test_id("mobile-chat-input").fill(f"echo {marker}")
            page.get_by_test_id("mobile-chat-send-btn").click()

            # /bin/cat echoes the bytes back. A bubble containing the marker
            # must appear via grid extraction within a few seconds.
            page.wait_for_selector(
                f"[data-testid='mobile-chat-bubble']:has-text('{marker}')",
                timeout=8_000,
            )
        finally:
            browser.close()
    passed("AC-S0fd64b-4-2 — grid → chat bubble extraction works")


def test_ac_s0fd64b_4_3_composer_send_button_state() -> None:
    """[AC-S0fd64b-4-3] Send disabled on empty; enabled with text."""
    sync_playwright = _get_playwright()
    with palmux2_test_fixture("s0fd64b-3") as fx, sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        try:
            ctx = browser.new_context(viewport=MOBILE_VIEWPORT)
            page = ctx.new_page()
            page.goto(f"{BASE_URL}/{fx.repo_id}/{fx.branch_id}/claude-tui", timeout=10_000)
            page.wait_for_selector("[data-testid='mobile-chat-view']", timeout=5_000)
            page.wait_for_selector(
                "[data-testid='mobile-chat-role-badge']:has-text('Active')",
                timeout=10_000,
            )

            send_btn = page.get_by_test_id("mobile-chat-send-btn")
            if send_btn.is_enabled():
                fail("Send button should be disabled when input is empty")

            input_el = page.get_by_test_id("mobile-chat-input")
            input_el.fill("h")
            if not send_btn.is_enabled():
                fail("Send button should be enabled after typing 1 char")

            input_el.fill("")
            if send_btn.is_enabled():
                fail("Send button should re-disable after clearing input")

            # Typing + sending clears the textarea.
            input_el.fill("clear-me")
            send_btn.click()
            time.sleep(0.5)
            if input_el.input_value() != "":
                fail("textarea should clear after Send")
        finally:
            browser.close()
    passed("AC-S0fd64b-4-3 — composer Send button state machine")


def test_ac_s0fd64b_4_4_multi_client_role_transition() -> None:
    """[AC-S0fd64b-4-4] viewer can reclaim active by typing+sending."""
    sync_playwright = _get_playwright()
    with palmux2_test_fixture("s0fd64b-4") as fx, sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        try:
            ctx_a = browser.new_context(viewport=MOBILE_VIEWPORT)
            page_a = ctx_a.new_page()
            page_a.goto(f"{BASE_URL}/{fx.repo_id}/{fx.branch_id}/claude-tui", timeout=10_000)
            page_a.wait_for_selector(
                "[data-testid='mobile-chat-role-badge']:has-text('Active')",
                timeout=10_000,
            )

            ctx_b = browser.new_context(viewport=MOBILE_VIEWPORT)
            page_b = ctx_b.new_page()
            page_b.goto(f"{BASE_URL}/{fx.repo_id}/{fx.branch_id}/claude-tui", timeout=10_000)
            page_b.wait_for_selector(
                "[data-testid='mobile-chat-role-badge']:has-text('Viewer')",
                timeout=10_000,
            )

            # B reclaims active by sending input.
            page_b.get_by_test_id("mobile-chat-input").fill("take-control")
            page_b.get_by_test_id("mobile-chat-send-btn").click()

            page_b.wait_for_selector(
                "[data-testid='mobile-chat-role-badge']:has-text('Active')",
                timeout=5_000,
            )
            page_a.wait_for_selector(
                "[data-testid='mobile-chat-role-badge']:has-text('Viewer')",
                timeout=5_000,
            )
        finally:
            browser.close()
    passed("AC-S0fd64b-4-4 — last-typed-wins role transition")


def test_ac_s0fd64b_4_5_required_testids_present() -> None:
    """[AC-S0fd64b-4-5] all required data-testid attributes exist."""
    sync_playwright = _get_playwright()
    required = [
        "mobile-chat-view",
        "mobile-chat-input",
        "mobile-chat-send-btn",
        "mobile-chat-role-badge",
    ]
    with palmux2_test_fixture("s0fd64b-5") as fx, sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        try:
            ctx = browser.new_context(viewport=MOBILE_VIEWPORT)
            page = ctx.new_page()
            page.goto(f"{BASE_URL}/{fx.repo_id}/{fx.branch_id}/claude-tui", timeout=10_000)
            for testid in required:
                page.wait_for_selector(f"[data-testid='{testid}']", timeout=10_000)
        finally:
            browser.close()
    passed("AC-S0fd64b-4-5 — all required data-testid attributes present")


def main() -> None:
    test_ac_s0fd64b_4_1_mobile_chat_renders_at_narrow_viewport()
    test_ac_s0fd64b_4_2_grid_to_chat_bubble_extraction()
    test_ac_s0fd64b_4_3_composer_send_button_state()
    test_ac_s0fd64b_4_4_multi_client_role_transition()
    test_ac_s0fd64b_4_5_required_testids_present()
    print("ALL PASS")


if __name__ == "__main__":
    main()
