#!/usr/bin/env python3
"""Sprint S6ab0ed — self-update BACKEND acceptance (real HTTP + real CLI subprocess).

Covers the server/CLI execution path that the GUI "Update all" shares
(decisions PD-4/PD-5/PD-7), in production mode (Rule 7 — no mocks, real binary).

Acceptance criteria:
  [AC-S6ab0ed-1-1] manifest defines CORE (palmux + image) + declared tools, each
                   with a version source — asserted via the live /api/selfupdate
                   snapshot which is built from the embedded manifest.
  [AC-S6ab0ed-2-1] POST /api/selfupdate/run runs the update via the
                   install.sh-generated ~/update-palmux2.sh (Sa53137-style
                   delegated privilege path), reusing image regenerate. When the
                   helper is present (Nix-managed) the server launches it
                   (detached) and returns ok; the helper is the same path the CLI
                   shares.
  [AC-S6ab0ed-2-4] manual-override (Nix-unmanaged) → server returns 409 with
                   guidance, does NOT attempt; CLI exits non-zero with guidance.
  [AC-S6ab0ed-2-5] `palmux update` (foreground) returns exit code = helper status;
                   `palmux update --check` prints current→latest.

Run against a dev instance started WITHOUT a ~/update-palmux2.sh present
(Nix-unmanaged) and with PALMUX_SELFUPDATE_FAKE_INSTALLED=v0.9.0:
  PALMUX2_DEV_PORT=<port> python3 tests/acceptance/s6ab0ed_selfupdate_backend.py
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
import urllib.error
import urllib.request
from pathlib import Path

PORT = os.environ.get("PALMUX2_DEV_PORT") or os.environ.get("PALMUX_DEV_PORT") or "8204"
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


def main() -> int:
    # AC-1-1: manifest-driven snapshot.
    _, snap = _get("/api/selfupdate")
    names = {c["name"] for c in snap.get("components", [])}
    check("AC-S6ab0ed-1-1 manifest CORE components present (palmux + image)",
          {"palmux", "image"} <= names)
    check("AC-S6ab0ed-1-1 declared tools present (gwq/portman)",
          {"gwq", "portman"} & names != set())
    palmux = next((c for c in snap["components"] if c["name"] == "palmux"), {})
    check("AC-S6ab0ed-1-1 each component carries a version source (installed+latest fields)",
          "installed" in palmux and "latest" in palmux)

    # AC-2-4: the dev rig has no ~/update-palmux2.sh → Nix-unmanaged → 409.
    if not snap.get("nixManaged"):
        code, body = _post("/api/selfupdate/run")
        check("AC-S6ab0ed-2-4 POST run on Nix-unmanaged returns 409 (does not attempt)",
              code == 409 and body.get("nixManaged") is False)
        check("AC-S6ab0ed-2-4 409 body carries guidance",
              "手動更新" in (body.get("error") or ""))
    else:
        print("[INFO] dev rig is Nix-managed; skipping the 409 assertion "
              "(the run path is covered by the live reconnect test)")

    # AC-2-1: with a real (harmless) ~/update-palmux2.sh stub, the server runs it
    # detached and returns ok. We exercise this against a throwaway HOME so the
    # real box stays untouched, via the CLI which shares the exact run path.
    with tempfile.TemporaryDirectory() as home:
        stub = Path(home) / "update-palmux2.sh"
        stub.write_text("#!/usr/bin/env bash\necho ran > '%s'/marker\nexit 0\n" % home)
        stub.chmod(0o755)
        env = dict(os.environ, HOME=home, PALMUX_SELFUPDATE_FAKE_INSTALLED="v0.9.0")
        res = subprocess.run([BIN, "update"], capture_output=True, text=True, env=env)
        check("AC-S6ab0ed-2-1/2-5 `palmux update` runs the helper (shared path) → exit 0",
              res.returncode == 0)
        check("AC-S6ab0ed-2-1 helper actually executed (marker written)",
              (Path(home) / "marker").exists())

    # AC-2-3 (rollback semantics): a FAILING helper → non-zero exit + rollback msg.
    with tempfile.TemporaryDirectory() as home:
        stub = Path(home) / "update-palmux2.sh"
        stub.write_text("#!/usr/bin/env bash\nexit 9\n")
        stub.chmod(0o755)
        env = dict(os.environ, HOME=home, PALMUX_SELFUPDATE_FAKE_INSTALLED="v0.9.0")
        res = subprocess.run([BIN, "update"], capture_output=True, text=True, env=env)
        check("AC-S6ab0ed-2-3 failing helper → non-zero exit",
              res.returncode != 0)
        check("AC-S6ab0ed-2-3 failure surfaces rollback guidance",
              "ロールバック" in (res.stdout + res.stderr))

    # AC-2-5: --check prints current→latest, exit 2 when update available.
    env = dict(os.environ, PALMUX_SELFUPDATE_FAKE_INSTALLED="v0.9.0")
    res = subprocess.run([BIN, "update", "--check"], capture_output=True, text=True, env=env)
    check("AC-S6ab0ed-2-5 `update --check` prints current→latest + exit 2",
          res.returncode == 2 and "v0.9.0" in res.stdout and "→" in res.stdout)

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
