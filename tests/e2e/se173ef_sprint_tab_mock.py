#!/usr/bin/env python3
"""Sprint Se173ef — Sprint tab MOCK tests (Story 3 error/edge states).

Companion to se173ef_sprint_tab.py (the real-backend E2E). These use
Playwright page.route to force the FE-only states that are hard to trigger
deterministically against a live backend:

  - Loading      (delayed detail response → Loading text)
  - Error        (500 detail response → ErrorBanner)
  - ParseError   (detail payload carrying parseErrors[] → ParseErrorsBanner
                  renders AND the rest of the page still renders — a single
                  corrupt artifact does not take the screen down)
  - PartialArtifacts (detail payload with only some artifacts → present
                  sections render, absent ones show empty-notes)

Tagged [AC-Se173ef-3-6]. The tab shell + auth come from the REAL server (so
the SPA boots); only the sprint detail endpoint is intercepted.

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

PORT = os.environ.get("PALMUX2_DEV_PORT_OVERRIDE") or os.environ.get("PALMUX2_DEV_PORT") or "8215"
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


def http_json(method: str, path: str, body=None):
    raw = json.dumps(body).encode() if body is not None else None
    h = {"Accept": "application/json"}
    if body is not None:
        h["Content-Type"] = "application/json"
    req = urllib.request.Request(f"{BASE_URL}{path}", method=method, data=raw, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=TIMEOUT_S) as resp:
            return resp.status, json.loads(resp.read().decode() or "{}")
    except urllib.error.HTTPError as e:
        return e.code, {}


def wait_for(predicate, timeout_s: float = 8.0, sleep_s: float = 0.2) -> bool:
    deadline = time.time() + timeout_s
    while time.time() < deadline:
        try:
            if predicate():
                return True
        except Exception:
            pass
        time.sleep(sleep_s)
    return False


MINIMAL_ROADMAP = {
    "project": "Se173ef Mock",
    "progress": {"current_sprint": "S_M", "total": 1, "done": 0, "in_progress": 1, "remaining": 0, "percentage": 0},
    "execution_order": ["S_M"],
    "sprints": {"S_M": {"title": "Mock sprint", "status": "in_progress", "milestone": True, "phase": "Phase 6", "detail_level": "detailed",
                        "stories": {"S_M-1": {"title": "s", "status": "pending", "acceptance_criteria": [{"id": "AC-S_M-1-1", "description": "d", "status": "pending"}], "tasks": {}}}}},
    "dependencies": {},
    "backlog": [],
}


def detail_payload(**overrides) -> dict:
    base = {
        "sprint": {"id": "S_M", "title": "Mock sprint", "status": "in_progress", "statusKind": "in-progress",
                   "milestone": True, "phase": "Phase 6", "detailLevel": "detailed", "stories": [], "lineRange": [0, 0]},
        "reopens": [], "guiSpecs": [], "scenarios": [], "acFindings": [], "additionalLogs": [],
        "decisions": [], "acceptanceMatrix": [], "e2eResults": {"sprintId": "S_M", "mock": {"total": 0, "passed": 0, "failed": 0}, "e2e": {"total": 0, "passed": 0, "failed": 0}, "acceptance": {"total": 0, "passed": 0, "failed": 0}},
    }
    base.update(overrides)
    return base


def run(repo_id: str, branch_id: str) -> None:
    try:
        from playwright.sync_api import sync_playwright  # type: ignore  # noqa: PLC0415
    except ImportError:
        fail("playwright not installed — required for Se173ef mock tests")

    detail_re = "**/sprint/sprints/S_M"
    url = f"{BASE_URL}/{repo_id}/{branch_id}/sprint?view=detail&sprintId=S_M"

    with sync_playwright() as p:
        browser = p.chromium.launch()
        try:
            ctx = browser.new_context(viewport={"width": 1200, "height": 800})

            # --- Loading ---
            # The detail request is held pending inside the route handler; while
            # it is pending the FE must show the Loading state. We inspect the DOM
            # from within the handler (each locator call is a fast browser
            # round-trip that yields, so React commits the Loading render), then
            # fulfill. A python sleep here would block the sync dispatcher thread,
            # so we spin on the DOM instead.
            page = ctx.new_page()
            loading_seen = {"v": False}

            def slow(route):
                for _ in range(40):
                    try:
                        el = page.locator("[data-testid='sprint-view']")
                        if el.count() and "Loading" in el.inner_text():
                            loading_seen["v"] = True
                            break
                    except Exception:
                        pass
                route.fulfill(status=200, content_type="application/json", body=json.dumps(detail_payload()))

            page.route(detail_re, slow)
            page.goto(url, wait_until="networkidle")
            assert_(loading_seen["v"], "Loading state not shown while detail request was pending")
            ok("AC-Se173ef-3-6", "Loading state rendered while detail request is in flight")
            page.unroute(detail_re)
            page.close()

            # --- Error (500) ---
            page = ctx.new_page()
            page.route(detail_re, lambda route: route.fulfill(status=500, content_type="application/json", body=json.dumps({"error": "boom"})))
            page.goto(url, wait_until="networkidle")
            assert_(wait_for(lambda: "boom" in page.locator("[data-testid='sprint-view']").inner_text(), 6.0),
                    "ErrorBanner not shown on 500")
            ok("AC-Se173ef-3-6", "Error (500) surfaces ErrorBanner")
            page.unroute(detail_re)
            page.close()

            # --- ParseError banner + page still renders ---
            page = ctx.new_page()
            payload = detail_payload(parseErrors=[{"section": "done-judgment.json", "detail": "JSON syntax error at line 3"}])
            page.route(detail_re, lambda route: route.fulfill(status=200, content_type="application/json", body=json.dumps(payload)))
            page.goto(url, wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-trust']", timeout=8000)
            body_txt = page.locator("[data-testid='sprint-view']").inner_text()
            assert_("done-judgment.json" in body_txt, "ParseErrorsBanner should show the corrupt section")
            assert_(page.locator("[data-testid='sprint-detail-guards']").count() == 1, "page must still render other sections despite a parse error")
            ok("AC-Se173ef-3-6", "ParseError: banner shown AND rest of page still renders (1 corrupt file ≠ dead screen)")
            page.unroute(detail_re)
            page.close()

            # --- PartialArtifacts: only verifyRun present ---
            page = ctx.new_page()
            partial = detail_payload(verifyRun={"overallMachineStatus": "pass", "runs": [{"name": "unit", "exitCode": 0, "machineStatus": "pass"}]})
            page.route(detail_re, lambda route: route.fulfill(status=200, content_type="application/json", body=json.dumps(partial)))
            page.goto(url, wait_until="networkidle")
            page.wait_for_selector("[data-testid='sprint-detail-machine-verdict']", timeout=8000)
            assert_("PASS" in page.locator("[data-testid='sprint-detail-machine-verdict']").inner_text().upper(), "verifyRun should render when present")
            assert_("未生成" in page.locator("[data-testid='sprint-detail-verifier-verdict']").inner_text(), "absent verifier should show empty-note")
            assert_("未生成" in page.locator("[data-testid='sprint-detail-guards']").inner_text(), "absent done-judgment should show empty-note")
            ok("AC-Se173ef-3-6", "PartialArtifacts: present sections render, absent ones show empty-notes")
            page.close()

            ctx.close()
        finally:
            browser.close()


def main() -> int:
    print(f"Se173ef sprint-tab MOCK tests against {BASE_URL}")
    code, _ = http_json("GET", "/api/health")
    if code != 200:
        fail(f"dev instance not healthy: {code}")

    with palmux2_test_fixture("se173ef-mock") as fx:
        (fx.path / "docs").mkdir(exist_ok=True)
        (fx.path / "docs" / "ROADMAP.json").write_text(json.dumps(MINIMAL_ROADMAP, ensure_ascii=False, indent=2))

        code, _ = http_json("POST", f"/api/repos/{urllib.parse.quote(fx.repo_id)}/branches/open", {"branchName": "main"})
        assert_(code in (200, 201), f"open branch: {code}")
        from s028_sprint_json import fetch_repos, find_branch  # type: ignore  # noqa: PLC0415
        b = find_branch(fetch_repos(), fx.repo_id, "main")
        assert_(b is not None, "main branch missing")
        branch_id = b["id"]  # type: ignore[index]

        def tabs():
            c, bd = http_json("GET", f"/api/repos/{urllib.parse.quote(fx.repo_id)}/branches/{urllib.parse.quote(branch_id)}/tabs")
            return c == 200 and any(t["type"] == "sprint" for t in bd.get("tabs", []))

        assert_(wait_for(tabs, 8.0), "sprint tab did not appear")
        run(fx.repo_id, branch_id)

    print("Se173ef MOCK: PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
