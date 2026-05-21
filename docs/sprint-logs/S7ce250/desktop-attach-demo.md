# desktop-attach-demo.md — S7ce250-5 Manual Smoke Log

**Status**: Pre-populated by sprint-auto (Story 5). Steps marked
`pending-manual` require a human to run the demo against a real `claude`
binary and record actual observations.

## Introduction

Sprint S7ce250 migrated the PTY daemon from the throwaway PoC
(`cmd/poc-pty/`) into the production palmux2 tab system
(`internal/tab/claudetui/`).  This document captures the manual smoke
scenarios that confirm the full interactive experience — visual rendering,
reconnect fidelity, session persistence — that automated tests cannot
cover because they use `/bin/cat` as a deterministic claude substitute.

The equivalent automated coverage is in
`tests/e2e/s7ce250_claude_tui.py` (hermetic, always runs in CI).

---

## Scenario 1: first attach — claude TUI starts and renders

**Steps**

1. Start the dev instance: `make serve INSTANCE=dev`
2. Open palmux2 in Chrome: `http://localhost:<port>/`
3. Open any repository in the Drawer.
4. Click the **Claude (TUI)** tab in the tab bar.
5. Observe the `data-testid=claude-tui-status` badge.
6. Within 1–2 seconds the badge should read `connected` and the xterm.js
   terminal should display the claude TUI splash screen.

**pending-manual**: Record the actual time-to-connected and confirm the
claude TUI rendered without visual artefacts (garbled ANSI sequences,
missing colours, etc.).

---

## Scenario 2: send a prompt and receive a streamed response

**Steps**

1. With the Claude (TUI) tab open and `status=connected`, click the
   terminal and type a short prompt, e.g. `What is 2 + 2?`, then press
   Enter.
2. The status badge should transition `connected → streaming` while bytes
   arrive.
3. The response should stream character-by-character in the xterm.js canvas.
4. After the response completes the badge should settle back to `connected`.

**pending-manual**: Confirm that streaming is smooth, the status badge
transitions correctly, and the response is coherent (no dropped bytes or
duplicate rendering).

---

## Scenario 3: WS disconnect and reconnect — ring buffer replay

**Steps**

1. With a session active and visible output in the terminal, open browser
   DevTools → Network → WS frames.
2. Close the WS connection manually (or click the **Reconnect** button if
   the connection drops).
3. Upon reconnect, the daemon should replay the ring buffer: the terminal
   should immediately re-display the previous output without re-running
   claude.
4. The `claude` subprocess should remain alive (session is not restarted).

**pending-manual**: Confirm replay fidelity — all ANSI sequences
(colours, cursor position) appear correctly after replay.

---

## Scenario 4: terminal resize — SIGWINCH propagation

**Steps**

1. With the terminal open and a wide prompt visible, resize the browser
   window to a narrow viewport.
2. The terminal should reflow within ~200 ms (SIGWINCH debounce).
3. The claude TUI should re-render at the new column width.
4. Verify via `POST /api/repos/{repoId}/branches/{branchId}/tabs/claude-tui/resize`
   that the daemon accepts `{"cols": <new>, "rows": <new>}` with HTTP 204.

**pending-manual**: Confirm visual reflow matches the new terminal size
and no output is lost during the resize.

---

## Scenario 5: session persistence across server restart

**Steps**

1. Start a claude session (the session ID is auto-detected from
   `~/.claude/projects/`).
2. Confirm `GET .../tabs/claude-tui/stats` shows `state=running` and
   `pid > 0`.
3. Stop the server: `make serve-stop INSTANCE=dev`.
4. Restart: `make serve INSTANCE=dev`.
5. Re-open the Claude (TUI) tab; the daemon should re-spawn with
   `claude --resume <last-session-id>` automatically.

**pending-manual**: Confirm the session is resumed rather than a fresh
session started.  The claude TUI should show the previous conversation
context.

---

## Automated verification summary

| AC | Test function | Status |
|---|---|---|
| 5-1 tab appears | `test_ac_s7ce250_5_1_tab_appears_in_list` | automated |
| 5-1 browser label | `test_ac_s7ce250_5_1_browser_tab_label` | automated |
| 5-2 WS attach starts daemon | `test_ac_s7ce250_5_2_ws_attach_starts_daemon` | automated |
| 5-3 echo round-trip | `test_ac_s7ce250_5_3_input_echoed_back` | automated |
| 5-4 resize POST 204 | `test_ac_s7ce250_5_4_resize_accepted` | automated |
| 5-5 branch close shuts down | `test_ac_s7ce250_5_5_branch_close_shuts_down_daemon` | automated |
| 5-2 smoke log present | `test_ac_s7ce250_5_smoke_log_present` | automated |
