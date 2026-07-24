#!/usr/bin/env python3
"""Sprint Sd0e1a9 Story 2 — tab-set changes always reach the browser (E2E).

Real-server E2E against a dev instance (make serve INSTANCE=dev, production
mode). ADR-0012 unified the notification path: before it, thirteen recompute
call sites existed and only ONE diffed and published, so a tab-set change made
by the sync loop / a container regenerate / startup population left every
connected browser rendering a stale TabBar until its next full REST reload.

Acceptance criteria:
  [AC-Sd0e1a9-2-2] (a) a tab added/removed from OUTSIDE the browser (REST API)
                   appears/disappears in the TabBar with no page reload
                   (b) after an external `tmux kill-session` and the sync_tmux
                   recovery that follows, the TabBar still agrees with the
                   backend — the S009-fix-1 class of "my Bash tabs vanished"
                   must not reappear

NOTE on (b): the recovery path is deliberately tab-set PRESERVING —
enrichRecoverySpecs reinstates every multi-instance window the in-memory tab
list knows about. So the assertion is agreement, not change. An earlier draft
of this Story's AC expected the recovery to change the tab set; it does not,
and testing for a change would have produced a test that could never fail
honestly.

This test NEVER calls page.reload(). That is the entire point.

Safety: runs against a THROWAWAY git repo created under the ghq root and
removed again; it never mutates a repo the user owns. Cleanup runs on success,
failure, and SIGTERM.

Exit code 0 = ALL PASS. Run standalone (dev instance must be up):
  make serve INSTANCE=dev
  python3 tests/e2e/sd0e1a9_tab_events.py
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
PLAYWRIGHT_TIMEOUT = 20_000  # ms
SYNC_TMUX_PERIOD_S = 5

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


def backend_tab_ids(repo_id: str, branch_id: str) -> list[str]:
    code, ts = http_json("GET", f"/api/repos/{repo_id}/branches/{branch_id}/tabs")
    if code != 200 or not isinstance(ts, dict):
        return []
    return [t["id"] for t in (ts.get("tabs") or [])]


def dom_tab_ids(page) -> set[str]:
    """Tab ids currently rendered in the TabBar (mirrors tab-bar.tsx testids)."""
    ids = set(page.eval_on_selector_all(
        '[data-testid^="tab-"]',
        "els => els.map(e => e.getAttribute('data-testid'))",
    ) or [])
    out = {i[len("tab-"):] for i in ids if i and i.startswith("tab-")
           and not i.startswith("tab-add-") and i != "tab-rename-input"}
    if page.locator('[data-testid="claude-tab"]').count() > 0:
        out.add("__claude__")
    return out


def make_throwaway_repo() -> tuple[str, str] | None:
    """Create a throwaway git repo under the ghq root. Returns (path, repoId)."""
    try:
        root = subprocess.run(["ghq", "root"], capture_output=True, text=True,
                              timeout=10, check=True).stdout.strip()
    except Exception as e:  # noqa: BLE001
        print(f"  (ghq root failed: {e})", file=sys.stderr)
        return None
    name = f"sd0e1a9ev-{int(time.time())}"
    ghq_path = f"github.com/palmux2-test/{name}"
    path = os.path.join(root, ghq_path)
    os.makedirs(path, exist_ok=True)
    _CLEANUP.append(lambda: shutil.rmtree(path, ignore_errors=True))
    for cmd in (["git", "init", "-q", "-b", "main"],
                ["git", "config", "user.email", "e2e@example.com"],
                ["git", "config", "user.name", "e2e"]):
        subprocess.run(cmd, cwd=path, check=True, timeout=15)
    with open(os.path.join(path, "README.md"), "w") as f:
        f.write("throwaway repo for tests/e2e/sd0e1a9_tab_events.py\n")
    subprocess.run(["git", "add", "-A"], cwd=path, check=True, timeout=15)
    subprocess.run(["git", "commit", "-qm", "init"], cwd=path, check=True, timeout=15)

    code, repos = http_json("GET", "/api/repos/available")
    if code == 200 and isinstance(repos, list):
        for r in repos:
            if isinstance(r, dict) and r.get("ghqPath") == ghq_path:
                return path, r["id"]
    print(f"  (throwaway repo {ghq_path} not visible to ghq)", file=sys.stderr)
    return None


def open_throwaway_workspace() -> tuple[str, str, str] | None:
    made = make_throwaway_repo()
    if made is None:
        return None
    _path, repo_id = made
    code, _ = http_json("POST", f"/api/repos/{repo_id}/open")
    if code != 200:
        print(f"  (open repo -> {code})", file=sys.stderr)
        return None
    _CLEANUP.append(lambda: http_json("POST", f"/api/repos/{repo_id}/close"))
    code, branch = http_json("POST", f"/api/repos/{repo_id}/branches/open",
                             body={"branchName": "main"})
    if code not in (200, 201) or not isinstance(branch, dict):
        print(f"  (open branch -> {code} {branch})", file=sys.stderr)
        return None
    session = (branch.get("tabSet") or {}).get("tmuxSession") or ""
    return repo_id, branch["id"], session


# ─── AC-2-2 (a): out-of-band tab mutation reaches the TabBar live ────────────

def test_out_of_band_tab_mutation(page, repo_id: str, branch_id: str) -> None:
    """[AC-Sd0e1a9-2-2] a REST-API tab add/remove updates the TabBar with no reload."""
    name = "AC-Sd0e1a9-2-2a"

    page.goto(f"{BASE_URL}/{repo_id}/{branch_id}/bash:bash",
              wait_until="domcontentloaded", timeout=PLAYWRIGHT_TIMEOUT)
    page.wait_for_selector('[data-testid="tab-bash:bash"]', timeout=PLAYWRIGHT_TIMEOUT)

    # Add a Bash tab from OUTSIDE the browser.
    code, added = http_json("POST", f"/api/repos/{repo_id}/branches/{branch_id}/tabs",
                            body={"type": "bash"})
    if code not in (200, 201) or not isinstance(added, dict):
        fail(name, f"POST /tabs -> {code} {added}")
        return
    new_id = added["id"]
    try:
        page.wait_for_selector(f'[data-testid="tab-{new_id}"]', timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:
        fail(name, f"tab {new_id} added via REST never appeared in the TabBar (no reload)")
        return

    # Remove it again from OUTSIDE the browser.
    code, _ = http_json("DELETE", f"/api/repos/{repo_id}/branches/{branch_id}/tabs/{new_id}")
    if code not in (200, 204):
        fail(name, f"DELETE /tabs/{new_id} -> {code}")
        return
    try:
        page.wait_for_selector(f'[data-testid="tab-{new_id}"]', state="detached",
                               timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:
        fail(name, f"tab {new_id} removed via REST never disappeared from the TabBar (no reload)")
        return
    ok(name, "REST-driven add and remove both reached the TabBar without a reload")


# ─── AC-2-2 (b): survives an external tmux kill + sync_tmux recovery ─────────

def test_survives_external_session_kill(page, repo_id: str, branch_id: str, session: str) -> None:
    """[AC-Sd0e1a9-2-2] after an external kill-session + recovery, DOM still matches BE."""
    name = "AC-Sd0e1a9-2-2b"
    if not session:
        fail(name, "branch reported no tmux session name")
        return

    # Give the workspace two extra Bash tabs so a silent loss would be visible.
    for _ in range(2):
        code, _ = http_json("POST", f"/api/repos/{repo_id}/branches/{branch_id}/tabs",
                            body={"type": "bash"})
        if code not in (200, 201):
            fail(name, f"could not seed extra bash tab: {code}")
            return
    before = backend_tab_ids(repo_id, branch_id)
    if len([t for t in before if t.startswith("bash:")]) < 3:
        fail(name, f"setup: expected 3 bash tabs, got {before}")
        return

    page.goto(f"{BASE_URL}/{repo_id}/{branch_id}/bash:bash",
              wait_until="domcontentloaded", timeout=PLAYWRIGHT_TIMEOUT)
    page.wait_for_selector('[data-testid="tab-bash:bash"]', timeout=PLAYWRIGHT_TIMEOUT)

    # Kill the tmux session behind palmux's back. NOTE: no page.reload() after this.
    r = subprocess.run(["tmux", "kill-session", "-t", session],
                       capture_output=True, text=True, timeout=15)
    if r.returncode != 0:
        fail(name, f"tmux kill-session failed: {r.stderr.strip()}")
        return

    # Wait for the sync_tmux recovery cycle, then compare DOM against the backend.
    deadline = time.time() + SYNC_TMUX_PERIOD_S * 4
    last_be: list[str] = []
    while time.time() < deadline:
        time.sleep(1.0)
        last_be = backend_tab_ids(repo_id, branch_id)
        dom = dom_tab_ids(page)
        be = {t for t in last_be if not t.startswith("claude:")}
        if be and be.issubset(dom):
            ok(name, f"after external kill + recovery the TabBar still matches the backend ({sorted(be)})")
            return
    fail(name, f"TabBar and backend disagree after recovery: backend={last_be} dom={sorted(dom_tab_ids(page))}")


def cleanup() -> None:
    for fn in reversed(_CLEANUP):
        try:
            fn()
        except Exception as e:  # noqa: BLE001
            print(f"  (cleanup step failed: {e})", file=sys.stderr)
    _CLEANUP.clear()


def main() -> int:
    def _on_term(_sig, _frm):
        print("SIGTERM received — cleaning up", file=sys.stderr)
        cleanup()
        sys.exit(1)

    signal.signal(signal.SIGTERM, _on_term)
    signal.signal(signal.SIGINT, _on_term)

    print(f"Sd0e1a9 Story 2 E2E against {BASE_URL}")
    try:
        code, _ = http_json("GET", "/api/repos")
        if code != 200:
            print(f"FAIL: dev instance not reachable at {BASE_URL} (GET /api/repos -> {code})",
                  file=sys.stderr)
            return 1

        try:
            from playwright.sync_api import sync_playwright
        except ImportError:
            print("FAIL: playwright not installed", file=sys.stderr)
            return 1

        ws = open_throwaway_workspace()
        if ws is None:
            print("FAIL: could not create a throwaway workspace", file=sys.stderr)
            return 1
        repo_id, branch_id, session = ws

        with sync_playwright() as pw:
            browser = pw.chromium.launch(headless=True)
            page = browser.new_page(viewport={"width": 1400, "height": 900})
            try:
                test_out_of_band_tab_mutation(page, repo_id, branch_id)
                test_survives_external_session_kill(page, repo_id, branch_id, session)
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
