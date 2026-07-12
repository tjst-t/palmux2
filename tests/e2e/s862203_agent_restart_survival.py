#!/usr/bin/env python3
"""Sprint S862203 Story 3 — AC-S862203-3-2 REAL-MACHINE E2E.

Proves, against a real running palmux2 (`make serve INSTANCE=dev`, REAL
claude, real Anthropic API calls — never the host palmux2) that across a
palmux2 restart:

  (a) an in-flight turn's transcript is restored with NO loss/duplication
      (the pipe-mode ptyhost + OffsetStore replay reconstruction, S862203-3
      task 2), and
  (b) a permission request that was OUTSTANDING at the moment of restart is
      reconstructed as a PENDING permission in the UI, answerable, and the
      gated tool executes to completion once answered.

FOOTGUN (documented in the S862203-1 spike and this story's task brief):
this host's `~/.claude/settings.json` sets `permissions.defaultMode:
"bypassPermissions"`. Going through the real Manager/Agent stack (not the
spike's raw NewClient bypass) already defaults new sessions to
`--permission-mode auto` (config.DefaultClaudePermissionMode), which
should still gate Bash — but to remove all ambiguity and match the task's
explicit instruction, this test FORCES the session's permission mode to
"manual" via the Permission-mode PillSelect in the composer BEFORE sending
any message, and hard-fails if no control_request/permission prompt is
observed (never silently "passes" having tested nothing).

This is a HERMETIC-ish setup: a throwaway fixture repo (via _fixture.py)
opened against the isolated `make serve INSTANCE=dev` instance running in
THIS worktree — never the host palmux2, matching CLAUDE.md's dev workflow
and this story's explicit constraint.

Exit code 0 = PASS. Run standalone:
  python3 tests/e2e/s862203_agent_restart_survival.py

Writes docs/sprint-logs/S862203/e2e-S862203-3.json with the result record.
"""
from __future__ import annotations

import json
import os
import signal
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid
from pathlib import Path
from typing import Any

sys.path.insert(0, os.path.dirname(__file__))

REPO_ROOT = Path(__file__).resolve().parents[2]
RESULT_PATH = REPO_ROOT / "docs/sprint-logs/S862203/e2e-S862203-3.json"
# INSTANCE isolates this test's dev palmux2 (portman name / tmux prefix / PID
# + log + env files) from the host palmux2 AND from any other concurrently
# running dev instance on a shared box (e.g. sibling autopilot worktrees).
# Defaults to "dev" (the documented CLAUDE.md dev rig); override with
# PALMUX_E2E_INSTANCE=<name> to run isolated when "dev" is already taken.
INSTANCE = os.environ.get("PALMUX_E2E_INSTANCE", "dev")
PID_FILE = REPO_ROOT / f"tmp/palmux-{INSTANCE}.pid"
ENV_FILE = REPO_ROOT / f"tmp/palmux-{INSTANCE}.portman.env"
LOG_FILE = REPO_ROOT / f"tmp/palmux-{INSTANCE}.log"
TMUX_PREFIX = f"_pmx_{INSTANCE}_"
PORT_ENV_KEY = f"PALMUX2_{INSTANCE.upper().replace('-', '_')}_PORT"
BIN = REPO_ROOT / "bin/palmux"
PLAYWRIGHT_TIMEOUT = 20_000


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def passed(msg: str) -> None:
    print(f"PASS: {msg}")


def run(*args: str, check: bool = True) -> subprocess.CompletedProcess:
    print(f"$ {' '.join(args)}")
    r = subprocess.run(args, cwd=REPO_ROOT, capture_output=True, text=True)
    if check and r.returncode != 0:
        fail(f"command failed (rc={r.returncode}): {' '.join(args)}\n{r.stdout[-3000:]}\n{r.stderr[-3000:]}")
    return r


def read_port() -> int:
    if not ENV_FILE.is_file():
        fail(f"{ENV_FILE} not found — dev instance never started?")
    for line in ENV_FILE.read_text().splitlines():
        if line.startswith(PORT_ENV_KEY + "="):
            return int(line.split("=", 1)[1])
    fail(f"{PORT_ENV_KEY} not found in {ENV_FILE}")
    raise AssertionError("unreachable")


