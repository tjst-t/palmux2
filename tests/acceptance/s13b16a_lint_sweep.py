#!/usr/bin/env python3
"""Sprint S13b16a acceptance tests — FE lint debt sweep.

Story-level scenario for a non-GUI sprint (lint cleanup): the developer's entry
point is the shell — they run `npm --prefix frontend run lint` and observe
errors=0 in the summary. This test exercises that exact entry point and asserts
on the eslint stylish output.

Each acceptance criterion is tagged `[AC-S13b16a-{N}-{M}]` so sprint verify can
trace coverage automatically.
"""
from __future__ import annotations

import json
import re
import subprocess
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[2]
LOG_DIR = REPO_ROOT / "docs" / "sprint-logs" / "S13b16a"
BASELINE_REGRESSION = LOG_DIR / "regression-baseline.json"
BASELINE_LINT = LOG_DIR / "lint-baseline.md"
FINAL_REGRESSION = LOG_DIR / "regression-final.json"
NEEDS_INPUT = LOG_DIR / "needs-user-input.json"
DECISIONS = LOG_DIR / "decisions.json"


def _run_lint() -> tuple[int, str]:
    """Invoke `npm --prefix frontend run lint` from the user's shell entry point."""
    proc = subprocess.run(
        ["npm", "--prefix", "frontend", "run", "lint"],
        cwd=REPO_ROOT,
        capture_output=True,
        text=True,
        timeout=120,
    )
    return proc.returncode, proc.stdout + proc.stderr


def _parse_eslint_summary(output: str) -> tuple[int, int]:
    """Extract (errors, warnings) from the `✖ N problems (E errors, W warnings)` line.
    Returns (0, 0) if eslint reports no problems (no summary line emitted)."""
    m = re.search(r"✖\s+\d+\s+problems\s+\((\d+)\s+errors?,\s+(\d+)\s+warnings?\)", output)
    if m:
        return int(m.group(1)), int(m.group(2))
    return 0, 0


def _per_rule_counts(output: str) -> dict[str, int]:
    """Count occurrences of each rule id in eslint stylish output (best-effort)."""
    counts: dict[str, int] = {}
    for ln in output.splitlines():
        s = ln.rstrip()
        if not s.strip():
            continue
        # Rule id is the last token on the line, after wide whitespace
        toks = s.split()
        cand = toks[-1]
        # Strip stray punctuation — the parser saw `'react-hooks/exhaustive-deps')` once
        cand = cand.strip("'\")")
        if "/" not in cand:
            continue
        if not re.fullmatch(r"[a-z@][a-z0-9/@-]+", cand):
            continue
        counts[cand] = counts.get(cand, 0) + 1
    return counts


# ---- Story S13b16a-0 ----

def test_ac_s13b16a_0_1():
    """[AC-S13b16a-0-1] regression-baseline.json exists with raw phase results."""
    assert BASELINE_REGRESSION.exists(), f"missing {BASELINE_REGRESSION}"
    data = json.loads(BASELINE_REGRESSION.read_text())
    # Required phases
    for phase in ("go_test", "go_build", "fe_build", "fe_lint"):
        assert phase in data["phases"], f"phase {phase} missing"
    assert "e2e_summary" in data
    s = data["e2e_summary"]
    assert s["total"] == 22, f"expected 22 E2E tests, got {s['total']}"
    print(f"[AC-S13b16a-0-1] PASS — baseline json has all phases + 22 E2E tests")


def test_ac_s13b16a_0_2():
    """[AC-S13b16a-0-2] baseline non-lint suite all green (E2E 22/22 + go test + builds)."""
    data = json.loads(BASELINE_REGRESSION.read_text())
    s = data["e2e_summary"]
    assert s["pass"] == 22 and s["fail"] == 0 and s["timeout"] == 0, f"E2E not all-green: {s}"
    for phase in ("go_test", "go_build", "fe_build"):
        r = data["phases"][phase]["result"]
        assert r == "pass", f"phase {phase} = {r}"
    # lint may still fail at baseline (we're about to fix it) — that is allowed
    print(f"[AC-S13b16a-0-2] PASS — all non-lint phases green")


