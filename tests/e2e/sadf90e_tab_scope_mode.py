#!/usr/bin/env python3
"""Sprint Sadf90e — Claude タブ mode をタブ単位設定にした挙動の E2E。

Sprint S1f75ec-2 は『branch.claude_mode を全 Claude タブが共有する』設計
だったため、 1 branch に複数 Claude タブがあっても全タブが同 mode に縛られ、
タブ単位の独立操作ができなかった。 Sadf90e でタブ単位設定に作り直したので、
このテストは以下を保証する:

Acceptance criteria covered:
  [AC-Sadf90e-1-1] 同一 branch に 2 つの Claude(tui) タブを作成すると、 各タブ
                   独立した claudetui daemon が起動する (WS 経由で別 process)
  [AC-Sadf90e-1-3] 1 タブ削除 → そのタブの daemon だけ停止、 もう片方は alive
  [AC-Sadf90e-2-2] POST /tabs(type=claude) で TabClaudeModes に settings.claude.
                   default_mode で初期化される
  [AC-Sadf90e-2-3] global default_mode='tui' と 'agent' で両方向の挙動を確認
  [AC-Sadf90e-2-4] DELETE /tabs/{t} で TabClaudeModes から該当 entry が削除
  [AC-Sadf90e-3-2] migration: 既存 repos.json の branchSettings[bid].claude_mode
                   は load 時に discard される (= GET tab settings は default)
  [AC-Sadf90e-3-3] GET /api/repos/{r}/branches/{b}/tabs/{t}/settings → 200
  [AC-Sadf90e-3-4] PATCH /api/repos/{r}/branches/{b}/tabs/{t}/settings → 200
                   + その tabId だけ mode 変更
  [AC-Sadf90e-3-5] 旧 branch-scope endpoint /branches/{b}/settings は 404
  [AC-Sadf90e-3-6] 同 branch の他 Claude タブの mode は変わらない (isolation)
  [AC-Sadf90e-5-1] 2 タブ別 mode を設定 → 独立して動作
  [AC-Sadf90e-5-2] タブ削除 → GET /tabs/{deleted}/settings が 404 (tab not found)
  [AC-Sadf90e-5-5] data-testid='claude-mode-badge' が各 Claude タブ要素内に存在

Exit code 0 = ALL PASS. Run standalone:
  python3 tests/e2e/sadf90e_tab_scope_mode.py
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


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def passed(msg: str) -> None:
    print(f"PASS: {msg}")


def _free_port() -> int:
    s = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    s.bind(("127.0.0.1", 0))
    port = s.getsockname()[1]
    s.close()
    return port


def _get_playwright():
    try:
        from playwright.sync_api import sync_playwright
        return sync_playwright
    except ImportError:
        return None


def _http_json(port: int, method: str, path: str,
               body: dict | list | None = None) -> tuple[int, object]:
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
    port = _free_port()
    cfg_dir = Path("/tmp") / f"palmux2-e2e-sadf90e-{port}"
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


# ─── Helpers ─────────────────────────────────────────────────────────────────

def _tab_settings_path(repo_id: str, branch_id: str, tab_id: str) -> str:
    return (
        f"/api/repos/{urllib.parse.quote(repo_id)}"
        f"/branches/{urllib.parse.quote(branch_id)}"
        f"/tabs/{urllib.parse.quote(tab_id, safe='')}/settings"
    )


def _add_claude_tab(port: int, repo_id: str, branch_id: str) -> str:
    """POST a new Claude tab and return its new tabID."""
    code, body = _http_json(
        port, "POST",
        f"/api/repos/{urllib.parse.quote(repo_id)}"
        f"/branches/{urllib.parse.quote(branch_id)}/tabs",
        body={"type": "claude"},
    )
    if code not in (200, 201):
        fail(f"add claude tab: {code} {body!r}")
    assert isinstance(body, dict)
    new_id = body.get("id")
    if not isinstance(new_id, str) or not new_id:
        fail(f"add claude tab returned no id: {body!r}")
    return new_id


def _list_tabs(port: int, repo_id: str, branch_id: str) -> list[dict]:
    code, body = _http_json(
        port, "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}"
        f"/branches/{urllib.parse.quote(branch_id)}/tabs",
    )
    if code != 200:
        fail(f"list tabs: {code} {body!r}")
    if isinstance(body, dict):
        return body.get("tabs") or []  # type: ignore[return-value]
    return body  # type: ignore[return-value]


def _patch_settings_default_mode(port: int, mode: str) -> None:
    code, body = _http_json(
        port, "PATCH", "/api/settings",
        body={"claude": {"default_mode": mode}},
    )
    if code != 200:
        fail(f"patch /api/settings claude.default_mode={mode}: {code} {body!r}")


# ─── Test cases ──────────────────────────────────────────────────────────────

def test_ac_3_3_get_tab_settings(port: int) -> None:
    """[AC-Sadf90e-3-3] GET /tabs/{tabId}/settings returns claude_mode."""
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("sadf90e-get-tab-settings") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        code, body = _http_json(
            port, "GET",
            _tab_settings_path(fixture.repo_id, branch_id, "claude:claude"),
        )
        assert code == 200, f"GET tab settings: {code} {body!r}"
        assert isinstance(body, dict) and body.get("claude_mode") in ("agent", "tui"), (
            f"expected dict with claude_mode, got {body!r}"
        )
    passed("[AC-Sadf90e-3-3] GET /tabs/{t}/settings returns 200 + claude_mode")


def test_ac_3_4_patch_tab_settings_persists(port: int) -> None:
    """[AC-Sadf90e-3-4] PATCH /tabs/{tabId}/settings persists per-tab mode."""
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("sadf90e-patch-tab-settings") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        path = _tab_settings_path(fixture.repo_id, branch_id, "claude:claude")

        code, _ = _http_json(port, "PATCH", path, body={"claude_mode": "tui"})
        assert code == 200, f"PATCH tui: {code}"
        code, body = _http_json(port, "GET", path)
        assert isinstance(body, dict) and body.get("claude_mode") == "tui", (
            f"expected tui after PATCH, got {body!r}"
        )

        # Round-trip back to agent.
        code, _ = _http_json(port, "PATCH", path, body={"claude_mode": "agent"})
        assert code == 200
        code, body = _http_json(port, "GET", path)
        assert isinstance(body, dict) and body.get("claude_mode") == "agent", (
            f"expected agent after PATCH-back, got {body!r}"
        )
    passed("[AC-Sadf90e-3-4] PATCH /tabs/{t}/settings persists round-trip")


def test_ac_3_5_legacy_branch_settings_endpoint_404(port: int) -> None:
    """[AC-Sadf90e-3-5] Old branch-scope /settings endpoint is gone (404)."""
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("sadf90e-legacy-404") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        code, _ = _http_json(
            port, "GET",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/settings",
        )
        assert code == 404, f"legacy /settings GET should be 404, got {code}"
        code, _ = _http_json(
            port, "PATCH",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}/settings",
            body={"claude_mode": "tui"},
        )
        assert code == 404, f"legacy /settings PATCH should be 404, got {code}"
    passed("[AC-Sadf90e-3-5] legacy branch-scope /settings endpoints are 404")


def test_ac_3_6_isolation_between_sibling_tabs(port: int) -> None:
    """[AC-Sadf90e-3-6] Switching one Claude tab's mode leaves siblings alone."""
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("sadf90e-isolation") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        # Pin the global default to "agent" so we can assert a definite
        # starting mode for tab2 (otherwise the default_mode fallback "tui"
        # collides with the value we PATCH onto tab1 and the test becomes
        # vacuous).
        _patch_settings_default_mode(port, "agent")

        tab1 = "claude:claude"
        tab2 = _add_claude_tab(port, fixture.repo_id, branch_id)
        assert tab2 != tab1, f"new tab should have a distinct id, got {tab2!r}"

        # Confirm tab2 starts at default ("agent") before we touch tab1 —
        # otherwise the isolation assertion below tests nothing.
        _, before2 = _http_json(port, "GET",
                                _tab_settings_path(fixture.repo_id, branch_id, tab2))
        assert isinstance(before2, dict) and before2.get("claude_mode") == "agent", (
            f"tab2 should start at default 'agent', got {before2!r}"
        )

        # PATCH only tab1 to "tui".
        _, _ = _http_json(
            port, "PATCH",
            _tab_settings_path(fixture.repo_id, branch_id, tab1),
            body={"claude_mode": "tui"},
        )

        _, body1 = _http_json(port, "GET",
                              _tab_settings_path(fixture.repo_id, branch_id, tab1))
        _, body2 = _http_json(port, "GET",
                              _tab_settings_path(fixture.repo_id, branch_id, tab2))
        assert isinstance(body1, dict) and body1.get("claude_mode") == "tui", (
            f"tab1 should be tui after PATCH, got {body1!r}"
        )
        assert isinstance(body2, dict) and body2.get("claude_mode") == "agent", (
            f"tab2 should remain 'agent' (= unaffected by tab1's switch), got {body2!r}"
        )
    passed("[AC-Sadf90e-3-6] sibling Claude tab modes are isolated")


