#!/usr/bin/env python3
"""Sprint S1f75ec Story 2 — Claude tab mode switch E2E.

Covers the full-stack acceptance criteria for unifying the two Claude tabs
into a single "Claude" tab with a per-branch mode setting (claude_mode:
"agent" | "tui").

Acceptance criteria covered:
  [AC-S1f75ec-2-1] GET /settings returns {"claude_mode": "agent"} for an
                   existing branch (migration default).
  [AC-S1f75ec-2-2] PATCH /settings persists claude_mode; GET confirms.
  [AC-S1f75ec-2-4] ⌘K palette command "switch-claude-mode" toggles mode
                   (browser test, pre-built only).
  [AC-S1f75ec-2-5] TabBar shows exactly 1 "Claude" tab
                   (data-testid='claude-tab' count == 1, browser test).
  [AC-S1f75ec-2-6] GET /{repo}/{branch}/claude-tui redirects to /claude
                   (Playwright follows the redirect, browser test).
  [AC-S1f75ec-2-7] data-testid='claude-mode-badge' shows current mode
                   (browser test, pre-built only).

Architecture note:
  Uses the same hermetic palmux2 pattern as s7ce250_claude_tui.py —
  launches bin/palmux with --claude-bin /bin/cat. Falls back to
  `go run ./cmd/palmux` when the binary is absent (browser tests skipped).

Exit code 0 = ALL PASS. Run standalone:
  python3 tests/e2e/s1f75ec_mode_switch.py
"""
from __future__ import annotations

import json
import os
import signal
import socket
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from contextlib import contextmanager
from pathlib import Path
from typing import Iterator

sys.path.insert(0, os.path.dirname(__file__))

REPO_ROOT = Path(__file__).resolve().parents[2]
PLAYWRIGHT_TIMEOUT = 20_000  # ms

_PREBUILT_BIN = REPO_ROOT / "bin" / "palmux"
_USE_PREBUILT = _PREBUILT_BIN.is_file()


# ─── Helpers ─────────────────────────────────────────────────────────────────

def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def passed(msg: str) -> None:
    print(f"PASS: {msg}")


def _free_port() -> int:
    with socket.socket(socket.AF_INET, socket.SOCK_STREAM) as s:
        s.bind(("127.0.0.1", 0))
        return s.getsockname()[1]


def _get_playwright():
    try:
        from playwright.sync_api import sync_playwright  # noqa: F401
        return sync_playwright
    except ImportError:
        print("SKIP: playwright not installed")
        sys.exit(0)


# ─── Low-level HTTP helpers ──────────────────────────────────────────────────

def _http_json(port: int, method: str, path: str,
               body: dict | None = None) -> tuple[int, object]:
    url = f"http://localhost:{port}{path}"
    raw = json.dumps(body).encode() if body is not None else None
    headers: dict[str, str] = {"Accept": "application/json"}
    if body is not None:
        headers["Content-Type"] = "application/json"
    req = urllib.request.Request(url, method=method, data=raw, headers=headers)
    try:
        with urllib.request.urlopen(req, timeout=20) as resp:
            code: int = resp.status
            data: bytes = resp.read()
    except urllib.error.HTTPError as exc:
        code = exc.code
        data = exc.read()
    try:
        return code, json.loads(data.decode() or "{}")
    except json.JSONDecodeError:
        return code, data.decode(errors="replace")


# ─── Hermetic palmux2 instance ───────────────────────────────────────────────

