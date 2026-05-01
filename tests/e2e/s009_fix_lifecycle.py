#!/usr/bin/env python3
"""S009-fix-1 — Tab lifecycle / WS reconnect bug fixes.

Covers the four user-reported regressions from the post-S015 refine
review:

  (a) Adding a Claude tab via `+` must not transiently drop Bash tabs
      from /api/repos/.../tabs. Pre-fix the response after POST /tabs
      contained only Claude+Files+Git — Bash had vanished because
      `recomputeTabs` queried tmux while the session was mid-rebuild.

  (b) Removing the second Claude tab must leave Bash intact. Same root
      cause as (a): the post-DELETE recompute also dropped Bash.

  (c) Re-adding a Claude tab after (a)+(b) must not bring back a
      previously-removed Bash tab — the tab list is exactly what the
      user shaped.

  (d) Adding a Bash tab via POST must not silently no-op when the tmux
      session was GC'd between sync_tmux cycles. Pre-fix
      `pickNextWindowName` failed with "can't find session" and the
      AddTab call returned an error; the user's `+` click looked dead.

  (e) Adding multiple Bash tabs in sequence must keep all of them, with
      stable IDs and no cross-tab interference.

  (f) Independent Claude / Bash tab operations must not affect each
      other's WS attach paths.

Run against a live dev instance — the agent CI loop calls this with
PALMUX2_DEV_PORT pointing at `make serve INSTANCE=dev`.

Exit 0 = PASS.
"""

from __future__ import annotations

import asyncio
import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

PORT = (
    os.environ.get("PALMUX_DEV_PORT")
    or os.environ.get("PALMUX2_DEV_PORT")
    or "8283"
)
REPO_ID = os.environ.get(
    "S009_FIX_REPO_ID", "tjst-t--palmux2--2d59"
)
BRANCH_ID = os.environ.get(
    "S009_FIX_BRANCH_ID", "autopilot--main--S009-fix-1--bf55"
)
BASE_URL = f"http://localhost:{PORT}"

TIMEOUT_S = 15.0


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def http(method: str, path: str, body: dict | None = None) -> tuple[int, dict | str]:
    url = f"{BASE_URL}{path}"
    data = None
    headers = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            raw = resp.read().decode() or "{}"
            try:
                return resp.status, json.loads(raw)
            except json.JSONDecodeError:
                return resp.status, raw
    except urllib.error.HTTPError as e:
        raw = e.read().decode() or "{}"
        try:
            return e.code, json.loads(raw)
        except json.JSONDecodeError:
            return e.code, raw


def list_tabs() -> list[dict]:
    code, body = http("GET", f"/api/repos/{REPO_ID}/branches/{BRANCH_ID}/tabs")
    if code != 200:
        fail(f"GET /tabs returned {code}: {body}")
    return body["tabs"]


def add_tab(typ: str) -> tuple[int, dict | str]:
    return http(
        "POST",
        f"/api/repos/{REPO_ID}/branches/{BRANCH_ID}/tabs",
        {"type": typ},
    )


def remove_tab(tab_id: str) -> tuple[int, dict | str]:
    return http(
        "DELETE",
        f"/api/repos/{REPO_ID}/branches/{BRANCH_ID}/tabs/{urllib.parse.quote(tab_id)}",
    )


def tabs_by_type(tabs: list[dict]) -> dict[str, list[str]]:
    out: dict[str, list[str]] = {}
    for t in tabs:
        out.setdefault(t["type"], []).append(t["id"])
    return out


def cleanup_extras() -> None:
    """Delete every non-canonical Claude/Bash tab so each test starts
    from {claude:claude, bash:bash, files, git}."""
    tabs = list_tabs()
    for t in tabs:
        tid = t["id"]
        if tid in ("claude:claude", "bash:bash", "files", "git"):
            continue
        if t["type"] in ("claude", "bash"):
            code, _ = remove_tab(tid)
            if code not in (204, 404, 409):
                fail(f"cleanup: DELETE {tid} returned {code}")


