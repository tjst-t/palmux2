#!/usr/bin/env python3
"""Sprint S1d2278 Story 6 — Track B go/no-go synthesis test.

Acceptance criteria:
  [AC-S1d2278-6-1] docs/sprint-logs/S1d2278/track-b-roadmap.md exists, >= 100 bytes,
                   and contains the 4-axis verdict table (all 4 axis labels present)
  [AC-S1d2278-6-2] track-b-roadmap.md contains a section 2 continuation block with
                   at least 3 sprint outlines (Sprint A, Sprint B, Sprint C headings)
  [AC-S1d2278-6-3] track-b-roadmap.md contains a section 3 continuation block with
                   the fallback Sprint X outline
  [AC-S1d2278-6-4] docs/sprint-logs/S1d2278/decisions.json exists and contains a
                   '6_7_pivot_decision' entry

This is a non-GUI static-analysis test; no running server is required.
Exit 0 = all PASS.
"""
from __future__ import annotations

import json
import sys
from pathlib import Path

# Resolve repo root relative to this test file's location
REPO_ROOT = Path(__file__).resolve().parents[2]
ROADMAP_PATH = REPO_ROOT / "docs" / "sprint-logs" / "S1d2278" / "track-b-roadmap.md"
DECISIONS_PATH = REPO_ROOT / "docs" / "sprint-logs" / "S1d2278" / "decisions.json"

MIN_BYTES = 100

# 4 axis labels that must appear in the roadmap
AXIS_LABELS = [
    "emulator 実用性",
    "PTY daemon 安定性",
    "autopilot 経路",
    "SDK クレジット",
]

# Sprint headings for section 2
SPRINT_HEADINGS = ["Sprint A", "Sprint B", "Sprint C"]

# Section markers
SECTION2_MARKERS = ["## 2.", "## 2.", "### 2.", "## 2 "]
SECTION3_MARKERS = ["## 3.", "## 3.", "### 3.", "## 3 "]
SPRINT_X_MARKERS = ["Sprint X", "Sprint X (fallback)", "Sprint X —", "Sprint X:"]


def _fail(ac: str, msg: str) -> None:
    print(f"FAIL [{ac}]: {msg}", file=sys.stderr)
    sys.exit(1)


def _pass(ac: str, msg: str) -> None:
    print(f"PASS [{ac}]: {msg}")


def main() -> int:
    print("==> S1d2278-6 track-b-roadmap synthesis AC test")
    print(f"    roadmap path : {ROADMAP_PATH}")
    print(f"    decisions path: {DECISIONS_PATH}")

    # ------------------------------------------------------------------
    # [AC-S1d2278-6-1] file exists, >= 100 bytes, 4-axis table present
    # ------------------------------------------------------------------
    if not ROADMAP_PATH.exists():
        _fail(
            "AC-S1d2278-6-1",
            f"track-b-roadmap.md does not exist at {ROADMAP_PATH}",
        )

    size = ROADMAP_PATH.stat().st_size
    if size < MIN_BYTES:
        _fail(
            "AC-S1d2278-6-1",
            f"file is only {size} bytes (minimum {MIN_BYTES})",
        )

    content = ROADMAP_PATH.read_text(encoding="utf-8")

    missing_axes = [label for label in AXIS_LABELS if label not in content]
    if missing_axes:
        _fail(
            "AC-S1d2278-6-1",
            f"4-axis verdict table missing labels: {missing_axes}",
        )

    _pass(
        "AC-S1d2278-6-1",
        f"track-b-roadmap.md exists ({size} bytes), all 4 axis labels present: {AXIS_LABELS}",
    )

    # ------------------------------------------------------------------
    # [AC-S1d2278-6-2] section 2 continuation block + Sprint A/B/C
    # ------------------------------------------------------------------
    has_section2 = any(marker in content for marker in SECTION2_MARKERS)
    if not has_section2:
        _fail(
            "AC-S1d2278-6-2",
            f"track-b-roadmap.md does not contain a section 2 marker (looked for: {SECTION2_MARKERS})",
        )

    missing_sprints = [h for h in SPRINT_HEADINGS if h not in content]
    if missing_sprints:
        _fail(
            "AC-S1d2278-6-2",
            f"track-b-roadmap.md is missing sprint outline headings: {missing_sprints}",
        )

    _pass(
        "AC-S1d2278-6-2",
        "section 2 present and Sprint A / Sprint B / Sprint C headings all found",
    )

    # ------------------------------------------------------------------
    # [AC-S1d2278-6-3] section 3 continuation block + Sprint X
    # ------------------------------------------------------------------
    has_section3 = any(marker in content for marker in SECTION3_MARKERS)
    if not has_section3:
        _fail(
            "AC-S1d2278-6-3",
            f"track-b-roadmap.md does not contain a section 3 marker (looked for: {SECTION3_MARKERS})",
        )

    has_sprint_x = any(marker in content for marker in SPRINT_X_MARKERS)
    if not has_sprint_x:
        _fail(
            "AC-S1d2278-6-3",
            f"track-b-roadmap.md does not contain a Sprint X fallback outline (looked for: {SPRINT_X_MARKERS})",
        )

    _pass(
        "AC-S1d2278-6-3",
        "section 3 present and Sprint X (fallback Track A pivot) outline found",
    )

    # ------------------------------------------------------------------
    # [AC-S1d2278-6-4] decisions.json exists with 6_7_pivot_decision
    # ------------------------------------------------------------------
    if not DECISIONS_PATH.exists():
        _fail(
            "AC-S1d2278-6-4",
            f"decisions.json does not exist at {DECISIONS_PATH}",
        )

    try:
        decisions_data = json.loads(DECISIONS_PATH.read_text(encoding="utf-8"))
    except json.JSONDecodeError as exc:
        _fail("AC-S1d2278-6-4", f"decisions.json is not valid JSON: {exc}")

    # Support both array root and {"decisions": [...]} object root
    if isinstance(decisions_data, list):
        decisions_list = decisions_data
    elif isinstance(decisions_data, dict):
        decisions_list = decisions_data.get("decisions", [])
    else:
        decisions_list = []

    pivot_entry = next(
        (d for d in decisions_list if isinstance(d, dict) and d.get("id") == "6_7_pivot_decision"),
        None,
    )
    if pivot_entry is None:
        _fail(
            "AC-S1d2278-6-4",
            "decisions.json does not contain an entry with id='6_7_pivot_decision'",
        )

    verdict = pivot_entry.get("verdict", "(missing)")
    _pass(
        "AC-S1d2278-6-4",
        f"decisions.json contains '6_7_pivot_decision' entry (verdict: {verdict!r})",
    )

    print("==> S1d2278-6 PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