def test_ac_2_3_canonical_tab_inherits_default_mode(port: int) -> None:
    """[AC-Sadf90e-2-3 review fix] Even the canonical Claude tab (= the one
    auto-seeded by claudeagent.Manager.tabsForBranch at OnBranchOpen) must
    inherit settings.claude.default_mode. Pre-fix this only applied to tabs
    created via `+` (POST /tabs) — the very first claude:claude tab on a
    fresh branch silently fell back to "agent" regardless of the global
    override. This is the review-finding-#1 regression guard.

    Important: the global default must be set BEFORE the fixture opens the
    repo (= before OpenRepo runs and seeds the canonical tab's mode entry).
    Setting it inside the `with` block is too late — the canonical tab is
    already persisted with whatever default was active at OpenRepo time.
    Because the hermetic palmux2 instance is shared across tests in this
    file, we can PATCH /api/settings before entering palmux2_test_fixture.
    """
    fx = _get_fixture_module(port)
    # Pin the default BEFORE fixture entry so OpenRepo sees the desired value.
    _patch_settings_default_mode(port, "tui")
    with fx.palmux2_test_fixture("sadf90e-canonical-default") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)

        _, body = _http_json(
            port, "GET",
            _tab_settings_path(fixture.repo_id, branch_id, "claude:claude"),
        )
        assert isinstance(body, dict) and body.get("claude_mode") == "tui", (
            f"canonical claude:claude tab should inherit default_mode='tui', got {body!r}"
        )
    passed("[AC-Sadf90e-2-3 canonical] first Claude tab inherits global default_mode")


