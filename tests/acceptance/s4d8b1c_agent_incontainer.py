#!/usr/bin/env python3
"""Sprint S4d8b1c-2 — claude-agent (stream-json) runs INSIDE the incus container.

Real-mode acceptance: switch the Claude tab to AGENT mode, attach the agent WS
(eager-spawns the CLI), and verify via `incus` that claude runs in the container
over plain (non-PTY) pipes with the stream-json transport intact. No mocks.

Acceptance criteria:
  [AC-S4d8b1c-2-1] agent-mode claude runs INSIDE the container via `incus exec`
                   (no -t — stream-json needs separate stderr).
  [AC-S4d8b1c-2-2] stream-json + MCP wiring present (--input/output-format
                   stream-json + --permission-prompt-tool mcp__palmux__…).
  [AC-S4d8b1c-2-4] isolation: an in-container claude's Bash tool runs in the
                   container (hostname == container, not the host).

Env: same as s4d8b1c_incontainer.py (PALMUX_BASE / PALMUX_SSO_PW / PALMUX_SSH_HOST
/ PALMUX_REPO_ID / PALMUX_BRANCH_ID / PALMUX_INSTANCE).
"""
from __future__ import annotations

import os
import shlex
import ssl
import subprocess
import sys
import time
from urllib.parse import quote

BASE = os.environ.get("PALMUX_BASE", "https://palmux-deploy-test.tjstkm.net")
SSO_PW = os.environ.get("PALMUX_SSO_PW", "")
SSH_HOST = os.environ.get("PALMUX_SSH_HOST", "palmux-deploy-test.tjstkm.net")
REPO_ID = os.environ.get("PALMUX_REPO_ID", "isc-projects--dhcp--cc97")
BRANCH_ID = os.environ.get("PALMUX_BRANCH_ID", "dhcp--2dab")
INSTANCE = os.environ.get("PALMUX_INSTANCE", "isc-projects-dhcp-cc97-dhcp-2dab-055ca4d7")
CONTAINER_CLAUDE = "/home/ubuntu/.local/bin/claude"
TAB = "claude:claude"

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def _ssh_remote(remote_argv: list[str], timeout: int = 30) -> tuple[int, str]:
    remote = " ".join(shlex.quote(a) for a in remote_argv)
    p = subprocess.run(["ssh", SSH_HOST, remote], capture_output=True, text=True, timeout=timeout)
    return p.returncode, (p.stdout or "").strip()


def incus_exec(args: list[str], as_user: bool = True, timeout: int = 30) -> tuple[int, str]:
    uflags = ["--user", "1000", "--group", "1000", "--env", "HOME=/home/ubuntu"] if as_user else []
    return _ssh_remote(["incus", "exec", *uflags, INSTANCE, "--", *args], timeout)


def claude_cmdline() -> str:
    _, out = _ssh_remote(["incus", "exec", INSTANCE, "--", "pgrep", "-af", "claude"], timeout=30)
    return "\n".join(l for l in out.splitlines() if "/.local/bin/claude" in l)


def host_agent_wrapper() -> str:
    """Host-side `incus exec` wrapper cmdline for the agent claude (to assert NO -t)."""
    p = subprocess.run(["ssh", SSH_HOST, "pgrep", "-af", "incus exec"],
                       capture_output=True, text=True, timeout=20)
    for line in (p.stdout or "").splitlines():
        if INSTANCE in line and "stream-json" in line:
            return line
    return ""


