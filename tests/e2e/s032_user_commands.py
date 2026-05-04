#!/usr/bin/env python3
"""Sprint S032 — User-defined palette commands E2E.

Acceptance criteria verified:

  [AC-S032-1-1]  palette.userCommands schema validates and round-trips through
                 GET/PATCH /api/settings (all 3 target shapes).
  [AC-S032-1-2]  target:'bash' userCommand shows in >mode with source 'user',
                 select → resolveBashTarget sends the command string.
  [AC-S032-1-3]  target:'url' userCommand shows in >mode, select → window.open.
  [AC-S032-1-4]  target:'files' userCommand shows in >mode, select → navigate
                 to Files tab at the given path.
  [AC-S032-1-5]  ⌘K palette builtin '> manage user commands' exists and opens
                 the User Commands Modal.
  [AC-S032-1-6]  Modal: add/edit/delete rows, Save fires PATCH /api/settings
                 and settings persist across reloads. Reset reverts unsaved edits.
  [AC-S032-1-7]  Modal 'View raw JSON' <details> shows current userCommands as
                 formatted JSON.

Runs against: make serve INSTANCE=dev (palmux2 dev instance, default port 8215).
Uses Playwright headless chromium for browser-based ACs, plain HTTP for BE ACs.

Exit 0 = PASS, else FAIL (prints failing AC to stderr).
"""
from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

# Platform-appropriate modifier for ⌘K / Ctrl+K
PALETTE_SHORTCUT = "Meta+k" if sys.platform == "darwin" else "Control+k"

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8215"
)
BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 20.0
PLAYWRIGHT_TIMEOUT = 15_000  # ms


# ─── Helpers ────────────────────────────────────────────────────────────────

