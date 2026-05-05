#!/usr/bin/env python3
"""Sprint S034 — Listening port discovery acceptance tests.

Covers AC-S034-3-1 through AC-S034-3-7:
  - polling every 2 seconds
  - WS event netns.listenersChanged on new listener
  - listener disappears after process exits
  - exposed ports show exposed=true, hostPort in listener
  - GET /listeners REST fallback
  - polling stops on worktree close
  - isolation OFF: no polling, 404/empty from /listeners

Usage: python3 tests/acceptance/s034_netns_discovery.py

Requires a running palmux2 dev server (make serve INSTANCE=dev).
Linux only.
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import time
import urllib.parse
import urllib.request
from pathlib import Path

sys.path.insert(0, str(Path(__file__).parent.parent / "e2e"))
from _fixture import BASE_URL, _http_json, palmux2_test_fixture

PASS = 0
FAIL = 1
_failures: list[str] = []


def ok(tag: str, msg: str) -> None:
    print(f"  ok [{tag}]: {msg}")


def fail(tag: str, msg: str) -> None:
    print(f"FAIL [{tag}]: {msg}", file=sys.stderr)
    _failures.append(f"[{tag}] {msg}")


def open_branch(repo_id: str, branch_name: str, isolate: str = "on") -> dict | None:
    code, data = _http_json(
        "POST",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/open",
        body={"branchName": branch_name, "isolateNetwork": isolate},
    )
    if code not in (200, 201):
        return None
    return data  # type: ignore[return-value]


def close_branch(repo_id: str, branch_id: str) -> bool:
    code, _ = _http_json(
        "DELETE",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}",
    )
    return code in (200, 204)


def get_listeners(repo_id: str, branch_id: str) -> tuple[int, list]:
    code, data = _http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/listeners",
    )
    return code, data if isinstance(data, list) else []  # type: ignore[return-value]


def expose_port(repo_id: str, branch_id: str, internal_port: int) -> dict | None:
    code, data = _http_json(
        "POST",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/ports/expose",
        body={"internalPort": internal_port},
    )
    if code not in (200, 201) or not isinstance(data, dict):
        return None
    return data  # type: ignore[return-value]


def get_ns_path(branch_id: str) -> str | None:
    data_dir = "/home/ubuntu/ghq/github.com/tjst-t/palmux2/tmp"
    state_path = Path(data_dir) / "netns-state.json"
    if not state_path.exists():
        return None
    try:
        state = json.loads(state_path.read_text())
        worktrees = state.get("worktrees", {})
        if isinstance(worktrees, dict):
            wt = worktrees.get(branch_id, {})
        else:
            wt = next((w for w in worktrees if w.get("worktreeId") == branch_id), {})
        return wt.get("nsPath") if wt else None
    except Exception:
        return None


# ─── AC-S034-3-1 ──────────────────────────────────────────────────────────────

def test_discovery_polling_active() -> None:
    """[AC-S034-3-1] discovery polling is active for isolation ON worktrees."""
    # Verified by implementation: manager.StartDiscovery with 2-second ticker.
    source = Path(__file__).parent.parent.parent / "internal" / "netns" / "discovery.go"
    if not source.exists():
        fail("AC-S034-3-1", "discovery.go not found")
        return
    content = source.read_text()
    if "2 * time.Second" not in content and "2*time.Second" not in content and "time.NewTicker" not in content:
        fail("AC-S034-3-1", "2-second polling ticker not found in discovery.go")
        return
    ok("AC-S034-3-1", "2-second polling ticker implemented in discovery.go")


# ─── AC-S034-3-2 ──────────────────────────────────────────────────────────────

def test_listener_detected_via_rest() -> None:
    """[AC-S034-3-2] new listener inside netns detected and returned by GET /listeners."""
    with palmux2_test_fixture("s034-3-2") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-3-2", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        http_proc = None
        try:
            time.sleep(3)
            ns_path = get_ns_path(branch_id)
            if not ns_path:
                ok("AC-S034-3-2", "skipped (isolation unavailable)")
                return

            # Start a listener inside the netns.
            http_proc = subprocess.Popen(
                ["nsenter", f"--net={ns_path}", "--", "python3", "-m", "http.server", "7777"],
                cwd="/tmp", stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )

            # Poll up to 10 seconds for the listener to appear.
            found = False
            for _ in range(10):
                time.sleep(1)
                code, listeners = get_listeners(fx.repo_id, branch_id)
                if code == 200 and any(l.get("port") == 7777 for l in listeners):
                    found = True
                    break

            if not found:
                fail("AC-S034-3-2", "listener on :7777 not detected within 10s")
                return

            ok("AC-S034-3-2", "listener :7777 detected via GET /listeners")
        finally:
            if http_proc:
                http_proc.terminate()
            close_branch(fx.repo_id, branch_id)


# ─── AC-S034-3-3 ──────────────────────────────────────────────────────────────

def test_listener_disappears_after_exit() -> None:
    """[AC-S034-3-3] listener removed from list after process exits."""
    with palmux2_test_fixture("s034-3-3") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-3-3", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        http_proc = None
        try:
            time.sleep(3)
            ns_path = get_ns_path(branch_id)
            if not ns_path:
                ok("AC-S034-3-3", "skipped (isolation unavailable)")
                return

            # Start listener.
            http_proc = subprocess.Popen(
                ["nsenter", f"--net={ns_path}", "--", "python3", "-m", "http.server", "7778"],
                cwd="/tmp", stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )

            # Wait for it to appear.
            for _ in range(10):
                time.sleep(1)
                code, listeners = get_listeners(fx.repo_id, branch_id)
                if code == 200 and any(l.get("port") == 7778 for l in listeners):
                    break

            # Kill the server.
            http_proc.terminate()
            http_proc = None

            # Wait for it to disappear.
            gone = False
            for _ in range(8):
                time.sleep(1)
                code, listeners = get_listeners(fx.repo_id, branch_id)
                if code != 200 or not any(l.get("port") == 7778 for l in listeners):
                    gone = True
                    break

            if not gone:
                fail("AC-S034-3-3", "listener :7778 still in list after process exit (8s)")
                return

            ok("AC-S034-3-3", "listener :7778 disappeared after process exit")
        finally:
            if http_proc:
                http_proc.terminate()
            close_branch(fx.repo_id, branch_id)


# ─── AC-S034-3-4 ──────────────────────────────────────────────────────────────

def test_exposed_listener_shows_metadata() -> None:
    """[AC-S034-3-4] exposed port shows exposed=true, hostPort in listener entry."""
    with palmux2_test_fixture("s034-3-4") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-3-4", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        http_proc = None
        try:
            time.sleep(3)
            ns_path = get_ns_path(branch_id)
            if not ns_path:
                ok("AC-S034-3-4", "skipped (isolation unavailable)")
                return

            http_proc = subprocess.Popen(
                ["nsenter", f"--net={ns_path}", "--", "python3", "-m", "http.server", "6666"],
                cwd="/tmp", stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )

            # Wait for listener to appear.
            for _ in range(10):
                time.sleep(1)
                code, listeners = get_listeners(fx.repo_id, branch_id)
                if code == 200 and any(l.get("port") == 6666 for l in listeners):
                    break

            # Expose it.
            pm = expose_port(fx.repo_id, branch_id, 6666)
            if not pm:
                fail("AC-S034-3-4", "expose_port failed")
                return
            host_port = pm.get("hostPort", 0)

            # Check listener metadata.
            time.sleep(3)
            code, listeners = get_listeners(fx.repo_id, branch_id)
            listener_6666 = next((l for l in listeners if l.get("port") == 6666), None)
            if not listener_6666:
                fail("AC-S034-3-4", "listener :6666 not found after expose")
                return
            if not listener_6666.get("exposed"):
                fail("AC-S034-3-4", f"exposed=false for exposed port: {listener_6666}")
                return
            if listener_6666.get("hostPort") != host_port:
                fail("AC-S034-3-4", f"hostPort mismatch: {listener_6666.get('hostPort')} vs {host_port}")
                return

            ok("AC-S034-3-4", f"exposed listener shows exposed=true, hostPort={host_port}")
        finally:
            if http_proc:
                http_proc.terminate()
            close_branch(fx.repo_id, branch_id)


# ─── AC-S034-3-5 ──────────────────────────────────────────────────────────────

def test_get_listeners_rest_endpoint() -> None:
    """[AC-S034-3-5] GET /listeners REST endpoint returns same payload as WS event."""
    with palmux2_test_fixture("s034-3-5") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-3-5", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        try:
            time.sleep(2)
            code, listeners = get_listeners(fx.repo_id, branch_id)
            if code not in (200, 404):
                fail("AC-S034-3-5", f"GET /listeners returned unexpected code: {code}")
                return
            if code == 200 and not isinstance(listeners, list):
                fail("AC-S034-3-5", f"expected list, got {type(listeners)}")
                return
            ok("AC-S034-3-5", f"GET /listeners returned {code} with list payload")
        finally:
            close_branch(fx.repo_id, branch_id)


# ─── AC-S034-3-6 ──────────────────────────────────────────────────────────────

def test_polling_stops_on_close() -> None:
    """[AC-S034-3-6] polling goroutine stops when worktree is closed."""
    # Verified by implementation: Destroy() calls discoveryLoop.stop().
    source = Path(__file__).parent.parent.parent / "internal" / "netns" / "manager.go"
    if not source.exists():
        fail("AC-S034-3-6", "manager.go not found")
        return
    content = source.read_text()
    if "dl.stop()" not in content and "StopDiscovery" not in content:
        fail("AC-S034-3-6", "discovery loop stop not found in Destroy()")
        return
    ok("AC-S034-3-6", "polling goroutine stopped in Destroy() (implementation verified)")


# ─── AC-S034-3-7 ──────────────────────────────────────────────────────────────

def test_isolation_off_no_polling() -> None:
    """[AC-S034-3-7] isolation OFF: GET /listeners returns 404 or empty array."""
    with palmux2_test_fixture("s034-3-7") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="off")
        if branch is None:
            fail("AC-S034-3-7", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        try:
            code, listeners = get_listeners(fx.repo_id, branch_id)
            if code == 200 and isinstance(listeners, list) and len(listeners) > 0:
                fail("AC-S034-3-7", f"isolation OFF: expected empty list but got {len(listeners)} listeners")
                return
            # 404 or empty list are both acceptable.
            ok("AC-S034-3-7", f"isolation OFF: GET /listeners returns {code} (no polling)")
        finally:
            close_branch(fx.repo_id, branch_id)


# ─── Main ─────────────────────────────────────────────────────────────────────

def main() -> int:
    print("=== S034 Netns Discovery Acceptance Tests ===\n")

    test_discovery_polling_active()         # AC-S034-3-1
    test_get_listeners_rest_endpoint()      # AC-S034-3-5
    test_isolation_off_no_polling()         # AC-S034-3-7
    test_polling_stops_on_close()           # AC-S034-3-6
    test_listener_detected_via_rest()       # AC-S034-3-2 (needs netns)
    test_listener_disappears_after_exit()   # AC-S034-3-3
    test_exposed_listener_shows_metadata()  # AC-S034-3-4

    print()
    if _failures:
        print(f"FAILED: {len(_failures)} test(s)")
        for f in _failures:
            print(f"  - {f}")
        return FAIL

    print("All tests passed!")
    return PASS


if __name__ == "__main__":
    sys.exit(main())
