#!/usr/bin/env python3
"""Sprint S018 — conversation utilities (search / export / /compact).

Drives a headless Chromium against the running dev palmux2 instance
to verify:

  (a) Cmd+F (or the search=1 URL flag) shows ConversationSearchBar,
      typing the query reports a match count, navigation cycles
      through hits and scrolls them into view, Esc closes the bar.
  (b) Markdown export produces a download whose body is `## User`
      / `## Assistant` markdown.
  (c) JSON export produces a JSON envelope with palmuxExport=1 and a
      `turns` array.
  (d) compactBoundary=1 renders a kind:"compact" boundary block with
      the "Compacted: N turns into 1 summary" headline.
  (e) compacting=1 shows the spinner banner.
  (f) Mobile viewport (375px): search bar + export dialog still
      operate.

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
    or "8203"
)
BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 20.0


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


# --------------------------------------------------------------------------
# tests
# --------------------------------------------------------------------------

def test_search_bar_basic(page) -> None:
    page.set_viewport_size({"width": 1280, "height": 800})
    page.goto(
        f"{BASE_URL}/__test/claude?turns=40&search=1&sessionId=s018-search",
        wait_until="domcontentloaded",
    )
    page.wait_for_selector("[data-testid='harness-root']", timeout=15000)
    page.wait_for_selector("[data-testid='conversation-search']", timeout=10000)

    # Type the synthetic needle (injected by the harness — see
    # injectSearchNeedles in test-harness.tsx).
    page.fill("[data-testid='conversation-search-input']", "palmux-search-needle")
    page.wait_for_timeout(120)

    count_text = (
        page.locator("[data-testid='conversation-search-count']").inner_text() or ""
    ).strip()
    # Two needles injected → "1/2".
    assert_("/2" in count_text, f"unexpected match count: {count_text!r}")
    ok("search/typing", f"count={count_text}")

    # The matched row is virtualised — search auto-scrolls it into view
    # via scrollToRow but the row materialisation runs in a follow-up
    # microtask. Wait for at least one <mark> to appear in the DOM.
    page.wait_for_function(
        "() => document.querySelectorAll('mark[data-testid=search-mark]').length > 0",
        timeout=5000,
    )
    mark_count = page.locator("mark[data-testid='search-mark']").count()
    assert_(mark_count >= 1, f"no <mark> highlights rendered: {mark_count}")
    ok("search/highlight", f"marks={mark_count}")

    # Press Enter → cycles to the next match (1/2 -> 2/2).
    page.locator("[data-testid='conversation-search-input']").press("Enter")
    page.wait_for_timeout(120)
    count_text2 = (
        page.locator("[data-testid='conversation-search-count']").inner_text() or ""
    ).strip()
    assert_(count_text2.startswith("2/"), f"Enter did not advance: {count_text2}")
    ok("search/next", f"count={count_text2}")

    # Press Shift+Enter → goes back.
    page.locator("[data-testid='conversation-search-input']").press("Shift+Enter")
    page.wait_for_timeout(120)
    count_text3 = (
        page.locator("[data-testid='conversation-search-count']").inner_text() or ""
    ).strip()
    assert_(count_text3.startswith("1/"), f"Shift+Enter did not go back: {count_text3}")
    ok("search/prev", f"count={count_text3}")

    # Escape closes the bar.
    page.locator("[data-testid='conversation-search-input']").press("Escape")
    page.wait_for_timeout(120)
    visible = page.locator("[data-testid='conversation-search']").count()
    assert_(visible == 0, f"Escape did not close the bar (count={visible})")
    ok("search/close", "Escape worked")


def test_search_cmdf_keyboard(page) -> None:
    """Cmd+F (Mac shortcut, treated as Control+F by Playwright on
    Linux) opens the bar from the closed state. The harness installs
    the same keydown handler as claude-agent-view."""
    page.goto(
        f"{BASE_URL}/__test/claude?turns=20&sessionId=s018-cmdf",
        wait_until="domcontentloaded",
    )
    page.wait_for_selector("[data-testid='harness-root']", timeout=10000)
    # Bar should NOT be visible without ?search=1.
    assert_(
        page.locator("[data-testid='conversation-search']").count() == 0,
        "search bar visible before Ctrl+F",
    )
    # Focus body so the binding fires.
    page.evaluate("() => document.body.focus()")
    page.keyboard.press("Control+f")
    page.wait_for_selector("[data-testid='conversation-search']", timeout=5000)
    ok("search/ctrlF", "opened by Control+F")
    # Close again so it doesn't leak into the next test.
    page.keyboard.press("Escape")


def test_search_auto_expand_thinking(page) -> None:
    """A match inside a folded ThinkingBlock should auto-expand the
    block and surface a highlight. We use the harness's regular
    "thinking" path by injecting a thinking turn via URL — but the
    simpler path here is just verifying the search-mark count > 0
    after typing a needle that is surfaced inside a tool_result body
    with the ?readLines=200 path."""
    page.goto(
        f"{BASE_URL}/__test/claude?turns=2&readLines=200&search=1&sessionId=s018-auto",
        wait_until="domcontentloaded",
    )
    page.wait_for_selector("[data-testid='conversation-search']", timeout=10000)
    # Search for a needle that's deep in the tool_result body — the
    # harness builds lines 1..N as "<n>\tline <n> of synthetic Read
    # result", so "line 150" appears at line 150.
    page.fill("[data-testid='conversation-search-input']", "line 150")
    page.wait_for_timeout(200)
    # The tool_result block must have data-search-match on its outer
    # element (the auto-expand signal).
    has_match = page.evaluate(
        """() => !!document.querySelector("[data-search-match='true']")"""
    )
    assert_(has_match, "no [data-search-match='true'] block found")
    ok("search/auto-expand", "tool_result expanded with search match")


def test_export_markdown_download(page) -> None:
    page.set_viewport_size({"width": 1280, "height": 800})
    page.goto(
        f"{BASE_URL}/__test/claude?turns=5&export=1&sessionId=s018-md",
        wait_until="domcontentloaded",
    )
    page.wait_for_selector("[data-testid='harness-root']", timeout=10000)
    page.click("[data-testid='harness-export-btn']")
    page.wait_for_selector("[data-testid='export-dialog']", timeout=5000)

    # Default format is markdown (verify radio).
    md_checked = page.locator("[data-testid='export-format-markdown']").is_checked()
    assert_(md_checked, "Markdown should be the default format")
    fname = page.locator("[data-testid='export-filename']").input_value()
    assert_(fname.endswith(".md"), f"default filename not .md: {fname}")

    with page.expect_download() as dl_info:
        page.click("[data-testid='export-download']")
    download = dl_info.value
    path = download.path()
    assert_(path is not None, "no download path")
    body = open(path, "rb").read().decode("utf-8")
    assert_("## User" in body, "Markdown body missing '## User'")
    assert_("## Assistant" in body, "Markdown body missing '## Assistant'")
    ok("export/markdown", f"file={download.suggested_filename}, size={len(body)}B")


def test_export_json_download(page) -> None:
    page.goto(
        f"{BASE_URL}/__test/claude?turns=3&export=1&sessionId=s018-json",
        wait_until="domcontentloaded",
    )
    page.wait_for_selector("[data-testid='harness-export-btn']", timeout=10000)
    page.click("[data-testid='harness-export-btn']")
    page.wait_for_selector("[data-testid='export-dialog']", timeout=5000)
    page.click("[data-testid='export-format-json']")
    fname = page.locator("[data-testid='export-filename']").input_value()
    assert_(fname.endswith(".json"), f"filename not .json: {fname}")

    with page.expect_download() as dl_info:
        page.click("[data-testid='export-download']")
    download = dl_info.value
    path = download.path()
    body = open(path, "rb").read().decode("utf-8")
    parsed = json.loads(body)
    assert_(parsed.get("palmuxExport") == 1, f"missing palmuxExport=1: {parsed}")
    assert_(isinstance(parsed.get("turns"), list), f"turns not a list: {parsed}")
    assert_(len(parsed["turns"]) >= 1, f"no turns in export: {parsed}")
    ok("export/json", f"file={download.suggested_filename}, turns={len(parsed['turns'])}")


def test_compact_boundary_renders(page) -> None:
    page.goto(
        f"{BASE_URL}/__test/claude?turns=3&compactBoundary=1&sessionId=s018-cb",
        wait_until="domcontentloaded",
    )
    page.wait_for_selector("[data-testid='harness-root']", timeout=10000)
    page.wait_for_selector("[data-testid='compact-boundary']", timeout=10000)
    headline = (
        page.locator("[data-testid='compact-boundary']").inner_text() or ""
    ).strip()
    assert_("Compacted" in headline, f"compact boundary missing headline: {headline!r}")
    assert_("summary" in headline, f"compact boundary missing 'summary': {headline!r}")
    ok("compact/boundary", f"headline contains: {headline.splitlines()[0][:80]}")


def test_compact_spinner_renders(page) -> None:
    page.goto(
        f"{BASE_URL}/__test/claude?turns=3&compacting=1&sessionId=s018-spin",
        wait_until="domcontentloaded",
    )
    page.wait_for_selector("[data-testid='compacting-spinner']", timeout=10000)
    txt = page.locator("[data-testid='compacting-spinner']").inner_text() or ""
    assert_("Compacting" in txt, f"spinner missing label: {txt}")
    ok("compact/spinner", "spinner banner rendered")


def test_mobile_search_and_export(page) -> None:
    page.set_viewport_size({"width": 375, "height": 667})
    page.goto(
        f"{BASE_URL}/__test/claude?turns=15&search=1&export=1&sessionId=s018-mobile",
        wait_until="domcontentloaded",
    )
    page.wait_for_selector("[data-testid='harness-root']", timeout=10000)
    page.wait_for_selector("[data-testid='conversation-search']", timeout=10000)
    # Type and verify match count area exists.
    page.fill("[data-testid='conversation-search-input']", "palmux-search-needle")
    page.wait_for_timeout(120)
    count_text = (
        page.locator("[data-testid='conversation-search-count']").inner_text() or ""
    ).strip()
    assert_("/" in count_text, f"mobile search count missing: {count_text!r}")
    # Open export dialog.
    page.click("[data-testid='harness-export-btn']")
    page.wait_for_selector("[data-testid='export-dialog']", timeout=5000)
    # Check it actually fits the viewport (width <= 350px or so).
    box = page.locator(".palmux-export-dialog").bounding_box()
    assert_(box is not None, "no bounding box for export dialog")
    assert_(box["width"] < 360, f"mobile export dialog too wide: {box['width']}")
    ok("mobile/search-export", f"count={count_text}, dialogW={box['width']:.0f}")


# --------------------------------------------------------------------------
# main
# --------------------------------------------------------------------------

def main() -> int:
    print(f"Running S018 E2E against {BASE_URL}")

    # Pre-flight: make sure the dev instance is up.
    code, _ = http("GET", "/api/settings")
    if code != 200:
        fail(f"dev instance not responding (GET /api/settings → {code}). Start with `make serve INSTANCE=dev`.")
    ok("preflight", "dev instance responsive")

    from playwright.sync_api import sync_playwright

    with sync_playwright() as p:
        browser = p.chromium.launch()
        context = browser.new_context()
        page = context.new_page()
        try:
            test_search_bar_basic(page)
            test_search_cmdf_keyboard(page)
            test_search_auto_expand_thinking(page)
            test_export_markdown_download(page)
            test_export_json_download(page)
            test_compact_boundary_renders(page)
            test_compact_spinner_renders(page)
            test_mobile_search_and_export(page)
        finally:
            context.close()
            browser.close()

    print("\nALL TESTS PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
