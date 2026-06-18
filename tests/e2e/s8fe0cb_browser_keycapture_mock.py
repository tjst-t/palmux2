#!/usr/bin/env python3
"""Sprint S8fe0cb — Browser tab raw-key forwarding past local OS IME (MOCK).

Frontend-only Playwright: all API/WS calls are intercepted so this runs against
any dev instance without a real Incus container or VNC connection. The noVNC RFB
is constructed but never connects (the WS attach is aborted); the keyboard
capture logic is observed through the component's own `__palmuxVncKeyTap` seam,
which mirrors every `rfb.sendKey(keysym, code, down)` call.

Acceptance criteria:
  [AC-S8fe0cb-1-1] Focused Browser tab captures keys on the hidden IME-disabled
                   input + rfb.sendKey forwards; local OS IME ON (keyCode 229 /
                   isComposing with a real e.code) still delivers raw keys.
  [AC-S8fe0cb-1-2] Modifiers / special keys / key-repeat / blur-release behave;
                   mouse-down on the canvas re-focuses the hidden input.
  [AC-S8fe0cb-1-3] Leaving the Browser tab removes the capture input; returning
                   re-enables it.
  [AC-S8fe0cb-1-4] Hidden input has password-manager / autofill avoidance attrs
                   and is off-screen (the real-OS-IME visual is a manual smoke).

Run:  PALMUX2_DEV_PORT=<port> python3 tests/e2e/s8fe0cb_browser_keycapture_mock.py
"""
from __future__ import annotations

import json
import os
import sys

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8215"
)
BASE_URL = f"http://localhost:{PORT}"
PLAYWRIGHT_TIMEOUT = 15_000

FAKE_REPO = "demo--repo--ab12"
FAKE_BRANCH = "feature--cd34"

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def get_playwright():
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("FAIL: playwright not installed", file=sys.stderr)
        sys.exit(1)
    return sync_playwright


def _fulfill(route, obj, status=200):
    route.fulfill(status=status, content_type="application/json", body=json.dumps(obj))


def _fake_repo(runtime_kind: str = "incus-container") -> dict:
    return {
        "id": FAKE_REPO,
        "ghqPath": "demo/repo",
        "fullPath": "/tmp/demo-repo",
        "starred": False,
        "openBranches": [{
            "id": FAKE_BRANCH,
            "name": "feature",
            "worktreePath": "/tmp/demo-repo",
            "repoId": FAKE_REPO,
            "isPrimary": True,
            "lastActivity": "2026-01-01T00:00:00Z",
            "tabSet": {
                "tmuxSession": f"_palmux_{FAKE_REPO}_{FAKE_BRANCH}",
                "tabs": [
                    {"id": "claude",    "type": "claude",  "name": "Claude",
                     "protected": True,  "multiple": False,
                     "windowName": "palmux:claude:claude"},
                    {"id": "browser",   "type": "browser", "name": "Browser",
                     "protected": False, "multiple": False, "windowName": ""},
                    {"id": "bash:bash", "type": "bash",    "name": "Bash",
                     "protected": False, "multiple": True,
                     "windowName": "palmux:bash:bash"},
                ],
            },
            "runtime": {"kind": runtime_kind, "state": "ready",
                        "address": "10.146.187.15"},
        }],
    }


def _browser_state_payload(state="running", available=True) -> dict:
    return {"state": state, "cdpReachable": (state == "running"), "available": available}


def _common_mocks(page, *, runtime_kind="incus-container", browser_state="running") -> None:
    page.route("**/api/runtimes", lambda r: _fulfill(r, {
        "kinds": [
            {"kind": "host", "available": True},
            {"kind": "incus-container", "available": True},
        ],
    }))
    fake_repo = _fake_repo(runtime_kind=runtime_kind)
    page.route(f"**/api/repos/{FAKE_REPO}", lambda r: _fulfill(r, fake_repo))
    page.route("**/api/repos", lambda r: _fulfill(r, [fake_repo]))

    def _browser_catch_all(r):
        req = r.request
        if req.method == "POST" and req.url.endswith("/start"):
            _fulfill(r, {"state": "running"})
        elif req.method == "POST" and req.url.endswith("/stop"):
            _fulfill(r, {"state": "stopped"})
        else:
            # noVNC WS /attach and any other GET — abort (no real VNC).
            r.abort()

    page.route(
        f"**/api/repos/{FAKE_REPO}/branches/{FAKE_BRANCH}/browser/**",
        _browser_catch_all,
    )
    page.route(
        f"**/api/repos/{FAKE_REPO}/branches/{FAKE_BRANCH}/browser/state",
        lambda r: _fulfill(r, _browser_state_payload(state=browser_state)),
    )


