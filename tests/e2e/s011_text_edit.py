#!/usr/bin/env python3
"""Sprint S011 — Files text edit E2E.

Drives the running dev palmux2 instance through the S011-1 acceptance
criteria for source-code editing:

  (a) Edit button toggles MonacoView into edit mode; typing changes
      the file on disk after Save.
  (b) `Ctrl+S` inside Monaco fires the same save flow.
  (c) Switching to a different file with an unsaved draft pops a
      confirm dialog (which we accept here — `localStorage` "dirty"
      survives the swap because we cancel via JS dialog handler).
  (d) Race between two writers: write the file out-of-band, then
      Save inside palmux2 → server returns 412 → conflict dialog
      appears with reload / overwrite / cancel.
  (e) Edit mode does NOT show autocomplete / suggestion popups
      (VISION scope-out). We type a long Go identifier prefix and
      verify Monaco's `suggest-widget` never lights up.
  (f) Find/Replace popup opens via Cmd+F (Monaco's `editor.action.startFindReplaceAction`).
  (g) Mobile (< 600 px) — Save button is reachable and works.

API-level checks (PUT 428 / 412 / 200) live in the same file but
use `urllib.request` — no Playwright needed for those.

Exit code 0 = PASS. Anything else = FAIL.
"""

from __future__ import annotations

import asyncio
import json
import os
import sys
import urllib.error
import urllib.request
from pathlib import Path

from playwright.async_api import async_playwright

PORT = (
    os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8276"
)
REPO_ID = os.environ.get("S011_REPO_ID", "tjst-t--palmux2--2d59")
BRANCH_ID = os.environ.get("S011_BRANCH_ID", "autopilot--main--S011--d724")
FIXTURE_DIR = "tmp/s011-fixtures"

BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 12.0


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def http(method: str, path: str, *, body: bytes | None = None,
         headers: dict[str, str] | None = None) -> tuple[int, dict[str, str], bytes]:
    url = f"{BASE_URL}{path}"
    req = urllib.request.Request(url, method=method, data=body, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            return resp.status, dict(resp.headers), resp.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers or {}), e.read()


def http_json(method: str, path: str, *, body: dict | None = None,
              headers: dict[str, str] | None = None) -> tuple[int, dict[str, str], dict | str]:
    raw = json.dumps(body).encode() if body is not None else None
    h = {"Accept": "application/json"}
    if body is not None:
        h["Content-Type"] = "application/json"
    if headers:
        h.update(headers)
    code, hdrs, data = http(method, path, body=raw, headers=h)
    try:
        decoded: dict | str = json.loads(data.decode() or "{}")
    except json.JSONDecodeError:
        decoded = data.decode(errors="replace")
    return code, hdrs, decoded


def file_url(rel: str) -> str:
    base = f"/{REPO_ID}/{BRANCH_ID}/files"
    return f"{base}/{'/'.join(rel.split('/'))}"


def api_raw(rel: str) -> str:
    return f"/api/repos/{REPO_ID}/branches/{BRANCH_ID}/files/raw?path={urllib_quote(rel)}"


def urllib_quote(s: str) -> str:
    import urllib.parse
    return urllib.parse.quote(s, safe="")


def ensure_fixtures(repo_root: Path) -> None:
    base = repo_root / "tmp" / "s011-fixtures"
    base.mkdir(parents=True, exist_ok=True)
    files = {
        "edit-me.go": "package main\n\nfunc main() {\n  // hello s011\n}\n",
        "race.txt": "first content\n",
        "ctrls.txt": "before\n",
        "switch-a.txt": "a-pristine\n",
        "switch-b.txt": "b-pristine\n",
        "mobile.txt": "mobile-before\n",
        "suggest.go": "package main\n",
        "find.go": "package main\n\nfunc helloFind() {}\nfunc helloFind2() {}\n",
        "api-only.txt": "api-original\n",
    }
    for name, body in files.items():
        path = base / name
        path.write_text(body)


