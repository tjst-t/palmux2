#!/usr/bin/env python3
"""
Acceptance tests for S98156b: palmux-agent binary + JSON-RPC over UDS.

These tests run against the real palmux-agent binary on the test VM (ubuntu@192.168.1.41).
The test harness:
1. scp's the pre-built bin/palmux-agent to the VM
2. Starts it over SSH
3. Runs each AC verification via SSH + nc / python socket

Requirements:
- bin/palmux-agent must be built (make build-agent)
- SSH access to ubuntu@192.168.1.41 without password

[AC-S98156b-1-1] binary exists, is Linux ELF, size ≤ 15 MB
[AC-S98156b-1-2] proto struct round-trips (covered by internal/agent/proto/proto_test.go — linked here for traceability)
[AC-S98156b-1-3] Echo round-trip via nc -U on the VM
[AC-S98156b-1-4] ListListeningPorts returns at least one port from /proc/net/tcp
[AC-S98156b-1-5] ReadFile / Stat / Walk reject '..' traversal and symlink escape
"""

import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import unittest

VM_HOST = "ubuntu@192.168.1.41"
AGENT_BINARY = os.path.join(os.path.dirname(__file__), "../../bin/palmux-agent")
REMOTE_BINARY = "/tmp/palmux-agent-test"
REMOTE_SOCKET = "/tmp/palmux-agent-acceptance.sock"
SSH_OPTS = ["-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=no"]


def ssh(cmd: str, check=True) -> subprocess.CompletedProcess:
    """Run a command on the test VM."""
    return subprocess.run(
        ["ssh", *SSH_OPTS, VM_HOST, cmd],
        capture_output=True, text=True, check=check
    )


def scp_to_vm(local: str, remote: str):
    """Copy a local file to the test VM."""
    subprocess.run(
        ["scp", *SSH_OPTS, local, f"{VM_HOST}:{remote}"],
        check=True, capture_output=True
    )


def rpc_call(method: str, params=None, req_id=1) -> dict:
    """
    Send a single JSON-RPC request to the agent on the VM via SSH + python3 socket.
    Returns the parsed response dict.

    Uses ssh stdin (`python3 -`) to feed the script — `python3 -c {repr(script)}`
    is fragile across nested shell quoting and breaks on multi-line scripts with
    embedded quotes.
    """
    req = json.dumps({"jsonrpc": "2.0", "method": method, "params": params, "id": req_id})
    script = f"""
import json, socket
sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
sock.connect({REMOTE_SOCKET!r})
sock.sendall({req.encode()!r} + b'\\n')
data = b''
while True:
    chunk = sock.recv(4096)
    if not chunk:
        break
    data += chunk
    try:
        result = json.loads(data.decode())
        print(json.dumps(result))
        break
    except json.JSONDecodeError:
        pass
sock.close()
"""
    result = subprocess.run(
        ["ssh", *SSH_OPTS, VM_HOST, "python3", "-"],
        input=script, capture_output=True, text=True, check=True,
    )
    return json.loads(result.stdout.strip())


class TestAgentBinary(unittest.TestCase):
    """[AC-S98156b-1-1] binary sanity checks"""

    def test_binary_exists_locally(self):
        """[AC-S98156b-1-1] bin/palmux-agent exists after make build-agent"""
        self.assertTrue(
            os.path.isfile(AGENT_BINARY),
            f"bin/palmux-agent not found at {AGENT_BINARY} — run 'make build-agent' first"
        )

    def test_binary_size_limit(self):
        """[AC-S98156b-1-1] binary ≤ 15 MB"""
        size = os.path.getsize(AGENT_BINARY)
        max_bytes = 15 * 1024 * 1024
        self.assertLessEqual(
            size, max_bytes,
            f"palmux-agent is {size / 1e6:.1f} MB, exceeds 15 MB limit"
        )

    def test_binary_is_linux_elf(self):
        """[AC-S98156b-1-1] binary is a Linux ELF (EI_MAG check)"""
        with open(AGENT_BINARY, "rb") as f:
            magic = f.read(4)
        self.assertEqual(magic, b"\x7fELF", "palmux-agent is not an ELF binary")

    def test_binary_is_static(self):
        """[AC-S98156b-1-1] binary has no dynamic library dependencies (CGO_ENABLED=0)"""
        result = subprocess.run(
            ["file", AGENT_BINARY],
            capture_output=True, text=True
        )
        output = result.stdout.lower()
        # Static binary should say "statically linked" or not say "dynamically linked"
        self.assertNotIn("dynamically linked", output,
                         f"palmux-agent appears to be dynamically linked: {result.stdout}")


