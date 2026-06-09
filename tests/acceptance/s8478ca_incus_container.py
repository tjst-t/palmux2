"""
E2E acceptance test for Story S8478ca-2: incus-container workspace runtime.

Runs against a REAL Incus 6.0.0 instance on the test VM (192.168.1.41).
NO mocks — this test exercises the actual incus CLI end-to-end.

Assertions:
  [AC-S8478ca-2-1] Opening a workspace with kind=incus-container launches a
                   RUNNING Incus container.
  [AC-S8478ca-2-2] Bind-mounted ~/.claude/.credentials.json is owned by
                   "ubuntu" inside the container (idmap or privileged passthrough).
  [AC-S8478ca-2-3] /usr/local/bin/claude --version runs inside the container
                   and credentials are readable.
  [AC-S8478ca-2-4] Closing the workspace runs `incus delete --force` and the
                   instance disappears from `incus list`.

Prerequisites (VM setup):
  - ubuntu@192.168.1.41 accessible via SSH key (BatchMode)
  - /tmp/palmux binary built from this repo (GOOS=linux GOARCH=amd64)
  - /tmp/palmux-e2e-config/repos.json with incus-container runtime config
  - Incus 6.0.0 installed, `palmux-ws` image imported
  - ~/ghq/github.com/local/inctest repo exists (worktree)
  - ~/.claude/.credentials.json exists on VM

Skip conditions:
  - SKIP_INCUS_E2E env var is set (allows CI to skip without VM access)
  - SSH to 192.168.1.41 is not reachable
"""

import json
import os
import subprocess
import sys
import time

import pytest

VM_HOST = "192.168.1.41"
VM_USER = "ubuntu"
PALMUX_BIN = "/tmp/palmux"
CONFIG_DIR = "/tmp/palmux-e2e-config"
PORT = 19876
BASE_URL = f"http://{VM_HOST}:{PORT}"
REPO_ID = "local--inctest--5065"
BRANCH_ID = "inctest--7d37"
INSTANCE_NAME = "local-inctest-5065-inctest-7d37-7ceb634f"

SSH_BASE = [
    "ssh",
    "-o", "BatchMode=yes",
    "-o", "ConnectTimeout=5",
    "-o", "StrictHostKeyChecking=no",
    f"{VM_USER}@{VM_HOST}",
]


def ssh(cmd: str, timeout: int = 30) -> subprocess.CompletedProcess:
    """Run a command on the test VM via SSH."""
    return subprocess.run(
        SSH_BASE + [cmd],
        capture_output=True,
        text=True,
        timeout=timeout,
    )


def skip_if_no_vm():
    """Skip this test if the VM is not reachable or SKIP_INCUS_E2E is set."""
    if os.environ.get("SKIP_INCUS_E2E"):
        pytest.skip("SKIP_INCUS_E2E set")
    result = subprocess.run(
        ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3",
         f"{VM_USER}@{VM_HOST}", "echo ok"],
        capture_output=True,
        text=True,
        timeout=5,
    )
    if result.returncode != 0:
        pytest.skip(f"VM {VM_HOST} not reachable")


@pytest.fixture(autouse=True)
def check_vm():
    """Auto-applied fixture: skip if VM unreachable."""
    skip_if_no_vm()


