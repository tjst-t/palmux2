#!/usr/bin/env python3
"""Sprint S1d2278 Story 4 — autopilot/sprint billing-path documentation test.

Acceptance criteria:
  [AC-S1d2278-4-1] docs/sprint-logs/S1d2278/autopilot-billing-path.md exists
  [AC-S1d2278-4-2] the file is >= 100 bytes
  [AC-S1d2278-4-3] the file contains a 結論 section heading

This is a non-GUI static-analysis test; no running server is required.
Exit 0 = PASS.
"""
from __future__ import annotations

import sys
from pathlib import Path

# Resolve repo root relative to this test file's location
REPO_ROOT = Path(__file__).resolve().parents[2]
REPORT_PATH = REPO_ROOT / "docs" / "sprint-logs" / "S1d2278" / "autopilot-billing-path.md"

MIN_BYTES = 100

VERDICT_PATTERNS = [
    "## 結論",
    "# 結論",
    "結論:",
]


def fail(ac: str, msg: str) -> None:
    print(f"FAIL [{ac}]: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(ac: str, msg: str) -> None:
    print(f"PASS [{ac}]: {msg}")


def main() -> int:
    print(f"==> S1d2278-4 billing-path AC test")
    print(f"    report path: {REPORT_PATH}")

    # [AC-S1d2278-4-1] file exists
    if not REPORT_PATH.exists():
        fail(
            "AC-S1d2278-4-1",
            f"autopilot-billing-path.md does not exist at {REPORT_PATH}",
        )
    ok("AC-S1d2278-4-1", "autopilot-billing-path.md exists")

    # [AC-S1d2278-4-2] file is >= 100 bytes
    size = REPORT_PATH.stat().st_size
    if size < MIN_BYTES:
        fail(
            "AC-S1d2278-4-2",
            f"file is only {size} bytes (minimum {MIN_BYTES})",
        )
    ok("AC-S1d2278-4-2", f"file size is {size} bytes (>= {MIN_BYTES})")

    # [AC-S1d2278-4-3] contains a 結論 section heading
    content = REPORT_PATH.read_text(encoding="utf-8")
    found = any(pattern in content for pattern in VERDICT_PATTERNS)
    if not found:
        fail(
            "AC-S1d2278-4-3",
            f"file does not contain a 結論 section (looked for: {VERDICT_PATTERNS})",
        )
    # Show which pattern matched
    matched = next(p for p in VERDICT_PATTERNS if p in content)
    ok("AC-S1d2278-4-3", f"結論 section found (matched pattern: {matched!r})")

    print("==> S1d2278-4 PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
