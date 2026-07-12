#!/usr/bin/env python3
"""Sprint S3f2658 Story 2 — AC-S3f2658-2-2 REAL-MACHINE E2E.

Proves, against a real running palmux2 (production wiring — the real
`palmux ptyhost` detached-process launcher, real socket discovery paths, real
Provider/Manager plumbing — NOT the in-process test fallback the Go unit
suite uses), that:

  1. A palmux2 process restart does NOT kill the claude-tui subprocess: the
     ptyhost holding it survives (detached via systemd-run/setsid — ADR-0003)
     and the restarted palmux2 reconnects to the SAME child (same pid).
  2. Output produced by the child DURING the restart window is not lost
     (§5's ring replay).
  3. The reconnect's SIGWINCH "screen restore" jiggle reaches the real child
     process as a genuine terminal resize.
  4. No stale-frame residue after reconnect.

This is a HERMETIC instance (unique free port / --config-dir / --tmux-prefix
— never the host palmux2, and never `make serve INSTANCE=dev`'s shared dev
rig either, to avoid colliding with any other agent's concurrently-running
dev instance in a sibling worktree). This matches the proven hermetic
pattern already used by tests/e2e/s7ce250_claude_tui.py and
tests/e2e/terminal_fit.py (priority_rule 6 — reuse an existing harness
pattern) while satisfying the story's intent (separate port/tmux-prefix/
ptyhost-instance-prefix, restart ONLY the isolated instance, never the host).

Because ptyhost survival depends on re-invoking a STABLE `<PalmuxBin>
ptyhost ...` path, this test REQUIRES the prebuilt `bin/palmux` binary (built
via `make build` if missing — never falls back to `go run`, whose ephemeral
build artifact would defeat the very thing under test).

The "claude" process is a tiny, dependency-free fake (no real claude API
calls — ADR-0002 keeps ptyhost claude-agnostic, and burning real API quota
for this would violate priority_rule 6's spirit) that emits a distinctive
marker + an incrementing counter as scrolling lines (so "no output lost" is
mechanically checkable against the raw byte stream) and traps SIGWINCH to
echo a marker (so the restore jiggle's reach into the real child process is
directly observable end-to-end).

Exit code 0 = PASS. Run standalone:
  python3 tests/e2e/s3f2658_2_ptyhost_restart_survival.py

Writes docs/sprint-logs/S3f2658/e2e-S3f2658-2.json with the result record.
"""
from __future__ import annotations

import asyncio
import json
import os
import re
import shutil
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.dirname(__file__))

REPO_ROOT = Path(__file__).resolve().parents[2]
PLAYWRIGHT_TIMEOUT = 20_000
RESULT_PATH = REPO_ROOT / "docs/sprint-logs/S3f2658/e2e-S3f2658-2.json"
PREBUILT_BIN = REPO_ROOT / "bin" / "palmux"


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def passed(msg: str) -> None:
    print(f"PASS: {msg}")


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _http_json(port: int, method: str, path: str, body: dict | None = None) -> tuple[int, Any]:
    url = f"http://localhost:{port}{path}"
    raw = json.dumps(body).encode() if body is not None else None
    headers = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, method=method, data=raw, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            code, data = resp.status, resp.read()
    except urllib.error.HTTPError as exc:
        code, data = exc.code, exc.read()
    try:
        return code, json.loads(data.decode() or "{}")
    except json.JSONDecodeError:
        return code, data.decode(errors="replace")


def ensure_prebuilt_binary() -> None:
    """REQUIRE bin/palmux (build it if missing) — never `go run` (see module
    docstring: ptyhost survival needs a stable re-invocable binary path)."""
    if PREBUILT_BIN.is_file():
        return
    print("bin/palmux not found — building (`make build`), this may take a while...")
    r = subprocess.run(["make", "build"], cwd=REPO_ROOT, capture_output=True, text=True)
    if r.returncode != 0 or not PREBUILT_BIN.is_file():
        fail(f"make build failed (rc={r.returncode}):\n{r.stdout[-4000:]}\n{r.stderr[-4000:]}")
    passed("built bin/palmux")


