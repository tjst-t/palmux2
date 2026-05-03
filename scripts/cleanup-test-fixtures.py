#!/usr/bin/env python3
"""Sprint S025 — palmux2 test fixture cleanup script.

Removes stale `palmux2-test/*` fixture repositories left behind by E2E
tests that crashed or were interrupted. Two channels are cleaned in one
pass:

  1. ghq folder: every directory under `<ghq root>/github.com/palmux2-test/`
     is rmtree'd.
  2. dev palmux2's `tmp/repos.json`: every entry whose ID begins with
     `palmux2-test--` is removed (preferring the running dev server's
     `POST /api/repos/{id}/close` so the tmux/state hooks fire; falling
     back to a direct file write if the server is offline).

Safety:

  * Only the **dev** config-dir is touched. Defaults to
    `<repo>/tmp/repos.json` (matches `make serve INSTANCE=dev`). Override
    with `--config-dir <path>`.
  * Host palmux2's config dir (`~/.config/palmux/`) is NEVER touched.
    The script refuses to operate on a directory that does not look like
    a palmux2 dev tmp dir (must contain repos.json AND live under the
    palmux2 repo's tree, OR be explicitly `--force`'d).
  * `--dry-run` prints the plan without touching anything.

Exit codes:
  0   success (or nothing to do)
  >0  unexpected error (filesystem permission, malformed repos.json, etc.)
"""
from __future__ import annotations

import argparse
import json
import os
import shutil
import subprocess
import sys
import urllib.error
import urllib.request
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
DEFAULT_CONFIG_DIR = REPO_ROOT / "tmp"
TEST_OWNER = "palmux2-test"
ID_PREFIX = f"{TEST_OWNER}--"


def ghq_root() -> Path:
    """Resolve the user's ghq root via `ghq root`."""
    out = subprocess.run(
        ["ghq", "root"], capture_output=True, text=True, check=True
    )
    return Path(out.stdout.strip())


def detect_dev_port(config_dir: Path) -> int | None:
    """Read `palmux-dev.portman.env` if present and return the dev port.

    Falls back to env var PALMUX2_DEV_PORT, then None (no running server).
    """
    env_file = config_dir / "palmux-dev.portman.env"
    if env_file.exists():
        for line in env_file.read_text().splitlines():
            line = line.strip()
            if line.startswith("PALMUX2_DEV_PORT="):
                try:
                    return int(line.split("=", 1)[1].strip())
                except ValueError:
                    pass
    if (port := os.environ.get("PALMUX2_DEV_PORT")):
        try:
            return int(port)
        except ValueError:
            return None
    return None


def list_test_repo_ids(config_dir: Path) -> list[str]:
    """Read repos.json and return all IDs whose prefix is the test owner."""
    repos_path = config_dir / "repos.json"
    if not repos_path.exists():
        return []
    try:
        entries = json.loads(repos_path.read_text())
    except json.JSONDecodeError as e:
        raise SystemExit(f"malformed repos.json: {e}") from e
    return [e["id"] for e in entries if isinstance(e, dict) and e.get("id", "").startswith(ID_PREFIX)]


def list_fixture_dirs() -> list[Path]:
    """Every directory under `<ghq root>/github.com/palmux2-test/`."""
    root = ghq_root() / "github.com" / TEST_OWNER
    if not root.exists():
        return []
    return sorted(p for p in root.iterdir() if p.is_dir())


def close_via_api(port: int, repo_id: str, timeout: float = 5.0) -> bool:
    """Call `POST /api/repos/{id}/close`. Returns True on 2xx."""
    url = f"http://localhost:{port}/api/repos/{repo_id}/close"
    req = urllib.request.Request(url, method="POST")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            return 200 <= resp.status < 300
    except urllib.error.HTTPError as e:
        # 404 means the repo is already gone — also fine.
        return e.code == 404
    except (urllib.error.URLError, TimeoutError, ConnectionError):
        return False


