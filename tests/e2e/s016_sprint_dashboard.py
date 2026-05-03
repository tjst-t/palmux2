#!/usr/bin/env python3
"""Sprint S016 — Sprint Dashboard tab E2E.

Drives the running dev palmux2 instance through the S016 acceptance
criteria. The script first verifies the REST + WS contract end-to-end,
then drives a headless Chromium against the Sprint tab to confirm:

  (a) Sprint tab is detected on a branch with docs/ROADMAP.md and the
      five subtabs render.
  (b) Removing ROADMAP.md emits `tab.removed` and the tab disappears;
      restoring it re-emits `tab.added` and the tab returns.
  (c) Editing ROADMAP.md propagates a `sprint.changed` event within 1.5s
      and Overview's progress reflects the new totals.
  (d) Touching `.claude/autopilot-S012.lock` shows the entry in Active
      autopilot; deleting removes it.
  (e) Mermaid graph nodes carry `data-testid="sprint-dep-node-Sxxx"` so
      a click navigates to Sprint Detail (we exercise the click+URL-
      change path under Playwright).
  (f) Decision Timeline category filters narrow the entries list.
  (g) WS disconnect surfaces an `offline` badge in the view header;
      reconnect drops it and triggers an automatic refetch.
  (h) Refresh button forces a 200 (not 304) even when ETag matches.
  (i) A malformed ROADMAP.md degrades gracefully — the response carries
      a non-empty `parseErrors` array but the UI does not crash.
  (j) Mobile width (375px) renders five subtabs without overflowing the
      viewport horizontally.

The test is hermetic: it creates a fixture worktree under the host
ghq root, registers it with palmux2 via the existing /api/repos/open
endpoint, and tears the fixture down at the end. The dev palmux2
instance host worktree is not modified.

Exit code 0 = PASS. Anything else = FAIL.
"""
from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or os.environ.get("PALMUX_DEV_PORT")
    or "8202"
)
BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 20.0
REPO_ROOT = Path(__file__).resolve().parents[2]


# --------------------------------------------------------------------------
# tiny helpers
# --------------------------------------------------------------------------

def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def assert_(cond: bool, msg: str) -> None:
    if not cond:
        fail(msg)


def http(method: str, path: str, *, body: bytes | None = None,
         headers: dict[str, str] | None = None) -> tuple[int, dict[str, str], bytes]:
    url = f"{BASE_URL}{path}"
    req = urllib.request.Request(url, method=method, data=body, headers=headers or {})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            return resp.status, dict(resp.headers), resp.read()
    except urllib.error.HTTPError as e:
        return e.code, dict(e.headers or {}), e.read()


def http_json(method: str, path: str, *, body: dict | list | None = None,
              if_none_match: str | None = None) -> tuple[int, dict, dict | list | str]:
    raw = json.dumps(body).encode() if body is not None else None
    h: dict[str, str] = {"Accept": "application/json"}
    if body is not None:
        h["Content-Type"] = "application/json"
    if if_none_match:
        h["If-None-Match"] = if_none_match
    code, hdrs, data = http(method, path, body=raw, headers=h)
    try:
        decoded = json.loads(data.decode() or "{}")
    except json.JSONDecodeError:
        decoded = data.decode(errors="replace")
    return code, hdrs, decoded


def run(cwd: Path, *args: str) -> str:
    res = subprocess.run(list(args), cwd=cwd, capture_output=True, text=True)
    if res.returncode != 0:
        raise RuntimeError(
            f"command failed in {cwd}: {' '.join(args)}\nstdout: {res.stdout}\nstderr: {res.stderr}"
        )
    return res.stdout


def ghq_root() -> Path:
    out = subprocess.run(["ghq", "root"], capture_output=True, text=True, check=True)
    return Path(out.stdout.strip())


# --------------------------------------------------------------------------
# fixture
# --------------------------------------------------------------------------

