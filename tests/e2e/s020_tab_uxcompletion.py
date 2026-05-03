#!/usr/bin/env python3
"""Sprint S020 — Tab UX completion E2E.

Drives the running dev palmux2 instance through the S020 acceptance:

  (a) Rename via API → display name changes → persists across `repos.json`
      reload (we re-read /api/repos and verify the new name).
  (b) Reorder via API → tabs come back in the new order on /api/repos.
  (c) Cross-group reorder rejected with 400 (Claude IDs cannot be
      reordered as part of a Bash payload).
  (d) WS event `tab.renamed` and `tab.reordered` fire on the events
      stream (best-effort: we listen for 5 seconds).
  (e) Bash focus ↔ Git focus key isolation: we exercise the keybinding
      library directly (the hook is registered only when the Git tab is
      mounted; switching to Bash unmounts it). Verified by checking
      that the keybindings.ts module exists and exports
      `useTabKeybindings` + `bindToTabType`.
  (f) Mobile parity: context menu items "Move left" / "Move right"
      / "Rename…" appear when long-press fires on a tab (we check the
      DOM after firing a synthetic right-click via Playwright; the
      mobile path is symmetric — long-press is wired to the same
      `onContext` callback).
  (g) `repos.json` schema: tab_overrides field is round-tripped (we
      read the file from disk after the rename + reorder).

Exit 0 = PASS.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8208"
)

BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 20.0
REPO_ROOT = Path(__file__).resolve().parents[2]


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def assert_(cond: bool, msg: str) -> None:
    if not cond:
        fail(msg)


def http(
    method: str,
    path: str,
    *,
    body: bytes | None = None,
    headers: dict[str, str] | None = None,
) -> tuple[int, dict[str, str], bytes]:
    url = f"{BASE_URL}{path}"
    req = urllib.request.Request(url, method=method, data=body, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            return resp.status, dict(resp.headers), resp.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers or {}), e.read()


def http_json(
    method: str, path: str, *, body: dict | list | None = None
) -> tuple[int, dict | list | str]:
    raw = json.dumps(body).encode() if body is not None else None
    h = {"Accept": "application/json"}
    if body is not None:
        h["Content-Type"] = "application/json"
    code, _hdrs, data = http(method, path, body=raw, headers=h)
    try:
        decoded: dict | list | str = json.loads(data.decode() or "{}")
    except json.JSONDecodeError:
        decoded = data.decode(errors="replace")
    return code, decoded


def run(cwd: Path, *args: str) -> str:
    res = subprocess.run(list(args), cwd=cwd, capture_output=True, text=True)
    if res.returncode != 0:
        raise RuntimeError(
            f"command failed in {cwd}: {' '.join(args)}\nstdout: {res.stdout}\nstderr: {res.stderr}"
        )
    return res.stdout


def ghq_root() -> Path:
    out = subprocess.run(["ghq", "root"], capture_output=True, text=True, check=True)
    return Path(out.stdout.strip())


# Fixture creation/cleanup is delegated to the shared helper in
# tests/e2e/_fixture.py (S025). The helper installs an atexit / signal
# hook so the fixture is removed even on Ctrl-C.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from _fixture import make_fixture as _make_fixture, Fixture as _Fixture  # noqa: E402

_FIXTURES: list[_Fixture] = []


def make_fixture_repo() -> tuple[Path, str, str]:
    fx = _make_fixture("s020")
    _FIXTURES.append(fx)
    return fx.path, fx.ghq_path, fx.repo_id


def get_branch(repo_id: str, branch_name: str) -> dict:
    code, repos = http_json("GET", "/api/repos")
    assert_(code == 200, f"GET /api/repos: {code}")
    for r in repos:  # type: ignore[union-attr]
        if r["id"] != repo_id:
            continue
        for b in r["openBranches"]:
            if b["name"] == branch_name:
                return b
    fail(f"branch {branch_name} not found")
    raise AssertionError("unreachable")


def fixture_cleanup(repo_id: str, repo_path: Path) -> None:
    # S025: delegate to the helper's cleanup so atexit registration stays
    # consistent. We match by repo_id; on the unlikely chance the entry is
    # missing (caller created it some other way), fall back to manual.
    for fx in list(_FIXTURES):
        if fx.repo_id == repo_id:
            fx._cleanup()
            try:
                _FIXTURES.remove(fx)
            except ValueError:
                pass
            return
    try:
        http_json("POST", f"/api/repos/{urllib.parse.quote(repo_id)}/close")
    except Exception:
        pass
    if repo_path.exists():
        try:
            shutil.rmtree(repo_path)
        except OSError:
            pass


# --- Test cases --------------------------------------------------------


def test_a_rename_persistence(repo_id: str) -> None:
    """Rename a Bash tab via PATCH → name updates → tabs reflect → repos.json
    has the tab_overrides entry."""
    branch = get_branch(repo_id, "main")
    # Add a second Bash tab to ensure rename works on a multi instance.
    code, t2 = http_json(
        "POST",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch['id'])}/tabs",
        body={"type": "bash"},
    )
    assert_(code in (200, 201), f"add bash tab: {code} {t2}")
    new_tab_id = t2["id"]  # type: ignore[index]
    print(f"  [a] added bash tab id={new_tab_id}")

    # PATCH rename.
    code, body = http_json(
        "PATCH",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch['id'])}/tabs/{urllib.parse.quote(new_tab_id)}",
        body={"name": "dev-server"},
    )
    assert_(code == 204, f"rename: {code} {body}")

    # Re-fetch.
    branch2 = get_branch(repo_id, "main")
    found = next(
        (t for t in branch2["tabSet"]["tabs"] if "dev-server" in (t.get("id") or "")),
        None,
    )
    assert_(found is not None,
            f"rename not reflected in tabs: {[t['id'] for t in branch2['tabSet']['tabs']]}")
    print(f"  [a] rename → new tab id={found['id']}, name={found['name']!r}")
    return found["id"]  # type: ignore[no-any-return]


def test_b_reorder_persistence(repo_id: str) -> None:
    """Add 3 Bash tabs, reorder, verify the new sequence is reflected
    on next /api/repos."""
    branch = get_branch(repo_id, "main")
    bash_tabs = [t for t in branch["tabSet"]["tabs"] if t["type"] == "bash"]
    # We need at least 3 total bash tabs for a meaningful reorder.
    while len(bash_tabs) < 3:
        code, _t = http_json(
            "POST",
            f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch['id'])}/tabs",
            body={"type": "bash"},
        )
        assert_(code in (200, 201), f"add bash tab: {code}")
        branch = get_branch(repo_id, "main")
        bash_tabs = [t for t in branch["tabSet"]["tabs"] if t["type"] == "bash"]
    ids = [t["id"] for t in bash_tabs]
    # Reverse the sequence and PUT.
    new_order = list(reversed(ids))
    code, body = http_json(
        "PUT",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch['id'])}/tabs/order",
        body={"order": new_order},
    )
    assert_(code == 204, f"reorder: {code} {body}")
    # Re-fetch and verify.
    branch3 = get_branch(repo_id, "main")
    bash_tabs2 = [t for t in branch3["tabSet"]["tabs"] if t["type"] == "bash"]
    got = [t["id"] for t in bash_tabs2]
    assert_(got == new_order,
            f"reorder did not persist; got {got}, wanted {new_order}")
    print(f"  [b] reorder OK: {got}")


def test_c_cross_group_reorder_rejected(repo_id: str) -> None:
    """Mixing Claude and Bash IDs in one PUT must 400."""
    branch = get_branch(repo_id, "main")
    claude_id = next(t["id"] for t in branch["tabSet"]["tabs"] if t["type"] == "claude")
    bash_id = next(t["id"] for t in branch["tabSet"]["tabs"] if t["type"] == "bash")
    code, body = http_json(
        "PUT",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch['id'])}/tabs/order",
        body={"order": [claude_id, bash_id]},
    )
    assert_(code == 400,
            f"cross-group reorder should 400, got {code} {body}")
    print(f"  [c] cross-group reorder rejected with 400 OK")


def test_d_singleton_reorder_rejected(repo_id: str) -> None:
    """Reorder payload that includes a singleton (Files / Git) must 400."""
    branch = get_branch(repo_id, "main")
    files_tab = next((t for t in branch["tabSet"]["tabs"] if t["type"] == "files"), None)
    if files_tab is None:
        print("  [d] (skip: no Files tab in fixture)")
        return
    code, body = http_json(
        "PUT",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch['id'])}/tabs/order",
        body={"order": [files_tab["id"]]},
    )
    assert_(code == 400, f"singleton reorder should 400, got {code} {body}")
    print(f"  [d] singleton reorder rejected with 400 OK")


def test_e_repos_json_overrides_round_trip(repo_id: str) -> None:
    """Verify the tab_overrides field landed in repos.json on disk."""
    p = REPO_ROOT / "tmp" / "repos.json"
    assert_(p.exists(), "repos.json not found at tmp/repos.json (dev instance using a different config dir?)")
    data = json.loads(p.read_text())
    entry = next((e for e in data if e.get("id") == repo_id), None)
    assert_(entry is not None, f"repo {repo_id} not in repos.json")
    overrides = entry.get("tabOverrides")  # type: ignore[union-attr]
    assert_(overrides is not None, f"tabOverrides missing in repos.json entry: {entry}")
    main_overrides = overrides.get("main", {})
    assert_("order" in main_overrides or "names" in main_overrides,
            f"branch overrides missing for 'main': {main_overrides}")
    print(f"  [e] tabOverrides round-trip OK: keys={list(main_overrides.keys())}")


def test_f_websocket_events(repo_id: str) -> None:
    """Listen on /api/events while issuing rename + reorder; require both
    `tab.renamed` and `tab.reordered` to land within 5s each."""
    try:
        import websockets  # type: ignore
    except ImportError:
        print("  [f] (skipped: `websockets` package not installed)")
        return

    import asyncio

    async def run_once() -> None:
        branch = get_branch(repo_id, "main")
        bash_tabs = [t for t in branch["tabSet"]["tabs"] if t["type"] == "bash"]
        if len(bash_tabs) < 2:
            print("  [f] (skip: fewer than 2 bash tabs)")
            return
        target = bash_tabs[0]
        async with websockets.connect(  # type: ignore[attr-defined]
            f"ws://localhost:{PORT}/api/events"
        ) as ws:
            # Issue rename.
            code, _ = http_json(
                "PATCH",
                f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch['id'])}/tabs/{urllib.parse.quote(target['id'])}",
                body={"name": "ws-rename-test"},
            )
            assert_(code == 204, f"rename during WS: {code}")

            # Reorder (just send the existing order to make sure it lands).
            branch2 = get_branch(repo_id, "main")
            bash2 = [t for t in branch2["tabSet"]["tabs"] if t["type"] == "bash"]
            order_payload = list(reversed([t["id"] for t in bash2]))
            code, _ = http_json(
                "PUT",
                f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch['id'])}/tabs/order",
                body={"order": order_payload},
            )
            assert_(code == 204, f"reorder during WS: {code}")

            seen_renamed = False
            seen_reordered = False
            deadline = time.time() + 6.0
            while time.time() < deadline and not (seen_renamed and seen_reordered):
                try:
                    frame = await asyncio.wait_for(ws.recv(), timeout=0.5)
                except asyncio.TimeoutError:
                    continue
                msg = json.loads(frame)
                t = msg.get("type")
                if t == "tab.renamed":
                    seen_renamed = True
                elif t == "tab.reordered":
                    seen_reordered = True
            assert_(seen_renamed, "tab.renamed not received")
            assert_(seen_reordered, "tab.reordered not received")
            print("  [f] WS tab.renamed + tab.reordered received OK")

    asyncio.run(run_once())


def test_g_keybinding_library_present() -> None:
    """The new lib/keybindings/ module must exist and export the
    documented hook + helper. We do a static check rather than runtime
    because it's pure FE plumbing."""
    p = REPO_ROOT / "frontend" / "src" / "lib" / "keybindings" / "index.ts"
    assert_(p.exists(), f"keybindings module missing at {p}")
    src = p.read_text()
    assert_("useTabKeybindings" in src, "useTabKeybindings export missing")
    assert_("bindToTabType" in src, "bindToTabType export missing")
    # Verify git-status was ported to use it.
    git_status = REPO_ROOT / "frontend" / "src" / "tabs" / "git" / "git-status.tsx"
    src2 = git_status.read_text()
    assert_("useTabKeybindings" in src2,
            "git-status.tsx not ported to useTabKeybindings")
    assert_("bindToTabType('git'" in src2,
            "git bindings not tagged with bindToTabType('git')")
    print("  [g] keybinding library present + git-status ported OK")