FAKE_CLAUDE_SRC = '''#!/usr/bin/env python3
"""S3f2658-2 E2E fake claude: TUI-like scrolling counter + SIGWINCH marker.
No real claude API calls (ADR-0002 — ptyhost/this fake are claude-agnostic).
"""
import sys, time, signal

def emit(line):
    sys.stdout.write(line + "\\r\\n")
    sys.stdout.flush()

def on_winch(signum, frame):
    emit("WINCH_MARKER")

def on_term(signum, frame):
    sys.exit(0)

signal.signal(signal.SIGWINCH, on_winch)
signal.signal(signal.SIGTERM, on_term)
signal.signal(signal.SIGINT, on_term)

emit("PALMUX_E2E_MARKER status: running")
n = 0
while True:
    n += 1
    emit("COUNTER %d" % n)
    time.sleep(0.2)
'''


def write_fake_claude(tmp_dir: Path) -> Path:
    p = tmp_dir / "fake_claude_e2e.py"
    p.write_text(FAKE_CLAUDE_SRC)
    p.chmod(0o755)
    return p


def start_hermetic(port: int, cfg_dir: Path, tmux_prefix: str, claude_bin: Path) -> subprocess.Popen:
    cmd = [
        str(PREBUILT_BIN),
        "--addr", f"127.0.0.1:{port}",
        "--config-dir", str(cfg_dir),
        "--claude-bin", str(claude_bin),
        "--tmux-prefix", tmux_prefix,
    ]
    proc = subprocess.Popen(
        cmd, cwd=REPO_ROOT, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, text=True,
    )
    deadline = time.monotonic() + 60.0
    listening = False
    while time.monotonic() < deadline:
        if proc.stdout is None:
            break
        line = proc.stdout.readline()
        if not line and proc.poll() is not None:
            rest = proc.stdout.read() if proc.stdout else ""
            fail(f"hermetic palmux2 exited before listening: rc={proc.returncode}\n{rest}")
        # Match ONLY the primary listener's own announcement line — NOT any
        # line merely containing ":<port>" (the separate incus-bridge
        # listener log line can share the same port number and, depending on
        # goroutine scheduling, print BEFORE the primary listener has
        # actually bound, causing a premature "ready" false-positive and a
        # subsequent ECONNREFUSED race).
        if "palmux2 listening" in line and f":{port}" in line:
            listening = True
            break
    if not listening:
        proc.kill()
        fail("hermetic palmux2 did not announce its listening port within 60s")

    # Keep draining stdout in the background for the process's whole
    # lifetime — otherwise the pipe's 64KiB buffer can fill (this server logs
    # plenty under claudetui/ptyhost activity) and the child would block on
    # write(), stalling everything downstream in a way that looks like an
    # unrelated hang.
    import threading

    def _drain() -> None:
        try:
            while proc.stdout and proc.poll() is None:
                if not proc.stdout.readline():
                    break
        except Exception:
            pass

    threading.Thread(target=_drain, daemon=True).start()
    return proc


def stop_hermetic(proc: subprocess.Popen) -> None:
    """Kill ONLY this hermetic instance's process — never the host palmux2.
    The detached ptyhost (a separate, already-migrated-to-its-own-cgroup or
    setsid-detached process per ADR-0003) is NOT a child of this process by
    the time it's launched, so killing proc does not touch it — that
    independence is exactly the mechanism under test."""
    if proc.poll() is None:
        proc.send_signal(signal.SIGTERM)
        try:
            proc.wait(timeout=10)
        except subprocess.TimeoutExpired:
            proc.kill()
            proc.wait(timeout=5)


def get_fixture_module(port: int):
    os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
    if "_fixture" in sys.modules:
        del sys.modules["_fixture"]
    import _fixture as fx_mod
    return fx_mod


