#!/usr/bin/env python3
"""Sprint Saa8506-2 — s006 attach (drag-and-drop) WIRE-level test.

This test resurrects the wire-level contract that S006 originally
established: attaching files to a Claude turn results in a well-shaped
`user.message` WebSocket frame carrying `@<abspath>` references (for
files) and `[image: <abspath>]` placeholders (for images), with the
filesystem-access boundary (`--add-dir`) delivered out-of-band via the
CLI spawn arguments rather than the WS frame.

S008 deleted the original click-to-pick UI and moved the attachment
entry point to drag-and-drop / paste / file-picker on the Composer.
This test asserts the wire-level effect of a drag-and-drop attach,
which the truncated `s006_add_dir_file.py` no longer covers.

Why this is its own file (separate from s008_upload_routes.py):
  - s008 covers the *end-to-end upload pipeline* — REST `/api/upload`
    + filesystem layout + per-tab attachment chips + 3 entry routes.
  - s006_attach_dnd_wire covers the *single wire-level invariant*:
    "after a drag-and-drop attach, the user.message WS frame carries
    @<abspath> for files, [image:...] for images, and NO addDirs key".
    A future refactor of the upload pipeline (replacing the chip UI,
    changing the upload protocol, etc.) might require rewriting s008
    in non-trivial ways but the wire contract this file enforces is
    the production-facing observable.

Acceptance criteria:
  [AC-Saa8506-2-1]
    - drag-and-drop event is dispatched to the Composer surface
    - the resulting WS frame is captured via Playwright's `framesent`
    - the frame carries `addDirs`-equivalent (= multiple `@<abspath>`
      lines, one per dropped file) and `@` file refs in the body
    - the frame's payload does NOT contain a top-level `addDirs` key
      (the upload root is conveyed via `claude --add-dir` at spawn)

  [AC-Saa8506-2-2]
    - this test uses the hermetic _fixture.py pattern (see
      `palmux2_test_fixture()` and `Fixture.open_claude_tab()`).

Exit code 0 = PASS.
"""
from __future__ import annotations

import asyncio
import base64
import json
import os
import sys
import time
from pathlib import Path
from typing import Any
from urllib.parse import quote

from playwright.async_api import async_playwright

# Saa8506: hermetic fixture.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from _fixture import palmux2_test_fixture, BASE_URL  # noqa: E402

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8215"
)

TIMEOUT_S = 12.0

# 1x1 transparent PNG, used in DataTransfer for the "image" dropped item.
PNG_1X1_B64 = (
    "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJ"
    "AAAADUlEQVR42mNk+P+/HgAFhAJ/wlseKgAAAABJRU5ErkJggg=="
)


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


async def main() -> None:
    print(f"==> Saa8506-2 / s006 dnd wire test starting (dev port {PORT})")
    with palmux2_test_fixture("s006-dnd") as fx:
        repo_id = fx.repo_id
        branch_id = fx.primary_branch_id()
        fx.open_claude_tab(branch_id)
        print(f"  hermetic repo={repo_id}  branch={branch_id}")
        await _run(repo_id, branch_id)


