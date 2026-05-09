#!/usr/bin/env python3
"""[AC-S43cfb1-1-8] Block-renderer kitchen-sink smoke test.

The Claude tab dispatches each Block kind to a per-kind renderer in
`frontend/src/tabs/claude-agent/blocks/`. S43cfb1-1 split this into a
flat dispatcher so every renderer ships independently. This test
verifies the dispatcher produces a non-empty render for ALL 11 known
block kinds (text, thinking, tool_use, tool_result, plan, permission,
ask, todo, hook, task-tree, compact) by driving the test harness's
`?blocks=all` mode and asserting:

  1. Each block kind shows up under a `data-testid="block-{kind}-..."`
     wrapper (the harness assigns these in `renderBlockWithTestId`).
  2. Each block's main text content matches the harness payload — the
     "kitchen-sink-{kind}-payload" sentinel string we feed in.

The renderers we expect to find:
  text, thinking, tool_use, tool_result, plan, permission, ask, todo,
  hook, task-tree, compact

`task-tree` is a wrapper renderer that the dispatcher chooses when a
`tool_use` block carries `name === 'Task'` AND the parent supplies a
`renderTaskChildren` callback (the harness does so in `blocks=all` mode).

Exit code 0 = PASS.
"""
from __future__ import annotations

import os
import sys

PORT = (
    os.environ.get("PALMUX2_DEV_PORT_OVERRIDE")
    or os.environ.get("PALMUX2_DEV_PORT")
    or "8203"
)
BASE_URL = f"http://localhost:{PORT}"

# Each entry: (kind, sentinel_substring) — sentinel must appear in the
# rendered DOM under the block wrapper. Sentinels are payloads the
# harness feeds the synthetic Turn[] (see `syntheticAllBlocksTurns`).
EXPECTED: list[tuple[str, str]] = [
    ("text", "kitchen-sink-text-payload"),
    # ThinkingBlock collapses to a "Thought ..." summary by default.
    # The summary is derived from the raw text via `summary()` so the
    # sentinel substring still appears in the DOM (truncated only for
    # very long text).
    ("thinking", "kitchen-sink-thinking-payload"),
    # ToolUseBlock — the Bash tool input renders the command in a
    # generic input panel. The header summary (toolSummary) extracts
    # `command` so the sentinel shows in either the collapsed or
    # expanded view.
    ("tool_use", "kitchen-sink-tooluse-payload"),
    ("tool_result", "kitchen-sink-toolresult-payload"),
    ("plan", "kitchen-sink-plan-payload"),
    # PermissionBlock renders the toolName + serialised input. Our
    # input includes the sentinel as a Bash command argument.
    ("permission", "kitchen-sink-permission-payload"),
    # AskQuestionBlock renders `question` text inline.
    ("ask", "kitchen-sink-ask-payload"),
    # TodoBlock renders the first todo's `content`.
    ("todo", "kitchen-sink-todo-payload"),
    # HookBlock renders stdout in the expanded body (default-expanded
    # when running, but our hook is finished — so we look for the
    # description in the meta block which always shows).
    ("hook", "PreToolUse"),
    # TaskTreeBlock — Task tool input renders description; the child
    # sub-agent turn renders separately under a nested wrapper, but
    # the outer block-task-tree wrapper covers both. We look for the
    # description sentinel.
    ("task-tree", "kitchen-sink-task-payload"),
    ("compact", "Compacted"),
]


def fail(msg: str) -> None:
    print(f"FAIL: {msg}", file=sys.stderr)
    sys.exit(1)


def ok(name: str, msg: str = "") -> None:
    print(f"  [{name}] {msg or 'OK'}")


def main() -> int:
    try:
        from playwright.sync_api import sync_playwright
    except ImportError:
        fail("playwright not installed — run `pip install playwright && playwright install chromium`")

    print(f"S43cfb1-1-8 block-render E2E against {BASE_URL}")
    with sync_playwright() as p:
        browser = p.chromium.launch(headless=True)
        ctx = browser.new_context(viewport={"width": 1024, "height": 1400})
        page = ctx.new_page()
        page_errors: list[str] = []
        page.on("pageerror", lambda e: page_errors.append(str(e)))

        page.goto(f"{BASE_URL}/__test/claude?blocks=all", wait_until="networkidle")
        page.wait_for_selector('[data-testid="harness-conversation"]', timeout=5000)
        # ConversationList virtualises rows. With only 11 turns + the
        # default tall viewport, all rows should mount, but give it a
        # short beat for measurement to settle.
        page.wait_for_timeout(400)

        # 1) Each block kind has a wrapper.
        for kind, sentinel in EXPECTED:
            sel = f'[data-testid^="block-{kind}-"]'
            count = page.locator(sel).count()
            if count < 1:
                # Dump the harness DOM for diagnostics.
                html = page.locator('[data-testid="harness-conversation"]').inner_html()
                preview = html[:2000]
                fail(
                    f"block kind {kind!r} not found in DOM (selector {sel!r}). "
                    f"DOM preview: {preview!r}"
                )
            ok(f"render-{kind}", f"{count} wrapper(s) found")

            # 2) Sentinel text appears under that wrapper.
            wrapper = page.locator(sel).first
            text = wrapper.inner_text()
            if sentinel not in text:
                # Try `text_content` (handles aria-hidden quirks) before
                # giving up.
                text2 = wrapper.text_content() or ""
                if sentinel not in text2:
                    fail(
                        f"block kind {kind!r} rendered but sentinel {sentinel!r} "
                        f"not present. text={text!r}"
                    )
            ok(f"sentinel-{kind}", f"{sentinel!r} present")

        if page_errors:
            fail(f"page error(s) raised during test: {page_errors!r}")

        ctx.close()
        browser.close()

    print("\nALL OK")
    return 0


if __name__ == "__main__":
    sys.exit(main())
