#!/usr/bin/env python3
"""Sprint Se173ef Story 4 — SPRINT_LOGS_SCHEMA.json ⇄ tab artifact contract.

Verifies (AC-Se173ef-4-1 / 4-2) that the skill's canonical
SPRINT_LOGS_SCHEMA.json reflects the CURRENT artifact set actually read by
the Sprint tab, that removed artifacts are marked deprecated, and that the
contract is documented so future artifact additions follow a single path.

The schema lives OUTSIDE the repo at
~/.claude/skills/sprint/references/SPRINT_LOGS_SCHEMA.json. This test reads
it there (a skill-file change intentionally not in the repo diff — recorded
in docs/sprint-logs/Se173ef/decisions.json).

No server needed — a static contract check. Exit 0 = PASS.
"""
from __future__ import annotations

import json
import os
import re
import sys
from pathlib import Path

REPO = Path(__file__).resolve().parents[2]
SCHEMA = Path(os.path.expanduser("~/.claude/skills/sprint/references/SPRINT_LOGS_SCHEMA.json"))


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(tag: str, msg: str = "") -> None:
    print(f"  [{tag}] {msg or 'OK'}")


def assert_(cond: bool, msg: str) -> None:
    if not cond:
        fail(msg)


def main() -> int:
    print("Se173ef Story 4 — schema contract check")
    assert_(SCHEMA.exists(), f"SPRINT_LOGS_SCHEMA.json not found at {SCHEMA}")
    try:
        schema = json.loads(SCHEMA.read_text())
    except json.JSONDecodeError as e:
        fail(f"SPRINT_LOGS_SCHEMA.json is not valid JSON: {e}")

    # AC-4-1: current artifact sections present.
    required_sections = [
        "decisions", "verify_run", "verification_report", "done_judgment",
        "compromises", "comprehension_report", "prototype_review", "reopen",
        "gui_spec", "scenario",
    ]
    for s in required_sections:
        assert_(s in schema, f"schema missing current-artifact section: {s}")
    ok("AC-Se173ef-4-1", f"schema documents all current artifacts: {', '.join(required_sections)}")

    # AC-4-1: deprecated artifacts explicitly noted.
    idx = schema.get("$artifact_index", {})
    dep = idx.get("deprecated", {})
    for d in ("acceptance-matrix.json", "e2e-results.json"):
        assert_(d in dep, f"deprecated artifact {d} not marked deprecated in $artifact_index")
    ok("AC-Se173ef-4-1", "removed artifacts (acceptance-matrix.json / e2e-results.json) marked deprecated")

    # AC-4-1: the $file names in the schema match what the tab backend reads.
    handler_src = ""
    for f in ("handler.go", "handler_review.go", "provider.go"):
        handler_src += (REPO / "internal" / "tab" / "sprint" / f).read_text()
    artifacts_src = (REPO / "internal" / "tab" / "sprint" / "parser" / "artifacts.go").read_text()
    current = idx.get("current", [])
    # Concrete (non-templated) filenames must appear in the tab source.
    for name in current:
        if "{" in name:  # templated (verify-run-{name}.log, gui-spec-{StoryID}.json)
            continue
        if name == "verification-results.json":
            continue  # companion, tab reads verify-run/verification-report instead
        assert_(name in handler_src or name in artifacts_src,
                f"schema lists {name} but the tab backend never reads it")
    ok("AC-Se173ef-4-1", "every concrete current-artifact filename is read by the tab backend")

    # Backend reads verify-run / verification-report / done-judgment / etc.
    for name in ("verify-run.json", "verification-report.json", "done-judgment.json",
                 "compromises.json", "comprehension-report.md", "prototype-review.json", "reopen.json"):
        assert_(name in handler_src, f"tab handler does not read {name}")
    ok("AC-Se173ef-4-1", "tab handler reads the full current trust-source artifact set")

    # AC-4-2: the contract is documented (README) with the drift-prevention procedure.
    readme = REPO / "docs" / "sprint-logs" / "README.md"
    assert_(readme.exists(), "docs/sprint-logs/README.md missing")
    txt = readme.read_text()
    assert_("SPRINT_LOGS_SCHEMA.json" in txt, "README does not reference the canonical schema")
    assert_(re.search(r"NEW artifact", txt), "README lacks the 'when a NEW artifact is introduced' procedure")
    assert_("$artifact_index" in txt, "README does not point at the schema's $artifact_index")
    ok("AC-Se173ef-4-2", "docs/sprint-logs/README.md documents the tab⇄skill contract + drift-prevention steps")

    print("Se173ef Story 4 schema contract: PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
