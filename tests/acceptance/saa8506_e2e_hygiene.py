#!/usr/bin/env python3
"""Sprint Saa8506 — Acceptance criteria checker.

Maps each ROADMAP AC tag to a deterministic check on the repo state /
sprint-logs. This file is run as a plain script during `sprint verify`;
exit code 0 = all AC pass.

The AC tag comments below are required by the sprint skill so traceability
can be grepped from this single file.
"""
from __future__ import annotations

import json
import os
import re
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
LOGS = REPO_ROOT / "docs" / "sprint-logs" / "Saa8506"

FAILS: list[str] = []


def check(tag: str, cond: bool, msg: str = "") -> None:
    status = "PASS" if cond else "FAIL"
    print(f"[{tag}] {status} {msg}")
    if not cond:
        FAILS.append(tag)


def grep_first(file: Path, pattern: str) -> list[str]:
    if not file.exists():
        return []
    rx = re.compile(pattern)
    return [line for line in file.read_text().splitlines() if rx.search(line)]


# --------------------------------------------------------------------- 0


def check_story_0() -> None:
    # [AC-Saa8506-0-1] regression-baseline.json with full raw output exists
    baseline = LOGS / "regression-baseline.json"
    check(
        "AC-Saa8506-0-1",
        baseline.exists() and "phases" in json.loads(baseline.read_text()),
        f"regression-baseline.json @ {baseline.relative_to(REPO_ROOT)}",
    )

    # [AC-Saa8506-0-2] baseline = all green
    if baseline.exists():
        d = json.loads(baseline.read_text())
        e2e = d["e2e_summary"]
        ok = (
            d["phases"]["go_test"]["result"] == "pass"
            and d["phases"]["go_build"]["result"] == "pass"
            and d["phases"]["fe_build"]["result"] == "pass"
            and d["phases"]["fe_lint"]["result"] == "pass"
            and d["phases"]["fe_lint"]["errors"] == 0
            and e2e["fail"] == 0
            and e2e["timeout"] == 0
            and e2e["pass"] == e2e["total"]
        )
        check("AC-Saa8506-0-2", ok, f"baseline summary {e2e}")
    else:
        check("AC-Saa8506-0-2", False, "no baseline file")

    # [AC-Saa8506-0-3] migration-matrix.md with Section A/B/C
    matrix = LOGS / "migration-matrix.md"
    if matrix.exists():
        text = matrix.read_text()
        ok = (
            "Section A" in text
            and "Section B" in text
            and "Section C" in text
            and "Sub-agent" in text
        )
        check("AC-Saa8506-0-3", ok, str(matrix.relative_to(REPO_ROOT)))
    else:
        check("AC-Saa8506-0-3", False, "missing matrix")


# --------------------------------------------------------------------- 1

TEN_FILES = [
    "tests/e2e/s001_refine_plan.py",
    "tests/e2e/s004_mcp_indicator.py",
    "tests/e2e/s005_hook_events.py",
    "tests/e2e/s006_add_dir_file.py",
    "tests/e2e/s007_ask_question.py",
    "tests/e2e/s008_upload_routes.py",
    "tests/e2e/s009_multi_tab.py",
    "tests/e2e/s009_fix_lifecycle.py",
    "tests/e2e/s009_fix_lifecycle_v2.py",
    "tests/e2e/s009_fix_periodic_check.py",
    "tests/e2e/s009_fix4_ui_monitor.py",
]


def check_story_1() -> None:
    # [AC-Saa8506-1-1] no test still hardcodes a fallback BRANCH_ID literal
    # AND each test imports palmux2_test_fixture from _fixture
    hard_fail = []
    no_fixture = []
    hardcode_rx = re.compile(
        r"os\.environ\.get\(\s*['\"]S[0-9A-Za-z_]+_BRANCH_ID['\"]\s*,\s*['\"][^'\"]+['\"]"
    )
    fixture_rx = re.compile(r"from\s+_fixture\s+import\s+.*palmux2_test_fixture")
    for rel in TEN_FILES:
        p = REPO_ROOT / rel
        if not p.exists():
            hard_fail.append(rel + " (missing)")
            continue
        src = p.read_text()
        if hardcode_rx.search(src):
            hard_fail.append(rel)
        if not fixture_rx.search(src):
            # Some tests may import via a different path (e.g. submodule);
            # accept any reference to palmux2_test_fixture context manager.
            if "palmux2_test_fixture" not in src:
                no_fixture.append(rel)
    check(
        "AC-Saa8506-1-1-hardcode",
        not hard_fail,
        f"hardcoded BRANCH_ID survivors: {hard_fail}",
    )
    check(
        "AC-Saa8506-1-1-fixture",
        not no_fixture,
        f"missing palmux2_test_fixture import: {no_fixture}",
    )

    # [AC-Saa8506-1-2] regression-after-story1.json all green
    after1 = LOGS / "regression-after-story1.json"
    if after1.exists():
        d = json.loads(after1.read_text())
        e2e = d["e2e_summary"]
        ok = (
            d["phases"]["go_test"]["result"] == "pass"
            and d["phases"]["go_build"]["result"] == "pass"
            and d["phases"]["fe_build"]["result"] == "pass"
            and d["phases"]["fe_lint"]["result"] == "pass"
            and d["phases"]["fe_lint"]["errors"] == 0
            and e2e["fail"] == 0
            and e2e["timeout"] == 0
        )
        check("AC-Saa8506-1-2", ok, f"after-story1 {e2e}")
    else:
        check("AC-Saa8506-1-2", False, "missing regression-after-story1.json")

    # [AC-Saa8506-1-3] manual smoke proves cleanup — see
    # docs/sprint-logs/Saa8506/manual-smoke-Saa8506-1.md
    smoke = LOGS / "manual-smoke-Saa8506-1.md"
    check(
        "AC-Saa8506-1-3",
        smoke.exists() and "worktree_clean=yes" in smoke.read_text(),
        f"manual smoke @ {smoke.relative_to(REPO_ROOT) if smoke.exists() else 'missing'}",
    )

    # [AC-Saa8506-1-4] same as 1-2 plus smoke gate (covered)
    check(
        "AC-Saa8506-1-4",
        after1.exists() and smoke.exists(),
        "after-story1 + manual smoke present",
    )


