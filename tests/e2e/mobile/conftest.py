"""Shared fixtures and helpers for the Palmux2 mobile E2E suite (S022).

The mobile suite runs against the same dev instance as the desktop suite,
but **every page** is opened with a phone viewport, touch emulation, and
an iPhone SE user-agent. Tests under ``tests/e2e/mobile/`` should call
:func:`mobile_context` from the helpers module instead of constructing
their own browser context.

Why a `conftest.py` despite using script-style entry points?

  Most of the existing palmux2 E2E tests are standalone Python scripts
  (``python3 tests/e2e/sNNN_*.py``) — there is no pytest harness today.
  The mobile suite keeps that convention but **also** exposes a
  :func:`mobile_context` helper here so all mobile scripts share the
  same viewport / device emulation. Imports work because Python adds
  the test file's directory to ``sys.path`` when run as a script.
"""

from __future__ import annotations

import os
import sys
import time
import urllib.error
import urllib.request
from contextlib import contextmanager
from pathlib import Path
from typing import Any, Iterator

# Resolve the dev port the same way every other E2E test does, with the
# S022-recommended override of 8209.
DEV_PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8209"
)
BASE_URL = f"http://localhost:{DEV_PORT}"

REPO_ROOT = Path(__file__).resolve().parents[3]

# iPhone SE 2nd gen — the canonical small-mobile reference per
# docs/sprint-logs/S022/audit.md. Width 375 picks up the broadest set of
# media queries (>= 320 baseline, < 600 mobile).
DEFAULT_VIEWPORT = {"width": 375, "height": 667}
DEFAULT_USER_AGENT = (
    "Mozilla/5.0 (iPhone; CPU iPhone OS 17_4 like Mac OS X) "
    "AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Mobile/15E148 Safari/604.1"
)


# ─────────────────────────── result helpers ───────────────────────────

PASS = "PASS"
FAIL = "FAIL"


def banner(scenario_id: str, title: str) -> None:
    print(f"\n[{scenario_id}] {title}")


def step(msg: str) -> None:
    print(f"  - {msg}")


def ok(msg: str) -> None:
    print(f"  ✓ {msg}")


def fail(msg: str) -> None:
    print(f"  ✗ FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def assert_(cond: bool, msg: str) -> None:
    if not cond:
        fail(msg)


# ─────────────────────────── server health ───────────────────────────


def wait_for_server(timeout_s: float = 30.0) -> None:
    """Poll ``GET /api/repos`` until the dev instance responds."""
    deadline = time.time() + timeout_s
    last_err: Exception | None = None
    while time.time() < deadline:
        try:
            with urllib.request.urlopen(f"{BASE_URL}/api/repos", timeout=2.0) as r:
                if 200 <= r.status < 500:
                    return
        except (urllib.error.URLError, urllib.error.HTTPError, ConnectionError) as e:
            last_err = e
        time.sleep(0.4)
    raise RuntimeError(
        f"dev server at {BASE_URL} did not respond in {timeout_s}s "
        f"(last error: {last_err!r}). Run `make serve INSTANCE=dev` first."
    )


# ─────────────────────────── playwright wiring ───────────────────────────


@contextmanager
def mobile_context(
    *,
    viewport: dict[str, int] | None = None,
    user_agent: str | None = None,
    has_touch: bool = True,
    is_mobile: bool = True,
    locale: str = "en-US",
    color_scheme: str = "dark",
) -> Iterator[Any]:
    """Yield a Playwright context configured as a mobile device.

    Caller is responsible for ``page = ctx.new_page(); page.goto(...)``.
    The fixture handles browser launch + cleanup. If Playwright is not
    installed, the function raises ``ModuleNotFoundError`` — callers
    should treat that as a skip.
    """
    from playwright.sync_api import sync_playwright  # type: ignore

    vp = viewport or DEFAULT_VIEWPORT
    ua = user_agent or DEFAULT_USER_AGENT

    with sync_playwright() as p:
        browser = p.chromium.launch()
        try:
            ctx = browser.new_context(
                viewport=vp,
                user_agent=ua,
                has_touch=has_touch,
                is_mobile=is_mobile,
                locale=locale,
                color_scheme=color_scheme,
                # Playwright treats `is_mobile=True` as 2x DPR by default;
                # keep that to mirror real devices.
            )
            yield ctx
        finally:
            browser.close()


# ─────────────────────────── shared scenario glue ───────────────────────────


def open_homepage(ctx: Any) -> Any:
    """Open BASE_URL with a fresh page and return the page handle."""
    page = ctx.new_page()
    page.goto(BASE_URL, wait_until="networkidle")
    # Smoke-check: the layout root should mount within a few seconds.
    page.wait_for_selector("body", timeout=5_000)
    return page


def click_tap(page: Any, selector: str, *, timeout_ms: int = 5_000) -> None:
    """Tap-friendly click that respects mobile emulation.

    Playwright's ``locator.tap()`` requires ``has_touch=True`` (which our
    context sets). Falls back to a regular click if tap is unsupported on
    the underlying browser build (some headless versions of Chromium).
    """
    locator = page.locator(selector).first
    locator.wait_for(state="visible", timeout=timeout_ms)
    try:
        locator.tap()
    except Exception:
        locator.click()


__all__ = [
    "BASE_URL",
    "DEV_PORT",
    "DEFAULT_VIEWPORT",
    "DEFAULT_USER_AGENT",
    "REPO_ROOT",
    "wait_for_server",
    "mobile_context",
    "open_homepage",
    "click_tap",
    "banner",
    "step",
    "ok",
    "fail",
    "assert_",
]
