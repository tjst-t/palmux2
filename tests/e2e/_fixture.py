"""Sprint S025 — common fixture helper for palmux2 E2E tests.

Every E2E test under `tests/e2e/` that needs a hermetic git fixture
should use `palmux2_test_fixture()` from this module so that the fixture
is always cleaned up — even on test failure or Ctrl-C.

Usage::

    from _fixture import palmux2_test_fixture, BASE_URL

    with palmux2_test_fixture("s015") as fx:
        repo_path = fx.path
        repo_id = fx.repo_id
        ghq_path = fx.ghq_path
        # ... drive the test against BASE_URL ...

The context manager performs:

  1. mkdir under `<ghq root>/github.com/palmux2-test/<sprint>-<ts>-<pid>/`
  2. `git init -b main` + identity config + initial commit + dummy origin
  3. `POST /api/repos/{id}/open` to register with the running palmux2
  4. yield `Fixture(...)`
  5. *finally*: `POST /api/repos/{id}/close` (best-effort) + `rmtree`

A signal handler / `atexit` hook ensures cleanup even if the test
process is killed mid-flight by SIGINT / SIGTERM. The hook iterates all
live `Fixture` instances and tears them down.
"""
from __future__ import annotations

import atexit
import json
import os
import shutil
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import weakref
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import Iterator

# --- Configuration -----------------------------------------------------

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8215"
)
BASE_URL = f"http://localhost:{PORT}"
TEST_OWNER = "palmux2-test"
TIMEOUT_S = 20.0


# --- Live-fixture registry for atexit / signal cleanup -----------------
#
# We keep weak references so a fixture that exits its `with` block
# normally is dropped from the registry and not double-cleaned.

_LIVE: "weakref.WeakSet[Fixture]" = weakref.WeakSet()
_HANDLERS_INSTALLED = False


def _install_handlers() -> None:
    global _HANDLERS_INSTALLED
    if _HANDLERS_INSTALLED:
        return
    _HANDLERS_INSTALLED = True

    def _cleanup_all(signum: int | None = None, frame=None) -> None:
        # Snapshot to a list — _LIVE may mutate during iteration as
        # weakrefs get collected.
        for fx in list(_LIVE):
            try:
                fx._cleanup()
            except Exception:  # pragma: no cover — best-effort hook
                pass
        if signum is not None:
            # Re-raise so the process actually dies with the signal.
            sys.exit(128 + signum)

    atexit.register(_cleanup_all)
    # SIGTERM: container/CI kill (default is SIG_DFL → atexit does NOT
    # run, so we must install a handler). SIGINT: Ctrl-C (default is
    # Python's `default_int_handler` → KeyboardInterrupt unwinds and
    # atexit DOES run, but installing our handler is still useful when
    # the body is in C code that swallows KeyboardInterrupt).
    _python_int = signal.default_int_handler
    for sig in (signal.SIGINT, signal.SIGTERM):
        try:
            prev = signal.getsignal(sig)
            # Don't replace a non-default handler the test author has set.
            if prev in (signal.SIG_DFL, signal.SIG_IGN, None, _python_int):
                signal.signal(sig, _cleanup_all)
        except (ValueError, OSError):
            # signal.signal() fails outside main thread; that's fine.
            pass


# --- HTTP helpers (no external dependencies) ---------------------------


