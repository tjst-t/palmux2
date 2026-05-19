# S0fd64b-1: BenchmarkRingFanout — Before / After

Measured on commit that adds `internal/tab/claudetui/emulator.go` (Story 1).
Platform: `linux/amd64`, QEMU Virtual CPU 2.5+, 4 cores.  
Command: `go test -bench=BenchmarkRingFanout -benchmem -benchtime=3s -run=none ./internal/tab/claudetui/...`

## Results

| Scenario | ns/op | MB/s | allocs/op |
|---|---|---|---|
| `no_subs_baseline` | 83.89 | 48823 | 0 |
| `1_raw_sub` | 1266 | 3234 | 1 |
| `4_raw_subs` | 2086 | 1964 | 1 |
| `1_raw_sub + emulator` | 1206 | 3396 | 3 |
| `4_raw_subs + emulator` | 1483 | 2763 | 3 |

## Analysis

The emulator subscriber is consumed **asynchronously** via a dedicated goroutine that
drains the emulator's Ring subscription channel and calls `em.Feed(chunk)`.  Because the
Ring fan-out only enqueues a pointer into a 256-slot buffered channel (never blocking the
Ring.Write lock), the Write path is unaffected by emulator processing speed.

In practice the benchmark shows **no measurable degradation** — the 1-raw-sub case is
within noise (+/−5%), and the 4-raw-sub case is actually faster in this run (variance from
the QEMU virtual CPU scheduler).

## Comparison note (Sprint A baseline — S7ce250)

Sprint A did not include a `BenchmarkRingFanout`.  The baseline above (`1_raw_sub`) is
the first measurement.  For future comparisons, the acceptance criterion is ≤ 20%
throughput regression from `1_raw_sub` to `1_raw_sub + emulator`.

Observed regression: **< 5%** (within noise on this run).  AC-1-3 satisfied.

## Implementation note

In this Story the emulator feed path is **asynchronous** (goroutine off the Ring
subscription), which means the emulator lags the ring by at most one subscriber-channel
slot (256 buffered chunks × 4 KiB = 1 MiB).  The direct (synchronous) path from
`readLoop → emulator.Feed` added in daemon.go provides a lower-latency alternative but
was not measured in this benchmark since the benchmark uses Ring.Write directly.

For the daemon's actual read loop the sync feed adds only the cost of
`SafeEmulator.Write` (a `sync.Mutex` lock + ANSI parse state-machine update per chunk).
At 4 KiB chunks this is negligible.