# Install the test tap BEFORE any page script runs so the very first key is seen.
# Also pre-dismiss the first-run onboarding wizard (its overlay intercepts clicks).
_TAP_INIT = """
try { localStorage.setItem('palmux:onboarding-seen', '1'); } catch (e) {}
window.__vncKeys = [];
window.__palmuxVncKeyTap = function (keysym, code, down) {
  window.__vncKeys.push({ keysym: keysym, code: code, down: down });
};
"""


def _goto_browser_running(page) -> None:
    page.add_init_script(_TAP_INIT)
    page.goto(
        f"{BASE_URL}/{FAKE_REPO}/{FAKE_BRANCH}/browser",
        timeout=PLAYWRIGHT_TIMEOUT, wait_until="load",
    )
    # Wait for running state → viewport + keycapture mount.
    page.wait_for_selector("[data-testid='browser-keycapture']",
                           timeout=PLAYWRIGHT_TIMEOUT)
    page.wait_for_selector("[data-testid='browser-viewport']",
                           timeout=PLAYWRIGHT_TIMEOUT)


def _dispatch_keydown(page, *, key, code, keyCode=0, isComposing=False):
    """Dispatch a real KeyboardEvent on the hidden capture input."""
    page.evaluate(
        """(args) => {
            const el = document.querySelector("[data-testid='browser-keycapture']");
            const ev = new KeyboardEvent('keydown', {
                key: args.key, code: args.code, keyCode: args.keyCode,
                isComposing: args.isComposing, bubbles: true, cancelable: true,
            });
            el.dispatchEvent(ev);
        }""",
        {"key": key, "code": code, "keyCode": keyCode, "isComposing": isComposing},
    )


def _dispatch_keyup(page, *, key, code):
    page.evaluate(
        """(args) => {
            const el = document.querySelector("[data-testid='browser-keycapture']");
            const ev = new KeyboardEvent('keyup', {
                key: args.key, code: args.code, bubbles: true, cancelable: true,
            });
            el.dispatchEvent(ev);
        }""",
        {"key": key, "code": code},
    )


def _keys(page) -> list:
    return page.evaluate("() => window.__vncKeys || []")


def _clear_keys(page) -> None:
    page.evaluate("() => { window.__vncKeys = []; }")


# ─── Test cases ───────────────────────────────────────────────────────────────

def test_ac1_ascii_forwarded(page) -> None:
    """[AC-S8fe0cb-1-1] ASCII keydown on the hidden input forwards via sendKey."""
    name = "AC-S8fe0cb-1-1/ascii"
    _common_mocks(page)
    _goto_browser_running(page)
    _clear_keys(page)

    cases = [
        ("a", "KeyA", 0x061),       # XK_a
        ("1", "Digit1", 0x031),     # XK_1
        ("Enter", "Enter", 0xff0d), # XK_Return
        ("Backspace", "Backspace", 0xff08),
        (" ", "Space", 0x020),
        ("-", "Minus", 0x02d),
    ]
    for key, code, _expect in cases:
        _dispatch_keydown(page, key=key, code=code)
        _dispatch_keyup(page, key=key, code=code)

    keys = _keys(page)
    downs = {(k["code"], k["keysym"]) for k in keys if k["down"]}
    missing = []
    for key, code, expect in cases:
        if not any(c == code and ks == expect for (c, ks) in downs):
            missing.append((code, hex(expect)))
    if missing:
        fail(name, f"missing/incorrect keysyms for {missing}; got downs={sorted(downs)}")
        return
    # Each down must have a matching up.
    ups = [(k["code"]) for k in keys if not k["down"]]
    for _key, code, _e in cases:
        if code not in ups:
            fail(name, f"no keyup forwarded for {code}")
            return
    ok(name, f"forwarded {len(cases)} ASCII keys with correct keysyms + releases")