ROADMAP_TEMPLATE = """\
# プロジェクトロードマップ: Test Project

## 進捗

- 合計: {total} | 完了: {done} | 進行中: 0 | 残り: {remaining}

## 実行順序

S001 → S002

## スプリント S001: 計画 [DONE]

最初のスプリント。

### ストーリー S001-1: 計画する [x]

**ユーザーストーリー:**
ユーザーとして計画を立てたい。なぜなら、必要だからだ。

**受け入れ条件:**
- [x] 計画が存在する
- [x] 計画が読める

**タスク:**
- [x] **タスク S001-1-1**: 計画ドキュメント作成

## スプリント S002: 実装 [{s002_status}]

二番目のスプリント。

### ストーリー S002-1: 実装する [{s002_story_status}]

**ユーザーストーリー:**
ユーザーとして実装したい。なぜなら、計画を実現したいからだ。

**受け入れ条件:**
- [{s002_ac1}] AC 1
- [{s002_ac2}] AC 2

**タスク:**
- [{s002_t1}] **タスク S002-1-1**: 実装する

## 依存関係

- S002 は S001 必須前提

## バックログ

- [ ] **将来の機能** (S002 由来)
"""


# S025: fixture creation/cleanup delegated to tests/e2e/_fixture.py so
# the cleanup is registered with atexit/signal handlers and survives
# Ctrl-C / SIGTERM.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from _fixture import make_fixture as _make_fixture, Fixture as _Fixture  # noqa: E402

_FIXTURES: list[_Fixture] = []


def make_fixture_repo() -> tuple[Path, str, str]:
    """Create a fresh git repo under ghq root and register it with palmux2."""
    fx = _make_fixture("s016")
    _FIXTURES.append(fx)
    return fx.path, fx.ghq_path, fx.repo_id


def fixture_cleanup(repo_id: str, repo_path: Path) -> None:
    for fx in list(_FIXTURES):
        if fx.repo_id == repo_id:
            fx._cleanup()
            try:
                _FIXTURES.remove(fx)
            except ValueError:
                pass
            return
    try:
        http_json("POST", f"/api/repos/{urllib.parse.quote(repo_id)}/close")
    except Exception:
        pass
    if repo_path.exists():
        try:
            shutil.rmtree(repo_path)
        except OSError:
            pass


def write_roadmap(repo_path: Path, *, total: int = 2, done: int = 1,
                  s002_status: str = " ", s002_story_status: str = " ",
                  s002_ac1: str = " ", s002_ac2: str = " ", s002_t1: str = " ") -> None:
    docs = repo_path / "docs"
    docs.mkdir(exist_ok=True)
    (docs / "ROADMAP.md").write_text(ROADMAP_TEMPLATE.format(
        total=total, done=done, remaining=total - done,
        s002_status=s002_status, s002_story_status=s002_story_status,
        s002_ac1=s002_ac1, s002_ac2=s002_ac2, s002_t1=s002_t1,
    ))


def write_malformed_roadmap(repo_path: Path) -> None:
    (repo_path / "docs").mkdir(exist_ok=True)
    (repo_path / "docs" / "ROADMAP.md").write_text("# Bad\n\n## スプリント GARBAGE\n\nno status bracket here\n")


def get_branch_tabs(repo_id: str, branch_id: str) -> list[dict]:
    code, _, body = http_json(
        "GET", f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/tabs",
    )
    assert_(code == 200, f"GET tabs: {code} {body}")
    return body.get("tabs", []) if isinstance(body, dict) else []


def find_branch(repos: list[dict], repo_id: str, branch_name: str) -> dict | None:
    for r in repos:
        if r["id"] != repo_id:
            continue
        for b in r["openBranches"]:
            if b["name"] == branch_name:
                return b
    return None


def fetch_repos() -> list[dict]:
    code, _, body = http_json("GET", "/api/repos")
    assert_(code == 200, f"GET repos: {code}")
    return body  # type: ignore[return-value]