@contextmanager
def hermetic_palmux2() -> Iterator[tuple[int, bool]]:
    """Start a hermetic palmux2 process.  Yields (port, has_frontend)."""
    port = _free_port()
    cfg_dir = Path("/tmp") / f"palmux2-e2e-s1f75ec-{port}"
    cfg_dir.mkdir(parents=True, exist_ok=True)

    if _USE_PREBUILT:
        cmd = [
            str(_PREBUILT_BIN),
            "--addr", f"127.0.0.1:{port}",
            "--config-dir", str(cfg_dir),
            "--claude-bin", "/bin/cat",
            "--tmux-prefix", f"_pmx_e2e{port}_",
        ]
        has_frontend = True
    else:
        cmd = [
            "go", "run", "./cmd/palmux",
            "--addr", f"127.0.0.1:{port}",
            "--config-dir", str(cfg_dir),
            "--claude-bin", "/bin/cat",
            "--tmux-prefix", f"_pmx_e2e{port}_",
        ]
        has_frontend = False

    proc = subprocess.Popen(
        cmd,
        cwd=REPO_ROOT,
        stdout=subprocess.PIPE,
        stderr=subprocess.STDOUT,
        text=True,
    )

    try:
        deadline = time.monotonic() + 60.0
        listening = False
        while time.monotonic() < deadline:
            if proc.stdout is None:
                break
            line = proc.stdout.readline()
            if not line and proc.poll() is not None:
                rest = proc.stdout.read() if proc.stdout else ""
                fail(f"palmux2 exited before listening: rc={proc.returncode}\n{rest}")
            if "palmux2 listening" in line or f":{port}" in line:
                listening = True
                break
        if not listening:
            proc.kill()
            fail("palmux2 did not announce its listening port within 60 s")
        yield port, has_frontend
    finally:
        if proc.poll() is None:
            proc.send_signal(signal.SIGTERM)
            try:
                proc.wait(timeout=10)
            except subprocess.TimeoutExpired:
                proc.kill()
                proc.wait(timeout=5)
        import shutil
        shutil.rmtree(cfg_dir, ignore_errors=True)


def _get_fixture_module(port: int):
    os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
    if "_fixture" in sys.modules:
        del sys.modules["_fixture"]
    import _fixture as fx_mod
    return fx_mod


# ─── Test cases ──────────────────────────────────────────────────────────────

def test_ac_2_1_migration_default_agent(port: int) -> None:
    """[AC-S1f75ec-2-1] Existing branch → GET /settings returns claude_mode='agent'."""
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-migration") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        code, body = _http_json(
            port, "GET",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/settings",
        )
        assert code == 200, f"[AC-S1f75ec-2-1] expected 200, got {code}: {body}"
        assert isinstance(body, dict), f"[AC-S1f75ec-2-1] expected dict, got {body!r}"
        assert body.get("claude_mode") == "agent", (
            f"[AC-S1f75ec-2-1] expected claude_mode='agent' (migration default), got {body}"
        )
    passed("[AC-S1f75ec-2-1] existing branch defaults to claude_mode='agent'")


def test_ac_2_2_patch_and_get_persistence(port: int) -> None:
    """[AC-S1f75ec-2-2] PATCH claude_mode='tui' → GET returns 'tui' (persisted)."""
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-persist") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        # PATCH to tui
        code, body = _http_json(
            port, "PATCH",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/settings",
            body={"claude_mode": "tui"},
        )
        assert code == 200, f"[AC-S1f75ec-2-2] PATCH failed: {code} {body}"
        assert isinstance(body, dict) and body.get("claude_mode") == "tui", (
            f"[AC-S1f75ec-2-2] PATCH response: {body}"
        )

        # GET to confirm persistence
        code2, body2 = _http_json(
            port, "GET",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/settings",
        )
        assert code2 == 200, f"[AC-S1f75ec-2-2] GET after PATCH failed: {code2} {body2}"
        assert isinstance(body2, dict) and body2.get("claude_mode") == "tui", (
            f"[AC-S1f75ec-2-2] GET after PATCH: expected tui, got {body2}"
        )

        # Round-trip back to agent
        _http_json(
            port, "PATCH",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/settings",
            body={"claude_mode": "agent"},
        )
        code3, body3 = _http_json(
            port, "GET",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/settings",
        )
        assert isinstance(body3, dict) and body3.get("claude_mode") == "agent", (
            f"[AC-S1f75ec-2-2] round-trip to agent failed: {body3}"
        )
    passed("[AC-S1f75ec-2-2] PATCH claude_mode → GET confirms persistence")


