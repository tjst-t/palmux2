#!/usr/bin/env python3
"""S016-fix-1 — Sprint Dashboard parser i18n + null safety.

Reproduces the regression behind S016-fix-1: a roadmap with English
section headers (`## Progress`, `## Sprint S001: ... [DONE]`,
`### Story S001-1: ... [x]`, `**Task S001-1-1**: ...`) crashed the
Sprint tab with `Cannot read properties of null (reading 'map')`
because the Go parser only matched Japanese headers and emitted JSON
`null` for `Sprints` / `Timeline`.

This script exercises three scenarios:

  (i18n)  English-header ROADMAP → parser extracts sprints / progress
          / dependencies, FE Sprint tab renders without crashing.
  (mixed) Japanese title under English heading still parses.
  (empty) ROADMAP with no sprints → API returns 200 with
          empty arrays (not nulls), FE renders empty state.

Hermetic: creates a fresh ghq-rooted fixture repo, registers it via
/api/repos/open, restores cleanup at the end.
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


def http_json(method: str, path: str, *, body: dict | list | None = None) -> tuple[int, dict, dict | list | str]:
    raw = json.dumps(body).encode() if body is not None else None
    h: dict[str, str] = {"Accept": "application/json"}
    if body is not None:
        h["Content-Type"] = "application/json"
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
# fixtures
# --------------------------------------------------------------------------

ROADMAP_EN = """\
# Project Roadmap: hydra-style

> An English-headered roadmap to exercise i18n parsing.

## Progress

- Total: 3 Sprints | Done: 1 | In Progress: 1 | Remaining: 1
- [█████░░░░░] 33%

## Execution Order

S001 -> **S002** -> S003

## Sprint S001: Project init [DONE]

Bootstraps the monorepo.

### Story S001-1: Go module setup [x]

- [x] **Task S001-1-1**: go mod init and directory layout
- [x] **Task S001-1-2**: Makefile targets

## Sprint S002: HTTP server [IN PROGRESS]

Implements the API layer.

### Story S002-1: routing skeleton [ ]

- [ ] **Task S002-1-1**: HTTP server boot
- [ ] **Task S002-1-2**: routing

## Sprint S003: Storage [ ]

Persistence layer.

### Story S003-1: schema [ ]

- [ ] **Task S003-1-1**: migrations

## Dependencies

- S002 depends on S001
- S003 depends on S002

## Backlog

- [ ] Future feature (S003 follow-up)
"""

ROADMAP_MIXED = """\
# Mixed Roadmap

## Progress

- Total: 1 Sprints | Done: 0 | In Progress: 1 | Remaining: 1

## Sprint S099: 日本語タイトル混在 [IN PROGRESS]

混在ヘッダーのテスト。

### ストーリー S099-1: English heading で Japanese story [ ]

- [ ] **Task S099-1-1**: タスク
"""

# An empty / nearly-empty roadmap — no sprint sections at all. Was
# crashing the FE before the fix because `Sprints` / `Timeline` were
# encoded as JSON `null`.
ROADMAP_EMPTY = """\
# Empty Roadmap

> No sprints yet — just stub the file.

## Progress