def remove_from_repos_json(config_dir: Path, ids_to_drop: list[str]) -> int:
    """Drop entries directly from repos.json. Returns count removed."""
    repos_path = config_dir / "repos.json"
    if not repos_path.exists() or not ids_to_drop:
        return 0
    entries = json.loads(repos_path.read_text())
    keep = [e for e in entries if e.get("id") not in set(ids_to_drop)]
    removed = len(entries) - len(keep)
    if removed > 0:
        repos_path.write_text(json.dumps(keep, indent=2) + "\n")
    return removed


def looks_like_palmux2_tmp(config_dir: Path, force: bool) -> bool:
    """Heuristic to refuse touching a non-dev config dir."""
    if force:
        return True
    repos_json = config_dir / "repos.json"
    if not repos_json.exists():
        # Nothing to corrupt — we'll only touch the ghq folder, OK.
        return True
    # Must live under the palmux2 repo we ship with.
    try:
        config_dir.resolve().relative_to(REPO_ROOT.resolve())
        return True
    except ValueError:
        return False


def main() -> int:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--config-dir", type=Path, default=DEFAULT_CONFIG_DIR,
                    help=f"palmux2 config directory (default: {DEFAULT_CONFIG_DIR})")
    ap.add_argument("--port", type=int, default=None,
                    help="dev server port (default: auto-detect from palmux-dev.portman.env or $PALMUX2_DEV_PORT)")
    ap.add_argument("--dry-run", action="store_true", help="print the plan without touching anything")
    ap.add_argument("--force", action="store_true",
                    help="allow operation on a config dir outside the palmux2 repo (dangerous)")
    args = ap.parse_args()

    config_dir: Path = args.config_dir.resolve()

    if not looks_like_palmux2_tmp(config_dir, args.force):
        print(
            f"refusing to touch {config_dir} — it is not under the palmux2 repo "
            "and might be a host palmux2 config dir. Use --force to override.",
            file=sys.stderr,
        )
        return 2

    port = args.port or detect_dev_port(config_dir)

    test_ids = list_test_repo_ids(config_dir)
    fixture_dirs = list_fixture_dirs()

    print(f"=== palmux2 test-fixture cleanup ===")
    print(f"config-dir : {config_dir}")
    print(f"ghq root   : {ghq_root()}")
    print(f"dev port   : {port if port else '(none — server offline, will write repos.json directly)'}")
    print(f"repos.json : {len(test_ids)} `{ID_PREFIX}*` entries to drop")
    for rid in test_ids:
        print(f"             - {rid}")
    print(f"fixtures   : {len(fixture_dirs)} directories to remove")
    for d in fixture_dirs:
        print(f"             - {d}")

    if args.dry_run:
        print("(dry-run, no changes)")
        return 0

    if not test_ids and not fixture_dirs:
        print("nothing to do.")
        return 0

    # 1. Drop repo entries (prefer API, fallback to direct file write).
    api_dropped = 0
    leftover_ids: list[str] = []
    if port and test_ids:
        for rid in test_ids:
            if close_via_api(port, rid):
                api_dropped += 1
            else:
                leftover_ids.append(rid)
    else:
        leftover_ids = list(test_ids)

    direct_dropped = remove_from_repos_json(config_dir, leftover_ids)
    print(f"repos.json : {api_dropped} dropped via API, {direct_dropped} dropped via direct write")

    # 2. Remove fixture directories.
    rm_count = 0
    rm_failed: list[tuple[Path, str]] = []
    for d in fixture_dirs:
        try:
            shutil.rmtree(d)
            rm_count += 1
        except OSError as e:
            rm_failed.append((d, str(e)))
    print(f"fixtures   : {rm_count} removed, {len(rm_failed)} failed")
    for d, err in rm_failed:
        print(f"             ! {d}: {err}", file=sys.stderr)

    # Exit non-zero if anything failed so CI notices.
    return 1 if rm_failed else 0


if __name__ == "__main__":
    sys.exit(main())
