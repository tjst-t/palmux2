#!/usr/bin/env python3
"""Migrate docs/sprint-logs/{SprintID}/*.md to *.json per SPRINT_LOGS_SCHEMA.json.

Schema-typed files we migrate:
  decisions.md          -> decisions.json
  acceptance-matrix.md  -> acceptance-matrix.json
  e2e-results.md        -> e2e-results.json
  refine.md             -> refine.json
  failures.md           -> failures.json
  gui-spec-*.md         -> gui-spec-*.json

All other .md files (investigation.md, audit.md, etc.) and subdirs are left alone.
"""
from __future__ import annotations

import json
import os
import re
import sys
from pathlib import Path
from typing import Any

ROOT = Path("/home/ubuntu/ghq/github.com/tjst-t/palmux2")
LOGS = ROOT / "docs" / "sprint-logs"

# Default timestamp when none is known.
DEFAULT_TS = "2026-05-01T00:00:00Z"

CATEGORY_MAP = {
    "planning decisions": "planning",
    "planning": "planning",
    "implementation decisions": "implementation",
    "implementation": "implementation",
    "review decisions": "review",
    "review": "review",
    "verify decisions": "verify",
    "verification": "verify",
    "build / verification": "verify",
    "verify": "verify",
    "backlog additions": "backlog",
    "backlog": "backlog",
    "architectural decisions": "implementation",
    "design decisions": "implementation",
    "what was broken": "context",
    "background": "context",
    "context": "context",
    "reported bugs (verbatim)": "context",
    "reported bugs": "context",
    "root cause": "context",
}


SECTION_RE = re.compile(r"^## (.+?)\s*$")
SUBSECTION_RE = re.compile(r"^### (.+?)\s*$")
BULLET_RE = re.compile(r"^- (?:\*\*(?P<title>.+?)\*\*\s*[:：]?\s*)?(?P<rest>.*)$")


def slug_category(heading: str) -> str:
    h = heading.strip().lower()
    if h in CATEGORY_MAP:
        return CATEGORY_MAP[h]
    # heuristic
    if "planning" in h:
        return "planning"
    if "implement" in h or "architect" in h or "design" in h:
        return "implementation"
    if "review" in h:
        return "review"
    if "verif" in h or "build" in h or "e2e" in h:
        return "verify"
    if "backlog" in h:
        return "backlog"
    if "broken" in h or "bug" in h or "background" in h or "context" in h or "root cause" in h:
        return "context"
    if "files touched" in h or "files" in h:
        return "implementation"
    return "general"


def migrate_decisions(path: Path, sprint_id: str) -> dict[str, Any]:
    text = path.read_text(encoding="utf-8")
    lines = text.split("\n")

    decisions: list[dict[str, Any]] = []

    # Walk: track current category from ## sections, current sub-context from ###,
    # collect bullets as decisions.
    current_category = "general"
    current_subcontext: str | None = None
    pending_bullet: dict[str, Any] | None = None

    def flush():
        nonlocal pending_bullet
        if pending_bullet is not None:
            # Trim trailing whitespace
            pending_bullet["detail"] = pending_bullet["detail"].strip()
            decisions.append(pending_bullet)
            pending_bullet = None

    in_code_block = False

    for raw in lines:
        line = raw.rstrip()
        # Track fenced code blocks — inside, do not parse bullets as new decisions
        if line.lstrip().startswith("```"):
            in_code_block = not in_code_block
            if pending_bullet is not None:
                pending_bullet["detail"] += "\n" + line
            continue
        if in_code_block:
            if pending_bullet is not None:
                pending_bullet["detail"] += "\n" + line
            continue

        m_section = SECTION_RE.match(line)
        if m_section:
            flush()
            heading = m_section.group(1).strip()
            current_category = slug_category(heading)
            current_subcontext = None
            continue
        m_sub = SUBSECTION_RE.match(line)
        if m_sub:
            flush()
            current_subcontext = m_sub.group(1).strip()
            continue

        # Bullet at top level (column 0)
        if line.startswith("- "):
            flush()
            m_b = BULLET_RE.match(line)
            if m_b:
                title = (m_b.group("title") or "").strip()
                rest = m_b.group("rest").strip()
                if not title:
                    # untitled bullet — first sentence as title
                    if rest:
                        title = rest.split("。")[0].split(". ")[0][:80].strip()
                pending_bullet = {
                    "timestamp": DEFAULT_TS,
                    "category": current_category,
                    "title": title or "(untitled)",
                    "detail": rest,
                }
                if current_subcontext:
                    pending_bullet["context"] = current_subcontext
            continue

        # Indented continuation (e.g., "  blah") of current bullet
        if pending_bullet is not None and (line.startswith("  ") or line == ""):
            content = line.strip()
            if content:
                if pending_bullet["detail"]:
                    pending_bullet["detail"] += " " + content
                else:
                    pending_bullet["detail"] = content
            continue

        # Otherwise non-indented prose at section level — finalize bullet
        if pending_bullet is not None:
            flush()
        # paragraph prose between bullets is dropped (decisions schema is bullet-shaped)

    flush()

    return {
        "sprint": sprint_id,
        "decisions": decisions,
    }