def wait_listening(port: int, timeout_s: float = 60.0) -> None:
    deadline = time.monotonic() + timeout_s
    while time.monotonic() < deadline:
        try:
            with urllib.request.urlopen(f"http://localhost:{port}/api/repos", timeout=2) as r:
                if r.status == 200:
                    return
        except (urllib.error.URLError, ConnectionError, OSError):
            pass
        time.sleep(0.2)
    fail(f"palmux2 dev instance did not start listening on {port} within {timeout_s}s")


def ensure_built() -> None:
    """`make serve INSTANCE=dev` (first call only) builds + starts. We want a
    fresh binary with THIS story's code, so always build once up front."""
    r = run("make", "build", check=False)
    if r.returncode != 0:
        fail(f"make build failed:\n{r.stdout[-4000:]}\n{r.stderr[-4000:]}")
    if not BIN.is_file():
        fail("bin/palmux missing after `make build`")


def start_dev_fresh() -> int:
    """`make serve INSTANCE=dev` — first start (also (re)builds, portman
    leases/keeps the port). Returns the port."""
    run("make", "serve", f"INSTANCE={INSTANCE}")
    port = read_port()
    wait_listening(port)
    return port


def restart_dev_only(port: int) -> None:
    """Restart ONLY the dev instance's process — kill the PID, relaunch the
    SAME prebuilt binary with the SAME flags `make serve INSTANCE=dev`
    itself uses (--config-dir ./tmp --tmux-prefix=_pmx_dev_), reusing the
    SAME portman-leased port. Deliberately does NOT go through `make serve`
    again (that would rebuild, adding tens of seconds of noise unrelated to
    what this AC is testing) and NEVER touches the host palmux2 — this
    process is not even a descendant of the host's tmux-managed session."""
    if not PID_FILE.is_file():
        fail(f"{PID_FILE} missing — dev instance not running?")
    old_pid = int(PID_FILE.read_text().strip())
    print(f"==> Restarting ONLY dev instance (pid={old_pid}, port={port})")
    try:
        os.kill(old_pid, signal.SIGTERM)
    except ProcessLookupError:
        pass
    deadline = time.monotonic() + 15.0
    while time.monotonic() < deadline:
        try:
            os.kill(old_pid, 0)
        except ProcessLookupError:
            break
        time.sleep(0.1)
    else:
        try:
            os.kill(old_pid, signal.SIGKILL)
        except ProcessLookupError:
            pass

    with open(LOG_FILE, "a") as logf:
        proc = subprocess.Popen(
            [
                str(BIN),
                "--addr", f"0.0.0.0:{port}",
                "--config-dir", "./tmp",
                f"--tmux-prefix={TMUX_PREFIX}",
            ],
            cwd=REPO_ROOT,
            stdout=logf,
            stderr=subprocess.STDOUT,
            start_new_session=True,
        )
    PID_FILE.write_text(str(proc.pid) + "\n")
    wait_listening(port, timeout_s=30.0)
    print(f"==> dev instance restarted (new pid={proc.pid})")


def stop_dev() -> None:
    run("make", "serve-stop", f"INSTANCE={INSTANCE}", check=False)


def force_agent_mode(port: int, repo_id: str, branch_id: str) -> None:
    """A fresh tab's frontend-observed default can render the OTHER Claude
    mode (claude-tui, PTY-based) before the backend's persisted claude_mode
    settles — mirrors the established s3f2658_2 pattern of explicitly
    PATCHing the desired mode rather than relying on an implicit default.
    This story's AC is specifically about claude-agent (stream-json)
    transport survival, so agent mode must be explicit and deterministic."""
    req = urllib.request.Request(
        f"http://localhost:{port}/api/repos/{urllib.parse.quote(repo_id)}"
        f"/branches/{urllib.parse.quote(branch_id)}/tabs/claude:claude/settings",
        method="PATCH",
        data=json.dumps({"claude_mode": "agent"}).encode(),
        headers={"Content-Type": "application/json"},
    )
    with urllib.request.urlopen(req, timeout=10) as r:
        if r.status != 200:
            fail(f"PATCH claude_mode=agent failed: {r.status}")


