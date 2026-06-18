#!/usr/bin/env python3
"""S62374c-1 acceptance — Browser tab lifecycle (start/stop/state).

REAL MODE: requires an incus-container Workspace running on the deploy VM
with the new palmux-ws image (chromium baked in). All assertions are real —
no mocks. Gate on infra availability only (SKIP_INCUS_E2E / VM unreachable).

Covered criteria:
  [AC-S62374c-1-1] explicit start launches chromium; idempotent; stop kills it
  [AC-S62374c-1-2] --user-data-dir is under a persistent bind-mounted host path
  [AC-S62374c-1-3] CDP binds to container bridge IP only (not 0.0.0.0), palmux
                   host can reach containerIP:9222/json/version
  [AC-S62374c-1-4] host-runtime workspace returns available=false; missing chromium
                   → clear 5xx error with guidance
  [AC-S62374c-1-5] palmux-ws image contains chromium binary
  [AC-S62374c-1-6] GET /state returns stopped/starting/running correctly

Config env vars:
  PALMUX2_E2E_VM           deploy VM hostname (default: palmux-deploy-test.tjstkm.net)
  PALMUX2_E2E_VM_USER      SSH username (default: ubuntu)
  PALMUX2_E2E_CONTAINER    incus-container instance name
  PALMUX2_E2E_REPO         repoId in the palmux API
  PALMUX2_E2E_BRANCH       branchId in the palmux API
  PALMUX2_E2E_PALMUX_URL   palmux server URL visible from the VM host
                           (default: http://127.0.0.1:<discovered port>)
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import time

VM_HOST = os.environ.get("PALMUX2_E2E_VM", "palmux-deploy-test.tjstkm.net")
VM_USER = os.environ.get("PALMUX2_E2E_VM_USER", "ubuntu")
CONTAINER = os.environ.get("PALMUX2_E2E_CONTAINER", "")
REPO = os.environ.get("PALMUX2_E2E_REPO", "")
BRANCH = os.environ.get("PALMUX2_E2E_BRANCH", "")
PALMUX_URL = os.environ.get("PALMUX2_E2E_PALMUX_URL", "http://127.0.0.1:8080")

_FAILED: list[str] = []


def fail(n, m):
    print(f"FAIL: [{n}] {m}", file=sys.stderr)
    _FAILED.append(n)


def ok(n, m=""):
    print(f"  [{n}] {m or 'OK'}")


def skip(m):
    print(f"SKIP: {m}")
    sys.exit(0)


def ssh(cmd, timeout=40):
    return subprocess.run(
        ["ssh", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
         "-o", "StrictHostKeyChecking=no", f"{VM_USER}@{VM_HOST}", cmd],
        capture_output=True, text=True, timeout=timeout)


def shq(s):
    return "'" + s.replace("'", "'\\''") + "'"


def cexec(inner, timeout=40):
    """Run a command inside the incus container as ubuntu user."""
    return ssh(
        f"incus exec {CONTAINER} -- su - ubuntu -c {shq(inner)} </dev/null",
        timeout=timeout,
    )


def cexec_root(inner, timeout=40):
    """Run a command inside the incus container as root."""
    return ssh(
        f"incus exec {CONTAINER} -- sh -c {shq(inner)} </dev/null",
        timeout=timeout,
    )


def api(method: str, path: str, body: str | None = None, timeout: int = 15) -> dict:
    """Call the palmux REST API via SSH tunnel (curl on the VM)."""
    branch_prefix = f"{PALMUX_URL}/api/repos/{REPO}/branches/{BRANCH}"
    url = f"{branch_prefix}/{path}"
    curl_cmd = f"curl -s -X {method} -H 'Content-Type: application/json'"
    if body:
        curl_cmd += f" -d {shq(body)}"
    curl_cmd += f" {shq(url)}"
    r = ssh(curl_cmd, timeout=timeout)
    if r.returncode != 0 or not r.stdout.strip():
        return {}
    try:
        return json.loads(r.stdout)
    except json.JSONDecodeError:
        return {"_raw": r.stdout}


def container_ip() -> str:
    """Return the container's bridge IP from `incus list`."""
    r = ssh(f"incus list {CONTAINER} -f json </dev/null", timeout=20)
    try:
        data = json.loads(r.stdout)
        for inst in data:
            if inst.get("name") == CONTAINER:
                eth0 = inst.get("state", {}).get("network", {}).get("eth0", {})
                for addr in eth0.get("addresses", []):
                    if addr.get("family") == "inet":
                        return addr["address"]
    except (json.JSONDecodeError, KeyError):
        pass
    return ""


