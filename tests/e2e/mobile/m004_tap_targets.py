#!/usr/bin/env python3
"""Mobile E2E — tap targets honour --tap-min-size at 375px (S022-1-2).

Sweeps every visible <button> in the rendered DOM at 375x667 and confirms
that:

  (a) The CSS variable --tap-min-size resolves to 36px (per audit.md D-2).
  (b) Every button with [data-tap-mobile] has computed min-height >= 36px
      and min-width >= 36px.
  (c) The body-level fallback rule sets the minimum button height to >=
      32px (the 4px tolerance from --tap-min-size to keep dense Toolbar /
      TabBar layouts working — per audit.md D-2 floor).

Exit code 0 = PASS. Anything else = FAIL.
"""
from __future__ import annotations

import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

from conftest import (  # noqa: E402
    fail,
    mobile_context,
    ok,
    open_homepage,
    step,
    wait_for_server,
)


def main() -> None:
    print("\n[M004] tap targets at 375x667")
    wait_for_server()
    try:
        with mobile_context(viewport={"width": 375, "height": 667}) as ctx:
            page = open_homepage(ctx)

            tap = page.evaluate(
                "() => getComputedStyle(document.documentElement)"
                ".getPropertyValue('--tap-min-size').trim()"
            )
            if tap != "36px":
                fail(f"--tap-min-size should be 36px, got {tap!r}")
            ok(f"--tap-min-size = {tap}")

            step("walk every visible <button>")
            stats = page.evaluate(
                """
                () => {
                    const out = {total:0, opt_in:0, opt_in_ok:0, body_ok:0, body_fail:[]};
                    for (const b of document.querySelectorAll('button')) {
                        const r = b.getBoundingClientRect();
                        if (r.width === 0 || r.height === 0) continue; // hidden
                        out.total++;
                        const cs = getComputedStyle(b);
                        const mh = parseFloat(cs.minHeight) || 0;
                        const mw = parseFloat(cs.minWidth) || 0;
                        const hasOptIn = b.hasAttribute('data-tap-mobile');
                        if (hasOptIn) {
                            out.opt_in++;
                            if (mh >= 36 && mw >= 36) out.opt_in_ok++;
                        }
                        const ignored = b.hasAttribute('data-tap-mobile-ignore');
                        if (!ignored) {
                            // Body rule sets min-height: 32px on every visible button
                            if (mh >= 32) {
                                out.body_ok++;
                            } else {
                                out.body_fail.push({
                                    cls: b.className && (b.className.baseVal || b.className),
                                    rect: {w: r.width, h: r.height},
                                    minHeight: mh,
                                    minWidth: mw,
                                });
                            }
                        }
                    }
                    return out;
                }
                """
            )

            ok(
                f"surveyed {stats['total']} visible buttons "
                f"({stats['opt_in']} have data-tap-mobile, "
                f"{stats['body_ok']} satisfy 32px floor)"
            )

            if stats["opt_in"] > 0 and stats["opt_in_ok"] != stats["opt_in"]:
                fail(
                    f"data-tap-mobile buttons should hit 36x36 each, "
                    f"only {stats['opt_in_ok']}/{stats['opt_in']} pass"
                )
            if stats["body_fail"]:
                # Show first 3 offenders for debug.
                for f in stats["body_fail"][:3]:
                    print(f"     fail: {f}")
                fail(
                    f"{len(stats['body_fail'])} buttons fall below the 32px "
                    f"body floor (see above)"
                )
    except ModuleNotFoundError:
        print("  (skipped: playwright not installed)")
        return

    print("M004 PASS")


if __name__ == "__main__":
    sys.exit(main() or 0)