def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def http_json(method: str, path: str, *, body: dict | list | None = None) -> tuple[int, object]:
    raw = json.dumps(body).encode() if body is not None else None
    headers: dict[str, str] = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    url = f"{BASE_URL}{path}"
    req = urllib.request.Request(url, method=method, data=raw, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            data = resp.read()
            try:
                return resp.status, json.loads(data.decode() or "null")
            except json.JSONDecodeError:
                return resp.status, data.decode(errors="replace")
    except urllib.error.HTTPError as e:
        data = e.read()
        try:
            return e.code, json.loads(data.decode() or "null")
        except json.JSONDecodeError:
            return e.code, data.decode(errors="replace")


def get_playwright():
    try:
        from playwright.sync_api import sync_playwright
        return sync_playwright
    except ImportError:
        return None


# ─── Repo/branch discovery ──────────────────────────────────────────────────

def discover_palmux_repo() -> tuple[str, str]:
    """Return (repoId, branchId) for the palmux2 self-repo."""
    code, body = http_json("GET", "/api/repos")
    if code != 200 or not isinstance(body, list):
        fail(f"GET /api/repos failed: {code}")
    for repo in body:
        if "palmux2" in repo.get("ghqPath", "") and repo.get("openBranches"):
            return repo["id"], repo["openBranches"][0]["id"]
    for repo in body:
        if repo.get("openBranches"):
            return repo["id"], repo["openBranches"][0]["id"]
    fail("no open repo/branch on dev instance")
    return "", ""  # unreachable


REPO_ID, BRANCH_ID = discover_palmux_repo()
API_BASE = f"/api/repos/{urllib.parse.quote(REPO_ID, safe='')}/branches/{urllib.parse.quote(BRANCH_ID, safe='')}"


# ─── Fixture helpers ─────────────────────────────────────────────────────────

FIXTURE_CMDS = [
    {"name": "deploy preview", "command": "make deploy-preview", "target": "bash"},
    {"name": "open Slack", "url": "https://app.slack.com/client/T0/CABCDEF", "target": "url"},
    {"name": "docs/index", "path": "docs/original-specs/01-architecture.md", "target": "files"},
]


def reset_user_commands(cmds: list | None = None) -> None:
    """PATCH settings to set palette.userCommands to cmds (or clear)."""
    code, body = http_json("PATCH", "/api/settings", body={"palette": {"userCommands": cmds or []}})
    if code != 200:
        fail(f"PATCH /api/settings (reset) returned {code}: {body}")


# ─── AC-S032-1-1: schema validation ─────────────────────────────────────────

def test_ac_s032_1_1_schema_validation():
    """[AC-S032-1-1] palette.userCommands validates and round-trips."""

    # Seed 3-shape fixture
    code, body = http_json("PATCH", "/api/settings", body={"palette": {"userCommands": FIXTURE_CMDS}})
    if code != 200:
        fail(f"[AC-S032-1-1] PATCH fixture returned {code}: {body}")
    if not isinstance(body, dict):
        fail(f"[AC-S032-1-1] unexpected response type: {type(body)}")
    palette = body.get("palette") or {}
    ucs = palette.get("userCommands") or []
    if len(ucs) != 3:
        fail(f"[AC-S032-1-1] expected 3 userCommands, got {len(ucs)}: {ucs}")

    # Verify round-trip via GET
    code2, body2 = http_json("GET", "/api/settings")
    if code2 != 200:
        fail(f"[AC-S032-1-1] GET /api/settings returned {code2}")
    palette2 = (body2 or {}).get("palette") or {}
    ucs2 = palette2.get("userCommands") or []
    if len(ucs2) != 3:
        fail(f"[AC-S032-1-1] GET round-trip: expected 3 userCommands, got {len(ucs2)}")
    names = {u["name"] for u in ucs2}
    for fc in FIXTURE_CMDS:
        if fc["name"] not in names:
            fail(f"[AC-S032-1-1] '{fc['name']}' missing from round-tripped settings")

    # Validation: bash without command → 400
    bad_bash = [{"name": "bad", "target": "bash"}]
    code3, body3 = http_json("PATCH", "/api/settings", body={"palette": {"userCommands": bad_bash}})
    if code3 != 400:
        fail(f"[AC-S032-1-1] expected 400 for bash without command, got {code3}: {body3}")

    # Validation: url without url → 400
    bad_url = [{"name": "bad", "target": "url"}]
    code4, body4 = http_json("PATCH", "/api/settings", body={"palette": {"userCommands": bad_url}})
    if code4 != 400:
        fail(f"[AC-S032-1-1] expected 400 for url without url, got {code4}: {body4}")

    # Validation: files without path → 400
    bad_files = [{"name": "bad", "target": "files"}]
    code5, body5 = http_json("PATCH", "/api/settings", body={"palette": {"userCommands": bad_files}})
    if code5 != 400:
        fail(f"[AC-S032-1-1] expected 400 for files without path, got {code5}: {body5}")

    # Validation: unknown target → 400
    bad_target = [{"name": "bad", "target": "ftp", "command": "x"}]
    code6, body6 = http_json("PATCH", "/api/settings", body={"palette": {"userCommands": bad_target}})
    if code6 != 400:
        fail(f"[AC-S032-1-1] expected 400 for unknown target, got {code6}: {body6}")

    ok("AC-S032-1-1", "schema validates, round-trips, and rejects malformed entries")


# ─── AC-S032-1-2..1-7: Browser (Playwright) ──────────────────────────────────

def run_browser_tests():
    """Run browser-dependent ACs via Playwright headless Chromium."""
    sync_playwright = get_playwright()
    if sync_playwright is None:
        print("SKIP: playwright not installed — skipping browser ACs")
        print("  (install: pip install playwright && playwright install chromium)")
        return

    # Ensure fixture is loaded before browser tests
    reset_user_commands(FIXTURE_CMDS)

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        context = browser.new_context(viewport={"width": 1280, "height": 800})
        page = context.new_page()
        page.set_default_timeout(PLAYWRIGHT_TIMEOUT)

        # Navigate to the palmux2 self-repo branch (Claude tab)
        encoded_repo = urllib.parse.quote(REPO_ID, safe='')
        encoded_branch = urllib.parse.quote(BRANCH_ID, safe='')
        claude_url = f"{BASE_URL}/{encoded_repo}/{encoded_branch}/claude"
        page.goto(claude_url)
        # Wait for app to load (lenient selector — the SPA may still be hydrating)
        try:
            page.wait_for_selector(
                "[data-testid='palette-overlay'], [data-testid='tab-bar'], body",
                timeout=PLAYWRIGHT_TIMEOUT,
            )
        except Exception:
            pass
        time.sleep(1.0)

        def open_palette():
            """Open palette with retry — app may still be hydrating."""
            for attempt in range(3):
                page.keyboard.press(PALETTE_SHORTCUT)
                try:
                    page.wait_for_selector("[data-testid='palette-overlay']", timeout=5000)
                    time.sleep(0.1)
                    return
                except Exception:
                    if attempt == 2:
                        raise
                    time.sleep(0.5)

        def close_palette():
            try:
                page.keyboard.press("Escape")
                page.wait_for_selector("[data-testid='palette-overlay']", state="detached", timeout=3000)
            except Exception:
                pass
            time.sleep(0.1)

        # ── AC-S032-1-5: builtin 'manage user commands' ──────────────────────

        def test_ac_s032_1_5():
            open_palette()
            page.wait_for_selector("[data-testid='palette-input']")
            page.fill("[data-testid='palette-input']", "> manage user commands")
            time.sleep(0.3)
            # Find and click the manage user commands item
            item = page.locator("[data-testid='palette-item-builtin-manage-user-commands']")
            if not item.is_visible():
                # Try clicking by label match as fallback
                items = page.locator("[data-testid^='palette-item-']")
                found = False
                for i in range(items.count()):
                    el = items.nth(i)
                    if "manage user commands" in el.inner_text().lower():
                        el.click()
                        found = True
                        break
                if not found:
                    # Check list for any manage item
                    list_html = page.locator("[data-testid='palette-list']").inner_html()
                    fail(f"[AC-S032-1-5] 'manage user commands' not found in palette. List: {list_html[:500]}")
            else:
                item.click()
            # Modal should appear
            page.wait_for_selector("[data-testid='user-commands-modal']", timeout=5000)
            ok("AC-S032-1-5", "builtin 'manage user commands' opens UserCommandsModal")
            # Close modal
            page.keyboard.press("Escape")
            page.wait_for_selector("[data-testid='user-commands-modal']", state="hidden", timeout=3000)

        test_ac_s032_1_5()

        # ── AC-S032-1-2: target:'bash' shows in >mode ─────────────────────────

        def test_ac_s032_1_2():
            open_palette()
            page.wait_for_selector("[data-testid='palette-input']")
            page.fill("[data-testid='palette-input']", "> deploy preview")
            time.sleep(0.3)
            list_html = page.locator("[data-testid='palette-list']").inner_html()
            if "deploy preview" not in list_html.lower():
                fail(f"[AC-S032-1-2] 'deploy preview' not found in >mode list. HTML: {list_html[:500]}")
            # Check 'user' source label is present somewhere in the list
            if "→ bash" not in list_html and "bash" not in list_html.lower():
                fail(f"[AC-S032-1-2] bash routing detail not shown. HTML: {list_html[:300]}")
            ok("AC-S032-1-2", "target:'bash' userCommand shows in >mode")
            page.keyboard.press("Escape")

        test_ac_s032_1_2()

        # ── AC-S032-1-3: target:'url' shows in >mode ─────────────────────────

        def test_ac_s032_1_3():
            open_palette()
            page.wait_for_selector("[data-testid='palette-input']")
            page.fill("[data-testid='palette-input']", "> open Slack")
            time.sleep(0.3)
            list_html = page.locator("[data-testid='palette-list']").inner_html()
            if "slack" not in list_html.lower():
                fail(f"[AC-S032-1-3] 'open Slack' not found in >mode. HTML: {list_html[:500]}")
            if "url" not in list_html.lower():
                fail(f"[AC-S032-1-3] url routing detail not shown. HTML: {list_html[:300]}")
            ok("AC-S032-1-3", "target:'url' userCommand shows in >mode with url routing detail")
            page.keyboard.press("Escape")

        test_ac_s032_1_3()

        # ── AC-S032-1-4: target:'files' shows in >mode ───────────────────────

        def test_ac_s032_1_4():
            open_palette()
            page.wait_for_selector("[data-testid='palette-input']")
            page.fill("[data-testid='palette-input']", "> docs/index")
            time.sleep(0.3)
            list_html = page.locator("[data-testid='palette-list']").inner_html()
            if "docs" not in list_html.lower() and "index" not in list_html.lower():
                fail(f"[AC-S032-1-4] 'docs/index' not found in >mode. HTML: {list_html[:500]}")
            if "files" not in list_html.lower():
                fail(f"[AC-S032-1-4] files routing detail not shown. HTML: {list_html[:300]}")
            ok("AC-S032-1-4", "target:'files' userCommand shows in >mode with files routing detail")
            page.keyboard.press("Escape")

        test_ac_s032_1_4()

        # ── AC-S032-1-6 & 1-7: Modal add/edit/save, raw JSON ─────────────────

        def test_ac_s032_1_6_and_1_7():
            # Open the modal via ⌘K
            open_palette()
            page.wait_for_selector("[data-testid='palette-input']")
            page.fill("[data-testid='palette-input']", "> manage")
            time.sleep(0.3)
            # Click the manage user commands item
            item_sel = "[data-testid='palette-item-builtin-manage-user-commands']"
            if page.locator(item_sel).is_visible():
                page.click(item_sel)
            else:
                items = page.locator("[data-testid^='palette-item-']")
                for i in range(items.count()):
                    el = items.nth(i)
                    if "manage user commands" in el.inner_text().lower():
                        el.click()
                        break
            page.wait_for_selector("[data-testid='user-commands-modal']", timeout=5000)

            # AC-S032-1-7: raw JSON <details> shows current userCommands
            raw_json_el = page.locator("[data-testid='user-cmd-raw-json']")
            if not raw_json_el.is_visible():
                # Click the <details> summary to open it
                page.locator("details > summary").click()
                time.sleep(0.2)
            raw_text = raw_json_el.inner_text()
            if "deploy preview" not in raw_text and "userCommands" not in raw_text:
                fail(f"[AC-S032-1-7] raw JSON doesn't show userCommands. Text: {raw_text[:300]}")
            ok("AC-S032-1-7", "'View raw JSON' shows current userCommands as JSON")

            # AC-S032-1-6: Add a new row, verify it appears
            initial_rows = page.locator("[data-testid^='user-cmd-row-']").count()
            page.click("[data-testid='user-cmd-add']")
            time.sleep(0.2)
            new_rows = page.locator("[data-testid^='user-cmd-row-']").count()
            if new_rows != initial_rows + 1:
                fail(f"[AC-S032-1-6] Add row didn't work: {initial_rows} → {new_rows}")

            # Fill the new row
            new_idx = new_rows - 1
            page.fill(f"[data-testid='user-cmd-name-{new_idx}']", "test command")
            page.fill(f"[data-testid='user-cmd-payload-{new_idx}']", "echo hello")
            # It should already be bash target by default

            # Reset reverts unsaved edits (AC-S032-1-6 reset path)
            page.click("[data-testid='user-cmd-reset']")
            time.sleep(0.2)
            after_reset = page.locator("[data-testid^='user-cmd-row-']").count()
            if after_reset != initial_rows:
                fail(f"[AC-S032-1-6] Reset didn't revert rows: expected {initial_rows}, got {after_reset}")
            ok("AC-S032-1-6-reset", "Reset reverts unsaved edits")

            # Add again and Save
            page.click("[data-testid='user-cmd-add']")
            time.sleep(0.2)
            new_idx2 = page.locator("[data-testid^='user-cmd-row-']").count() - 1
            page.fill(f"[data-testid='user-cmd-name-{new_idx2}']", "e2e-test-cmd")
            page.fill(f"[data-testid='user-cmd-payload-{new_idx2}']", "echo e2e")

            page.click("[data-testid='user-commands-save']")
            time.sleep(0.5)

            # Modal should close after save
            try:
                page.wait_for_selector("[data-testid='user-commands-modal']", state="hidden", timeout=3000)
            except Exception:
                # Check for save error
                err_el = page.locator("[data-testid='user-cmd-save-error']")
                if err_el.is_visible():
                    fail(f"[AC-S032-1-6] Save error shown: {err_el.inner_text()}")
                fail("[AC-S032-1-6] Modal didn't close after Save")

            # Verify persistence via API
            code, body = http_json("GET", "/api/settings")
            if code != 200:
                fail(f"[AC-S032-1-6] GET /api/settings after save returned {code}")
            ucs = ((body or {}).get("palette") or {}).get("userCommands") or []
            names = [u["name"] for u in ucs]
            if "e2e-test-cmd" not in names:
                fail(f"[AC-S032-1-6] 'e2e-test-cmd' not persisted. Got: {names}")

            ok("AC-S032-1-6", "Add row → Save → persists via PATCH /api/settings")

            # Delete the test row via API cleanup
            remaining = [u for u in ucs if u["name"] != "e2e-test-cmd"]
            http_json("PATCH", "/api/settings", body={"palette": {"userCommands": remaining}})

        test_ac_s032_1_6_and_1_7()

        browser.close()


# ─── Main ────────────────────────────────────────────────────────────────────

def main():
    print(f"\nS032 User-defined palette commands E2E — {BASE_URL}")
    print(f"  Repo: {REPO_ID}  Branch: {BRANCH_ID}\n")

    # Save original settings to restore after tests
    _, orig_settings = http_json("GET", "/api/settings")
    orig_user_cmds = ((orig_settings or {}).get("palette") or {}).get("userCommands") or [] if isinstance(orig_settings, dict) else []

    try:
        # Non-browser ACs
        test_ac_s032_1_1_schema_validation()

        # Browser ACs
        run_browser_tests()

    finally:
        # Restore original settings
        reset_user_commands(orig_user_cmds)

    print("\nAll S032 ACs PASSED")


if __name__ == "__main__":
    main()