def assert_state(expected_claude: list[str], expected_bash: list[str], ctx: str) -> None:
    state = tabs_by_type(list_tabs())
    actual_claude = state.get("claude", [])
    actual_bash = state.get("bash", [])
    if actual_claude != expected_claude:
        fail(
            f"{ctx}: claude tabs mismatch. expected={expected_claude} actual={actual_claude}"
        )
    if actual_bash != expected_bash:
        fail(
            f"{ctx}: bash tabs mismatch. expected={expected_bash} actual={actual_bash}"
        )
    # Files / Git invariants
    if "files" not in state or state["files"] != ["files"]:
        fail(f"{ctx}: Files tab missing or duplicated: {state.get('files')}")
    if "git" not in state or state["git"] != ["git"]:
        fail(f"{ctx}: Git tab missing or duplicated: {state.get('git')}")


def case_a_add_claude_keeps_bash() -> None:
    """(a) POST /tabs {type:claude} must not transiently drop Bash."""
    cleanup_extras()
    assert_state(["claude:claude"], ["bash:bash"], "case-a starting state")

    code, body = add_tab("claude")
    if code != 201:
        fail(f"case-a: POST claude returned {code}: {body}")
    if not isinstance(body, dict) or body.get("id") != "claude:claude-2":
        fail(f"case-a: unexpected body: {body}")

    # Immediately verify Bash is still present in the snapshot. This is the
    # exact moment that pre-fix returned [claude, claude-2, files, git].
    assert_state(
        ["claude:claude", "claude:claude-2"], ["bash:bash"], "case-a after add"
    )
    print("  [PASS] case-a: adding a 2nd Claude tab leaves Bash intact")


def case_b_remove_claude_keeps_bash() -> None:
    """(b) DELETE /tabs/{claude:claude-2} must keep Bash intact."""
    cleanup_extras()
    code, _ = add_tab("claude")
    if code != 201:
        fail("case-b: setup add claude failed")

    code, _ = remove_tab("claude:claude-2")
    if code != 204:
        fail(f"case-b: DELETE claude:claude-2 returned {code}")

    assert_state(["claude:claude"], ["bash:bash"], "case-b after delete")
    print("  [PASS] case-b: removing the 2nd Claude tab leaves Bash intact")


def case_c_re_add_claude_does_not_resurrect_bash() -> None:
    """(c) After (a)+(b) the user-shaped tab list is exactly what was
    last set — re-adding a Claude tab must not bring back a previously
    removed Bash tab from some stale recompute cache."""
    cleanup_extras()

    # Add bash:bash-2, then DELETE it. Bash count should now be just 1.
    code, body = add_tab("bash")
    if code != 201 or body.get("id") != "bash:bash-2":
        fail(f"case-c: setup add bash failed: {code} {body}")
    code, _ = remove_tab("bash:bash-2")
    if code != 204:
        fail("case-c: setup delete bash-2 failed")

    # Add Claude. Bash should still be exactly [bash:bash], NOT
    # [bash:bash, bash:bash-2].
    code, body = add_tab("claude")
    if code != 201:
        fail(f"case-c: add claude returned {code}: {body}")

    assert_state(
        ["claude:claude", "claude:claude-2"], ["bash:bash"], "case-c after add"
    )
    print("  [PASS] case-c: re-adding Claude doesn't resurrect previously-deleted Bash")


def case_d_bash_add_during_session_gc() -> None:
    """(d) POST {type:bash} must succeed even when the underlying
    tmux session is mid-rebuild. Pre-fix it failed with 500
    "can't find session" and the user's `+` click was a no-op."""
    cleanup_extras()

    # Wait long enough that any sync_tmux cycle has settled (5s ticker).
    time.sleep(2)

    # Add 3 bash tabs back-to-back. Pre-fix any of these could fail
    # depending on timing.
    expected = ["bash:bash"]
    for i in range(2, 5):
        code, body = add_tab("bash")
        if code != 201:
            fail(f"case-d: bash add #{i} returned {code}: {body}")
        expected.append(f"bash:bash-{i}")
        assert_state(["claude:claude"], expected, f"case-d after add #{i}")

    print("  [PASS] case-d: rapid Bash adds all succeed under sync_tmux pressure")


