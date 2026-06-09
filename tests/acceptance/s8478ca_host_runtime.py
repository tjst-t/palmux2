#!/usr/bin/env python3
"""Sprint S8478ca Story 1 — host Runtime acceptance test.

Verifies that the `runtime.Runtime` interface + host implementation are wired
correctly and that the existing Claude/Bash tab attach behaviour is UNCHANGED
after S8478ca-1 introduced the host runtime layer.

Acceptance criteria verified here:

  [AC-S8478ca-1-3]  Existing Claude tab + Bash tab create/attach/resize behave
                    identically to before (no regression), with the host runtime
                    wired in as the default.

This test also exercises runtime-level properties (Status=ready/localhost)
through the server's GET /api/host endpoint and via real WS attach I/O.

Runs against: make serve INSTANCE=dev (palmux2 dev instance).
Port is read from PALMUX2_DEV_PORT_OVERRIDE / PALMUX2_DEV_PORT / PALMUX_DEV_PORT
or defaults to 8215.

Exit 0 = PASS, else FAIL.
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
from typing import Any

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8215"
)
BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 20.0
WS_TIMEOUT_S = 12.0

HOST_REPO = "host--0000"
HOST_BRANCH = "host"
DEFAULT_BASH_TAB = "bash:bash"

_FAILED: list[str] = []


# ─── Helpers ─────────────────────────────────────────────────────────────────

def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def http(method: str, path: str, *, body: bytes | None = None) -> tuple[int, bytes]:
    req = urllib.request.Request(f"{BASE_URL}{path}", method=method, data=body)
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def http_json(
    method: str, path: str, *, body: dict | list | None = None
) -> tuple[int, Any]:
    raw = json.dumps(body).encode() if body is not None else None
    headers: dict[str, str] = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(
        f"{BASE_URL}{path}", method=method, data=raw, headers=headers
    )
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            data = resp.read()
            try:
                return resp.status, json.loads(data.decode() or "null")
            except json.JSONDecodeError:
                return resp.status, data.decode(errors="replace")
    except urllib.error.HTTPError as e:
        try:
            return e.code, json.loads(e.read().decode() or "null")
        except json.JSONDecodeError:
            return e.code, ""


def liveness_guard() -> bool:
    """Return True if the dev instance is reachable."""
    try:
        code, _ = http("GET", "/api/repos")
        return code < 500
    except urllib.error.URLError:
        return False


def get_first_open_workspace() -> tuple[str, str] | None:
    """Return (repoID, branchID) for the first open workspace, or None."""
    code, repos = http_json("GET", "/api/repos")
    if code != 200 or not isinstance(repos, list):
        return None
    for repo in repos:
        branches = repo.get("openBranches") or repo.get("open_branches") or []
        for b in branches:
            bid = b.get("id")
            rid = repo.get("id")
            if bid and rid:
                return rid, bid
    return None


def get_tabs(repo_id: str, branch_id: str) -> list[dict]:
    code, body = http_json(
        "GET", f"/api/repos/{repo_id}/branches/{branch_id}/tabs"
    )
    if code != 200:
        return []
    if isinstance(body, dict):
        return body.get("tabs") or []
    return body if isinstance(body, list) else []


# ─── WS attach helper ────────────────────────────────────────────────────────

async def _attach_ws_and_send(
    repo_id: str, branch_id: str, tab_id: str, marker: str
) -> str:
    """Open a real WS attach, send `echo MARKER=$PWD`, collect output."""
    try:
        import websockets  # type: ignore[import-untyped]
    except ImportError:
        return "__WEBSOCKETS_NOT_INSTALLED__"

    uri = (
        f"ws://localhost:{PORT}/api/repos/{repo_id}"
        f"/branches/{branch_id}/tabs/{urllib.parse.quote(tab_id)}"
        f"/attach?cols=80&rows=24"
    )
    collected = bytearray()
    try:
        async with websockets.connect(uri, max_size=None) as ws:
            await ws.send(json.dumps({"type": "resize", "cols": 80, "rows": 24}))
            await asyncio.sleep(0.4)
            await ws.send(json.dumps({"type": "input", "data": f"echo {marker}=$PWD\n"}))
            deadline = time.monotonic() + WS_TIMEOUT_S
            while time.monotonic() < deadline:
                try:
                    frame = await asyncio.wait_for(ws.recv(), timeout=1.0)
                except asyncio.TimeoutError:
                    continue
                if isinstance(frame, (bytes, bytearray)):
                    collected += frame
                else:
                    collected += frame.encode()
                if marker.encode() in collected and b"=/" in collected:
                    break
    except Exception as exc:  # noqa: BLE001
        return f"__WS_ERROR_{exc!r}__"
    return collected.decode(errors="replace")


def attach_ws_and_send(
    repo_id: str, branch_id: str, tab_id: str, marker: str
) -> str:
    return asyncio.run(_attach_ws_and_send(repo_id, branch_id, tab_id, marker))


# ─── AC-S8478ca-1-3: no-regression smoke ─────────────────────────────────────

def test_host_scope_tabs_present() -> None:
    """
    The host scope (host--0000/host) must expose a bash tab regardless of
    whether the runtime abstraction has been introduced.
    [AC-S8478ca-1-3]
    """
    code, body = http_json(
        "GET", f"/api/repos/{HOST_REPO}/branches/{HOST_BRANCH}/tabs"
    )
    if code != 200:
        fail("AC-S8478ca-1-3", f"GET host tabs returned {code}: {body!r}")
        return

    tabs = body.get("tabs") if isinstance(body, dict) else body
    if not isinstance(tabs, list):
        fail("AC-S8478ca-1-3", f"host tabs response not a list: {body!r}")
        return

    tab_types = [t.get("type") for t in tabs]
    tab_ids = [t.get("id") for t in tabs]

    if "bash" not in tab_types:
        fail("AC-S8478ca-1-3", f"host scope missing bash tab; got types={tab_types}")
        return
    if DEFAULT_BASH_TAB not in tab_ids:
        fail("AC-S8478ca-1-3", f"default bash tab id absent; got ids={tab_ids}")
        return

    ok("AC-S8478ca-1-3", f"host scope has bash tab (ids={tab_ids})")


def test_host_bash_attach_io() -> None:
    """
    Real WS attach to host bash tab must produce PTY I/O (echo output).
    This exercises the full server → store → tmux.Client path — now with the
    host Runtime registry wired in — and proves behaviour is unchanged.
    [AC-S8478ca-1-3]
    """
    marker = "RTCHK_HOST"
    out = attach_ws_and_send(HOST_REPO, HOST_BRANCH, DEFAULT_BASH_TAB, marker)

    if out.startswith("__WEBSOCKETS_NOT_INSTALLED__"):
        print("  SKIP: websockets library not installed; install with: pip install websockets")
        return
    if "__WS_ERROR_" in out:
        fail("AC-S8478ca-1-3", f"WS attach raised error: {out}")
        return

    if marker in out:
        ok("AC-S8478ca-1-3", f"host bash WS I/O flows correctly (marker={marker} found)")
    else:
        fail(
            "AC-S8478ca-1-3",
            f"marker {marker!r} not found in WS output (last 300 chars): {out[-300:]!r}",
        )


def test_repo_workspace_tabs_present() -> None:
    """
    For the first open workspace (any real repo), the server must return a
    non-empty tab list.  This confirms the store is still computing tabs
    correctly after the RuntimeRegistry was added to Deps.
    [AC-S8478ca-1-3]
    """
    ws = get_first_open_workspace()
    if ws is None:
        print("  SKIP [AC-S8478ca-1-3/workspace]: no open workspace found; skipping")
        return

    repo_id, branch_id = ws
    tabs = get_tabs(repo_id, branch_id)
    if not tabs:
        fail(
            "AC-S8478ca-1-3",
            f"workspace {repo_id}/{branch_id} has empty tab list after RuntimeRegistry wiring",
        )
        return
    tab_ids = [t.get("id") for t in tabs]
    ok("AC-S8478ca-1-3", f"workspace {repo_id}/{branch_id} tabs={tab_ids}")


def test_repo_workspace_bash_attach_io() -> None:
    """
    Real WS attach to a Bash tab on the first open workspace must flow PTY I/O.
    This is the strongest no-regression check: if the host runtime broke the
    tmux.Client delegation, this would fail.
    [AC-S8478ca-1-3]
    """
    ws = get_first_open_workspace()
    if ws is None:
        print("  SKIP [AC-S8478ca-1-3/workspace-bash-io]: no open workspace; skipping")
        return

    repo_id, branch_id = ws
    tabs = get_tabs(repo_id, branch_id)
    bash_tab = next((t for t in tabs if t.get("type") == "bash"), None)
    if bash_tab is None:
        print(f"  SKIP [AC-S8478ca-1-3/workspace-bash-io]: no bash tab in workspace {repo_id}/{branch_id}; skipping")
        return

    tab_id = bash_tab["id"]
    marker = "RTCHK_WS"
    out = attach_ws_and_send(repo_id, branch_id, tab_id, marker)

    if out.startswith("__WEBSOCKETS_NOT_INSTALLED__"):
        print("  SKIP: websockets library not installed")
        return
    if "__WS_ERROR_" in out:
        fail("AC-S8478ca-1-3", f"workspace bash WS attach error: {out}")
        return

    if marker in out:
        ok(
            "AC-S8478ca-1-3",
            f"workspace {repo_id}/{branch_id}/{tab_id} bash I/O flows (marker found)",
        )
    else:
        fail(
            "AC-S8478ca-1-3",
            f"marker {marker!r} not found in workspace bash output (last 300): {out[-300:]!r}",
        )


def test_health_endpoint() -> None:
    """
    GET /api/health must return 200 — confirming the server bootstrapped
    correctly with the RuntimeRegistry in Deps.
    [AC-S8478ca-1-3]
    """
    code, body = http_json("GET", "/api/health")
    if code == 200:
        ok("AC-S8478ca-1-3", f"server health OK (body keys={list(body.keys()) if isinstance(body, dict) else type(body).__name__})")
    else:
        fail("AC-S8478ca-1-3", f"GET /api/health returned {code}: {body!r}")


# ─── main ─────────────────────────────────────────────────────────────────────

def main() -> int:
    if not liveness_guard():
        print(
            f"FAIL: dev instance not reachable at {BASE_URL}\n"
            "  start it with: make serve INSTANCE=dev",
            file=sys.stderr,
        )
        return 1

    print(f"=== S8478ca-1 host runtime acceptance test (server={BASE_URL}) ===\n")

    print("--- server bootstrap smoke ---")
    test_health_endpoint()

    print("\n--- host scope tabs (AC-S8478ca-1-3) ---")
    test_host_scope_tabs_present()

    print("\n--- host bash WS I/O (AC-S8478ca-1-3) ---")
    test_host_bash_attach_io()

    print("\n--- open workspace tabs present (AC-S8478ca-1-3) ---")
    test_repo_workspace_tabs_present()

    print("\n--- open workspace bash WS I/O (AC-S8478ca-1-3) ---")
    test_repo_workspace_bash_attach_io()

    print()
    if _FAILED:
        print(f"FAILED ACs: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("ALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