def get_fixture_module(port: int):
    os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
    if "_fixture" in sys.modules:
        del sys.modules["_fixture"]
    import _fixture as fx_mod
    return fx_mod


def get_conv_text(page) -> str:
    return page.evaluate(
        """() => {
            const c = document.querySelector('[data-testid="claude-conversation"]');
            return c?.innerText || '';
        }"""
    ) or ""


def permission_prompt_visible(page) -> str | None:
    return page.evaluate(
        """() => {
            const blocks = document.querySelectorAll('[class*="permission" i], [data-testid*="permission" i]');
            for (const b of blocks) {
                if (b.offsetHeight > 0) return b.innerText?.slice(0, 300);
            }
            const btns = document.querySelectorAll('button');
            for (const b of btns) {
                const t = b.innerText || '';
                if (/allow|deny|approve/i.test(t) && b.offsetHeight > 0) return `BTN: ${t}`;
            }
            return null;
        }"""
    )


def send_failed_visible(page) -> bool:
    return bool(page.evaluate(
        """() => (document.body.innerText || '').includes('Send failed')"""
    ))


def send_with_retry(page, ta, send_btn, prompt: str, timeout_s: float = 90.0) -> None:
    """Fill + click Send, retrying if the backend is still mid-respawn.

    A permission-mode change triggers Agent.respawnClient (kill the old CLI
    — now a real SHUTDOWN round-trip through the pipe-mode ptyhost, which
    can genuinely take several real seconds against a REAL claude process —
    then spawn + Initialize a new one). Sending while that is still in
    flight hits the backend's a.starting guard ("client is already
    starting") — a real, but transient, condition — and the frontend does
    NOT auto-retry a failed send, so it would otherwise sit there forever
    showing a stale error. Retrying here (client-driven, like a real user
    hitting Send again) is the robust way to ride out that window without
    having to predict its exact duration."""
    deadline = time.monotonic() + timeout_s
    attempt = 0
    while time.monotonic() < deadline:
        attempt += 1
        ta.click()
        ta.fill(prompt)
        page.wait_for_timeout(150)
        send_btn.click()
        page.wait_for_timeout(1500)
        if not send_failed_visible(page):
            passed(f"send succeeded on attempt {attempt}")
            return
        print(f"(send attempt {attempt} hit a transient backend-starting error, retrying...)")
        page.wait_for_timeout(2000)
    fail(f"send never succeeded after {timeout_s}s of retries (backend stuck starting?)")


def set_permission_mode_manual(page) -> None:
    pill = page.locator('button[aria-label="Permission mode"]').first
    pill.wait_for(timeout=10_000)
    pill.click()
    page.wait_for_timeout(300)
    option = page.locator('ul[role="listbox"] li[role="option"]', has_text="manual").first
    if option.count() == 0:
        # Dump available options for diagnostics before failing.
        opts_text = page.evaluate(
            """() => {
                const ul = document.querySelector('ul[role="listbox"]');
                return ul ? Array.from(ul.querySelectorAll('li')).map(li => li.innerText) : null;
            }"""
        )
        fail(f"'manual' option not found in Permission mode dropdown; options={opts_text!r}")
    option.click()
    page.wait_for_timeout(300)
    label = pill.inner_text()
    if "manual" not in label.lower():
        fail(f"Permission mode pill still shows {label!r} after selecting 'manual'")
    passed(f"permission mode forced to manual (pill now shows {label!r})")


def screenshot(page, path: Path) -> None:
    try:
        page.screenshot(path=str(path))
    except Exception as exc:  # best-effort
        print(f"(screenshot failed, non-fatal: {exc})")