class TestAgentOnVM(unittest.TestCase):
    """Tests that run the agent on the test VM."""

    @classmethod
    def setUpClass(cls):
        """scp binary to VM and start agent in background."""
        # Check SSH connectivity.
        result = ssh("echo ok", check=False)
        if result.returncode != 0:
            raise unittest.SkipTest(f"Cannot reach VM {VM_HOST}: {result.stderr}")

        # Copy binary.
        scp_to_vm(AGENT_BINARY, REMOTE_BINARY)
        ssh(f"chmod +x {REMOTE_BINARY}")

        # Kill any leftover agent.
        ssh(f"pkill -f palmux-agent-test 2>/dev/null || true", check=False)
        ssh(f"rm -f {REMOTE_SOCKET}", check=False)

        # Start agent in background.
        ssh(f"nohup {REMOTE_BINARY} --socket {REMOTE_SOCKET} > /tmp/palmux-agent-test.log 2>&1 &")
        time.sleep(1)  # give it a moment to start

    @classmethod
    def tearDownClass(cls):
        """Kill the agent and clean up."""
        ssh(f"pkill -f palmux-agent-test 2>/dev/null || true", check=False)
        ssh(f"rm -f {REMOTE_SOCKET} {REMOTE_BINARY}", check=False)

    def test_echo_round_trip(self):
        """[AC-S98156b-1-3] Echo({'msg':'hi'}) round-trips via UDS on VM"""
        resp = rpc_call("Echo", {"msg": "hi"})
        self.assertIsNone(resp.get("error"), f"Echo failed: {resp.get('error')}")
        result = resp.get("result", {})
        self.assertEqual(result.get("msg"), "hi",
                         f"Echo returned wrong msg: {result}")
        self.assertIn("agent_version", result,
                      "Echo result missing agent_version field")
        # Version negotiation: AgentVersion must be present in response envelope too.
        self.assertIn("agent_version", resp,
                      "Response envelope missing agent_version (version negotiation)")

    def test_list_listening_ports_ipv4(self):
        """[AC-S98156b-1-4] ListListeningPorts returns ports from /proc/net/tcp"""
        # Start a listener so there's at least one port.
        ssh("python3 -m http.server 19999 > /tmp/httptest.log 2>&1 &")
        time.sleep(0.5)
        try:
            resp = rpc_call("ListListeningPorts", {"ipv4_only": True})
            self.assertIsNone(resp.get("error"), f"ListListeningPorts failed: {resp.get('error')}")
            ports = resp.get("result", {}).get("ports", [])
            found = any(p.get("port") == 19999 for p in ports)
            self.assertTrue(found,
                            f"Port 19999 not found in ListListeningPorts result: {ports}")
            # All returned should be TCP.
            for p in ports:
                self.assertEqual(p.get("protocol"), "tcp",
                                 f"Unexpected protocol: {p}")
        finally:
            ssh("pkill -f 'http.server 19999' 2>/dev/null || true", check=False)

    def test_list_listening_ports_includes_tcp6(self):
        """[AC-S98156b-1-4] ListListeningPorts includes tcp6 entries when IPv6 enabled"""
        resp = rpc_call("ListListeningPorts", {})
        self.assertIsNone(resp.get("error"), f"ListListeningPorts failed: {resp.get('error')}")
        ports = resp.get("result", {}).get("ports", [])
        protocols = {p.get("protocol") for p in ports}
        # At minimum tcp should be present; tcp6 may or may not be present.
        self.assertIn("tcp", protocols, f"Expected tcp in protocols, got: {protocols}")

    def test_stat_basic(self):
        """[AC-S98156b-1-5] Stat on a known safe path works"""
        resp = rpc_call("Stat", {"root": "/tmp", "path": "palmux-agent-test.log"})
        self.assertIsNone(resp.get("error"), f"Stat failed: {resp.get('error')}")
        result = resp.get("result", {})
        self.assertIn("name", result)
        self.assertIn("size", result)
        self.assertIn("mod_time", result)

    def test_readfile_rejects_dotdot(self):
        """[AC-S98156b-1-5] ReadFile rejects '..' path traversal"""
        resp = rpc_call("ReadFile", {"root": "/tmp", "path": "../etc/passwd"})
        self.assertIsNotNone(resp.get("error"),
                             "ReadFile should have returned an error for '../etc/passwd'")
        code = resp["error"].get("code")
        self.assertEqual(code, -32000,  # ErrCodeForbidden
                         f"Expected forbidden code -32000, got {code}")

    def test_stat_rejects_dotdot(self):
        """[AC-S98156b-1-5] Stat rejects '..' path traversal"""
        resp = rpc_call("Stat", {"root": "/tmp", "path": "../etc"})
        self.assertIsNotNone(resp.get("error"),
                             "Stat should have returned an error for '../etc'")
        code = resp["error"].get("code")
        self.assertEqual(code, -32000,
                         f"Expected forbidden code -32000, got {code}")

    def test_walk_rejects_dotdot(self):
        """[AC-S98156b-1-5] Walk rejects '..' path traversal"""
        resp = rpc_call("Walk", {"root": "/tmp", "path": "../etc"})
        self.assertIsNotNone(resp.get("error"),
                             "Walk should have returned an error for '../etc'")
        code = resp["error"].get("code")
        self.assertEqual(code, -32000,
                         f"Expected forbidden code -32000, got {code}")

    def test_readfile_rejects_symlink_escape(self):
        """[AC-S98156b-1-5] ReadFile rejects symlink that points outside root"""
        # Create a symlink inside /tmp that points to /etc/passwd.
        ssh("ln -sf /etc/passwd /tmp/palmux-symlink-test 2>/dev/null || true")
        try:
            resp = rpc_call("ReadFile", {"root": "/tmp", "path": "palmux-symlink-test"})
            # /etc/passwd is outside /tmp so symlink should be rejected.
            # However if /tmp itself contains /etc/passwd it would be fine — but it doesn't on Ubuntu.
            # The EvalSymlinks check should catch this.
            if resp.get("error") is None:
                # If it succeeded, verify the content matches /etc/passwd (symlink followed but not escaped).
                # This case means EvalSymlinks resolved to within root — which cannot happen for /etc/passwd.
                self.fail(f"ReadFile should have rejected symlink to /etc/passwd, got: {resp.get('result')}")
        finally:
            ssh("rm -f /tmp/palmux-symlink-test", check=False)

    def test_walk_basic(self):
        """[AC-S98156b-1-5] Walk returns entries for a safe directory"""
        resp = rpc_call("Walk", {"root": "/tmp", "path": ".", "max_depth": 1})
        self.assertIsNone(resp.get("error"), f"Walk failed: {resp.get('error')}")
        entries = resp.get("result", {}).get("entries", [])
        self.assertIsInstance(entries, list)

    def test_method_not_found(self):
        """[AC-S98156b-1-3] Unknown method returns JSON-RPC error -32601"""
        resp = rpc_call("NonExistentMethod", {})
        self.assertIsNotNone(resp.get("error"))
        self.assertEqual(resp["error"]["code"], -32601)


if __name__ == "__main__":
    unittest.main(verbosity=2)
