#!/usr/bin/env python3
"""Sprint Se173ef — Sprint tab follows the current skill artifact set.

Story 3 (GUI, trust-source-first IA redesign) authoritative E2E, plus API
support checks for Stories 1 & 2 (backend artifact + ROADMAP-field exposure).

The fixture seeds a ROADMAP with four sprints and the FULL modern artifact
set so the tab renders real data deterministically:

  S_OLD    done, NO modern artifacts        → backward-compat empty-notes
  S_MILE   done + milestone + phase,         → trust=PASS, guards, AC,
           verify-run/verification-report/     comprehension (Markdown),
           done-judgment/compromises/          prototype/gui-spec state diagram,
           comprehension/prototype-review/     deploy-test-smoke additional log
           deploy-test-smoke
  S_CUR    in_progress, one needs_user_review → Review queue, overlooked,
           story + verification-report(warn)   reopen history
           + reopen.json
  S_COARSE pending, detail_level=coarse       → coarse placeholder badge

Every ROADMAP acceptance criterion for Stories 1/2/3 is tagged [AC-Se173ef-*].
Playwright (headless chromium) is REQUIRED — no silent skip.

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
    or "8215"
)
BASE_URL = f"http://localhost:{PORT}"
TIMEOUT_S = 20.0

sys.path.insert(0, str(Path(__file__).resolve().parent))
from _fixture import palmux2_test_fixture  # noqa: E402


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(tag: str, msg: str = "") -> None:
    print(f"  [{tag}] {msg or 'OK'}")


def assert_(cond: bool, msg: str) -> None:
    if not cond:
        fail(msg)


def http_json(method: str, path: str) -> tuple[int, dict | list | str]:
    req = urllib.request.Request(f"{BASE_URL}{path}", method=method, headers={"Accept": "application/json"})
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            data = resp.read()
            try:
                return resp.status, json.loads(data.decode() or "{}")
            except json.JSONDecodeError:
                return resp.status, data.decode(errors="replace")
    except urllib.error.HTTPError as e:
        data = e.read()
        try:
            return e.code, json.loads(data.decode() or "{}")
        except json.JSONDecodeError:
            return e.code, data.decode(errors="replace")


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

COMPREHENSION_MD = """\
# Comprehension Report — S_MILE

## What changed

- Declarative shared folders became a single incus profile.
- GUI/config can add shared folders with hot propagation.

## Why this way

- Reuse incus-native profile (priority_rule 6).

## What to verify