def test_ac1_local_ime_on_still_forwards(page) -> None:
    """[AC-S8fe0cb-1-1] keyCode 229 / isComposing event that still carries a real
    e.code (simulating local OS IME ON) still forwards the raw key."""
    name = "AC-S8fe0cb-1-1/ime-on"
    _common_mocks(page)
    _goto_browser_running(page)
    _clear_keys(page)

    # Simulate the OS-IME-ON case: a real code is present even though keyCode=229.
    _dispatch_keydown(page, key="a", code="KeyA", keyCode=229, isComposing=True)
    _dispatch_keyup(page, key="a", code="KeyA")

    keys = _keys(page)
    down = [k for k in keys if k["down"] and k["code"] == "KeyA"]
    if not down:
        fail(name, f"keyCode 229 + real e.code was dropped (no forward); got={keys}")
        return
    if down[0]["keysym"] != 0x061:
        fail(name, f"keysym for 'a' under IME-on = {down[0]['keysym']}, want 0x61")
        return
    ok(name, "local IME ON (keyCode 229 + real e.code) still forwards raw 'a'")


def test_ac1_input_stays_empty(page) -> None:
    """[AC-S8fe0cb-1-1] preventDefault keeps the hidden password input empty."""
    name = "AC-S8fe0cb-1-1/empty-input"
    _common_mocks(page)
    _goto_browser_running(page)

    page.locator("[data-testid='browser-keycapture']").focus()
    # Type via real keyboard (goes through the focused input).
    page.keyboard.type("hello")
    val = page.eval_on_selector(
        "[data-testid='browser-keycapture']", "el => el.value")
    if val:
        fail(name, f"hidden input accumulated text: {val!r} (preventDefault failed)")
        return
    ok(name, "hidden input stays empty after typing (preventDefault works)")


def test_ac2_modifiers_and_special(page) -> None:
    """[AC-S8fe0cb-1-2] modifiers + special keys forward with correct keysyms."""
    name = "AC-S8fe0cb-1-2/modifiers-special"
    _common_mocks(page)
    _goto_browser_running(page)
    _clear_keys(page)

    cases = [
        ("Control", "ControlLeft", 0xffe3),
        ("Shift", "ShiftLeft", 0xffe1),
        ("Alt", "AltLeft", 0xffe9),
        ("ArrowUp", "ArrowUp", 0xff52),
        ("Escape", "Escape", 0xff1b),
        ("Tab", "Tab", 0xff09),
        ("Delete", "Delete", 0xffff),
        ("F1", "F1", 0xffbe),
    ]
    for key, code, _e in cases:
        _dispatch_keydown(page, key=key, code=code)
        _dispatch_keyup(page, key=key, code=code)

    keys = _keys(page)
    downs = {(k["code"], k["keysym"]) for k in keys if k["down"]}
    bad = []
    for key, code, expect in cases:
        if not any(c == code and ks == expect for (c, ks) in downs):
            bad.append((code, hex(expect)))
    if bad:
        fail(name, f"missing/incorrect modifier/special keysyms {bad}; got={sorted(downs)}")
        return
    ok(name, f"forwarded {len(cases)} modifier/special keys correctly")


def test_ac2_key_repeat_same_keysym(page) -> None:
    """[AC-S8fe0cb-1-2] held key (repeated keydown w/o keyup) re-sends same keysym."""
    name = "AC-S8fe0cb-1-2/key-repeat"
    _common_mocks(page)
    _goto_browser_running(page)
    _clear_keys(page)

    for _ in range(3):
        _dispatch_keydown(page, key="a", code="KeyA")
    keys = _keys(page)
    downs = [k for k in keys if k["down"] and k["code"] == "KeyA"]
    if len(downs) < 3:
        fail(name, f"key-repeat produced {len(downs)} downs, want >=3")
        return
    if len({d["keysym"] for d in downs}) != 1:
        fail(name, f"key-repeat keysyms drifted: {[d['keysym'] for d in downs]}")
        return
    ok(name, f"key-repeat sent {len(downs)} consistent downs (keysym stable)")


