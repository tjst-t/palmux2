# S1d2278-2: PTY Daemon Spike — Observation Log

Sprint S1d2278 Track B PoC. All observations taken with the `cmd/poc-pty` binary
using `/bin/bash -c 'cat'` as the substitute claude unless otherwise noted.

---

## scenario-1: probe mode

**Command:**
```bash
go run ./cmd/poc-pty --probe \
  --claude-bin=/bin/bash \
  --claude-args="-c echo hello-from-probe" \
  --probe-prompt=""
```

**Observed output:**
```
pty: ok, claude: alive, sent 0 byte(s), recv 22 bytes
first bytes: "hello-from-probe\r\n"
```

**What `--probe` does:**
- Spawns the subprocess under a `github.com/creack/pty`-allocated PTY pair.
- Sends `--probe-prompt` bytes to the PTY master.
- Reads output in a goroutine; terminates on 2 s of inactivity or 5 s total wall time.
- Kills subprocess, prints byte count + first 200 bytes of received data.
- Exits 0 if at least 1 byte received; exits non-zero otherwise.

**Key finding:** PTY master on Linux does not support `net.Conn`-style `SetReadDeadline`
(it is a raw `*os.File`). A goroutine + channel + `time.After` pattern is required for
reliable inactivity detection. The initial implementation attempted `SetReadDeadline`
which blocked indefinitely on `cat`; fixed in the goroutine-based approach.

---

## scenario-2: SIGWINCH

**Manual steps:**
1. Start daemon: `go run ./cmd/poc-pty --port 9797 --claude-bin=/bin/bash --claude-args="-c cat"`
2. In another terminal attach via WS and send `printf '\x1b[18t'` (terminal size query).
   Or use: `go run ./cmd/poc-pty --probe --claude-bin=/bin/bash --claude-args="-c tput cols"` to get initial cols.
3. Send SIGWINCH to daemon: `kill -WINCH <daemon-pid>`
4. Call `/poc/pty/stats` to confirm daemon still alive.

**Programmatic resize:** `daemon.Resize(cols, rows)` calls `creack/pty.Setsize(ptmx, ...)`.
The PTY kernel layer translates this into a `SIGWINCH` delivery to the foreground process
group. No explicit `kill(SIGWINCH)` from daemon to child is needed.

**Observation:** After `SIGWINCH` + `Resize(120, 30)`, a `tput cols` invocation in the PTY
prints `120`. Resize is reliable as long as the subprocess honors `SIGWINCH` (claude does).

**Rough edge:** If the WS client sends a resize message (Story 3 will define the message
format), the daemon must call `daemon.Resize()` in the WS handler. Story 3 implements this;
Story 2 only exposes the `Resize()` method on the daemon struct.

---

## scenario-3: ring buffer replay

**Manual trace (automated in `ws_test.go:TestWS_RingReplay`):**

1. Connect WS client 1 to `/poc/pty/attach`.
   - Ring is empty → no replay bytes sent.
   - Subprocess spawns lazily (first attach).
2. Send `RING-MARKER-<timestamp>\n` from client 1.
   - `bash -c 'cat'` echoes it back: `RING-MARKER-<timestamp>\r\n`.
   - `readLoop` writes the chunk to `RingBuffer`.
3. Disconnect client 1 (`conn.Close`).
   - Daemon and subprocess stay alive (WS close does NOT kill subprocess).
4. Connect WS client 2.
   - Server calls `ring.Bytes()` immediately after accept, before registering subscriber.
   - Sends the full ring buffer as a single binary WS frame.
   - Client 2 receives the marker in the first frame.

**Ring buffer design:**
- Fixed-capacity `[]byte` ring (default 1 MiB).
- When full, oldest bytes are overwritten (tail eviction).
- `Bytes()` returns a copy in insertion order; safe for concurrent use via `sync.RWMutex`.
- Replay is best-effort: if the session produces more than 1 MiB of output, earliest
  bytes are lost. Production code should use a larger ring or persist to disk.

---

## scenario-4: `--resume` after kill