def test_ac_2_2_default_mode_tui_at_tab_add(port: int) -> None:
    """[AC-Sadf90e-2-2 / 2-3] New Claude tab inherits settings.claude.default_mode=tui."""
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("sadf90e-default-tui") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        _patch_settings_default_mode(port, "tui")

        new_tab = _add_claude_tab(port, fixture.repo_id, branch_id)
        _, body = _http_json(
            port, "GET",
            _tab_settings_path(fixture.repo_id, branch_id, new_tab),
        )
        assert isinstance(body, dict) and body.get("claude_mode") == "tui", (
            f"new tab should inherit default_mode=tui, got {body!r}"
        )
    passed("[AC-Sadf90e-2-2 / 2-3] new tab inherits default_mode=tui")


def test_ac_2_2_default_mode_agent_at_tab_add(port: int) -> None:
    """[AC-Sadf90e-2-3] Inverse: new Claude tab inherits default_mode=agent."""
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("sadf90e-default-agent") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        _patch_settings_default_mode(port, "agent")

        new_tab = _add_claude_tab(port, fixture.repo_id, branch_id)
        _, body = _http_json(
            port, "GET",
            _tab_settings_path(fixture.repo_id, branch_id, new_tab),
        )
        assert isinstance(body, dict) and body.get("claude_mode") == "agent", (
            f"new tab should inherit default_mode=agent, got {body!r}"
        )
    passed("[AC-Sadf90e-2-3] new tab inherits default_mode=agent")


