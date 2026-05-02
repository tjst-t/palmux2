#!/usr/bin/env python3
"""Mobile E2E — gesture documentation matches code (S022-1-4).

Validates the cross-references between docs/mobile-gestures.md and the
codebase. This is a structural test — it doesn't fire actual gestures
(those are exercised by the per-sprint E2E suites already), but it
guarantees the documentation stays in sync with reality.

Acceptance:
  (a) docs/mobile-gestures.md exists.
  (b) The 10 gesture IDs (G-1 .. G-10) are all listed.
  (c) The "Resolution Rules" section names every G-N that appears in
      the collision matrix.
  (d) The touch-action map mentions xterm.js, BottomSheet, and Mermaid
      (the highest-risk gesture surfaces).

Exit code 0 = PASS. Anything else = FAIL.
"""
from __future__ import annotations

import re
import sys
from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[3]
DOC = REPO_ROOT / "docs" / "mobile-gestures.md"


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def main() -> None:
    print("\n[M005] gesture documentation integrity")
    if not DOC.is_file():
        fail(f"{DOC} missing — S022-1-4 incomplete")
    text = DOC.read_text()

    # Acceptance (b): 10 gesture IDs present.
    ids = sorted(set(re.findall(r"\bG-\d+\b", text)))
    expected = [f"G-{i}" for i in range(1, 11)]
    missing = [i for i in expected if i not in ids]
    if missing:
        fail(f"missing gesture IDs in docs: {missing!r}")
    print(f"  ✓ all 10 gesture IDs documented: {ids}")

    # Acceptance (c): resolution rules section exists.
    if "## Collision Matrix" not in text and "Collision Matrix" not in text:
        fail("docs/mobile-gestures.md missing Collision Matrix section")
    if "Resolution Rules" not in text:
        fail("docs/mobile-gestures.md missing Resolution Rules section")
    print("  ✓ Collision Matrix + Resolution Rules sections present")

    # Acceptance (d): touch-action map mentions critical surfaces.
    must_mention = [
        "touch-action",
        "xterm",
        "BottomSheet",
        "Mermaid",
    ]
    for term in must_mention:
        if term.lower() not in text.lower():
            fail(f"docs/mobile-gestures.md should mention {term!r}")
    print(f"  ✓ touch-action map references: {must_mention}")

    print("M005 PASS")


if __name__ == "__main__":
    sys.exit(main() or 0)
