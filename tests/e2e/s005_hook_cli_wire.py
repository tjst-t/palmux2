#!/usr/bin/env python3
"""Sprint S005 — live-CLI wire-format validation.

Spins up `claude --include-hook-events --output-format=stream-json` in a
disposable temp cwd (its own .claude/settings.json registers a benign
PreToolUse hook), sends one user message that forces a Bash invocation,
and asserts the resulting stream contains:

    1. A `system/hook_started` envelope with hook_id + hook_event="PreToolUse"
    2. A matching `system/hook_response` envelope with stdout="HOOK_PROBE\\n",
       exit_code=0, outcome="success"

This is the contract that internal/tab/claudeagent/normalize.go's
processHookStarted / processHookResponse paths translate into kind:"hook"
blocks, so a CLI release that breaks the wire shape will fail this test
loudly rather than degrading the UI silently.

No palmux server is involved. Exit 0 = PASS.
"""

from __future__ import annotations

import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def passed(msg: str) -> None:
    print(f"PASS: {msg}")


def main() -> None:
    print("==> S005 live-CLI wire-format check")
    with tempfile.TemporaryDirectory(prefix="s005-cli-probe-") as cwd:
        claude_dir = Path(cwd) / ".claude"
        claude_dir.mkdir()
        (claude_dir / "settings.json").write_text(
            json.dumps(
                {
                    "hooks": {
                        "PreToolUse": [
                            {
                                "matcher": "Bash",
                                "hooks": [
                                    {"type": "command", "command": "echo HOOK_PROBE"}
                                ],
                            }
                        ]
                    }
                }
            )
        )
        # When --print is on with --output-format=stream-json, the CLI
        # accepts a positional prompt argument (plain text) — no need for
        # JSON wrapping on stdin.
        cmd = [
            "claude",
            "--output-format",
            "stream-json",
            "--include-partial-messages",
            "--include-hook-events",
            "--verbose",
            "--permission-mode",
            "bypassPermissions",
            "--setting-sources",
            "project",
            "--print",
            "Run this in the shell: ls /",
        ]
        proc = subprocess.run(
            cmd,
            capture_output=True,
            text=True,
            cwd=cwd,
            timeout=180,
        )
        if proc.returncode != 0:
            print(proc.stderr, file=sys.stderr)
            fail(f"claude exited {proc.returncode}")
        events = []
        for line in proc.stdout.splitlines():
            line = line.strip()
            if not line:
                continue
            try:
                events.append(json.loads(line))
            except json.JSONDecodeError:
                continue

        started = [
            e for e in events
            if e.get("type") == "system"
            and e.get("subtype") == "hook_started"
        ]
        responses = [
            e for e in events
            if e.get("type") == "system"
            and e.get("subtype") == "hook_response"
        ]
        if not started:
            fail(f"no hook_started envelope in {len(events)} events")
        passed(f"saw {len(started)} hook_started envelope(s)")
        if not responses:
            fail("no hook_response envelope")
        passed(f"saw {len(responses)} hook_response envelope(s)")

        # 1) Started carries hook_id + hook_event.
        s0 = started[0]
        for k in ("hook_id", "hook_event", "hook_name"):
            if not s0.get(k):
                fail(f"hook_started missing {k!r}: {s0}")
        passed(
            f"hook_started shape OK (hook_event={s0.get('hook_event')!r}, "
            f"hook_name={s0.get('hook_name')!r})"
        )

        # 2) Find a matching response by hook_id.
        matched = next(
            (r for r in responses if r.get("hook_id") == s0.get("hook_id")),
            None,
        )
        if matched is None:
            fail(f"no hook_response with hook_id={s0.get('hook_id')!r}")
        for k in ("stdout", "exit_code", "outcome", "hook_event"):
            if k not in matched:
                fail(f"hook_response missing {k!r}: {matched}")
        if matched.get("stdout") != "HOOK_PROBE\n":
            fail(f"stdout mismatch: {matched.get('stdout')!r}")
        if matched.get("exit_code") != 0:
            fail(f"exit_code != 0: {matched.get('exit_code')!r}")
        if matched.get("outcome") != "success":
            fail(f"outcome != 'success': {matched.get('outcome')!r}")
        passed(
            f"hook_response shape OK "
            f"(stdout={matched['stdout']!r}, exit_code={matched['exit_code']}, "
            f"outcome={matched['outcome']!r})"
        )

    print("==> S005 live-CLI wire-format PASSED")


if __name__ == "__main__":
    try:
        main()
    except KeyboardInterrupt:
        sys.exit(130)
