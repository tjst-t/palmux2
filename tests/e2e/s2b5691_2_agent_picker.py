#!/usr/bin/env python3
"""Sprint S2b5691 Story 2 — real codex/opencode E2E (AC-S2b5691-2-2/2-3).

Unlike s2b5691_2_agent_picker_mock.py (no real agent process), this file
requires REAL codex-cli / real opencode binaries and drives them through the
ACTUAL web UI (Playwright, headless Chromium against the real agent-tui
xterm.js renderer) — proving the PTY WS attach + a real trivial turn
completing, and the Activity Inbox / notify-capability generalization, are
reachable end-to-end from the browser, not just over raw HTTP.

Acceptance criteria covered:
  [AC-S2b5691-2-2] A codex tab and an opencode tab both: (a) actually
                    connect their PTY WS (status leaves "connecting"), and
                    (b) complete one real turn for a trivial prompt — proven
                    via the real Claude-Code-hook-style notify round trip
                    each adapter's own turn-completion hook fires
                    (`cmd/palmux hook --agent=<kind>` -> POST /api/notify ->
                    events WS -> Activity Inbox), which is both the
                    documented notify capability AND a robust,
                    non-ANSI-parsing turn-completion signal.
  [AC-S2b5691-2-3] Activity Inbox correctly attributes the notification to
                    the originating tab/agent name ("Open Codex" / "Open
                    opencode"), and the notify-capability badge matches each
                    adapter's real Capabilities().Notify (codex=turn_end ->
                    badge visible; opencode=full -> no badge).

Runs a fully self-contained, isolated instance (own --config-dir/--addr/
--tmux-prefix/throwaway repo — same pattern as
tests/acceptance/s2b5691_codex_opencode_incontainer.py and the maultiagent
reference's sdec0a7_multiagent.py hermetic_palmux2()) so it never touches
the current Claude session's own palmux2/tmux (see CLAUDE.md "palmux2 自身の
中で palmux2 を開発するときの注意"). Runtime is plain HOST (no incus) — the
GUI spec's edge_cases note this AC applies unconditionally on host exec, and
host keeps the test fast/dependency-light.

codex isolation: a throwaway $CODEX_HOME is seeded with a COPY of this
host's real ~/.codex/auth.json (so the real ChatGPT-auth session works) plus
a config.toml pre-trusting the throwaway repo path (avoids codex's
interactive "trust this folder?" first-run dialog) and
--dangerously-bypass-approvals-and-sandbox (avoids per-command approval
prompts) — the real user's ~/.codex is never written to.
opencode isolation: opencode has no per-directory trust registry; --auto
(auto-approve permissions) is enough. It reads the real
~/.local/share/opencode/auth.json directly (read-only) — no isolation
needed.

Env overrides: PALMUX2_S2B5691_2_ADDR (default 127.0.0.1:18985), turn
timeout PALMUX2_S2B5691_2_TURN_TIMEOUT_S (default 120).

Cleanup: always runs (best-effort), even on failure.

Run:
    python3 tests/e2e/s2b5691_2_agent_picker.py

Exit codes: 0 = ALL PASS. 1 = failure. 2 = SKIP (codex/opencode not on PATH —
per DESIGN_PRINCIPLES priority_rule 0 this must not be silently treated as a
pass; it means AC-S2b5691-2-2 needs a manual run on a host that has them).
"""
from __future__ import annotations

import json
import os
import random
import shutil
import signal
import string
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

sys.path.insert(0, os.path.dirname(__file__))

REPO_ROOT = Path(__file__).resolve().parents[2]
PLAYWRIGHT_TIMEOUT = 20_000  # ms
TURN_TIMEOUT_S = float(os.environ.get("PALMUX2_S2B5691_2_TURN_TIMEOUT_S", "120"))
ADDR = os.environ.get("PALMUX2_S2B5691_2_ADDR", "127.0.0.1:18985")
BASE = f"http://{ADDR}"
HOME = os.path.expanduser("~")
SUFFIX = "".join(random.choices(string.ascii_lowercase + string.digits, k=6))
REPO_NAME = f"s2b5691-2-tw{SUFFIX}"
REPO_DIR = os.path.join(HOME, "ghq", "github.com", "local", REPO_NAME)
TMUX_PREFIX = f"_pmx_s2b5691_2_{SUFFIX}_"

_FAILED: list[str] = []


def fail(name: str, msg: str) -> None:
    print(f"FAIL [{name}] {msg}", file=sys.stderr)
    _FAILED.append(name)


def ok(name: str, msg: str = "") -> None:
    print(f"PASS [{name}] {msg or 'OK'}")


