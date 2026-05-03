#!/usr/bin/env python3
"""Migrate docs/ROADMAP.md to docs/ROADMAP.json following ROADMAP_SCHEMA.json."""
from __future__ import annotations

import json
import re
import sys
from pathlib import Path

ROOT = Path("/home/ubuntu/ghq/github.com/tjst-t/palmux2")
SRC = ROOT / "docs" / "ROADMAP.md"
DST = ROOT / "docs" / "ROADMAP.json"


# Sprint status mapping from header markers (content INSIDE [...])
def parse_sprint_status(marker: str) -> str:
    m = marker.strip().lower()
    # markers we see in ROADMAP.md:
    #   DONE, DONE / refined, x, (space), IN PROGRESS, BLOCKED
    if "done" in m or m == "x":
        return "done"
    if "in progress" in m or "wip" in m:
        return "in_progress"
    if "blocked" in m:
        return "blocked"
    return "pending"


def parse_story_status(marker: str) -> str:
    m = marker.strip().lower()
    if "done" in m or m == "x":
        return "done"
    if "blocked" in m:
        return "blocked"
    return "pending"


def parse_task_status(marker: str) -> str:
    # marker comes in as "[x]" or "[ ]"
    return "done" if marker.strip().lower() == "[x]" else "pending"


def parse_ac_status(marker: str) -> str:
    return "pass" if marker.strip().lower() == "[x]" else "pending"


HEADER_SPRINT_RE = re.compile(r"^## スプリント\s+(?P<id>S[0-9A-Za-z\-]+):\s*(?P<title>.+?)\s*\[(?P<status>[^\]]+)\]\s*$")
HEADER_STORY_RE = re.compile(r"^### ストーリー\s+(?P<id>S[0-9A-Za-z\-]+-\d+):\s*(?P<title>.+?)\s*\[(?P<status>[^\]]+)\]\s*$")
TASK_RE = re.compile(r"^- (?P<mark>\[[ xX]\])\s+\*\*タスク\s+(?P<id>S[0-9A-Za-z\-]+-\d+-\d+)\*\*:\s*(?P<rest>.*)$")
AC_RE = re.compile(r"^- (?P<mark>\[[ xX]\])\s+(?P<text>.+)$")