def assert_api_only_flow() -> None:
    """API-level acceptance for the PUT/If-Match flow — no browser.
       Covers: 428 (no header), 412 (mismatch), 200 (match), and
       ETag round-trip on GET."""
    rel = f"{FIXTURE_DIR}/api-only.txt"

    # GET with stat — confirm ETag is present.
    code, hdrs, data = http_json("GET", api_raw(rel) + "&stat=1")
    if code != 200:
        fail(f"api: stat returned {code}: {data}")
    etag = hdrs.get("Etag") or hdrs.get("ETag")
    if not etag:
        fail("api: GET stat did not emit ETag header")

    # PUT without If-Match → 428.
    code, _, data = http_json("PUT", api_raw(rel), body={"content": "rejected"})
    if code != 428:
        fail(f"api: PUT without If-Match expected 428, got {code}: {data}")

    # PUT with bogus If-Match → 412 + ETag in response.
    code, hdrs, data = http_json("PUT", api_raw(rel), body={"content": "rejected"},
                                 headers={"If-Match": '"bogus"'})
    if code != 412:
        fail(f"api: PUT with bogus If-Match expected 412, got {code}: {data}")
    cur_etag = hdrs.get("Etag") or hdrs.get("ETag")
    if not cur_etag:
        fail("api: 412 response missing current ETag header")

    # PUT with correct If-Match → 200 and new ETag.
    code, hdrs, data = http_json("PUT", api_raw(rel), body={"content": "api-saved"},
                                 headers={"If-Match": cur_etag})
    if code != 200:
        fail(f"api: PUT with correct If-Match expected 200, got {code}: {data}")
    new_etag = hdrs.get("Etag") or hdrs.get("ETag")
    if not new_etag or new_etag == cur_etag:
        fail(f"api: ETag did not advance on save: was={cur_etag} now={new_etag}")
    if isinstance(data, dict) and data.get("etag") != new_etag:
        fail(f"api: response.etag mismatch with header: body={data} header={new_etag}")

    # Verify content actually written.
    code, _, body = http_json("GET", api_raw(rel))
    if not (isinstance(body, dict) and body.get("content") == "api-saved"):
        fail(f"api: file content not updated: {body}")


async def open_file(page, rel: str) -> None:
    await page.goto(BASE_URL + file_url(rel), wait_until="load")
    await page.wait_for_selector('[data-testid="file-preview"]', timeout=15000)


async def wait_for_monaco_edit(page) -> None:
    """Wait until the Monaco editor is mounted in edit mode and focused.
       Monaco's editable surface is `.view-lines` (the rendered text);
       the input target is the hidden `.inputarea` textarea (as opposed
       to the IME textarea which is hidden and aria-hidden). We focus
       the inputarea via JS to bypass Monaco's pointer-events-blocking
       view-line layer."""
    await page.wait_for_selector('[data-testid="monaco-view"][data-mode="edit"]',
                                 timeout=15000)
    # Click the rendered text area to position the cursor — Monaco
    # forwards the focus to its hidden inputarea automatically.
    await page.locator('[data-testid="monaco-view"] .view-lines').click()
    # Belt-and-braces focus on the `.inputarea` (textarea) directly via
    # JS — Playwright's click sometimes hits `.view-line` overlays.
    await page.evaluate("""() => {
      const ta = document.querySelector('[data-testid="monaco-view"] textarea.inputarea');
      if (ta) ta.focus();
    }""")


