#!/usr/bin/env python3
"""S009-fix-4 — Direct UI-level "Reconnecting" detection.

Previous E2E (s009_fix_periodic_check.py) measures WS continuity at the
network layer. The user still sees "Reconnecting…" labels in the browser
even after server-side metrics are clean — meaning whatever drives the
overlay is something else (FE state machine, terminal-manager LRU, prop
churn, etc.).

This harness mounts a real headless Chromium against the dev instance,
opens a single Bash tab, and tails the DOM for the literal string
"Reconnecting" anywhere in the body for `S009_FIX4_DURATION_S` seconds
(default 180). Every appearance is captured with timestamp + screenshot
into `docs/sprint-logs/S009-fix-4/` so the regression can be inspected
even after the run.

Pass = zero appearances. Fail = any appearance, with timeline.

Environment:
  PALMUX2_DEV_PORT       port of dev instance (default 8285)
  S009_FIX4_REPO_ID      repo id (default tjst-t--palmux2--2d59)
  S009_FIX4_BRANCH_ID    branch id (auto-detected if unset)
  S009_FIX4_TAB_ID       tab id (default bash:bash)
  S009_FIX4_DURATION_S   monitoring window seconds (default 180)
  S009_FIX4_OUT_DIR      where to write screenshots / trace
                         (default docs/sprint-logs/S009-fix-4)
"""

from __future__ import annotations

import asyncio
import json
import os
import sys
import time
import urllib.request
from dataclasses import dataclass, field

from playwright.async_api import async_playwright

PORT = os.environ.get("PALMUX2_DEV_PORT") or "8285"
REPO_ID = os.environ.get("S009_FIX4_REPO_ID", "tjst-t--palmux2--2d59")
BRANCH_ID = os.environ.get("S009_FIX4_BRANCH_ID", "")
TAB_ID = os.environ.get("S009_FIX4_TAB_ID", "bash:bash")
DURATION_S = float(os.environ.get("S009_FIX4_DURATION_S", "180"))
OUT_DIR = os.environ.get(
    "S009_FIX4_OUT_DIR",
    os.path.join(
        os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))),
        "docs",
        "sprint-logs",
        "S009-fix-4",
    ),
)

BASE_URL = f"http://localhost:{PORT}"


def resolve_branch_id() -> str:
    if BRANCH_ID:
        return BRANCH_ID
    with urllib.request.urlopen(f"{BASE_URL}/api/repos", timeout=10) as r:
        body = json.loads(r.read().decode())
    for repo in body if isinstance(body, list) else []:
        if repo.get("id") == REPO_ID:
            for b in repo.get("openBranches", []) or []:
                # Prefer a non-current-worktree branch (so `make serve` mutations
                # on this worktree don't bias the test). We just take the first
                # available though — sufficient for repro.
                return b["id"]
    raise SystemExit(f"no open branches for repo {REPO_ID}")


@dataclass
class Event:
    t: float
    kind: str
    detail: str = ""


@dataclass
class Result:
    started: float = 0.0
    ended: float = 0.0
    events: list[Event] = field(default_factory=list)

    def add(self, kind: str, detail: str = "") -> None:
        self.events.append(Event(t=time.time(), kind=kind, detail=detail))

    def reconnect_events(self) -> list[Event]:
        return [e for e in self.events if e.kind == "ui_reconnecting"]


