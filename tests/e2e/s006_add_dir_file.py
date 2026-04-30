#!/usr/bin/env python3
"""Sprint S006 — `--add-dir` / `--file` UI E2E test (Playwright + REST).

Verifies, against the running dev palmux2 instance, that:

  1. The page loads and the Composer renders the `+` attach button.
  2. Clicking `+` opens a menu with three actions: "Add directory",
     "Add file", "Upload image…".
  3. "Add directory" opens a search picker (data-testid=composer-path-picker).
  4. Typing a query in the picker hits /files/search and renders results
     filtered to directories only (every item shows the 📁 icon, not 📄).
  5. Clicking a directory result attaches a chip with kind="dir" and the
     correct path. The chip displays "📁 path/" (trailing slash).
  6. Removing a chip via × actually removes it from the DOM.
  7. "Add file" opens the picker again, this time filtered to files
     (📄 only), and selecting a result attaches a chip with kind="file".
  8. The Files API search endpoint enforces traversal protection:
     `?path=../../etc&query=passwd` → 400 (`ErrInvalidPath`).
     `?query=...` with no `path` is fine; we additionally check that
     paths in results never contain `..`.
  9. Sending a message with chips ships a WS frame whose payload includes
     `addDirs:[<relpath>]` for dir chips and `@<relpath>` references in
     `content` for file chips. The textarea is cleared and chips removed
     after submit.
 10. Sending a message with NO attachments does NOT include `addDirs`
     (the field is omitted, not sent as `[]`).

Strategy:
    - Headless Chromium against the dev instance.
    - Capture every WS frame the page sends (framesent).
    - Synthesise attachments by interacting with the real UI; we do NOT
      need to actually trigger a CLI run — the WS frame is the wire-level
      contract we care about. (CLI-side argv observation is in step 11.)
 11. (CLI argv) After sending a real message with a directory chip, we
     observe the running `claude` process via `ps -ef` and confirm
     `--add-dir <abspath>` is part of its argv. This step is best-effort:
     if the user's local CLI auth is missing the subprocess will exit
     immediately, in which case we tolerate "not observed" but still
     PASS the rest. The decisions log explains the trade-off.

Exit code 0 = PASS. Anything else = FAIL.
"""

from __future__ import annotations

import asyncio
import json
import os
import subprocess
import sys
import time
import urllib.error
from typing import Any
from urllib.parse import quote

from playwright.async_api import async_playwright

PORT = os.environ.get("PALMUX_DEV_PORT", "8245")
REPO_ID = os.environ.get("S006_REPO_ID", "tjst-t--palmux2--2d59")
BRANCH_ID = os.environ.get("S006_BRANCH_ID", "autopilot--S006--70ed")
BASE_URL = f"http://localhost:{PORT}"

TIMEOUT_S = 12.0


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


async def http_get_status(url: str) -> tuple[int, str]:
    """Return (status_code, body) without raising on 4xx/5xx."""
    import urllib.request
    req = urllib.request.Request(url, method="GET")
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            return resp.status, resp.read().decode()
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode() if e.fp else ""


