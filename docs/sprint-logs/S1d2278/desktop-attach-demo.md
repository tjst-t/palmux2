# desktop-attach-demo.md — S1d2278-3 Manual Smoke Log

**Status**: Pre-populated by sprint-auto (Story 3). Sections marked "to be confirmed manually" require a human to run the PoC against the real `claude` binary and fill in actual observations.

## scenario-1: connect → echo prompt → see response

**Expected flow (to be confirmed manually)**

1. Build the binary: `go build -o /tmp/poc-pty ./cmd/poc-pty`
2. Run against real claude: `/tmp/poc-pty --port 9333 --claude-bin claude`
3. Open `http://localhost:9333/` in Chrome or Firefox.
4. The `pty-poc-status` badge should transition `connecting → connected` within 1–2 seconds as the WS handshake completes and the claude TUI initialises.
5. Type a prompt (e.g., `What is 2 + 2?`) and press Enter. The xterm.js canvas should stream claude's response in real time.
6. The status badge should briefly switch to `streaming` while bytes arrive and settle back to `connected` after ~1 s of inactivity.

**Automated verification (deterministic substitute)**

In CI and sprint-auto E2E the daemon is started with `--claude-bin /bin/bash --claude-args "-c cat"`. Sending `echo hello-from-poc\n` over the WS receives the echoed bytes within 5 s. This exercises the bidirectional PTY path and ring-buffer wiring without depending on real claude availability.

## scenario-2: reconnect with replay

**Expected flow (to be confirmed manually)**

1. With the daemon running and the page open, type a recognisable marker string (e.g., `RECONNECT-TEST-001`).
2. Click the browser Refresh button or click the **Reconnect** button on the page to simulate a WS disconnect.
3. Upon reconnect, the daemon replays the entire ring buffer to the new WS client. The xterm.js canvas should immediately re-display the previous output including the marker, without re-sending anything to the PTY subprocess.
4. Observe that the claude session itself was not restarted — it continues from where it was left.

**Automated verification**

AC-3-3 (`test_ac_s1d2278_3_3_replay_on_reconnect`) automates this: sends a unique marker, verifies echo, disconnects, reconnects, asserts the marker appears in the replayed ring bytes.

## scenario-3: observed limitations / rough edges

The following limitations are known at the time of Story 3 implementation. They are intentional PoC trade-offs, not bugs to fix in this Sprint.

- **Single client only**: The current daemon multiplexes the ring buffer to multiple simultaneous WS clients, but UX concurrency (two humans typing into the same PTY) is untested and may produce interleaved output.
- **No terminal resize propagation**: The xterm.js FitAddon resizes the DOM element on window resize, but the PTY `SIGWINCH` / `ioctl TIOCSWINSZ` is not sent to the subprocess in the current implementation. Output reflow will be incorrect if the window is resized.
- **CDN-dependent HTML**: The static page loads xterm.js from jsdelivr. Offline or air-gapped environments will see a blank terminal. Production integration should bundle xterm.js locally.
- **No reconnect back-off**: The Reconnect button triggers an immediate WS reconnect with no exponential back-off. Rapid repeated clicking could flood the daemon.
- **`cat` vs real claude**: The automated suite uses `cat` as the subprocess. Real claude outputs ANSI escape sequences, mouse-event codes, and interactive TUI frames that may expose xterm.js rendering issues not covered by the mock. Manual smoke is required to confirm the full interactive experience.
- **To be confirmed manually**: actual frame rate, latency, and visual fidelity when typed prompts are processed by real claude in a PTY. The ring-buffer replay correctness for ANSI sequences (colour, cursor positioning) also requires human visual inspection.
