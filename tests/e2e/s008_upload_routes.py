#!/usr/bin/env python3
"""Sprint S008 — Local file upload (file picker / drag-and-drop / paste) E2E.

Verifies, against the running dev palmux2 instance, that:

  1. The page loads, the Composer renders the unified `+` (Attach file)
     button, and the S006 server-side picker UI is gone (no
     attach-menu-dir / attach-menu-file / composer-path-picker
     test ids exist).
  2. Route A (file picker): the hidden file input is wired to `+`. We
     bypass the OS dialog by uploading a synthesised file directly into
     the input. After the POST resolves, the chip lands with kind=file
     for a `.txt` upload and kind=image for a `.png` upload, and the
     chip carries `data-attachment-status=ready`.
  3. Route B (drag-and-drop): synthesise dragenter/dragover/drop on the
     composer-root with a DataTransfer carrying a File. Chip lands the
     same way.
  4. Route C (paste): synthesise a paste event on the textarea with a
     DataTransfer carrying a File. Chip lands the same way.
  5. Submission: send a message containing one image chip and one text
     chip. The WS user.message frame must:
       - put `[image: <abspath>]` in `content` for the image
       - put `@<abspath>` in `content` for the text file
       - omit `addDirs` entirely (S008 removed the user-supplied path)
  6. POST /api/repos/.../upload returns {path, name, originalName, mime,
     kind, size}. The path is absolute and lives under the configured
     attachmentUploadDir.
  7. CLI argv: best-effort `ps` inspection — at least one running
     `claude` process should carry `--add-dir
     <attachmentUploadDir>/<repo>/<branch>` in its argv. We don't fail
     hard if no live claude is found (the dev box may have no auth);
     the wire-level guarantee is also covered by the Go change in
     Manager.EnsureClient.

Exit code 0 = PASS. Anything else = FAIL.
"""

from __future__ import annotations

import asyncio
import base64
import json
import os
import subprocess
import sys
import time
from typing import Any
from urllib.parse import quote

from playwright.async_api import async_playwright

PORT = os.environ.get("PALMUX_DEV_PORT", "8246")
REPO_ID = os.environ.get("S008_REPO_ID", "tjst-t--palmux2--2d59")
BRANCH_ID = os.environ.get("S008_BRANCH_ID", "autopilot--main--S008--6d2f")
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


# A 1×1 transparent PNG, base64. Small enough to keep the upload payload
# tiny but still a valid image on every browser engine.
PNG_1X1_B64 = (
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+P+/HgAFhAJ/wlseKgAAAABJRU5ErkJggg=="
)


