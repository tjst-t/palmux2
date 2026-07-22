#!/usr/bin/env python3
"""Sfa2bab AC-1-2 / AC-1-3: real-appliance dogfood of the codex/opencode tabs.

Drives a REAL palmuxOS appliance VM through its OWN public surface — the same
REST + terminal-WebSocket path a browser uses — rather than poking incus
directly. That distinction is the whole point of this Sprint: S2b5691's
acceptance test (tests/acceptance/s2b5691_codex_opencode_incontainer.py) proves
the notify HOOK works by running codex/opencode under `incus exec` with a stub
receiver, which passes even when palmux2 never wires the hook up in the first
place. Sfa2bab's original run found exactly that bug (bridgeNotifyURL() returned
"" under a wildcard bind, so the hook was silently never injected) and it was
only visible from the tab path.

  [AC-Sfa2bab-1-2] codex / opencode tabs each: PTY attaches, a real turn runs to
                   completion in-container, AND an Activity Inbox notification
                   arrives for that tab.
  [AC-Sfa2bab-1-3] bash + claude tabs still work (no regression).

The appliance under test MUST be running a build that contains the Sc4f091 fix;
a v0.16.0 release image will fail AC-1-2's notify half by design. Build it with
`nix build .#appliance-qcow2-local` (bakes local working-tree source) — plain
`.#appliance-qcow2` fetches the released binary and would silently test old code.

Usage:
  python3 tests/acceptance/sfa2bab_appliance_dogfood.py --base http://127.0.0.1:17683
"""

from __future__ import annotations

import argparse
import asyncio
import json
import sys
import time
import urllib.error
import urllib.request

import websockets

PROMPT = "Reply with exactly the single word PALMUXOK and nothing else."
MARKER = "PALMUXOK"
AGENTS = ("codex:codex", "opencode:opencode")

_failures: list[str] = []
_passes: list[str] = []


def ok(name: str, msg: str = "") -> None:
    _passes.append(name)
    print(f"PASS [{name}] {msg}", flush=True)


def fail(name: str, msg: str) -> None:
    _failures.append(name)
    print(f"FAIL [{name}] {msg}", flush=True)


def api(base: str, method: str, path: str, body: dict | None = None, timeout: int = 30):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method)
    if data:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read().decode()
            return r.status, (json.loads(raw) if raw.strip() else None)
    except urllib.error.HTTPError as e:
        raw = e.read().decode()
        try:
            return e.code, json.loads(raw)
        except Exception:
            return e.code, raw
    except Exception as e:  # connection refused etc.
        return 0, str(e)


def wait_healthy(base: str, timeout: float) -> bool:
    deadline = time.time() + timeout
    while time.time() < deadline:
        status, _ = api(base, "GET", "/api/health", timeout=5)
        if status == 200:
            return True
        time.sleep(2)
    return False


def notifications_for(base: str, tab_id: str) -> list[dict]:
    """Activity Inbox entries for one tab.

    GET /api/notifications returns notify.Hub.All(): a map keyed by
    "<repoId>/<branchId>" whose values are BranchState{notifications: [...]},
    NOT a flat list — flatten before filtering.
    """
    status, body = api(base, "GET", "/api/notifications")
    if status != 200 or not isinstance(body, dict):
        return []
    out: list[dict] = []
    for state in body.values():
        for n in (state or {}).get("notifications") or []:
            if n.get("tabId") == tab_id:
                out.append(n)
    return out


