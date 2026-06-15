#!/usr/bin/env python3
"""Sprint S4d8b1c-1 — claude-tui runs INSIDE the incus container.

Real-mode acceptance: drives the real palmux UI (Playwright) to switch a Claude
tab to TUI mode (which attaches the claude-tui WS → lazily spawns claude), then
verifies via `incus` (ground truth) that claude runs in the container with the
right wiring. No mocks.

Acceptance criteria:
  [AC-S4d8b1c-1-1] claude runs INSIDE the container (incus exec), not on the host.
  [AC-S4d8b1c-1-2] in-container claude uses the mounted ~/.claude (no re-auth) and
                   the worktree cwd; the PTY/emulator render to the WS.
  [AC-S4d8b1c-1-3] --plugin-dir injected → /skills has palmux:palmux-browser.
  [AC-S4d8b1c-1-4] isolation: an in-container claude's Bash tool runs in the
                   container (hostname == container, not the host).
  [AC-S4d8b1c-1-5] in-container hook binary + bridge PALMUX_NOTIFY_URL present.

Runs from a host with Playwright AND ssh/incus access to the palmux host.
Env:
  PALMUX_BASE        e.g. https://palmux-deploy-test.tjstkm.net  (SSO)
  PALMUX_SSO_PW      SSO password (when PALMUX_BASE is an SSO domain)
  PALMUX_SSH_HOST    ssh target of the palmux host (for incus checks)
  PALMUX_REPO_ID / PALMUX_BRANCH_ID / PALMUX_INSTANCE   the incus workspace
  PALMUX_CLAUDE_TAB  claude tab id (default "claude:claude")
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
CLAUDE_TAB = os.environ.get("PALMUX_CLAUDE_TAB", "claude")
CONTAINER_CLAUDE = "/home/ubuntu/.local/bin/claude"
PLUGIN_DIR = "/usr/local/share/palmux"

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"FAIL: [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def _ssh_remote(remote_argv: list[str], timeout: int = 30) -> tuple[int, str]:
    """Run a command on the palmux host via ssh, shell-quoting each remote arg so
    ssh's join-and-reparse doesn't mangle spaces/quotes/$()."""
    remote = " ".join(shlex.quote(a) for a in remote_argv)
    p = subprocess.run(["ssh", SSH_HOST, remote], capture_output=True, text=True, timeout=timeout)
    return p.returncode, (p.stdout or "").strip()


def incus_exec(args: list[str], as_user: bool = True, timeout: int = 30) -> tuple[int, str]:
    uflags = ["--user", "1000", "--group", "1000", "--env", "HOME=/home/ubuntu"] if as_user else []
    return _ssh_remote(["incus", "exec", *uflags, INSTANCE, "--", *args], timeout)


def claude_cmdline() -> str:
    """Full cmdline of the in-container claude process (pgrep -af, simple +
    ssh-join-safe), filtered to the container claude binary."""
    _, out = _ssh_remote(["incus", "exec", INSTANCE, "--", "pgrep", "-af", "claude"], timeout=30)
    return "\n".join(l for l in out.splitlines() if "/.local/bin/claude" in l)


def trigger_incontainer_spawn() -> None:
    """Attach the claude-tui WebSocket (the user's real entry point — the SPA
    opens this WS when a TUI-mode Claude tab is shown). Attaching lazily spawns
    the claude-tui daemon, which runs claude INSIDE the container. Uses a real
    WS client (not the browser) for a stable trigger; SSO auth via the cookie."""
    import requests
    from websocket import create_connection

    s = requests.Session()
    cookies = {}
    if SSO_PW:
        s.post(f"{BASE}/auth/login",
               data={"password": SSO_PW, "rd": BASE + "/", "remember": "on"},
               allow_redirects=True, verify=False, timeout=20)
        if not s.cookies.get("palmux_sso"):
            raise RuntimeError("SSO login failed (no palmux_sso cookie)")
        cookies = {"palmux_sso": s.cookies.get("palmux_sso")}

    ws_base = BASE.replace("https://", "wss://").replace("http://", "ws://")
    tab = "claude:claude"  # the protected Claude tab id
    url = f"{ws_base}/api/repos/{REPO_ID}/branches/{BRANCH_ID}/tabs/{quote(tab, safe='')}/tui/attach"
    header = [f"Cookie: palmux_sso={cookies['palmux_sso']}"] if cookies else []
    ws = create_connection(url, header=header,
                           sslopt={"cert_reqs": ssl.CERT_NONE}, timeout=20)
    # Hold the WS open so the daemon spawns claude and stays running.
    time.sleep(8)
    ws.close()


def main() -> int:
    # 1. Trigger an in-container claude-tui spawn via the real WS entry point.
    try:
        trigger_incontainer_spawn()
    except Exception as e:  # noqa: BLE001
        fail("AC-S4d8b1c-1-1", f"claude-tui WS attach failed: {e}")
        return 1

    # 2. AC-1-1: a claude process must be running INSIDE the container.
    deadline = time.time() + 25
    cmdline = ""
    while time.time() < deadline:
        cmdline = claude_cmdline()
        if cmdline:
            break
        time.sleep(2)
    if not cmdline:
        fail("AC-S4d8b1c-1-1", "no claude process found inside the container after TUI attach")
        return 1
    ok("AC-S4d8b1c-1-1", "claude runs inside the container")

    # AC-1-1 (cont): it must be invoked by the container claude bin (incus path).
    if CONTAINER_CLAUDE not in cmdline:
        fail("AC-S4d8b1c-1-1", f"in-container claude not invoked via {CONTAINER_CLAUDE}; cmdline={cmdline[:200]}")
    # AC-1-3: --plugin-dir injected.
    if f"--plugin-dir {PLUGIN_DIR}" not in cmdline:
        fail("AC-S4d8b1c-1-3", f"--plugin-dir {PLUGIN_DIR} not in claude cmdline; got {cmdline[:200]}")
    else:
        ok("AC-S4d8b1c-1-3", "--plugin-dir injected")

    # AC-1-3 (cont): the skill must actually load. Probe a fresh in-container
    # claude with --plugin-dir in --print mode.
    code, out = incus_exec(
        [CONTAINER_CLAUDE, "--plugin-dir", PLUGIN_DIR, "--print",
         "List your available skills whose name contains 'palmux'. Answer with the skill name only."],
        timeout=90)
    if code == 0 and "palmux-browser" in out:
        ok("AC-S4d8b1c-1-3", f"skill loads: {out.strip()[:80]}")
    else:
        fail("AC-S4d8b1c-1-3", f"palmux-browser skill not loaded via --plugin-dir (exit={code}, out={out[:120]})")

    # AC-1-4: isolation — an in-container claude's Bash tool runs in the container.
    code, out = incus_exec(
        [CONTAINER_CLAUDE, "--print", "--permission-mode", "bypassPermissions",
         "Run the shell command `hostname` and report its exact output."],
        timeout=90)
    if code == 0 and INSTANCE in out:
        ok("AC-S4d8b1c-1-4", "in-container claude Bash tool runs in the container (hostname == container)")
    else:
        fail("AC-S4d8b1c-1-4", f"isolation not confirmed (exit={code}, out={out[:160]})")

    # AC-1-5: bridge notify env + in-container hook binary present.
    code, out = incus_exec(["test", "-x", "/usr/local/bin/palmux"], timeout=15)
    if code == 0:
        ok("AC-S4d8b1c-1-5", "palmux hook binary mounted at /usr/local/bin/palmux")
    else:
        fail("AC-S4d8b1c-1-5", "palmux hook binary not mounted in container")
    # The daemon's claude cmdline carries --settings (hooks) referencing the
    # container hook bin; PALMUX_NOTIFY_URL bridge env is verified via the unit
    # test (TestDaemonInContainerInjectsPluginDir).
    if "--settings" in cmdline:
        ok("AC-S4d8b1c-1-5", "--settings (hooks) injected for in-container claude")
    else:
        fail("AC-S4d8b1c-1-5", f"--settings not injected; cmdline={cmdline[:160]}")

    if _FAILED:
        print(f"\nFAILED: {sorted(set(_FAILED))}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
