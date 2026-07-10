#!/usr/bin/env python3
"""Sprint S4323c8-4 — tab bar restyled to a general VS Code-style tab UI.

Verifies:
  - Tabs render flat, without border-radius (no rounded pill look).
  - The active tab is visually distinguishable from inactive tabs via a
    different computed background-color (AC-1/AC-2).
  - Every pre-existing tab-bar affordance still works after the restyle
    (AC-3, no regressions): the "+" add-tab button, the AGENT mode badge,
    the unread-notification badge, drag-and-drop reorder (draggable
    attribute), and the right-click context menu's Close/Rename controls
    (which drives the rename-input testid).

Uses the hermetic palmux2 binary + a throwaway `_fixture` repo (multiple
tabs — claude/files/git/bash — are present by default, no extra setup
needed).

Acceptance criteria covered:
  [AC-S4323c8-4-1] flat, no-border-radius VS Code-style tabs
  [AC-S4323c8-4-2] active vs inactive tabs are visually distinguishable
  [AC-S4323c8-4-3] all pre-existing tab-bar features/testids preserved

Exit code 0 = ALL PASS. Run standalone:
  python3 tests/e2e/s4323c8_tab_ui.py
"""
from __future__ import annotations

import json
import os
import re
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

sys.path.insert(0, os.path.dirname(__file__))
REPO = Path(__file__).resolve().parents[2]
BIN = REPO / "bin" / "palmux"
TO = 20_000


def free_port() -> int:
    with socket.socket() as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def http_json(port: int, method: str, path: str, body: dict | None = None):
    url = f"http://localhost:{port}{path}"
    raw = json.dumps(body).encode() if body is not None else None
    headers = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, method=method, data=raw, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            code, data = resp.status, resp.read()
    except urllib.error.HTTPError as e:
        code, data = e.code, e.read()
    try:
        return code, json.loads(data.decode() or "{}")
    except json.JSONDecodeError:
        return code, data.decode(errors="replace")