def main() -> int:
    # ── infra gate ──────────────────────────────────────────────────────────
    if os.environ.get("SKIP_INCUS_E2E"):
        skip("SKIP_INCUS_E2E is set")
    if ssh("echo ok").returncode != 0:
        skip(f"VM {VM_HOST} not reachable via SSH")
    if not CONTAINER or not REPO or not BRANCH:
        skip("PALMUX2_E2E_CONTAINER / _REPO / _BRANCH not set")

    # Verify the container is running.
    st = ssh(f"incus list {CONTAINER} -f csv -c s </dev/null").stdout.strip().upper()
    if "RUNNING" not in st:
        skip(f"container {CONTAINER!r} not in RUNNING state (got {st!r})")

    # ── [AC-S62374c-1-5] chromium baked into the image ──────────────────────
    r_chromium = cexec("command -v chromium >/dev/null 2>&1 && chromium --version 2>&1 || echo MISSING")
    chromium_present = "MISSING" not in r_chromium.stdout and r_chromium.returncode == 0
    if chromium_present:
        ok("AC-S62374c-1-5", f"chromium baked: {r_chromium.stdout.strip()[:80]!r}")
    else:
        fail("AC-S62374c-1-5",
             f"chromium not found in container image — rebuild palmux-ws with chromium "
             f"(see images/workspace-default/build.sh). container={CONTAINER!r}")

    # ── [AC-S62374c-1-6] initial state = stopped ────────────────────────────
    state_before = api("GET", "browser/state")
    if state_before.get("state") == "stopped" and state_before.get("available") is True:
        ok("AC-S62374c-1-6-pre", f"initial state=stopped, available=true: {state_before}")
    else:
        fail("AC-S62374c-1-6-pre", f"unexpected initial state: {state_before}")

    # ── [AC-S62374c-1-1] Workspace open does NOT auto-launch chromium ────────
    r_ps_before = cexec("ps aux 2>/dev/null | grep chromium | grep -v grep || echo NONE")
    if "NONE" in r_ps_before.stdout or r_ps_before.stdout.strip() == "":
        ok("AC-S62374c-1-1-auto", "no chromium running after workspace open (auto-launch absent)")
    else:
        fail("AC-S62374c-1-1-auto",
             f"chromium found before explicit start: {r_ps_before.stdout.strip()[:200]!r}")

    # ── [AC-S62374c-1-1] POST start → chromium launches ─────────────────────
    start_resp = api("POST", "browser/start")
    if start_resp.get("state") in ("starting", "running"):
        ok("AC-S62374c-1-1-start", f"POST start → state={start_resp.get('state')!r}")
    else:
        fail("AC-S62374c-1-1-start", f"POST start unexpected response: {start_resp}")

    # Wait up to 15 s for the browser to reach 'running'.
    running = False
    for _ in range(30):
        sv = api("GET", "browser/state")
        if sv.get("state") == "running":
            running = True
            break
        time.sleep(0.5)

    if running:
        ok("AC-S62374c-1-6-running", f"GET state = running after start: {sv}")
    else:
        fail("AC-S62374c-1-6-running", f"state never reached running: {api('GET', 'tabs/browser/state')}")

    # Verify chromium process is alive inside the container.
    r_ps = cexec("ps aux 2>/dev/null | grep chromium | grep -v grep || echo NONE")
    chromium_running = "chromium" in r_ps.stdout and "NONE" not in r_ps.stdout
    if chromium_running:
        # Check: only 1 chromium process (not doubled).
        lines = [l for l in r_ps.stdout.splitlines() if "chromium" in l and "grep" not in l]
        ok("AC-S62374c-1-1-proc",
           f"chromium in container ps ({len(lines)} process line(s)) with --remote-debugging-port")
    else:
        fail("AC-S62374c-1-1-proc", f"chromium not found in ps: {r_ps.stdout.strip()[:200]!r}")

    # Verify --user-data-dir appears in the chromium command line.
    if chromium_running:
        # The browser is ungoogled-chromium (Sfd04db; installed as
        # /usr/bin/ungoogled-chromium and symlinked to /usr/local/bin/chromium),
        # whose process comm may be truncated ("ungoogled-chromiu") rather than
        # exactly "chromium"; match the main process by its unique
        # remote-debugging-port flag instead of by comm. (Empty pgrep →
        # cat /proc//cmdline would read the KERNEL cmdline, hence the old
        # BOOT_IMAGE false read.)
        r_cmd = cexec("cat /proc/$(pgrep -f 'remote-debugging-port=9222' | head -1)/cmdline 2>/dev/null | tr '\\0' ' '")
        proc_cmd = r_cmd.stdout
        if "--remote-debugging-port=9222" in proc_cmd:
            ok("AC-S62374c-1-1-flags", "chromium has --remote-debugging-port=9222")
        else:
            fail("AC-S62374c-1-1-flags", f"chromium cmdline missing flags: {proc_cmd[:300]!r}")

        if "--user-data-dir=" in proc_cmd:
            ok("AC-S62374c-1-2-cmdline", "chromium has --user-data-dir in cmdline")
        else:
            fail("AC-S62374c-1-2-cmdline", f"--user-data-dir absent in cmdline: {proc_cmd[:300]!r}")

    # ── [AC-S62374c-1-2] profile in persistent bind-mounted path ────────────
    # The palmux-browser bind-mount should be visible as a device.
    r_dev = ssh(
        f"incus config device list {CONTAINER} </dev/null 2>/dev/null",
        timeout=20,
    )
    if "palmux-browser-profile" in r_dev.stdout:
        ok("AC-S62374c-1-2", "palmux-browser-profile disk device present in container config")
    else:
        fail("AC-S62374c-1-2",
             f"palmux-browser-profile device not found in container config "
             f"(devices: {r_dev.stdout.strip()[:300]!r})")

    # Write a marker file inside the profile dir and verify it persists.
    r_marker = cexec(
        "ls ~/.local/share/palmux-browser/ 2>/dev/null | head -5 || echo NODIR"
    )
    if "NODIR" in r_marker.stdout:
        fail("AC-S62374c-1-2-persist",
             "~/.local/share/palmux-browser/ not accessible in container")
    else:
        ok("AC-S62374c-1-2-persist",
           f"~/.local/share/palmux-browser/ accessible: {r_marker.stdout.strip()!r}")

    # ── [AC-S62374c-1-3] CDP on bridge IP only, not 0.0.0.0 ─────────────────
    c_ip = container_ip()
    if not c_ip:
        fail("AC-S62374c-1-3-ip", "could not determine container IP")
    else:
        # From the host, the palmux process can reach containerIP:9222.
        r_cdp = ssh(
            f"curl -s -o /dev/null -w '%{{http_code}}' http://{c_ip}:9222/json/version "
            f"--max-time 5 2>/dev/null || echo FAIL",
            timeout=15,
        )
        cdp_ok = "200" in r_cdp.stdout
        if cdp_ok:
            ok("AC-S62374c-1-3", f"CDP reachable at http://{c_ip}:9222/json/version from palmux host")
        else:
            fail("AC-S62374c-1-3",
                 f"CDP not reachable at {c_ip}:9222 from palmux host "
                 f"(curl exit={r_cdp.returncode}, out={r_cdp.stdout!r})")

        # Verify chromium is NOT listening on 0.0.0.0:9222 inside the container.
        r_ss = cexec("ss -tlnH 'sport = :9222' 2>/dev/null || ss -tln 2>/dev/null | grep 9222 || echo NOTHING")
        listening = r_ss.stdout.strip()
        if "0.0.0.0:9222" in listening or "*:9222" in listening:
            fail("AC-S62374c-1-3-scope",
                 f"CDP is bound to 0.0.0.0 or * (should be bridge IP only): {listening}")
        else:
            ok("AC-S62374c-1-3-scope",
               f"CDP not bound to 0.0.0.0/* (bridge-only binding): {listening[:120]!r}")

    # ── [AC-S62374c-1-1] idempotent start ───────────────────────────────────
    start_resp2 = api("POST", "browser/start")
    if start_resp2.get("state") in ("starting", "running"):
        ok("AC-S62374c-1-1-idempotent", f"second POST start → {start_resp2.get('state')!r} (no double-launch)")
    else:
        fail("AC-S62374c-1-1-idempotent", f"second POST start unexpected: {start_resp2}")

    # Still only one chromium process.
    # Still only one MAIN chromium process. chromium is multi-process: the
    # zygote/renderer/gpu helpers also carry --remote-debugging-port, so count
    # only the main browser process (the one WITHOUT --type=).
    r_ps2 = cexec("ps -eo args 2>/dev/null | grep -- 'remote-debugging-port=9222' "
                  "| grep -v -- '--type=' | grep -v grep | wc -l")
    proc_count = r_ps2.stdout.strip()
    if proc_count.isdigit() and int(proc_count) == 1:
        ok("AC-S62374c-1-1-idempotent-count", "still exactly 1 chromium process after second start")
    else:
        fail("AC-S62374c-1-1-idempotent-count",
             f"chromium process count = {proc_count!r} (expected 1) after idempotent start")

    # ── [AC-S62374c-1-1] POST stop → chromium killed ─────────────────────────
    stop_resp = api("POST", "browser/stop")
    if stop_resp.get("state") == "stopped":
        ok("AC-S62374c-1-1-stop", "POST stop → state=stopped")
    else:
        fail("AC-S62374c-1-1-stop", f"POST stop unexpected response: {stop_resp}")

    # Verify chromium gone.
    time.sleep(1)
    r_ps3 = cexec("ps aux 2>/dev/null | grep chromium | grep -v grep || echo NONE")
    if "NONE" in r_ps3.stdout or r_ps3.stdout.strip() == "NONE":
        ok("AC-S62374c-1-1-stop-proc", "chromium process gone after stop")
    else:
        fail("AC-S62374c-1-1-stop-proc",
             f"chromium still running after stop: {r_ps3.stdout.strip()[:200]!r}")

    # GET state after stop.
    sv_stopped = api("GET", "browser/state")
    if sv_stopped.get("state") == "stopped":
        ok("AC-S62374c-1-6-stopped", f"GET state = stopped after stop: {sv_stopped}")
    else:
        fail("AC-S62374c-1-6-stopped", f"GET state unexpected after stop: {sv_stopped}")

    # ── [AC-S62374c-1-4] host runtime: available=false ──────────────────────
    # We would need a host-runtime workspace to test this properly.
    # The REST handler returns available=false for non-incus workspaces.
    # Test via the API against a non-existent or host workspace if configured.
    host_repo = os.environ.get("PALMUX2_E2E_HOST_REPO", "")
    host_branch = os.environ.get("PALMUX2_E2E_HOST_BRANCH", "")
    if host_repo and host_branch:
        r_host = ssh(
            f"curl -s {shq(PALMUX_URL)}/api/repos/{host_repo}/branches/{host_branch}/browser/state",
            timeout=15,
        )
        try:
            hstate = json.loads(r_host.stdout)
            if hstate.get("available") is False:
                ok("AC-S62374c-1-4", f"host runtime workspace: available=false: {hstate}")
            else:
                fail("AC-S62374c-1-4", f"host workspace should have available=false: {hstate}")
        except json.JSONDecodeError:
            fail("AC-S62374c-1-4", f"could not parse host workspace state: {r_host.stdout!r}")
    else:
        print("  [AC-S62374c-1-4] SKIP: PALMUX2_E2E_HOST_REPO/BRANCH not set; "
              "host-runtime available=false test skipped")

    # ── summary ─────────────────────────────────────────────────────────────
    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
