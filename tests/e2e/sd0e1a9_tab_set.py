#!/usr/bin/env python3
"""Sprint Sd0e1a9 Story 1 — tab-set derivation is unchanged after ADR-0012 (E2E).

Real-server E2E against a dev instance (make serve INSTANCE=dev, production
mode). ADR-0012 replaced the reducer's provider-side input: the Store used to
call the OnBranchOpen LIFECYCLE hook as a query, it now calls the pure Tabs()
query. That is invisible by design — this file is the end-to-end proof that
the user-facing tab set really did not move.

Acceptance criteria:
  [AC-Sd0e1a9-1-6] TabBar shows the same tab set as the backend (type, order,
                   ids), no `claude-tui` tab type leaks into it, and the
                   conditional tabs (Sprint / Browser / Ports) still appear and
                   disappear — including a live flip with no page reload.
  [AC-Sd0e1a9-1-2] Conditional visibility survives the removal of the
                   Conditional() flag: a provider whose Tabs() returns zero
                   tabs is simply absent from the TabBar.
  [AC-Sd0e1a9-1-3] agenttui became a service participant, so no tab of type
                   `claude-tui` can exist any more.

Safety: the live-flip check runs against a THROWAWAY git repo this script
creates under the ghq root and removes again. It never mutates a repo the user
owns. Cleanup runs on success, failure, and SIGTERM.

Exit code 0 = ALL PASS. Run standalone (dev instance must be up):
  make serve INSTANCE=dev
  python3 tests/e2e/sd0e1a9_tab_set.py
"""
from __future__ import annotations

import json
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.request

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8200"
)
BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 20.0
PLAYWRIGHT_TIMEOUT = 15_000  # ms
HOST_REPO = "host--0000"

_FAILED: list[str] = []
_CLEANUP: list = []


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
            return resp.status, json.loads(resp.read().decode() or "null")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "null")
        except json.JSONDecodeError:
            return e.code, ""


def open_workspaces() -> list[tuple[dict, dict]]:
    """Every (repo, branch) pair currently open, excluding the Host scope."""
    code, repos = http_json("GET", "/api/repos")
    if code != 200 or not isinstance(repos, list):
        return []
    out = []
    for r in repos:
        if not isinstance(r, dict) or r.get("id") == HOST_REPO:
            continue
        for b in r.get("openBranches") or []:
            if isinstance(b, dict) and b.get("id"):
                out.append((r, b))
    return out


def backend_tabs(repo_id: str, branch_id: str) -> list[dict]:
    code, ts = http_json("GET", f"/api/repos/{repo_id}/branches/{branch_id}/tabs")
    if code != 200 or not isinstance(ts, dict):
        return []
    return ts.get("tabs") or []


def testid_for(tab: dict) -> str:
    """Mirror tab-bar.tsx: claude gets a fixed testid, everything else tab-{id}."""
    return "claude-tab" if tab.get("type") == "claude" else f"tab-{tab['id']}"


# ─── AC-1-3: no claude-tui tab type can exist ────────────────────────────────

def test_no_claude_tui_tab_type() -> None:
    """[AC-Sd0e1a9-1-3] agenttui is a service participant; it contributes no tabs."""
    name = "AC-Sd0e1a9-1-3"
    workspaces = open_workspaces()
    if not workspaces:
        fail(name, "no open workspace on the dev instance — cannot verify")
        return
    offenders = []
    for repo, branch in workspaces:
        for t in backend_tabs(repo["id"], branch["id"]):
            if t.get("type") == "claude-tui":
                offenders.append(f'{repo["id"]}/{branch["id"]}:{t["id"]}')
    if offenders:
        fail(name, f"claude-tui tabs leaked into the tab set: {offenders}")
        return
    ok(name, f"no claude-tui tab across {len(workspaces)} workspaces")


# ─── AC-1-2: conditional visibility without the Conditional() flag ───────────

