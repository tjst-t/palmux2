#!/usr/bin/env python3
"""S009-fix-3 — Periodic Bash WS reconnect cycle detection.

Bug report (post-S009-fix-2):

  > 数秒使えて、 数秒 Reconnecting、 数秒使えるを繰り返す。
  > たぶんチェックしたタイミングでは OK なんだろうね

The previous E2E (s009_fix_lifecycle_v2.py) sampled state in 30s
windows and used a generous "drop budget" so dual-instance reconnect
noise wouldn't fail the test. That methodology hid the actual user-
observed pathology — a periodic ~3-10s reconnect cycle that goes on
forever.

This harness fixes that with **continuous monitoring over 180 seconds**
and **zero tolerance for drops**. It runs three independent checks
in parallel for the same Bash WS:

  - **WS frame continuity**: keep one WebSocket open the whole time.
    Record every close/error event with a timestamp. Pass = 0 closes.

  - **Periodic input round-trip**: send a unique echo command every
    2 s, verify the response within 3 s. Pass = every send echoes
    back. (The user's report manifests as the marker NOT echoing
    during the "Reconnecting" phase.)

  - **Server log scrape**: tail the dev palmux2 log file and count
    `sync_tmux: killing zombie session` events that target our test
    branch. Pass = zero zombie kills for our branch.

Exits 0 on PASS. Exits 1 with a structured failure report on FAIL,
including a millisecond-resolution timeline of the events that
triggered the failure so the root cause can be diagnosed.

Environment:
  PALMUX2_DEV_PORT      port of `make serve INSTANCE=dev` (default 8284)
  PALMUX2_DEV_LOG       path to the dev palmux2 log
                          (default ./tmp/palmux-dev.log)
  S009_FIX_REPO_ID      repository ID under test
                          (default tjst-t--palmux2--2d59)
  S009_FIX_BRANCH_ID    branch ID under test (default: pick first
                          openBranch of REPO_ID)
  S009_FIX_DURATION_S   monitoring window in seconds (default 180)
"""

from __future__ import annotations

import asyncio
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, field

import websockets

PORT = (
    os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8284"
)
DEV_LOG = os.environ.get(
    "PALMUX2_DEV_LOG",
    os.path.join(
        os.path.dirname(os.path.dirname(os.path.dirname(os.path.abspath(__file__)))),
        "tmp",
        "palmux-dev.log",
    ),
)
REPO_ID = os.environ.get("S009_FIX_REPO_ID", "tjst-t--palmux2--2d59")
BRANCH_ID = os.environ.get("S009_FIX_BRANCH_ID", "")
DURATION_S = float(os.environ.get("S009_FIX_DURATION_S", "180"))

BASE_URL = f"http://localhost:{PORT}"
WS_URL = f"ws://localhost:{PORT}"
HTTP_TIMEOUT = 15.0


def http(method: str, path: str, body: dict | None = None):
    url = f"{BASE_URL}{path}"
    data = None
    headers = {"Accept": "application/json"}
    if body is not None:
        data = json.dumps(body).encode()
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, data=data, method=method, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=HTTP_TIMEOUT) as resp:
            raw = resp.read().decode() or "{}"
            try:
                return resp.status, json.loads(raw)
            except json.JSONDecodeError:
                return resp.status, raw
    except urllib.error.HTTPError as e:
        raw = e.read().decode() or "{}"
        try:
            return e.code, json.loads(raw)
        except json.JSONDecodeError:
            return e.code, raw


def resolve_branch_id() -> str:
    """If BRANCH_ID isn't set, pick the first open branch of REPO_ID."""
    if BRANCH_ID:
        return BRANCH_ID
    code, body = http("GET", "/api/repos")
    if code != 200:
        raise SystemExit(f"GET /api/repos: {code} {body}")
    for r in body if isinstance(body, list) else []:
        if r.get("id") == REPO_ID:
            for b in r.get("openBranches", []) or []:
                return b["id"]
    raise SystemExit(f"no open branches for repo {REPO_ID}")


@dataclass
class Event:
    t: float
    kind: str  # ws_open, ws_close, ws_error, marker_sent, marker_echo, marker_timeout, log_zombie_kill, log_recover, log_ensure_warn
    detail: str = ""


