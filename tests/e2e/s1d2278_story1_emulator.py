#!/usr/bin/env python3
"""Sprint S1d2278 Story 1 — Go terminal-emulator library spike tests.

Acceptance criteria:
  [AC-S1d2278-1-1] go test ./internal/poc/emulator/ -run TestCellbufSpike exits 0
  [AC-S1d2278-1-2] go test ./internal/poc/emulator/ -run TestAltSpike exits 0
  [AC-S1d2278-1-3] docs/sprint-logs/S1d2278/emulator-comparison.md exists,
                   is >= 100 bytes, and contains all 5 required section headings

This is a non-GUI static-analysis + go-test runner; no running server is required.
Exit 0 = PASS.
"""
from __future__ import annotations

import subprocess
import sys
from pathlib import Path

# Resolve repo root relative to this test file's location
REPO_ROOT = Path(__file__).resolve().parents[2]
REPORT_PATH = (
    REPO_ROOT / "docs" / "sprint-logs" / "S1d2278" / "emulator-comparison.md"
)
EMULATOR_PKG = "./internal/poc/emulator/"

MIN_BYTES = 100

REQUIRED_HEADINGS = [
    "## 1. 候補と理由",
    "## 2. 6 軸カバレッジ表",
    "## 3. メンテ状況",
    "## 4. 自前実装で書く場合の工数推定",
    "## 5. 推奨",
]


def fail(ac: str, msg: str) -> None:
    print(f"FAIL [{ac}]: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(ac: str, msg: str) -> None:
    print(f"PASS [{ac}]: {msg}")


def run_go_test(run_pattern: str, ac: str) -> None:
    """Run go test with -run <pattern> and assert exit code 0."""
    cmd = [
        "go",
        "test",
        EMULATOR_PKG,
        "-run",
        run_pattern,
        "-v",
        "-timeout",
        "60s",
    ]
    print(f"  ==> running: {' '.join(cmd)}")
    result = subprocess.run(
        cmd,
        cwd=str(REPO_ROOT),
        capture_output=True,
        text=True,
    )
    if result.stdout:
        for line in result.stdout.splitlines():
            print(f"    {line}")
    if result.stderr:
        for line in result.stderr.splitlines():
            print(f"    STDERR: {line}", file=sys.stderr)
    if result.returncode != 0:
        fail(
            ac,
            f"go test -run {run_pattern} exited with code {result.returncode}",
        )
    ok(ac, f"go test -run {run_pattern} exited 0")


def main() -> int:
    print("==> S1d2278-1 emulator spike AC test")
    print(f"    repo root: {REPO_ROOT}")

    # [AC-S1d2278-1-1] TestCellbufSpike passes
    print("\n--- AC-S1d2278-1-1: charmbracelet/x/vt spike (TestCellbufSpike) ---")
    run_go_test("TestCellbufSpike", "AC-S1d2278-1-1")

    # [AC-S1d2278-1-2] TestAltSpike passes
    print("\n--- AC-S1d2278-1-2: hinshun/vt10x spike (TestAltSpike) ---")
    run_go_test("TestAltSpike", "AC-S1d2278-1-2")

    # [AC-S1d2278-1-3] Comparison report checks
    print("\n--- AC-S1d2278-1-3: emulator-comparison.md ---")
    print(f"    report path: {REPORT_PATH}")

    if not REPORT_PATH.exists():
        fail(
            "AC-S1d2278-1-3",
            f"emulator-comparison.md does not exist at {REPORT_PATH}",
        )

    size = REPORT_PATH.stat().st_size
    if size < MIN_BYTES:
        fail(
            "AC-S1d2278-1-3",
            f"file is only {size} bytes (minimum {MIN_BYTES})",
        )

    content = REPORT_PATH.read_text(encoding="utf-8")
    missing = [h for h in REQUIRED_HEADINGS if h not in content]
    if missing:
        fail(
            "AC-S1d2278-1-3",
            f"missing required headings: {missing}",
        )

    ok(
        "AC-S1d2278-1-3",
        f"emulator-comparison.md: exists, {size} bytes, all 5 headings present",
    )

    print("\n==> All AC checks passed.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