def test_ac2_blur_releases_held(page) -> None:
    """[AC-S8fe0cb-1-2] blur while a key is held releases it (no stuck key)."""
    name = "AC-S8fe0cb-1-2/blur-release"
    _common_mocks(page)
    _goto_browser_running(page)
    _clear_keys(page)

    # Press and hold Shift (down, no up), then blur the input.
    _dispatch_keydown(page, key="Shift", code="ShiftLeft")
    page.evaluate(
        "() => document.querySelector(\"[data-testid='browser-keycapture']\").blur()")
    keys = _keys(page)
    ups = [k for k in keys if (not k["down"]) and k["code"] == "ShiftLeft"]
    if not ups:
        fail(name, f"Shift was not released on blur (stuck key); got={keys}")
        return
    ok(name, "held Shift released on blur (no stuck modifier)")


def test_ac2_canvas_mousedown_refocuses_input(page) -> None:
    """[AC-S8fe0cb-1-2] mouse-down on the canvas re-routes focus to hidden input."""
    name = "AC-S8fe0cb-1-2/canvas-refocus"
    _common_mocks(page)
    _goto_browser_running(page)

    # Blur first so we can observe the refocus.
    page.evaluate(
        "() => document.querySelector(\"[data-testid='browser-keycapture']\").blur()")
    # Mouse-down on the viewport.
    page.locator("[data-testid='browser-viewport']").dispatch_event("mousedown")
    # refocus is deferred via setTimeout(0); poll for focus.
    focused = False
    for _ in range(20):
        focused = page.evaluate(
            "() => document.activeElement === "
            "document.querySelector(\"[data-testid='browser-keycapture']\")")
        if focused:
            break
        page.wait_for_timeout(25)
    if not focused:
        fail(name, "canvas mousedown did not refocus the hidden capture input")
        return
    ok(name, "canvas mousedown re-focuses hidden input (keys keep flowing)")


def test_ac2_canvas_click_keeps_held_modifier(page) -> None:
    """[AC-S8fe0cb-1-2] mouse-down on the canvas while a modifier is HELD must NOT
    release it (the canvas-refocus round-trip keeps focus, so no stuck-down loss).
    Regression guard for the review finding that blur→releaseAll dropped modifiers
    on every canvas click."""
    name = "AC-S8fe0cb-1-2/canvas-click-keeps-held"
    _common_mocks(page)
    _goto_browser_running(page)
    _clear_keys(page)

    # Press and hold Shift (down, no up).
    _dispatch_keydown(page, key="Shift", code="ShiftLeft")
    # Now mouse-down on the canvas (as if shift-click on the remote).
    page.locator("[data-testid='browser-viewport']").dispatch_event("mousedown")
    page.wait_for_timeout(50)
    keys = _keys(page)
    # No Shift key-UP should have been forwarded — the modifier is still held.
    shift_ups = [k for k in keys if (not k["down"]) and k["code"] == "ShiftLeft"]
    if shift_ups:
        fail(name, f"held Shift was released on canvas click (lost modifier); keys={keys}")
        return
    # And focus must remain on (or be restored to) the hidden input.
    focused = page.evaluate(
        "() => document.activeElement === "
        "document.querySelector(\"[data-testid='browser-keycapture']\")")
    if not focused:
        fail(name, "hidden input lost focus after canvas mousedown")
        return
    ok(name, "held Shift survives a canvas click (no spurious release)")


def test_ac2_unmount_releases_held(page) -> None:
    """[AC-S8fe0cb-1-2/1-3] leaving the Browser tab while a key is HELD releases it
    (release fires before RFB disconnect — no stuck key on the remote). Regression
    guard for the effect-cleanup ordering finding."""
    name = "AC-S8fe0cb-1-2/unmount-release"
    _common_mocks(page)
    _goto_browser_running(page)
    _clear_keys(page)

    # Hold Control, then navigate away via SPA tab-click (unmount LiveViewport
    # WITHOUT a full document reload, so the tap survives to observe the release).
    _dispatch_keydown(page, key="Control", code="ControlLeft")
    bash_tab = page.locator("[data-testid='tab-bash:bash']").first
    if bash_tab.count() < 1:
        fail(name, "bash tab not present for SPA navigation")
        return
    bash_tab.click()
    page.wait_for_timeout(250)
    keys = _keys(page)
    ctrl_ups = [k for k in keys if (not k["down"]) and k["code"] == "ControlLeft"]
    if not ctrl_ups:
        fail(name, f"held Control not released on tab-leave (stuck key); keys={keys}")
        return
    ok(name, "held Control released on unmount (release before disconnect)")