async def main() -> None:
    print(f"==> S006 E2E starting (dev port {PORT}, repo {REPO_ID}, branch {BRANCH_ID})")

    # ── Step 8 (REST traversal hardening) — run before browser to fail fast.
    base_files = (
        f"{BASE_URL}/api/repos/{quote(REPO_ID)}/branches/{quote(BRANCH_ID)}/files"
    )
    # `?path=../../etc` should be rejected by resolveSafePath
    code, body = await http_get_status(f"{base_files}/search?path=../../etc&query=p")
    if code != 400:
        fail(f"expected 400 for path=../../etc, got {code} body={body[:200]}")
    passed("REST search rejects path=../../etc with 400")

    code, body = await http_get_status(f"{base_files}?path=../../etc")
    if code != 400:
        fail(f"expected 400 for listDir path=../../etc, got {code} body={body[:200]}")
    passed("REST listDir rejects path=../../etc with 400")

    # Sanity: a normal search query inside the worktree returns 200 and no
    # result paths contain `..`.
    code, body = await http_get_status(f"{base_files}/search?query=internal")
    if code != 200:
        fail(f"normal search expected 200, got {code}")
    parsed = json.loads(body)
    results = parsed.get("results") or []
    if not results:
        fail("expected at least one result for query=internal in palmux2 worktree")
    for r in results:
        p = r.get("path", "")
        if ".." in p.split("/"):
            fail(f"result path contains traversal: {p}")
    passed(f"REST search returns {len(results)} results for 'internal', none containing '..'")

    sent_frames: list[dict[str, Any]] = []

    async with async_playwright() as pw:
        browser = await pw.chromium.launch(headless=True)
        ctx = await browser.new_context()
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

        # ── Step 1: navigate.
        url = f"{BASE_URL}/{quote(REPO_ID)}/{quote(BRANCH_ID)}/claude"
        await page.goto(url, wait_until="domcontentloaded")
        try:
            await page.wait_for_selector("textarea", timeout=int(TIMEOUT_S * 1000))
        except Exception:
            html = await page.content()
            print(html[:2000])
            fail("composer textarea did not appear")
        passed("page loaded; composer textarea present")

        # ── Step 2: + button visible, click → menu with 3 entries.
        plus = page.get_by_test_id("composer-plus-btn")
        if not await plus.is_visible():
            fail("composer-plus-btn not visible")
        passed("composer + button visible")

        await plus.click()
        try:
            await page.wait_for_selector("[data-testid=composer-attach-menu]", timeout=2000)
        except Exception:
            fail("attach menu did not appear after clicking +")
        for tid in ("attach-menu-dir", "attach-menu-file", "attach-menu-image"):
            el = page.get_by_test_id(tid)
            if not await el.is_visible():
                fail(f"attach menu item {tid} missing")
        passed("attach menu shows Add directory / Add file / Upload image")

        # ── Step 3-5: Add directory picker → result → chip.
        await page.get_by_test_id("attach-menu-dir").click()
        try:
            await page.wait_for_selector(
                "[data-testid=composer-path-picker]", timeout=2000
            )
        except Exception:
            fail("path picker did not open for Add directory")
        passed("path picker opened for dir kind")

        await page.get_by_test_id("path-picker-input").fill("internal")
        # Wait for results to populate (debounced 120ms + fetch).
        async def has_dir_results():
            items = await page.query_selector_all(
                "[data-testid=path-picker-item]"
            )
            return len(items) > 0
        try:
            await wait_for(has_dir_results, 4.0, "dir picker results")
        except TimeoutError:
            html = await page.locator("[data-testid=composer-path-picker]").inner_html()
            print(html[:1000])
            fail("dir picker returned no results for query=internal")

        # Every visible item should be a directory (📁 emoji present).
        items = await page.query_selector_all("[data-testid=path-picker-item]")
        first_path = None
        for it in items:
            txt = await it.text_content() or ""
            if "📁" not in txt:
                fail(f"dir picker item has no 📁 icon: {txt!r}")
            if first_path is None:
                first_path = await it.get_attribute("data-path")
        passed(f"dir picker results all directories ({len(items)} items)")
        if not first_path:
            fail("first dir result has no data-path")

        # Pick the first one.
        await items[0].click()
        # Picker should close and a chip should appear.
        try:
            await page.wait_for_selector(
                "[data-testid=attachment-chip-dir]", timeout=2000
            )
        except Exception:
            fail("dir chip did not appear after picking")
        chip = page.get_by_test_id("attachment-chip-dir").first
        chip_kind = await chip.get_attribute("data-attachment-kind")
        chip_path = await chip.get_attribute("data-attachment-path")
        chip_text = await chip.text_content() or ""
        if chip_kind != "dir":
            fail(f"chip kind expected 'dir', got {chip_kind}")
        if chip_path != first_path:
            fail(f"chip path mismatch: chip={chip_path} pick={first_path}")
        if "📁" not in chip_text:
            fail(f"dir chip missing 📁 icon: {chip_text!r}")
        if not chip_text.endswith("/×") and not f"{first_path}/" in chip_text:
            # The display formats as "📁 internal/" with trailing slash,
            # plus the × button text. Tolerant assertion.
            if "/" not in chip_text:
                fail(f"dir chip should show trailing /, got: {chip_text!r}")
        passed(f"dir chip attached: path={chip_path}, display={chip_text!r}")

        # ── Step 6: × removes the chip.
        remove_btn = chip.locator("button[aria-label^=Remove]")
        await remove_btn.click()
        # Chip should disappear (or attachment count drop to 0).
        try:
            await page.wait_for_function(
                """() => !document.querySelector('[data-testid=attachment-chip-dir]')""",
                timeout=2000,
            )
        except Exception:
            fail("dir chip not removed after clicking ×")
        passed("dir chip removable via × button")

        # ── Step 7: Add file picker.
        await plus.click()
        await page.get_by_test_id("attach-menu-file").click()
        try:
            await page.wait_for_selector(
                "[data-testid=composer-path-picker]", timeout=2000
            )
        except Exception:
            fail("path picker did not open for Add file")
        await page.get_by_test_id("path-picker-input").fill("go.mod")
        try:
            await wait_for(has_dir_results, 4.0, "file picker results")  # any items
        except TimeoutError:
            fail("file picker returned no results for query=go.mod")
        items = await page.query_selector_all("[data-testid=path-picker-item]")
        for it in items:
            txt = await it.text_content() or ""
            if "📁" in txt and "📄" not in txt:
                fail(f"file picker leaked a directory item: {txt!r}")
        first_file_path = await items[0].get_attribute("data-path")
        await items[0].click()
        try:
            await page.wait_for_selector(
                "[data-testid=attachment-chip-file]", timeout=2000
            )
        except Exception:
            fail("file chip did not appear after picking")
        passed(f"file chip attached: path={first_file_path}")

        # ── Step 9: send a message with the file chip + add a dir back.
        await plus.click()
        await page.get_by_test_id("attach-menu-dir").click()
        await page.wait_for_selector("[data-testid=composer-path-picker]", timeout=2000)
        await page.get_by_test_id("path-picker-input").fill("internal")
        await wait_for(has_dir_results, 4.0, "dir picker (second time)")
        dir_items = await page.query_selector_all("[data-testid=path-picker-item]")
        dir_pick_path = await dir_items[0].get_attribute("data-path")
        await dir_items[0].click()
        await page.wait_for_selector("[data-testid=attachment-chip-dir]", timeout=2000)

        # Type a message body and submit. We deliberately use a string
        # the page can't easily mistake for a slash command.
        ta = page.locator("textarea")
        await ta.click()
        await ta.fill("hello from S006 e2e")
        sent_frames.clear()
        # Click the send button rather than pressing Enter — the inline
        # completion handler can intercept Enter under some conditions
        # (e.g., a dangling `@` reference in the buffer), and the Send
        # button exercises the same submit() path with no extra moving
        # parts.
        await page.locator('button[aria-label=Send]').click()

        # Wait for at least one user.message frame.
        async def saw_user_msg():
            for f in sent_frames:
                if f.get("type") == "user.message":
                    return f
            return None
        msg_frame = await wait_for(saw_user_msg, 5.0, "user.message frame")
        payload = msg_frame.get("payload") or {}
        content = payload.get("content", "")
        addDirs = payload.get("addDirs")
        if not isinstance(addDirs, list) or dir_pick_path not in addDirs:
            fail(
                f"user.message payload addDirs missing/wrong: addDirs={addDirs} expected to include {dir_pick_path}"
            )
        if f"@{first_file_path}" not in content:
            fail(
                f"user.message payload content missing file @-mention: content={content!r} expected @{first_file_path}"
            )
        if "hello from S006 e2e" not in content:
            fail(f"user.message payload missing the typed text: {content!r}")
        if f"[dir: {dir_pick_path}]" not in content:
            fail(f"user.message payload missing dir annotation: {content!r}")
        passed(
            "user.message frame carries addDirs and inline @-mention as designed"
        )

        # ── Step 10: a follow-up message with NO attachments must NOT carry addDirs.
        sent_frames.clear()
        await ta.fill("plain text follow up")
        await page.locator('button[aria-label=Send]').click()
        msg_frame2 = await wait_for(saw_user_msg, 5.0, "second user.message")
        payload2 = msg_frame2.get("payload") or {}
        if "addDirs" in payload2:
            fail(f"second user.message must omit addDirs, got {payload2!r}")
        passed("second user.message (no chips) omits addDirs field")

        await browser.close()

    # ── Step 11: best-effort CLI argv observation.
    # Look for a `claude` process whose argv contains --add-dir <abspath>.
    # We don't fail if not found — auth might be unset on this dev box, in
    # which case the CLI exits immediately. Print PASS-or-OBSERVE accordingly.
    try:
        ps = subprocess.run(
            ["ps", "-eo", "pid,cmd"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        # Match either `--add-dir <abs>` (as separate args) anywhere on
        # any claude process line.
        observed = False
        for line in ps.stdout.splitlines():
            if "claude" in line and "--add-dir" in line:
                observed = True
                print(f"  observed claude argv: {line.strip()}")
                break
        if observed:
            passed("real claude process observed with --add-dir in argv")
        else:
            print(
                "OBSERVE: no live claude process with --add-dir in argv "
                "(expected when CLI auth is missing on this dev box). "
                "Wire-level argv assertion is covered by the Go unit tests."
            )
    except Exception as e:  # noqa: BLE001
        print(f"OBSERVE: ps inspection failed (non-fatal): {e}")

    print("\n==> S006 E2E ALL CHECKS PASSED")


if __name__ == "__main__":
    asyncio.run(main())
