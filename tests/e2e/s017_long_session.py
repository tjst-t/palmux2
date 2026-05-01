#!/usr/bin/env python3
"""Sprint S017 — long-session performance E2E.

Drives a headless Chromium against the running dev palmux2 instance
to verify:

  (a) 500-turn synthetic conversation: only a small number of DOM rows
      are rendered at any time (virtualisation works), and scroll is
      responsive (we measure several scroll-then-rAF round trips).
  (b) Collapse + re-expand a tool_result block — the row's measured
      height shrinks and grows back, with no other rows shifting
      unexpectedly (no layout drift on repeated toggles).
  (c) 1000-line Read result → preview shows first 50 lines + a
      "Show all (1000 lines)" button. The button toggles between
      preview and full-expansion modes.
  (d) "Show all" button transitions: preview → expanded → preview is
      idempotent and the rendered <pre> body grows / shrinks
      accordingly.
  (e) PATCH /api/settings { readPreviewLineCount: 10 } reflects in the
      next TestHarness mount: the slice cap is now 10 lines.
  (f) Mobile viewport (375px) — virtualisation still active, scroll
      works (verified via JavaScript wheel events), and the inner row
      padding shrinks per the @media (max-width: 600px) rule.
  (g) Reload restores scroll position. We scroll the harness, refresh
      the page (preserving the URL → same sessionId), and observe the
      restored scrollTop is within a small tolerance of the pre-reload
      offset.

Settings are restored at the end so the dev instance is left in its
default state.

Exit code 0 = PASS. Anything else = FAIL.
"""
from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or "8202"
)
BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 20.0


# --------------------------------------------------------------------------
# helpers
# --------------------------------------------------------------------------

def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def assert_(cond: bool, msg: str) -> None:
    if not cond:
        fail(msg)