def main() -> None:
    print("s4323c8_tab_ui — tab bar restyled to a general VS Code-style tab UI")
    if not BIN.is_file():
        print("SKIP: no prebuilt binary (make build)")
        sys.exit(0)
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("SKIP: playwright not installed")
        sys.exit(0)

    port = free_port()
    os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
    cfg = Path(f"/tmp/palmux2-tabui-{port}")
    cfg.mkdir(parents=True, exist_ok=True)
    proc = subprocess.Popen(
        [str(BIN), "--addr", f"127.0.0.1:{port}", "--config-dir", str(cfg),
         "--claude-bin", "/bin/cat", "--tmux-prefix", f"_pmx_tabui{port}_"],
        cwd=REPO, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True)
    dl = time.time() + 30
    while time.time() < dl:
        line = proc.stdout.readline() if proc.stdout else ""
        if f":{port}" in line or "listening" in line:
            break
        if proc.poll() is not None:
            print("FAIL: server died")
            sys.exit(1)

    failed = 0
    try:
        import _fixture as fx
        with fx.palmux2_test_fixture("s4323c8-tabui") as fixture:
            repo_id = fixture.repo_id
            bid = fixture.primary_branch_id(timeout_s=10.0)
            claude_tab_id = fixture.open_claude_tab(bid, timeout_s=8.0)
            # The canonical bash tab is seeded a couple of seconds after
            # branch open by the periodic tmux sync (see _fixture docs) —
            # wait for it via REST before driving the browser so the tab
            # bar has >1 tab (claude + bash, plus the protected files/git
            # REST-view tabs) from the first paint.
            fixture.wait_for_tab(bid, "bash:bash", timeout_s=10.0)

            # Set claude_mode=agent so the AGENT mode badge renders
            # (AC-3: mode badge must survive the restyle).
            code, _ = http_json(
                port, "PATCH",
                f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(bid)}"
                f"/tabs/{urllib.parse.quote(claude_tab_id)}/settings",
                {"claude_mode": "agent"},
            )
            if code != 200:
                print(f"FAIL: PATCH claude_mode=agent returned {code}")
                failed += 1

            # Inject one unread notification on the claude tab so the
            # unread-count badge renders (AC-3: badge must survive).
            code, _ = http_json(
                port, "POST", "/api/notify",
                {"repoId": repo_id, "branchId": bid, "type": "urgent",
                 "requestId": "s4323c8-badge-1", "tabId": claude_tab_id,
                 "message": "test notification"},
            )
            if code not in (200, 202):
                print(f"FAIL: POST /api/notify returned {code}")
                failed += 1

            # Load the *bash* tab as the active view, not the Claude tab:
            # main-area.tsx auto-clears a branch's unread notifications the
            # instant a Claude tab becomes the active tab ("visiting the
            # agent counts as 'I read it'" — see clearBranchNotifications
            # wiring). Landing on the bash tab keeps the injected
            # notification's unreadCount intact so the Claude tab (now the
            # *inactive* one) still shows its badges, which is exactly what
            # we want to assert against for AC-3.
            url = (f"http://localhost:{port}/{urllib.parse.quote(repo_id, safe='')}"
                   f"/{urllib.parse.quote(bid, safe='')}/{urllib.parse.quote('bash:bash', safe='')}")
            with sync_playwright() as p:
                b = p.chromium.launch(headless=True)
                ctx = b.new_context(viewport={"width": 1280, "height": 800})
                ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                pg = ctx.new_page()
                pg.goto(url, wait_until="load", timeout=TO)
                pg.wait_for_function("document.getElementById('root').innerHTML.length > 100", timeout=TO)
                pg.wait_for_selector("[data-testid='claude-tab']", timeout=TO)
                pg.wait_for_selector("[role='tab'][data-tab-type='bash']", timeout=TO)

                claude_tab = pg.locator("[data-testid='claude-tab']")
                bash_tab = pg.locator("[role='tab'][data-tab-type='bash']").first

                # [AC-S4323c8-4-1] flat, no-border-radius VS Code-style tabs.
                radius = claude_tab.evaluate("el => getComputedStyle(el).borderRadius")
                if radius not in ("0px", "0px 0px 0px 0px", ""):
                    print(f"FAIL: tab has border-radius {radius!r} (expected flat/no-radius)")
                    failed += 1
                else:
                    print(f"PASS: tabs are flat, no border-radius (got {radius!r})")

                if bash_tab.get_attribute("aria-selected") != "true":
                    print("FAIL: bash tab (current route) is not aria-selected=true")
                    failed += 1
                if claude_tab.get_attribute("aria-selected") == "true":
                    print("FAIL: claude tab is unexpectedly aria-selected=true")
                    failed += 1

                # [AC-S4323c8-4-2] active vs inactive tabs are visually
                # distinguishable via computed background-color.
                active_bg = bash_tab.evaluate("el => getComputedStyle(el).backgroundColor")
                inactive_bg = claude_tab.evaluate("el => getComputedStyle(el).backgroundColor")
                if active_bg == inactive_bg:
                    print(f"FAIL: active tab bg ({active_bg}) == inactive tab bg ({inactive_bg})")
                    failed += 1
                else:
                    print(f"PASS: active tab bg ({active_bg}) != inactive tab bg ({inactive_bg})")

                # [AC-S4323c8-4-3] regression checks — every pre-existing
                # tab-bar affordance is still present/wired after the restyle.
                if pg.locator("[role='tablist']").count() < 1:
                    print("FAIL: [role='tablist'] missing")
                    failed += 1
                else:
                    print("PASS: role=tablist preserved")

                if pg.locator("[data-testid='tab-add-bash']").count() < 1:
                    print("FAIL: [data-testid='tab-add-bash'] (+) missing")
                    failed += 1
                else:
                    print("PASS: + add-tab button preserved")

                badge = pg.locator("[data-testid='claude-mode-badge']")
                if badge.count() < 1 or "agent" not in (badge.inner_text() or "").lower():
                    print("FAIL: claude-mode-badge (AGENT) not rendered")
                    failed += 1
                else:
                    print("PASS: AGENT mode badge preserved")

                # The unread badge only shows on a Claude tab that is *not*
                # currently active (main-area.tsx auto-clears a branch's
                # unread notifications the instant a Claude tab becomes the
                # active view) — which is exactly the state here since the
                # bash tab is the active route. The badge itself lands a
                # beat after first paint (bootstrap fetches notifications
                # in parallel with everything else), so poll instead of
                # asserting on the very first render.
                try:
                    pg.wait_for_function(
                        "document.querySelector(\"[data-testid='claude-tab']\").textContent.includes('1')",
                        timeout=TO,
                    )
                    print("PASS: unread notification badge preserved")
                except Exception:
                    claude_tab_text = claude_tab.inner_text() or ""
                    print(f"FAIL: unread notification badge ('1') not found in claude tab text {claude_tab_text!r}")
                    failed += 1

                if bash_tab.get_attribute("draggable") != "true":
                    print("FAIL: bash tab lost draggable=true (DnD reorder affordance)")
                    failed += 1
                else:
                    print("PASS: draggable attribute preserved (DnD reorder)")

                # Right-click context menu → Close control still wired.
                bash_tab.click(button="right")
                pg.wait_for_timeout(200)
                close_item = pg.locator("[role='menuitem']", has_text=re.compile("Close tab", re.I))
                if close_item.count() < 1:
                    print("FAIL: context menu 'Close tab' item missing")
                    failed += 1
                else:
                    print("PASS: close-tab context menu item preserved")
                pg.keyboard.press("Escape")
                pg.wait_for_timeout(150)

                # Right-click context menu → Rename control still wired,
                # and drives the tab-rename-input testid.
                bash_tab.click(button="right")
                pg.wait_for_timeout(200)
                rename_item = pg.locator("[role='menuitem']", has_text=re.compile("^Rename", re.I))
                if rename_item.count() < 1:
                    print("FAIL: context menu 'Rename…' item missing")
                    failed += 1
                else:
                    rename_item.click()
                    try:
                        pg.wait_for_selector("[data-testid='tab-rename-input']", timeout=TO)
                        print("PASS: rename affordance preserved (rename input appears)")
                    except Exception:
                        print("FAIL: tab-rename-input did not appear after Rename click")
                        failed += 1
                    pg.keyboard.press("Escape")

                b.close()
    finally:
        if proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=8)
            except subprocess.TimeoutExpired:
                proc.kill()
        import shutil
        shutil.rmtree(cfg, ignore_errors=True)

    print(f"\ns4323c8_tab_ui: {'ALL PASS' if failed == 0 else 'FAILED'}")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