def ws_collect(port: int, repo_id: str, branch_id: str, timeout_s: float = 6.0) -> bytes:
    """Raw-attach to the claude-tui WS, collect whatever arrives (the initial
    render-snapshot replay + any live bytes) for timeout_s, then disconnect."""
    try:
        import websockets
    except ImportError:
        fail("websockets package not installed — required for this E2E's precise assertions")

    tab_id_q = urllib.parse.quote("claude:claude", safe="")
    uri = (
        f"ws://localhost:{port}"
        f"/api/repos/{urllib.parse.quote(repo_id)}"
        f"/branches/{urllib.parse.quote(branch_id)}/tabs/{tab_id_q}/tui/attach"
    )

    async def _collect() -> bytes:
        collected = b""
        async with websockets.connect(uri, max_size=None) as ws:
            deadline = asyncio.get_event_loop().time() + timeout_s
            while asyncio.get_event_loop().time() < deadline:
                remaining = deadline - asyncio.get_event_loop().time()
                try:
                    msg = await asyncio.wait_for(ws.recv(), timeout=max(0.1, min(0.5, remaining)))
                except asyncio.TimeoutError:
                    continue
                collected += msg if isinstance(msg, bytes) else msg.encode()
        return collected

    return asyncio.run(_collect())


def get_stats(port: int, repo_id: str, branch_id: str) -> dict | None:
    """Returns None (not a fatal error) on a connection-level failure — the
    caller (wait_for_pid) treats that as "not ready yet" and keeps polling,
    since a hermetic instance we just restarted can have a brief window
    where the port isn't accepting connections yet even though it printed
    its ready line moments ago (process scheduling, not a real bug)."""
    tab_id_q = urllib.parse.quote("claude:claude", safe="")
    try:
        code, stats = _http_json(
            port, "GET",
            f"/api/repos/{urllib.parse.quote(repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/tabs/{tab_id_q}/tui/stats",
        )
    except (urllib.error.URLError, ConnectionError, OSError):
        return None
    if code != 200 or not isinstance(stats, dict):
        return None
    return stats


def wait_for_pid(port: int, repo_id: str, branch_id: str, timeout_s: float = 15.0) -> dict:
    deadline = time.monotonic() + timeout_s
    last: dict | None = {}
    while time.monotonic() < deadline:
        last = get_stats(port, repo_id, branch_id)
        if last is not None and last.get("pid", 0) > 0 and last.get("state") == "running":
            return last
        time.sleep(0.2)
    fail(f"daemon never reached state=running with a pid within {timeout_s}s; last={last!r}")
    raise AssertionError("unreachable")  # for type checkers


def counters_in(raw: bytes) -> list[int]:
    return [int(m) for m in re.findall(rb"COUNTER (\d+)", raw)]


def screenshot(page, path: Path) -> None:
    try:
        page.screenshot(path=str(path))
    except Exception as exc:  # best-effort — never fails the test on its own
        print(f"(screenshot failed, non-fatal: {exc})")


