#!/usr/bin/env python3
"""S011-fix-1 — Markdown file Edit button regression E2E.

Per S011 spec: "既存の MD preview は維持。 編集モードへトグル可能 (Monaco で md として開く)".
The original S011 implementation hard-coded `isEditable()` to return
false for `markdown`, leaving the rendered preview without an Edit
button. This test guards the fix:

  (1) Open a `.md` file → MarkdownView renders → Edit button visible
      (regression guard — was missing).
  (2) Click Edit → MonacoView mounts in edit mode with `markdown`
      language; the original markdown source is the editor content.
  (3) Type a new line → dirty indicator lights up.
  (4) Ctrl+S → save succeeds, dirty cleared, file mutated on disk.
  (5) Click Done → toggle back to view mode → MarkdownView shows
      the updated rendered Markdown (with the new line included).
  (6) Cancel/discard path: open MD, click Edit, type a draft, click
      Done while dirty → confirm dialog → dismiss returns to view
      mode without persisting the draft.
  (7) Mobile (< 600 px) parity — Edit button reachable on mobile.

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
    or "8200"
)
REPO_ID = os.environ.get("S011_REPO_ID", "tjst-t--palmux2--2d59")
BRANCH_ID = os.environ.get("S011_BRANCH_ID", "autopilot--main--S011-fix-1--4863")
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


def urllib_quote(s: str) -> str:
    import urllib.parse
    return urllib.parse.quote(s, safe="")


PRISTINE_MD = """# S011-fix-1 sample

This is a **Markdown** sample for the regression test.