def sh(*args: str, timeout: int = 30) -> tuple[int, str]:
    p = subprocess.run(args, capture_output=True, text=True, timeout=timeout,
                        stdin=subprocess.DEVNULL)
    return p.returncode, (p.stdout + p.stderr)


def api(method: str, path: str, body: dict | None = None, timeout: int = 20):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(BASE + path, data=data, method=method,
                                  headers={"Content-Type": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=timeout) as r:
            raw = r.read()
            return r.status, (json.loads(raw) if raw else None)
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        try:
            return exc.code, json.loads(raw) if raw else None
        except json.JSONDecodeError:
            return exc.code, raw.decode(errors="replace")


def _get_playwright():
    try:
        from playwright.sync_api import sync_playwright
        return sync_playwright
    except ImportError:
        print("SKIP: playwright not installed")
        sys.exit(2)


def main() -> int:  # noqa: C901
    if shutil.which("codex") is None or shutil.which("opencode") is None:
        print("SKIP (MANUAL SMOKE REQUIRED): this host lacks real codex/opencode — "
              "per DESIGN_PRINCIPLES priority_rule 0 this scenario must not be "
              "silently mocked/skipped-as-pass; run this on a host that has them.",
              file=sys.stderr)
        return 2

    sync_playwright = _get_playwright()

    tmp_cfg = tempfile.mkdtemp(prefix="palmux2-s2b5691-2-cfg-")
    codex_home = tempfile.mkdtemp(prefix="palmux2-s2b5691-2-codexhome-")
    binary_path = os.path.join(tmp_cfg, "palmux2-s2b5691-2")
    proc: subprocess.Popen | None = None

    try:
        # 1. Build the current worktree's binary (own copy, avoids clashing
        # with any other bin/palmux in flight).
        print("building palmux2 binary...")
        rc, out = sh("go", "build", "-o", binary_path, "./cmd/palmux", timeout=180)
        if rc != 0:
            fail("build", out[-2000:])
            return 1
        ok("build", binary_path)

        # 2. Throwaway repo.
        os.makedirs(REPO_DIR, exist_ok=True)
        sh("git", "-C", REPO_DIR, "init", "-q", "-b", "main")
        sh("git", "-C", REPO_DIR, "config", "user.email", "test@example.com")
        sh("git", "-C", REPO_DIR, "config", "user.name", "test")
        with open(os.path.join(REPO_DIR, "README.md"), "w") as f:
            f.write("s2b5691-2 throwaway\n")
        sh("git", "-C", REPO_DIR, "add", "README.md")
        sh("git", "-C", REPO_DIR, "commit", "-q", "-m", "init")

        # 3. Isolated $CODEX_HOME: copy the real ChatGPT auth (read-only
        # reuse of this host's already-authenticated session) and pre-trust
        # the throwaway repo path so codex's TUI never shows the "trust this
        # folder?" first-run dialog (which a blind Playwright keystroke
        # script cannot reliably answer).
        real_auth = os.path.join(HOME, ".codex", "auth.json")
        if os.path.isfile(real_auth):
            shutil.copy(real_auth, os.path.join(codex_home, "auth.json"))
        else:
            print("SKIP (MANUAL SMOKE REQUIRED): ~/.codex/auth.json not found — "
                  "codex is installed but not logged in on this host.",
                  file=sys.stderr)
            return 2
        with open(os.path.join(codex_home, "config.toml"), "w") as f:
            f.write(f'[projects."{REPO_DIR}"]\ntrust_level = "trusted"\n')

        # 4. config.toml enabling codex + opencode with real bypass/auto
        # flags so the interactive TUI never blocks on a confirmation
        # dialog a scripted E2E can't answer.
        with open(os.path.join(tmp_cfg, "config.toml"), "w") as f:
            f.write(
                '[agents.codex]\n'
                'command = "codex"\n'
                'args = ["--dangerously-bypass-approvals-and-sandbox"]\n\n'
                '[agents.opencode]\n'
                'command = "opencode"\n'
                'args = ["--auto"]\n'
            )

        # 5. Start the throwaway instance (host runtime, no incus).
        env = dict(os.environ)
        env["CODEX_HOME"] = codex_home
        proc = subprocess.Popen(
            [binary_path, "--addr", ADDR, "--config-dir", tmp_cfg,
             "--tmux-prefix", TMUX_PREFIX],
            stdout=open(os.path.join(tmp_cfg, "server.log"), "w"),
            stderr=subprocess.STDOUT,
            env=env,
        )
        deadline = time.time() + 30
        healthy = False
        while time.time() < deadline:
            try:
                urllib.request.urlopen(BASE + "/api/health", timeout=2)
                healthy = True
                break
            except Exception:  # noqa: BLE001
                time.sleep(1)
        if not healthy:
            fail("startup", "instance never became healthy")
            return 1
        ok("startup", f"throwaway instance up at {BASE}")

        status, agents = api("GET", "/api/agents")
        kinds = {a["kind"]: a for a in agents} if agents else {}
        if status != 200 or {"claude", "codex", "opencode"} > set(kinds):
            fail("setup", f"GET /api/agents unexpected: status={status} kinds={list(kinds)}")
            return 1
        if kinds["codex"]["capabilities"]["notify"] != "turn_end":
            fail("setup", f"codex notify capability = {kinds['codex']['capabilities']!r}, want turn_end")
            return 1
        if kinds["opencode"]["capabilities"]["notify"] != "full":
            fail("setup", f"opencode notify capability = {kinds['opencode']['capabilities']!r}, want full")
            return 1
        ok("setup", "GET /api/agents: codex=turn_end, opencode=full (matches badge expectations)")

        status, available = api("GET", "/api/repos/available")
        entry = next((r for r in available if r["ghqPath"].endswith(REPO_NAME)), None)
        if entry is None:
            fail("open-repo", f"throwaway repo not found: {available!r}")
            return 1
        repo_id = entry["id"]
        status, repo = api("POST", f"/api/repos/{urllib.parse.quote(repo_id)}/open")
        if status != 200:
            fail("open-repo", f"POST open: status={status} body={repo!r}")
            return 1
        branch = repo["openBranches"][0]
        branch_id = branch["id"]
        tab_types = {t["id"] for t in branch["tabSet"]["tabs"]}
        if not {"codex:codex", "opencode:opencode"} <= tab_types:
            fail("tabs-seeded", f"codex/opencode tabs missing: {sorted(tab_types)}")
            return 1
        ok("tabs-seeded", f"codex/opencode auto-seeded: {sorted(tab_types)}")

        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                page = browser.new_page()

                run_turn_and_check_inbox(
                    page, repo_id, branch_id, tab_id="codex:codex",
                    kind="codex", display_name="Codex", notify_level="turn_end",
                    prompt="Reply with exactly the single word PALMUXOK and nothing else.\n",
                )
                run_turn_and_check_inbox(
                    page, repo_id, branch_id, tab_id="opencode:opencode",
                    kind="opencode", display_name="opencode", notify_level="full",
                    prompt="Reply with exactly the single word PALMUXOK and nothing else.\n",
                )
            finally:
                browser.close()

    finally:
        print("\n--- cleanup ---")
        if proc is not None and proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
        sh("pkill", "-f", binary_path if 'binary_path' in dir() else "palmux2-s2b5691-2")
        rc, out = sh("tmux", "list-sessions", "-F", "#{session_name}")
        for name in out.splitlines():
            if name.startswith(TMUX_PREFIX):
                sh("tmux", "kill-session", "-t", name)
        if os.path.isdir(REPO_DIR):
            shutil.rmtree(REPO_DIR, ignore_errors=True)
        shutil.rmtree(tmp_cfg, ignore_errors=True)
        shutil.rmtree(codex_home, ignore_errors=True)

    if _FAILED:
        print(f"\nFAILED: {len(_FAILED)} case(s): {', '.join(_FAILED)}", file=sys.stderr)
        return 1
    print("\nALL PASS")
    return 0


def run_turn_and_check_inbox(page, repo_id: str, branch_id: str, *, tab_id: str,
                              kind: str, display_name: str, notify_level: str,
                              prompt: str) -> None:
    name = f"AC-S2b5691-2-2/2-3 ({kind})"
    url = (f"{BASE}/{urllib.parse.quote(repo_id, safe='')}"
           f"/{urllib.parse.quote(branch_id, safe='')}/{urllib.parse.quote(tab_id, safe='')}")
    page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")

    # [AC-S2b5691-2-2 part a]: the agent-tui renderer mounts and its PTY WS
    # actually connects (status leaves "connecting").
    term = page.locator(
        "[data-testid='agent-tui-terminal'], [data-testid='claude-tui-terminal']")
    try:
        term.first.wait_for(timeout=PLAYWRIGHT_TIMEOUT)
    except Exception:  # noqa: BLE001
        fail(name, f"{kind}: agent-tui terminal never mounted")
        return
    status_el = page.locator(
        "[data-testid='agent-tui-status'], [data-testid='claude-tui-status']").first
    connected = False
    deadline = time.time() + 30
    while time.time() < deadline:
        try:
            txt = status_el.inner_text(timeout=1000)
        except Exception:  # noqa: BLE001
            txt = ""
        if txt in ("connected", "streaming"):
            connected = True
            break
        time.sleep(1)
    if not connected:
        fail(name, f"{kind}: PTY WS never reached connected/streaming (last status seen)")
        return
    ok(f"{name} PTY connect", f"{kind} WS connected")

    # [AC-S2b5691-2-3]: notify-capability badge matches the real adapter.
    row = page.locator(f"[data-tab-id='{tab_id}']")
    badge = row.locator("[data-testid='notify-capability-badge']")
    if notify_level == "full":
        if badge.count() != 0:
            fail(name, f"{kind} (notify=full) unexpectedly shows a notify-capability badge")
        else:
            ok(f"{name} badge", f"{kind} (full) shows no notify-capability badge")
    else:
        if badge.count() != 1:
            fail(name, f"{kind} (notify={notify_level}) missing notify-capability badge")
        else:
            level = badge.get_attribute("data-notify-level")
            if level != notify_level:
                fail(name, f"{kind} badge data-notify-level={level!r}, want {notify_level!r}")
            else:
                ok(f"{name} badge", f"{kind} badge shows notify-level={level}")

    # [AC-S2b5691-2-2 part b]: a trivial prompt completes one real turn,
    # confirmed via the real notify hook -> Activity Inbox round trip
    # (robust; avoids parsing ANSI-rendered TUI output).
    term.first.click()
    # Poll until the TUI has actually painted its splash/input UI before
    # typing, rather than a fixed sleep — found empirically that codex
    # renders near-instantly but opencode's boot (provider handshake, splash
    # screen) can take ~5s+, and typing into a still-blank screen loses the
    # keystrokes entirely (nothing is listening for them yet).
    for _ in range(20):
        if len(term.first.inner_text().strip()) > 20:
            break
        page.wait_for_timeout(1000)
    page.keyboard.type(prompt.rstrip("\n"))
    # A short pause between the last typed character and Enter is required —
    # found empirically (root-caused via a debug rig that dumped the live
    # terminal text after send): pressing Enter in the SAME tick as the last
    # keystroke lets xterm.js/the browser coalesce them, and the '\r' lands
    # in codex/opencode's line-editor as a literal character appended to the
    # draft rather than a distinct submit keystroke — the terminal renders
    # the full prompt sitting unsubmitted in the input box forever, and no
    # turn ever starts server-side. Splitting type() and press("Enter") into
    # separate ticks (matching how a human actually types) fixes it.
    page.wait_for_timeout(500)
    page.keyboard.press("Enter")
    # Give the Enter keystroke's WS frame time to actually flush to the PTY
    # before we navigate away below — page.goto() tears down the JS
    # execution context / WebSocket immediately, and keyboard.press()
    # returns as soon as the DOM event dispatches, not once xterm's onData
    # -> ws.send() has gone out over the wire. Racing the two silently
    # drops the Enter keystroke.
    page.wait_for_timeout(1500)

    found_entry = False
    deadline = time.time() + TURN_TIMEOUT_S
    while time.time() < deadline:
        # Reload the branch view (re-navigates, forcing a fresh WS
        # subscription pickup) so the bell badge / inbox state is current,
        # then open the inbox and look for our tab's entry.
        page.goto(
            f"{BASE}/{urllib.parse.quote(repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}/claude",
            timeout=PLAYWRIGHT_TIMEOUT, wait_until="load",
        )
        try:
            page.click("button[aria-label='Activity inbox']", timeout=5000)
            page.wait_for_selector("[data-testid='activity-inbox-event-list']", timeout=5000)
        except Exception:  # noqa: BLE001
            time.sleep(3)
            continue
        open_btn = page.locator(
            "[data-testid='activity-inbox-open-agent']", has_text=f"Open {display_name}")
        if open_btn.count() > 0:
            found_entry = True
            # A live WS notification event can re-render the Inbox list
            # between the count() check above and the click below,
            # detaching the pinned element handle — re-locate fresh right
            # before clicking (with its own short retry) rather than
            # reusing the possibly-stale `open_btn.first` reference.
            for _attempt in range(3):
                try:
                    page.locator(
                        "[data-testid='activity-inbox-open-agent']",
                        has_text=f"Open {display_name}",
                    ).first.click(timeout=10000)
                    break
                except Exception:  # noqa: BLE001
                    if _attempt == 2:
                        raise
                    page.wait_for_timeout(500)
            page.wait_for_timeout(800)
            landed = urllib.parse.unquote(page.url).split(f"{branch_id}/")[-1]
            if landed != tab_id:
                fail(name, f"'Open {display_name}' navigated to {landed!r}, want {tab_id!r}")
            else:
                ok(name, f"{kind} turn completed; Activity Inbox showed "
                          f"'Open {display_name}' and routed to {tab_id}")
            break
        time.sleep(3)

    if not found_entry:
        fail(name, f"{kind}: no 'Open {display_name}' Activity Inbox entry appeared "
                    f"within {TURN_TIMEOUT_S}s of sending the prompt")


if __name__ == "__main__":
    sys.exit(main())