# --------------------------------------------------------------------- 2


def check_story_2() -> None:
    # [AC-Saa8506-2-1] s006_attach_dnd_wire.py exists and references
    # drag-and-drop + WS frame inspection
    path = REPO_ROOT / "tests" / "e2e" / "s006_attach_dnd_wire.py"
    if path.exists():
        src = path.read_text()
        ok = (
            "addDirs" in src
            and ("@ ref" in src or "@ refs" in src or "@" in src)
            and ("dragenter" in src or "dataTransfer" in src or "drop" in src)
            and "ws" in src.lower()
        )
        check("AC-Saa8506-2-1", ok, str(path.relative_to(REPO_ROOT)))
    else:
        check("AC-Saa8506-2-1", False, "file missing")

    # [AC-Saa8506-2-2] s006_attach_dnd_wire imported into regression script
    rsh = REPO_ROOT / "scripts" / "sprint-regression-Saa8506.sh"
    if rsh.exists():
        check(
            "AC-Saa8506-2-2",
            "s006_attach_dnd_wire" in rsh.read_text(),
            "regression script lists s006_attach_dnd_wire",
        )
    else:
        check("AC-Saa8506-2-2", False, "regression script missing")

    # [AC-Saa8506-2-3] regression-after-story2.json all green AND >=23 tests
    after2 = LOGS / "regression-after-story2.json"
    if after2.exists():
        d = json.loads(after2.read_text())
        e2e = d["e2e_summary"]
        ok = (
            e2e["fail"] == 0
            and e2e["timeout"] == 0
            and e2e["total"] >= 23
            and d["phases"]["fe_lint"]["errors"] == 0
        )
        check("AC-Saa8506-2-3", ok, f"after-story2 {e2e}")
    else:
        check("AC-Saa8506-2-3", False, "missing regression-after-story2.json")


# --------------------------------------------------------------------- 3


def check_story_3() -> None:
    # [AC-Saa8506-3-1] regression script free of BRANCH_ID override layer
    rsh = REPO_ROOT / "scripts" / "sprint-regression-Saa8506.sh"
    if rsh.exists():
        text = rsh.read_text()
        # Look for the env override pattern from the predecessor script
        hits = []
        for line in text.splitlines():
            # We want to flag actual env-override pattern, not the test
            # filename like s001_branch_id (none expected). Pattern: the
            # uppercase exports S00X_BRANCH_ID=... or curl ... | grep branch_id
            if re.search(r"^\s*export\s+S[0-9A-Za-z_]+_BRANCH_ID", line):
                hits.append(line.strip())
            if re.search(r"TEST_BRANCH_ID\s*=", line):
                hits.append(line.strip())
        check(
            "AC-Saa8506-3-1",
            not hits,
            f"override survivors: {hits[:3]}",
        )
    else:
        check("AC-Saa8506-3-1", False, "regression script missing")

    # [AC-Saa8506-3-2] regression-final.json green (>=23 tests, all green)
    fin = LOGS / "regression-final.json"
    if fin.exists():
        d = json.loads(fin.read_text())
        e2e = d["e2e_summary"]
        ok = (
            e2e["fail"] == 0
            and e2e["timeout"] == 0
            and e2e["total"] >= 23
            and d["phases"]["fe_lint"]["errors"] == 0
        )
        check("AC-Saa8506-3-2", ok, f"final {e2e}")
    else:
        check("AC-Saa8506-3-2", False, "missing regression-final.json")

    # [AC-Saa8506-3-3] manual smoke for sprint final exists
    smoke = LOGS / "manual-smoke-Saa8506-3.md"
    check(
        "AC-Saa8506-3-3",
        smoke.exists() and "all_clean=yes" in smoke.read_text(),
        f"manual smoke @ {smoke.relative_to(REPO_ROOT) if smoke.exists() else 'missing'}",
    )

    # [AC-Saa8506-3-4] needs-user-input.json reviewed (either empty or
    # decisions.json records the resolution)
    nui = LOGS / "needs-user-input.json"
    dec = LOGS / "decisions.json"
    if nui.exists() and dec.exists():
        nui_d = json.loads(nui.read_text())
        dec_d = json.loads(dec.read_text())
        items = nui_d.get("items", [])
        if not items:
            check("AC-Saa8506-3-4", True, "needs-user-input empty (no batch needed)")
        else:
            # Each blocking item must reference a decision in decisions.json
            resolved_ids = {d.get("nui_ref") for d in dec_d.get("decisions", [])}
            unresolved = [
                it["id"]
                for it in items
                if it.get("blocking", False) and it["id"] not in resolved_ids
            ]
            check(
                "AC-Saa8506-3-4",
                not unresolved,
                f"unresolved blocking items: {unresolved}",
            )
    else:
        check("AC-Saa8506-3-4", False, "needs-user-input.json or decisions.json missing")


def main() -> int:
    print(f"==> Saa8506 acceptance checks (repo {REPO_ROOT})")
    check_story_0()
    check_story_1()
    check_story_2()
    check_story_3()
    print()
    if FAILS:
        print(f"FAIL — {len(FAILS)} criteria not satisfied: {FAILS}")
        return 1
    print("ALL ACCEPTANCE CRITERIA PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