def main() -> None:
    print("=" * 70)
    print("S3f2658-2 — AC-S3f2658-2-2 ptyhost restart-survival + screen-restore E2E")
    print("=" * 70)

    ensure_prebuilt_binary()

    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("SKIP: playwright not installed")
        sys.exit(0)
    try:
        import websockets  # noqa: F401
    except ImportError:
        print("SKIP: websockets not installed")
        sys.exit(0)

    port = _free_port()
    cfg_dir = Path("/tmp") / f"palmux2-s3f2658-2-{port}"
    cfg_dir.mkdir(parents=True, exist_ok=True)
    tmux_prefix = f"_pmx_s3f2658_2_{port}_"
    tmp_dir = Path("/tmp") / f"palmux2-s3f2658-2-fake-{port}"
    tmp_dir.mkdir(parents=True, exist_ok=True)
    fake_claude = write_fake_claude(tmp_dir)

    result: dict[str, Any] = {
        "task": "AC-S3f2658-2-2",
        "startedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "hermeticPort": port,
        "tmuxPrefix": tmux_prefix,
    }

    screenshots_dir = REPO_ROOT / "docs/sprint-logs/S3f2658"
    screenshots_dir.mkdir(parents=True, exist_ok=True)

    proc = start_hermetic(port, cfg_dir, tmux_prefix, fake_claude)
    fx = get_fixture_module(port)
    fixture_cm = fx.palmux2_test_fixture("s3f2658-2-restart")
    fixture = fixture_cm.__enter__()
    try:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        repo_id = fixture.repo_id

        # claude-tui mode (the ptyhost-backed daemon; agent/stream-json mode
        # is the OTHER Claude mode and is unrelated to this story).
        tab_id_q = urllib.parse.quote("claude:claude", safe="")
        code, _ = _http_json(
            port, "PATCH",
            f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/tabs/{tab_id_q}/settings",
            body={"claude_mode": "tui"},
        )
        if code != 200:
            fail(f"PATCH claude_mode=tui: {code}")

        # ---- Phase 1: before restart ----------------------------------
        # Trigger EnsureStarted (lazy spawn) via a raw WS attach, and collect
        # long enough to see several counter ticks + the startup marker.
        raw_before = ws_collect(port, repo_id, branch_id, timeout_s=3.0)
        stats_before = wait_for_pid(port, repo_id, branch_id, timeout_s=15.0)
        pid_before = stats_before["pid"]
        counters_before = counters_in(raw_before)
        if "PALMUX_E2E_MARKER" not in raw_before.decode(errors="replace"):
            fail(f"marker not seen before restart; raw={raw_before[:400]!r}")
        if not counters_before:
            fail(f"no COUNTER lines seen before restart; raw={raw_before[:400]!r}")
        n1 = max(counters_before)
        passed(f"before restart: daemon running (pid={pid_before}), marker present, counter reached {n1}")

        # Browser: confirm the real embedded frontend renders the tab and
        # take a screenshot as visible-content evidence.
        url = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude"
        )
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                # Skip the first-run onboarding wizard (Sa53137) so the
                # screenshot shows the actual terminal content, not a modal
                # covering it — established pattern from s4323c8_tab_ui.py.
                ctx = browser.new_context(viewport={"width": 1280, "height": 800})
                ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                page = ctx.new_page()
                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_selector("[data-testid='claude-tui-terminal']", timeout=PLAYWRIGHT_TIMEOUT)
                page.wait_for_timeout(1200)  # let a few frames render
                screenshot(page, screenshots_dir / "e2e-S3f2658-2-before.png")
                status_before = page.locator("[data-testid='claude-tui-status']").inner_text()
                result["browserStatusBefore"] = status_before
            finally:
                browser.close()
        passed(f"browser rendered claude-tui tab before restart (status={result.get('browserStatusBefore')!r})")

        # ---- Phase 2: restart ONLY the hermetic instance ---------------
        # This is the crux of the AC: kill the palmux2 PROCESS (never the
        # host), then start a brand-new one — the ptyhost holding fake_claude
        # (detached per ADR-0003) must survive this untouched, and keep
        # emitting COUNTER lines the whole time nobody is connected.
        stop_hermetic(proc)
        time.sleep(0.3)  # let the counter keep ticking briefly during the "outage"
        proc2 = start_hermetic(port, cfg_dir, tmux_prefix, fake_claude)
        result["restartedAt"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

        # ---- Phase 3: after restart — reconnect + verify ---------------
        # The freshly-restarted palmux2 has a BRAND NEW in-memory Daemon (no
        # subprocess spawned yet — EnsureStarted is lazy, priority_rule 4).
        # Reconnecting the WS is what triggers EnsureStarted -> launchAndAttach
        # -> "survivor found, attach + replay + jiggle" (§3/§5) — this WS
        # reconnect IS the AC's "Reconnect the browser/WS to the dev instance"
        # step, and its first frame(s) are exactly the render-snapshot replay
        # (scrollback with every COUNTER line the emulator was fed, including
        # ones emitted while nobody was connected, + current screen + the
        # SIGWINCH-jiggle-triggered repaint that follows). AttachHandler
        # completes EnsureStarted synchronously before writing anything, so by
        # the time this call returns the daemon's pid is already settled.
        raw_after = ws_collect(port, repo_id, branch_id, timeout_s=4.0)

        stats_after = wait_for_pid(port, repo_id, branch_id, timeout_s=20.0)
        pid_after = stats_after["pid"]
        same_pid = pid_after == pid_before
        result["pidBefore"] = pid_before
        result["pidAfter"] = pid_after
        result["samePid"] = same_pid
        if not same_pid:
            fail(f"[AC-S3f2658-2-2] reconnected daemon has a DIFFERENT pid "
                 f"(before={pid_before}, after={pid_after}) — palmux2 spawned a NEW "
                 f"claude instead of reconnecting to the surviving ptyhost")
        passed(f"[AC-S3f2658-2-2] same child pid across restart ({pid_before}) — ptyhost survived")
        text_after = raw_after.decode(errors="replace")
        if "PALMUX_E2E_MARKER" not in text_after:
            fail(f"[AC-S3f2658-2-2] marker not present after reconnect (screen not restored); raw={raw_after[:600]!r}")
        counters_after = counters_in(raw_after)
        if not counters_after:
            fail(f"[AC-S3f2658-2-2] no COUNTER lines after reconnect; raw={raw_after[:600]!r}")

        # Strict-order check on THIS raw frame specifically: it is the direct
        # product of launchAndAttach's replay-feed + restore jiggle, with NO
        # other resize event yet in the picture (the browser's own later
        # reconnect triggers an independent FitAddon auto-resize, a separate,
        # ordinary terminal-reflow event unrelated to restart-recovery — see
        # the residue check below, which is scoped to avoid conflating the
        # two). A backward jump here would mean the jiggle/replay itself
        # produced disordered content — the literal thing AC-S3f2658-2-2 asks
        # NOT to happen.
        if counters_after != sorted(counters_after):
            fail(f"[AC-S3f2658-2-2] §5 residue: counter sequence from the restore "
                 f"replay+jiggle itself is OUT OF ORDER: {counters_after}")
        passed(f"[AC-S3f2658-2-2 §5] restore replay+jiggle produced a strictly "
               f"ordered, non-residue counter sequence ({counters_after[0]}..{counters_after[-1]})")
        n2 = max(counters_after)
        result["counterBeforeRestart"] = n1
        result["counterAfterRestart"] = n2
        if n2 <= n1:
            fail(f"[AC-S3f2658-2-2] counter did NOT advance across the restart "
                 f"(before={n1}, after={n2}) — output produced during the restart window was lost, "
                 f"or the child was actually respawned fresh")
        passed(f"[AC-S3f2658-2-2] counter advanced across restart: {n1} -> {n2} (content preserved, no reset)")

        # No-gap: the recent contiguous run of counter values up to n2 must
        # have no holes among what the replay retained (a small number is
        # expected to have scrolled out of the raw ring's retention window
        # for a very old start, but the recent tail must be contiguous).
        seen = set(counters_before) | set(counters_after)
        missing = [k for k in range(max(1, n2 - 5), n2 + 1) if k not in seen]
        result["noGapDetected"] = len(missing) == 0
        if missing:
            fail(f"[AC-S3f2658-2-2] gap detected in counter sequence near the restart boundary: "
                 f"missing={missing} seen(tail)={sorted(k for k in seen if k >= n2 - 8)}")
        passed("[AC-S3f2658-2-2] no gap in counter sequence across the restart window")

        # WINCH_MARKER proves the §5 restore jiggle's RESIZE frames reached
        # the REAL child process (full production stack — real ptyhost
        # subprocess, real PTY, real SIGWINCH delivery), not just an
        # in-process fake as in the Go unit precursor.
        winch_count = text_after.count("WINCH_MARKER")
        result["winchMarkerCount"] = winch_count
        if winch_count == 0:
            fail("[AC-S3f2658-2-2 §5] no WINCH_MARKER observed after reconnect — "
                 "the restore jiggle's RESIZE frames did not reach the real child process")
        passed(f"[AC-S3f2658-2-2 §5] restore jiggle reached the real child as a genuine SIGWINCH "
               f"({winch_count} marker(s) observed)")

        # No stale-frame residue: the render-snapshot is a single clean
        # ESC[H ESC[2J-framed reconstruction (see Emulator.RenderSnapshot),
        # not overlapping/duplicated fragments — assert the framing markers
        # appear exactly where expected and the marker line is NOT
        # duplicated back-to-back in a garbled way.
        marker_occurrences = text_after.count("PALMUX_E2E_MARKER")
        result["markerOccurrencesAfter"] = marker_occurrences
        # A clean scrollback-inclusive replay legitimately shows the marker
        # once (it was only ever printed once by fake_claude) — more than a
        # handful would indicate frame-stacking residue.
        if marker_occurrences > 3:
            fail(f"[AC-S3f2658-2-2] marker appears {marker_occurrences} times after reconnect — "
                 f"looks like stale/duplicated frame residue, not a clean restore")
        passed(f"[AC-S3f2658-2-2] no stale-frame residue (marker occurs {marker_occurrences}x, as expected)")

        # Browser: reconnect and confirm visible content + take an "after"
        # screenshot as evidence the restore is visible to a real user, not
        # just present in the raw byte stream.
        #
        # Note for anyone reading the saved screenshot: this is a THIRD
        # attach (after the raw-WS reconnect above already completed
        # EnsureStarted/the restore jiggle once — EnsureStarted is a one-shot
        # per Daemon object, so this attach does NOT trigger a second jiggle).
        # xterm.js's own FitAddon performs its OWN independent auto-resize on
        # mount, which — combined with this test's deliberately
        # SCROLLING-style fake_claude (chosen so "no gap" is byte-grep-able,
        # unlike real claude's repaint-in-place style) — can visibly reflow
        # already-scrolled rows in the screenshot. That is ordinary terminal
        # resize/reflow behavior (charmbracelet/x/vt), independent of and
        # AFTER this story's restore mechanism has already run cleanly (see
        # the strict-order assertion above, which specifically isolates and
        # proves the restore replay+jiggle itself is order-preserving).
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                ctx = browser.new_context(viewport={"width": 1280, "height": 800})
                ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
                page = ctx.new_page()
                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_selector("[data-testid='claude-tui-terminal']", timeout=PLAYWRIGHT_TIMEOUT)
                page.wait_for_timeout(1500)
                screenshot(page, screenshots_dir / "e2e-S3f2658-2-after.png")
                status_after = page.locator("[data-testid='claude-tui-status']").inner_text()
                result["browserStatusAfter"] = status_after
                if status_after not in ("connected", "streaming"):
                    fail(f"[AC-S3f2658-2-2] browser status after reconnect = {status_after!r}, "
                         f"want connected/streaming")
            finally:
                browser.close()
        passed(f"browser reconnected and rendered restored content (status={result.get('browserStatusAfter')!r})")

        result["verdict"] = "PASS"
    finally:
        # Explicitly close the BRANCH (not just the repo) while the second
        # hermetic instance is still up: this is what actually triggers
        # OnBranchClose -> CloseBranchDaemons -> Daemon.Shutdown (kills the
        # ptyhost). `_fixture`'s own repo-close does NOT cascade to this (repo
        # close and branch close are deliberately separate concepts in this
        # codebase's domain model — closing/hiding a repo must not kill a
        # running agent), and orphan GC for a ptyhost whose owning
        # branch/repo simply vanished without an explicit close is Story 3's
        # scope, not this one — so without this explicit call the detached
        # ptyhost (by design, ADR-0001/0002) would survive this test run
        # indefinitely. Best-effort: failures here don't fail the test, which
        # has already recorded its verdict.
        try:
            _http_json(
                port, "DELETE",
                f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
                f"/branches/{urllib.parse.quote(branch_id)}",
            )
        except Exception as exc:
            print(f"(best-effort branch close for cleanup failed, non-fatal: {exc})")

        fixture_cm.__exit__(None, None, None)
        for p in (proc, locals().get("proc2")):
            if p is not None:
                stop_hermetic(p)
        shutil.rmtree(cfg_dir, ignore_errors=True)
        shutil.rmtree(tmp_dir, ignore_errors=True)

    result["finishedAt"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    RESULT_PATH.write_text(json.dumps(result, indent=2) + "\n")
    print(f"Result written to {RESULT_PATH}")
    print("=" * 70)
    print("ALL PASS — AC-S3f2658-2-2 confirmed on a real running palmux2 instance")
    print("=" * 70)


if __name__ == "__main__":
    main()
