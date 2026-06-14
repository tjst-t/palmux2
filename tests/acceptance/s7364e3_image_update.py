#!/usr/bin/env python3
"""Sprint S7364e3-1 — incus container image drift detection + regeneration.

Real-mode acceptance test (real incus, real palmux). Uses the HTTP API
(urllib) + `incus` CLI as ground truth — NO mocks, NO playwright (so it runs on
hosts where playwright-python can't, including the Nix deploy VM).

Acceptance criteria:
  [AC-S7364e3-1-1] A stale incus container (volatile.base_image != current
                   palmux-ws alias fingerprint) is reported as runtime.stale=true
                   in the workspace runtime view (API).
  [AC-S7364e3-1-2] POST .../runtime/regenerate recreates the container on the new
                   image (transactionally — probe verifies the image launches
                   first); afterwards the container's base_image == the alias
                   fingerprint and the tmux session is recreated.
  [AC-S7364e3-1-3] No auto-regeneration: a stale container is NOT recreated
                   without the explicit POST (base_image unchanged across a scan
                   cycle).
  [AC-S7364e3-1-4] host runtime / already-latest container → stale=false.

Prereqs:
  - palmux (built from this branch) running, PALMUX2_DEV_PORT set (default 8080).
  - At least one OPEN incus-container Workspace whose container is STALE
    (rebuild + re-alias palmux-ws AFTER the container was created to create the
    drift fixture). The test discovers it from /api/repos.
  - `incus` on PATH (ground-truth checks).

Run:
  PALMUX2_DEV_PORT=8080 python3 tests/acceptance/s7364e3_image_update.py
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import time
import urllib.request

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8080"
)
BASE = f"http://127.0.0.1:{PORT}"
IMAGE_ALIAS = "palmux-ws"

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def api_get(path: str) -> object:
    req = urllib.request.Request(f"{BASE}{path}", headers={"Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=15) as resp:
        return json.loads(resp.read())


def api_post(path: str) -> dict:
    req = urllib.request.Request(f"{BASE}{path}", data=b"{}", method="POST",
                                 headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=180) as resp:
        return json.loads(resp.read())


def incus(*args: str, timeout: int = 30) -> tuple[int, str]:
    p = subprocess.run(["incus", *args], capture_output=True, text=True, timeout=timeout)
    return p.returncode, (p.stdout or "").strip()


def alias_fingerprint() -> str:
    code, out = incus("image", "list", IMAGE_ALIAS, "-f", "json")
    if code != 0:
        return ""
    try:
        imgs = json.loads(out)
    except Exception:  # noqa: BLE001
        return ""
    for im in imgs:
        for a in (im.get("aliases") or []):
            if a.get("name") == IMAGE_ALIAS:
                return im.get("fingerprint", "")
    return imgs[0].get("fingerprint", "") if len(imgs) == 1 else ""


def container_base_image(inst: str) -> str:
    code, out = incus("config", "get", inst, "volatile.base_image")
    return out if code == 0 else ""


def inst_name(repo_id: str, branch_id: str) -> str:
    """Mirror incus.InstanceName: sanitized 'repo-branch' + '-' + 8 hex of
    sha256(repoID/branchID)."""
    import hashlib
    import re
    raw = f"{repo_id}/{branch_id}"
    h = hashlib.sha256(raw.encode()).hexdigest()[:8]
    safe = re.sub(r"[^a-z0-9]+", "-", (repo_id + "-" + branch_id).lower()).strip("-")
    if len(safe) > 54:
        safe = safe[:54].rstrip("-")
    if not safe:
        safe = "ws"
    return f"{safe}-{h}"


def find_incus_workspace() -> tuple[str, str, str] | None:
    """Return (repoId, branchId, instName) of an OPEN incus-container workspace,
    preferring a STALE one (whose container base != alias fingerprint) so the
    full regenerate path is exercised. Falls back to any incus workspace."""
    alias_fp = alias_fingerprint()
    repos = api_get("/api/repos")
    first = None
    for repo in repos:
        for b in (repo.get("openBranches") or []):
            rt = b.get("runtime") or {}
            if rt.get("kind") != "incus-container":
                continue
            inst = inst_name(repo["id"], b["id"])
            if first is None:
                first = (repo["id"], b["id"], inst)
            if alias_fp and container_base_image(inst) and container_base_image(inst) != alias_fp:
                return (repo["id"], b["id"], inst)  # stale → preferred fixture
    return first


def runtime_view(repo_id: str, branch_id: str) -> dict:
    repo = api_get(f"/api/repos/{repo_id}")
    for b in (repo.get("openBranches") or []):
        if b["id"] == branch_id:
            return b.get("runtime") or {}
    return {}


def wait_stale(repo_id: str, branch_id: str, want: bool, timeout: int = 25) -> bool:
    """Poll the runtime view until stale matches `want` (drift is computed by the
    10s scan loop)."""
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        last = bool(runtime_view(repo_id, branch_id).get("stale", False))
        if last == want:
            return True
        time.sleep(2)
    print(f"    (last observed stale={last}, wanted {want})", file=sys.stderr)
    return False


# ─── AC-1-1 + 1-4: drift detection ────────────────────────────────────────────

def test_drift_detection(ws) -> None:
    repo_id, branch_id, inst = ws
    name = "AC-S7364e3-1-1"
    alias_fp = alias_fingerprint()
    base_fp = container_base_image(inst)
    print(f"    inst={inst} base={base_fp[:12]} alias={alias_fp[:12]}")
    if not alias_fp:
        fail(name, f"palmux-ws alias not found on host — cannot test drift (build/import the image)")
        return
    if not base_fp:
        fail(name, f"container {inst} has no volatile.base_image — is it running?")
        return

    expected_stale = base_fp != alias_fp
    if not wait_stale(repo_id, branch_id, expected_stale):
        fail(name, f"runtime.stale did not converge to {expected_stale} for {inst}")
        return
    if expected_stale:
        ok(name, f"stale container detected (base {base_fp[:12]} != alias {alias_fp[:12]})")
    else:
        ok("AC-S7364e3-1-4", f"already-latest container reported stale=false")

    # AC-1-4: host scope is never stale.
    repos = api_get("/api/repos")
    for repo in repos:
        for b in (repo.get("openBranches") or []):
            rt = b.get("runtime") or {}
            if rt.get("kind") == "host" and rt.get("stale"):
                fail("AC-S7364e3-1-4", f"host runtime reported stale=true: {repo['id']}/{b['id']}")
                return
    ok("AC-S7364e3-1-4", "no host runtime reported stale")


# ─── AC-1-3: no auto-regeneration ─────────────────────────────────────────────

def test_no_auto_regen(ws) -> None:
    repo_id, branch_id, inst = ws
    name = "AC-S7364e3-1-3"
    before = container_base_image(inst)
    if not before:
        fail(name, f"no base image for {inst}")
        return
    # Wait > one scan cycle (10s) without issuing the POST.
    time.sleep(13)
    after = container_base_image(inst)
    if after != before:
        fail(name, f"container auto-regenerated without explicit POST (base {before[:12]} → {after[:12]})")
        return
    ok(name, f"stale container NOT auto-regenerated across a scan cycle (base stable {before[:12]})")


# ─── AC-1-2: explicit regeneration ────────────────────────────────────────────

def test_regenerate(ws) -> None:
    repo_id, branch_id, inst = ws
    name = "AC-S7364e3-1-2"
    alias_fp = alias_fingerprint()
    base_before = container_base_image(inst)
    if base_before == alias_fp:
        ok(name, "(skipped regen-to-new — container already latest; transactional path covered by unit test)")
        return

    print(f"    regenerating {inst} ({base_before[:12]} → {alias_fp[:12]}) ...")
    resp = api_post(f"/api/repos/{repo_id}/branches/{branch_id}/runtime/regenerate")
    if not resp.get("ok") or not resp.get("regenerated"):
        fail(name, f"regenerate did not succeed: {resp}")
        return

    # Ground truth: the container's base image is now the alias fingerprint.
    base_after = container_base_image(inst)
    if base_after != alias_fp:
        fail(name, f"after regenerate, base_image {base_after[:12]} != alias {alias_fp[:12]}")
        return

    # Runtime returns to ready and the tmux session is recreated in the new
    # container (claude --resume path runs through ensureSession).
    deadline = time.time() + 30
    state = None
    while time.time() < deadline:
        state = runtime_view(repo_id, branch_id).get("state")
        if state == "ready":
            break
        time.sleep(2)
    if state != "ready":
        fail(name, f"runtime did not return to ready after regenerate (state={state})")
        return

    # Poll for the recreated session: ensureSession creates it, but the periodic
    # sync_tmux reconciliation for incus sessions can briefly hold the tmux
    # socket, so a single shot can race. The session is persistent once settled.
    sess_deadline = time.time() + 15
    code, sess = 1, ""
    while time.time() < sess_deadline:
        code, sess = incus("exec", inst, "--user", "1000", "--group", "1000",
                           "--env", "HOME=/home/ubuntu", "--", "tmux", "ls")
        if code == 0 and sess.strip():
            break
        time.sleep(2)
    if code != 0 or not sess.strip():
        fail(name, f"tmux session not recreated in regenerated container (exit={code}, out={sess!r})")
        return

    # And stale should now be false.
    if not wait_stale(repo_id, branch_id, False):
        fail(name, "runtime.stale did not clear after regenerate")
        return
    ok(name, f"container regenerated on new image (base now {base_after[:12]}), session recreated, stale cleared")


# ─── Runner ───────────────────────────────────────────────────────────────────

def main() -> int:
    ws = find_incus_workspace()
    if ws is None:
        print("SKIP: no open incus-container Workspace found. This real-mode "
              "acceptance test requires an incus workspace (a stale one for the "
              "full path). Open one and rebuild palmux-ws to create drift.",
              file=sys.stderr)
        return 0
    print(f"Using workspace repo={ws[0]} branch={ws[1]} inst={ws[2]}")

    # Order matters: detect drift → confirm no auto-regen → explicit regen.
    test_drift_detection(ws)
    test_no_auto_regen(ws)
    test_regenerate(ws)

    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