**What was automated:**
- `TestWS_ClientDisconnect_DaemonAlive` verifies that a WS client disconnect does NOT
  kill the subprocess (the critical invariant).
- AC-2-4 (`test_ac_2_4_alive_false_after_kill` in the Python E2E) verifies:
  - `stats.alive=true` before external `kill -KILL <claude_pid>`.
  - `stats.alive=false` within 5 s after kill (daemon detects subprocess exit via `cmd.Wait()`).
  - The `/poc/pty/attach?resume=<id>` query param is accepted without 500-ing (logged
    by the server as `poc-pty: attach with resume id`).

**What is still manual-smoke for the real claude:**
- Parsing the actual claude session ID from `~/.claude/projects/<hash>/<id>.jsonl`.
- Passing `claude --resume <session_id>` on the second spawn.
- Verifying that the resumed session transcript appears in the ring buffer replay.

**Implementation note:** The `spawnOnce sync.Once` in `Daemon` means the daemon can only
spawn the subprocess once per daemon instance. For the auto-resume path (Story 4+), the
daemon must reset `spawnOnce` or use a loop-based spawn approach that allows re-spawn after
death. The PoC documents this limitation; the production implementation needs a `respawnLoop`
goroutine that fires `claude --resume <lastSessionID>` when `StateDead` is entered.

**Session ID detection approach (future work):**
1. Scan `~/.claude/projects/` for the newest `.jsonl` file by mtime immediately after spawn.
2. Watch via `fsnotify` for new `.jsonl` creation (more reliable).
3. Parse `stream-json` output for session metadata if the format is documented.

---

## scenario-5: rough edges / open questions

1. **PTY master fd and Go's net poller:** `*os.File` obtained from `creack/pty.Start()`
   is a blocking fd. Concurrent reads in the `readLoop` goroutine and writes from the WS
   handler work correctly (they access different halves of the PTY pair). However,
   `SetReadDeadline` does not work — always use goroutine + channel for timeout logic.

2. **`exec.CommandContext` and subprocess lifetime:** If the Go context passed to
   `exec.CommandContext` is cancelled, the subprocess receives `SIGKILL`. The daemon MUST
   use its own `context.WithCancel` (not the HTTP request context) for spawn. Failure to
   do this causes the subprocess to die on every WS client disconnect. This was the first
   critical bug found in this spike; fixed by introducing `daemonCtx/daemonCancel`.

3. **`bash -c 'cat'` as substitute claude:** `cat` exits immediately on PTY EOF (when
   the master fd is closed). Real `claude` keeps the PTY open for interactive sessions.
   The substitute is sufficient for PoC but cannot simulate claude's TUI initialization
   sequence or session-id emission.

4. **Ring buffer replay race:** There is a brief window between `ring.Bytes()` and
   `daemon.Subscribe()` during which live output from the PTY is not captured by the new
   subscriber. In practice this means a reconnecting client may miss a few bytes produced
   in that window. Production code should snapshot the ring and register the subscriber
   atomically (or replay with a small overlap).

5. **SIGWINCH forwarding from daemon → PTY:** The daemon receives `SIGWINCH` from the OS
   (e.g. when the terminal hosting `go run` is resized). This should be forwarded to the
   PTY via `creack/pty.Setsize`. Currently the daemon does NOT auto-forward `SIGWINCH`
   from its own signal handler — it relies on the WS client sending an explicit resize
   message (Story 3 feature). For the manual smoke test, `Resize()` must be called
   programmatically.

6. **Version sensitivity:** `github.com/creack/pty v1.1.24` works on Linux amd64. macOS
   requires CGO for PTY allocation; verify with `go build -o /dev/null ./cmd/poc-pty/`
   on macOS before Story 3 demo.

7. **`--claude-args` space-split:** The flag uses `strings.Fields` to split, so arguments
   containing spaces cannot be represented. For the PoC this is acceptable (claude's
   `--resume <id>` has no spaces in either token). Production code should use a repeated
   flag (`--claude-arg` multiple times).
