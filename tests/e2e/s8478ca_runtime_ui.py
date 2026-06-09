#!/usr/bin/env python3
"""Sprint S8478ca Story 5 — Workspace Runtime selector + state badge (E2E).

Real-server E2E against a dev instance (make serve INSTANCE=dev, production
mode). Drives the actual UI as a user would and asserts UI state observed
through the real backend (runtime caps via GET /api/runtimes, runtime view on
the branch object pushed over WS /api/events).

The dynamic non-host runtime states (starting → ready → error) require a live
Incus bring-up and are exercised in the MOCK companion
(s8478ca_runtime_ui_mock.py); this file covers what a stock dev instance can
observe for real:
  - host runtime is always selectable; incus-container is greyed iff Incus is
    absent on the dev host (asserted against the real GET /api/runtimes)
  - a host-runtime Workspace shows a runtime chip/badge in state=ready
  - the S0c6a1b Host login scope (repoId=host--0000) is labelled distinctly
    from a runtime.kind=host chip (no runtime selector on the Host scope)

Acceptance criteria:
  [AC-S8478ca-5-1] runtime selector offers host / incus-container; incus-container
                   is disabled + install tooltip when Incus is unavailable
  [AC-S8478ca-5-2] header runtime chip + drawer state badge render and reflect
                   the branch runtime view (ready for host)
  [AC-S8478ca-5-3] runtime.kind=host vs Host login scope (host--0000) are
                   distinguished by distinct labels / data-testid

Exit code 0 = ALL PASS. Run standalone (dev instance must be up):
  make serve INSTANCE=dev
  python3 tests/e2e/s8478ca_runtime_ui.py
"""
from __future__ import annotations

import json
import os
import sys
import urllib.error
import urllib.request

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8215"
)
BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 20.0
PLAYWRIGHT_TIMEOUT = 15_000  # ms

HOST_REPO = "host--0000"  # S0c6a1b synthetic login scope

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def http_json(method: str, path: str, *, body: dict | None = None) -> tuple[int, object]:
    raw = json.dumps(body).encode() if body is not None else None
    headers = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(f"{BASE_URL}{path}", method=method, data=raw, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            data = resp.read()
            return resp.status, json.loads(data.decode() or "null")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "null")
        except json.JSONDecodeError:
            return e.code, ""


def get_playwright():
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("FAIL: playwright not installed (pip install playwright && playwright install chromium)",
              file=sys.stderr)
        sys.exit(1)
    return sync_playwright


def first_real_workspace() -> tuple[str, str] | None:
    """Return (repoId, branchId) of the first non-Host open Workspace, or None."""
    code, repos = http_json("GET", "/api/repos")
    if code != 200 or not isinstance(repos, list):
        return None
    for r in repos:
        if not isinstance(r, dict) or r.get("id") == HOST_REPO:
            continue
        for b in r.get("openBranches") or r.get("branches") or []:
            if isinstance(b, dict) and b.get("id"):
                return r["id"], b["id"]
    return None


# ─── AC-5-1: runtime caps drive selector greying ─────────────────────────────

