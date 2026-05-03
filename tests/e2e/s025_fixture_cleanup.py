#!/usr/bin/env python3
"""Sprint S025 — E2E test fixture cleanup hygiene.

Validates the cleanup machinery introduced in S025:

  (a) `scripts/cleanup-test-fixtures.py` removes pre-existing
      `palmux2-test/*` fixtures (folder + repos.json entries).
  (b) The `palmux2_test_fixture()` context manager removes the fixture
      on normal exit — no `palmux2-test--*` left in repos.json, no
      directory left under `~/ghq/github.com/palmux2-test/`.
  (c) The same context manager also cleans up when the test body
      raises — finally-block guarantee.
  (d) `make e2e-cleanup` works as a CLI alias and clears any leftover
      fixtures created out-of-band.

The test contract: at the end of each scenario, the steady-state
invariant is enforced — `~/ghq/github.com/palmux2-test/` is empty AND
no `palmux2-test--*` IDs are listed by `GET /api/repos`.

Exit code 0 = PASS. Anything else = FAIL.
"""
from __future__ import annotations

import json
import os
import subprocess
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or "8215"
)
BASE_URL = f"http://localhost:{PORT}"
REPO_ROOT = Path(__file__).resolve().parents[2]

# Make tests/e2e/_fixture.py importable.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from _fixture import palmux2_test_fixture, make_fixture, _LIVE  # noqa: E402


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def assert_(cond: bool, msg: str) -> None:
    if not cond:
        fail(msg)


def http_get_json(path: str):
    req = urllib.request.Request(f"{BASE_URL}{path}", method="GET",
                                 headers={"Accept": "application/json"})
    with urllib.request.urlopen(req, timeout=10.0) as resp:
        return resp.status, json.loads(resp.read().decode())


def ghq_root() -> Path:
    out = subprocess.run(["ghq", "root"], capture_output=True, text=True, check=True)
    return Path(out.stdout.strip())


def palmux2_test_dir() -> Path:
    return ghq_root() / "github.com" / "palmux2-test"


def list_test_repo_ids() -> list[str]:
    code, repos = http_get_json("/api/repos")
    assert_(code == 200, f"GET /api/repos: {code}")
    return [r["id"] for r in repos if r["id"].startswith("palmux2-test--")]


def list_fixture_dirs() -> list[Path]:
    d = palmux2_test_dir()
    return sorted(p for p in d.iterdir() if p.is_dir()) if d.exists() else []


def assert_clean_state(label: str) -> None:
    ids = list_test_repo_ids()
    dirs = list_fixture_dirs()
    assert_(not ids,  f"{label}: repos.json still has palmux2-test--* entries: {ids}")
    assert_(not dirs, f"{label}: fixture dirs still present: {dirs}")
    print(f"  [{label}] clean (0 ids, 0 dirs)")


# --- Scenarios ---------------------------------------------------------


def scenario_a_script_clears_pre_existing() -> None:
    """Create 2 leftover fixtures by hand (bypass context manager), run
    the cleanup script, verify both are gone."""
    print("[a] cleanup script removes pre-existing fixtures")
    # Forge two leftovers via the helper but skip the context manager so
    # we don't auto-cleanup. We deliberately create + register them, then
    # discard the Fixture object so the steady-state stays "dirty" until
    # the script runs.
    f1 = make_fixture("s025-leak-a")
    f2 = make_fixture("s025-leak-b")
    # Drop them from the live registry so the helper's atexit hook
    # doesn't double-clean.
    _LIVE.discard(f1)
    _LIVE.discard(f2)

    # Confirm dirty state.
    ids = list_test_repo_ids()
    assert_(f1.repo_id in ids and f2.repo_id in ids,
            f"forged leftovers not in repos.json: have {ids}")
    assert_(f1.path.exists() and f2.path.exists(), "fixture dirs missing")
    print(f"  forged: {f1.repo_id}, {f2.repo_id}")

    # Run the cleanup script.
    res = subprocess.run(
        ["python3", str(REPO_ROOT / "scripts" / "cleanup-test-fixtures.py"),
         "--config-dir", str(REPO_ROOT / "tmp")],
        capture_output=True, text=True,
    )
    assert_(res.returncode == 0, f"cleanup script failed: {res.returncode}\n{res.stdout}\n{res.stderr}")
    print(f"  cleanup script: rc={res.returncode}")

    assert_clean_state("a")


def scenario_b_normal_exit_cleans_up() -> None:
    """Use the context manager normally; on exit the fixture must be gone."""
    print("[b] context manager cleans up on normal exit")
    with palmux2_test_fixture("s025-normal") as fx:
        # During the with-block the fixture must exist.
        assert_(fx.repo_id in list_test_repo_ids(),
                f"fixture not registered while in context: {fx.repo_id}")
        assert_(fx.path.exists(), "fixture path missing inside context")
    # Allow palmux2's filesystem watch to settle.
    time.sleep(0.5)
    assert_clean_state("b")


def scenario_c_exception_in_body_still_cleans() -> None:
    """Raise an exception inside the with-block; finally must still cleanup."""
    print("[c] context manager cleans up even when body raises")
    captured_id: str | None = None
    try:
        with palmux2_test_fixture("s025-throws") as fx:
            captured_id = fx.repo_id
            assert_(fx.repo_id in list_test_repo_ids(), "fixture not registered")
            raise RuntimeError("intentional failure for S025-c")
    except RuntimeError as e:
        assert_(str(e) == "intentional failure for S025-c", f"wrong exception: {e}")
    assert_(captured_id is not None, "context manager never yielded")
    time.sleep(0.5)
    assert_clean_state("c")


def scenario_d_make_target() -> None:
    """`make e2e-cleanup` clears whatever the helper missed."""
    print("[d] make e2e-cleanup target works")
    # Forge one leftover that escapes the helper's registry.
    leak = make_fixture("s025-make-target")
    _LIVE.discard(leak)
    assert_(leak.repo_id in list_test_repo_ids(), "leak not present before make target")

    res = subprocess.run(
        ["make", "e2e-cleanup"],
        cwd=str(REPO_ROOT), capture_output=True, text=True,
    )
    assert_(res.returncode == 0,
            f"make e2e-cleanup failed: rc={res.returncode}\n{res.stdout}\n{res.stderr}")
    print(f"  make e2e-cleanup: rc={res.returncode}")

    time.sleep(0.5)
    assert_clean_state("d")


# --- main --------------------------------------------------------------


def main() -> None:
    print(f"S025 — fixture cleanup hygiene E2E (port {PORT})")
    code, _ = http_get_json("/api/repos")
    assert_(code == 200, f"server reachability: {code}")

    # Always start clean.
    assert_clean_state("pre")

    scenario_a_script_clears_pre_existing()
    scenario_b_normal_exit_cleans_up()
    scenario_c_exception_in_body_still_cleans()
    scenario_d_make_target()

    print("S025 E2E: PASS")


if __name__ == "__main__":
    main()