@dataclass
class Result:
    branch_id: str = ""
    started: float = 0.0
    ended: float = 0.0
    events: list[Event] = field(default_factory=list)

    def add(self, kind: str, detail: str = "") -> None:
        self.events.append(Event(t=time.time(), kind=kind, detail=detail))

    def closes(self) -> list[Event]:
        return [e for e in self.events if e.kind == "ws_close"]

    def marker_failures(self) -> list[Event]:
        return [e for e in self.events if e.kind in ("marker_timeout", "marker_dropped")]

    def zombie_kills(self) -> list[Event]:
        return [e for e in self.events if e.kind == "log_zombie_kill"]

    def short_timeline(self, max_lines: int = 60) -> str:
        rows = []
        for e in self.events[:max_lines]:
            dt = e.t - self.started
            rows.append(f"  +{dt:7.2f}s  {e.kind:20s}  {e.detail}")
        if len(self.events) > max_lines:
            rows.append(f"  ... ({len(self.events) - max_lines} more events)")
        return "\n".join(rows)


async def ws_continuity_watcher(
    branch_id: str, deadline: float, result: Result
) -> None:
    """Open a single Bash attach WS and keep it open until deadline.
    Record every close/error event. The harness automatically reopens
    the connection so a single drop doesn't end the watcher early —
    we want to count drops, not bail on the first one.
    """
    url = (
        f"{WS_URL}/api/repos/{REPO_ID}/branches/{branch_id}/tabs/"
        f"bash:bash/attach?cols=80&rows=24"
    )
    while time.time() < deadline:
        try:
            ws = await websockets.connect(url, max_size=2**24)
        except Exception as exc:
            result.add("ws_error", f"connect: {exc!r}")
            await asyncio.sleep(0.5)
            continue
        result.add("ws_open", url)
        try:
            while time.time() < deadline:
                try:
                    msg = await asyncio.wait_for(ws.recv(), timeout=2.0)
                    _ = msg
                except asyncio.TimeoutError:
                    continue
                except websockets.ConnectionClosed as cc:
                    result.add("ws_close", f"code={cc.code} reason={cc.reason!r}")
                    break
                except Exception as exc:
                    result.add("ws_error", f"recv: {exc!r}")
                    break
        finally:
            try:
                await ws.close()
            except Exception:
                pass


async def marker_round_trip_watcher(
    branch_id: str, deadline: float, result: Result, period_s: float = 2.0
) -> None:
    """Every `period_s` seconds, send a unique echo to a fresh
    bash:bash WS and wait up to 3 s for the marker to come back.
    Skipping drops in marker_round_trip is what produced the user-
    visible "Reconnecting" UI label.
    """
    url = (
        f"{WS_URL}/api/repos/{REPO_ID}/branches/{branch_id}/tabs/"
        f"bash:bash/attach?cols=80&rows=24"
    )
    iteration = 0
    while time.time() < deadline:
        iteration += 1
        marker = f"PALMUX_S009FIX3_{iteration}_{int(time.time() * 1000)}"
        result.add("marker_sent", marker)
        try:
            ws = await websockets.connect(url, max_size=2**24)
        except Exception as exc:
            result.add("marker_dropped", f"connect failed: {exc!r}")
            await asyncio.sleep(period_s)
            continue
        try:
            # drain
            try:
                while True:
                    _ = await asyncio.wait_for(ws.recv(), timeout=0.2)
            except asyncio.TimeoutError:
                pass
            except websockets.ConnectionClosed as cc:
                result.add("marker_dropped", f"closed during drain: {cc.code}")
                continue
            try:
                await ws.send(json.dumps({"type": "input", "data": f"echo {marker}\n"}))
            except Exception as exc:
                result.add("marker_dropped", f"send failed: {exc!r}")
                continue
            seen = False
            t_end = time.time() + 3.0
            while time.time() < t_end:
                try:
                    msg = await asyncio.wait_for(ws.recv(), timeout=0.5)
                except asyncio.TimeoutError:
                    continue
                except websockets.ConnectionClosed as cc:
                    result.add(
                        "marker_dropped", f"closed waiting for echo: {cc.code}"
                    )
                    break
                if isinstance(msg, (bytes, bytearray)):
                    if marker.encode() in msg:
                        seen = True
                        break
                elif isinstance(msg, str) and marker in msg:
                    seen = True
                    break
            if seen:
                result.add("marker_echo", marker)
            else:
                result.add("marker_timeout", marker)
        finally:
            try:
                await ws.close()
            except Exception:
                pass
        await asyncio.sleep(period_s)