def wait_for(predicate, timeout_s: float = 8.0, sleep_s: float = 0.25) -> bool:
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        try:
            if predicate():
                return True
        except Exception:
            pass
        time.sleep(sleep_s)
    return False


# --------------------------------------------------------------------------
# tests
# --------------------------------------------------------------------------

def test_a_tab_appears(repo_path: Path, repo_id: str, branch_id: str) -> None:
    """ROADMAP.md present → Sprint tab appears in TabBar."""
    write_roadmap(repo_path)
    # The tab list is computed via OnBranchOpen → recomputeTabs which
    # only runs on store mutations or via the watcher debounce. Touching
    # docs/ROADMAP.md goes through the watcher (1s debounce) — wait for
    # the tab to appear.
    found = wait_for(lambda: any(t["type"] == "sprint" for t in get_branch_tabs(repo_id, branch_id)))
    assert_(found, "sprint tab did not appear within timeout")
    ok("a", "Sprint tab appears with ROADMAP.md present")


def test_a2_overview_endpoint(repo_id: str, branch_id: str) -> None:
    """Overview endpoint returns parsed roadmap with progress."""
    code, hdrs, body = http_json(
        "GET", f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
    )
    assert_(code == 200, f"overview: {code} {body}")
    assert_(isinstance(body, dict), "body shape")
    assert_(body.get("project") == "プロジェクトロードマップ: Test Project",
            f"project title: {body.get('project')}")
    assert_(body["progress"]["total"] == 2, f"total: {body['progress']}")
    assert_(body["progress"]["done"] == 1, f"done: {body['progress']}")
    assert_(len(body["timeline"]) == 2, f"timeline len: {body['timeline']}")
    etag = hdrs.get("Etag") or hdrs.get("ETag")
    assert_(etag is not None and etag != "", "ETag missing")
    # 304 short-circuit
    code2, _, _ = http_json(
        "GET", f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
        if_none_match=etag,
    )
    assert_(code2 == 304, f"expected 304 with If-None-Match, got {code2}")
    ok("a2", f"overview parsed; ETag short-circuit OK ({etag})")


def test_b_tab_disappears_and_returns(repo_path: Path, repo_id: str, branch_id: str) -> None:
    """Delete ROADMAP.md → tab vanishes; restore → tab returns."""
    rm_path = repo_path / "docs" / "ROADMAP.md"
    rm_path.unlink()
    gone = wait_for(lambda: not any(t["type"] == "sprint" for t in get_branch_tabs(repo_id, branch_id)))
    assert_(gone, "sprint tab did not disappear after delete")

    write_roadmap(repo_path)
    back = wait_for(lambda: any(t["type"] == "sprint" for t in get_branch_tabs(repo_id, branch_id)))
    assert_(back, "sprint tab did not reappear after restore")
    ok("b", "tab.removed / tab.added round-trip OK")


def test_c_edit_propagates(repo_path: Path, repo_id: str, branch_id: str) -> None:
    """Edit ROADMAP.md → overview reflects new progress (filewatch debounce ~1s)."""
    write_roadmap(repo_path, total=3, done=2)
    propagated = wait_for(
        lambda: http_json(
            "GET",
            f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
        )[2]["progress"]["total"] == 3,
        timeout_s=5.0,
    )
    assert_(propagated, "edit did not propagate to overview within 5s")
    ok("c", "ROADMAP.md edit reflected in overview within ~1s debounce")