def test_ac_2_3_global_default_override_tui(port: int) -> None:
    """[AC-S1f75ec-2-3] settings.claude.default_mode='tui' → new branch defaults to tui.

    Verifies the path that was unwired pre-verify: opening a new branch
    after PATCH /api/settings sets claude.default_mode=tui must persist
    a BranchSettings entry with claude_mode='tui' (not the migration
    fallback 'agent').
    """
    import subprocess
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-default-tui") as fixture:
        fixture.primary_branch_id(timeout_s=10.0)  # ensure server up + primary open

        # Set global default = tui.
        code, _ = _http_json(
            port, "PATCH", "/api/settings",
            body={"claude": {"default_mode": "tui"}},
        )
        assert code == 200, f"[AC-S1f75ec-2-3] PATCH /api/settings: {code}"

        # Create a brand-new local branch in the fixture repo, then open it
        # via the public API.  palmux2 will gwq add the worktree, which runs
        # InitBranchSettings(repoID, branchID, settings.ClaudeDefaultMode()).
        subprocess.run(
            ["git", "-C", str(fixture.path), "branch", "feature-default-tui", "main"],
            check=True,
        )
        code, body = _http_json(
            port, "POST",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}/branches/open",
            body={"branchName": "feature-default-tui"},
        )
        assert code in (200, 201), f"[AC-S1f75ec-2-3] open new branch: {code} {body}"
        assert isinstance(body, dict)
        new_branch_id = body.get("id")
        assert new_branch_id, f"[AC-S1f75ec-2-3] open response missing id: {body}"

        code, settings = _http_json(
            port, "GET",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(new_branch_id)}/settings",
        )
        assert code == 200, f"[AC-S1f75ec-2-3] GET new-branch settings: {code} {settings}"
        assert isinstance(settings, dict)
        assert settings.get("claude_mode") == "tui", (
            f"[AC-S1f75ec-2-3] new branch should inherit global default 'tui', "
            f"got {settings}"
        )
    passed("[AC-S1f75ec-2-3] global default 'tui' applies to newly-opened branch")


def test_ac_2_3_global_default_override_agent(port: int) -> None:
    """[AC-S1f75ec-2-3] settings.claude.default_mode='agent' → new branch defaults to agent.

    Inverse direction: global override switched to 'agent' must propagate
    to newly-opened branches.
    """
    import subprocess
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-default-agent") as fixture:
        fixture.primary_branch_id(timeout_s=10.0)

        code, _ = _http_json(
            port, "PATCH", "/api/settings",
            body={"claude": {"default_mode": "agent"}},
        )
        assert code == 200, f"[AC-S1f75ec-2-3] PATCH /api/settings: {code}"

        subprocess.run(
            ["git", "-C", str(fixture.path), "branch", "feature-default-agent", "main"],
            check=True,
        )
        code, body = _http_json(
            port, "POST",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}/branches/open",
            body={"branchName": "feature-default-agent"},
        )
        assert code in (200, 201), f"[AC-S1f75ec-2-3] open new branch: {code} {body}"
        new_branch_id = body.get("id")
        assert new_branch_id

        code, settings = _http_json(
            port, "GET",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(new_branch_id)}/settings",
        )
        assert isinstance(settings, dict)
        assert settings.get("claude_mode") == "agent", (
            f"[AC-S1f75ec-2-3] new branch should inherit global default 'agent', "
            f"got {settings}"
        )
    passed("[AC-S1f75ec-2-3] global default 'agent' applies to newly-opened branch")


def test_ac_2_2_invalid_mode_rejected(port: int) -> None:
    """[AC-S1f75ec-2-2] PATCH with invalid claude_mode → 4xx error."""
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-invalid") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        code, body = _http_json(
            port, "PATCH",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/settings",
            body={"claude_mode": "invalid_value"},
        )
        assert code >= 400, (
            f"[AC-S1f75ec-2-2] expected 4xx for invalid mode, got {code}: {body}"
        )
    passed("[AC-S1f75ec-2-2] invalid claude_mode rejected with 4xx")


def test_ac_2_5_single_claude_tab_in_tabbar(port: int) -> None:
    """[AC-S1f75ec-2-5] Browser: TabBar shows exactly 1 tab with data-testid='claude-tab'."""
    if not _USE_PREBUILT:
        print("SKIP: test_ac_2_5 (no embedded frontend in go-run mode)")
        return
    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-single-tab") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        url = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude"
        )
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                # Wait for the tab bar to populate.
                page.wait_for_selector("[data-testid='claude-tab']", timeout=PLAYWRIGHT_TIMEOUT)
                # Count: must be exactly 1.
                count = page.locator("[data-testid='claude-tab']").count()
                assert count == 1, (
                    f"[AC-S1f75ec-2-5] expected exactly 1 [data-testid='claude-tab'], got {count}"
                )
                # Verify the old "claude-tui" tab label is gone.
                tui_count = page.locator("text='Claude (TUI)'").count()
                assert tui_count == 0, (
                    f"[AC-S1f75ec-2-5] 'Claude (TUI)' tab label still visible ({tui_count} occurrences)"
                )
            finally:
                browser.close()
    passed("[AC-S1f75ec-2-5] TabBar shows exactly 1 Claude tab; 'Claude (TUI)' label gone")