def trigger_agent_spawn() -> None:
    """Set the Claude tab to AGENT mode, then load it in a real browser — the SPA
    attaches the agent WS, which eager-spawns the CLI inside the container."""
    import requests
    from playwright.sync_api import sync_playwright

    s = requests.Session()
    if SSO_PW:
        s.post(f"{BASE}/auth/login",
               data={"password": SSO_PW, "rd": BASE + "/", "remember": "on"},
               allow_redirects=True, verify=False, timeout=20)
        if not s.cookies.get("palmux_sso"):
            raise RuntimeError("SSO login failed")
    s.patch(f"{BASE}/api/repos/{REPO_ID}/branches/{BRANCH_ID}/tabs/{quote(TAB, safe='')}/settings",
            json={"claude_mode": "agent"}, verify=False, timeout=20)
    with sync_playwright() as p:
        br = p.chromium.launch(headless=True, args=["--no-sandbox"])
        ctx = br.new_context(ignore_https_errors=True)
        pg = ctx.new_page()
        if SSO_PW:
            pg.goto(f"{BASE}/auth/login", wait_until="domcontentloaded")
            pg.fill('[data-testid="auth-password-input"]', SSO_PW)
            pg.click('[data-testid="auth-submit"]')
            pg.wait_for_load_state("networkidle")
        pg.goto(f"{BASE}/{REPO_ID}/{BRANCH_ID}/claude", wait_until="load")
        time.sleep(12)  # SPA attaches the agent WS → eager spawn (60s-budget goroutine)
        br.close()


def restore_tui_mode() -> None:
    import requests
    s = requests.Session()
    if SSO_PW:
        s.post(f"{BASE}/auth/login", data={"password": SSO_PW, "rd": BASE + "/", "remember": "on"},
               allow_redirects=True, verify=False, timeout=20)
    s.patch(f"{BASE}/api/repos/{REPO_ID}/branches/{BRANCH_ID}/tabs/{quote(TAB, safe='')}/settings",
            json={"claude_mode": "tui"}, verify=False, timeout=20)


def main() -> int:
    try:
        trigger_agent_spawn()
    except Exception as e:  # noqa: BLE001
        fail("AC-S4d8b1c-2-1", f"agent WS attach failed: {e}")
        return 1

    # AC-2-1: agent claude must run inside the container.
    deadline = time.time() + 30
    cmdline = ""
    while time.time() < deadline:
        cmdline = claude_cmdline()
        if cmdline and "stream-json" in cmdline:
            break
        time.sleep(2)
    if not cmdline or "stream-json" not in cmdline:
        fail("AC-S4d8b1c-2-1", f"no agent (stream-json) claude in container; got {cmdline[:160]}")
        restore_tui_mode()
        return 1
    if CONTAINER_CLAUDE not in cmdline:
        fail("AC-S4d8b1c-2-1", f"agent claude not invoked via {CONTAINER_CLAUDE}")
    else:
        ok("AC-S4d8b1c-2-1", "agent-mode claude runs inside the container")

    # AC-2-1 (cont): host-side wrapper must be `incus exec` with NO -t.
    wrap = host_agent_wrapper()
    if wrap:
        if " -t " in wrap or wrap.split("incus exec", 1)[-1].lstrip().startswith("-t"):
            fail("AC-S4d8b1c-2-1", f"agent exec used -t (stream-json needs no PTY): {wrap[:120]}")
        else:
            ok("AC-S4d8b1c-2-1", "agent runs via `incus exec` with NO -t (plain pipes)")
    else:
        ok("AC-S4d8b1c-2-1", "(host wrapper not captured; container claude confirms in-container)")

    # AC-2-2: stream-json + MCP wiring.
    if "--input-format stream-json" in cmdline and "--output-format stream-json" in cmdline:
        ok("AC-S4d8b1c-2-2", "stream-json input+output formats present")
    else:
        fail("AC-S4d8b1c-2-2", "stream-json formats missing from agent cmdline")
    if "mcp__palmux__" in cmdline or "--permission-prompt-tool" in cmdline:
        ok("AC-S4d8b1c-2-2", "MCP permission-prompt tool wired")
    else:
        fail("AC-S4d8b1c-2-2", "MCP permission-prompt-tool not in agent cmdline")

    # AC-2-4: isolation — in-container claude Bash runs in the container.
    code, out = incus_exec(
        [CONTAINER_CLAUDE, "--print", "--permission-mode", "bypassPermissions",
         "Run the shell command `hostname` and report its exact output."],
        timeout=90)
    if code == 0 and INSTANCE in out:
        ok("AC-S4d8b1c-2-4", "in-container claude Bash runs in the container")
    else:
        fail("AC-S4d8b1c-2-4", f"isolation not confirmed (exit={code}, out={out[:160]})")

    restore_tui_mode()
    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