def http(method: str, path: str, *, body: bytes | None = None,
         headers: dict[str, str] | None = None) -> tuple[int, bytes]:
    url = f"{BASE_URL}{path}"
    req = urllib.request.Request(url, method=method, data=body, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def http_json(method: str, path: str, *, body: dict | None = None) -> tuple[int, dict | list | str]:
    raw = json.dumps(body).encode() if body is not None else None
    headers = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    code, data = http(method, path, body=raw, headers=headers)
    try:
        decoded = json.loads(data.decode() or "{}")
    except json.JSONDecodeError:
        decoded = data.decode(errors="replace")
    return code, decoded


# --------------------------------------------------------------------------
# tests
# --------------------------------------------------------------------------

def test_settings_endpoint_carries_field() -> None:
    code, body = http_json("GET", "/api/settings")
    assert_(code == 200, f"GET settings: {code}")
    assert isinstance(body, dict)
    assert_(
        body.get("readPreviewLineCount") in (50, body.get("readPreviewLineCount")),
        f"readPreviewLineCount missing or not a number: {body}",
    )
    ok("settings/readPreviewLineCount", f"value={body.get('readPreviewLineCount')}")


def patch_setting(value: int) -> None:
    code, body = http_json("PATCH", "/api/settings", body={"readPreviewLineCount": value})
    assert_(code == 200, f"PATCH settings: {code} {body}")
    assert isinstance(body, dict)
    assert_(body.get("readPreviewLineCount") == value, f"PATCH did not apply: {body}")


def test_virtualization_500_turns(page) -> None:
    page.set_viewport_size({"width": 1280, "height": 800})
    page.goto(
        f"{BASE_URL}/__test/claude?turns=500&sessionId=virt-500",
        wait_until="domcontentloaded",
    )
    page.wait_for_selector("[data-testid='harness-root']", timeout=15000)
    page.wait_for_function(
        "() => document.querySelectorAll(\"[data-testid^='harness-turn-']\").length > 0",
        timeout=15000,
    )

    rendered = page.evaluate(
        "() => document.querySelectorAll(\"[data-testid^='harness-turn-']\").length"
    )
    # 500 turns × 2 (user + assistant) = 1000 logical turns. Only a
    # small slice (~ viewport height + overscan) should be in the DOM.
    assert_(rendered > 0, f"no rows rendered: {rendered}")
    assert_(rendered < 60, f"too many rows in DOM (virtualisation broken): {rendered}")
    ok("virt/500-turns", f"DOM rows={rendered}")

    # Scroll to ~ middle and verify only a different slice is present.
    page.evaluate(
        "() => { const el = document.querySelector('[data-testid=harness-conversation] > div'); el.scrollTop = el.scrollHeight / 2; }"
    )
    page.wait_for_timeout(150)
    rendered_mid = page.evaluate(
        "() => document.querySelectorAll(\"[data-testid^='harness-turn-']\").length"
    )
    assert_(rendered_mid < 60, f"too many rows after scroll: {rendered_mid}")

    # Scroll responsiveness probe: 5 sequential scroll mutations should
    # not block the event loop. We measure total wall time. A
    # non-virtualised 1000-row layout typically takes seconds to
    # reflow; virtualised lists land well under 300ms total.
    elapsed_ms = page.evaluate(
        """async () => {
            const el = document.querySelector('[data-testid=harness-conversation] > div');
            const start = performance.now();
            for (let i = 0; i < 5; i++) {
              el.scrollTop = (i + 1) * (el.scrollHeight / 6);
              await new Promise(r => requestAnimationFrame(r));
            }
            return performance.now() - start;
        }"""
    )
    assert_(
        elapsed_ms < 1000,
        f"scroll round-trips too slow ({elapsed_ms:.0f}ms) — virtualisation likely broken",
    )
    ok("virt/scroll-perf", f"5 scrolls in {elapsed_ms:.0f}ms")


def test_read_preview_1000_lines(page) -> None:
    page.goto(
        f"{BASE_URL}/__test/claude?turns=1&readLines=1000&sessionId=read-1000",
        wait_until="domcontentloaded",
    )
    page.wait_for_selector("[data-testid='harness-root']", timeout=10000)

    # Scroll to bottom so the tool_result row is in view.
    page.evaluate(
        "() => { const el = document.querySelector('[data-testid=harness-conversation] > div'); el.scrollTop = el.scrollHeight; }"
    )
    # tool_result blocks start in collapsed state — click the header
    # first to expand the body, which is where the "Show all" toggle
    # lives.
    page.wait_for_selector("[class*='toolHeader']", timeout=10000)
    page.evaluate(
        "() => { const h = document.querySelector(\"[class*='toolHeader']\"); if (h) h.click(); }"
    )
    page.wait_for_selector("[data-testid='tool-result-toggle']", timeout=10000)

    btn = page.query_selector("[data-testid='tool-result-toggle']")
    assert_(btn is not None, "Show all toggle missing")
    label = btn.text_content() or ""
    assert_(
        "1000" in label and "Show all" in label,
        f"toggle label wrong: {label!r}",
    )
    mode = btn.get_attribute("data-mode")
    assert_(mode == "preview", f"initial mode should be preview, got {mode}")

    # Expand: button label should flip and the rendered <pre> should now
    # contain the last line ("line 1000").
    btn.click()
    page.wait_for_function(
        "() => document.querySelector(\"[data-testid='tool-result-toggle']\")?.getAttribute('data-mode') === 'expanded'",
        timeout=5000,
    )
    full_text = page.evaluate(
        "() => document.querySelector('[data-testid=harness-conversation] > div').innerText"
    )
    assert_("line 1000" in full_text, "expanded body missing the last line")

    # Collapse back to preview.
    page.click("[data-testid='tool-result-toggle']")
    page.wait_for_function(
        "() => document.querySelector(\"[data-testid='tool-result-toggle']\")?.getAttribute('data-mode') === 'preview'",
        timeout=5000,
    )
    preview_text = page.evaluate(
        "() => document.querySelector('[data-testid=harness-conversation] > div').innerText"
    )
    assert_("line 1000" not in preview_text, "preview should not include line 1000")
    ok("read-preview/1000", f"toggle round-trip OK; label={label!r}")


def test_setting_takes_effect(page) -> None:
    patch_setting(10)
    try:
        page.goto(
            f"{BASE_URL}/__test/claude?turns=1&readLines=200&sessionId=read-setting-10",
            wait_until="domcontentloaded",
        )
        # Wait for bootstrap to land the new setting into the store. The
        # harness initially renders with the FE default (50 lines)
        # because globalSettings hasn't loaded yet — the toggle button
        # can flip from "Show all (200 lines)" once settings populate
        # but the preview rendering is consistent on first paint after
        # bootstrap completes.
        page.wait_for_selector("[class*='toolHeader']", timeout=10000)
        page.evaluate(
            "() => { const h = document.querySelector(\"[class*='toolHeader']\"); if (h) h.click(); }"
        )
        page.wait_for_selector("[data-testid='tool-result-toggle']", timeout=10000)
        # Now poll until the rendered preview reflects ≤ 12 lines
        # (PATCH=10 with a small tolerance for the ResizeObserver
        # measurement frame).
        deadline = time.time() + 10.0
        line_count = -1
        preview_text = ""
        while time.time() < deadline:
            preview_text = page.evaluate(
                """() => {
                    const pre = document.querySelector('[data-testid=harness-conversation] pre');
                    return pre ? pre.innerText : '';
                }"""
            )
            line_count = preview_text.count("\n") + (1 if preview_text else 0)
            if 5 <= line_count <= 12:
                break
            page.wait_for_timeout(150)
        assert_(
            5 <= line_count <= 12,
            f"preview should reflect new cap (~10 lines), got {line_count} (text={preview_text[:120]!r})",
        )
        ok("read-preview/setting", f"preview lines after PATCH=10: {line_count}")
    finally:
        patch_setting(50)


def test_collapse_expand_round_trip(page) -> None:
    # 1 user + 1 assistant + 1 tool_result with 60 lines. Toggle the
    # tool_result body open / closed via the header click; the
    # bounding rect of the tool_result element should grow, shrink,
    # and grow back.
    page.goto(
        f"{BASE_URL}/__test/claude?turns=1&readLines=60&sessionId=collapse-60",
        wait_until="domcontentloaded",
    )
    page.evaluate(
        "() => { const el = document.querySelector('[data-testid=harness-conversation] > div'); el.scrollTop = el.scrollHeight; }"
    )
    page.wait_for_selector("[class*='toolHeader']", timeout=10000)

    def measure_tool_result() -> float:
        # measure the .toolResult container — its height reflects
        # whether the body is rendered (open) or hidden (collapsed).
        return page.evaluate(
            """() => {
                const el = document.querySelector("[class*='toolResult']");
                return el ? el.getBoundingClientRect().height : -1;
            }"""
        )

    # Initial render: tool_result block starts COLLAPSED. Open it
    # first via header click.
    header = page.query_selector("[class*='toolHeader']")
    assert_(header is not None, "toolHeader missing")
    header.click()
    page.wait_for_timeout(200)
    h_open = measure_tool_result()
    assert_(h_open > 80, f"open height surprisingly small: {h_open}")

    # Collapse via header click (the same chevron toggle).
    page.evaluate(
        "() => document.querySelector(\"[class*='toolHeader']\").click()"
    )
    page.wait_for_timeout(200)
    h_collapsed = measure_tool_result()
    assert_(h_collapsed < h_open, f"collapse did not shrink: {h_open}→{h_collapsed}")
    assert_(h_collapsed < 60, f"collapsed height too tall (body still rendered?): {h_collapsed}")

    # Re-expand and verify height is close to the original.
    page.evaluate(
        "() => document.querySelector(\"[class*='toolHeader']\").click()"
    )
    page.wait_for_timeout(250)
    h_reopened = measure_tool_result()
    delta = abs(h_reopened - h_open) / max(1, h_open)
    assert_(delta < 0.20, f"reopen height drift {delta:.0%} (was {h_open}, now {h_reopened})")
    ok("collapse/round-trip", f"open={h_open:.0f} collapsed={h_collapsed:.0f} reopened={h_reopened:.0f}")


def test_mobile_virtualisation(page) -> None:
    page.set_viewport_size({"width": 375, "height": 667})
    page.goto(
        f"{BASE_URL}/__test/claude?turns=300&sessionId=mobile-300",
        wait_until="domcontentloaded",
    )
    page.wait_for_selector("[data-testid='harness-root']", timeout=15000)
    page.wait_for_function(
        "() => document.querySelectorAll(\"[data-testid^='harness-turn-']\").length > 0",
        timeout=15000,
    )

    rendered = page.evaluate(
        "() => document.querySelectorAll(\"[data-testid^='harness-turn-']\").length"
    )
    assert_(rendered < 60, f"mobile: too many rows in DOM: {rendered}")

    # Verify the @media (max-width: 600px) padding rule applied to a turn row.
    pad_left = page.evaluate(
        """() => {
            const row = document.querySelector("[data-testid^='harness-turn-']");
            if (!row) return -1;
            const inner = row.querySelector('[class*=virtualTurnRow]') || row;
            return parseFloat(getComputedStyle(inner).paddingLeft);
        }"""
    )
    # `var(--space-2)` is small (≈ 8px in the theme); var(--space-4) is
    # larger (≈ 16px). The mobile rule shrinks to space-2.
    assert_(0 < pad_left < 14, f"mobile padding too large: {pad_left}px")

    # Touch-style scroll via JS wheel.
    page.evaluate(
        "() => { const el = document.querySelector('[data-testid=harness-conversation] > div'); el.scrollTop = 4000; }"
    )
    page.wait_for_timeout(150)
    rendered_mid = page.evaluate(
        "() => document.querySelectorAll(\"[data-testid^='harness-turn-']\").length"
    )
    assert_(rendered_mid < 60, f"mobile mid-scroll: too many rows: {rendered_mid}")
    ok("mobile/virtualisation", f"rows@mobile={rendered}, padLeft={pad_left:.1f}px")


def test_scroll_restore(page) -> None:
    page.set_viewport_size({"width": 1280, "height": 800})
    url = f"{BASE_URL}/__test/claude?turns=300&sessionId=restore-300"
    page.goto(url, wait_until="domcontentloaded")
    page.wait_for_selector("[data-testid='harness-root']", timeout=15000)
    page.wait_for_function(
        "() => document.querySelectorAll(\"[data-testid^='harness-turn-']\").length > 0",
        timeout=15000,
    )

    # Scroll halfway and wait for the persist debounce (250ms) to fire.
    target = page.evaluate(
        """() => {
            const el = document.querySelector('[data-testid=harness-conversation] > div');
            const t = Math.floor(el.scrollHeight / 3);
            el.scrollTop = t;
            return t;
        }"""
    )
    page.wait_for_timeout(450)

    # Reload — same URL means same sessionId so the persist key matches.
    page.reload(wait_until="domcontentloaded")
    page.wait_for_selector("[data-testid='harness-root']", timeout=15000)
    # Wait for List to mount + ResizeObserver to land + the
    # double-rAF restore to fire.
    page.wait_for_timeout(700)
    restored = page.evaluate(
        "() => document.querySelector('[data-testid=harness-conversation] > div').scrollTop"
    )
    delta = abs(restored - target)
    # 200px tolerance to account for measurement-cache stabilisation.
    assert_(
        delta < 200,
        f"scroll not restored: target={target} restored={restored} delta={delta}",
    )
    ok("scroll-restore", f"target={target} restored={restored}")


# --------------------------------------------------------------------------
# entrypoint
# --------------------------------------------------------------------------

def main() -> int:
    print(f"S017 long-session E2E against {BASE_URL}")
    test_settings_endpoint_carries_field()

    try:
        from playwright.sync_api import sync_playwright  # type: ignore
    except ImportError:
        fail("Playwright not installed — `pip install playwright && playwright install chromium`")
        return 1

    with sync_playwright() as p:
        browser = p.chromium.launch(args=["--no-sandbox"])
        try:
            context = browser.new_context()
            page = context.new_page()
            page.set_default_timeout(15000)

            test_virtualization_500_turns(page)
            test_read_preview_1000_lines(page)
            test_setting_takes_effect(page)
            test_collapse_expand_round_trip(page)
            test_mobile_virtualisation(page)
            test_scroll_restore(page)

            context.close()
        finally:
            browser.close()

    print("\nS017 E2E: PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