- deploy-test real smoke passed 11/11.
"""


def make_roadmap() -> dict:
    return {
        "project": "Se173ef Test Project",
        "description": "E2E fixture for the Sprint-tab artifact rework",
        "progress": {"current_sprint": "S_CUR", "total": 4, "done": 2, "in_progress": 1, "remaining": 1, "percentage": 50.0},
        "execution_order": ["S_OLD", "S_MILE", "S_CUR", "S_COARSE"],
        "sprints": {
            "S_OLD": {
                "title": "Old sprint (no modern artifacts)",
                "status": "done",
                "description": "Legacy sprint.",
                "milestone": False,
                "phase": "Phase 5",
                "stories": {
                    "S_OLD-1": {"title": "legacy story", "status": "done", "acceptance_criteria": [], "tasks": {}}
                },
            },
            "S_MILE": {
                "title": "Milestone sprint — declarative shares",
                "status": "done",
                "description": "## Goal\n\nProfile-as-mold shared folders.",
                "milestone": True,
                "phase": "Phase 6",
                "detail_level": "detailed",
                "review_reason": "incus profile の live 伝播は実 incus でしか検証できない",
                "stories": {
                    "S_MILE-1": {
                        "title": "declare shared profile",
                        "status": "done",
                        "user_story": "As a user I want declarative shares.",
                        "acceptance_criteria": [
                            {"id": "AC-S_MILE-1-1", "description": "profile is idempotent", "status": "pass"},
                            {"id": "AC-S_MILE-1-2", "description": "reconcile self-heals", "status": "pass"},
                        ],
                        "tasks": {"S_MILE-1-1": {"title": "profile", "status": "done"}},
                    }
                },
            },
            "S_CUR": {
                "title": "Current sprint — needs review",
                "status": "in_progress",
                "description": "In flight.",
                "milestone": False,
                "phase": "Phase 6",
                "detail_level": "detailed",
                "stories": {
                    "S_CUR-1": {
                        "title": "coupling not wired",
                        "status": "needs_user_review",
                        "review_reason": "Guard 5 (call-path) fail: API↔Workflow trigger missing",
                        "user_review_required": True,
                        "acceptance_criteria": [
                            {"id": "AC-S_CUR-1-1", "description": "token persisted on login", "status": "pass"}
                        ],
                        "tasks": {"S_CUR-1-1": {"title": "wire", "status": "pending"}},
                    }
                },
            },
            "S_COARSE": {
                "title": "Future coarse placeholder",
                "status": "pending",
                "description": "Goal only.",
                "milestone": False,
                "detail_level": "coarse",
                "stories": {},
            },
        },
        "dependencies": {
            "S_MILE": {"depends_on": ["S_OLD"], "reason": "extends the legacy share device layout"},
            "S_CUR": {"depends_on": ["S_MILE"], "reason": "reuses the shared_dirs mold"},
        },
        "backlog": [
            {
                "title": "declarative shares follow-up widget",
                "description": "polish the declarative shares GUI",
                "reason": "deferred polish",
                "added_in": "S_MILE",
                "priority": "low",
                "status": "pending",
            },
            {"title": "Unrelated Monaco diff pane", "description": "commit-diff endpoint", "reason": "size M", "status": "pending"},
        ],
    }


def seed_artifacts(repo_path: Path) -> None:
    logs = repo_path / "docs" / "sprint-logs"
    # S_MILE — full modern artifact set (trust=PASS).
    mile = logs / "S_MILE"
    mile.mkdir(parents=True, exist_ok=True)
    (mile / "verify-run-unit.log").write_text("ok\n__VERIFY_EXIT_CODE__:unit:0\n")
    (mile / "verify-run-e2e.log").write_text("ok\n__VERIFY_EXIT_CODE__:e2e:0\n")
    (mile / "verify-run.json").write_text(json.dumps({
        "$machine_authored": True,
        "sprint": "S_MILE",
        "command_source": "declared (.claude/verify.json)",
        "runs": [
            {"name": "unit", "command": "go test ./...", "exit_code": 0, "log": "docs/sprint-logs/S_MILE/verify-run-unit.log", "machine_status": "pass", "junit": {"total": 5, "passed": 5, "failed": 0, "errored": 0, "skipped": 0}},
            {"name": "e2e", "command": "playwright", "exit_code": 0, "log": "docs/sprint-logs/S_MILE/verify-run-e2e.log", "machine_status": "pass", "junit": None},
        ],
        "overall_machine_status": "pass",
    }))
    (mile / "verification-report.json").write_text(json.dumps({
        "sprint": "S_MILE",
        "overall": "pass",
        "verifier_model": "claude (read-only)",
        "stories": {
            "S_MILE-1": {
                "verdict": "ok",
                "ac_findings": [
                    {"ac": "AC-S_MILE-1-1", "status": "pass", "evidence": "shared_profile.go:206"},
                    {"ac": "AC-S_MILE-1-2", "status": "pass", "evidence": "sync_worktree.go:210"},
                ],
                "forbidden_category_findings": [],
            }
        },
        "findings": [],
        "summary": {"ac_failures": 0, "ac_warnings": 0, "forbidden_warnings": 0, "forbidden_failures": 0, "adr_conflicts": 0, "overlooked_count": 0},
    }))
    (mile / "done-judgment.json").write_text(json.dumps({
        "sprint": "S_MILE",
        "precondition": {"detail_level": "detailed", "stories_nonempty": True, "ok": True},
        "stories": {
            "S_MILE-1": {
                "guard_1_not_user_review_required": "pass — ok",
                "guard_2_nil_injection_mock": "pass — real incus",
                "guard_3_mock_as_real": "pass — real-mode smoke",
                "guard_4_pr9_exception": "pass — no exception claimed",
                "guard_5_call_path": "pass — deploy-apply→SetSharedDirs grep-present",
                "guard_6_deferral_comments": "pass — no deferrals",
                "overall": "ok",
            }
        },
    }))
    (mile / "compromises.json").write_text(json.dumps({
        "stopped_at": "milestone_S_MILE",
        "compromises": [
            {"type": "test_assertion_weakened", "severity": "high", "story": "S_MILE-1", "file": "a.spec.ts", "rationale": "OAuth mock 回避", "recommended_action": "厳密化"}
        ],
        "blockers_encountered": [
            {"type": "agent_terminated", "severity": "medium", "detail": "sprint agent finalization 前に終了", "resolution": "autopilot 代行"}
        ],
        "scope_changes": [],
    }))
    (mile / "comprehension-report.md").write_text(COMPREHENSION_MD)
    (mile / "prototype-review.json").write_text(json.dumps({
        "sprint_range": ["S_MILE"],
        "screens": [{"file": "prototype/shares.html", "story": "S_MILE-1", "feedback_rounds": 1, "approved": True}],
        "design_decisions": ["shared folder GUI をデプロイタブに追加"],
        "approved_by_user": True,
        "approved_at": "2026-07-05",
    }))
    (mile / "gui-spec-S_MILE-1.json").write_text(json.dumps({
        "sprint": "S_MILE",
        "story": "S_MILE-1",
        "state_diagram": "stateDiagram-v2\n    [*] --> Empty\n    Empty --> Populated: load\n    Populated --> Saving: apply\n    Saving --> Saved",
        "endpoint_contracts": [{"path": "/api/deploy", "method": "GET", "registered": True}],
        "test_files": {"e2e": "tests/e2e/shares.py"},
    }))
    (mile / "deploy-test-smoke.json").write_text(json.dumps({
        "sprint": "S_MILE", "kind": "real-incus production smoke", "overall": "PASS",
        "checks": [{"name": "profile devices", "status": "pass"}],
    }))
    (mile / "decisions.json").write_text(json.dumps({
        "sprint": "S_MILE",
        "decisions": [{"timestamp": "2026-07-05T00:00:00Z", "category": "implementation", "title": "profile-as-mold", "detail": "incus native profile を活用"}],
    }))

    # S_CUR — needs_user_review + overlooked + reopen.
    cur = logs / "S_CUR"
    cur.mkdir(parents=True, exist_ok=True)
    (cur / "verification-report.json").write_text(json.dumps({
        "sprint": "S_CUR",
        "overall": "warn",
        "stories": {
            "S_CUR-1": {
                "verdict": "needs_user_review",
                "ac_findings": [
                    {"ac": "AC-S_CUR-1-1", "status": "fail", "evidence": "login handler has no localStorage.setItem", "overlooked_by_autopilot": True, "recommended_action": "add persistence + a test"}
                ],
                "forbidden_category_findings": [],
            }
        },
        "findings": [{"category": "acceptance_criteria", "story": "S_CUR-1", "ac": "AC-S_CUR-1-1", "verdict": "fail", "detail": "coupling absent in code", "overlooked_by_autopilot": True}],
        "summary": {"ac_failures": 1, "ac_warnings": 0, "forbidden_warnings": 0, "forbidden_failures": 0, "adr_conflicts": 0, "overlooked_count": 1},
    }))
    (cur / "done-judgment.json").write_text(json.dumps({
        "sprint": "S_CUR",
        "stories": {
            "S_CUR-1": {
                "guard_1_not_user_review_required": "fail — user_review_required set",
                "guard_5_call_path": "fail — API↔Workflow trigger absent",
                "overall": "needs_user_review",
            }
        },
    }))
    (cur / "reopen.json").write_text(json.dumps({
        "sprint_id": "S_CUR",
        "reopened_at": "2026-07-06T10:00:00Z",
        "triggered_by": "milestone_review",
        "milestone": "M-Phase6",
        "reason": "AC 違反: トークンが localStorage に保存されていない",
        "affected_acceptance_criteria": ["AC-S_CUR-1-1"],
    }))


def open_branch_get_id(repo_id: str) -> str:
    code, _ = http_json("POST", f"/api/repos/{urllib.parse.quote(repo_id)}/branches/open")
    # branchName body omitted → open default; retry with explicit main.
    if code not in (200, 201):
        req = urllib.request.Request(
            f"{BASE_URL}/api/repos/{urllib.parse.quote(repo_id)}/branches/open",
            method="POST", data=json.dumps({"branchName": "main"}).encode(),
            headers={"Content-Type": "application/json"},
        )
        urllib.request.urlopen(req, timeout=TIMEOUT_S).read()
    from s028_sprint_json import fetch_repos, find_branch  # type: ignore  # noqa: PLC0415
    repos = fetch_repos()
    b = find_branch(repos, repo_id, "main")
    assert_(b is not None, "main branch missing after open")
    return b["id"]  # type: ignore[index]


def sp_url(repo_id: str, branch_id: str, view: str, extra: str = "") -> str:
    return f"{BASE_URL}/{repo_id}/{branch_id}/sprint?view={view}{extra}"


def api(repo_id: str, branch_id: str, path: str):
    return http_json("GET", f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/sprint{path}")


# ---------------------------------------------------------------------------
# Story 1 & 2 — backend API support checks
# ---------------------------------------------------------------------------

def test_api_story1(repo_id: str, branch_id: str) -> None:
    code, body = api(repo_id, branch_id, "/sprints/S_MILE")
    assert_(code == 200, f"detail S_MILE: {code}")
    assert_(body["verifyRun"]["overallMachineStatus"] == "pass", f"verifyRun: {body.get('verifyRun')}")
    names = {r["name"] for r in body["verifyRun"]["runs"]}
    assert_({"unit", "e2e"} <= names, f"runs: {names}")
    ok("AC-Se173ef-1-1", "verify-run.json parsed (overall=pass, runs unit/e2e)")

    assert_(body["verification"]["overall"] == "pass", "verification overall")
    assert_(body["verification"]["summary"]["acFailures"] == 0, "verification summary")
    assert_(len(body["doneJudgment"]["stories"]) == 1, "done-judgment stories")
    assert_(body["doneJudgment"]["stories"][0]["overall"] == "ok", "done-judgment overall")
    assert_(len(body["compromises"]["compromises"]) == 1, "compromises")
    assert_(body["compromises"]["compromises"][0]["severity"] == "high", "compromise severity")
    ok("AC-Se173ef-1-1", "verification-report / done-judgment / compromises parsed")

    assert_("What changed" in "".join(body["comprehension"]["headings"]), f"comprehension headings: {body['comprehension']['headings']}")
    assert_("What changed" in body["comprehension"]["markdown"], "comprehension markdown")
    ok("AC-Se173ef-1-2", "comprehension-report.md returned with headings")

    # AC-1-3: AC findings derived from verification-report + ROADMAP (not matrix).
    acids = {r["ac"] for r in body["acFindings"]}
    assert_({"AC-S_MILE-1-1", "AC-S_MILE-1-2"} <= acids, f"acFindings: {acids}")
    assert_(all(r["status"] == "pass" for r in body["acFindings"]), "ac findings status")
    ok("AC-Se173ef-1-3", "AC matrix derived from verification-report + ROADMAP")

    # AC-1-5: generic smoke log collection.
    logs = {l["file"]: l for l in body["additionalLogs"]}
    assert_("deploy-test-smoke.json" in logs, f"additionalLogs: {list(logs)}")
    assert_(logs["deploy-test-smoke.json"]["overall"] == "pass", "smoke log overall")
    ok("AC-Se173ef-1-5", "generic additional smoke log collected (deploy-test-smoke.json=pass)")

    # AC-1-4: old sprint (no modern artifacts) does not error.
    code_old, body_old = api(repo_id, branch_id, "/sprints/S_OLD")
    assert_(code_old == 200, f"detail S_OLD: {code_old}")
    assert_(body_old.get("verifyRun") in (None, {}), "S_OLD should have no verifyRun")
    assert_(body_old.get("doneJudgment") in (None, {}), "S_OLD should have no doneJudgment")
    ok("AC-Se173ef-1-4", "old sprint (no modern artifacts) returns 200, sections omitted")


def test_api_story2(repo_id: str, branch_id: str) -> None:
    code, body = api(repo_id, branch_id, "/sprints/S_MILE")
    assert_(code == 200, f"detail: {code}")
    sp = body["sprint"]
    assert_(sp["detailLevel"] == "detailed" and sp["phase"] == "Phase 6" and sp["reviewReason"], f"sprint meta: {sp}")
    assert_(sp["milestone"] is True, "milestone flag")
    ok("AC-Se173ef-2-1", "sprint detailLevel/phase/reviewReason/milestone exposed")

    _, cur = api(repo_id, branch_id, "/sprints/S_CUR")
    story = cur["sprint"]["stories"][0]
    assert_(story["statusKind"] == "needs-user-review", f"statusKind: {story['statusKind']}")
    assert_(story["userReviewRequired"] is True, "userReviewRequired")
    assert_(story.get("reviewReason"), "story reviewReason")
    ok("AC-Se173ef-2-2", "story needs_user_review distinguished from done + new state exposed")

    _, ov = api(repo_id, branch_id, "/overview")
    by = {t["id"]: t for t in ov["timeline"]}
    assert_(by["S_COARSE"]["coarse"] is True, f"coarse flag: {by['S_COARSE']}")
    assert_(by["S_MILE"]["phase"] == "Phase 6", "timeline phase")
    ok("AC-Se173ef-2-1b", "timeline carries detailLevel/phase; coarse distinguishable")

    _, dep = api(repo_id, branch_id, "/dependencies")
    reasons = {d.get("from"): d.get("reason") for d in dep["dependencies"]}
    assert_(reasons.get("S_MILE") == "extends the legacy share device layout", f"dep reasons: {reasons}")
    ok("AC-Se173ef-2-3", "dependency reason included in graph response")

    _, bl = api(repo_id, branch_id, "/backlog")
    assert_(bl["total"] == 2, f"backlog total: {bl['total']}")
    items = {i["title"]: i for i in bl["items"]}
    followup = items.get("declarative shares follow-up widget")
    assert_(followup is not None and followup.get("priority") == "low" and followup.get("addedIn") == "S_MILE", f"backlog fields: {followup}")
    ok("AC-Se173ef-2-4", "backlog exposes full fields (priority/addedIn/reason/status)")


# ---------------------------------------------------------------------------
# Story 3 — browser (Playwright) authoritative
# ---------------------------------------------------------------------------

def test_playwright(repo_id: str, branch_id: str) -> None:
    try:
        from playwright.sync_api import sync_playwright  # type: ignore  # noqa: PLC0415
    except ImportError:
        fail("playwright not installed — required for Se173ef E2E")

    with sync_playwright() as p:
        browser = p.chromium.launch()
        try:
            ctx = browser.new_context(viewport={"width": 1280, "height": 900})
            page = ctx.new_page()

            # --- IA: 5 subtabs present, Refine absent ---
            page.goto(sp_url(repo_id, branch_id, "overview"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-view']", timeout=10000)
            for v in ["overview", "detail", "review", "milestones", "decisions"]:
                assert_(page.locator(f"[data-testid='sprint-subtab-{v}']").count() == 1, f"subtab {v} missing")
            assert_(page.locator("[data-testid='sprint-subtab-refine']").count() == 0, "Refine tab should be removed")
            assert_(page.locator("[data-testid='sprint-subtab-dependencies']").count() == 0, "Dependencies tab folded into Overview")
            ok("AC-Se173ef-3-7", "Option A IA: 5 tabs (Overview/Detail/Review/Milestones/Decisions); Refine+Dependencies removed")

            # --- Overview: rollup + folded backlog + timeline default filter ---
            page.wait_for_selector("[data-testid='sprint-overview-rollup']", timeout=8000)
            nur = page.locator("[data-testid='sprint-rollup-needsreview']").inner_text()
            assert_("1" in nur, f"rollup needs_user_review should be 1: {nur!r}")
            # default timeline filter = incomplete only → done sprints hidden.
            assert_(page.locator("[data-testid='sprint-timeline-S_OLD']").count() == 0, "done S_OLD should be hidden by default (incomplete-only)")
            assert_(page.locator("[data-testid='sprint-timeline-S_CUR']").count() == 1, "in_progress S_CUR should be visible by default")
            page.locator("[data-testid='sprint-timeline-filter-all']").click()
            page.wait_for_selector("[data-testid='sprint-timeline-S_OLD']", timeout=6000)
            assert_(page.locator("[data-testid='sprint-timeline-coarse-S_COARSE']").count() == 1, "coarse badge missing on S_COARSE")
            ok("AC-Se173ef-3-7b", "timeline defaults to incomplete-only; 'all' reveals done; coarse badge shown")

            # folded backlog default = unpromoted; promoted item hidden until filter.
            page.wait_for_selector("[data-testid='sprint-backlog-panel']", timeout=8000)
            n_unprom = page.locator("[data-testid='sprint-backlog-item']").count()
            assert_(n_unprom >= 1, "backlog should list unpromoted items by default")
            # expand first backlog item → drill-down content.
            first = page.locator("[data-testid='sprint-backlog-item']").first
            first.locator("button").first.click()
            assert_(wait_for(lambda: "reason" in first.inner_text().lower() or "size M" in first.inner_text() or "deferred" in first.inner_text(), 4.0),
                    f"backlog item did not expand with detail: {first.inner_text()[:200]}")
            ok("AC-Se173ef-3-8", "backlog folded in Overview, default unpromoted, item expands with drill-down")

            # --- Detail (S_MILE): trust-source-first ordering ---
            page.goto(sp_url(repo_id, branch_id, "detail", "&sprintId=S_MILE"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-trust']", timeout=10000)
            # trust panel appears before the guards panel (top-of-page).
            trust_y = page.eval_on_selector("[data-testid='sprint-detail-trust']", "el => el.getBoundingClientRect().top")
            guards_y = page.eval_on_selector("[data-testid='sprint-detail-guards']", "el => el.getBoundingClientRect().top")
            assert_(trust_y < guards_y, "trust panel must be above the guards panel (trust-source-first)")
            mv = page.locator("[data-testid='sprint-detail-machine-verdict']").inner_text()
            assert_("PASS" in mv.upper(), f"machine verdict should show PASS: {mv!r}")
            vv = page.locator("[data-testid='sprint-detail-verifier-verdict']").inner_text()
            assert_("PASS" in vv.upper(), f"verifier verdict should show PASS: {vv!r}")
            assert_(page.locator("[data-testid='sprint-detail-run-unit']").count() == 1, "unit run chip missing")
            ok("AC-Se173ef-3-1", "verification view: machine verdict + verifier verdict at the top, run chips present")

            # 6-guard grid.
            assert_(page.locator("[data-testid='sprint-detail-guard-row-S_MILE-1']").count() == 1, "guard row missing")
            sv = page.locator("[data-testid='sprint-detail-story-verdict-S_MILE-1']").inner_text()
            assert_("ok" in sv, f"story verdict should be ok: {sv!r}")
            ok("AC-Se173ef-3-2", "done-judgment guard grid rendered with story verdict")

            # milestone artifacts: compromises severity + comprehension Markdown.
            assert_(page.locator("[data-testid='sprint-detail-compromises']").count() == 1, "compromises panel missing")
            comp = page.locator("[data-testid='sprint-detail-comprehension']")
            assert_(comp.locator("h2, h3").count() >= 1, "comprehension should render Markdown headings")
            assert_("## What changed" not in comp.inner_text(), "comprehension appears raw (not rendered)")
            ok("AC-Se173ef-3-3", "milestone view: compromises severity-graded + comprehension rendered as Markdown")

            # prototype + gui-spec state diagram (mermaid → svg or text fallback).
            assert_(page.locator("[data-testid='sprint-detail-prototype']").count() == 1, "prototype panel missing")
            assert_(wait_for(lambda: page.locator("[data-testid='sprint-detail-statediagram']").count() == 1, 6.0), "state diagram not rendered")
            ok("AC-Se173ef-3-4", "prototype-review + gui-spec state_diagram rendered")

            # ROADMAP meta badges.
            assert_(page.locator("[data-testid='sprint-detail-badge-milestone']").count() == 1, "milestone badge missing")
            assert_(page.locator("[data-testid='sprint-detail-badge-phase']").count() == 1, "phase badge missing")
            assert_(page.locator("[data-testid='sprint-detail-badge-detaillevel']").count() == 1, "detailLevel badge missing")
            assert_(page.locator("[data-testid='sprint-detail-badge-reviewreason']").count() == 1, "reviewReason badge missing")
            # additional log surfaced.
            assert_(page.locator("[data-testid='sprint-detail-additional-logs']").count() == 1, "additional logs panel missing")
            ok("AC-Se173ef-3-5", "ROADMAP meta badges (milestone/phase/detailLevel/reviewReason) + additional logs shown")

            # --- Detail (S_OLD): backward-compat empty-notes, no crash ---
            page.goto(sp_url(repo_id, branch_id, "detail", "&sprintId=S_OLD"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-trust']", timeout=10000)
            assert_("未生成" in page.locator("[data-testid='sprint-detail-machine-verdict']").inner_text(), "old sprint should show '未生成' machine verdict note")
            assert_(page.locator("[data-testid='sprint-detail-guards']").count() == 1, "guards panel should still render (empty-note) for old sprint")
            ok("AC-Se173ef-3-6", "backward compat: old sprint renders empty-notes, no crash (Empty state)")

            # --- Detail (S_CUR): overlooked highlight + needs_user_review badge path ---
            page.goto(sp_url(repo_id, branch_id, "detail", "&sprintId=S_CUR"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-trust']", timeout=10000)
            assert_(page.locator("[data-testid='sprint-detail-overlooked']").count() >= 1, "overlooked_by_autopilot AC not highlighted")
            assert_(page.locator("[data-testid='sprint-detail-badge-reopened']").count() == 1, "reopened badge missing on re-opened sprint")
            sv_cur = page.locator("[data-testid='sprint-detail-story-verdict-S_CUR-1']").inner_text()
            assert_("needs_user_review" in sv_cur, f"S_CUR-1 verdict should be needs_user_review: {sv_cur!r}")
            ok("AC-Se173ef-3-1b", "overlooked AC highlighted; needs_user_review + reopened surfaced")

            # --- Review tab (cross-sprint queue) ---
            page.goto(sp_url(repo_id, branch_id, "review"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-review']", timeout=10000)
            assert_(page.locator("[data-testid='sprint-review-story-S_CUR-1']").count() == 1, "needs_user_review story missing from Review queue")
            assert_("S_CUR" in page.locator("[data-testid='sprint-review-reopen']").inner_text(), "reopen history missing in Review")
            assert_("S_MILE" in page.locator("[data-testid='sprint-review-compromises']").inner_text(), "high compromise missing in Review")
            assert_("S_CUR" in page.locator("[data-testid='sprint-review-overlooked']").inner_text(), "overlooked missing in Review")
            ok("AC-Se173ef-3-7c", "Review tab aggregates needs_user_review + high compromise + overlooked + reopen across sprints")

            # --- Milestones tab ---
            page.goto(sp_url(repo_id, branch_id, "milestones"), wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-milestones']", timeout=10000)
            assert_(page.locator("[data-testid='sprint-milestone-entry-S_MILE']").count() == 1, "S_MILE milestone entry missing")
            mcomp = page.locator("[data-testid='sprint-milestone-comprehension-S_MILE']")
            assert_(mcomp.locator("h2, h3").count() >= 1, "milestone comprehension should render Markdown")
            ok("AC-Se173ef-3-3b", "Milestones tab renders comprehension (Markdown) + compromises per milestone")

            ctx.close()
        finally:
            browser.close()


def main() -> int:
    print(f"Se173ef sprint-tab E2E against {BASE_URL}")
    code, _ = http_json("GET", "/api/health")
    if code != 200:
        fail(f"dev instance not healthy: {code}")

    with palmux2_test_fixture("se173ef") as fx:
        repo_path = fx.path
        repo_id = fx.repo_id
        (repo_path / "docs").mkdir(exist_ok=True)
        (repo_path / "docs" / "ROADMAP.json").write_text(json.dumps(make_roadmap(), ensure_ascii=False, indent=2))
        seed_artifacts(repo_path)

        branch_id = open_branch_get_id(repo_id)
        print(f"  repo_id={repo_id} branch_id={branch_id}")

        def tabs():
            c, b = http_json("GET", f"/api/repos/{urllib.parse.quote(repo_id)}/branches/{urllib.parse.quote(branch_id)}/tabs")
            return c == 200 and isinstance(b, dict) and any(t["type"] == "sprint" for t in b.get("tabs", []))

        assert_(wait_for(tabs, 8.0), "sprint tab did not appear after writing ROADMAP.json")

        test_api_story1(repo_id, branch_id)
        test_api_story2(repo_id, branch_id)
        test_playwright(repo_id, branch_id)

    print("Se173ef E2E: PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
