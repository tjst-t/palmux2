#!/usr/bin/env python3
"""Sprint S4b9df4-1 — Rewind UI surface coverage (complement to s019).

Verifies the user-turn editor (pencil → editor mount → cancel/escape +
version arrow rendering for archived versions) so Story 4 can lift the
editingTurnId state back into UserTurnEditor without regressing the
flow. This is a smaller surface check than s019_rewind.py — it only
covers the **mount/cancel** path that Story 4 specifically touches.

  [AC-S4b9df4-1-4]
   - hover/click pencil reveals + opens editor
   - Escape exits editor
   - data-turn-id remains in DOM after editor exit (no row remount)

Exit code 0 = PASS. Anything else = FAIL.
"""
from __future__ import annotations

import os
import sys

from playwright.sync_api import sync_playwright

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8201"
)
BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 12.0


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def run() -> None:
    print(f"==> S4b9df4-1 rewind-flow E2E (port {PORT})")
    with sync_playwright() as pw:
        browser = pw.chromium.launch(headless=True)
        page = browser.new_page(viewport={"width": 1280, "height": 800})
        page.goto(
            f"{BASE_URL}/__test/claude?turns=6&rewind=1&sessionId=s4b9df4-rewind",
            wait_until="domcontentloaded",
        )
        page.wait_for_selector("[data-testid='harness-root']", timeout=int(TIMEOUT_S * 1000))

        # ── 1. Pencil click mounts the editor.
        pencil = page.locator("[data-testid='rewind-edit-turn-user-0']")
        pencil.wait_for(timeout=int(TIMEOUT_S * 1000))
        pencil.click(force=True)
        page.wait_for_selector("[data-testid='user-turn-editor']", timeout=int(TIMEOUT_S * 1000))
        ok("editor/mount-on-pencil-click")

        # ── 2. Escape exits the editor and the bubble re-renders.
        # Focus the editor area first so Escape goes through.
        editor = page.locator("[data-testid='user-turn-editor']")
        editor.click()
        page.keyboard.press("Escape")
        # Wait for the editor to disappear.
        page.wait_for_function(
            "() => !document.querySelector(\"[data-testid='user-turn-editor']\")",
            timeout=int(TIMEOUT_S * 1000),
        )
        ok("editor/escape-exits")

        # ── 3. data-turn-id remains in DOM after editor exit (the
        # critical contract for S4b9df4-4: lifting state back into
        # UserTurnEditor must not re-mount the row).
        turn_id = page.locator("[data-testid='harness-turn-turn-user-0']")
        assert turn_id.count() == 1, "user-0 turn vanished after editor exit"
        ok("editor/exit-does-not-unmount-row")

        # ── 4. Re-open the editor — should still work after the close.
        page.locator("[data-testid='rewind-edit-turn-user-0']").click(force=True)
        page.wait_for_selector("[data-testid='user-turn-editor']", timeout=int(TIMEOUT_S * 1000))
        ok("editor/can-re-mount")

        browser.close()
    print("\n==> S4b9df4-1 rewind-flow E2E PASSED")


if __name__ == "__main__":
    run()