def migrate_refine(path: Path, sprint_id: str) -> dict[str, Any]:
    """refine.md is more free-form. We extract bullet items from "What was broken" or
    "Architectural decisions" sections and treat them as refinements where possible.
    Schema: refinements: [{id, feedback, change, files, tests_rerun, tests_passed}]
    Since the actual MD does not strictly follow this shape, we do best-effort:
    each top-level bullet under a numbered "1. ..." style or "### N." subsection
    becomes a refinement entry.
    """
    text = path.read_text(encoding="utf-8")
    lines = text.split("\n")

    refinements: list[dict[str, Any]] = []
    counter = 0

    # Try strategy: look for "### 1. ..."-style subsections and treat them as
    # individual refinements. Fall back to top-level bullets.
    sub_re = re.compile(r"^### (?P<num>\d+)\.\s*(?P<rest>.+)\s*$")

    current_ref: dict[str, Any] | None = None
    in_code = False

    def flush_ref():
        nonlocal current_ref
        if current_ref is not None:
            current_ref["change"] = current_ref["change"].strip()
            refinements.append(current_ref)
            current_ref = None

    for raw in lines:
        line = raw.rstrip()
        if line.lstrip().startswith("```"):
            in_code = not in_code
            if current_ref is not None:
                current_ref["change"] += "\n" + line
            continue
        if in_code and current_ref is not None:
            current_ref["change"] += "\n" + line
            continue

        m_sub = sub_re.match(line)
        if m_sub:
            flush_ref()
            counter += 1
            current_ref = {
                "id": int(m_sub.group("num")),
                "feedback": "",
                "change": "",
                "files": [],
                "tests_rerun": [],
                "tests_passed": None,
                "title": m_sub.group("rest").strip(),
            }
            continue

        if current_ref is not None:
            content = line.strip()
            if content:
                if current_ref["change"]:
                    current_ref["change"] += " " + content
                else:
                    current_ref["change"] = content

    flush_ref()

    if not refinements:
        # Fallback: treat top-level bullets like decisions but in refinements shape
        for raw in lines:
            line = raw.strip()
            if line.startswith("- "):
                m_b = BULLET_RE.match(line)
                if m_b:
                    counter += 1
                    title = (m_b.group("title") or "").strip()
                    rest = m_b.group("rest").strip()
                    refinements.append(
                        {
                            "id": counter,
                            "feedback": title,
                            "change": rest,
                            "files": [],
                            "tests_rerun": [],
                            "tests_passed": None,
                        }
                    )

    return {
        "sprint": sprint_id,
        "refinements": refinements,
    }