- Total: 0 Sprints | Done: 0 | In Progress: 0 | Remaining: 0
"""


# S025: fixture creation/cleanup delegated to tests/e2e/_fixture.py.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from _fixture import make_fixture as _make_fixture, Fixture as _Fixture  # noqa: E402

_FIXTURES: list[_Fixture] = []


def make_fixture_repo(suffix: str) -> tuple[Path, str, str]:
    fx = _make_fixture(f"s016-fix1-{suffix}")
    _FIXTURES.append(fx)
    code, _, _ = http_json(
        "POST", f"/api/repos/{urllib.parse.quote(fx.repo_id)}/branches/open",
        body={"branchName": "main"},
    )
    assert_(code in (200, 201), f"open branch main: {code}")
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


def find_branch_id(repo_id: str) -> str:
    code, _, repos = http_json("GET", "/api/repos")
    assert_(code == 200, f"repos: {code}")
    for r in repos:  # type: ignore[union-attr]
        if r["id"] != repo_id:
            continue
        for b in r["openBranches"]:
            if b["name"] == "main":
                return b["id"]
    fail(f"main branch not found for {repo_id}")
    return ""  # unreachable


# --------------------------------------------------------------------------
# tests
# --------------------------------------------------------------------------

def test_english_headers(repo_id: str, branch_id: str, repo_path: Path) -> None:
    """English-header ROADMAP parses; arrays are real arrays."""
    docs = repo_path / "docs"
    docs.mkdir(exist_ok=True)
    (docs / "ROADMAP.md").write_text(ROADMAP_EN)

    # Wait for the watcher debounce to make the tab appear.
    def has_sprint_tab() -> bool:
        code, _, body = http_json(
            "GET",
            f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/tabs",
        )
        if code != 200 or not isinstance(body, dict):
            return False
        return any(t["type"] == "sprint" for t in body.get("tabs", []))

    assert_(wait_for(has_sprint_tab), "Sprint tab did not appear with English ROADMAP")

    code, _, body = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
    )
    assert_(code == 200, f"overview: {code} {body}")
    assert_(isinstance(body, dict), "overview body shape")
    assert_(body.get("project") == "Project Roadmap: hydra-style",
            f"title: {body.get('project')}")
    # Progress parsed from English line.
    assert_(body["progress"]["total"] == 3, f"total: {body['progress']}")
    assert_(body["progress"]["done"] == 1, f"done: {body['progress']}")
    assert_(body["progress"]["inProgress"] == 1, f"inProgress: {body['progress']}")
    # Timeline must be a real array (not null), with all 3 sprints.
    timeline = body.get("timeline")
    assert_(isinstance(timeline, list), f"timeline must be list, got {type(timeline).__name__}")
    assert_(len(timeline) == 3, f"timeline len: {timeline}")
    ids = sorted(t["id"] for t in timeline)
    assert_(ids == ["S001", "S002", "S003"], f"timeline ids: {ids}")
    kinds = {t["id"]: t["statusKind"] for t in timeline}
    assert_(kinds["S001"] == "done", f"S001 kind: {kinds}")
    assert_(kinds["S002"] == "in-progress", f"S002 kind: {kinds}")
    assert_(kinds["S003"] == "pending", f"S003 kind: {kinds}")
    # currentSprint should be the first non-done — S002.
    assert_(body.get("currentSprint", {}).get("id") == "S002",
            f"currentSprint: {body.get('currentSprint')}")
    assert_(isinstance(body.get("activeAutopilot"), list), "activeAutopilot must be list")

    # Sprint detail for S001 should expose stories and tasks.
    code, _, sd = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/sprints/S001",
    )
    assert_(code == 200, f"sprintDetail S001: {code} {sd}")
    sprint = sd["sprint"]
    assert_(isinstance(sprint["stories"], list) and len(sprint["stories"]) == 1,
            f"S001 stories: {sprint.get('stories')}")
    story = sprint["stories"][0]
    assert_(story["id"] == "S001-1", f"S001 story id: {story}")
    assert_(isinstance(story["tasks"], list) and len(story["tasks"]) == 2,
            f"S001-1 tasks: {story.get('tasks')}")
    assert_(story["tasks"][0]["id"] == "S001-1-1", f"task id: {story['tasks']}")

    # Dependencies endpoint parses English "depends on" phrasing — the
    # parser extracts S-IDs from the line and the sprint list is
    # populated as an array, never null.
    code, _, dep = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/dependencies",
    )
    assert_(code == 200, f"dependencies: {code}")
    assert_(isinstance(dep["sprints"], list) and len(dep["sprints"]) == 3,
            f"dep sprints: {dep.get('sprints')}")
    assert_(isinstance(dep["dependencies"], list), "deps must be list")
    ok("english", "English-header ROADMAP fully parsed; arrays not null")


def test_mixed_headers(repo_id: str, branch_id: str, repo_path: Path) -> None:
    """English heading + Japanese story title + JP heading still parse."""
    (repo_path / "docs" / "ROADMAP.md").write_text(ROADMAP_MIXED)
    time.sleep(1.5)
    code, _, body = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
    )
    assert_(code == 200, f"overview mixed: {code}")
    assert_(len(body["timeline"]) == 1, f"timeline: {body['timeline']}")
    assert_(body["timeline"][0]["id"] == "S099", f"sprint id: {body['timeline']}")
    code, _, sd = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/sprints/S099",
    )
    assert_(code == 200, f"sprintDetail S099: {code}")
    assert_(len(sd["sprint"]["stories"]) == 1, f"stories: {sd['sprint'].get('stories')}")
    assert_(sd["sprint"]["stories"][0]["id"] == "S099-1",
            f"story id: {sd['sprint']['stories'][0]}")
    ok("mixed", "Mixed JP/EN headers parse correctly")


def test_empty_roadmap(repo_id: str, branch_id: str, repo_path: Path) -> None:
    """No-sprint ROADMAP yields 200 with empty (not null) arrays."""
    (repo_path / "docs" / "ROADMAP.md").write_text(ROADMAP_EMPTY)
    time.sleep(1.5)
    code, _, body = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
    )
    assert_(code == 200, f"overview empty: {code}")
    # Critical: timeline / activeAutopilot must be [] (not null) so the
    # FE's `.map()` and `.length` calls don't blow up.
    assert_(body.get("timeline") == [], f"timeline must be [], got {body.get('timeline')}")
    assert_(body.get("activeAutopilot") == [], f"activeAutopilot must be []: {body.get('activeAutopilot')}")
    code, _, dep = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/dependencies",
    )
    assert_(code == 200, f"dependencies empty: {code}")
    assert_(dep.get("sprints") == [], f"sprints must be []: {dep.get('sprints')}")
    assert_(dep.get("dependencies") == [], f"dependencies must be []: {dep.get('dependencies')}")
    ok("empty", "no-sprint ROADMAP yields empty arrays, not null")


def test_playwright_no_crash(repo_id: str, branch_id: str, repo_path: Path) -> None:
    """Playwright drives the Sprint tab on an English ROADMAP — no JS crash."""
    try:
        from playwright.sync_api import sync_playwright  # type: ignore
    except ImportError:
        ok("playwright", "(skipped: playwright not installed)")
        return

    # Restore the English ROADMAP so the tab is rich.
    (repo_path / "docs" / "ROADMAP.md").write_text(ROADMAP_EN)
    time.sleep(1.5)

    url = f"{BASE_URL}/{repo_id}/{branch_id}/sprint?view=overview"
    with sync_playwright() as p:
        browser = p.chromium.launch()
        try:
            ctx = browser.new_context(viewport={"width": 1280, "height": 800})
            page = ctx.new_page()
            page_errors: list[str] = []
            page.on("pageerror", lambda exc: page_errors.append(str(exc)))
            page.goto(url, wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-view']", timeout=10000)
            # Overview should render the timeline with 3 dots.
            page.wait_for_selector(
                "[data-testid^='sprint-timeline-']",
                state="attached",
                timeout=8000,
            )
            timeline_dots = page.query_selector_all("[data-testid^='sprint-timeline-']")
            assert_(len(timeline_dots) == 3, f"timeline dots: {len(timeline_dots)}")
            # Click through each subtab — none should throw.
            for tab in ("detail", "dependencies", "decisions", "refine", "overview"):
                page.click(f"[data-testid='sprint-subtab-{tab}']")
                page.wait_for_selector("[data-testid='sprint-view']", timeout=8000)
                # Small settle wait so any async render can fire its
                # error before we move on.
                page.wait_for_timeout(200)
            assert_(
                not page_errors,
                f"page errors during sprint navigation:\n  - " + "\n  - ".join(page_errors),
            )

            # Now switch to the empty roadmap and revisit Overview — must
            # not crash even when timeline / activeAutopilot arrays are
            # empty (would have been null pre-fix and crashed `.map()`).
            (repo_path / "docs" / "ROADMAP.md").write_text(ROADMAP_EMPTY)
            page.wait_for_timeout(2000)  # filewatch debounce
            page.click("[data-testid='sprint-subtab-overview']")
            # `state="attached"` because the empty timeline div has no
            # content and therefore zero height (not "visible").
            page.wait_for_selector(
                "[data-testid='sprint-overview-timeline']",
                state="attached",
                timeout=8000,
            )
            page.wait_for_timeout(500)
            assert_(
                not page_errors,
                f"page errors with empty ROADMAP:\n  - " + "\n  - ".join(page_errors),
            )
        finally:
            browser.close()
    ok("playwright", "no JS crash navigating Sprint tab on English / empty ROADMAP")


# --------------------------------------------------------------------------
# main
# --------------------------------------------------------------------------

def main() -> int:
    print(f"S016-fix-1 i18n + null safety E2E against {BASE_URL}")
    code, _, _ = http_json("GET", "/api/health")
    if code != 200:
        fail(f"dev instance not healthy: {code}")

    repo_path, ghq_path, repo_id = make_fixture_repo("i18n")
    print(f"  fixture repo: {ghq_path} (id={repo_id})")
    try:
        branch_id = find_branch_id(repo_id)
        print(f"  branch_id: {branch_id}")
        test_english_headers(repo_id, branch_id, repo_path)
        test_mixed_headers(repo_id, branch_id, repo_path)
        test_empty_roadmap(repo_id, branch_id, repo_path)
        test_playwright_no_crash(repo_id, branch_id, repo_path)
    finally:
        fixture_cleanup(repo_id, repo_path)
    print("S016-fix-1 E2E: PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