def main() -> int:
    content = SRC.read_text(encoding="utf-8")
    lines = content.split("\n")

    project_title = "Palmux v2"
    description = ""
    # Capture project description from first paragraph after H1
    for ln in lines:
        if ln.startswith("# "):
            project_title = ln[2:].strip().split(":")[0].strip().split("—")[0].strip() or project_title
            project_title = "Palmux v2"
            break

    # Parse top-level "## 進捗" block
    progress = {
        "current_sprint": None,
        "total": 0,
        "done": 0,
        "in_progress": 0,
        "remaining": 0,
        "percentage": 0,
    }
    for i, ln in enumerate(lines):
        if ln.strip() == "## 進捗":
            for j in range(i, min(i + 10, len(lines))):
                m = re.search(
                    r"合計:\s*(\d+)\s*スプリント\s*\|\s*完了:\s*(\d+)\s*\|\s*進行中:\s*(\d+)\s*\|\s*残り:\s*(\d+)",
                    lines[j],
                )
                if m:
                    total = int(m.group(1))
                    done = int(m.group(2))
                    in_progress = int(m.group(3))
                    remaining = int(m.group(4))
                    progress["total"] = total
                    progress["done"] = done
                    progress["in_progress"] = in_progress
                    progress["remaining"] = remaining
                    progress["percentage"] = round(100 * done / total) if total else 0
                    break
            break

    # Parse "## 実行順序" line
    execution_order: list[str] = []
    for i, ln in enumerate(lines):
        if ln.strip() == "## 実行順序":
            for j in range(i + 1, min(i + 6, len(lines))):
                line = lines[j].strip()
                if not line:
                    continue
                # Collect S-IDs from arrow-separated format
                ids = re.findall(r"S\d+(?:-fix-\d+)?", line)
                # Need to dedupe while preserving order
                seen = set()
                for sid in ids:
                    if sid not in seen:
                        seen.add(sid)
                        execution_order.append(sid)
                if execution_order:
                    break
            break

    # Determine current_sprint from first non-done sprint in execution_order
    # (we'll set this after parsing sprints)

    # Slice document into sprint sections
    sprint_indices: list[tuple[int, str, str, str]] = []
    for i, ln in enumerate(lines):
        m = HEADER_SPRINT_RE.match(ln)
        if m:
            sprint_indices.append((i, m.group("id"), m.group("title"), m.group("status")))
    # Find boundaries (end at "## 依存関係" or "## バックログ" or next "## ")
    end_indices: list[int] = []
    for i, ln in enumerate(lines):
        if ln.startswith("## ") and not ln.startswith("## スプリント"):
            end_indices.append(i)

    sprints: dict[str, dict] = {}

    def find_section_end(start: int) -> int:
        # The end is the next "## " heading after start
        for k in range(start + 1, len(lines)):
            if lines[k].startswith("## "):
                return k
        return len(lines)

    for idx, (line_i, sprint_id, title, status_marker) in enumerate(sprint_indices):
        end_i = find_section_end(line_i)
        section = lines[line_i:end_i]

        # Parse description: paragraphs between sprint header and first ###
        description_lines: list[str] = []
        story_start = None
        for k, sl in enumerate(section[1:], start=1):
            if sl.startswith("### "):
                story_start = k
                break
            description_lines.append(sl)
        # strip leading/trailing blanks and quote lines (status notes)
        sprint_desc = "\n".join(description_lines).strip()

        # Locate stories within section
        story_indices = []
        for k, sl in enumerate(section):
            m = HEADER_STORY_RE.match(sl)
            if m:
                story_indices.append((k, m.group("id"), m.group("title"), m.group("status")))

        stories: dict[str, dict] = {}

        for sidx, (sline_i, story_id, story_title, story_status_marker) in enumerate(story_indices):
            send_i = (
                story_indices[sidx + 1][0] if sidx + 1 < len(story_indices) else len(section)
            )
            ssection = section[sline_i:send_i]

            # Walk the section to find ユーザーストーリー / 受け入れ条件 / タスク blocks
            user_story = ""
            acceptance_criteria: list[dict] = []
            tasks: dict[str, dict] = {}

            mode = None  # "user_story" | "ac" | "tasks"
            ac_index = 0
            for line in ssection[1:]:  # skip header line
                stripped = line.strip()
                if stripped.startswith("**ユーザーストーリー") or stripped.startswith("**ユーザストーリー"):
                    mode = "user_story_pending"
                    continue
                if stripped.startswith("**受け入れ条件"):
                    mode = "ac"
                    continue
                if stripped.startswith("**タスク:") or stripped == "**タスク**":
                    mode = "tasks"
                    continue
                # Sub-headings inside AC / タスク blocks (e.g., **Rendering:**, **バックエンド:**, **E2E:**)
                # are NOT mode-resetters — they're sub-category labels. We keep current mode.
                # Only reset on top-level non-tracked sections like **設計の中核**, **背景**, **状況**, **方針**.
                if stripped.startswith("**設計の中核") or stripped.startswith("**背景") or stripped.startswith("**状況") or stripped.startswith("**方針") or stripped.startswith("**前提"):
                    mode = "other"
                    continue

                if mode == "user_story_pending":
                    # collect lines until next ** heading
                    if stripped:
                        if user_story:
                            user_story += "\n" + stripped
                        else:
                            user_story = stripped
                elif mode == "ac":
                    m_task = TASK_RE.match(stripped)
                    if m_task:
                        # never mind, this is a task — skip
                        continue
                    m_ac = AC_RE.match(stripped)
                    if m_ac:
                        ac_index += 1
                        acceptance_criteria.append(
                            {
                                "id": f"AC-{story_id}-{ac_index}",
                                "description": m_ac.group("text").strip(),
                                "test": "",
                                "status": parse_ac_status(m_ac.group("mark")),
                            }
                        )
                elif mode == "tasks":
                    m_task = TASK_RE.match(stripped)
                    if m_task:
                        rest = m_task.group("rest").strip()
                        # split on em-dash for title vs description
                        if " — " in rest:
                            t_title, _, t_desc = rest.partition(" — ")
                        elif "—" in rest:
                            t_title, _, t_desc = rest.partition("—")
                        else:
                            t_title, t_desc = rest, ""
                        tasks[m_task.group("id")] = {
                            "title": t_title.strip(),
                            "description": t_desc.strip(),
                            "status": parse_task_status(m_task.group("mark").lower()),
                        }
                # other modes / outside any block: ignore

            stories[story_id] = {
                "title": story_title.strip(),
                "status": parse_story_status(story_status_marker),
                "user_story": user_story.strip(),
                "acceptance_criteria": acceptance_criteria,
                "tasks": tasks,
            }

        sprints[sprint_id] = {
            "title": title.strip(),
            "status": parse_sprint_status(status_marker),
            "description": sprint_desc,
            "milestone": False,
            "stories": stories,
        }

    # Set current_sprint = first non-done in execution_order
    current_sprint = None
    for sid in execution_order:
        s = sprints.get(sid)
        if s and s["status"] != "done":
            current_sprint = sid
            break
    progress["current_sprint"] = current_sprint

    # Re-derive progress totals from sprint statuses for accuracy
    derived_total = len(sprints)
    derived_done = sum(1 for s in sprints.values() if s["status"] == "done")
    derived_in_progress = sum(1 for s in sprints.values() if s["status"] == "in_progress")
    derived_remaining = derived_total - derived_done - derived_in_progress
    # If document said different numbers, prefer derived; warn via stderr
    if progress["total"] and progress["total"] != derived_total:
        print(
            f"warn: progress total in doc ({progress['total']}) differs from derived ({derived_total}); using derived",
            file=sys.stderr,
        )
    progress["total"] = derived_total
    progress["done"] = derived_done
    progress["in_progress"] = derived_in_progress
    progress["remaining"] = derived_remaining
    progress["percentage"] = round(100 * derived_done / derived_total) if derived_total else 0

    # Parse 依存関係 section
    dependencies: dict[str, dict] = {}
    dep_start = None
    backlog_start = None
    for i, ln in enumerate(lines):
        if ln.strip() == "## 依存関係":
            dep_start = i
        if ln.strip() == "## バックログ":
            backlog_start = i
    if dep_start is not None:
        end = backlog_start if backlog_start is not None else len(lines)
        dep_section = lines[dep_start + 1:end]
        for ln in dep_section:
            ln = ln.rstrip()
            m = re.match(r"^- (S\d+(?:-fix-\d+)?)\s*(?:\([^)]*\))?\s*(?:は|—|\-)?\s*(.*)$", ln)
            if m:
                sid = m.group(1)
                text = m.group(2).strip()
                if not text:
                    text = ln[2:].strip()
                # extract referenced sprint IDs from text
                refs = re.findall(r"S\d+(?:-fix-\d+)?", text)
                refs = [r for r in refs if r != sid]
                # dedupe preserve order
                seen = set()
                deduped = []
                for r in refs:
                    if r not in seen:
                        seen.add(r)
                        deduped.append(r)
                dependencies[sid] = {
                    "depends_on": deduped,
                    "reason": text,
                }

    # Parse バックログ section
    backlog: list[dict] = []
    if backlog_start is not None:
        bl = lines[backlog_start + 1:]
        # Each backlog entry begins with "- [x]" or "- [ ]"
        # Multi-line entries continue with indented text or unindented continuation lines
        current = None
        for ln in bl:
            if ln.startswith("- [x]") or ln.startswith("- [ ]"):
                # finalize previous
                if current is not None:
                    backlog.append(current)
                done_marker = ln.startswith("- [x]")
                rest = ln[5:].strip()
                # skip done items (struck out / completed)
                if done_marker:
                    current = None
                    continue
                # extract title (bold) + remainder
                m = re.match(r"^\*\*(?P<title>.+?)\*\*\s*(?P<tail>.*)$", rest)
                if m:
                    title = m.group("title").strip()
                    tail = m.group("tail").strip()
                else:
                    # title might be plain
                    title = rest
                    tail = ""
                current = {
                    "title": title,
                    "description": tail,
                    "added_in": None,
                    "reason": None,
                }
            elif current is not None:
                # continuation line (description)
                line_strip = ln.strip()
                if not line_strip:
                    continue
                if current["description"]:
                    current["description"] += " " + line_strip
                else:
                    current["description"] = line_strip
        if current is not None:
            backlog.append(current)

    out = {
        "project": project_title,
        "description": "Web ベースのターミナルクライアント。tmux セッションをブラウザから操作する。",
        "progress": progress,
        "execution_order": execution_order,
        "sprints": sprints,
        "dependencies": dependencies,
        "backlog": backlog,
    }

    DST.write_text(json.dumps(out, ensure_ascii=False, indent=2) + "\n", encoding="utf-8")
    print(f"wrote {DST} ({len(sprints)} sprints, {len(execution_order)} in execution_order, {len(backlog)} backlog)")
    return 0


if __name__ == "__main__":
    sys.exit(main())