async def main() -> int:
    branch_id = resolve_branch_id()
    os.makedirs(OUT_DIR, exist_ok=True)
    target_url = f"{BASE_URL}/{REPO_ID}/{branch_id}/{TAB_ID}"
    print(f"S009-fix-4 UI monitor")
    print(f"  base:    {BASE_URL}")
    print(f"  repo:    {REPO_ID}")
    print(f"  branch:  {branch_id}")
    print(f"  tab:     {TAB_ID}")
    print(f"  url:     {target_url}")
    print(f"  duration: {DURATION_S:.0f}s")
    print(f"  out:     {OUT_DIR}")

    result = Result(started=time.time())
    deadline = result.started + DURATION_S

    async with async_playwright() as pw:
        browser = await pw.chromium.launch(
            headless=True, args=["--no-sandbox", "--disable-dev-shm-usage"]
        )
        context = await browser.new_context(viewport={"width": 1280, "height": 800})
        page = await context.new_page()

        # Capture WS lifecycle for diagnostic.
        page.on(
            "websocket",
            lambda ws: ws_attach_listeners(ws, result),
        )
        page.on(
            "console",
            lambda msg: result.add(
                "console", f"{msg.type}: {msg.text[:200]}"
            )
            if msg.type in ("error", "warning")
            else None,
        )
        page.on(
            "pageerror",
            lambda err: result.add("pageerror", repr(err)[:300]),
        )

        await page.goto(target_url, wait_until="domcontentloaded")
        await page.wait_for_timeout(1500)
        result.add("nav_done", target_url)

        # Take an initial screenshot for evidence.
        try:
            await page.screenshot(path=os.path.join(OUT_DIR, "initial.png"))
        except Exception as exc:
            result.add("screenshot_error", repr(exc))

        # Tail DOM for "Reconnecting" / "Connecting" / 再接続 every 250 ms.
        # Also poll for any element with class containing "overlay" so we
        # catch the loading overlay even when the literal text is hidden.
        last_label = None
        ticks = 0
        while time.time() < deadline:
            ticks += 1
            try:
                snap = await page.evaluate(
                    """
                    () => {
                      const txt = document.body.innerText || '';
                      const overlays = Array.from(
                        document.querySelectorAll('[class*="overlay"]')
                      ).map(e => ({
                        cls: e.className,
                        txt: (e.textContent || '').slice(0, 80),
                      }));
                      return { txt, overlays };
                    }
                    """
                )
            except Exception as exc:
                result.add("eval_error", repr(exc)[:200])
                await asyncio.sleep(0.5)
                continue
            txt = (snap.get("txt") or "")
            overlays = snap.get("overlays") or []

            # Look for the canonical overlay label.
            label = None
            if "Reconnecting" in txt:
                label = "Reconnecting"
            elif any("Reconnecting" in (o.get("txt") or "") for o in overlays):
                label = "Reconnecting"

            if label and label != last_label:
                # New occurrence — record + screenshot.
                t = time.time()
                fname = f"reconnect_{int(t * 1000)}.png"
                try:
                    await page.screenshot(path=os.path.join(OUT_DIR, fname))
                except Exception:
                    pass
                result.add(
                    "ui_reconnecting",
                    f"label={label} overlays={overlays} screenshot={fname}",
                )
            last_label = label

            # Periodic progress.
            if ticks % 60 == 0:
                rec = len(result.reconnect_events())
                elapsed = time.time() - result.started
                print(
                    f"  [{elapsed:6.1f}s] reconnect_events={rec}"
                )

            await asyncio.sleep(0.25)

        # Final state snapshot.
        try:
            await page.screenshot(path=os.path.join(OUT_DIR, "final.png"))
        except Exception:
            pass

        await context.close()
        await browser.close()

    result.ended = time.time()
    rec = result.reconnect_events()
    duration = result.ended - result.started
    print()
    print(
        f"Summary: duration={duration:.1f}s  ui_reconnect_events={len(rec)}"
    )
    # Dump full event timeline to JSON.
    trace_path = os.path.join(OUT_DIR, "trace.json")
    with open(trace_path, "w") as f:
        json.dump(
            {
                "started": result.started,
                "ended": result.ended,
                "events": [
                    {"t": e.t, "rel": e.t - result.started, "kind": e.kind, "detail": e.detail}
                    for e in result.events
                ],
            },
            f,
            indent=2,
        )
    print(f"  trace: {trace_path}")
    if rec:
        print("FAIL: 'Reconnecting' text appeared in the UI")
        for e in rec[:20]:
            dt = e.t - result.started
            print(f"  +{dt:7.2f}s  {e.detail}")
        return 1
    print("PASS: no 'Reconnecting' text observed in the UI")
    return 0


def ws_attach_listeners(ws, result: Result) -> None:
    url = ws.url
    if "/attach" not in url and "/agent" not in url and "/events" not in url:
        return
    result.add("ws_open", url)
    ws.on("close", lambda: result.add("ws_close", url))
    ws.on(
        "framereceived",
        lambda payload: None,
    )


if __name__ == "__main__":
    try:
        sys.exit(asyncio.run(main()))
    except KeyboardInterrupt:
        print("\nInterrupted")
        sys.exit(2)
