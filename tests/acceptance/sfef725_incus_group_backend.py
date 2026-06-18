#!/usr/bin/env python3
"""Sprint Sfef725 — incus-admin stale-group BACKEND acceptance.

Real HTTP against the running dev server + real CLI subprocess (production mode,
test-discipline Rule 7 — no MOCK/--fake-core/DRY_RUN; the classifier itself is
never bypassed, only its gid/membership/process-group INPUTS are varied via the
documented E2E seam, exactly as the prompt endorses).

Acceptance criteria:
  [AC-Sfef725-1-1] palmux compares the running process's effective groups against
                   the incus-admin gid and classifies a structured state, exposed
                   via GET /api/incus-group.
  [AC-Sfef725-1-2] `palmux runtime doctor` judges incus-admin by the RUNNING
                   service process's groups (not an `sg` subshell) and prints a
                   user-manager-restart remedy for the stale state.
  [AC-Sfef725-1-3] three distinct states (ok / stale / not-member) each yield a
                   distinct remedy. (n/a is the 4th, when incus is absent.)

Run:
  PALMUX2_DEV_PORT=<port> python3 tests/acceptance/sfef725_incus_group_backend.py

The dev server for the 3-state HTTP checks must be started with the seam env so
the endpoint can be driven through each state; this script also re-checks the
default (un-forced) endpoint shape and the CLI doctor/fix-verb directly.
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path

PORT = os.environ.get("PALMUX2_DEV_PORT") or os.environ.get("PALMUX_DEV_PORT") or "8210"
BASE = f"http://localhost:{PORT}"
REPO = Path(__file__).resolve().parents[2]
BIN = os.environ.get("PALMUX_BIN", str(REPO / "bin" / "palmux"))

_F: list[str] = []


def check(name, cond):
    print(f"[{'PASS' if cond else 'FAIL'}] {name}")
    if not cond:
        _F.append(name)


def _get(path):
    with urllib.request.urlopen(f"{BASE}{path}", timeout=20) as r:
        return r.status, json.load(r)


def _post(path):
    req = urllib.request.Request(f"{BASE}{path}", method="POST")
    try:
        with urllib.request.urlopen(req, timeout=20) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:  # type: ignore[attr-defined]
        return e.code, json.loads(e.read().decode() or "{}")


def _run_server_in_state(state: str, fn):
    """Start a throwaway dev server forcing `state`, run fn(port), tear down."""
    import socket
    import time

    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    env = dict(os.environ, PALMUX_INCUS_GROUP_FAKE_STATE=state)
    cfg = REPO / "tmp" / f"sfef725-{state}"
    cfg.mkdir(parents=True, exist_ok=True)
    proc = subprocess.Popen(
        [BIN, "--addr", f"127.0.0.1:{port}", "--config-dir", str(cfg),
         "--tmux-prefix", f"_pmx_sfef725_{state}_"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env,
    )
    try:
        for _ in range(50):
            try:
                urllib.request.urlopen(f"http://127.0.0.1:{port}/api/health", timeout=2)
                break
            except Exception:
                time.sleep(0.2)
        fn(port)
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except Exception:
            proc.kill()


def main() -> int:
    # ── AC-1-1 / AC-1-3: the three states via the real endpoint ───────────────
    states = {}

    def grab(state):
        def _f(port):
            with urllib.request.urlopen(
                f"http://127.0.0.1:{port}/api/incus-group", timeout=10
            ) as r:
                states[state] = json.load(r)
        return _f

    for st in ("ok", "stale", "not-member", "n/a"):
        _run_server_in_state(st, grab(st))

    check("AC-Sfef725-1-1 GET /api/incus-group returns a structured state",
          all("state" in states[s] for s in ("ok", "stale", "not-member", "n/a")))
    check("AC-Sfef725-1-1 ok state classified",
          states["ok"]["state"] == "ok")
    check("AC-Sfef725-1-3 stale state distinct + user-manager remedy",
          states["stale"]["state"] == "stale"
          and states["stale"]["remedy"] == "restart-user-manager")
    check("AC-Sfef725-1-3 not-member state distinct + usermod remedy",
          states["not-member"]["state"] == "not-member"
          and states["not-member"]["remedy"] == "usermod")
    check("AC-Sfef725-1-3 n/a state when incus absent",
          states["n/a"]["state"] == "n/a")
    check("AC-Sfef725-1-3 remedies are all distinct across the 3 actionable states",
          len({states[s]["remedy"] for s in ("ok", "stale", "not-member")}) == 3)

    # stale detail must clarify a plain --user restart is NOT enough.
    check("AC-Sfef725-1-2 stale detail says plain --user restart is NOT enough",
          "NOT enough" in states["stale"]["detail"])
    # not-member detail must carry the usermod command.
    check("AC-Sfef725-1-3 not-member detail carries usermod command",
          "usermod -aG incus-admin" in states["not-member"]["detail"])

    # ── AC-1-2: doctor uses the running-service groups, not an sg subshell ─────
    # The doctor's group section reads the running palmux2 service via systemctl
    # --user show MainPID; on the dev rig there is no such unit, so it falls back
    # to its own process groups and classifies a real state without an sg false
    # positive. We assert it RUNS and emits a group line + a state-specific
    # remedy when applicable (host-dependent; we accept ok / n/a / stale lines).
    res = subprocess.run([BIN, "runtime", "doctor"], capture_output=True, text=True)
    out = res.stdout + res.stderr
    check("AC-Sfef725-1-2 doctor emits an incus-admin group line",
          "incus-admin group" in out)
    check("AC-Sfef725-1-2 doctor never uses `sg incus-admin` (no subshell false-positive)",
          "sg incus-admin" not in out)

    # ── fix-incus-group verb: --check is a no-op that prints the resolved cmd ──
    res = subprocess.run([BIN, "fix-incus-group", "--check"], capture_output=True, text=True)
    check("AC-Sfef725-2-2 `palmux fix-incus-group --check` prints the fixed verb (systemctl restart user@<uid>)",
          res.returncode == 0 and "systemctl restart user@" in res.stdout)
    # Non-root real invocation refuses + shows the manual equivalent.
    res = subprocess.run([BIN, "fix-incus-group"], capture_output=True, text=True)
    check("AC-Sfef725-2-2 non-root fix-incus-group refuses + shows manual equivalent",
          res.returncode != 0 and "systemctl restart user@" in (res.stdout + res.stderr))

    # ── AC-2-5: install.sh installs a verb-limited NOPASSWD sudoers drop-in ────
    install_sh = (REPO / "scripts" / "install.sh").read_text()
    check("AC-Sfef725-2-5 install.sh writes the fix-incus-group sudoers drop-in",
          "/etc/sudoers.d/palmux-fix-incus-group" in install_sh)
    check("AC-Sfef725-2-5 sudoers grants EXACTLY the fix-incus-group verb (NOPASSWD, verb-limited)",
          "NOPASSWD: ${PALMUX_BIN_FOR_SUDOERS} fix-incus-group" in install_sh)
    check("AC-Sfef725-2-5 install.sh visudo-validates the drop-in",
          "visudo -cf /etc/sudoers.d/palmux-fix-incus-group" in install_sh)
    # The exact rendered sudoers line must pass visudo (verb-limited, no injection).
    rendered = ("ubuntu ALL=(root) NOPASSWD: /usr/bin/palmux2 fix-incus-group, "
                "/usr/bin/palmux2 fix-incus-group *\n")
    import tempfile as _tf
    from shutil import which as _which
    if _which("visudo"):
        with _tf.NamedTemporaryFile("w", suffix=".sudoers", delete=False) as tf:
            tf.write("# test\n" + rendered)
            tf_path = tf.name
        v = subprocess.run(["visudo", "-cf", tf_path], capture_output=True, text=True)
        os.unlink(tf_path)
        check("AC-Sfef725-2-5 rendered sudoers line passes visudo -cf",
              v.returncode == 0)
    else:
        print("[INFO] visudo not present — skipping the live visudo-parse check "
              "(the install.sh drop-in self-validates with visudo at install time)")

    # ── AC-3-1: install.sh re-surfaces the one-time-action in the summary ──────
    check("AC-Sfef725-3-1 install.sh re-surfaces the stale-group warning at the end (summary block)",
          "_incus_stale_pending" in install_sh
          and "ONE-TIME ACTION REQUIRED" in install_sh
          and "systemctl restart user@" in install_sh)
    check("AC-Sfef725-3-1 summary points to the GUI recover button",
          "incus-admin を適用" in install_sh)

    print()
    if _F:
        print(f"{len(_F)} FAILED:")
        for f in _F:
            print("  -", f)
        return 1
    print("ALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