def migrate_e2e_results(path: Path, sprint_id: str) -> dict[str, Any]:
    """e2e-results.md is typically a small flat log block. We capture it as one
    summary test entry plus the raw log in a structured form.
    """
    text = path.read_text(encoding="utf-8")
    lines = [ln.rstrip() for ln in text.split("\n") if ln.strip()]

    tests: list[dict[str, Any]] = []
    pass_count = 0
    fail_count = 0
    total = 0
    overall_pass = True
    server_command = ""
    run_at = DEFAULT_TS

    # Detect lines with "[a]", "[b]", etc. or "PASS:" / "FAIL:" prefixes
    for ln in lines:
        m = re.match(r"^\s*\[(?P<id>[^\]]+)\]\s*(?P<desc>.+)$", ln)
        if m:
            total += 1
            tests.append(
                {
                    "name": f"[{m.group('id')}] {m.group('desc').strip()}",
                    "file": "",
                    "status": "pass",
                    "duration_ms": None,
                    "error": None,
                }
            )
            pass_count += 1
            continue
        m2 = re.match(r"^\s*(PASS|FAIL):\s*(.+)$", ln)
        if m2:
            total += 1
            status = "pass" if m2.group(1) == "PASS" else "fail"
            tests.append(
                {
                    "name": m2.group(2).strip(),
                    "file": "",
                    "status": status,
                    "duration_ms": None,
                    "error": None,
                }
            )
            if status == "pass":
                pass_count += 1
            else:
                fail_count += 1
                overall_pass = False
            continue
        if ln.startswith("S") and ":" in ln and "PASS" in ln:
            # final summary "Sxxx E2E: PASS"
            continue
        # capture server / port hints
        m3 = re.search(r"localhost:(\d+)", ln)
        if m3 and not server_command:
            server_command = f"http://localhost:{m3.group(1)}"

    return {
        "sprint": sprint_id,
        "run_at": run_at,
        "server_command": server_command,
        "summary": {
            "total": total,
            "pass": pass_count,
            "fail": fail_count,
            "skip": 0,
        },
        "tests": tests,
        "raw_log": text,
    }


def write_json(target: Path, data: dict[str, Any]) -> None:
    target.write_text(json.dumps(data, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")


def backup_md(md_path: Path) -> None:
    bak = md_path.with_suffix(md_path.suffix + ".bak")
    md_path.rename(bak)


SCHEMA_TYPED_NAMES = {
    "decisions.md",
    "acceptance-matrix.md",
    "e2e-results.md",
    "refine.md",
    "failures.md",
}


def is_gui_spec(name: str) -> bool:
    return name.startswith("gui-spec-") and name.endswith(".md")


def main() -> int:
    migrated = 0
    skipped: list[str] = []
    for sprint_dir in sorted(LOGS.iterdir()):
        if not sprint_dir.is_dir():
            continue
        sprint_id = sprint_dir.name
        for entry in sorted(sprint_dir.iterdir()):
            if not entry.is_file():
                continue
            name = entry.name
            if name.endswith(".md"):
                if name in SCHEMA_TYPED_NAMES or is_gui_spec(name):
                    if name == "decisions.md":
                        data = migrate_decisions(entry, sprint_id)
                    elif name == "refine.md":
                        data = migrate_refine(entry, sprint_id)
                    elif name == "e2e-results.md":
                        data = migrate_e2e_results(entry, sprint_id)
                    elif name == "failures.md":
                        # treat similarly to decisions but tagged as failures
                        d = migrate_decisions(entry, sprint_id)
                        data = {
                            "sprint": sprint_id,
                            "failures": [
                                {
                                    "story": None,
                                    "type": "general",
                                    "summary": dec["title"],
                                    "attempts": [
                                        {"approach": dec["title"], "result": dec["detail"]}
                                    ],
                                    "resolution": None,
                                }
                                for dec in d["decisions"]
                            ],
                        }
                    elif name == "acceptance-matrix.md":
                        data = {"sprint": sprint_id, "matrix": {}}
                    elif is_gui_spec(name):
                        data = {
                            "sprint": sprint_id,
                            "story": name.removeprefix("gui-spec-").removesuffix(".md"),
                            "raw_md": entry.read_text(encoding="utf-8"),
                        }
                    else:
                        continue

                    target = entry.with_suffix(".json")
                    write_json(target, data)
                    backup_md(entry)
                    migrated += 1
                    print(f"  migrated {entry.relative_to(ROOT)} -> {target.name}")
                else:
                    skipped.append(str(entry.relative_to(ROOT)))
    print(f"\n{migrated} files migrated.")
    if skipped:
        print(f"\n{len(skipped)} schema-external .md files left untouched:")
        for s in skipped:
            print(f"  - {s}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