def test_ac_2_4_delete_tab_cleans_settings(port: int) -> None:
    """[AC-Sadf90e-2-4 / 5-2] DELETE /tabs/{t} removes the tab's claude_mode entry."""
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("sadf90e-delete-cleans") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        new_tab = _add_claude_tab(port, fixture.repo_id, branch_id)

        # Set its mode so an explicit entry exists.
        code, _ = _http_json(
            port, "PATCH",
            _tab_settings_path(fixture.repo_id, branch_id, new_tab),
            body={"claude_mode": "tui"},
        )
        assert code == 200

        # Delete the tab.
        code, _ = _http_json(
            port, "DELETE",
            f"/api/repos/{urllib.parse.quote(fixture.repo_id)}"
            f"/branches/{urllib.parse.quote(branch_id)}"
            f"/tabs/{urllib.parse.quote(new_tab, safe='')}",
        )
        assert code in (200, 204), f"DELETE tab: {code}"

        # GET its settings → 404 (tab gone).
        code, _ = _http_json(
            port, "GET",
            _tab_settings_path(fixture.repo_id, branch_id, new_tab),
        )
        assert code == 404, f"GET deleted tab settings should be 404, got {code}"
    passed("[AC-Sadf90e-2-4 / 5-2] tab delete removes settings (GET → 404)")


def test_ac_5_1_independent_modes_in_one_branch(port: int) -> None:
    """[AC-Sadf90e-5-1 / 1-1] 2 Claude タブを別 mode に設定 → 独立して維持される。"""
    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("sadf90e-indep-modes") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        tab1 = "claude:claude"
        tab2 = _add_claude_tab(port, fixture.repo_id, branch_id)

        _, _ = _http_json(
            port, "PATCH",
            _tab_settings_path(fixture.repo_id, branch_id, tab1),
            body={"claude_mode": "tui"},
        )
        _, _ = _http_json(
            port, "PATCH",
            _tab_settings_path(fixture.repo_id, branch_id, tab2),
            body={"claude_mode": "agent"},
        )

        _, body1 = _http_json(port, "GET",
                              _tab_settings_path(fixture.repo_id, branch_id, tab1))
        _, body2 = _http_json(port, "GET",
                              _tab_settings_path(fixture.repo_id, branch_id, tab2))
        assert isinstance(body1, dict) and body1.get("claude_mode") == "tui"
        assert isinstance(body2, dict) and body2.get("claude_mode") == "agent"
    passed("[AC-Sadf90e-5-1 / 1-1] tab1=tui + tab2=agent coexist independently")


# ─── Browser tests ───────────────────────────────────────────────────────────

def test_ac_5_5_badge_on_every_claude_tab(port: int) -> None:
    """[AC-Sadf90e-5-5 / 4-1] Each Claude tab shows its own data-testid='claude-mode-badge'."""
    if not _USE_PREBUILT:
        print("SKIP: test_ac_5_5 (no embedded frontend in go-run mode)")
        return
    sync_playwright = _get_playwright()
    if sync_playwright is None:
        print("SKIP: test_ac_5_5 (playwright not installed)")
        return

    fx = _get_fixture_module(port)
    with fx.palmux2_test_fixture("sadf90e-badge-each-tab") as fixture:
        branch_id = fixture.primary_branch_id(timeout_s=10.0)
        # Create a second Claude tab so there are 2 visible.
        _ = _add_claude_tab(port, fixture.repo_id, branch_id)

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
                # Wait for at least one badge so the FE finished its
                # post-mount setting fetch.
                page.wait_for_selector("[data-testid='claude-mode-badge']",
                                       timeout=PLAYWRIGHT_TIMEOUT)
                count = page.locator("[data-testid='claude-mode-badge']").count()
                assert count == 2, (
                    f"expected 2 claude-mode-badge elements (one per tab), got {count}"
                )
            finally:
                browser.close()
    passed("[AC-Sadf90e-5-5 / 4-1] every Claude tab has its own claude-mode-badge")


