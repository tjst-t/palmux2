#!/usr/bin/env python3
"""Sprint S62374c-2 — Browser tab (real backend + real Incus container), noVNC.

noVNC rework: the Browser tab no longer renders a CDP screencast <img> nor a
palmux URL bar. Instead, a headful chromium runs on Xvfb inside the container,
x11vnc serves RFB, and the frontend uses @novnc/novnc to draw a <canvas> and
forward all input (mouse/key/IME). palmux is a raw WS↔TCP byte-pipe. Japanese
input is handled server-side by fcitx5-mozc.

Requires:
  - palmux running (PALMUX2_DEV_PORT or PALMUX2_DEV_PORT_OVERRIDE)
  - An incus-container Workspace open (PALMUX2_REPO_ID, PALMUX2_BRANCH_ID)
  - The Browser tab available (palmux-ws image with chromium + xvfb + x11vnc +
    fcitx5-mozc + fcitx5-frontend-gtk*)

Acceptance criteria verified here:
  [AC-S62374c-2-1]  noVNC viewport renders a <canvas> when running
  [AC-S62374c-2-2]  click on the canvas injects input (does not throw)
  [AC-S62374c-2-3]  no palmux URL bar / Go / back / forward / reload (chromium UI)
  [AC-S62374c-2-4]  Start/Stop button + badge follow state
  [AC-S62374c-2-5]  VNC/CDP not exposed: state has no 5900/9222/addr
  [AC-S62374c-2-6]  mobile viewport (375×667) shows viewport + badge
  [AC-S62374c-2-10] ↗ Open renders browser-fullscreen standalone
  [AC-S62374c-2-11] Japanese input via noVNC canvas → fcitx5-mozc (real container)

The Japanese-IME test (AC-2-11) needs to read the remote textarea value, which
lives on the container's CDP endpoint (bridge IP, non-public). It therefore
shells out to `incus exec` on the palmux host and is SKIPPED unless
PALMUX2_INCUS_CONTAINER is set to the container instance name.

Run:
  PALMUX2_DEV_PORT=8215 \
  PALMUX2_REPO_ID=<repoId> \
  PALMUX2_BRANCH_ID=<branchId> \
  PALMUX2_INCUS_CONTAINER=<instName> \
  python3 tests/e2e/s62374c_browser_ui.py
  (PALMUX2_INCUS_CONTAINER is optional; it enables AC-2-11.)
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import time

PORT      = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8215"
)
REPO_ID   = os.environ.get("PALMUX2_REPO_ID",   "")
BRANCH_ID = os.environ.get("PALMUX2_BRANCH_ID", "")
CONTAINER = os.environ.get("PALMUX2_INCUS_CONTAINER", "")
BASE_URL  = f"http://localhost:{PORT}"
PLAYWRIGHT_TIMEOUT = 20_000

# Container-side node + playwright-core path (baked into palmux-ws image).
PWCORE = "/usr/lib/node_modules/playwright-core"
INCUS_USER = ["--user", "1000", "--group", "1000",
              "--env", "HOME=/home/ubuntu", "--env", "DISPLAY=:99"]

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def skip(name: str, msg: str) -> None:
    print(f"  [{name}] SKIP: {msg}")


def get_playwright():
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("FAIL: playwright not installed", file=sys.stderr)
        sys.exit(1)
    return sync_playwright


def browser_base() -> str:
    return f"/api/repos/{REPO_ID}/branches/{BRANCH_ID}/browser"


def api_get(url: str) -> dict:
    import urllib.request
    req = urllib.request.Request(f"{BASE_URL}{url}", headers={"Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=10) as resp:
        return json.loads(resp.read())


def api_post(url: str, body: dict | None = None) -> dict:
    import urllib.request
    data = json.dumps(body or {}).encode()
    req = urllib.request.Request(
        f"{BASE_URL}{url}", data=data, method="POST",
        headers={"Content-Type": "application/json", "Accept": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=15) as resp:
        return json.loads(resp.read())


def ensure_running() -> bool:
    """Start the browser if not running; return True once running."""
    s = api_get(f"{browser_base()}/state")
    if s.get("state") == "running":
        return True
    try:
        api_post(f"{browser_base()}/start")
    except Exception:  # noqa: BLE001
        return False
    deadline = time.time() + 30
    while time.time() < deadline:
        if api_get(f"{browser_base()}/state").get("state") == "running":
            return True
        time.sleep(1)
    return False


def _goto_browser(page, base_url=None) -> None:
    url = base_url or BASE_URL
    page.goto(f"{url}/{REPO_ID}/{BRANCH_ID}/browser",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_selector("[data-testid='browser-tab-panel']",
                           timeout=PLAYWRIGHT_TIMEOUT)


def _wait_canvas(page):
    """Wait for the noVNC viewport and its <canvas> child; return the canvas."""
    page.wait_for_selector("[data-testid='browser-viewport']", timeout=PLAYWRIGHT_TIMEOUT)
    for _ in range(40):
        c = page.query_selector("[data-testid='browser-viewport'] canvas")
        if c:
            return c
        time.sleep(0.5)
    return None


# ─── AC-2-4: Start/Stop + badge ───────────────────────────────────────────────

def test_ac4_start_stop(page) -> None:
    """[AC-S62374c-2-4] Start → badge=running, Stop → badge=stopped."""
    name = "AC-S62374c-2-4"
    _goto_browser(page)

    try:
        api_post(f"{browser_base()}/stop")
    except Exception:  # noqa: BLE001
        pass
    page.reload(wait_until="load")
    page.wait_for_selector("[data-testid='browser-tab-panel']", timeout=PLAYWRIGHT_TIMEOUT)

    start_btn = page.locator("[data-testid='browser-start']")
    if start_btn.count() == 0:
        page.locator("[data-testid='browser-stop']").first.click()
        page.wait_for_selector("[data-testid='browser-start']", timeout=PLAYWRIGHT_TIMEOUT)

    start_btn.first.click()

    def badge_is_running():
        t = (page.locator("[data-testid='browser-state-badge']").first.text_content() or "")
        return "running" in t.lower()

    deadline = time.time() + 30
    while time.time() < deadline:
        if badge_is_running():
            break
        time.sleep(1)

    badge_text = page.locator("[data-testid='browser-state-badge']").first.text_content() or ""
    if "running" not in badge_text.lower():
        fail(name, f"badge did not reach 'running' within 30s (got {badge_text!r})")
        return
    ok(name, "badge reached 'running' after Start")

    page.locator("[data-testid='browser-stop']").first.click()
    page.wait_for_selector("[data-testid='browser-start']", timeout=PLAYWRIGHT_TIMEOUT)
    badge_text = page.locator("[data-testid='browser-state-badge']").first.text_content() or ""
    if "stopped" not in badge_text.lower():
        fail(name, f"badge did not reach 'stopped' after Stop (got {badge_text!r})")
        return
    ok(name, "badge reached 'stopped' after Stop")


# ─── AC-2-1: noVNC canvas renders ─────────────────────────────────────────────

def test_ac1_canvas_renders(page) -> None:
    """[AC-S62374c-2-1] noVNC viewport renders a <canvas> when running."""
    name = "AC-S62374c-2-1"
    if not ensure_running():
        fail(name, "browser did not reach running state")
        return
    _goto_browser(page)
    canvas = _wait_canvas(page)
    if not canvas:
        fail(name, "noVNC <canvas> never appeared inside browser-viewport")
        return
    ok(name, "noVNC canvas rendered in viewport (RFB connected)")


# ─── AC-2-2: click input injection ───────────────────────────────────────────

def test_ac2_click_input(page) -> None:
    """[AC-S62374c-2-2] Click on the noVNC canvas injects input (no throw)."""
    name = "AC-S62374c-2-2"
    if not ensure_running():
        fail(name, "browser not running — skipping input test")
        return
    _goto_browser(page)
    canvas = _wait_canvas(page)
    if not canvas:
        fail(name, "no canvas to click")
        return
    try:
        # Click via the viewport center (canvas may report 0-box mid-connect).
        vp = page.locator("[data-testid='browser-viewport']").first
        vp.click(position={"x": 80, "y": 80}, timeout=5000)
        ok(name, "click on noVNC canvas did not throw (RFB pointer dispatched)")
    except Exception as e:  # noqa: BLE001
        fail(name, f"canvas click raised: {e}")


# ─── AC-2-3: no palmux URL bar (chromium's own UI) ───────────────────────────

def test_ac3_no_palmux_urlbar(page) -> None:
    """[AC-S62374c-2-3] palmux has no URL bar / nav buttons (chromium's UI)."""
    name = "AC-S62374c-2-3"
    if not ensure_running():
        fail(name, "browser not running")
        return
    _goto_browser(page)
    _wait_canvas(page)
    removed = ["browser-url-input", "browser-go", "browser-back",
               "browser-forward", "browser-reload", "browser-keycapture"]
    present = [tid for tid in removed
               if page.locator(f"[data-testid='{tid}']").count() > 0]
    if present:
        fail(name, f"palmux still renders removed nav controls: {present}")
        return
    ok(name, "no palmux URL bar / nav controls (navigation via chromium UI)")


# ─── AC-2-5: VNC/CDP not exposed to client ───────────────────────────────────

def test_ac5_ports_not_exposed(page) -> None:
    """[AC-S62374c-2-5] VNC(5900)/CDP(9222)/addr never surfaced to the client."""
    name = "AC-S62374c-2-5"
    s = api_get(f"{browser_base()}/state")
    blob = json.dumps(s)
    leaked = [tok for tok in ("9222", "5900", "addr") if tok in blob]
    if leaked:
        fail(name, f"state response leaked {leaked}: {s}")
        return
    ok(name, "no CDP/VNC port or addr in state response (backend-only)")


# ─── AC-2-6: mobile viewport ─────────────────────────────────────────────────

def test_ac6_mobile_viewport(page) -> None:
    """[AC-S62374c-2-6] Mobile width (375px) shows viewport + badge."""
    name = "AC-S62374c-2-6"
    if not ensure_running():
        fail(name, "browser not running — skipping mobile test")
        return
    _goto_browser(page)
    _wait_canvas(page)
    for tid in ["browser-state-badge", "browser-viewport"]:
        el = page.locator(f"[data-testid='{tid}']").first
        if not el.is_visible():
            fail(name, f"[data-testid='{tid}'] not visible at mobile width")
            return
    ok(name, "mobile viewport: badge + viewport visible")


# ─── AC-2-10: fullscreen popout ──────────────────────────────────────────────

def test_ac10_fullscreen_popout(page) -> None:
    """[AC-S62374c-2-10] ↗ Open → browser-fullscreen standalone page."""
    name = "AC-S62374c-2-10"
    if not ensure_running():
        fail(name, "browser not running — skipping popout test")
        return
    _goto_browser(page)
    popout = page.locator("[data-testid='browser-popout']").first
    if not popout.is_visible():
        fail(name, "browser-popout not visible in running state")
        return
    href = popout.get_attribute("href") or ""
    full_url = f"{BASE_URL}{href}" if href.startswith("/") else href
    page.goto(full_url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    try:
        page.wait_for_selector("[data-testid='browser-fullscreen']", timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:  # noqa: BLE001
        fail(name, f"browser-fullscreen not found at {full_url}")
        return
    ok(name, "browser-fullscreen renders standalone at popout href")


# ─── AC-2-11: Japanese input via noVNC + fcitx5-mozc ──────────────────────────

def _incus(args: list[str], as_user: bool = True, timeout: int = 30) -> subprocess.CompletedProcess:
    cmd = ["incus", "exec"] + (INCUS_USER if as_user else []) + [CONTAINER, "--"] + args
    return subprocess.run(cmd, capture_output=True, text=True, timeout=timeout)


def _stage_ime_page() -> None:
    """Write /tmp/ime.html in the container and navigate remote chromium to it."""
    html = ("<!doctype html><meta charset=utf-8><body style=\"margin:0\">"
            "<textarea id=t autofocus style=\"width:1400px;height:600px;font-size:32px\">"
            "</textarea></body>")
    _incus(["sh", "-c", f"cat > /tmp/ime.html <<'EOF'\n{html}\nEOF"])
    nav = (
        "const{chromium}=require('%s');(async()=>{"
        "const b=await chromium.connectOverCDP('http://127.0.0.1:9222');"
        "const c=b.contexts()[0];const p=c.pages()[0]||await c.newPage();"
        "await p.goto('file:///tmp/ime.html');"
        "await p.evaluate(()=>{const t=document.querySelector('#t');t.value='';t.focus();});"
        "await p.bringToFront();await b.close();})().catch(e=>{console.error(e.message);process.exit(1)})"
    ) % PWCORE
    _incus(["node", "-e", nav])


def _read_textarea() -> str:
    rd = (
        "const{chromium}=require('%s');(async()=>{"
        "const b=await chromium.connectOverCDP('http://127.0.0.1:9222');"
        "const p=b.contexts()[0].pages()[0];"
        "const v=await p.evaluate(()=>document.querySelector('#t')?document.querySelector('#t').value:'');"
        "console.log('VAL='+JSON.stringify(v));await b.close();})()"
        ".catch(e=>{console.error(e.message);process.exit(1)})"
    ) % PWCORE
    r = _incus(["node", "-e", rd])
    for line in (r.stdout or "").splitlines():
        if line.startswith("VAL="):
            return json.loads(line[4:])
    return ""


def test_ac11_japanese_input(page) -> None:
    """[AC-S62374c-2-11] Japanese input via noVNC canvas → fcitx5-mozc."""
    name = "AC-S62374c-2-11"
    if not CONTAINER:
        skip(name, "PALMUX2_INCUS_CONTAINER not set (needs container CDP readback)")
        return
    if not ensure_running():
        fail(name, "browser not running")
        return

    # Stage a focused, empty textarea in the remote chromium.
    try:
        _stage_ime_page()
    except Exception as e:  # noqa: BLE001
        fail(name, f"failed to stage remote ime.html: {e}")
        return

    # Open the noVNC viewport and send keys through the real RFB path.
    _goto_browser(page)
    canvas = _wait_canvas(page)
    if not canvas:
        fail(name, "no canvas to type into")
        return
    time.sleep(2)  # let RFB finish the initial framebuffer
    vp = page.locator("[data-testid='browser-viewport']").first
    box = vp.bounding_box()
    page.mouse.click(box["x"] + box["width"] / 2, box["y"] + box["height"] / 2)
    time.sleep(0.6)

    page.keyboard.press("Control+Space"); time.sleep(0.5)   # activate mozc
    for ch in "nihongo":
        page.keyboard.press(ch); time.sleep(0.07)
    time.sleep(0.6)
    page.keyboard.press("Space"); time.sleep(0.6)           # convert にほんご→日本語
    page.keyboard.press("Enter"); time.sleep(0.6)           # commit
    page.keyboard.press("Control+Space"); time.sleep(0.3)   # back to ASCII
    for ch in "xyz":
        page.keyboard.press(ch); time.sleep(0.07)
    time.sleep(1.0)

    value = _read_textarea()
    has_jp = any("぀" <= c <= "ヿ" or "一" <= c <= "鿿" for c in value)
    if not has_jp:
        fail(name, f"no Japanese in remote textarea after noVNC input (got {value!r})")
        return
    if "xyz" not in value:
        fail(name, f"ASCII portion missing after IME toggle (got {value!r})")
        return
    ok(name, f"Japanese input via noVNC verified (textarea={value!r})")


# ─── Runner ───────────────────────────────────────────────────────────────────

def main() -> int:
    if not REPO_ID or not BRANCH_ID:
        print(
            "SKIP: PALMUX2_REPO_ID and PALMUX2_BRANCH_ID not set. "
            "Real-backend E2E requires an open incus-container Workspace.",
            file=sys.stderr,
        )
        return 0

    sync_playwright = get_playwright()
    tests_desktop = [
        test_ac4_start_stop,
        test_ac1_canvas_renders,
        test_ac2_click_input,
        test_ac3_no_palmux_urlbar,
        test_ac5_ports_not_exposed,
        test_ac10_fullscreen_popout,
        test_ac11_japanese_input,
    ]
    tests_mobile = [
        test_ac6_mobile_viewport,
    ]

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True, args=["--no-sandbox"])
        try:
            for tc in tests_desktop:
                ctx = browser.new_context(viewport={"width": 1280, "height": 800})
                pg = ctx.new_page()
                try:
                    tc(pg)
                except Exception as e:  # noqa: BLE001
                    fail(tc.__name__, f"unexpected: {e}")
                finally:
                    ctx.close()

            for tc in tests_mobile:
                ctx = browser.new_context(viewport={"width": 375, "height": 667},
                                          is_mobile=True)
                pg = ctx.new_page()
                try:
                    tc(pg)
                except Exception as e:  # noqa: BLE001
                    fail(tc.__name__, f"unexpected: {e}")
                finally:
                    ctx.close()
        finally:
            browser.close()

    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