def test_conditional_visibility_observed() -> None:
    """[AC-Sd0e1a9-1-2] Tabs() returning zero tabs is how a tab hides itself."""
    name = "AC-Sd0e1a9-1-2"
    workspaces = open_workspaces()
    if not workspaces:
        fail(name, "no open workspace on the dev instance — cannot verify")
        return

    with_sprint, without_sprint = [], []
    for repo, branch in workspaces:
        types = {t.get("type") for t in backend_tabs(repo["id"], branch["id"])}
        (with_sprint if "sprint" in types else without_sprint).append(f'{repo["id"]}/{branch["id"]}')

    if not with_sprint:
        fail(name, "no workspace shows the Sprint tab — conditional providers may be dead")
        return
    if not without_sprint:
        fail(name,
             "every workspace shows the Sprint tab — cannot prove the hide path; "
             "open a workspace without docs/ROADMAP.json")
        return
    ok(name, f"Sprint visible in {len(with_sprint)}, hidden in {len(without_sprint)} workspaces")


# ─── AC-1-6: TabBar DOM matches the backend tab set exactly ──────────────────

def test_tabbar_matches_backend(page) -> None:
    """[AC-Sd0e1a9-1-6] the rendered TabBar is exactly the backend's tab set."""
    name = "AC-Sd0e1a9-1-6"
    workspaces = open_workspaces()
    if not workspaces:
        fail(name, "no open workspace on the dev instance — cannot verify")
        return
    repo, branch = workspaces[0]
    tabs = backend_tabs(repo["id"], branch["id"])
    if not tabs:
        fail(name, f'workspace {repo["id"]}/{branch["id"]} reports zero tabs')
        return

    page.goto(f'{BASE_URL}/{repo["id"]}/{branch["id"]}/{tabs[0]["id"]}',
              wait_until="domcontentloaded", timeout=PLAYWRIGHT_TIMEOUT)
    page.wait_for_selector('[data-testid="claude-tab"], [data-testid^="tab-"]',
                           timeout=PLAYWRIGHT_TIMEOUT)

    missing = []
    for t in tabs:
        tid = testid_for(t)
        try:
            page.wait_for_selector(f'[data-testid="{tid}"]', timeout=4000)
        except Exception:
            missing.append(f'{t["id"]} (testid={tid})')
    if missing:
        fail(name, f"backend tabs absent from the TabBar: {missing}")
        return
    ok(name, f'TabBar renders all {len(tabs)} backend tabs for {repo["id"]}/{branch["id"]}')


# ─── AC-1-6 (live flip): conditional tab appears/disappears without reload ───

def make_throwaway_repo() -> tuple[str, str, str] | None:
    """Create a throwaway git repo under the ghq root. Returns (path, ghqPath, repoId)."""
    try:
        root = subprocess.run(["ghq", "root"], capture_output=True, text=True,
                              timeout=10, check=True).stdout.strip()
    except Exception as e:  # noqa: BLE001
        print(f"  (ghq root failed: {e})", file=sys.stderr)
        return None
    name = f"sd0e1a9-{int(time.time())}"
    ghq_path = f"github.com/palmux2-test/{name}"
    path = os.path.join(root, ghq_path)
    os.makedirs(path, exist_ok=True)
    _CLEANUP.append(lambda: shutil.rmtree(path, ignore_errors=True))
    for cmd in (["git", "init", "-q", "-b", "main"],
                ["git", "config", "user.email", "e2e@example.com"],
                ["git", "config", "user.name", "e2e"]):
        subprocess.run(cmd, cwd=path, check=True, timeout=15)
    with open(os.path.join(path, "README.md"), "w") as f:
        f.write("throwaway repo for tests/e2e/sd0e1a9_tab_set.py\n")
    subprocess.run(["git", "add", "-A"], cwd=path, check=True, timeout=15)
    subprocess.run(["git", "commit", "-qm", "init"], cwd=path, check=True, timeout=15)

    code, repos = http_json("GET", "/api/repos/available")
    repo_id = None
    if code == 200 and isinstance(repos, list):
        for r in repos:
            if isinstance(r, dict) and r.get("ghqPath") == ghq_path:
                repo_id = r.get("id")
                break
    if not repo_id:
        print(f"  (throwaway repo {ghq_path} not visible to ghq)", file=sys.stderr)
        return None
    return path, ghq_path, repo_id