def test_ac_2_6_claude_tui_url_redirects_to_claude(port: int) -> None:
    """[AC-S1f75ec-2-6] GET /{repo}/{branch}/claude-tui → redirects to /claude."""
    if not _USE_PREBUILT:
        print("SKIP: test_ac_2_6 (no embedded frontend in go-run mode)")
        return
    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-redirect") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        tui_url = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude-tui"
        )
        canonical_pattern = (
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude"
        )
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                # Playwright follows client-side redirects (React Router Navigate).
                resp = page.goto(tui_url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                final_url = page.url
                assert canonical_pattern in final_url, (
                    f"[AC-S1f75ec-2-6] expected URL to contain '{canonical_pattern}', "
                    f"got '{final_url}'"
                )
                assert "/claude-tui" not in final_url.split("?")[0], (
                    f"[AC-S1f75ec-2-6] URL still contains '/claude-tui': {final_url}"
                )
            finally:
                browser.close()
    passed(f"[AC-S1f75ec-2-6] /claude-tui redirects to /claude in browser")


def test_ac_2_7_mode_badge_visible(port: int) -> None:
    """[AC-S1f75ec-2-7] data-testid='claude-mode-badge' shows current mode."""
    if not _USE_PREBUILT:
        print("SKIP: test_ac_2_7 (no embedded frontend in go-run mode)")
        return
    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-badge") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        # Default mode is "agent" for existing branches.
        url = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude"
        )
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                page.wait_for_selector(
                    "[data-testid='claude-mode-badge']", timeout=PLAYWRIGHT_TIMEOUT
                )
                badge = page.locator("[data-testid='claude-mode-badge']").first
                assert badge.is_visible(), "[AC-S1f75ec-2-7] claude-mode-badge not visible"
                text = badge.inner_text().strip().upper()
                assert text in ("AGENT", "TUI"), (
                    f"[AC-S1f75ec-2-7] badge text {text!r} not 'Agent' or 'TUI'"
                )
            finally:
                browser.close()
    passed("[AC-S1f75ec-2-7] claude-mode-badge is visible and shows Agent or TUI")


def test_ac_2_4_palette_switch_command(port: int) -> None:
    """[AC-S1f75ec-2-4] ⌘K palette lists switch-claude-mode and can toggle mode."""
    if not _USE_PREBUILT:
        print("SKIP: test_ac_2_4 (no embedded frontend in go-run mode)")
        return
    sync_playwright = _get_playwright()
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("s1f75ec-palette") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        url = (
            f"http://localhost:{port}"
            f"/{urllib.parse.quote(fixture.repo_id, safe='')}"
            f"/{urllib.parse.quote(branch_id, safe='')}"
            f"/claude"
        )
        with sync_playwright() as p:
            browser = p.chromium.launch(headless=True)
            try:
                page = browser.new_page()
                page.goto(url, timeout=PLAYWRIGHT_TIMEOUT, wait_until="load")
                page.wait_for_function(
                    "document.getElementById('root').innerHTML.length > 100",
                    timeout=PLAYWRIGHT_TIMEOUT,
                )
                page.wait_for_selector("[data-testid='claude-tab']", timeout=PLAYWRIGHT_TIMEOUT)

                # Open palette with Ctrl+K, type ">switch-claude-mode"
                page.keyboard.press("Control+k")
                page.wait_for_selector("[data-testid='palette-input']", timeout=PLAYWRIGHT_TIMEOUT)
                page.fill("[data-testid='palette-input']", ">switch-claude-mode")
                # Give the palette time to render results.
                time.sleep(0.5)

                # The switch-claude-mode command should appear in the results.
                # Check by data-testid which is palette-item-switch-claude-mode.
                item_selector = "[data-testid='palette-item-switch-claude-mode']"
                page.wait_for_selector(item_selector, timeout=PLAYWRIGHT_TIMEOUT)
                assert page.locator(item_selector).count() >= 1, (
                    "[AC-S1f75ec-2-4] switch-claude-mode palette item not found"
                )

                # Get badge text before switch.
                page.keyboard.press("Escape")
                page.wait_for_selector("[data-testid='claude-mode-badge']", timeout=5_000)
                badge_before = page.locator("[data-testid='claude-mode-badge']").first.inner_text().strip().upper()

                # Open palette again, select the command by clicking directly.
                page.keyboard.press("Control+k")
                page.wait_for_selector("[data-testid='palette-input']", timeout=PLAYWRIGHT_TIMEOUT)
                page.fill("[data-testid='palette-input']", ">switch-claude-mode")
                time.sleep(0.5)
                page.wait_for_selector("[data-testid='palette-item-switch-claude-mode']", timeout=PLAYWRIGHT_TIMEOUT)
                page.click("[data-testid='palette-item-switch-claude-mode']")

                # Wait for the async patchSettings to complete and the badge to re-render.
                time.sleep(2.0)
                page.wait_for_selector("[data-testid='claude-mode-badge']", timeout=5_000)
                badge_after = page.locator("[data-testid='claude-mode-badge']").first.inner_text().strip().upper()

                expected_after = "TUI" if badge_before == "AGENT" else "AGENT"
                assert badge_after == expected_after, (
                    f"[AC-S1f75ec-2-4] expected badge to flip from {badge_before!r} to {expected_after!r}, "
                    f"got {badge_after!r}"
                )
            finally:
                browser.close()
    passed("[AC-S1f75ec-2-4] palette switch-claude-mode command toggles mode badge")