async def main() -> None:
    repo_root = Path(__file__).resolve().parents[2]
    ensure_fixtures(repo_root)

    # Reach the dev server.
    code, _, _ = http_json("GET", "/api/health")
    if code != 200:
        fail(f"/api/health returned {code}")

    # Confirm fixtures listable.
    code, _, listing = http_json("GET",
                                 f"/api/repos/{REPO_ID}/branches/{BRANCH_ID}/files?path={urllib_quote(FIXTURE_DIR)}")
    if code != 200:
        fail(f"fixture dir not listable (status {code}): {listing}")

    print("Pre-flight: server up, fixtures present.")

    # API-level acceptance first — fastest signal.
    assert_api_only_flow()
    print("(api) PUT 428 / 412 / 200 + ETag round-trip OK")

    passes = 1
    async with async_playwright() as p:
        browser = await p.chromium.launch()
        try:
            ctx = await browser.new_context(viewport={"width": 1280, "height": 800})
            page = await ctx.new_page()

            # ────────── (a) Edit → type → click Save → file changes.
            edit_rel = f"{FIXTURE_DIR}/edit-me.go"
            await open_file(page, edit_rel)
            await page.wait_for_selector('[data-testid="monaco-view"]', timeout=15000)
            await page.locator('[data-testid="edit-button"]').click()
            await wait_for_monaco_edit(page)
            # Move cursor to end and type a marker line.
            await page.keyboard.press('Control+End')
            await page.keyboard.type("// EDIT_MARKER_S011\n")
            # Wait for the dirty indicator to appear.
            await page.wait_for_selector('[data-testid="dirty-indicator"]', timeout=5000)
            # Click Save.
            await page.locator('[data-testid="save-button"]').click()
            # The dirty indicator should disappear within a few seconds.
            await page.wait_for_selector('[data-testid="dirty-indicator"]',
                                         state="detached", timeout=5000)
            # Verify on disk.
            on_disk = (repo_root / FIXTURE_DIR / "edit-me.go").read_text()
            if "// EDIT_MARKER_S011" not in on_disk:
                fail(f"(a) marker not on disk: got\n{on_disk}")
            print("(a) Edit → type → Save → file updated OK")
            passes += 1

            # ────────── (b) Ctrl+S also saves.
            ctrls_rel = f"{FIXTURE_DIR}/ctrls.txt"
            await open_file(page, ctrls_rel)
            await page.locator('[data-testid="edit-button"]').click()
            await wait_for_monaco_edit(page)
            await page.keyboard.press('Control+End')
            await page.keyboard.type("CTRLS_OK\n")
            await page.wait_for_selector('[data-testid="dirty-indicator"]', timeout=5000)
            # Ctrl+S inside Monaco
            await page.keyboard.press('Control+s')
            await page.wait_for_selector('[data-testid="dirty-indicator"]',
                                         state="detached", timeout=5000)
            on_disk = (repo_root / FIXTURE_DIR / "ctrls.txt").read_text()
            if "CTRLS_OK" not in on_disk:
                fail(f"(b) Ctrl+S did not save: got {on_disk!r}")
            print("(b) Ctrl+S triggers Save OK")
            passes += 1

            # ────────── (c) Confirm on switching files with unsaved draft.
            # We navigate to switch-a.txt; the FilesView resolves splat
            # to the parent dir + selected file, so the FileList shows
            # both switch-a.txt and switch-b.txt. After typing into
            # switch-a, clicking switch-b in the list triggers the
            # in-tab `selectFile()` confirm dialog.
            await open_file(page, f"{FIXTURE_DIR}/switch-a.txt")
            await page.locator('[data-testid="edit-button"]').click()
            await wait_for_monaco_edit(page)
            await page.keyboard.press('Control+End')
            await page.keyboard.type("UNSAVED_A2\n")
            await page.wait_for_selector('[data-testid="dirty-indicator"]', timeout=5000)
            confirms = []
            def _on_confirm_sync(d):
                confirms.append(d.message)
                asyncio.ensure_future(d.dismiss())  # cancel the switch
            page.on('dialog', _on_confirm_sync)
            await page.locator('button:has-text("switch-b.txt")').first.click()
            await page.wait_for_timeout(500)
            # Drop the dialog handler — the dirty buffer is still
            # active, and downstream tests could trip a confirm we
            # don't expect.
            page.remove_listener('dialog', _on_confirm_sync)
            if not confirms:
                fail("(c) expected confirm dialog when switching files with dirty buffer, "
                     "got none")
            # We dismissed → URL still on switch-a.
            if 'switch-a.txt' not in page.url:
                fail(f"(c) URL changed despite dismissing confirm: {page.url}")
            print(f"(c) confirm dialog fired on switch with dirty draft: {confirms[0][:60]}…")
            passes += 1
            # Reset the dirty state so subsequent tests don't dump
            # confirms on every navigation. Re-enter view mode (which
            # asks "discard?") and accept.
            def _accept(d):
                asyncio.ensure_future(d.accept())
            page.on('dialog', _accept)
            await page.locator('[data-testid="cancel-edit-button"]').click()
            await page.wait_for_timeout(300)
            page.remove_listener('dialog', _accept)

            # ────────── (d) Conflict 412 → conflict dialog.
            race_rel = f"{FIXTURE_DIR}/race.txt"
            # Start fresh: write known content out-of-band.
            (repo_root / FIXTURE_DIR / "race.txt").write_text("first content\n")
            await open_file(page, race_rel)
            await page.locator('[data-testid="edit-button"]').click()
            await wait_for_monaco_edit(page)
            await page.keyboard.press('Control+End')
            await page.keyboard.type("PALMUX_EDIT\n")
            await page.wait_for_selector('[data-testid="dirty-indicator"]', timeout=5000)
            # OUT-OF-BAND write — bumps mtime so the ETag changes.
            (repo_root / FIXTURE_DIR / "race.txt").write_text("second content from cli\n")
            # Now hit Save: server should reply 412 and the conflict dialog mounts.
            await page.locator('[data-testid="save-button"]').click()
            await page.wait_for_selector('[data-testid="conflict-dialog"]', timeout=5000)
            # Click Reload → discards the local draft, loads on-disk content.
            await page.locator('[data-testid="conflict-reload"]').click()
            await page.wait_for_selector('[data-testid="conflict-dialog"]',
                                         state="detached", timeout=5000)
            # After reload, dirty indicator gone.
            await page.wait_for_selector('[data-testid="dirty-indicator"]',
                                         state="detached", timeout=5000)
            print("(d) 412 → conflict dialog → Reload OK")
            passes += 1

            # ────────── (d2) Conflict overwrite path.
            (repo_root / FIXTURE_DIR / "race.txt").write_text("server-wins-content\n")
            await open_file(page, race_rel)
            await page.locator('[data-testid="edit-button"]').click()
            await wait_for_monaco_edit(page)
            await page.keyboard.press('Control+End')
            await page.keyboard.type("PALMUX_OVERWRITE\n")
            await page.wait_for_selector('[data-testid="dirty-indicator"]', timeout=5000)
            (repo_root / FIXTURE_DIR / "race.txt").write_text("server-wins-content-2\n")
            await page.locator('[data-testid="save-button"]').click()
            await page.wait_for_selector('[data-testid="conflict-dialog"]', timeout=5000)
            await page.locator('[data-testid="conflict-overwrite"]').click()
            await page.wait_for_selector('[data-testid="conflict-dialog"]',
                                         state="detached", timeout=5000)
            on_disk = (repo_root / FIXTURE_DIR / "race.txt").read_text()
            if "PALMUX_OVERWRITE" not in on_disk:
                fail(f"(d2) overwrite did not write our content: {on_disk!r}")
            print("(d2) 412 → conflict dialog → Overwrite OK")
            passes += 1

            # ────────── (e) No autocomplete suggestions (VISION scope-out).
            await open_file(page, f"{FIXTURE_DIR}/suggest.go")
            await page.locator('[data-testid="edit-button"]').click()
            await wait_for_monaco_edit(page)
            await page.keyboard.press('Control+End')
            await page.keyboard.type("\nfunc longSymbolPrefix")
            # Suggest widget would have class `suggest-widget`. Wait
            # ~600ms and assert it's not visible.
            await page.wait_for_timeout(800)
            visible = await page.evaluate("""() => {
              const s = document.querySelector('.monaco-editor .suggest-widget');
              if (!s) return false;
              return !s.classList.contains('hidden');
            }""")
            if visible:
                fail("(e) suggest-widget visible — autocomplete leaked into edit mode")
            print("(e) no autocomplete suggestions in edit mode OK")
            passes += 1

            # ────────── (f) Find/Replace popup opens via Cmd+F.
            await open_file(page, f"{FIXTURE_DIR}/find.go")
            await page.locator('[data-testid="edit-button"]').click()
            await wait_for_monaco_edit(page)
            await page.keyboard.press('Control+f')
            await page.wait_for_selector('.monaco-editor .find-widget', timeout=5000)
            print("(f) Find widget opens via Ctrl+F OK")
            passes += 1

            # ────────── (g) Mobile (< 600 px) — Save button reachable.
            await ctx.close()
            ctx = await browser.new_context(viewport={"width": 414, "height": 812})
            page = await ctx.new_page()
            mob_rel = f"{FIXTURE_DIR}/mobile.txt"
            await open_file(page, mob_rel)
            await page.locator('[data-testid="edit-button"]').click()
            await wait_for_monaco_edit(page)
            await page.keyboard.press('Control+End')
            await page.keyboard.type("MOBILE_SAVED\n")
            await page.wait_for_selector('[data-testid="dirty-indicator"]', timeout=5000)
            # Save button must still be visible inside 414px viewport.
            box = await page.locator('[data-testid="save-button"]').bounding_box()
            if not box or box["x"] + box["width"] > 430:
                fail(f"(g) Save button overflows mobile viewport: {box}")
            await page.locator('[data-testid="save-button"]').click()
            await page.wait_for_selector('[data-testid="dirty-indicator"]',
                                         state="detached", timeout=5000)
            on_disk = (repo_root / FIXTURE_DIR / "mobile.txt").read_text()
            if "MOBILE_SAVED" not in on_disk:
                fail(f"(g) mobile save did not write content: {on_disk!r}")
            print("(g) mobile (414px) Save flow OK")
            passes += 1
        finally:
            await browser.close()

    print(f"\nPASS: {passes} S011 text-edit assertions OK")


if __name__ == "__main__":
    asyncio.run(main())