def main() -> None:
    print("=" * 70)
    print("S862203-3 — AC-S862203-3-2 real-claude agent restart survival E2E")
    print("=" * 70)

    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        print("SKIP: playwright not installed")
        sys.exit(0)

    marker_id = uuid.uuid4().hex[:12]
    marker_path = f"/tmp/palmux_s862203_3_{marker_id}.txt"
    marker_text = f"E2E_MARKER_{marker_id}"
    Path(marker_path).unlink(missing_ok=True)

    result: dict[str, Any] = {
        "story": "S862203-3",
        "ac": "AC-S862203-3-2",
        "startedAt": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "markerId": marker_id,
    }
    screenshots_dir = REPO_ROOT / "docs/sprint-logs/S862203"
    screenshots_dir.mkdir(parents=True, exist_ok=True)

    ensure_built()
    stop_dev()  # clean slate — never assume a stray prior dev instance
    port = start_dev_fresh()
    result["devPort"] = port
    fx = get_fixture_module(port)
    fixture_cm = fx.palmux2_test_fixture("s862203-3-restart")
    fixture = fixture_cm.__enter__()
    branch_id = ""
    try:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        repo_id = fixture.repo_id
        result["repoId"] = repo_id
        result["branchId"] = branch_id
        force_agent_mode(port, repo_id, branch_id)

        url = f"http://localhost:{port}/{repo_id}/{branch_id}/claude:claude"

        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            ctx = browser.new_context(viewport={"width": 1280, "height": 900})
            ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
            page = ctx.new_page()
            page.goto(url, wait_until="networkidle", timeout=PLAYWRIGHT_TIMEOUT)
            page.wait_for_selector('[data-testid="claude-conversation"]', timeout=15_000)
            page.wait_for_timeout(2000)  # let the WS eager-spawn kick off

            # ---- Force manual permission mode (the footgun guard) --------
            set_permission_mode_manual(page)
            # The mode change triggers a background respawn (kill the old
            # CLI + spawn/Initialize a new one against the SAME repo) —
            # give it a moment before sending (send_with_retry below also
            # rides out any remaining race, this just makes the common case
            # not burn a retry).
            page.wait_for_timeout(2000)

            ta = page.locator('textarea[placeholder*="Message Claude" i]').first
            ta.wait_for(timeout=5_000)
            send_btn = page.locator('button[aria-label="Send"]').first

            # ---- Send the permission-gated message (retrying through the
            # transient post-mode-change respawn window) -------------------
            # prose_sentinel lives ONLY in the user-message prose (never in
            # the Bash command or the permission block's command/description),
            # so its presence after restart proves the PRE-permission
            # transcript (the user bubble) was reconstructed — the exact
            # content that was LOST in the replay-only behaviour (AC-3-2(a)
            # gap). It is a lowercase alnum token so terminal/markdown
            # rendering can't split it.
            prose_sentinel = f"userbubblesentinel{marker_id}"
            prompt = (
                f"This is a test conversation ({prose_sentinel}). Use the Bash "
                f"tool to run exactly this one command and nothing else — no "
                f"explanation, no other tool, no reading files first: "
                f"echo {marker_text} > {marker_path}"
            )
            send_with_retry(page, ta, send_btn, prompt)
            passed("sent permission-gated Bash prompt")

            # ---- Wait for the control_request to actually fire -----------
            deadline = time.monotonic() + 60.0
            perm_text = None
            while time.monotonic() < deadline:
                perm_text = permission_prompt_visible(page)
                if perm_text:
                    break
                time.sleep(1)
            if not perm_text:
                fail(
                    "[AC-S862203-3-2] no permission prompt appeared within 60s — "
                    "manual mode did not gate the Bash call (footgun: verify "
                    "~/.claude/settings.json / --permission-mode wiring), the test "
                    "would otherwise silently pass having tested nothing"
                )
            passed(f"[AC-S862203-3-2 pre-req] control_request fired, permission prompt visible: {perm_text!r}")
            result["controlRequestFired"] = True

            text_before = get_conv_text(page)
            result["convLenBefore"] = len(text_before)
            # Sanity: the user-bubble prose sentinel AND the assistant's Bash
            # tool_use must be on screen BEFORE the restart (so their ABSENCE
            # after restart is a genuine loss, not a never-rendered artefact).
            if prose_sentinel not in text_before:
                fail(f"[AC-S862203-3-2 pre-check] user-bubble sentinel {prose_sentinel!r} not "
                     f"present BEFORE restart; conv={text_before[:500]!r}")
            result["userBubblePresentBeforeRestart"] = True
            screenshot(page, screenshots_dir / "e2e-S862203-3-before-restart.png")

            ctx.close()
            browser.close()

        # ---- Restart ONLY the dev palmux2 (never host) --------------------
        restart_dev_only(port)
        result["restartedAt"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())

        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            ctx = browser.new_context(viewport={"width": 1280, "height": 900})
            ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
            page = ctx.new_page()
            page.goto(url, wait_until="networkidle", timeout=PLAYWRIGHT_TIMEOUT)
            page.wait_for_selector('[data-testid="claude-conversation"]', timeout=15_000)
            page.wait_for_timeout(2000)  # let the WS snapshot + any live catch-up settle

            text_after = get_conv_text(page)
            result["convLenAfter"] = len(text_after)
            screenshot(page, screenshots_dir / "e2e-S862203-3-after-restart.png")

            # ---- (a) IN-FLIGHT TURN transcript restored with no loss/dup ----
            # The pre-permission content — specifically the user-message
            # bubble (proven by the prose sentinel, which is NOT in the
            # replayed permission block) — MUST be present after restart.
            # This is the AC-3-2(a) assertion the verifier flagged: it FAILS
            # against replay-only reconstruction (the sentinel lives before
            # the pending control_request's frontier, so replay-only never
            # rebuilds it) and PASSES with the .jsonl backfill.
            sentinel_occurrences = text_after.count(prose_sentinel)
            result["userBubbleSentinelOccurrencesAfterRestart"] = sentinel_occurrences
            if sentinel_occurrences == 0:
                fail("[AC-S862203-3-2 part a] the PRE-permission user bubble is MISSING "
                     "from the post-restart transcript — the in-flight turn was NOT fully "
                     "restored (replay-only reconstruction loses everything before the "
                     "pending permission's frontier)")
            if sentinel_occurrences > 1:
                fail(f"[AC-S862203-3-2 part a] the user bubble sentinel appears "
                     f"{sentinel_occurrences}x post-restart — DUPLICATED at the "
                     f".jsonl-backfill / replay seam")
            # The assistant's Bash tool_use command must also be present (the
            # earlier tool_use line the verifier called out). It appears in
            # BOTH the tool_use block and the permission block, so we only
            # assert presence, and rely on the sentinel for the strict
            # no-loss/no-dup guarantee.
            if marker_path not in text_after:
                fail("[AC-S862203-3-2 part a] the assistant Bash tool_use command is "
                     "absent after restart")
            # Coarse no-truncation guard: the restored transcript must not be
            # dramatically shorter than before (replay-only shrank it ~374→205).
            result["convLenBefore"] = result.get("convLenBefore")
            if result["convLenAfter"] < int(result["convLenBefore"] * 0.85):
                fail(f"[AC-S862203-3-2 part a] transcript shrank across restart "
                     f"({result['convLenBefore']}→{result['convLenAfter']} chars) — content lost")
            passed(f"[AC-S862203-3-2 part a] FULL in-flight turn restored after restart: "
                   f"user bubble present (sentinel x{sentinel_occurrences}), Bash tool_use present, "
                   f"convLen {result['convLenBefore']}→{result['convLenAfter']} (no loss/dup)")
            result["transcriptLossless"] = True

            # ---- (b) permission surfaced + answerable ----------------------
            deadline = time.monotonic() + 20.0
            perm_after = None
            while time.monotonic() < deadline:
                perm_after = permission_prompt_visible(page)
                if perm_after:
                    break
                time.sleep(1)
            if not perm_after:
                fail("[AC-S862203-3-2 part b] permission prompt did NOT reappear "
                     "after restart — the restart-window permission request was "
                     "lost instead of being reconstructed as pending")
            passed(f"[AC-S862203-3-2 part b] pending permission RESURFACED after restart: {perm_after!r}")
            result["permissionSurfacedAfterRestart"] = True

            # Answer "allow" — try the y-shortcut first (established
            # pattern), fall back to clicking an Allow button.
            allowed = False
            for label in ("y", "Y"):
                page.keyboard.press(label)
                page.wait_for_timeout(1000)
                if Path(marker_path).is_file():
                    allowed = True
                    break
            if not allowed:
                allow_btn = page.locator('button:has-text("Allow")').first
                if allow_btn.count() > 0:
                    allow_btn.click()
                    allowed = True
            result["permissionAnswered"] = allowed
            if not allowed:
                fail("[AC-S862203-3-2 part b] could not find an Allow control "
                     "to answer the resurfaced permission")
            passed("[AC-S862203-3-2 part b] answered 'allow' from the UI")

            # ---- gated tool actually executes + turn completes -------------
            deadline = time.monotonic() + 60.0
            tool_executed = False
            while time.monotonic() < deadline:
                if Path(marker_path).is_file():
                    content = Path(marker_path).read_text().strip()
                    if content == marker_text:
                        tool_executed = True
                        break
                time.sleep(1)
            result["toolExecuted"] = tool_executed
            if not tool_executed:
                fail(f"[AC-S862203-3-2 part b] gated Bash tool did NOT execute within "
                     f"45s of answering allow — marker file {marker_path} not written "
                     f"with expected content")
            passed(f"[AC-S862203-3-2 part b] gated tool EXECUTED — marker file written "
                   f"with expected content ({marker_text})")

            # Turn should have continued to a visible result (assistant text
            # after the tool call, or at minimum the permission block is no
            # longer pending).
            page.wait_for_timeout(2000)
            perm_final = permission_prompt_visible(page)
            result["permissionStillPendingAfterAnswer"] = bool(perm_final)
            if perm_final:
                print(f"(note: a permission-shaped element is still visible post-answer: {perm_final!r} — "
                      f"non-fatal, may be residual DOM from a NEW unrelated prompt; tool execution already confirmed)")
            passed("[AC-S862203-3-2] turn continued past the answered permission (tool ran to completion)")

            screenshot(page, screenshots_dir / "e2e-S862203-3-after-answer.png")
            ctx.close()
            browser.close()

        # ---- Second restart: the answered permission must NOT re-surface --
        restart_dev_only(port)
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            ctx = browser.new_context(viewport={"width": 1280, "height": 900})
            ctx.add_init_script("window.sessionStorage.setItem('palmux:onboarding-skipped','1')")
            page = ctx.new_page()
            page.goto(url, wait_until="networkidle", timeout=PLAYWRIGHT_TIMEOUT)
            page.wait_for_selector('[data-testid="claude-conversation"]', timeout=15_000)
            page.wait_for_timeout(2000)
            perm_second = permission_prompt_visible(page)
            result["permissionResurfacedOnSecondRestart"] = bool(perm_second)
            if perm_second:
                fail(f"[AC-S862203-3-2] the ALREADY-ANSWERED permission re-surfaced on a "
                     f"SECOND restart ({perm_second!r}) — offset persistence did not "
                     f"advance past the answered request")
            passed("[AC-S862203-3-2] second restart does NOT re-surface the "
                   "already-answered permission (offset persistence advanced correctly)")
            ctx.close()
            browser.close()

        result["verdict"] = "PASS"
    finally:
        try:
            if branch_id:
                urllib.request.urlopen(
                    urllib.request.Request(
                        f"http://localhost:{port}/api/repos/{fixture.repo_id}/branches/{branch_id}",
                        method="DELETE",
                    ),
                    timeout=10,
                )
        except Exception as exc:
            print(f"(best-effort branch close failed, non-fatal: {exc})")
        try:
            fixture_cm.__exit__(None, None, None)
        except Exception as exc:
            print(f"(fixture cleanup failed, non-fatal: {exc})")
        stop_dev()
        Path(marker_path).unlink(missing_ok=True)

    result["finishedAt"] = time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime())
    RESULT_PATH.write_text(json.dumps(result, indent=2) + "\n")
    print(f"Result written to {RESULT_PATH}")
    print("=" * 70)
    print("ALL PASS — AC-S862203-3-2 confirmed on a real running palmux2 dev instance")
    print("=" * 70)


if __name__ == "__main__":
    main()