# ─── Runner ──────────────────────────────────────────────────────────────────

def main() -> None:
    print("=" * 60)
    print("S1f75ec Story 2 — Claude tab mode switch E2E")
    mode = "pre-built binary" if _USE_PREBUILT else "go run (fallback, no frontend)"
    print(f"Mode: {mode}")
    print("Starting hermetic palmux2 ...")
    print("=" * 60)

    passed_count = 0
    failed_count = 0
    skipped_count = 0

    def _run(name: str, fn) -> None:
        nonlocal passed_count, failed_count, skipped_count
        try:
            fn()
            passed_count += 1
        except SystemExit as e:
            if e.code == 0:
                skipped_count += 1  # SKIP prints to stdout and sys.exit(0)
            else:
                raise
        except Exception as exc:
            print(f"FAIL: {name}: {exc}", file=sys.stderr)
            import traceback
            traceback.print_exc(file=sys.stderr)
            failed_count += 1

    with hermetic_palmux2() as (port, has_frontend):
        print(f"[ok] palmux2 listening on port {port}")
        os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
        _get_fixture_module(port)

        _run("test_ac_2_1_migration_default_agent",
             lambda: test_ac_2_1_migration_default_agent(port))

        _run("test_ac_2_2_patch_and_get_persistence",
             lambda: test_ac_2_2_patch_and_get_persistence(port))

        _run("test_ac_2_2_invalid_mode_rejected",
             lambda: test_ac_2_2_invalid_mode_rejected(port))

        _run("test_ac_2_3_global_default_override_tui",
             lambda: test_ac_2_3_global_default_override_tui(port))

        _run("test_ac_2_3_global_default_override_agent",
             lambda: test_ac_2_3_global_default_override_agent(port))

        if has_frontend:
            _run("test_ac_2_5_single_claude_tab_in_tabbar",
                 lambda: test_ac_2_5_single_claude_tab_in_tabbar(port))

            _run("test_ac_2_6_claude_tui_url_redirects_to_claude",
                 lambda: test_ac_2_6_claude_tui_url_redirects_to_claude(port))

            _run("test_ac_2_7_mode_badge_visible",
                 lambda: test_ac_2_7_mode_badge_visible(port))

            _run("test_ac_2_4_palette_switch_command",
                 lambda: test_ac_2_4_palette_switch_command(port))
        else:
            for name in [
                "test_ac_2_5_single_claude_tab_in_tabbar",
                "test_ac_2_6_claude_tui_url_redirects_to_claude",
                "test_ac_2_7_mode_badge_visible",
                "test_ac_2_4_palette_switch_command",
            ]:
                print(f"SKIP: {name} (no embedded frontend in go-run mode)")
                skipped_count += 1

    total = passed_count + failed_count
    print()
    print("=" * 60)
    print(f"S1f75ec-2 E2E Results: {passed_count}/{total} passed, {skipped_count} skipped")
    if failed_count > 0:
        sys.exit(1)
    print("ALL PASS")


if __name__ == "__main__":
    main()