def test_ac_s13b16a_0_3():
    """[AC-S13b16a-0-3] lint-baseline.md captures per-rule per-file matrix."""
    assert BASELINE_LINT.exists(), f"missing {BASELINE_LINT}"
    text = BASELINE_LINT.read_text()
    # Must contain rule names and Story scope assignment
    assert "react-hooks/set-state-in-effect" in text
    assert "react-hooks/refs" in text
    assert "Story scope assignment" in text
    assert "S13b16a-1" in text and "S13b16a-2" in text and "S13b16a-3" in text
    # Must contain at least one file path
    assert "frontend/src/" in text
    print(f"[AC-S13b16a-0-3] PASS — lint-baseline.md present with per-rule per-file matrix")


# ---- Story S13b16a-1 ----

def test_ac_s13b16a_1_1():
    """[AC-S13b16a-1-1] no react-hooks/set-state-in-effect errors remain."""
    rc, out = _run_lint()
    counts = _per_rule_counts(out)
    n = counts.get("react-hooks/set-state-in-effect", 0)
    assert n == 0, f"set-state-in-effect still has {n} occurrences:\n{out[-2000:]}"
    print(f"[AC-S13b16a-1-1] PASS — react-hooks/set-state-in-effect = 0")


def test_ac_s13b16a_1_2():
    """[AC-S13b16a-1-2] regression-after-story1.json all green (lint excluded)."""
    p = LOG_DIR / "regression-story1.json"
    assert p.exists(), f"missing {p}"
    data = json.loads(p.read_text())
    s = data["e2e_summary"]
    assert s["pass"] == 22 and s["fail"] == 0 and s["timeout"] == 0, f"E2E not all-green: {s}"
    for phase in ("go_test", "go_build", "fe_build"):
        assert data["phases"][phase]["result"] == "pass", f"{phase} failed"
    print(f"[AC-S13b16a-1-2] PASS — story1 regression all green (lint excluded)")


def test_ac_s13b16a_1_3():
    """[AC-S13b16a-1-3] manual smoke recorded for Story 1."""
    p = LOG_DIR / "manual-smoke-S13b16a-1.md"
    assert p.exists(), f"missing {p}"
    txt = p.read_text()
    assert "PASS" in txt or "pass" in txt, f"manual smoke does not record a pass: {txt[:300]}"
    print(f"[AC-S13b16a-1-3] PASS — manual smoke S13b16a-1 recorded")


# ---- Story S13b16a-2 ----

def test_ac_s13b16a_2_1():
    """[AC-S13b16a-2-1] no react-hooks/refs errors remain."""
    rc, out = _run_lint()
    counts = _per_rule_counts(out)
    n = counts.get("react-hooks/refs", 0)
    assert n == 0, f"react-hooks/refs still has {n} occurrences:\n{out[-2000:]}"
    print(f"[AC-S13b16a-2-1] PASS — react-hooks/refs = 0")


def test_ac_s13b16a_2_2():
    """[AC-S13b16a-2-2] regression-after-story2.json all green + manual smoke recorded."""
    p = LOG_DIR / "regression-story2.json"
    assert p.exists(), f"missing {p}"
    data = json.loads(p.read_text())
    s = data["e2e_summary"]
    assert s["pass"] == 22 and s["fail"] == 0 and s["timeout"] == 0, f"E2E not all-green: {s}"
    for phase in ("go_test", "go_build", "fe_build"):
        assert data["phases"][phase]["result"] == "pass", f"{phase} failed"
    smoke = LOG_DIR / "manual-smoke-S13b16a-2.md"
    assert smoke.exists(), f"missing {smoke}"
    print(f"[AC-S13b16a-2-2] PASS — story2 regression + smoke OK")


# ---- Story S13b16a-3 ----

def test_ac_s13b16a_3_1():
    """[AC-S13b16a-3-1] residual zoo errors all resolved (react-refresh, no-empty, etc.)."""
    rc, out = _run_lint()
    counts = _per_rule_counts(out)
    # All Story 3 target rules must be 0
    for rule in (
        "react-refresh/only-export-components",
        "no-empty",
        "@typescript-eslint/no-unused-expressions",
        "@next/next/no-img-element",
        "react-hooks/exhaustive-deps",
        "react-hooks/rules-of-hooks",
        "react-hooks/immutability",
        "@typescript-eslint/no-unused-vars",
    ):
        n = counts.get(rule, 0)
        assert n == 0, f"rule {rule} still has {n} occurrences"
    print(f"[AC-S13b16a-3-1] PASS — residual zoo errors all resolved")