def _http(method: str, path: str, *, body: bytes | None = None,
          headers: dict[str, str] | None = None) -> tuple[int, bytes]:
    url = f"{BASE_URL}{path}"
    req = urllib.request.Request(url, method=method, data=body, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            return resp.status, resp.read()
    except urllib.error.HTTPError as e:
        return e.code, e.read()


def _http_json(method: str, path: str, *, body: dict | list | None = None) -> tuple[int, dict | list | str]:
    raw = json.dumps(body).encode() if body is not None else None
    h = {"Accept": "application/json"}
    if body is not None:
        h["Content-Type"] = "application/json"
    code, data = _http(method, path, body=raw, headers=h)
    try:
        return code, json.loads(data.decode() or "{}")
    except json.JSONDecodeError:
        return code, data.decode(errors="replace")


def _ghq_root() -> Path:
    out = subprocess.run(["ghq", "root"], capture_output=True, text=True, check=True)
    return Path(out.stdout.strip())


def _run(cwd: Path, *args: str) -> None:
    subprocess.run(args, cwd=cwd, check=True, capture_output=True, text=True)


# --- Fixture object ----------------------------------------------------


@dataclass(eq=False)
class Fixture:
    """A registered, hermetic palmux2 fixture repo.

    `eq=False` keeps Python's default identity-based __hash__ so the
    object can live in a WeakSet (the cleanup registry).
    """
    sprint: str
    path: Path           # absolute filesystem path
    ghq_path: str        # ghq-relative ("github.com/palmux2-test/sNNN-…")
    repo_id: str         # palmux2 repo ID

    _cleaned: bool = False

    def _cleanup(self) -> None:
        if self._cleaned:
            return
        self._cleaned = True
        # 1. Best-effort close via API so palmux2 drops repos.json + tmux.
        try:
            _http_json("POST", f"/api/repos/{urllib.parse.quote(self.repo_id)}/close")
        except Exception:
            pass
        # 2. rmtree the fixture dir.
        if self.path.exists():
            try:
                shutil.rmtree(self.path)
            except OSError:
                pass


# --- Public API --------------------------------------------------------


def make_fixture(sprint: str) -> Fixture:
    """Create + register a new fixture. Caller is responsible for cleanup."""
    _install_handlers()

    root = _ghq_root()
    stamp = time.strftime("%Y%m%d-%H%M%S")
    rel = f"github.com/{TEST_OWNER}/{sprint}-{stamp}-{os.getpid()}"
    path = root / rel
    if path.exists():
        # Pid + timestamp collision: bump with a counter.
        for n in range(2, 10):
            cand = root / f"{rel}-{n}"
            if not cand.exists():
                path, rel = cand, f"{rel}-{n}"
                break
        else:
            raise RuntimeError(f"cannot find unused fixture path under {rel}")

    path.mkdir(parents=True, exist_ok=False)
    _run(path, "git", "init", "-b", "main")
    _run(path, "git", "config", "user.email", "test@example.com")
    _run(path, "git", "config", "user.name", "Test")
    _run(path, "git", "config", "commit.gpgsign", "false")
    (path / "README.md").write_text("hi\n")
    _run(path, "git", "add", ".")
    _run(path, "git", "commit", "-m", "init")
    # Fake remote so gwq can derive worktree base path.
    _run(path, "git", "remote", "add", "origin", f"https://example.com/{rel}.git")

    # palmux2 derives the ID itself; ask it via /repos/available.
    code, avail = _http_json("GET", "/api/repos/available")
    if code != 200:
        raise RuntimeError(f"GET /api/repos/available: {code} {avail}")
    repo_id: str | None = None
    for entry in avail:  # type: ignore[union-attr]
        if entry.get("ghqPath") == rel:
            repo_id = entry["id"]
            break
    if repo_id is None:
        # Try once more after a short delay (filesystem rescan races).
        time.sleep(0.5)
        code, avail = _http_json("GET", "/api/repos/available")
        for entry in avail:  # type: ignore[union-attr]
            if entry.get("ghqPath") == rel:
                repo_id = entry["id"]
                break
    if repo_id is None:
        # Cleanup before raising.
        try:
            shutil.rmtree(path)
        except OSError:
            pass
        raise RuntimeError(f"fixture {rel} not surfaced by /api/repos/available")

    code, _ = _http_json("POST", f"/api/repos/{urllib.parse.quote(repo_id)}/open")
    if code not in (200, 201, 204):
        try:
            shutil.rmtree(path)
        except OSError:
            pass
        raise RuntimeError(f"open repo {repo_id}: {code}")

    fx = Fixture(sprint=sprint, path=path, ghq_path=rel, repo_id=repo_id)
    _LIVE.add(fx)
    return fx


@contextmanager
def palmux2_test_fixture(sprint: str) -> Iterator[Fixture]:
    """Context manager: create + register fixture, cleanup on exit (even on exception)."""
    fx = make_fixture(sprint)
    try:
        yield fx
    finally:
        fx._cleanup()