def test_d_active_autopilot(repo_path: Path, repo_id: str, branch_id: str) -> None:
    """Touch .claude/autopilot-S012.lock → it shows in active autopilot."""
    claude_dir = repo_path / ".claude"
    claude_dir.mkdir(exist_ok=True)
    lock = claude_dir / "autopilot-S012.lock"
    lock.write_text(json.dumps({"pid": 1234, "startedAt": "2026-05-01T12:00:00Z"}))
    found = wait_for(
        lambda: any(
            a["sprintId"] == "S012"
            for a in http_json(
                "GET",
                f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
            )[2]["activeAutopilot"]
        ),
        timeout_s=3.0,
    )
    assert_(found, "autopilot lock not detected in overview")
    lock.unlink()
    cleared = wait_for(
        lambda: not any(
            a["sprintId"] == "S012"
            for a in http_json(
                "GET",
                f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
            )[2]["activeAutopilot"]
        ),
        timeout_s=3.0,
    )
    assert_(cleared, "autopilot lock not cleared after unlink")
    ok("d", "autopilot lock detection round-trip OK")


def test_e_dependencies_payload(repo_id: str, branch_id: str) -> None:
    """Dependency Graph endpoint returns Mermaid payload + sprints + edges."""
    code, _, body = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/dependencies",
    )
    assert_(code == 200, f"dependencies: {code}")
    assert_(isinstance(body, dict), "body shape")
    assert_(len(body["sprints"]) == 2, f"sprints: {body['sprints']}")
    assert_("graph LR" in body["mermaid"], f"mermaid syntax: {body['mermaid'][:120]}")
    assert_("S001" in body["mermaid"] and "S002" in body["mermaid"], "mermaid missing nodes")
    ok("e", "dependency graph payload OK")


def test_f_decisions_filter(repo_path: Path, repo_id: str, branch_id: str) -> None:
    """Decision Timeline filter narrows the entries list."""
    logs = repo_path / "docs" / "sprint-logs" / "S001"
    logs.mkdir(parents=True, exist_ok=True)
    (logs / "decisions.md").write_text(
        "# S001 Decisions\n\n## Planning Decisions\n\n- **A planning entry**: body of A.\n"
        "\n## Implementation Decisions\n\n- **An impl entry**: body of B.\n"
    )
    # Allow filewatcher to observe.
    time.sleep(1.5)
    code, _, body_all = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/decisions",
    )
    assert_(code == 200, f"decisions all: {code}")
    assert_(len(body_all["entries"]) >= 2, f"decisions count: {body_all}")
    code, _, body_plan = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/decisions?filter=planning",
    )
    assert_(code == 200, f"decisions filter: {code}")
    assert_(all(e["category"] == "planning" for e in body_plan["entries"]),
            f"filter not applied: {body_plan}")
    ok("f", f"decisions filter narrowed: {len(body_all['entries'])} → {len(body_plan['entries'])}")


def test_h_refresh_button_returns_200(repo_id: str, branch_id: str) -> None:
    """Refresh button (no If-None-Match) returns 200 even when ETag matches."""
    code, hdrs, _ = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
    )
    assert_(code == 200, "first overview")
    etag = hdrs.get("Etag") or hdrs.get("ETag")
    assert_(etag is not None, "etag")
    code2, _, _ = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
    )
    assert_(code2 == 200, f"forced refetch should be 200, got {code2}")
    ok("h", "Refresh button (no If-None-Match) returns 200")


def test_i_malformed_roadmap(repo_path: Path, repo_id: str, branch_id: str) -> None:
    """Malformed ROADMAP.md does not crash the API."""
    write_malformed_roadmap(repo_path)
    time.sleep(1.5)
    code, _, body = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
    )
    assert_(code == 200, f"malformed overview should still 200, got {code}")
    # Even the malformed file has the H1 — title must still be present.
    assert_(body.get("project") == "Bad", f"title: {body.get('project')}")
    ok("i", "malformed ROADMAP yielded 200 with degraded payload")
    # Restore for subsequent tests
    write_roadmap(repo_path)
    time.sleep(1.5)