def test_ac_5_1_runtime_caps_and_selector() -> None:
    """[AC-S8478ca-5-1] selector offers host/incus-container; incus greyed iff absent."""
    name = "AC-S8478ca-5-1"
    code, caps = http_json("GET", "/api/runtimes")
    if code != 200 or not isinstance(caps, dict):
        fail(name, f"GET /api/runtimes returned {code} / {caps!r}")
        return
    kinds = {k.get("kind"): k for k in caps.get("kinds", []) if isinstance(k, dict)}
    if "host" not in kinds or not kinds["host"].get("available", False):
        fail(name, f"host runtime must always be available; caps={caps!r}")
        return
    if "incus-container" not in kinds:
        fail(name, f"incus-container kind missing from caps; caps={caps!r}")
        return
    incus_available = bool(kinds["incus-container"].get("available"))

    sync_playwright = get_playwright()
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            page = browser.new_context(viewport={"width": 1280, "height": 800}).new_page()
            page.goto(BASE_URL, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
            # Open the repo picker (Open repo flow hosts the runtime selector).
            page.wait_for_selector("[data-testid='repo-picker-open']", timeout=PLAYWRIGHT_TIMEOUT)
            page.click("[data-testid='repo-picker-open']")
            page.wait_for_selector("[data-testid='runtime-selector']", timeout=PLAYWRIGHT_TIMEOUT)
            if page.locator("[data-testid='runtime-option-host']").count() < 1:
                fail(name, "runtime-option-host not present")
                return
            if incus_available:
                if page.locator("[data-testid='runtime-option-incus-container']").count() < 1:
                    fail(name, "Incus available but incus-container option not selectable")
                    return
                ok(name, "host + incus-container both selectable (Incus present)")
            else:
                disabled = page.locator("[data-testid='runtime-option-incus-container-disabled']")
                if disabled.count() < 1:
                    fail(name, "Incus absent but incus-container is not disabled")
                    return
                disabled.first.hover()
                page.wait_for_selector("[data-testid='runtime-incus-install-tooltip']",
                                       timeout=PLAYWRIGHT_TIMEOUT)
                ok(name, "incus-container greyed + install tooltip shown (Incus absent)")
        finally:
            browser.close()


# ─── AC-5-2: host workspace shows runtime chip/badge in ready ─────────────────

def test_ac_5_2_runtime_badge() -> None:
    """[AC-S8478ca-5-2] header chip + drawer badge reflect runtime view (host=ready)."""
    name = "AC-S8478ca-5-2"
    ws = first_real_workspace()
    if ws is None:
        fail(name, "no non-Host open Workspace available on dev instance to assert badge")
        return
    repo_id, branch_id = ws
    # The runtime view must be present on the branch object (REST + WS).
    code, repo = http_json("GET", f"/api/repos/{repo_id}")
    if code != 200 or not isinstance(repo, dict):
        fail(name, f"GET /api/repos/{repo_id} -> {code}")
        return
    branch = next((b for b in (repo.get("openBranches") or repo.get("branches") or [])
                   if isinstance(b, dict) and b.get("id") == branch_id), None)
    rt = (branch or {}).get("runtime")
    if not isinstance(rt, dict) or "state" not in rt or "kind" not in rt:
        fail(name, f"branch.runtime view missing kind/state; got {rt!r}")
        return

    sync_playwright = get_playwright()
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            page = browser.new_context(viewport={"width": 1280, "height": 800}).new_page()
            page.goto(f"{BASE_URL}/{repo_id}/{branch_id}/claude",
                      timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
            chip = page.wait_for_selector("[data-testid='runtime-chip']", timeout=PLAYWRIGHT_TIMEOUT)
            badge = page.wait_for_selector("[data-testid='workspace-runtime-badge']",
                                           timeout=PLAYWRIGHT_TIMEOUT)
            chip_state = chip.get_attribute("data-runtime-state")
            badge_state = badge.get_attribute("data-runtime-state")
            if chip_state not in ("ready", "starting", "stopped", "error"):
                fail(name, f"chip data-runtime-state invalid: {chip_state!r}")
                return
            if badge_state != chip_state:
                fail(name, f"badge ({badge_state}) and chip ({chip_state}) disagree")
                return
            if rt.get("kind") == "host" and chip_state != "ready":
                fail(name, f"host runtime should be ready, chip={chip_state}")
                return
            ok(name, f"runtime chip+badge in state={chip_state} (kind={rt.get('kind')})")
        finally:
            browser.close()


# ─── AC-5-3: Host login scope vs runtime.kind=host ───────────────────────────

def test_ac_5_3_host_scope_distinct() -> None:
    """[AC-S8478ca-5-3] Host login scope (host--0000) labelled distinctly; no selector."""
    name = "AC-S8478ca-5-3"
    sync_playwright = get_playwright()
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            page = browser.new_context(viewport={"width": 1280, "height": 800}).new_page()
            page.goto(BASE_URL, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
            # Host scope is reachable via its own drawer section / URL.
            page.goto(f"{BASE_URL}/{HOST_REPO}/host/bash:bash",
                      timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
            label = page.wait_for_selector("[data-testid='host-scope-label']",
                                           timeout=PLAYWRIGHT_TIMEOUT)
            text = (label.text_content() or "").lower()
            if "login" not in text and "host" not in text:
                fail(name, f"host-scope-label text unexpected: {text!r}")
                return
            # The Host login scope must NOT expose a runtime selector or runtime chip.
            if page.locator("[data-testid='runtime-selector']").count() > 0:
                fail(name, "Host login scope must not show a runtime selector")
                return
            if page.locator("[data-testid='runtime-chip']").count() > 0:
                fail(name, "Host login scope must not show a runtime.kind chip (confusable)")
                return
            ok(name, "Host login scope labelled distinctly with no runtime selector/chip")
        finally:
            browser.close()


def main() -> int:
    try:
        code, _ = http_json("GET", "/api/repos")
    except urllib.error.URLError as e:
        print(f"FAIL: dev instance not reachable at {BASE_URL}: {e}", file=sys.stderr)
        print("  start it with: make serve INSTANCE=dev", file=sys.stderr)
        return 1

    test_ac_5_1_runtime_caps_and_selector()
    test_ac_5_2_runtime_badge()
    test_ac_5_3_host_scope_distinct()

    if _FAILED:
        print(f"\nFAILED ACs: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