def test_h_playwright_rename_and_reorder_ui(repo_id: str) -> None:
    """Browser UI: open the fixture branch, right-click a Bash tab,
    pick Rename, type a new name, press Enter, verify the tab label
    updates. Then verify the context menu also has Move left / Move
    right entries (mobile parity)."""
    try:
        from playwright.sync_api import sync_playwright  # type: ignore
    except ImportError:
        print("  [h] (skipped: playwright not installed)")
        return

    branch = get_branch(repo_id, "main")
    primary_bash = next(t for t in branch["tabSet"]["tabs"] if t["type"] == "bash")
    target_url = (
        f"{BASE_URL}/{urllib.parse.quote(repo_id)}/{urllib.parse.quote(branch['id'])}/"
        f"{urllib.parse.quote(primary_bash['id'])}"
    )

    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        try:
            ctx = browser.new_context(viewport={"width": 1280, "height": 800})
            page = ctx.new_page()
            page.goto(target_url, wait_until="domcontentloaded")
            page.wait_for_timeout(2_000)

            # Find the bash tab in the rendered TabBar.
            tab_btn = page.locator(f'[data-tab-type="bash"]').first
            assert_(tab_btn.count() >= 1, "no bash tab visible in TabBar")
            # Right-click → custom context menu (Palmux blocks the
            # browser default and renders its own).
            tab_btn.click(button="right")
            page.wait_for_timeout(400)

            # Verify the menu has Rename + Move left + Move right + Close.
            menu_text = page.locator("body").inner_text()
            assert_("Rename" in menu_text, "Rename… missing from context menu")
            assert_("Move left" in menu_text or "Move right" in menu_text,
                    "Move left/right missing from context menu")

            # Click Rename (it shows as "Rename…" in the menu).
            page.get_by_text("Rename…", exact=False).first.click()
            page.wait_for_timeout(300)
            input_el = page.locator('[data-testid="tab-rename-input"]').first
            assert_(input_el.count() >= 1, "rename input did not appear")
            input_el.fill("ui-rename")
            input_el.press("Enter")
            page.wait_for_timeout(800)

            # Re-fetch and assert the rename was committed.
            branch_after = get_branch(repo_id, "main")
            renamed = next(
                (t for t in branch_after["tabSet"]["tabs"] if "ui-rename" in (t.get("id") or "")),
                None,
            )
            assert_(renamed is not None,
                    f"UI rename did not commit; tabs: {[t['id'] for t in branch_after['tabSet']['tabs']]}")
            print(f"  [h] UI rename committed: {renamed['id']!r}")
        finally:
            browser.close()


# --- main --------------------------------------------------------------


def main() -> None:
    print("S020 — Tab UX completion E2E (port", PORT + ")")
    code, _ = http_json("GET", "/api/repos")
    assert_(code == 200, f"server reachability: {code}")

    repo_path, ghq_path, repo_id = make_fixture_repo()
    print(f"  fixture: {ghq_path} → {repo_id}")
    try:
        test_a_rename_persistence(repo_id)
        test_b_reorder_persistence(repo_id)
        test_c_cross_group_reorder_rejected(repo_id)
        test_d_singleton_reorder_rejected(repo_id)
        test_e_repos_json_overrides_round_trip(repo_id)
        test_f_websocket_events(repo_id)
        test_g_keybinding_library_present()
        test_h_playwright_rename_and_reorder_ui(repo_id)
        print("S020 E2E: PASS")
    finally:
        fixture_cleanup(repo_id, repo_path)


if __name__ == "__main__":
    main()
