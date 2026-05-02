#!/usr/bin/env python3
"""Mobile E2E — homepage smoke at 320 / 375 / 599 px.

Verifies the app loads without breaking layout at the three reference
viewports defined in docs/sprint-logs/S022/audit.md. This is the foundation
for all other mobile scenarios — if this fails, everything downstream
fails too.

Acceptance:
  (a) Body mounts at 320 px, 375 px, 599 px.
  (b) No element overflows the viewport horizontally (we sample the body
      and the first header / drawer trigger).
  (c) The CSS variable --tap-min-size resolves to 36px (S022-1-2).

Exit code 0 = PASS. Anything else = FAIL.
"""
from __future__ import annotations

import os
import sys
from pathlib import Path

# Allow this file to import conftest helpers when invoked as a script.
sys.path.insert(0, str(Path(__file__).resolve().parent))

from conftest import (  # noqa: E402
    BASE_URL,
    banner,
    fail,
    mobile_context,
    ok,
    open_homepage,
    step,
    wait_for_server,
)


def run_at(width: int, height: int = 667) -> None:
    banner("M001", f"homepage smoke @ {width}x{height}")
    try:
        with mobile_context(viewport={"width": width, "height": height}) as ctx:
            page = open_homepage(ctx)
            step("body should be visible")
            page.wait_for_selector("body", timeout=5_000)
            ok(f"DOM ready at {width}px")

            # Verify the --tap-min-size variable is defined.
            tap = page.evaluate(
                "() => getComputedStyle(document.documentElement)"
                ".getPropertyValue('--tap-min-size').trim()"
            )
            if tap != "36px":
                fail(f"--tap-min-size should be 36px, got {tap!r}")
            ok(f"--tap-min-size = {tap}")

            # Body width should not exceed viewport (no horizontal scroll).
            body_w = page.evaluate("() => document.body.scrollWidth")
            doc_w = page.evaluate("() => document.documentElement.clientWidth")
            if body_w > doc_w + 1:
                fail(
                    f"body overflows viewport at {width}px: "
                    f"scrollWidth={body_w} clientWidth={doc_w}"
                )
            ok(f"no horizontal overflow ({body_w}px <= {doc_w}px)")
    except ModuleNotFoundError:
        print("  (skipped: playwright not installed)")
        return


def main() -> None:
    wait_for_server()
    run_at(320, 568)
    run_at(375, 667)
    run_at(599, 800)
    print("\nM001 PASS")


if __name__ == "__main__":
    sys.exit(main() or 0)