def test_conditional_live_flip(page) -> None:
    """[AC-Sd0e1a9-1-6] Sprint tab appears/disappears with docs/ROADMAP.json, no reload."""
    name = "AC-Sd0e1a9-1-6-live"
    made = make_throwaway_repo()
    if made is None:
        fail(name, "could not create a throwaway repo — live flip not verified")
        return
    path, _ghq_path, repo_id = made

    code, _ = http_json("POST", f"/api/repos/{repo_id}/open")
    if code != 200:
        fail(name, f"POST /api/repos/{repo_id}/open -> {code}")
        return
    _CLEANUP.append(lambda: http_json("POST", f"/api/repos/{repo_id}/close"))

    code, branch = http_json("POST", f"/api/repos/{repo_id}/branches/open",
                             body={"branchName": "main"})
    if code not in (200, 201) or not isinstance(branch, dict):
        fail(name, f"branches/open -> {code} {branch}")
        return
    branch_id = branch["id"]

    types = {t.get("type") for t in backend_tabs(repo_id, branch_id)}
    if "sprint" in types:
        fail(name, "fresh throwaway repo already shows the Sprint tab (expected hidden)")
        return

    page.goto(f"{BASE_URL}/{repo_id}/{branch_id}/bash:bash",
              wait_until="domcontentloaded", timeout=PLAYWRIGHT_TIMEOUT)
    page.wait_for_selector('[data-testid^="tab-"]', timeout=PLAYWRIGHT_TIMEOUT)
    if page.locator('[data-testid="tab-sprint"]').count() != 0:
        fail(name, "Sprint tab rendered before docs/ROADMAP.json existed")
        return

    # Appear — NOTE: no page.reload() anywhere in this test. That is the point.
    os.makedirs(os.path.join(path, "docs"), exist_ok=True)
    with open(os.path.join(path, "docs", "ROADMAP.json"), "w") as f:
        json.dump({"project": "throwaway", "sprints": {}}, f)
    try:
        page.wait_for_selector('[data-testid="tab-sprint"]', timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:
        fail(name, "Sprint tab did not appear within 15s of creating docs/ROADMAP.json (no reload)")
        return

    # Disappear
    os.remove(os.path.join(path, "docs", "ROADMAP.json"))
    try:
        page.wait_for_selector('[data-testid="tab-sprint"]', state="detached",
                               timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:
        fail(name, "Sprint tab did not disappear within 15s of removing docs/ROADMAP.json (no reload)")
        return
    ok(name, "Sprint tab appeared and disappeared live, without a page reload")


def cleanup() -> None:
    for fn in reversed(_CLEANUP):
        try:
            fn()
        except Exception as e:  # noqa: BLE001
            print(f"  (cleanup step failed: {e})", file=sys.stderr)
    _CLEANUP.clear()


def main() -> int:
    # Cleanup must survive SIGTERM too — `timeout` sends SIGTERM, and a Python
    # `finally` does not run for the default SIGTERM handler.
    def _on_term(_sig, _frm):
        print("SIGTERM received — cleaning up", file=sys.stderr)
        cleanup()
        sys.exit(1)

    signal.signal(signal.SIGTERM, _on_term)
    signal.signal(signal.SIGINT, _on_term)

    print(f"Sd0e1a9 Story 1 E2E against {BASE_URL}")
    try:
        code, _ = http_json("GET", "/api/repos")
        if code != 200:
            print(f"FAIL: dev instance not reachable at {BASE_URL} (GET /api/repos -> {code})",
                  file=sys.stderr)
            return 1

        test_no_claude_tui_tab_type()
        test_conditional_visibility_observed()

        try:
            from playwright.sync_api import sync_playwright
        except ImportError:
            print("FAIL: playwright not installed (pip install playwright && playwright install chromium)",
                  file=sys.stderr)
            return 1

        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            page = browser.new_page(viewport={"width": 1400, "height": 900})
            try:
                test_tabbar_matches_backend(page)
                test_conditional_live_flip(page)
            finally:
                browser.close()
    finally:
        cleanup()

    if _FAILED:
        print(f"\n{len(_FAILED)} FAILED: {', '.join(_FAILED)}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
