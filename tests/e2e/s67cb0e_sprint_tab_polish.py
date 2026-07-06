#!/usr/bin/env python3
"""Sprint S67cb0e — Sprint tab UX polish E2E.

Tests the four stories delivered in S67cb0e:

  Story 1 (AC-1): Overview timeline → JIRA-style table with 5 columns,
    status pills, depends-on chips (clickable), milestone ★, keyboard nav,
    sticky thead.
  Story 2 (AC-2): pushState history — browser back/forward across
    Overview↔Detail and Detail(A)↔Detail(B), plus invalid ?view= normalization.
  Story 3 (AC-3): Prev/Next + dropdown in Sprint Detail.
  Story 4 (AC-4): Markdown rendering for description in Detail and Overview.

Every ROADMAP acceptance criterion (AC-S67cb0e-1-1..1-6, 2-1..2-4, 3-1..3-6,
4-1..4-5) is verified by exactly one authoritative assertion. A handful of
API-level checks reuse a tag where they verify the same AC as the authoritative
browser check (noted inline). Nothing is silently skipped.

Fixture: 3 sprints in execution_order (S_A done, S_B in_progress+milestone,
S_C pending), a dependencies map giving S_B a prereq (S_A), and S_B's
description containing Markdown (heading, list, inline code, fenced code, GFM
table, emphasis, external https link).

Playwright is REQUIRED for this suite. If it is not installed the suite fails
(non-zero exit); there is no silent skip path.

Exit 0 = PASS, non-zero = FAIL.
"""
from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or "8204"
)
BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 20.0

sys.path.insert(0, str(Path(__file__).resolve().parent))
from _fixture import palmux2_test_fixture  # noqa: E402


# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------

def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(tag: str, msg: str = "") -> None:
    print(f"  [{tag}] {msg or 'OK'}")


def assert_(cond: bool, msg: str) -> None:
    if not cond:
        fail(msg)


def http_json(
    method: str,
    path: str,
    *,
    body: dict | list | None = None,
    if_none_match: str | None = None,
) -> tuple[int, dict, dict | list | str]:
    raw = json.dumps(body).encode() if body is not None else None
    h: dict[str, str] = {"Accept": "application/json"}
    if body is not None:
        h["Content-Type"] = "application/json"
    if if_none_match:
        h["If-None-Match"] = if_none_match
    url = f"{BASE_URL}{path}"
    req = urllib.request.Request(url, method=method, data=raw, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            data = resp.read()
            try:
                decoded = json.loads(data.decode() or "{}")
            except json.JSONDecodeError:
                decoded = data.decode(errors="replace")
            return resp.status, dict(resp.headers), decoded
    except urllib.error.HTTPError as e:
        data = e.read()
        try:
            decoded = json.loads(data.decode() or "{}")
        except json.JSONDecodeError:
            decoded = data.decode(errors="replace")
        return e.code, dict(e.headers or {}), decoded


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


# ---------------------------------------------------------------------------
# Fixture data
# ---------------------------------------------------------------------------

MARKDOWN_DESCRIPTION = """\
## Sprint goals

This sprint delivers **Sprint tab UX polish** with *several* moving parts:

- JIRA-style timeline table with `milestone` and `dependsOn` fields
- pushState history for in-tab navigation
- Prev/Next + dropdown in Sprint Detail

### Acceptance checklist

| Story | Status |
|-------|--------|
| S67cb0e-1 | pending |
| S67cb0e-4 | pending |

```go
func main() { fmt.Println("fenced code block") }
```

External reference: [Anthropic docs](https://docs.anthropic.com)
"""


def make_roadmap_json() -> dict:
    return {
        "project": "S67cb0e Test Project",
        "description": "E2E fixture for S67cb0e sprint tab polish",
        "progress": {
            "current_sprint": "S_B",
            "total": 3,
            "done": 1,
            "in_progress": 1,
            "remaining": 1,
            "percentage": 33.3,
        },
        "execution_order": ["S_A", "S_B", "S_C"],
        "sprints": {
            "S_A": {
                "title": "Alpha sprint",
                "status": "done",
                "description": "Simple first sprint.",
                "milestone": False,
                "stories": {},
            },
            "S_B": {
                "title": "Beta sprint — milestone",
                "status": "in_progress",
                "description": MARKDOWN_DESCRIPTION,
                "milestone": True,
                "stories": {},
            },
            "S_C": {
                "title": "Gamma sprint (pending)",
                "status": "pending",
                "description": "",
                "milestone": False,
                "stories": {},
            },
        },
        "dependencies": {
            "S_B": {"depends_on": ["S_A"], "reason": "B builds on A"},
            "S_C": {"depends_on": ["S_B"], "reason": "C builds on B"},
        },
        "backlog": [],
    }


def write_roadmap(repo_path: Path, doc: dict) -> None:
    docs = repo_path / "docs"
    docs.mkdir(exist_ok=True)
    (docs / "ROADMAP.json").write_text(json.dumps(doc, ensure_ascii=False, indent=2))


def get_branch_tabs(repo_id: str, branch_id: str) -> list[dict]:
    code, _, body = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/tabs",
    )
    assert_(code == 200, f"GET tabs: {code} {body}")
    return body.get("tabs", []) if isinstance(body, dict) else []