async def log_tail_watcher(branch_id: str, deadline: float, result: Result) -> None:
    """Tail the dev palmux2 log and record every line that
    references the test branch's session name. The session_name in
    the log is `_palmux_<repoId>_<branchId>` (or the configured
    prefix once S009-fix-3 is applied)."""
    if not os.path.exists(DEV_LOG):
        result.add(
            "log_error", f"dev log not found at {DEV_LOG}; skipping log scrape"
        )
        return
    # Match the canonical _palmux_ prefix and any optional alternative
    # the fix may introduce (`_palmux_dev_`).
    sess_re = re.compile(
        r"session=(_palmux[A-Za-z0-9_]*?_" + re.escape(REPO_ID) + r"_"
        + re.escape(branch_id) + r")"
    )
    branch_re = re.compile(r'branch="?([^"\s]+)"?')

    f = open(DEV_LOG, "r")
    f.seek(0, os.SEEK_END)  # tail-from-now
    try:
        while time.time() < deadline:
            line = f.readline()
            if not line:
                await asyncio.sleep(0.2)
                continue
            line = line.rstrip()
            # Zombie session kill targeting OUR branch.
            if "killing zombie session" in line and sess_re.search(line):
                result.add("log_zombie_kill", line)
            # ensureSession warning targeting OUR branch.
            elif "ensureSession" in line and "duplicate session" in line:
                m = sess_re.search(line)
                if m:
                    result.add("log_ensure_warn", line)
            # Recovery line for OUR branch.
            elif "recovering session" in line:
                m = branch_re.search(line)
                if m and m.group(1) in (
                    branch_id,
                    branch_id.replace("--", "/"),
                    "main",
                ):
                    result.add("log_recover", line)
    finally:
        f.close()


async def main() -> None:
    code, body = http("GET", "/api/health")
    if code != 200:
        raise SystemExit(f"health check failed: {code} {body}")
    branch_id = resolve_branch_id()
    print(f"S009-fix-3 periodic check: {BASE_URL}  repo={REPO_ID}  branch={branch_id}")
    print(f"  duration: {DURATION_S:.0f}s  dev log: {DEV_LOG}")
    result = Result(branch_id=branch_id, started=time.time())
    deadline = result.started + DURATION_S

    tasks = [
        asyncio.create_task(ws_continuity_watcher(branch_id, deadline, result)),
        asyncio.create_task(
            marker_round_trip_watcher(branch_id, deadline, result, period_s=2.0)
        ),
        asyncio.create_task(log_tail_watcher(branch_id, deadline, result)),
    ]
    # Periodic progress dots so the test isn't silent for 3 minutes.
    progress_task = asyncio.create_task(_progress(deadline, result))
    tasks.append(progress_task)
    await asyncio.gather(*tasks, return_exceptions=True)
    result.ended = time.time()

    print()
    closes = result.closes()
    fails = result.marker_failures()
    kills = result.zombie_kills()
    duration = result.ended - result.started
    print(
        f"Summary: duration={duration:.1f}s  ws_closes={len(closes)}  "
        f"marker_fails={len(fails)}  zombie_kills_for_our_branch={len(kills)}"
    )

    failed = bool(closes or fails or kills)
    if failed:
        print("FAIL: detected reconnect cycle / zombie kill activity")
        print("Timeline (first 60 events):")
        print(result.short_timeline(60))
        # Distil the failure pattern. Compute time deltas between
        # consecutive close events; periodic ~few-second gap is the
        # smoking gun.
        if len(closes) >= 2:
            deltas = [
                round(closes[i + 1].t - closes[i].t, 2)
                for i in range(len(closes) - 1)
            ]
            print(f"close→close intervals (s): {deltas}")
        sys.exit(1)
    print("PASS: no reconnect cycle detected over the monitoring window")


async def _progress(deadline: float, result: Result) -> None:
    last = result.started
    while time.time() < deadline:
        await asyncio.sleep(15)
        elapsed = time.time() - result.started
        closes = len(result.closes())
        fails = len(result.marker_failures())
        kills = len(result.zombie_kills())
        sent = sum(1 for e in result.events if e.kind == "marker_sent")
        echoed = sum(1 for e in result.events if e.kind == "marker_echo")
        print(
            f"  [{elapsed:6.1f}s] markers sent={sent} echoed={echoed} "
            f"ws_closes={closes} marker_fails={fails} zombie_kills={kills}"
        )
        last = time.time()


if __name__ == "__main__":
    try:
        asyncio.run(main())
    except KeyboardInterrupt:
        print("\nInterrupted")
        sys.exit(2)