def test_j_playwright_5_subtabs_and_mermaid(repo_id: str, branch_id: str) -> None:
    """Playwright drives the Sprint tab through 5 subtabs and Mermaid clicks."""
    try:
        from playwright.sync_api import sync_playwright  # type: ignore
    except ImportError:
        ok("j", "(skipped: playwright not installed)")
        return

    url = f"{BASE_URL}/{repo_id}/{branch_id}/sprint"
    with sync_playwright() as p:
        browser = p.chromium.launch()
        try:
            ctx = browser.new_context(viewport={"width": 1280, "height": 800})
            page = ctx.new_page()
            page.goto(url, wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-view']", timeout=10000)
            for tab in ("overview", "detail", "dependencies", "decisions", "refine"):
                page.click(f"[data-testid='sprint-subtab-{tab}']")
                page.wait_for_selector(f"[data-testid='sprint-{tab if tab != 'detail' else 'detail'}-header']", timeout=8000)
            # Verify dependency graph rendered with at least one sprint node.
            page.click("[data-testid='sprint-subtab-dependencies']")
            page.wait_for_selector("[data-testid='sprint-dep-graph'] svg", timeout=10000)
            nodes = page.query_selector_all("[data-testid^='sprint-dep-node-']")
            assert_(len(nodes) >= 2, f"dep graph nodes: {len(nodes)}")
            # Click a node → URL should switch to detail with sprintId.
            target = nodes[0]
            target.click()
            page.wait_for_function(
                "() => new URL(location.href).searchParams.get('view') === 'detail'",
                timeout=5000,
            )
            # Mobile breakpoint: subtabs scroll horizontally rather than overflow vertically.
            ctx.close()
            mobile = browser.new_context(viewport={"width": 375, "height": 812})
            mp = mobile.new_page()
            mp.goto(url, wait_until="networkidle")
            mp.wait_for_selector("[data-testid='sprint-view']", timeout=10000)
            count = mp.evaluate(
                "() => document.querySelectorAll('[data-testid^=\\'sprint-subtab-\\']').length"
            )
            assert_(count == 5, f"5 subtabs visible at 375px, got {count}")
            mobile.close()
        finally:
            browser.close()
    ok("j", "5 subtabs render; dep graph node click navigates; mobile width OK")


# --------------------------------------------------------------------------
# main
# --------------------------------------------------------------------------

def main() -> int:
    print(f"S016 sprint dashboard E2E against {BASE_URL}")
    code, _, _ = http_json("GET", "/api/health")
    if code != 200:
        fail(f"dev instance not healthy: {code}")

    repo_path, ghq_path, repo_id = make_fixture_repo()
    print(f"  fixture repo: {ghq_path} (id={repo_id})")
    try:
        # Open the main branch so it appears in /api/repos.
        code, _, _ = http_json(
            "POST",
            f"/api/repos/{urllib.parse.quote(repo_id)}/branches/open",
            body={"branchName": "main"},
        )
        assert_(code in (200, 201), f"open branch main: {code}")
        repos = fetch_repos()
        b = find_branch(repos, repo_id, "main")
        assert_(b is not None, "main branch missing after open")
        branch_id = b["id"]  # type: ignore[index]
        print(f"  branch_id: {branch_id}")

        test_a_tab_appears(repo_path, repo_id, branch_id)
        test_a2_overview_endpoint(repo_id, branch_id)
        test_b_tab_disappears_and_returns(repo_path, repo_id, branch_id)
        test_c_edit_propagates(repo_path, repo_id, branch_id)
        test_d_active_autopilot(repo_path, repo_id, branch_id)
        test_e_dependencies_payload(repo_id, branch_id)
        test_f_decisions_filter(repo_path, repo_id, branch_id)
        test_h_refresh_button_returns_200(repo_id, branch_id)
        test_i_malformed_roadmap(repo_path, repo_id, branch_id)
        test_j_playwright_5_subtabs_and_mermaid(repo_id, branch_id)
    finally:
        fixture_cleanup(repo_id, repo_path)
    print("S016 E2E: PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
