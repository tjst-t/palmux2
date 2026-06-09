"""
E2E acceptance test for S8478ca-refine: in-place runtime switch via header chip.

Runs against a REAL Incus 6.0.0 instance on the test VM (192.168.1.41).
NO mocks — this test exercises the actual PATCH .../runtime endpoint and
verifies the full migration cycle: host→incus, then incus→host.

Assertions:
  [AC-refine-BE-1] Starting state: workspace opens as host; the tmux session
                   exists on the HOST (not inside a container).
  [AC-refine-BE-2] PATCH {kind:"incus-container"} returns {restarted:true}.
  [AC-refine-BE-3] After PATCH, the HOST tmux session for that workspace is
                   gone; an Incus container is RUNNING and UNPRIVILEGED, and
                   the workspace tmux session lives INSIDE the container.
  [AC-refine-BE-4] PATCH {kind:"host"} (switch back) returns {restarted:true};
                   the container is deleted and the session is back on the host.

Prerequisites (VM setup — same as s8478ca_incus_container.py):
  - ubuntu@192.168.1.41 accessible via SSH key
  - /tmp/palmux binary built for linux/amd64 from this repo
  - /tmp/palmux-e2e-config/repos.json with incus-container runtime config
    (same config file as the incus_container test — the repo must be Open)
  - Incus 6.0.0 installed, `palmux-ws` image imported
  - ~/ghq/github.com/local/inctest repo exists on the VM
  - /etc/subuid+/etc/subgid contain root:1000:1
  - Docker stopped (or iptables FORWARD policy=ACCEPT)

Skip conditions:
  - SKIP_INCUS_E2E env var is set
  - SSH to 192.168.1.41 is not reachable

Log output is saved to docs/sprint-logs/S8478ca/run-runtime-switch.log.
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
PORT = 19877  # use a different port from the incus_container tests
BASE_URL = f"http://{VM_HOST}:{PORT}"

# Same repo/branch as the incus_container test.
REPO_ID = "local--inctest--5065"
BRANCH_ID = "inctest--7d37"
INSTANCE_NAME = "local-inctest-5065-inctest-7d37-7ceb634f"
SESSION_NAME = f"_palmux_{REPO_ID}_{BRANCH_ID}"

LOG_PATH = "docs/sprint-logs/S8478ca/run-runtime-switch.log"

SSH_BASE = [
    "ssh",
    "-o", "BatchMode=yes",
    "-o", "ConnectTimeout=5",
    "-o", "StrictHostKeyChecking=no",
    f"{VM_USER}@{VM_HOST}",
]

_log_lines: list[str] = []


def log(msg: str) -> None:
    ts = time.strftime("%H:%M:%S")
    line = f"[{ts}] {msg}"
    print(line)
    _log_lines.append(line)


def flush_log() -> None:
    os.makedirs(os.path.dirname(LOG_PATH), exist_ok=True)
    with open(LOG_PATH, "w") as f:
        f.write("\n".join(_log_lines) + "\n")


def ssh(cmd: str, timeout: int = 30) -> subprocess.CompletedProcess:
    """Run a command on the test VM via SSH."""
    r = subprocess.run(
        SSH_BASE + [cmd],
        capture_output=True,
        text=True,
        timeout=timeout,
    )
    log(f"SSH: {cmd[:80]!r} → rc={r.returncode}")
    return r


def skip_if_no_vm():
    if os.environ.get("SKIP_INCUS_E2E"):
        pytest.skip("SKIP_INCUS_E2E set")
    result = subprocess.run(
        ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=3",
         f"{VM_USER}@{VM_HOST}", "echo ok"],
        capture_output=True, text=True, timeout=5,
    )
    if result.returncode != 0:
        pytest.skip(f"VM {VM_HOST} not reachable")


def http_patch(path: str, body: dict, timeout: int = 60) -> tuple[int, dict]:
    """PATCH via curl on the VM."""
    body_json = json.dumps(body).replace("'", "'\\''")
    cmd = (
        f"curl -s -w '\\n%{{http_code}}' "
        f"-X PATCH "
        f"-H 'Content-Type: application/json' "
        f"-d '{body_json}' "
        f"'{BASE_URL}{path}'"
    )
    r = ssh(cmd, timeout=timeout)
    if r.returncode != 0:
        return -1, {}
    lines = r.stdout.strip().split("\n")
    status_line = lines[-1].strip()
    body_text = "\n".join(lines[:-1])
    try:
        status = int(status_line)
    except ValueError:
        return -1, {}
    try:
        resp = json.loads(body_text) if body_text else {}
    except json.JSONDecodeError:
        resp = {}
    log(f"PATCH {path} → {status} {resp}")
    return status, resp


@pytest.fixture(autouse=True)
def check_vm():
    skip_if_no_vm()


@pytest.fixture(scope="module", autouse=True)
def palmux_server():
    """Start palmux on the VM (HOST runtime) and clean up after tests."""
    skip_if_no_vm()

    # Clean up any stale state.
    log("Cleaning up stale state…")
    ssh(f"pkill -f 'palmux.*{PORT}' 2>/dev/null || true")
    time.sleep(0.5)
    ssh(f"incus delete --force {INSTANCE_NAME} </dev/null 2>/dev/null || true")
    # Ensure the repo is open as HOST (not incus) at startup.
    # We write a fresh config that opens the workspace with kind=host.
    # repos.json is a TOP-LEVEL ARRAY of RepoEntry; per-Workspace runtime lives
    # under branchSettings.<branchID>.runtime (Story -3 schema).
    config_json = json.dumps([{
        "id": REPO_ID,
        "ghqPath": "github.com/local/inctest",
        "branchSettings": {
            BRANCH_ID: {"runtime": {"kind": "host"}}
        }
    }])
    ssh(f"mkdir -p {CONFIG_DIR}")
    # write via stdin to avoid shell-quoting issues with the JSON
    import base64 as _b64
    enc = _b64.b64encode(config_json.encode()).decode()
    ssh(f"echo '{enc}' | base64 -d > {CONFIG_DIR}/repos.json")

    start_script = (
        "#!/bin/bash\n"
        f"pkill -f 'palmux.*{PORT}' 2>/dev/null || true\n"
        "sleep 0.3\n"
        f"chmod +x {PALMUX_BIN}\n"
        f"nohup {PALMUX_BIN} --addr 0.0.0.0:{PORT} --config-dir {CONFIG_DIR} "
        f"</dev/null >/tmp/palmux-switch.log 2>&1 &\n"
        "disown\n"
        "echo started\n"
    )
    import tempfile
    with tempfile.NamedTemporaryFile(mode="w", suffix=".sh", delete=False) as f:
        f.write(start_script)
        script_path = f.name
    os.chmod(script_path, 0o755)
    scp_r = subprocess.run(
        ["scp", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=no",
         script_path, f"{VM_USER}@{VM_HOST}:/tmp/e2e_start_switch.sh"],
        capture_output=True, text=True, timeout=10,
    )
    os.unlink(script_path)
    assert scp_r.returncode == 0, f"scp failed: {scp_r.stderr}"

    run_r = ssh("chmod +x /tmp/e2e_start_switch.sh && /tmp/e2e_start_switch.sh")
    assert run_r.returncode == 0, f"start script failed: {run_r.stderr}"

    # Wait up to 30 s for the server to be listening.
    deadline = time.time() + 30
    started = False
    while time.time() < deadline:
        r = ssh(f"curl -s -o /dev/null -w '%{{http_code}}' '{BASE_URL}/api/health' 2>/dev/null")
        if r.stdout.strip() == "200":
            started = True
            break
        time.sleep(2)

    if not started:
        server_log = ssh("cat /tmp/palmux-switch.log 2>/dev/null").stdout
        log(f"Server log:\n{server_log}")
        pytest.fail(f"palmux did not start within 30 s")

    # Wait for the workspace to open (SyncTmux creates the session within 5 s).
    deadline = time.time() + 20
    while time.time() < deadline:
        r = ssh(f"tmux has-session -t {SESSION_NAME} 2>/dev/null && echo yes || echo no")
        if r.stdout.strip() == "yes":
            log("Workspace session ready on host")
            break
        time.sleep(2)

    log("palmux started, workspace open")
    yield

    # Teardown.
    log("Tearing down…")
    ssh(f"pkill -f 'palmux.*{PORT}' 2>/dev/null || true")
    ssh(f"incus delete --force {INSTANCE_NAME} </dev/null 2>/dev/null || true")
    flush_log()


class TestRuntimeSwitch:
    """[AC-refine-BE-*] In-place host↔incus-container switch."""

    def test_ac_refine_be1_initial_host_session(self):
        """[AC-refine-BE-1] Workspace starts as host: tmux session on host, no container."""
        log("Test BE-1: initial host session")
        # Verify session exists on the HOST tmux server.
        r = ssh(f"tmux has-session -t {SESSION_NAME} 2>/dev/null && echo yes || echo no")
        assert r.stdout.strip() == "yes", (
            f"expected session {SESSION_NAME!r} on host tmux, not found"
        )
        # Verify NO container is running.
        r2 = ssh(f"incus list {INSTANCE_NAME} -f json </dev/null 2>&1")
        if r2.returncode == 0:
            entries = json.loads(r2.stdout)
            running = [e for e in entries if e["name"] == INSTANCE_NAME
                       and e["status"].upper() == "RUNNING"]
            assert not running, (
                f"Container already running before switch: {running}"
            )
        log("BE-1 OK: host session present, no container")

    def test_ac_refine_be2_patch_returns_restarted(self):
        """[AC-refine-BE-2] PATCH {kind:incus-container} returns restarted:true."""
        log("Test BE-2: PATCH host→incus, expect restarted:true")
        status, resp = http_patch(
            f"/api/repos/{REPO_ID}/branches/{BRANCH_ID}/runtime",
            {"kind": "incus-container"},
            timeout=90,
        )
        assert status == 200, f"PATCH returned HTTP {status}: {resp}"
        assert resp.get("ok") is True, f"ok!=true in response: {resp}"
        assert resp.get("restarted") is True, (
            f"restarted!=true in response: {resp}\n"
            "The workspace was open but the server did not perform an in-place restart."
        )
        log(f"BE-2 OK: PATCH returned restarted=True, runtime={resp.get('runtime')!r}")

    def test_ac_refine_be3_host_session_gone_container_running(self):
        """[AC-refine-BE-3] After switch: host session gone, container running w/ in-container session."""
        log("Test BE-3: verify host session gone, container running")
        # (a) HOST session must be gone.
        r = ssh(f"tmux has-session -t {SESSION_NAME} 2>/dev/null && echo yes || echo no")
        assert r.stdout.strip() == "no", (
            f"Expected HOST session {SESSION_NAME!r} to be killed after switch to incus; "
            f"it is still present."
        )
        log("BE-3a: host session gone ✓")

        # (b) Container must be RUNNING.
        deadline = time.time() + 30
        container_running = False
        while time.time() < deadline:
            r2 = ssh(f"incus list {INSTANCE_NAME} -f json </dev/null 2>&1")
            if r2.returncode == 0:
                entries = json.loads(r2.stdout)
                inst = next((e for e in entries if e["name"] == INSTANCE_NAME), None)
                if inst and inst["status"].upper() == "RUNNING":
                    container_running = True
                    break
            time.sleep(2)
        assert container_running, (
            f"Container {INSTANCE_NAME!r} is not RUNNING after switch. "
            "Check palmux logs: /tmp/palmux-switch.log"
        )
        log("BE-3b: container RUNNING ✓")

        # (c) Container must be UNPRIVILEGED.
        r3 = ssh(f"incus config get {INSTANCE_NAME} security.privileged </dev/null 2>&1")
        assert r3.returncode == 0
        val = r3.stdout.strip().lower()
        assert val in ("", "false", "0"), (
            f"Container is privileged (security.privileged={val!r}); "
            "must be unprivileged — check host /etc/subuid+/etc/subgid (root:1000:1)"
        )
        log("BE-3c: container unprivileged ✓")

        # (d) Workspace tmux session must exist INSIDE the container.
        deadline2 = time.time() + 30
        in_container = False
        while time.time() < deadline2:
            r4 = ssh(f"incus exec {INSTANCE_NAME} -- tmux ls </dev/null 2>&1")
            if r4.returncode == 0 and r4.stdout.strip():
                in_container = True
                break
            time.sleep(3)
        assert in_container, (
            f"No tmux session inside container {INSTANCE_NAME!r} — "
            f"ensureSession may have run against the host tmux instead of incus. "
            f"Container tmux output: {r4.stdout!r}"
        )
        log(f"BE-3d: in-container tmux sessions: {r4.stdout.strip()!r} ✓")

    def test_ac_refine_be4_switch_back_to_host(self):
        """[AC-refine-BE-4] PATCH {kind:host} deletes container, session back on host."""
        log("Test BE-4: switch back to host")
        status, resp = http_patch(
            f"/api/repos/{REPO_ID}/branches/{BRANCH_ID}/runtime",
            {"kind": "host"},
            timeout=60,
        )
        assert status == 200, f"PATCH returned HTTP {status}: {resp}"
        assert resp.get("restarted") is True, f"restarted!=true on switch-back: {resp}"
        log(f"BE-4a: PATCH incus→host returned restarted=True ✓")

        # Container must be deleted.
        deadline = time.time() + 30
        container_gone = False
        while time.time() < deadline:
            r = ssh(f"incus list {INSTANCE_NAME} -f json </dev/null 2>&1")
            if r.returncode == 0:
                entries = json.loads(r.stdout)
                if not any(e["name"] == INSTANCE_NAME for e in entries):
                    container_gone = True
                    break
            time.sleep(2)
        assert container_gone, (
            f"Container {INSTANCE_NAME!r} still exists after switch back to host"
        )
        log("BE-4b: container deleted ✓")

        # Session back on the HOST tmux server.
        deadline2 = time.time() + 30
        host_back = False
        while time.time() < deadline2:
            r2 = ssh(f"tmux has-session -t {SESSION_NAME} 2>/dev/null && echo yes || echo no")
            if r2.stdout.strip() == "yes":
                host_back = True
                break
            time.sleep(2)
        assert host_back, (
            f"HOST session {SESSION_NAME!r} not found after switch back to host"
        )
        log("BE-4c: host session recreated ✓")


if __name__ == "__main__":
    skip_if_no_vm()
    sys.exit(pytest.main([__file__, "-v", "--tb=short"]))