def test_ac_s13b16a_3_2():
    """[AC-S13b16a-3-2] `npm run lint` reports errors=0 (warnings <= baseline 9)."""
    rc, out = _run_lint()
    errors, warnings = _parse_eslint_summary(out)
    assert errors == 0, f"lint errors = {errors} (expected 0)\n{out[-2000:]}"
    assert warnings <= 9, f"warnings = {warnings} > baseline 9"
    # rc must also be 0 — eslint exits 1 on any error
    assert rc == 0, f"npm lint exit = {rc}, expected 0"
    print(f"[AC-S13b16a-3-2] PASS — errors=0 warnings={warnings}")


def test_ac_s13b16a_3_3():
    """[AC-S13b16a-3-3] regression-final.json all green incl. lint."""
    assert FINAL_REGRESSION.exists(), f"missing {FINAL_REGRESSION}"
    data = json.loads(FINAL_REGRESSION.read_text())
    s = data["e2e_summary"]
    assert s["pass"] == 22 and s["fail"] == 0 and s["timeout"] == 0, f"final E2E: {s}"
    for phase in ("go_test", "go_build", "fe_build", "fe_lint"):
        r = data["phases"][phase]["result"]
        assert r == "pass", f"final phase {phase} = {r}"
    smoke = LOG_DIR / "manual-smoke-S13b16a-3.md"
    assert smoke.exists(), f"missing {smoke}"
    print(f"[AC-S13b16a-3-3] PASS — regression-final all green + manual smoke recorded")


def test_ac_s13b16a_3_4():
    """[AC-S13b16a-3-4] needs-user-input.json items batched into decisions.json."""
    if not NEEDS_INPUT.exists() or json.loads(NEEDS_INPUT.read_text()).get("items", []) == []:
        # No items raised — skip clause is allowed by AC text ("空なら直接 sprint done")
        print(f"[AC-S13b16a-3-4] PASS — needs-user-input.json empty, no batch needed")
        return
    items = json.loads(NEEDS_INPUT.read_text()).get("items", [])
    assert DECISIONS.exists(), f"needs-user-input has {len(items)} items but no decisions.json"
    decisions = json.loads(DECISIONS.read_text())
    assert "user_decisions" in decisions, f"decisions.json lacks user_decisions block"
    decided_ids = {d["nui_id"] for d in decisions.get("user_decisions", [])}
    for it in items:
        if it.get("blocking", False):
            assert it["id"] in decided_ids, f"blocking item {it['id']} not decided"
    print(f"[AC-S13b16a-3-4] PASS — {len(items)} NUI items recorded, {len(decided_ids)} decided")


def main() -> int:
    tests = [
        ("AC-S13b16a-0-1", test_ac_s13b16a_0_1),
        ("AC-S13b16a-0-2", test_ac_s13b16a_0_2),
        ("AC-S13b16a-0-3", test_ac_s13b16a_0_3),
        ("AC-S13b16a-1-1", test_ac_s13b16a_1_1),
        ("AC-S13b16a-1-2", test_ac_s13b16a_1_2),
        ("AC-S13b16a-1-3", test_ac_s13b16a_1_3),
        ("AC-S13b16a-2-1", test_ac_s13b16a_2_1),
        ("AC-S13b16a-2-2", test_ac_s13b16a_2_2),
        ("AC-S13b16a-3-1", test_ac_s13b16a_3_1),
        ("AC-S13b16a-3-2", test_ac_s13b16a_3_2),
        ("AC-S13b16a-3-3", test_ac_s13b16a_3_3),
        ("AC-S13b16a-3-4", test_ac_s13b16a_3_4),
    ]
    fails: list[str] = []
    for tag, fn in tests:
        try:
            fn()
        except AssertionError as e:
            print(f"[{tag}] FAIL — {e}")
            fails.append(tag)
        except Exception as e:
            print(f"[{tag}] ERROR — {e!r}")
            fails.append(tag)
    print()
    if fails:
        print(f"FAILED: {len(fails)} / {len(tests)} — {fails}")
        return 1
    print(f"OK: {len(tests)} / {len(tests)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