# ---------------------------------------------------------------------------
# Backend API supporting checks
#
# These verify the data that backs the browser-rendered UI. Each shares its
# tag with the authoritative browser assertion of the SAME AC (noted inline),
# or owns a tag where the AC is itself an API-contract criterion (3-6).
# ---------------------------------------------------------------------------

def test_overview_timeline_fields(repo_id: str, branch_id: str) -> None:
    """API support for AC-1-2 (status), AC-1-3 (deps), AC-1-4 (milestone)."""
    code, _, body = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
    )
    assert_(code == 200, f"overview: {code} {body}")
    timeline = body.get("timeline", [])
    assert_(len(timeline) == 3, f"expected 3 timeline entries, got {len(timeline)}: {timeline}")

    by_id = {t["id"]: t for t in timeline}

    a = by_id.get("S_A")
    b = by_id.get("S_B")
    c = by_id.get("S_C")
    assert_(a is not None and b is not None and c is not None, "missing sprint(s) in timeline")

    # statusKind backing the rendered pills (authoritative pill check is AC-1-2 browser).
    assert_(a["statusKind"] == "done", f"S_A statusKind: {a['statusKind']}")
    assert_(b["statusKind"] == "in-progress", f"S_B statusKind: {b['statusKind']}")
    assert_(c["statusKind"] == "pending", f"S_C statusKind: {c['statusKind']}")
    ok("AC-S67cb0e-1-2", "API: statusKind backs pills (done / in-progress / pending)")

    # dependsOn backing the rendered chips (authoritative chip nav is AC-1-3 browser).
    assert_(a.get("dependsOn") == [], f"S_A dependsOn should be [], got {a.get('dependsOn')}")
    assert_(isinstance(a.get("dependsOn"), list), f"dependsOn must be list (not null): {a}")
    assert_(b.get("dependsOn") == ["S_A"], f"S_B dependsOn should be ['S_A'], got {b.get('dependsOn')}")
    assert_(c.get("dependsOn") == ["S_B"], f"S_C dependsOn should be ['S_B'], got {c.get('dependsOn')}")
    ok("AC-S67cb0e-1-3", "API: dependsOn backs chips (S_B→S_A, S_C→S_B; empty serializes as [])")

    # milestone flags backing the rendered ★ (authoritative ★ check is AC-1-4 browser).
    assert_(a.get("milestone") is False, f"S_A milestone should be false: {a}")
    assert_(b.get("milestone") is True, f"S_B milestone should be true: {b}")
    assert_(c.get("milestone") is False, f"S_C milestone should be false: {c}")
    ok("AC-S67cb0e-1-4", "API: milestone flags back ★ (S_B=true, S_A/S_C=false)")


def test_overview_execution_order(repo_id: str, branch_id: str) -> None:
    """[AC-S67cb0e-3-6] No new endpoint: ordered sprint list comes from /sprint/overview."""
    code, _, body = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/overview",
    )
    assert_(code == 200, f"overview: {code}")
    ids = [t["id"] for t in body.get("timeline", [])]
    assert_(ids == ["S_A", "S_B", "S_C"], f"execution order mismatch: {ids}")
    ok("AC-S67cb0e-3-6", f"prev/next order sourced from existing /sprint/overview timeline: {ids}")


