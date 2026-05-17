#!/usr/bin/env python3
"""Sprint S1d2278 Story 5 — SDK credit analysis documentation test.

Acceptance criteria:
  [AC-S1d2278-5-1] docs/sprint-logs/S1d2278/sdk-credit-analysis.md exists
  [AC-S1d2278-5-2] the file is >= 100 bytes and contains a 判定 section
  [AC-S1d2278-5-3] the file contains a verdict keyword (within|overage|large-overage|unknown)

This is a non-GUI static-analysis test; no running server is required.
Exit 0 = PASS.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

# Resolve repo root relative to this test file's location
REPO_ROOT = Path(__file__).resolve().parents[2]
REPORT_PATH = REPO_ROOT / "docs" / "sprint-logs" / "S1d2278" / "sdk-credit-analysis.md"

MIN_BYTES = 100

VERDICT_SECTION_PATTERNS = [
    "## 3. 判定",
    "## 判定",
    "# 判定",
    "判定:",
    "判定：",
]

VERDICT_KEYWORDS = re.compile(r"\b(within|overage|large-overage|unknown)\b")


def fail(ac: str, msg: str) -> None:
    print(f"FAIL [{ac}]: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(ac: str, msg: str) -> None:
    print(f"PASS [{ac}]: {msg}")


def main() -> int:
    print(f"==> S1d2278-5 sdk-credit-analysis AC test")
    print(f"    report path: {REPORT_PATH}")

    # [AC-S1d2278-5-1] file exists
    if not REPORT_PATH.exists():
        fail(
            "AC-S1d2278-5-1",
            f"sdk-credit-analysis.md does not exist at {REPORT_PATH}",
        )
    ok("AC-S1d2278-5-1", "sdk-credit-analysis.md exists")

    # [AC-S1d2278-5-2] file is >= 100 bytes and contains a 判定 section
    size = REPORT_PATH.stat().st_size
    if size < MIN_BYTES:
        fail(
            "AC-S1d2278-5-2",
            f"file is only {size} bytes (minimum {MIN_BYTES})",
        )

    content = REPORT_PATH.read_text(encoding="utf-8")

    found_section = any(pattern in content for pattern in VERDICT_SECTION_PATTERNS)
    if not found_section:
        fail(
            "AC-S1d2278-5-2",
            f"file does not contain a 判定 section (looked for: {VERDICT_SECTION_PATTERNS})",
        )
    matched_section = next(p for p in VERDICT_SECTION_PATTERNS if p in content)
    ok(
        "AC-S1d2278-5-2",
        f"file is {size} bytes (>= {MIN_BYTES}) and contains 判定 section (matched: {matched_section!r})",
    )

    # [AC-S1d2278-5-3] contains a verdict keyword
    match = VERDICT_KEYWORDS.search(content)
    if not match:
        fail(
            "AC-S1d2278-5-3",
            "file does not contain a verdict keyword (within|overage|large-overage|unknown)",
        )
    ok("AC-S1d2278-5-3", f"verdict keyword found: {match.group(0)!r}")

    print("==> S1d2278-5 PASS")
    return 0


if __name__ == "__main__":
    sys.exit(main())