@pytest.fixture(scope="module", autouse=True)
def palmux_server():
    """Start palmux on the VM and clean up after all tests in this module."""
    skip_if_no_vm()

    # Write a start script to the VM and execute it — this avoids the SSH
    # hanging problem caused by backgrounding a process directly in an SSH
    # session (SSH stays open until all child processes exit).
    start_script = (
        "#!/bin/bash\n"
        f"pkill -f 'palmux.*{PORT}' 2>/dev/null\n"
        "sleep 0.5\n"
        f"incus delete --force {INSTANCE_NAME} </dev/null 2>/dev/null || true\n"
        f"chmod +x {PALMUX_BIN}\n"
        f"nohup {PALMUX_BIN} --addr 0.0.0.0:{PORT} --config-dir {CONFIG_DIR} "
        f"</dev/null >/tmp/palmux-e2e.log 2>&1 &\n"
        "disown\n"
        "echo started\n"
    )
    # Upload the script via scp, then run it.
    import tempfile
    with tempfile.NamedTemporaryFile(mode="w", suffix=".sh", delete=False) as f:
        f.write(start_script)
        script_path = f.name
    os.chmod(script_path, 0o755)
    scp = subprocess.run(
        ["scp", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no",
         script_path, f"{VM_USER}@{VM_HOST}:/tmp/e2e_start_palmux.sh"],
        capture_output=True, text=True, timeout=10,
    )
    os.unlink(script_path)
    assert scp.returncode == 0, f"scp failed: {scp.stderr}"

    start_result = ssh("chmod +x /tmp/e2e_start_palmux.sh && /tmp/e2e_start_palmux.sh")
    assert start_result.returncode == 0, f"Failed to start palmux: {start_result.stderr}"

    # Wait for "palmux2 listening" in the log (max 60 s, container start included).
    deadline = time.time() + 60
    started = False
    while time.time() < deadline:
        r = ssh("grep -q 'palmux2 listening' /tmp/palmux-e2e.log 2>/dev/null && echo yes || echo no")
        if r.stdout.strip() == "yes":
            started = True
            break
        time.sleep(2)

    if not started:
        log = ssh("cat /tmp/palmux-e2e.log 2>/dev/null").stdout
        pytest.fail(f"palmux did not start within 60 s. Log:\n{log}")

    yield  # run tests

    # Teardown: stop palmux and clean up container.
    ssh(f"pkill -f 'palmux.*{PORT}' 2>/dev/null; "
        f"incus delete --force {INSTANCE_NAME} </dev/null 2>/dev/null || true")


