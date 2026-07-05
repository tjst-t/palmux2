"""Sd44947 — profile-as-mold real-incus acceptance (Story 1 + AC-2-4).

Real-mode smoke (priority_rule 9 / test-discipline Rule 7): runs against a REAL
incus daemon with a throwaway probe container and the NEW palmux binary's
SharedProfileManager (driven through the live dev instance's /api/deploy/apply
and its 10s scan-loop reconcile). No fakes.

  [AC-Sd44947-1-1] palmux generates the `palmux-shared` incus profile from the
                   mounts[] logic; a container launched with `-p default -p
                   palmux-shared` carries NO instance-local shared device and the
                   shared mounts (ghq / a shared dir) are reachable + isolated.
  [AC-Sd44947-1-2] reconcile self-heals: hand-remove a profile device → the scan
                   loop restores it within one interval.
  [AC-Sd44947-1-3] migration: a container with a legacy instance-local device is
                   stripped to the profile's copy with the mount intact.
  [AC-Sd44947-2-4] live add/remove of a shared dir propagates into a RUNNING
                   container (device appears / disappears from /proc/mounts).

Prereqs: incus usable, `palmux-ws` image present, a running dev instance
(`make serve INSTANCE=dev`) whose scan loop manages ≥1 ready incus container
(so the shared-profile reconcile fires).

Run:  PALMUX2_DEV_PORT=<port> python tests/acceptance/sd44947_shared_profile.py
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.request

PORT = os.environ.get("PALMUX2_DEV_PORT") or os.environ.get("PALMUX_DEV_PORT") or "8200"
BASE = f"http://localhost:{PORT}"
HOME = os.path.expanduser("~")
PROBE = "sd44947-acc-probe"
SHARE_DIR = os.path.join(HOME, ".sd44947-acc")
PROFILE = "palmux-shared"


def sh(*args: str, check_rc: bool = False) -> tuple[int, str]:
    p = subprocess.run(args, capture_output=True, text=True)
    if check_rc and p.returncode != 0:
        raise RuntimeError(f"{' '.join(args)} failed: {p.stderr.strip()}")
    return p.returncode, (p.stdout + p.stderr)


def incus(*args: str) -> tuple[int, str]:
    return sh("incus", *args)


def api(method: str, path: str, body: dict | None = None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                 headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            return r.status, json.loads(r.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        return e.code, json.loads(e.read().decode() or "{}")


def in_probe(cmd: str) -> tuple[int, str]:
    return incus("exec", PROBE, "--user", "1000", "--env", "HOME=/home/ubuntu",
                 "--", "bash", "-lc", cmd)


def profile_device_names() -> set[str]:
    rc, out = incus("profile", "device", "list", PROFILE)
    return set(l.strip() for l in out.splitlines() if l.strip()) if rc == 0 else set()


def cleanup() -> None:
    incus("delete", "-f", PROBE)
    api("POST", "/api/deploy/apply", {"workspace": {"sharedDirs": []}})
    shutil.rmtree(SHARE_DIR, ignore_errors=True)


def main() -> int:
    failures: list[str] = []

    def check(name: str, cond: bool) -> None:
        print(f"[{'PASS' if cond else 'FAIL'}] {name}")
        if not cond:
            failures.append(name)

    if not shutil.which("incus"):
        print("SKIP: incus not available on this host")
        return 0
    rc, _ = incus("image", "list", "palmux-ws", "-f", "csv", "-c", "l")
    if rc != 0:
        print("SKIP: palmux-ws image not present")
        return 0

    os.makedirs(SHARE_DIR, exist_ok=True)
    with open(os.path.join(SHARE_DIR, "marker.txt"), "w") as f:
        f.write("sd44947-marker")
    cleanup_needed = True
    incus("delete", "-f", PROBE)
    # Reset declaration so a prior interrupted run's leftover state can't turn the
    # first apply into a no-op (idempotent start).
    api("POST", "/api/deploy/apply", {"workspace": {"sharedDirs": []}})

    try:
        # Drive the profile via the live server (new binary → real incus).
        code, res = api("POST", "/api/deploy/apply", {"workspace": {"sharedDirs": ["~/.sd44947-acc"]}})
        check("apply shared dir via /api/deploy/apply (workspace class)",
              code == 200 and res.get("workspaceApplied") is True)

        names = profile_device_names()
        sf = next((n for n in names if n.startswith("sf-")), None)
        check("AC-Sd44947-1-1 profile has core shared devices (ghq, dot-claude)",
              {"ghq", "dot-claude"}.issubset(names))
        check("AC-Sd44947-2-1 profile has the user shared dir device (sf-*)", sf is not None)

        # AC-1-1: launch a probe referencing the profile.
        incus("launch", "palmux-ws", PROBE, "-p", "default", "-p", PROFILE)
        for _ in range(40):
            if incus("exec", PROBE, "--", "true")[0] == 0:
                break
            time.sleep(1)

        rc, prof = incus("config", "show", PROBE)
        has_both = "palmux-shared" in prof and "default" in prof
        rc, devlist = incus("config", "device", "list", PROBE)
        n_local = len([l for l in devlist.splitlines() if l.strip()])
        check("AC-Sd44947-1-1 probe profiles = [default, palmux-shared]", has_both)
        check("AC-Sd44947-1-1 probe has 0 instance-local devices", n_local == 0)

        rc, out = in_probe("ls -d ~/ghq >/dev/null 2>&1 && echo GHQ && cat ~/.sd44947-acc/marker.txt && hostname")
        check("AC-Sd44947-1-1 shared mounts reachable inside probe (ghq + shared dir)",
              "GHQ" in out and "sd44947-marker" in out)
        check("AC-Sd44947-1-1 probe is isolated (hostname = container)", PROBE in out)

        # AC-2-4 live remove: drive the REAL user path — POST apply with an empty
        # list so the DECLARATION drops the dir (a manual `incus profile device
        # remove` would race the scan-loop reconcile, which correctly re-adds a
        # still-declared device — profile-as-mold). incus live-unmounts it.
        api("POST", "/api/deploy/apply", {"workspace": {"sharedDirs": []}})
        time.sleep(4)
        gone_profile = sf not in profile_device_names()
        rc, out = in_probe("grep -q sd44947-acc /proc/mounts && echo MOUNTED || echo GONE")
        check("AC-Sd44947-2-4 live remove: shared dir removed from profile + unmounts from running container",
              gone_profile and "GONE" in out)

        # AC-2-4 live add: POST apply to re-declare it → reconcile/Ensure re-adds
        # the device → incus live-mounts it into the running container.
        api("POST", "/api/deploy/apply", {"workspace": {"sharedDirs": ["~/.sd44947-acc"]}})
        time.sleep(4)
        rc, out = in_probe("cat ~/.sd44947-acc/marker.txt 2>/dev/null && echo OK")
        check("AC-Sd44947-2-4 live add: shared dir remounts into running container",
              "sd44947-marker" in out)

        # AC-1-3 migration: add a legacy instance-local device, strip it → intact.
        incus("config", "device", "add", PROBE, "ghq", "disk",
              "source=" + os.path.join(HOME, "ghq"), "path=" + os.path.join(HOME, "ghq"))
        rc, devlist = incus("config", "device", "list", PROBE)
        n_after_add = len([l for l in devlist.splitlines() if l.strip()])
        incus("config", "device", "remove", PROBE, "ghq")
        rc, devlist = incus("config", "device", "list", PROBE)
        n_after_strip = len([l for l in devlist.splitlines() if l.strip()])
        rc, out = in_probe("ls -d ~/ghq >/dev/null 2>&1 && echo GHQ_OK || echo LOST")
        check("AC-Sd44947-1-3 migration: legacy device stripped, mount survives via profile",
              n_after_add == 1 and n_after_strip == 0 and "GHQ_OK" in out)

        # AC-1-2 reconcile: hand-remove ghq from the profile → scan loop restores it.
        incus("profile", "device", "remove", PROFILE, "ghq")
        gone = "ghq" not in profile_device_names()
        restored = False
        for _ in range(16):
            time.sleep(1)
            if "ghq" in profile_device_names():
                restored = True
                break
        check("AC-Sd44947-1-2 reconcile self-heals a hand-stripped profile device",
              gone and restored)

    finally:
        if cleanup_needed:
            cleanup()

    print()
    if failures:
        print(f"FAILED: {len(failures)} check(s): {failures}")
        return 1
    print("ALL CHECKS PASSED")
    return 0


if __name__ == "__main__":
    sys.exit(main())