async def _run(repo_id: str, branch_id: str) -> None:
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

        # Inspect every frame sent to the agent WS so we can assert on
        # the user.message frame shape.
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

        url = f"{BASE_URL}/{quote(repo_id)}/{quote(branch_id)}/claude"
        await page.goto(url, wait_until="domcontentloaded")
        try:
            await page.wait_for_selector("textarea", timeout=int(TIMEOUT_S * 1000))
        except Exception:
            html = await page.content()
            print(html[:1500])
            fail("composer textarea did not appear")
        passed("page loaded; composer textarea present")

        composer_root = page.get_by_test_id("composer-root")
        if not await composer_root.is_visible():
            fail("composer-root not visible")
        passed("composer-root drop target visible")

        # ── 1. Drop a "directory-like" set: an image PNG + a text file.
        # This simulates the S006 era 'attach a directory of stuff' user
        # intent: multiple files arriving in one DataTransfer drop.
        dt = await page.evaluate_handle(
            """async ([b64Png, txtB64, imgName, txtName]) => {
                const decode = (b64) => {
                    const bin = atob(b64);
                    const arr = new Uint8Array(bin.length);
                    for (let i = 0; i < bin.length; i++) arr[i] = bin.charCodeAt(i);
                    return arr;
                };
                const png = new File([decode(b64Png)], imgName, { type: 'image/png' });
                const txt = new File(
                    [decode(txtB64)], txtName, { type: 'text/plain' }
                );
                const dt = new DataTransfer();
                dt.items.add(png);
                dt.items.add(txt);
                return dt;
            }""",
            [
                PNG_1X1_B64,
                base64.b64encode(b"hello from saa8506 dnd wire\n").decode(),
                "saa8506-dnd-image.png",
                "saa8506-dnd-note.txt",
            ],
        )
        await composer_root.dispatch_event("dragenter", {"dataTransfer": dt})
        await composer_root.dispatch_event("dragover", {"dataTransfer": dt})
        await composer_root.dispatch_event("drop", {"dataTransfer": dt})
        passed("dispatched dragenter / dragover / drop on composer-root")

        # ── 2. Wait for both chips to flip to ready (= upload succeeded).
        async def two_ready_chips() -> dict[str, str] | None:
            img_chips = page.locator("[data-testid=attachment-chip-image]")
            file_chips = page.locator("[data-testid=attachment-chip-file]")
            img_path: str | None = None
            for i in range(await img_chips.count()):
                chip = img_chips.nth(i)
                if (await chip.get_attribute("data-attachment-status")) != "ready":
                    continue
                p = await chip.get_attribute("data-attachment-path")
                if p and "saa8506-dnd-image" in p:
                    img_path = p
                    break
            file_path: str | None = None
            for i in range(await file_chips.count()):
                chip = file_chips.nth(i)
                if (await chip.get_attribute("data-attachment-status")) != "ready":
                    continue
                p = await chip.get_attribute("data-attachment-path")
                if p and "saa8506-dnd-note" in p:
                    file_path = p
                    break
            if img_path and file_path:
                return {"image": img_path, "file": file_path}
            return None

        try:
            paths = await wait_for(two_ready_chips, 10.0, "both dnd chips ready")
        except TimeoutError as e:
            fail(f"drag-and-drop chips never became ready: {e}")
        passed(
            f"image chip path={paths['image']}, file chip path={paths['file']}"
        )

        # The upload root reported by the dev server's --add-dir flag.
        expected_upload_root = (
            f"/tmp/palmux-uploads/{repo_id}/{branch_id}"
        )
        if not paths["image"].startswith(expected_upload_root):
            fail(
                f"image upload path does not live under per-branch root: "
                f"got {paths['image']!r}, expected prefix {expected_upload_root!r}"
            )
        if not paths["file"].startswith(expected_upload_root):
            fail(
                f"file upload path does not live under per-branch root: "
                f"got {paths['file']!r}, expected prefix {expected_upload_root!r}"
            )
        passed(
            f"both chips reside under per-branch upload root "
            f"({expected_upload_root})"
        )

        # ── 3. Type something + submit so the wire frame is emitted.
        # Click the explicit Send button — pressing Enter on the textarea
        # is also a valid submit path, but the button is the most
        # deterministic surface in headless Chromium across viewports.
        ta = page.locator("textarea")
        await ta.click()
        await ta.fill("Please read both attached files.")
        sent_frames.clear()
        await page.locator("button[aria-label=Send]").click()

        async def find_user_msg() -> dict | None:
            for f in sent_frames:
                if f.get("type") == "user.message":
                    return f
            return None

        msg_frame = await wait_for(find_user_msg, 5.0, "user.message frame")
        payload = msg_frame.get("payload") or {}
        content = payload.get("content", "")
        passed(
            f"user.message frame observed (content len={len(content)})"
        )

        # ── 4. Wire-level assertions:
        #   (a) `[image: <abspath>]` for the PNG
        #   (b) `@<abspath>` for the text file
        #   (c) NO top-level `addDirs` key in the payload — the
        #       filesystem-access boundary is delivered via the CLI's
        #       --add-dir flag at spawn, not via the WS frame.
        if f"[image: {paths['image']}]" not in content:
            fail(
                f"user.message content missing [image: ...]: "
                f"content={content!r}"
            )
        passed("image ref serialised as [image: <abspath>]")

        if f"@{paths['file']}" not in content:
            fail(
                f"user.message content missing @<abspath> for text file: "
                f"content={content!r}"
            )
        passed("file ref serialised as @<abspath>")

        if "addDirs" in payload:
            fail(
                f"user.message payload includes addDirs key — invariant "
                f"violated: {payload!r}"
            )
        passed("user.message payload has NO addDirs key (out-of-band only)")

        # Bonus: the prose text and both refs come through together.
        if "Please read both attached files." not in content:
            fail(f"typed prose missing from content: {content!r}")
        passed("typed prose preserved alongside refs")

        await ctx.close()
        await browser.close()

    print("==> Saa8506-2 / s006 dnd wire test PASSED")


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        sys.exit(130)