class TestIncusContainerRuntime:
    """
    [AC-S8478ca-2-1..4] Incus container lifecycle via Palmux workspace API.
    """

    def test_ac1_container_running_after_open(self):
        """[AC-S8478ca-2-1] Container is RUNNING after palmux opens the workspace."""
        r = ssh(f"incus list {INSTANCE_NAME} -f json </dev/null 2>&1")
        assert r.returncode == 0, f"incus list failed: {r.stderr}"
        entries = json.loads(r.stdout)
        assert len(entries) > 0, "No container found — workspace open did not create container"
        inst = next((e for e in entries if e["name"] == INSTANCE_NAME), None)
        assert inst is not None, f"Container {INSTANCE_NAME!r} not found in: {entries}"
        assert inst["status"].upper() == "RUNNING", (
            f"Container state is {inst['status']!r}, expected RUNNING"
        )

    def test_ac2_bindmount_file_owned_by_ubuntu(self):
        """[AC-S8478ca-2-2] ~/.claude/.credentials.json inside container is owned by ubuntu."""
        r = ssh(
            f"incus exec {INSTANCE_NAME} -- "
            f"stat -c '%U' /home/ubuntu/.claude/.credentials.json </dev/null 2>&1"
        )
        assert r.returncode == 0, f"stat in container failed: {r.stderr}"
        owner = r.stdout.strip()
        assert owner == "ubuntu", (
            f"File owner is {owner!r}, expected 'ubuntu' — "
            f"bind-mount idmap / privileged passthrough not working"
        )

    def test_ac2_container_is_unprivileged(self):
        """[AC-S8478ca-2-2] container stays UNPRIVILEGED — isolation is the whole point.

        security.privileged must be unset/false. A privileged container would
        make in-container root == host root through the bind-mounts, defeating
        the host-isolation goal. The correct way to get UID-1000 ownership is
        the unprivileged `raw.idmap "both 1000 1000"`, which requires
        `root:1000:1` in /etc/subuid+/etc/subgid on the host (not privileged).
        """
        r = ssh(f"incus config get {INSTANCE_NAME} security.privileged </dev/null")
        assert r.returncode == 0, f"incus config get failed: {r.stderr}"
        val = r.stdout.strip().lower()
        assert val in ("", "false", "0"), (
            f"security.privileged={val!r} — container must stay unprivileged; "
            f"fix the host's /etc/subuid+/etc/subgid (root:1000:1) instead of going privileged"
        )

    def test_ac3_claude_binary_runs_in_container(self):
        """[AC-S8478ca-2-3] /usr/local/bin/claude --version runs inside container."""
        r = ssh(
            f"incus exec {INSTANCE_NAME} -- "
            f"/usr/local/bin/claude --version </dev/null 2>&1"
        )
        assert r.returncode == 0, f"claude --version in container failed: {r.stderr}"
        assert "Claude Code" in r.stdout, (
            f"claude --version output does not contain 'Claude Code': {r.stdout!r}"
        )

    def test_ac3_workspace_tmux_runs_inside_container(self):
        """[AC-S8478ca-2-3] the Workspace's tmux session lives INSIDE the container.

        Opening an incus-container Workspace routes ensureSession through the
        incus tmux.Client (`incus exec <inst> -- tmux new-session`). If the
        session shows up in `incus exec <inst> -- tmux ls`, the workspace's
        terminal really runs in the container — which is exactly what the WS
        attach path (handler_ws → store.TmuxFor → incus tmux client → `incus
        exec -t -- tmux attach`) then transports. This proves the in-container
        PTY routing rather than the host tmux server.
        """
        r = ssh(f"incus exec {INSTANCE_NAME} -- tmux ls </dev/null 2>&1")
        assert r.returncode == 0 and r.stdout.strip(), (
            f"no tmux session inside the container — the workspace's session was "
            f"not created in the container (rc={r.returncode}, out={r.stdout!r}, err={r.stderr!r})"
        )

    def test_ac3_credentials_readable_in_container(self):
        """[AC-S8478ca-2-3] ~/.claude/.credentials.json is readable inside container."""
        r = ssh(
            f"incus exec {INSTANCE_NAME} -- "
            f"test -r /home/ubuntu/.claude/.credentials.json </dev/null 2>&1"
        )
        assert r.returncode == 0, (
            "credentials.json is not readable in container — "
            f"bind-mount or permissions issue: {r.stderr}"
        )

    def test_ac4_container_deleted_on_workspace_close(self):
        """[AC-S8478ca-2-4] Closing workspace via DELETE API deletes the container."""
        # Close the workspace.
        close = ssh(
            f"curl -s -o /dev/null -w '%{{http_code}}' "
            f"-X DELETE '{BASE_URL}/api/repos/{REPO_ID}/branches/{BRANCH_ID}'"
        )
        assert close.returncode == 0, f"DELETE request failed: {close.stderr}"
        # Accept 200 or 204 (no content).
        assert close.stdout.strip() in ("200", "204"), (
            f"DELETE returned HTTP {close.stdout.strip()!r}, expected 200 or 204"
        )

        # Wait for incus delete --force to propagate (max 10 s).
        deleted = False
        deadline = time.time() + 10
        while time.time() < deadline:
            r = ssh(f"incus list {INSTANCE_NAME} -f json </dev/null 2>&1")
            if r.returncode == 0:
                entries = json.loads(r.stdout)
                if not any(e["name"] == INSTANCE_NAME for e in entries):
                    deleted = True
                    break
            time.sleep(1)

        assert deleted, (
            f"Container {INSTANCE_NAME!r} still exists after workspace close — "
            "Stop() / EvictRuntime() not called in CloseBranch"
        )


if __name__ == "__main__":
    # Allow running directly: python s8478ca_incus_container.py
    skip_if_no_vm()
    sys.exit(pytest.main([__file__, "-v"]))