- bullet 1
- bullet 2
"""


def ensure_fixture(repo_root: Path) -> Path:
    base = repo_root / "tmp" / "s011-fixtures"
    base.mkdir(parents=True, exist_ok=True)
    md = base / "fix1-sample.md"
    md.write_text(PRISTINE_MD)
    return md


async def open_file(page, rel: str) -> None:
    await page.goto(BASE_URL + file_url(rel), wait_until="load")
    await page.wait_for_selector('[data-testid="file-preview"]', timeout=15000)


async def wait_for_monaco_edit(page) -> None:
    await page.wait_for_selector('[data-testid="monaco-view"][data-mode="edit"]',
                                 timeout=15000)
    await page.locator('[data-testid="monaco-view"] .view-lines').click()
    await page.evaluate("""() => {
      const ta = document.querySelector('[data-testid="monaco-view"] textarea.inputarea');
      if (ta) ta.focus();
    }""")


async def main() -> None:
    repo_root = Path(__file__).resolve().parents[2]
    md_path = ensure_fixture(repo_root)
    md_rel = f"{FIXTURE_DIR}/fix1-sample.md"

    code, _, _ = http_json("GET", "/api/health")
    if code != 200:
        fail(f"/api/health returned {code}")

    print(f"Pre-flight: server up @ {BASE_URL}, fixture {md_rel} ready.")

    passes = 0
    async with async_playwright() as p:
        browser = await p.chromium.launch()
        try:
            ctx = await browser.new_context(viewport={"width": 1280, "height": 800})
            page = await ctx.new_page()

            # ────────── (1) MD preview shows Edit button (regression).
            await open_file(page, md_rel)
            # MarkdownView mounted (default mode = view).
            await page.wait_for_selector('[data-testid="markdown-view"]', timeout=15000)
            # Edit button MUST be visible — this assertion failed pre-fix.
            edit_btn = page.locator('[data-testid="edit-button"]')
            if await edit_btn.count() == 0:
                fail("(1) Edit button missing on .md file (S011 regression)")
            if not await edit_btn.is_visible():
                fail("(1) Edit button present but not visible on .md")
            print("(1) Edit button visible on rendered MD preview OK")
            passes += 1

            # ────────── (2) Click Edit → Monaco markdown editor.
            await edit_btn.click()
            await wait_for_monaco_edit(page)
            # Verify language attribute is markdown.
            lang = await page.locator('[data-testid="monaco-view"]').get_attribute("data-language")
            if lang != "markdown":
                fail(f"(2) expected data-language=markdown, got {lang!r}")
            # Raw markdown source should be present — Monaco shows the
            # `#` heading prefix and `-` bullet markers, which the
            # rendered MarkdownView would have replaced. We sample
            # `textContent` (not `innerText`) so Monaco's syntax
            # highlight spans don't drop characters.
            view_text = await page.locator('[data-testid="monaco-view"] .view-lines').evaluate(
                "(el) => el.textContent || ''"
            )
            # Monaco may use NBSP ( ) for indentation/render so we
            # normalize all whitespace to plain ASCII spaces before
            # asserting.
            normalized = ''.join(' ' if c.isspace() else c for c in view_text)
            if "# S011-fix-1 sample" not in normalized:
                fail(f"(2) `#` heading source missing in Monaco edit mode:\nraw={view_text!r}\nnorm={normalized!r}")
            if "- bullet 1" not in normalized:
                fail(f"(2) `-` bullet markers missing in Monaco edit mode:\nraw={view_text!r}\nnorm={normalized!r}")
            print("(2) Edit toggle → Monaco markdown source visible OK")
            passes += 1

            # ────────── (3) Type → dirty indicator on.
            await page.keyboard.press('Control+End')
            await page.keyboard.type("\n- bullet 3 from edit\n")
            await page.wait_for_selector('[data-testid="dirty-indicator"]', timeout=5000)
            print("(3) Typing flips dirty indicator on OK")
            passes += 1

            # ────────── (4) Ctrl+S saves → file mutated on disk.
            await page.keyboard.press('Control+s')
            await page.wait_for_selector('[data-testid="dirty-indicator"]',
                                         state="detached", timeout=5000)
            on_disk = md_path.read_text()
            if "bullet 3 from edit" not in on_disk:
                fail(f"(4) Ctrl+S did not save MD; file:\n{on_disk}")
            print("(4) Ctrl+S saved markdown on disk OK")
            passes += 1

            # ────────── (5) Done → view mode → updated MarkdownView.
            await page.locator('[data-testid="cancel-edit-button"]').click()
            await page.wait_for_selector('[data-testid="markdown-view"]', timeout=5000)
            md_render = await page.locator('[data-testid="markdown-view"]').inner_text()
            if "bullet 3 from edit" not in md_render:
                fail(f"(5) updated MD not rendered after toggle back:\n{md_render[:200]}")
            print("(5) Toggle back to view mode shows updated rendered MD OK")
            passes += 1

            # ────────── (6) Discard path: edit → dirty → Done → confirm.
            md_path.write_text(PRISTINE_MD)  # reset for clean baseline
            await open_file(page, md_rel)
            await page.wait_for_selector('[data-testid="markdown-view"]', timeout=15000)
            await page.locator('[data-testid="edit-button"]').click()
            await wait_for_monaco_edit(page)
            await page.keyboard.press('Control+End')
            await page.keyboard.type("\nDISCARD_ME\n")
            await page.wait_for_selector('[data-testid="dirty-indicator"]', timeout=5000)
            confirms: list[str] = []
            def _accept(d):
                confirms.append(d.message)
                asyncio.ensure_future(d.accept())
            page.on('dialog', _accept)
            await page.locator('[data-testid="cancel-edit-button"]').click()
            await page.wait_for_timeout(400)
            page.remove_listener('dialog', _accept)
            if not confirms:
                fail("(6) expected discard-confirm dialog when leaving edit with dirty draft")
            on_disk = md_path.read_text()
            if "DISCARD_ME" in on_disk:
                fail(f"(6) discarded text leaked to disk:\n{on_disk}")
            await page.wait_for_selector('[data-testid="markdown-view"]', timeout=5000)
            print(f"(6) discard path: confirm fired ({confirms[0][:40]}…), disk pristine OK")
            passes += 1

            # ────────── (7) Mobile parity.
            await ctx.close()
            ctx = await browser.new_context(viewport={"width": 414, "height": 812})
            page = await ctx.new_page()
            md_path.write_text(PRISTINE_MD)
            await open_file(page, md_rel)
            await page.wait_for_selector('[data-testid="markdown-view"]', timeout=15000)
            mob_btn = page.locator('[data-testid="edit-button"]')
            if await mob_btn.count() == 0 or not await mob_btn.is_visible():
                fail("(7) Edit button not visible on mobile MD view")
            box = await mob_btn.bounding_box()
            if not box or box["x"] + box["width"] > 430:
                fail(f"(7) Edit button overflows mobile viewport: {box}")
            await mob_btn.click()
            await wait_for_monaco_edit(page)
            print("(7) mobile (414px) Edit button reachable + Monaco mounts OK")
            passes += 1
        finally:
            await browser.close()

    print(f"\nPASS: {passes} S011-fix-1 MD-edit assertions OK")


if __name__ == "__main__":
    asyncio.run(main())