def test_detail_description_source(repo_id: str, branch_id: str) -> None:
    """API support for AC-4-1: sprint detail carries the markdown source."""
    code, _, body = http_json(
        "GET",
        f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint/sprints/S_B",
    )
    assert_(code == 200, f"sprint detail: {code} {body}")
    desc = body.get("sprint", {}).get("description", "")
    assert_("Sprint goals" in desc, f"description missing 'Sprint goals' heading: {desc[:200]}")
    assert_("| Story |" in desc, f"description missing GFM table: {desc[:200]}")
    assert_("docs.anthropic.com" in desc, f"description missing external link: {desc[:200]}")
    ok("AC-S67cb0e-4-1", "API: sprint detail description carries Markdown source")


# ---------------------------------------------------------------------------
# Browser (Playwright) tests — REQUIRED. No silent skip.
# ---------------------------------------------------------------------------

def test_playwright_stories(repo_id: str, branch_id: str) -> None:
    """Browser-driven authoritative assertions for Stories 1–4."""
    try:
        from playwright.sync_api import sync_playwright  # type: ignore  # noqa: PLC0415
    except ImportError:
        fail("playwright not installed — required for S67cb0e E2E")

    overview_url = f"{BASE_URL}/{repo_id}/{branch_id}/sprint?view=overview"
    detail_url = (
        lambda sid: f"{BASE_URL}/{repo_id}/{branch_id}/sprint?view=detail&sprintId={sid}"
    )

    with sync_playwright() as p:
        browser = p.chromium.launch()
        try:
            ctx = browser.new_context(viewport={"width": 1280, "height": 800})
            page = ctx.new_page()

            # ================================================================
            # Story 1: JIRA-style timeline table
            # ================================================================
            page.goto(overview_url, wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-view']", timeout=10000)
            page.wait_for_selector("[data-testid='sprint-timeline-table']", timeout=10000)
            # Se173ef: the Overview timeline now defaults to "未完了のみ", which
            # hides done sprints (S_A). Reveal all so the done-sprint rows this
            # suite exercises are present. (Same ACs, just a new default filter.)
            page.locator("[data-testid='sprint-timeline-filter-all']").click()
            page.wait_for_selector("[data-testid='sprint-timeline-S_A']", timeout=8000)

            # [AC-S67cb0e-1-1] Table renders with the 5 expected column headers.
            table = page.locator("[data-testid='sprint-timeline-table']")
            assert_(table.count() == 1, "sprint-timeline-table not found")
            headers = page.locator("[data-testid='sprint-timeline-table'] thead th")
            n_headers = headers.count()
            assert_(n_headers == 5, f"expected 5 column headers, got {n_headers}")
            header_texts = [headers.nth(i).inner_text().strip() for i in range(n_headers)]
            expected_headers = ["Sprint", "Title", "Status", "Depends on", "Milestone"]
            for want in expected_headers:
                assert_(
                    any(want.lower() in h.lower() for h in header_texts),
                    f"missing column header {want!r}; got {header_texts}",
                )
            ok("AC-S67cb0e-1-1", f"table renders with 5 headers: {header_texts}")

            # [AC-S67cb0e-1-2] Status pill element rendered per row with correct status.
            status_a = page.locator("[data-testid='sprint-timeline-status-S_A']")
            assert_(status_a.count() == 1, "status pill sprint-timeline-status-S_A not found")
            status_a_text = status_a.inner_text().strip()
            assert_("done" in status_a_text, f"S_A status pill should read 'done', got {status_a_text!r}")
            # Confirm pills exist for the other rows too (with their statuses).
            status_b_text = page.locator("[data-testid='sprint-timeline-status-S_B']").inner_text().strip()
            status_c_text = page.locator("[data-testid='sprint-timeline-status-S_C']").inner_text().strip()
            assert_("in-progress" in status_b_text, f"S_B pill should read 'in-progress', got {status_b_text!r}")
            assert_("pending" in status_c_text, f"S_C pill should read 'pending', got {status_c_text!r}")
            # Row still carries data-statuskind for color-kind verification.
            row_a_kind = page.get_attribute("[data-testid='sprint-timeline-S_A']", "data-statuskind")
            assert_(row_a_kind == "done", f"S_A row data-statuskind should be 'done', got {row_a_kind!r}")
            ok("AC-S67cb0e-1-2", f"status pills rendered: S_A={status_a_text!r} S_B={status_b_text!r} S_C={status_c_text!r}")

            # [AC-S67cb0e-1-3] Depends-on chip renders AND clicking it navigates to the prereq.
            dep_chip = page.locator("[data-testid='sprint-timeline-dep-S_B-S_A']")
            assert_(dep_chip.count() == 1, "dep chip S_B→S_A not found")
            assert_(dep_chip.inner_text().strip() == "S_A", f"dep chip text should be 'S_A': {dep_chip.inner_text()!r}")
            dep_chip.click()
            page.wait_for_selector("[data-testid='sprint-detail-header']", timeout=8000)
            page.wait_for_function(
                "() => location.search.includes('sprintId=S_A')", timeout=8000
            )
            dep_url = page.url
            assert_("view=detail" in dep_url, f"dep chip click should go to detail view: {dep_url}")
            assert_("sprintId=S_A" in dep_url, f"dep chip should navigate to prereq S_A: {dep_url}")
            assert_("sprintId=S_B" not in dep_url, f"dep chip must NOT navigate to row's own sprint S_B: {dep_url}")
            ok("AC-S67cb0e-1-3", "dep chip click navigates to prereq S_A (stopPropagation + onOpenSprint(dep))")

            # Return to overview for the remaining Story 1 checks.
            page.goto(overview_url, wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-timeline-table']", timeout=10000)
            # Se173ef: the Overview timeline now defaults to "未完了のみ", which
            # hides done sprints (S_A). Reveal all so the done-sprint rows this
            # suite exercises are present. (Same ACs, just a new default filter.)
            page.locator("[data-testid='sprint-timeline-filter-all']").click()
            page.wait_for_selector("[data-testid='sprint-timeline-S_A']", timeout=8000)

            # [AC-S67cb0e-1-4] Milestone ★ present for S_B, absent for non-milestone S_A.
            row_b = page.locator("[data-testid='sprint-timeline-S_B']")
            row_a = page.locator("[data-testid='sprint-timeline-S_A']")
            assert_(row_b.count() == 1 and row_a.count() == 1, "S_A / S_B rows not found")
            assert_("★" in row_b.inner_text(), f"milestone ★ not found in S_B row: {row_b.inner_text()[:200]}")
            assert_("★" not in row_a.inner_text(), f"milestone ★ should be absent in non-milestone S_A row: {row_a.inner_text()[:200]}")
            ok("AC-S67cb0e-1-4", "milestone ★ present for S_B, absent for S_A")

            # [AC-S67cb0e-1-5] Row activation by keyboard (Enter then Space).
            # -- Enter --
            row_a.focus()
            page.keyboard.press("Enter")
            page.wait_for_selector("[data-testid='sprint-detail-header']", timeout=8000)
            page.wait_for_function("() => location.search.includes('sprintId=S_A')", timeout=8000)
            assert_("view=detail" in page.url and "sprintId=S_A" in page.url,
                    f"Enter on row should navigate to S_A detail: {page.url}")
            # back to overview
            page.go_back()
            page.wait_for_selector("[data-testid='sprint-timeline-table']", timeout=8000)
            # Se173ef: filter resets to "未完了のみ" on remount — reveal done again.
            page.locator("[data-testid='sprint-timeline-filter-all']").click()
            page.wait_for_selector("[data-testid='sprint-timeline-S_A']", timeout=8000)
            # -- Space --
            row_a = page.locator("[data-testid='sprint-timeline-S_A']")
            row_a.focus()
            page.keyboard.press("Space")
            page.wait_for_selector("[data-testid='sprint-detail-header']", timeout=8000)
            page.wait_for_function("() => location.search.includes('sprintId=S_A')", timeout=8000)
            assert_("view=detail" in page.url and "sprintId=S_A" in page.url,
                    f"Space on row should navigate to S_A detail: {page.url}")
            ok("AC-S67cb0e-1-5", "row activation by keyboard (Enter + Space) navigates to detail")

            # [AC-S67cb0e-1-6] Sticky thead — computed style on a thead th is position:sticky.
            page.goto(overview_url, wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-timeline-table'] thead th", timeout=10000)
            thead_pos = page.eval_on_selector(
                "[data-testid='sprint-timeline-table'] thead th",
                "el => getComputedStyle(el).position",
            )
            assert_(thead_pos == "sticky", f"thead th position should be 'sticky', got {thead_pos!r}")
            ok("AC-S67cb0e-1-6", "thead th computed position == 'sticky'")

            # ================================================================
            # Story 2: pushState history + URL normalization
            # ================================================================
            # [AC-S67cb0e-2-1] User-initiated nav uses pushState (not replace):
            #   Overview → click row to Detail → Back returns to Overview.
            #   (If it were replace, Back would not return here.)
            page.goto(overview_url, wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-timeline-table']", timeout=10000)
            # Se173ef: the Overview timeline now defaults to "未完了のみ", which
            # hides done sprints (S_A). Reveal all so the done-sprint rows this
            # suite exercises are present. (Same ACs, just a new default filter.)
            page.locator("[data-testid='sprint-timeline-filter-all']").click()
            page.wait_for_selector("[data-testid='sprint-timeline-S_A']", timeout=8000)
            page.locator("[data-testid='sprint-timeline-S_A']").click()
            page.wait_for_selector("[data-testid='sprint-detail-header']", timeout=8000)
            page.wait_for_function("() => location.search.includes('sprintId=S_A')", timeout=8000)
            page.go_back()
            page.wait_for_selector("[data-testid='sprint-overview-timeline']", timeout=8000)
            back_url = page.url
            assert_("view=detail" not in back_url, f"Back should leave detail view: {back_url}")
            ok("AC-S67cb0e-2-1", "user nav uses pushState: row click→detail, Back→overview")

            # [AC-S67cb0e-2-2] Invalid ?view= is normalized to overview via replace
            #   (no extra history entry). Strategy: load a known prior page (a valid
            #   detail), then goto the garbage URL, wait for the rewrite to view=overview,
            #   then Back must land on the prior page (not the garbage URL).
            page.goto(detail_url("S_C"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-header']", timeout=8000)
            page.wait_for_function("() => location.search.includes('sprintId=S_C')", timeout=8000)
            page.goto(f"{BASE_URL}/{repo_id}/{branch_id}/sprint?view=garbage", wait_until="networkidle")
            # Overview renders despite the invalid view.
            page.wait_for_selector("[data-testid='sprint-overview-timeline']", timeout=10000)
            # URL gets rewritten to view=overview.
            page.wait_for_function("() => location.search.includes('view=overview')", timeout=8000)
            norm_url = page.url
            assert_("view=overview" in norm_url, f"invalid view should be rewritten to overview: {norm_url}")
            assert_("garbage" not in norm_url, f"normalized URL should not contain 'garbage': {norm_url}")
            # Because the rewrite is a replace, Back skips the garbage URL and
            # returns to the prior detail (S_C), proving no history entry was added.
            page.go_back()
            page.wait_for_selector("[data-testid='sprint-detail-header']", timeout=8000)
            page.wait_for_function("() => location.search.includes('sprintId=S_C')", timeout=8000)
            after_back = page.url
            assert_("garbage" not in after_back, f"Back must not land on garbage URL: {after_back}")
            assert_("sprintId=S_C" in after_back, f"Back should return to prior detail S_C: {after_back}")
            ok("AC-S67cb0e-2-2", "invalid ?view= normalized to overview via replace (no extra history)")

            # [AC-S67cb0e-2-3] Overview→Detail→Back→Overview, then Forward→Detail.
            page.goto(overview_url, wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-timeline-table']", timeout=10000)
            # Se173ef: the Overview timeline now defaults to "未完了のみ", which
            # hides done sprints (S_A). Reveal all so the done-sprint rows this
            # suite exercises are present. (Same ACs, just a new default filter.)
            page.locator("[data-testid='sprint-timeline-filter-all']").click()
            page.wait_for_selector("[data-testid='sprint-timeline-S_A']", timeout=8000)
            page.locator("[data-testid='sprint-timeline-S_A']").click()
            page.wait_for_selector("[data-testid='sprint-detail-header']", timeout=8000)
            page.wait_for_function("() => location.search.includes('sprintId=S_A')", timeout=8000)
            page.go_back()
            page.wait_for_selector("[data-testid='sprint-overview-timeline']", timeout=8000)
            assert_("view=detail" not in page.url, f"Back should return to overview: {page.url}")
            page.go_forward()
            page.wait_for_selector("[data-testid='sprint-detail-header']", timeout=8000)
            page.wait_for_function("() => location.search.includes('sprintId=S_A')", timeout=8000)
            assert_("view=detail" in page.url and "sprintId=S_A" in page.url,
                    f"Forward should return to S_A detail: {page.url}")
            ok("AC-S67cb0e-2-3", "Overview→Detail→Back→Overview→Forward→Detail")

            # [AC-S67cb0e-2-4] Detail(S_A)→Detail(S_B)→Back→S_A→Forward→S_B
            #   (pushState across detail-to-detail). Direct URL loads are used here
            #   as supporting steps to set up the two detail entries.
            page.goto(detail_url("S_A"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-header']", timeout=8000)
            page.wait_for_function("() => location.search.includes('sprintId=S_A')", timeout=8000)
            # Navigate S_A → S_B via the in-tab dropdown so it goes through pushState.
            page.locator("[data-testid='sprint-detail-sprint-select']").select_option(value="S_B")
            page.wait_for_function("() => location.search.includes('sprintId=S_B')", timeout=8000)
            page.go_back()
            page.wait_for_function("() => location.search.includes('sprintId=S_A')", timeout=8000)
            assert_("sprintId=S_A" in page.url, f"Back from S_B detail should return to S_A: {page.url}")
            page.go_forward()
            page.wait_for_function("() => location.search.includes('sprintId=S_B')", timeout=8000)
            assert_("sprintId=S_B" in page.url, f"Forward should return to S_B: {page.url}")
            ok("AC-S67cb0e-2-4", "Detail(S_A)→Detail(S_B)→Back→S_A→Forward→S_B (pushState)")

            # ================================================================
            # Story 3: Prev/Next + dropdown
            # ================================================================
            page.goto(detail_url("S_B"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-header']", timeout=8000)
            page.wait_for_selector("[data-testid='sprint-detail-prev']", timeout=8000)

            # [AC-S67cb0e-3-1] Prev/Next buttons + select all present on a detail view.
            prev_btn = page.locator("[data-testid='sprint-detail-prev']")
            next_btn = page.locator("[data-testid='sprint-detail-next']")
            select = page.locator("[data-testid='sprint-detail-sprint-select']")
            assert_(prev_btn.count() == 1, "sprint-detail-prev button not found")
            assert_(next_btn.count() == 1, "sprint-detail-next button not found")
            assert_(select.count() == 1, "sprint-detail-sprint-select not found")
            ok("AC-S67cb0e-3-1", "Prev/Next/select elements present on detail view")

            # [AC-S67cb0e-3-2] Prev disabled at first sprint (S_A) AND Next disabled at last (S_C).
            page.goto(detail_url("S_A"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-prev']", timeout=8000)
            assert_(page.locator("[data-testid='sprint-detail-prev']").is_disabled(),
                    "Prev should be disabled at first sprint S_A")
            page.goto(detail_url("S_C"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-next']", timeout=8000)
            assert_(page.locator("[data-testid='sprint-detail-next']").is_disabled(),
                    "Next should be disabled at last sprint S_C")
            ok("AC-S67cb0e-3-2", "Prev disabled at first (S_A); Next disabled at last (S_C)")

            # [AC-S67cb0e-3-3] Prev/Next click switches sprint and updates URL (pushState).
            page.goto(detail_url("S_C"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-prev']", timeout=8000)
            page.locator("[data-testid='sprint-detail-prev']").click()
            page.wait_for_function("() => location.search.includes('sprintId=S_B')", timeout=8000)
            assert_("sprintId=S_B" in page.url, f"Prev from S_C should switch to S_B: {page.url}")
            page.locator("[data-testid='sprint-detail-next']").click()
            page.wait_for_function("() => location.search.includes('sprintId=S_C')", timeout=8000)
            assert_("sprintId=S_C" in page.url, f"Next from S_B should switch to S_C: {page.url}")
            # pushState: Back from S_C returns to S_B.
            page.go_back()
            page.wait_for_function("() => location.search.includes('sprintId=S_B')", timeout=8000)
            assert_("sprintId=S_B" in page.url, f"Back after Next should return to S_B (pushState): {page.url}")
            ok("AC-S67cb0e-3-3", "Prev/Next switch sprint + update URL (pushState)")

            # [AC-S67cb0e-3-4] Dropdown lists ALL sprints (3 options) with current selected.
            page.goto(detail_url("S_B"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-sprint-select']", timeout=8000)
            sel = page.locator("[data-testid='sprint-detail-sprint-select']")
            n_opts = sel.locator("option").count()
            assert_(n_opts == 3, f"expected 3 options in select, got {n_opts}")
            sel_value = sel.input_value()
            assert_(sel_value == "S_B", f"dropdown value should equal current sprint S_B, got {sel_value!r}")
            ok("AC-S67cb0e-3-4", f"dropdown lists {n_opts} sprints; current (S_B) selected")

            # [AC-S67cb0e-3-5] Selecting a different sprint navigates; Back returns (pushState).
            sel.select_option(value="S_C")
            page.wait_for_function("() => location.search.includes('sprintId=S_C')", timeout=8000)
            assert_("sprintId=S_C" in page.url, f"selecting S_C should update URL: {page.url}")
            page.go_back()
            page.wait_for_function("() => location.search.includes('sprintId=S_B')", timeout=8000)
            assert_("sprintId=S_B" in page.url, f"Back after dropdown select should return to S_B: {page.url}")
            ok("AC-S67cb0e-3-5", "dropdown select navigates (S_B→S_C) + Back returns to S_B (pushState)")

            # ================================================================
            # Story 4: Markdown rendering
            # ================================================================
            page.goto(detail_url("S_B"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-description']", timeout=8000)
            desc_el = page.locator("[data-testid='sprint-detail-description']")
            assert_(desc_el.count() == 1, "sprint-detail-description not found")

            # [AC-S67cb0e-4-1] Description renders Markdown as HTML (heading present), not raw.
            heading = desc_el.locator("h2, h3")
            assert_(heading.count() >= 1, "no <h2>/<h3> heading found in rendered description (raw text?)")
            assert_("## Sprint goals" not in desc_el.inner_text(),
                    "description appears to be raw markdown (found literal '## Sprint goals')")
            ok("AC-S67cb0e-4-1", f"description rendered as HTML ({heading.count()} h2/h3 headings)")

            # [AC-S67cb0e-4-2] List rendered (<li> present).
            li = desc_el.locator("li")
            assert_(li.count() >= 1, "no <li> elements in rendered description")
            ok("AC-S67cb0e-4-2", f"list rendered ({li.count()} <li>)")

            # [AC-S67cb0e-4-3] Inline/fenced code (<code>), GFM table (<table>), emphasis (strong/em).
            code = desc_el.locator("code")
            tbl = desc_el.locator("table")
            strong = desc_el.locator("strong")
            em = desc_el.locator("em")
            assert_(code.count() >= 1, "no <code> elements in rendered description")
            assert_(tbl.count() >= 1, "no <table> (GFM table) in rendered description")
            assert_(strong.count() >= 1, "no <strong> (bold emphasis) in rendered description")
            assert_(em.count() >= 1, "no <em> (italic emphasis) in rendered description")
            ok("AC-S67cb0e-4-3",
               f"code({code.count()}) + table({tbl.count()}) + strong({strong.count()}) + em({em.count()})")

            # [AC-S67cb0e-4-4] External https link: target=_blank AND rel containing noopener.
            ext_link = desc_el.locator("a[target='_blank']")
            assert_(ext_link.count() >= 1, "no external link (target=_blank) in rendered description")
            rel = ext_link.first.get_attribute("rel") or ""
            assert_("noopener" in rel, f"external link rel should contain 'noopener', got {rel!r}")
            ok("AC-S67cb0e-4-4", f"external link target=_blank + rel={rel!r}")

            # [AC-S67cb0e-4-5] Shared component: Overview current-sprint description (S_B,
            #   in_progress, has markdown) renders a heading too — deterministic, no skip.
            page.goto(overview_url, wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-overview-timeline']", timeout=10000)
            curr_desc = page.locator("[data-testid='sprint-overview-current-description']")
            assert_(curr_desc.count() == 1,
                    "overview current-sprint description not found (current_sprint=S_B must render its markdown)")
            curr_heading = curr_desc.locator("h2, h3")
            assert_(curr_heading.count() >= 1,
                    "overview current-sprint description should render a heading via shared MarkdownBlock")
            ok("AC-S67cb0e-4-5", "Sprint description + Files viewer share MarkdownBlock (overview current renders heading)")

            ctx.close()
        finally:
            browser.close()


def test_default_sprint_resolution(repo_id: str, branch_id: str) -> None:
    """[HOTFIX-default-sprint] Opening Sprint Detail with no sprintId resolves
    a default: in-progress sprint → else next non-done → else last sprint.

    Fixture has S_A=done, S_B=in_progress, S_C=pending, so the default must
    resolve to the in-progress sprint S_B, via replace (so Back does not
    bounce on the bare ?view=detail URL).
    """
    try:
        from playwright.sync_api import sync_playwright  # type: ignore  # noqa: PLC0415
    except ImportError:
        fail("playwright not installed — required for S67cb0e E2E")

    with sync_playwright() as p:
        browser = p.chromium.launch()
        try:
            ctx = browser.new_context(viewport={"width": 1280, "height": 800})
            page = ctx.new_page()

            # Open Sprint Detail directly with NO sprintId.
            page.goto(f"{BASE_URL}/{repo_id}/{branch_id}/sprint?view=detail",
                      wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-header']", timeout=10000)
            page.wait_for_function("() => location.search.includes('sprintId=S_B')",
                                   timeout=8000)
            assert_("sprintId=S_B" in page.url,
                    f"default detail should resolve to in-progress S_B: {page.url}")
            ok("HOTFIX-default-sprint", "no sprintId → resolves to in-progress S_B")

            # Resolution used replace (not push): from overview → bare detail →
            # Back must land on overview, not bounce on a bare detail URL.
            page.goto(f"{BASE_URL}/{repo_id}/{branch_id}/sprint?view=overview",
                      wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-overview-timeline']", timeout=8000)
            page.goto(f"{BASE_URL}/{repo_id}/{branch_id}/sprint?view=detail",
                      wait_until="networkidle")
            page.wait_for_function("() => location.search.includes('sprintId=S_B')",
                                   timeout=8000)
            page.go_back()
            page.wait_for_selector("[data-testid='sprint-overview-timeline']", timeout=8000)
            assert_("sprintId=" not in page.url,
                    f"Back from resolved detail should reach overview: {page.url}")
            ok("HOTFIX-default-sprint", "resolution used replace (Back → overview, no bounce)")

            ctx.close()
        finally:
            browser.close()


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main() -> int:
    print(f"S67cb0e sprint tab polish E2E against {BASE_URL}")
    code, _, _ = http_json("GET", "/api/health")
    if code != 200:
        fail(f"dev instance not healthy: {code}")

    with palmux2_test_fixture("s67cb0e") as fx:
        repo_path = fx.path
        repo_id = fx.repo_id
        print(f"  fixture: {fx.ghq_path} (id={repo_id})")

        # Seed the fixture ROADMAP
        write_roadmap(repo_path, make_roadmap_json())

        # Open main branch
        code, _, _ = http_json(
            "POST",
            f"/api/repos/{urllib.parse.quote(repo_id)}/branches/open",
            body={"branchName": "main"},
        )
        assert_(code in (200, 201), f"open branch main: {code}")

        # Get branch id
        from s028_sprint_json import fetch_repos, find_branch  # type: ignore # noqa: PLC0415
        repos = fetch_repos()
        b = find_branch(repos, repo_id, "main")
        assert_(b is not None, "main branch missing after open")
        branch_id = b["id"]  # type: ignore[index]
        print(f"  branch_id: {branch_id}")

        # Wait for sprint tab to appear
        found = wait_for(
            lambda: any(t["type"] == "sprint" for t in get_branch_tabs(repo_id, branch_id)),
            timeout_s=8.0,
        )
        assert_(found, "sprint tab did not appear after writing ROADMAP.json")
        print("  sprint tab visible")

        # --- API-level supporting checks (back the rendered UI) ---
        test_overview_timeline_fields(repo_id, branch_id)
        test_overview_execution_order(repo_id, branch_id)
        test_detail_description_source(repo_id, branch_id)

        # --- Browser-driven authoritative tests (required) ---
        test_playwright_stories(repo_id, branch_id)
        test_default_sprint_resolution(repo_id, branch_id)

    print("S67cb0e E2E: PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