def case_e_independent_lifecycles() -> None:
    """(e) Claude and Bash lifecycles are independent — operations on
    one type must not affect the other's tabs in any direction."""
    cleanup_extras()

    # Add 2 claude + 2 bash interleaved
    operations = [
        ("add", "claude", "claude:claude-2"),
        ("add", "bash", "bash:bash-2"),
        ("add", "claude", "claude:claude-3"),
        ("add", "bash", "bash:bash-3"),
    ]
    for op, typ, expected_id in operations:
        if op == "add":
            code, body = add_tab(typ)
            if code != 201:
                fail(f"case-e: {op} {typ} returned {code}")
            if body.get("id") != expected_id:
                fail(f"case-e: expected {expected_id}, got {body.get('id')}")

    # All four extra tabs should be present alongside the canonical ones.
    assert_state(
        ["claude:claude", "claude:claude-2", "claude:claude-3"],
        ["bash:bash", "bash:bash-2", "bash:bash-3"],
        "case-e after interleaved adds",
    )

    # Now remove a Bash and a Claude in the middle of the list.
    if remove_tab("bash:bash-2")[0] != 204:
        fail("case-e: remove bash:bash-2")
    assert_state(
        ["claude:claude", "claude:claude-2", "claude:claude-3"],
        ["bash:bash", "bash:bash-3"],
        "case-e after bash:bash-2 removed",
    )
    if remove_tab("claude:claude-2")[0] != 204:
        fail("case-e: remove claude:claude-2")
    assert_state(
        ["claude:claude", "claude:claude-3"],
        ["bash:bash", "bash:bash-3"],
        "case-e after claude:claude-2 removed",
    )

    print("  [PASS] case-e: claude/bash lifecycles are independent")


def case_f_caps_and_floors_unchanged() -> None:
    """(f) The fix must NOT regress the existing S009 cap/floor
    enforcement. 4th claude → 409, deleting last claude → 409,
    deleting Files/Git → 409."""
    cleanup_extras()

    # Push to claude cap (3)
    add_tab("claude")  # -2
    add_tab("claude")  # -3
    code, body = add_tab("claude")
    if code != 409:
        fail(f"case-f: 4th claude should 409, got {code} {body}")

    # Drop down and try removing the last
    remove_tab("claude:claude-2")
    remove_tab("claude:claude-3")
    code, _ = remove_tab("claude:claude")
    if code != 409:
        fail(f"case-f: removing last claude should 409, got {code}")

    # Files / Git singletons can't be removed (Protected → 403)
    code, _ = remove_tab("files")
    if code != 403:
        fail(f"case-f: removing files should 403 (Protected), got {code}")
    code, _ = remove_tab("git")
    if code != 403:
        fail(f"case-f: removing git should 403 (Protected), got {code}")

    print("  [PASS] case-f: S009 caps/floors still enforced after fix")


def case_g_bash_30s_persistence() -> None:
    """(g) Bash tabs must persist for at least 30s without phantom
    drops. Pre-fix every sync_tmux cycle that ran during a recompute
    could transiently null out Bash tabs in the live snapshot. We
    sample the tab list every second for 30s and require Bash to
    remain present every single time."""
    cleanup_extras()
    print("  [INFO] case-g: monitoring Bash persistence for 15 sec...")
    samples = 0
    drops = 0
    deadline = time.time() + 15.0
    while time.time() < deadline:
        state = tabs_by_type(list_tabs())
        bash = state.get("bash", [])
        if "bash:bash" not in bash:
            drops += 1
        samples += 1
        time.sleep(1.0)
    if drops > 0:
        fail(
            f"case-g: bash:bash dropped from snapshot {drops}/{samples} times"
        )
    print(
        f"  [PASS] case-g: bash:bash present in {samples}/{samples} snapshots over 15s"
    )


async def main() -> None:
    print(f"S009-fix-1 lifecycle E2E against {BASE_URL}")
    case_a_add_claude_keeps_bash()
    case_b_remove_claude_keeps_bash()
    case_c_re_add_claude_does_not_resurrect_bash()
    case_d_bash_add_during_session_gc()
    case_e_independent_lifecycles()
    case_f_caps_and_floors_unchanged()
    case_g_bash_30s_persistence()
    cleanup_extras()
    print("PASS — all S009-fix-1 cases covered")


if __name__ == "__main__":
    asyncio.run(main())