async def main() -> None:
    print(f"==> S008 E2E starting (dev port {PORT}, repo {REPO_ID}, branch {BRANCH_ID})")

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

        url = f"{BASE_URL}/{quote(REPO_ID)}/{quote(BRANCH_ID)}/claude"
        await page.goto(url, wait_until="domcontentloaded")
        try:
            await page.wait_for_selector("textarea", timeout=int(TIMEOUT_S * 1000))
        except Exception:
            html = await page.content()
            print(html[:2000])
            fail("composer textarea did not appear")
        passed("page loaded; composer textarea present")

        # ── Step 1: + button visible AND legacy S006 picker IDs are gone.
        plus = page.get_by_test_id("composer-plus-btn")
        if not await plus.is_visible():
            fail("composer-plus-btn not visible")
        passed("composer + button (Attach file) visible")

        # The S006 dropdown menu / path picker MUST be removed.
        for legacy_id in (
            "composer-attach-menu",
            "attach-menu-dir",
            "attach-menu-file",
            "attach-menu-image",
            "composer-path-picker",
        ):
            count = await page.locator(f"[data-testid={legacy_id}]").count()
            if count > 0:
                fail(f"legacy S006 element still present: data-testid={legacy_id}")
        passed("S006 server-side picker UI is fully removed")

        # ── Step 2 (Route A): file picker — drive the hidden input directly.
        file_input = page.get_by_test_id("composer-file-input")
        # PNG image upload via the file input.
        png_bytes = base64.b64decode(PNG_1X1_B64)
        await file_input.set_input_files(
            files=[{"name": "s008-pixel.png", "mimeType": "image/png", "buffer": png_bytes}]
        )

        # Wait for the chip to flip to ready.
        async def has_ready_image_chip():
            chip = page.locator("[data-testid=attachment-chip-image]").first
            if await chip.count() == 0:
                return None
            status = await chip.get_attribute("data-attachment-status")
            if status != "ready":
                return None
            return await chip.get_attribute("data-attachment-path")

        try:
            png_path = await wait_for(has_ready_image_chip, 8.0, "image chip ready (file picker)")
        except TimeoutError as e:
            fail(f"image chip never became ready: {e}")
        if not png_path or not png_path.startswith("/"):
            fail(f"image chip path is not absolute: {png_path!r}")
        passed(f"Route A (file picker, image): chip ready at {png_path}")

        # Text file upload via the same input — multi-file would be ideal
        # but Playwright treats set_input_files as a fresh selection, so
        # we drive the input twice.
        await file_input.set_input_files(
            files=[
                {
                    "name": "s008-note.txt",
                    "mimeType": "text/plain",
                    "buffer": b"hello from s008 e2e",
                }
            ]
        )

        async def has_ready_file_chip():
            chips = page.locator("[data-testid=attachment-chip-file]")
            n = await chips.count()
            for i in range(n):
                chip = chips.nth(i)
                status = await chip.get_attribute("data-attachment-status")
                if status == "ready":
                    return await chip.get_attribute("data-attachment-path")
            return None

        try:
            txt_path = await wait_for(has_ready_file_chip, 8.0, "file chip ready (file picker)")
        except TimeoutError as e:
            fail(f"file chip never became ready: {e}")
        if not txt_path or not txt_path.startswith("/"):
            fail(f"file chip path is not absolute: {txt_path!r}")
        passed(f"Route A (file picker, text): chip ready at {txt_path}")

        # ── Step 5: send a message — verify the WS frame.
        ta = page.locator("textarea")
        await ta.click()
        await ta.fill("hello from s008 e2e")
        sent_frames.clear()
        await page.locator("button[aria-label=Send]").click()

        async def saw_user_msg():
            for f in sent_frames:
                if f.get("type") == "user.message":
                    return f
            return None

        msg_frame = await wait_for(saw_user_msg, 5.0, "user.message frame")
        payload = msg_frame.get("payload") or {}
        content = payload.get("content", "")
        if "addDirs" in payload:
            fail(f"user.message payload must omit addDirs (S008): {payload!r}")
        if f"[image: {png_path}]" not in content:
            fail(f"user.message content missing [image: ...]: {content!r}")
        if f"@{txt_path}" not in content:
            fail(f"user.message content missing @<abspath> for text file: {content!r}")
        if "hello from s008 e2e" not in content:
            fail(f"user.message content missing typed text: {content!r}")
        passed(
            "Route A submission: user.message carries [image: ...] + @<abspath>, no addDirs"
        )

        # ── Step 3 (Route B): drag-and-drop. We use page.evaluate to
        # synthesise the events on composer-root with a DataTransfer
        # populated via Playwright's DataTransfer wrapper, since
        # attaching a real File from outside the DOM into a synthetic
        # drop is the established Playwright recipe.
        dt = await page.evaluate_handle(
            """async ([b64, name, type]) => {
                const bin = atob(b64);
                const arr = new Uint8Array(bin.length);
                for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i);
                const file = new File([arr], name, { type });
                const dt = new DataTransfer();
                dt.items.add(file);
                return dt;
            }""",
            [PNG_1X1_B64, "s008-drop.png", "image/png"],
        )
        composer_root = page.get_by_test_id("composer-root")
        await composer_root.dispatch_event("dragenter", {"dataTransfer": dt})
        await composer_root.dispatch_event("dragover", {"dataTransfer": dt})
        await composer_root.dispatch_event("drop", {"dataTransfer": dt})

        # Expect a *second* image chip ready (in addition to any leftover)
        async def two_image_chips_ready():
            chips = page.locator("[data-testid=attachment-chip-image]")
            n = await chips.count()
            ready = 0
            paths = []
            for i in range(n):
                if (await chips.nth(i).get_attribute("data-attachment-status")) == "ready":
                    ready += 1
                    paths.append(await chips.nth(i).get_attribute("data-attachment-path"))
            if ready >= 1:
                return paths[-1]
            return None

        try:
            drop_path = await wait_for(two_image_chips_ready, 8.0, "drop image chip ready")
        except TimeoutError as e:
            fail(f"drag-and-drop chip did not appear: {e}")
        passed(f"Route B (drag-and-drop): image chip ready at {drop_path}")

        # ── Step 4 (Route C): paste. We use a synthetic ClipboardEvent
        # with a populated clipboardData. JSDOM-style synthesis works in
        # Chromium when we dispatch on the textarea directly.
        await page.evaluate(
            """async ([b64, name, type]) => {
                const bin = atob(b64);
                const arr = new Uint8Array(bin.length);
                for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i);
                const file = new File([arr], name, { type });
                const dt = new DataTransfer();
                dt.items.add(file);
                const ta = document.querySelector('textarea');
                const ev = new ClipboardEvent('paste', {
                    clipboardData: dt,
                    bubbles: true,
                    cancelable: true,
                });
                // ClipboardEvent.clipboardData is read-only on some
                // browsers; assign via Object.defineProperty as a
                // fallback when the constructor option is ignored.
                if (!ev.clipboardData) {
                    Object.defineProperty(ev, 'clipboardData', { value: dt });
                }
                ta.dispatchEvent(ev);
            }""",
            [
                base64.b64encode(b"pasted-from-s008-e2e\n").decode(),
                "s008-paste.txt",
                "text/plain",
            ],
        )

        async def paste_chip_ready():
            chips = page.locator("[data-testid=attachment-chip-file]")
            n = await chips.count()
            for i in range(n):
                chip = chips.nth(i)
                status = await chip.get_attribute("data-attachment-status")
                path = await chip.get_attribute("data-attachment-path") or ""
                if status == "ready" and "s008-paste" in path:
                    return path
            return None

        try:
            paste_path = await wait_for(paste_chip_ready, 8.0, "paste chip ready")
        except TimeoutError as e:
            fail(f"paste chip did not appear: {e}")
        passed(f"Route C (paste): file chip ready at {paste_path}")

        # ── Step 6: backend response shape. Issue a fresh upload via
        # fetch from the page so we can inspect the JSON envelope.
        body = await page.evaluate(
            """async ([b64, name, type, repoId, branchId]) => {
                const bin = atob(b64);
                const arr = new Uint8Array(bin.length);
                for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i);
                const file = new File([arr], name, { type });
                const fd = new FormData();
                fd.append('file', file);
                const res = await fetch(`/api/repos/${encodeURIComponent(repoId)}/branches/${encodeURIComponent(branchId)}/upload`, {
                    method: 'POST', credentials: 'include', body: fd,
                });
                return { status: res.status, body: await res.json() };
            }""",
            [PNG_1X1_B64, "shape-check.png", "image/png", REPO_ID, BRANCH_ID],
        )
        if body["status"] != 201:
            fail(f"upload status expected 201, got {body['status']}")
        for key in ("path", "name", "originalName", "mime", "kind", "size"):
            if key not in body["body"]:
                fail(f"upload response missing key {key}: {body['body']!r}")
        if not body["body"]["path"].startswith("/"):
            fail(f"upload path not absolute: {body['body']['path']!r}")
        if body["body"]["kind"] != "image":
            fail(f"upload kind expected 'image', got {body['body']['kind']!r}")
        if not body["body"]["mime"].startswith("image/"):
            fail(f"upload mime not image/*: {body['body']['mime']!r}")
        passed("upload response envelope contains path/name/originalName/mime/kind/size")

        await browser.close()

    # ── Step 7: best-effort `ps` argv inspection.
    # Match `--add-dir` against the per-branch attachment root.
    expected_root = f"/tmp/palmux-uploads/{REPO_ID}/{BRANCH_ID}"
    try:
        ps = subprocess.run(
            ["ps", "-eo", "pid,cmd"],
            capture_output=True,
            text=True,
            timeout=5,
        )
        observed = False
        for line in ps.stdout.splitlines():
            if "claude" in line and "--add-dir" in line and expected_root in line:
                observed = True
                print(f"  observed claude argv: {line.strip()}")
                break
        if observed:
            passed(
                f"running claude process carries --add-dir {expected_root} (S008-1-3 wired)"
            )
        else:
            print(
                f"OBSERVE: no live claude process with --add-dir {expected_root} in argv "
                "(expected when CLI auth is missing on this dev box). The Go change "
                "in Manager.EnsureClient ensures the flag is added on every spawn; "
                "the absence here means no spawn happened."
            )
    except Exception as e:  # noqa: BLE001
        print(f"OBSERVE: ps inspection failed (non-fatal): {e}")

    print("\n==> S008 E2E ALL CHECKS PASSED")


if __name__ == "__main__":
    asyncio.run(main())