async def drive_tab(base: str, repo_id: str, branch_id: str, tab_id: str,
                    prompt: str, marker: str, settle: float,
                    turn_timeout: float) -> tuple[bool, str]:
    """Attach to a tui tab over WS, send `prompt`, collect output until `marker`.

    Returns (marker_seen, captured_text). The agent-tui WS takes RAW utf-8 bytes
    (no JSON envelope) — see frontend/src/lib/terminal-manager.ts encodeInput().
    """
    ws_base = base.replace("http://", "ws://").replace("https://", "wss://")
    url = (f"{ws_base}/api/repos/{repo_id}/branches/{branch_id}"
           f"/tabs/{tab_id}/tui/attach")
    captured: list[str] = []
    seen = False
    async with websockets.connect(url, max_size=None, open_timeout=30) as ws:
        # Let the agent's TUI finish painting before typing; these CLIs drop
        # input sent while they are still initialising.
        end_settle = time.time() + settle
        while time.time() < end_settle:
            try:
                msg = await asyncio.wait_for(ws.recv(), timeout=1.0)
                captured.append(msg.decode(errors="replace") if isinstance(msg, bytes) else str(msg))
            except asyncio.TimeoutError:
                pass
        await ws.send(prompt.encode())
        await asyncio.sleep(0.4)
        await ws.send(b"\r")
        deadline = time.time() + turn_timeout
        while time.time() < deadline:
            try:
                msg = await asyncio.wait_for(ws.recv(), timeout=2.0)
            except asyncio.TimeoutError:
                continue
            captured.append(msg.decode(errors="replace") if isinstance(msg, bytes) else str(msg))
            # Only count the marker if it appears AFTER the echoed prompt, so
            # the agent's own echo of our instruction is not mistaken for its
            # answer. The prompt text itself contains the marker word.
            text = "".join(captured)
            after = text.split(prompt)[-1] if prompt in text else text
            if marker in after:
                seen = True
                break
    return seen, "".join(captured)


