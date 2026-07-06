#!/usr/bin/env python3
"""Sprint S673a42 — real-backend API contract for the appliance update kicks.

Drives the REAL HTTP surface of a real dev palmux2 (no endpoint mocking). The dev
box is NOT NixOS, so this asserts the REAL contract observable there:
  - GET /api/selfupdate carries the backend-sourced applianceFlakeTarget field
    (empty off-appliance) → the FE never hardcodes the flake path.
  - The host-update kick + status are verb-limited to the NixOS appliance (409
    elsewhere) — the privileged path is not reachable on a plain host.
  - The palmux-ws image-install job runs for real (via the E2E-rig seam that
    replaces the 810 MB `runtime install` with a harmless command): POST starts it,
    GET reports running→done, and a concurrent POST is refused.

The NixOS-only UI (button, note, image row) + the real generation swap / reconnect
are covered by s673a42_update_panel_mock.py and the green appliance smoke
(AC-2-4 / AC-3-3).

Acceptance criteria:
  [AC-S673a42-1-1] /api/selfupdate exposes applianceFlakeTarget (backend single source).
  [AC-S673a42-2-1] host-update kick uses the verb-limited path — 409 off-appliance.
  [AC-S673a42-2-3] host-update status 409 off-appliance (privilege boundary held).
  [AC-S673a42-3-1] image-install job: POST 202 → GET running → done, no error.
  [AC-S673a42-3-2] image-install in-flight guard: concurrent POST → 409.
"""
from __future__ import annotations

import json
import os
import socket
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
BIN = os.environ.get("PALMUX_BIN", str(REPO / "bin" / "palmux"))
_FAILED: list[str] = []


def fail(name, msg):
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name, msg=""):
    print(f"  [{name}] {msg or 'OK'}")


def _free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


def _req(base, method, path):
    req = urllib.request.Request(base + path, method=method)
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
            body = resp.read().decode()
            return resp.status, (json.loads(body) if body else {})
    except urllib.error.HTTPError as e:
        body = e.read().decode()
        try:
            return e.code, json.loads(body)
        except Exception:
            return e.code, {"raw": body}


def main():
    port = _free_port()
    cfg = tempfile.mkdtemp(prefix="s673a42-api-")
    env = dict(os.environ)
    # E2E-rig seam: run a harmless command instead of the real image download.
    env["PALMUX_IMAGE_INSTALL_CMD"] = "sleep 1; exit 0"
    proc = subprocess.Popen(
        [BIN, "--addr", f"127.0.0.1:{port}", "--config-dir", cfg,
         "--tmux-prefix", "_pmx_s673a42_api_"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL, env=env,
    )
    base = f"http://127.0.0.1:{port}"
    try:
        # Wait for health.
        for _ in range(60):
            try:
                urllib.request.urlopen(base + "/api/health", timeout=2)
                break
            except Exception:
                time.sleep(0.2)

        # [AC-S673a42-1-1] applianceFlakeTarget present in the snapshot (empty here).
        st, snap = _req(base, "GET", "/api/selfupdate")
        if st != 200:
            fail("AC-S673a42-1-1", f"GET /api/selfupdate status {st}")
        elif "applianceFlakeTarget" not in snap:
            fail("AC-S673a42-1-1", f"snapshot missing applianceFlakeTarget key: {list(snap)}")
        elif snap.get("applianceFlakeTarget", "x") != "":
            fail("AC-S673a42-1-1", f"off-appliance applianceFlakeTarget should be empty, got {snap['applianceFlakeTarget']!r}")
        else:
            ok("AC-S673a42-1-1", "applianceFlakeTarget present + empty off-appliance (backend-sourced)")

        # [AC-S673a42-2-1] host-update kick refused off-appliance (verb-limited path).
        st, body = _req(base, "POST", "/api/selfupdate/rebuild")
        if st != 409:
            fail("AC-S673a42-2-1", f"POST /api/selfupdate/rebuild off-appliance status {st}, want 409")
        else:
            ok("AC-S673a42-2-1", "host-update kick → 409 off-appliance (privilege boundary)")

        # [AC-S673a42-2-3] host-update status also 409 off-appliance.
        st, _ = _req(base, "GET", "/api/selfupdate/rebuild")
        if st != 409:
            fail("AC-S673a42-2-3", f"GET /api/selfupdate/rebuild off-appliance status {st}, want 409")
        else:
            ok("AC-S673a42-2-3", "host-update status → 409 off-appliance")

        # [AC-S673a42-3-1] image-install job runs for real (seam) → running → done.
        st, body = _req(base, "POST", "/api/selfupdate/image-install")
        if st != 202:
            fail("AC-S673a42-3-1", f"POST /api/selfupdate/image-install status {st}, want 202: {body}")
        else:
            # [AC-S673a42-3-2] a concurrent POST while running is refused.
            st2, _ = _req(base, "POST", "/api/selfupdate/image-install")
            if st2 != 409:
                fail("AC-S673a42-3-2", f"concurrent image-install POST status {st2}, want 409")
            else:
                ok("AC-S673a42-3-2", "concurrent image-install → 409 (in-flight guard)")

            # Poll until the job finishes.
            done = False
            for _ in range(30):
                _, s = _req(base, "GET", "/api/selfupdate/image-install")
                if not s.get("running", True):
                    done = True
                    if s.get("error"):
                        fail("AC-S673a42-3-1", f"image-install reported error: {s['error']}")
                    else:
                        ok("AC-S673a42-3-1", "image-install job: 202 → running → done, no error")
                    break
                time.sleep(0.5)
            if not done:
                fail("AC-S673a42-3-1", "image-install job never finished")
    finally:
        proc.terminate()
        try:
            proc.wait(timeout=5)
        except Exception:
            proc.kill()

    print()
    if _FAILED:
        print(f"{len(_FAILED)} FAILED:")
        for f in _FAILED:
            print("  -", f)
        return 1
    print("ALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