def test_ac3_leave_tab_removes_capture(page) -> None:
    """[AC-S8fe0cb-1-3] leaving the Browser tab removes the capture input;
    returning re-enables it."""
    name = "AC-S8fe0cb-1-3/leave-return"
    _common_mocks(page)
    _goto_browser_running(page)

    if page.locator("[data-testid='browser-keycapture']").count() < 1:
        fail(name, "capture input not present in browser tab")
        return

    # Navigate to the bash tab.
    page.goto(f"{BASE_URL}/{FAKE_REPO}/{FAKE_BRANCH}/bash:bash",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    page.wait_for_timeout(300)
    if page.locator("[data-testid='browser-keycapture']").count() != 0:
        fail(name, "capture input still in DOM after leaving Browser tab")
        return

    # Return to the Browser tab.
    page.goto(f"{BASE_URL}/{FAKE_REPO}/{FAKE_BRANCH}/browser",
              timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
    try:
        page.wait_for_selector("[data-testid='browser-keycapture']",
                               timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:  # noqa: BLE001
        fail(name, "capture input not re-mounted on returning to Browser tab")
        return
    ok(name, "capture removed on leave, re-enabled on return")


def test_ac4_password_manager_avoidance_attrs(page) -> None:
    """[AC-S8fe0cb-1-4] hidden input has password-manager / autofill avoidance
    attributes and is off-screen."""
    name = "AC-S8fe0cb-1-4/avoidance-attrs"
    _common_mocks(page)
    _goto_browser_running(page)

    el = page.locator("[data-testid='browser-keycapture']").first
    attrs = page.evaluate(
        """() => {
            const el = document.querySelector("[data-testid='browser-keycapture']");
            const r = el.getBoundingClientRect();
            const cs = getComputedStyle(el);
            return {
                type: el.getAttribute('type'),
                autocomplete: el.getAttribute('autocomplete'),
                ariaHidden: el.getAttribute('aria-hidden'),
                tabIndex: el.tabIndex,
                name: el.getAttribute('name'),
                onep: el.getAttribute('data-1p-ignore'),
                lp: el.getAttribute('data-lpignore'),
                formType: el.getAttribute('data-form-type'),
                opacity: cs.opacity,
                width: r.width, height: r.height,
            };
        }""")
    problems = []
    if attrs["type"] != "password":
        problems.append(f"type={attrs['type']!r} (want password)")
    if attrs["autocomplete"] != "off":
        problems.append(f"autocomplete={attrs['autocomplete']!r}")
    if attrs["ariaHidden"] != "true":
        problems.append(f"aria-hidden={attrs['ariaHidden']!r}")
    if attrs["tabIndex"] != -1:
        problems.append(f"tabIndex={attrs['tabIndex']!r}")
    if not attrs["name"] or attrs["name"] in ("password", "username", "email"):
        problems.append(f"name={attrs['name']!r} not obscured")
    if attrs["onep"] != "true":
        problems.append("missing data-1p-ignore")
    if attrs["lp"] != "true":
        problems.append("missing data-lpignore")
    if attrs["formType"] != "other":
        problems.append(f"data-form-type={attrs['formType']!r}")
    # off-screen: opacity 0 or ~zero rendered size.
    if attrs["opacity"] != "0" and (attrs["width"] > 4 or attrs["height"] > 4):
        problems.append(f"not visually hidden (opacity={attrs['opacity']}, "
                        f"size={attrs['width']}x{attrs['height']})")
    if problems:
        fail(name, "; ".join(problems))
        return
    _ = el  # presence asserted above
    ok(name, "hidden input has all password-manager/autofill avoidance attrs + off-screen")


# ─── Runner ───────────────────────────────────────────────────────────────────

def main() -> int:
    sync_playwright = get_playwright()
    tests = [
        test_ac1_ascii_forwarded,
        test_ac1_local_ime_on_still_forwards,
        test_ac1_input_stays_empty,
        test_ac2_modifiers_and_special,
        test_ac2_key_repeat_same_keysym,
        test_ac2_blur_releases_held,
        test_ac2_canvas_click_keeps_held_modifier,
        test_ac2_unmount_releases_held,
        test_ac2_canvas_mousedown_refocuses_input,
        test_ac3_leave_tab_removes_capture,
        test_ac4_password_manager_avoidance_attrs,
    ]
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            for tc in tests:
                ctx = browser.new_context(viewport={"width": 1280, "height": 800})
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
