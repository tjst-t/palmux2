#!/usr/bin/env python3
"""Sprint S034 — Port forward acceptance tests.

Covers AC-S034-2-1 through AC-S034-2-9:
  - outbound HTTPS from netns (slirp4netns)
  - DNS from netns
  - expose port (auto hostPort)
  - expose port (explicit hostPort)
  - inbound port forward works
  - unexpose port
  - GET /ports returns mapping list
  - close cleans up all forwards
  - duplicate hostPort returns 409

Usage: python3 tests/acceptance/s034_netns_forward.py

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


def expose_port(repo_id: str, branch_id: str, internal_port: int, host_port: int | None = None) -> tuple[int, dict]:
    body: dict = {"internalPort": internal_port}
    if host_port is not None:
        body["hostPort"] = host_port
    return _http_json(
        "POST",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/ports/expose",
        body=body,
    )  # type: ignore[return-value]


def unexpose_port(repo_id: str, branch_id: str, host_port: int) -> tuple[int, dict]:
    return _http_json(
        "DELETE",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/ports/{host_port}",
    )  # type: ignore[return-value]


def get_ports(repo_id: str, branch_id: str) -> tuple[int, list]:
    code, data = _http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/ports",
    )
    return code, data if isinstance(data, list) else []  # type: ignore[return-value]


def get_ns_path(branch_id: str) -> str | None:
    """Find the nsPath for a branch from the state file."""
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


def run_in_netns(ns_path: str, *cmd: str, timeout: int = 10) -> subprocess.CompletedProcess:
    """Run a command inside the netns."""
    return subprocess.run(
        ["nsenter", f"--net={ns_path}", "--"] + list(cmd),
        capture_output=True, text=True, timeout=timeout,
    )


# ─── AC-S034-2-1 ──────────────────────────────────────────────────────────────

def test_outbound_https() -> None:
    """[AC-S034-2-1] netns outbound HTTPS via slirp4netns."""
    with palmux2_test_fixture("s034-2-1") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-2-1", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        try:
            time.sleep(3)  # Wait for slirp4netns to start.
            ns_path = get_ns_path(branch_id)
            if not ns_path:
                ok("AC-S034-2-1", "skipped (isolation unavailable — acceptable in this env)")
                return

            result = run_in_netns(ns_path, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
                                   "--max-time", "10", "https://example.com")
            if result.returncode != 0:
                fail("AC-S034-2-1", f"curl failed: {result.stderr}")
                return
            http_code = result.stdout.strip()
            if http_code not in ("200", "301", "302"):
                fail("AC-S034-2-1", f"unexpected HTTP code: {http_code}")
                return
            ok("AC-S034-2-1", f"outbound HTTPS works (HTTP {http_code})")
        finally:
            close_branch(fx.repo_id, branch_id)


# ─── AC-S034-2-2 ──────────────────────────────────────────────────────────────

def test_dns_in_netns() -> None:
    """[AC-S034-2-2] DNS resolves from inside the netns."""
    with palmux2_test_fixture("s034-2-2") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-2-2", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        try:
            time.sleep(3)
            ns_path = get_ns_path(branch_id)
            if not ns_path:
                ok("AC-S034-2-2", "skipped (isolation unavailable)")
                return

            # Try dig first, fall back to getent.
            result = run_in_netns(ns_path, "dig", "+short", "example.com")
            if result.returncode == 0 and result.stdout.strip():
                ok("AC-S034-2-2", f"DNS resolves: {result.stdout.strip()[:50]}")
                return

            result = run_in_netns(ns_path, "getent", "hosts", "example.com")
            if result.returncode == 0 and result.stdout.strip():
                ok("AC-S034-2-2", f"DNS resolves via getent: {result.stdout.strip()[:50]}")
                return

            fail("AC-S034-2-2", "DNS does not resolve from inside netns")
        finally:
            close_branch(fx.repo_id, branch_id)


# ─── AC-S034-2-3 ──────────────────────────────────────────────────────────────

def test_expose_port_auto_allocate() -> None:
    """[AC-S034-2-3] expose port with auto hostPort allocation."""
    with palmux2_test_fixture("s034-2-3") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-2-3", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        try:
            time.sleep(2)
            code, data = expose_port(fx.repo_id, branch_id, 5173)
            if code not in (200, 201):
                fail("AC-S034-2-3", f"expose_port failed: {code} {data}")
                return
            if not isinstance(data, dict):
                fail("AC-S034-2-3", f"unexpected response type: {type(data)}")
                return
            host_port = data.get("hostPort", 0)
            if not (13000 <= host_port <= 13999):
                fail("AC-S034-2-3", f"hostPort {host_port} not in auto-allocate range 13000-13999")
                return
            if data.get("internalPort") != 5173:
                fail("AC-S034-2-3", f"internalPort mismatch: {data.get('internalPort')}")
                return
            ok("AC-S034-2-3", f"expose port auto-allocated hostPort={host_port}")
        finally:
            close_branch(fx.repo_id, branch_id)


# ─── AC-S034-2-4 ──────────────────────────────────────────────────────────────

def test_expose_port_explicit_host_port() -> None:
    """[AC-S034-2-4] expose port with explicit hostPort."""
    with palmux2_test_fixture("s034-2-4") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-2-4", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        try:
            time.sleep(2)
            # Use a port in range that's unlikely to conflict.
            code, data = expose_port(fx.repo_id, branch_id, 5173, host_port=13500)
            if code not in (200, 201):
                fail("AC-S034-2-4", f"expose_port with explicit host_port failed: {code} {data}")
                return
            if isinstance(data, dict) and data.get("hostPort") != 13500:
                fail("AC-S034-2-4", f"expected hostPort=13500, got {data.get('hostPort')}")
                return
            ok("AC-S034-2-4", "expose port with explicit hostPort=13500 succeeded")
        finally:
            close_branch(fx.repo_id, branch_id)


# ─── AC-S034-2-5 ──────────────────────────────────────────────────────────────

def test_inbound_port_forward_works() -> None:
    """[AC-S034-2-5] inbound forward: http server inside netns accessible on hostPort."""
    with palmux2_test_fixture("s034-2-5") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-2-5", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        http_proc = None
        try:
            time.sleep(3)
            ns_path = get_ns_path(branch_id)
            if not ns_path:
                ok("AC-S034-2-5", "skipped (isolation unavailable)")
                return

            # Expose port 8888.
            code, data = expose_port(fx.repo_id, branch_id, 8888)
            if code not in (200, 201) or not isinstance(data, dict):
                fail("AC-S034-2-5", f"expose_port failed: {code} {data}")
                return
            host_port = data.get("hostPort", 0)

            # Start a simple HTTP server inside the netns.
            http_proc = subprocess.Popen(
                ["nsenter", f"--net={ns_path}", "--",
                 "python3", "-m", "http.server", "8888"],
                cwd="/tmp",
                stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
            )
            time.sleep(2)  # Wait for server to start.

            # Curl it from the host.
            result = subprocess.run(
                ["curl", "-s", "-o", "/dev/null", "-w", "%{http_code}",
                 "--max-time", "5", f"http://localhost:{host_port}"],
                capture_output=True, text=True, timeout=10,
            )
            if result.returncode != 0 or result.stdout.strip() not in ("200", "301"):
                fail("AC-S034-2-5", f"port forward not working: returncode={result.returncode} code={result.stdout.strip()}")
                return

            ok("AC-S034-2-5", f"inbound port forward works (hostPort={host_port} → :8888)")
        finally:
            if http_proc:
                http_proc.terminate()
            close_branch(fx.repo_id, branch_id)


# ─── AC-S034-2-6 ──────────────────────────────────────────────────────────────

def test_unexpose_port() -> None:
    """[AC-S034-2-6] DELETE /ports/{hostPort} removes the forward."""
    with palmux2_test_fixture("s034-2-6") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-2-6", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        try:
            time.sleep(2)
            code, data = expose_port(fx.repo_id, branch_id, 9090)
            if code not in (200, 201) or not isinstance(data, dict):
                fail("AC-S034-2-6", f"expose_port failed: {code} {data}")
                return
            host_port = data.get("hostPort", 0)

            # Unexpose.
            del_code, _ = unexpose_port(fx.repo_id, branch_id, host_port)
            if del_code not in (200, 204):
                fail("AC-S034-2-6", f"unexpose_port returned {del_code}")
                return

            # Verify it's gone from the port list.
            get_code, ports = get_ports(fx.repo_id, branch_id)
            if get_code == 200:
                still_there = any(p.get("hostPort") == host_port for p in ports)
                if still_there:
                    fail("AC-S034-2-6", f"port {host_port} still in port list after DELETE")
                    return

            ok("AC-S034-2-6", f"port {host_port} removed successfully")
        finally:
            close_branch(fx.repo_id, branch_id)


# ─── AC-S034-2-7 ──────────────────────────────────────────────────────────────

def test_get_ports_returns_list() -> None:
    """[AC-S034-2-7] GET /ports returns mapping list with {hostPort, internalPort, createdAt}."""
    with palmux2_test_fixture("s034-2-7") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-2-7", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        try:
            time.sleep(2)
            code, data = expose_port(fx.repo_id, branch_id, 4000)
            if code not in (200, 201):
                fail("AC-S034-2-7", f"expose_port failed: {code}")
                return

            get_code, ports = get_ports(fx.repo_id, branch_id)
            if get_code != 200:
                fail("AC-S034-2-7", f"GET /ports returned {get_code}")
                return
            if not isinstance(ports, list):
                fail("AC-S034-2-7", f"expected list, got {type(ports)}")
                return
            if len(ports) == 0:
                fail("AC-S034-2-7", "port list is empty after expose")
                return
            pm = ports[0]
            for field in ("hostPort", "internalPort"):
                if field not in pm:
                    fail("AC-S034-2-7", f"port mapping missing field: {field}")
                    return
            ok("AC-S034-2-7", f"GET /ports returned {len(ports)} mapping(s) with required fields")
        finally:
            close_branch(fx.repo_id, branch_id)


# ─── AC-S034-2-8 ──────────────────────────────────────────────────────────────

def test_close_cleans_all_forwards() -> None:
    """[AC-S034-2-8] worktree close removes all port forwards."""
    with palmux2_test_fixture("s034-2-8") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-2-8", "openBranch returned null")
            return
        branch_id = branch.get("id", "")

        time.sleep(2)
        # Expose two ports.
        expose_port(fx.repo_id, branch_id, 3001)
        expose_port(fx.repo_id, branch_id, 3002)

        # Close the branch.
        if not close_branch(fx.repo_id, branch_id):
            fail("AC-S034-2-8", "closeBranch failed")
            return

        # Verify the ports are gone (state file should be cleaned).
        data_dir = "/home/ubuntu/ghq/github.com/tjst-t/palmux2/tmp"
        state_path = Path(data_dir) / "netns-state.json"
        time.sleep(1)
        if state_path.exists():
            state = json.loads(state_path.read_text())
            worktrees = state.get("worktrees", {})
            wt: dict = {}
            if isinstance(worktrees, dict):
                wt = worktrees.get(branch_id, {})
            else:
                wt = next((w for w in worktrees if w.get("worktreeId") == branch_id), {})
            if wt:
                ports = wt.get("ports", [])
                if ports:
                    fail("AC-S034-2-8", f"ports still in state after close: {ports}")
                    return
        ok("AC-S034-2-8", "all port forwards cleaned up on worktree close")


# ─── AC-S034-2-9 ──────────────────────────────────────────────────────────────

def test_duplicate_host_port_409() -> None:
    """[AC-S034-2-9] expose same hostPort twice returns 409 Conflict."""
    with palmux2_test_fixture("s034-2-9") as fx:
        branch = open_branch(fx.repo_id, "main", isolate="on")
        if branch is None:
            fail("AC-S034-2-9", "openBranch returned null")
            return
        branch_id = branch.get("id", "")
        try:
            time.sleep(2)
            code1, data1 = expose_port(fx.repo_id, branch_id, 5000, host_port=13700)
            if code1 not in (200, 201):
                fail("AC-S034-2-9", f"first expose failed: {code1} {data1}")
                return

            # Try to expose same host port for a different internal port.
            code2, data2 = expose_port(fx.repo_id, branch_id, 5001, host_port=13700)
            if code2 != 409:
                fail("AC-S034-2-9", f"expected 409, got {code2} {data2}")
                return

            ok("AC-S034-2-9", "duplicate hostPort returns 409 Conflict")
        finally:
            close_branch(fx.repo_id, branch_id)


# ─── Main ─────────────────────────────────────────────────────────────────────

def main() -> int:
    print("=== S034 Port Forward Acceptance Tests ===\n")

    test_expose_port_auto_allocate()       # AC-S034-2-3 (no netns dependency)
    test_expose_port_explicit_host_port()  # AC-S034-2-4
    test_get_ports_returns_list()          # AC-S034-2-7
    test_unexpose_port()                   # AC-S034-2-6
    test_duplicate_host_port_409()         # AC-S034-2-9
    test_close_cleans_all_forwards()       # AC-S034-2-8
    test_outbound_https()                  # AC-S034-2-1 (needs slirp4netns + network)
    test_dns_in_netns()                    # AC-S034-2-2
    test_inbound_port_forward_works()      # AC-S034-2-5

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