async def drive_bash(base: str, repo_id: str, branch_id: str, tab_id: str) -> tuple[bool, str]:
    """Bash tabs are tmux-backed and take {type:'input',data} JSON frames."""
    ws_base = base.replace("http://", "ws://").replace("https://", "wss://")
    url = f"{ws_base}/api/repos/{repo_id}/branches/{branch_id}/tabs/{tab_id}/attach"
    token = "palmux-sfa2bab-regression-ok"
    captured: list[str] = []
    async with websockets.connect(url, max_size=None, open_timeout=30) as ws:
        await asyncio.sleep(2.0)
        await ws.send(json.dumps({"type": "input", "data": f"echo {token}\n"}))
        deadline = time.time() + 30
        while time.time() < deadline:
            try:
                msg = await asyncio.wait_for(ws.recv(), timeout=2.0)
            except asyncio.TimeoutError:
                continue
            captured.append(msg.decode(errors="replace") if isinstance(msg, bytes) else str(msg))
            text = "".join(captured)
            # Two occurrences = the typed command echo + the actual output.
            if text.count(token) >= 2:
                return True, text
    return False, "".join(captured)


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base", default="http://127.0.0.1:17683")
    ap.add_argument("--repo-substr", default="",
                    help="substring of the throwaway repo's ghqPath (default: first available)")
    ap.add_argument("--settle", type=float, default=25.0,
                    help="seconds to let the agent TUI paint before typing")
    ap.add_argument("--turn-timeout", type=float, default=180.0)
    ap.add_argument("--notify-timeout", type=float, default=150.0)
    args = ap.parse_args()
    base = args.base.rstrip("/")

    if not wait_healthy(base, 180):
        fail("startup", f"{base}/api/health never returned 200")
        return 1
    ok("startup", f"appliance reachable at {base}")

    status, agents = api(base, "GET", "/api/agents")
    kinds = {a["kind"] for a in agents} if isinstance(agents, list) else set()
    if status == 200 and {"claude", "codex", "opencode"} <= kinds:
        ok("agents", f"GET /api/agents = {sorted(kinds)}")
    else:
        fail("agents", f"status={status} kinds={sorted(kinds)} "
                       "(are [agents.codex]/[agents.opencode] in config.toml?)")
        return 1

    status, available = api(base, "GET", "/api/repos/available")
    if status != 200 or not available:
        fail("open-repo", f"no repos available: status={status} body={available}")
        return 1
    entry = next((r for r in available if args.repo_substr in r.get("ghqPath", "")), None)
    if entry is None:
        fail("open-repo", f"no repo matching {args.repo_substr!r}")
        return 1
    repo_id = entry["id"]
    status, repo = api(base, "POST", f"/api/repos/{repo_id}/open", timeout=60)
    if status != 200:
        fail("open-repo", f"open failed: status={status} body={repo}")
        return 1
    branch = repo["openBranches"][0]
    branch_id = branch["id"]
    tab_ids = {t["id"] for t in branch["tabSet"]["tabs"]}
    if set(AGENTS) <= tab_ids:
        ok("tabs-seeded", f"agent tabs present: {sorted(tab_ids)}")
    else:
        fail("tabs-seeded", f"codex/opencode tabs missing: {sorted(tab_ids)}")
        return 1

    status, rt = api(base, "PATCH", f"/api/repos/{repo_id}/branches/{branch_id}/runtime",
                     {"kind": "incus-container"}, timeout=180)
    if status != 200:
        fail("runtime-switch", f"status={status} body={rt}")
        return 1
    ok("runtime-switch", "workspace switched to incus-container")

    # [AC-Sfa2bab-1-2] the actual dogfood: a real turn per agent tab, and the
    # Activity Inbox notification that Sfa2bab's first run never received.
    for tab_id in AGENTS:
        label = tab_id.split(":")[0]
        before = len(notifications_for(base, tab_id))
        try:
            seen, text = asyncio.run(drive_tab(base, repo_id, branch_id, tab_id,
                                               PROMPT, MARKER, args.settle,
                                               args.turn_timeout))
        except Exception as e:
            fail(f"AC-Sfa2bab-1-2 ({label}) turn", f"WS attach/drive failed: {e}")
            continue
        if seen:
            ok(f"AC-Sfa2bab-1-2 ({label}) turn", f"{label} answered {MARKER} in-container")
        else:
            fail(f"AC-Sfa2bab-1-2 ({label}) turn",
                 f"{MARKER} never appeared within {args.turn_timeout}s; tail={text[-600:]!r}")

        deadline = time.time() + args.notify_timeout
        got = None
        while time.time() < deadline:
            entries = notifications_for(base, tab_id)
            if len(entries) > before:
                got = entries[-1]
                break
            time.sleep(3)
        if got is not None:
            ok(f"AC-Sfa2bab-1-2 ({label}) notify",
               f"Activity Inbox entry type={got.get('type')!r} title={got.get('title')!r}")
        else:
            fail(f"AC-Sfa2bab-1-2 ({label}) notify",
                 f"no Activity Inbox entry for {tab_id} within {args.notify_timeout}s "
                 "(this is the exact Sfa2bab symptom Sc4f091 fixed)")

    # [AC-Sfa2bab-1-3] regression: bash tab still executes commands.
    bash_tab = next((t for t in sorted(tab_ids) if t.startswith("bash")), None)
    if bash_tab is None:
        fail("AC-Sfa2bab-1-3 (bash)", "no bash tab in the tab set")
    else:
        try:
            seen, text = asyncio.run(drive_bash(base, repo_id, branch_id, bash_tab))
            if seen:
                ok("AC-Sfa2bab-1-3 (bash)", "bash tab executed a command in-container")
            else:
                fail("AC-Sfa2bab-1-3 (bash)", f"echo output never observed; tail={text[-400:]!r}")
        except Exception as e:
            fail("AC-Sfa2bab-1-3 (bash)", f"WS attach failed: {e}")

    # claude tab: spawn/attach only. A full authenticated claude turn needs this
    # throwaway VM to hold real credentials, which Sfa2bab deliberately does not
    # do (copying the dev box's live ~/.claude into a disposable VM was rejected
    # there). Attach proving the PTY comes up is the honest scope here — stated
    # explicitly rather than dressed up as a full turn.
    claude_tab = next((t for t in sorted(tab_ids) if t.startswith("claude")), None)
    if claude_tab is None:
        fail("AC-Sfa2bab-1-3 (claude)", "no claude tab in the tab set")
    else:
        try:
            seen, text = asyncio.run(drive_tab(base, repo_id, branch_id, claude_tab,
                                               "", "", 20.0, 1.0))
            if text.strip():
                ok("AC-Sfa2bab-1-3 (claude)", "claude tab PTY attached and painted (turn NOT attempted: no creds in VM)")
            else:
                fail("AC-Sfa2bab-1-3 (claude)", "claude tab produced no output on attach")
        except Exception as e:
            fail("AC-Sfa2bab-1-3 (claude)", f"WS attach failed: {e}")

    print("\n=== summary ===", flush=True)
    print(f"pass={len(_passes)} fail={len(_failures)}", flush=True)
    if _failures:
        print("FAILED: " + ", ".join(_failures), flush=True)
        return 1
    print("ALL PASS", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