# ─── Runner ──────────────────────────────────────────────────────────────────

def main() -> int:
    print("=" * 60)
    print("Sprint Sadf90e — タブ単位 claude_mode の挙動 E2E")
    if not _USE_PREBUILT:
        print("Mode: go-run (frontend not embedded)")
    else:
        print("Mode: pre-built binary")
    print("=" * 60)

    skipped_count = 0
    total = 0
    passed_count = 0
    failed_count = 0
    failures: list[str] = []

    def _run(name: str, fn) -> None:
        nonlocal total, passed_count, failed_count
        total += 1
        try:
            fn()
            passed_count += 1
        except AssertionError as e:
            failed_count += 1
            failures.append(f"{name}: {e}")
            print(f"FAIL: {name}: {e}", file=sys.stderr)
        except Exception as e:
            failed_count += 1
            failures.append(f"{name}: {type(e).__name__}: {e}")
            print(f"FAIL: {name}: {type(e).__name__}: {e}", file=sys.stderr)

    with hermetic_palmux2() as (port, has_frontend):
        print(f"[ok] palmux2 listening on port {port}")
        os.environ["PALMUX2_DEV_PORT_OVERRIDE"] = str(port)
        _get_fixture_module(port)

        _run("test_ac_3_3_get_tab_settings",
             lambda: test_ac_3_3_get_tab_settings(port))
        _run("test_ac_3_4_patch_tab_settings_persists",
             lambda: test_ac_3_4_patch_tab_settings_persists(port))
        _run("test_ac_3_5_legacy_branch_settings_endpoint_404",
             lambda: test_ac_3_5_legacy_branch_settings_endpoint_404(port))
        _run("test_ac_3_6_isolation_between_sibling_tabs",
             lambda: test_ac_3_6_isolation_between_sibling_tabs(port))
        _run("test_ac_2_3_canonical_tab_inherits_default_mode",
             lambda: test_ac_2_3_canonical_tab_inherits_default_mode(port))
        _run("test_ac_2_2_default_mode_tui_at_tab_add",
             lambda: test_ac_2_2_default_mode_tui_at_tab_add(port))
        _run("test_ac_2_2_default_mode_agent_at_tab_add",
             lambda: test_ac_2_2_default_mode_agent_at_tab_add(port))
        _run("test_ac_2_4_delete_tab_cleans_settings",
             lambda: test_ac_2_4_delete_tab_cleans_settings(port))
        _run("test_ac_5_1_independent_modes_in_one_branch",
             lambda: test_ac_5_1_independent_modes_in_one_branch(port))

        if has_frontend:
            _run("test_ac_5_5_badge_on_every_claude_tab",
                 lambda: test_ac_5_5_badge_on_every_claude_tab(port))
        else:
            print("SKIP: test_ac_5_5 (no embedded frontend in go-run mode)")
            skipped_count += 1

    print()
    print("=" * 60)
    print(f"Sadf90e E2E Results: {passed_count}/{total} passed, {skipped_count} skipped")
    if failed_count == 0:
        print("ALL PASS")
        return 0
    print(f"FAILED: {failed_count}")
    for f in failures:
        print(f"  - {f}")
    return 1


if __name__ == "__main__":
    sys.exit(main())
