package claudeagent

// SPIKE S862203-1 (no-halt-agent Sprint 2, gating).
//
// Question (ADR-0004 revisit gate, see
// docs/DESIGN/adr/ADR-0004-agent-pipe-mode-offset-replay.json and
// docs/sprint-logs/S862203/scenario-S862203-1.json): does the REAL claude
// CLI, in stream-json mode, wait for and ACCEPT an MCP-routed permission
// control_request response that arrives LATE (10s / 30s / 60s), or does it
// time out / cancel / abort the gated tool before a delayed "allow"
// arrives? ADR-0004's replay design assumes the CLI's own deadline for this
// specific control_request exceeds palmux2's realistic restart window (a
// few seconds to ~15s) -- if not, the design must be revisited before
// Wave 2/3 of S862203 are built.
//
// This file is a THROWAWAY spike harness, not production code:
//   - it is entirely test-only (spike_control_deadline_test.go)
//   - it is env-gated (PALMUX_SPIKE_S862203_1=1) and excluded from the
//     default `go test ./...` run, mirroring the PALMUX_SURVIVAL_SMOKE /
//     PALMUX_REALINCUS_SMOKE pattern already used in
//     internal/tab/claudetui/survival_gate_test.go
//   - it spawns the REAL `claude` binary via the package's own NewClient
//     (client.go) -- the exact production wire path (mcp_message
//     control_request -> in-process mcpServer.handle -> tools/call
//     "permission_prompt" -> PermissionRequester) -- so the observed
//     timing is genuinely production-representative, not a simulation.
//
// Consumes real Anthropic API quota: one real-claude turn per delay tier
// (10s / 30s / 60s by default -- 3 turns total). Keep additional runs to a
// minimum; see docs/sprint-logs/S862203/spike-S862203-1-1.json for the
// recorded result of the last run.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// ─────────────────────────────────────────────────────────────────
// Recorder: timestamped event log shared across the permission
// requester, the message handler, and a custom slog handler so the
// whole run can be reconstructed and inspected after the fact for
// the "was anything cancelled/timed out BEFORE the late response
// arrived" question.
// ─────────────────────────────────────────────────────────────────

type spikeEvent struct {
	At     time.Time `json:"at"`
	Kind   string    `json:"kind"` // "message" | "log" | "permission" | "marker"
	Detail string    `json:"detail"`
}

type spikeRecorder struct {
	mu     sync.Mutex
	start  time.Time
	events []spikeEvent
}

func newSpikeRecorder() *spikeRecorder {
	return &spikeRecorder{start: time.Now()}
}

func (r *spikeRecorder) record(kind, detail string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, spikeEvent{At: time.Now(), Kind: kind, Detail: detail})
}

func (r *spikeRecorder) snapshot() []spikeEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]spikeEvent, len(r.events))
	copy(out, r.events)
	return out
}

// relMs formats an event's offset from run-start in milliseconds, for
// human-readable notes.
func (r *spikeRecorder) relMs(t time.Time) int64 {
	return t.Sub(r.start).Milliseconds()
}

// spikeLogHandler is a minimal slog.Handler that forwards every record into
// the recorder. This is how we catch respondControl's "write control
// response" warning (logged by client.go when a stdin write fails, e.g.
// because the CLI process has already exited) -- the strongest available
// signal that a late response was NOT accepted, short of parsing raw
// control_response frames.
type spikeLogHandler struct {
	rec *spikeRecorder
}

func (h *spikeLogHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *spikeLogHandler) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *spikeLogHandler) WithGroup(string) slog.Handler            { return h }
func (h *spikeLogHandler) Handle(_ context.Context, rec slog.Record) error {
	var sb strings.Builder
	sb.WriteString(rec.Message)
	rec.Attrs(func(a slog.Attr) bool {
		fmt.Fprintf(&sb, " %s=%v", a.Key, a.Value)
		return true
	})
	h.rec.record("log", sb.String())
	return nil
}

// ─────────────────────────────────────────────────────────────────
// Delayed permission requester: withholds the "allow" for `delay`,
// then resolves. Records arrival + resolution timestamps.
// ─────────────────────────────────────────────────────────────────

type spikeDelayedPermission struct {
	delay time.Duration
	rec   *spikeRecorder

	mu        sync.Mutex
	arrivedAt time.Time
	resolveAt time.Time
}

func (p *spikeDelayedPermission) RequestPermission(ctx context.Context, toolName string, input json.RawMessage, toolUseID string) (permissionResponse, error) {
	now := time.Now()
	p.mu.Lock()
	p.arrivedAt = now
	p.mu.Unlock()
	p.rec.record("permission", fmt.Sprintf("control_request arrived: tool=%s tool_use_id=%s input=%s", toolName, toolUseID, truncate(string(input), 300)))

	select {
	case <-time.After(p.delay):
	case <-ctx.Done():
		p.rec.record("permission", fmt.Sprintf("ctx cancelled while withholding response after %s: %v", time.Since(now), ctx.Err()))
		return permissionResponse{}, ctx.Err()
	}

	resolved := time.Now()
	p.mu.Lock()
	p.resolveAt = resolved
	p.mu.Unlock()
	p.rec.record("permission", fmt.Sprintf("sending late allow after withholding %s", resolved.Sub(now)))
	return permissionResponse{Behavior: "allow"}, nil
}

func (p *spikeDelayedPermission) timestamps() (arrived, resolved time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.arrivedAt, p.resolveAt
}

// ─────────────────────────────────────────────────────────────────
// Message observation: watch the real stream for the turn-ending
// "result" envelope (and anything else interesting) so we can tell
// whether the turn actually continued to completion after the late
// allow, or ended early (which would indicate the CLI gave up).
// ─────────────────────────────────────────────────────────────────

type spikeResult struct {
	At       time.Time
	IsError  bool
	Result   string
	NumTurns int
}

func newSpikeMessageWatcher(rec *spikeRecorder) (onMessage MessageHandler, resultCh chan spikeResult) {
	resultCh = make(chan spikeResult, 4)
	onMessage = func(msg streamMsg) {
		switch msg.Type {
		case "result":
			rec.record("message", fmt.Sprintf("result: is_error=%v result=%s num_turns=%d", msg.IsError, truncate(msg.Result, 200), msg.NumTurns))
			select {
			case resultCh <- spikeResult{At: time.Now(), IsError: msg.IsError, Result: msg.Result, NumTurns: msg.NumTurns}:
			default:
			}
		case "system":
			rec.record("message", fmt.Sprintf("system/%s", msg.Subtype))
		case "assistant", "user":
			rec.record("message", fmt.Sprintf("%s message (len=%d)", msg.Type, len(msg.Message)))
		case "stream_event":
			// High volume / low information for this spike -- skip logging
			// individual deltas, they'd drown the record.
		default:
			rec.record("message", fmt.Sprintf("%s/%s", msg.Type, msg.Subtype))
		}
	}
	return onMessage, resultCh
}

// ─────────────────────────────────────────────────────────────────
// Tier result + JSON record shape (mirrors
// docs/sprint-logs/S3f2658/spike-S3f2658-1-1.json's flat, evidence-heavy
// style).
// ─────────────────────────────────────────────────────────────────

type spikeTierResult struct {
	DelaySeconds            int      `json:"delay_s"`
	Accepted                bool     `json:"accepted"`
	ToolExecuted            bool     `json:"tool_executed"`
	TurnContinued           bool     `json:"turn_continued"`
	ObservedTimeoutOrCancel bool     `json:"observed_timeout_or_cancel"`
	ResultIsError           bool     `json:"result_is_error"`
	ResultSummary           string   `json:"result_summary"`
	MarkerContent           string   `json:"marker_content"`
	RequestArrivalRelMs     int64    `json:"request_arrival_rel_ms"`
	ResponseSentRelMs       int64    `json:"response_sent_rel_ms"`
	ResultRelMs             int64    `json:"result_rel_ms,omitempty"`
	Notes                   string   `json:"notes"`
	RawEvents               []string `json:"raw_events"`
}

// runControlDeadlineTier spawns one real claude CLI turn, forces a single
// Bash permission control_request, withholds the allow for `delay`, then
// sends it and observes the outcome. Returns a fully-populated tier result;
// never fails the test itself on a HALT-worthy outcome (the caller decides
// the verdict) but DOES fail on harness-level errors (spawn failure, etc.)
// since those aren't informative about claude's behaviour.
func runControlDeadlineTier(t *testing.T, delay time.Duration) spikeTierResult {
	t.Helper()

	tmpDir := t.TempDir()
	markerFile := filepath.Join(tmpDir, fmt.Sprintf("spike-marker-%ds.txt", int(delay.Seconds())))
	markerText := fmt.Sprintf("SPIKE_S862203_1_%ds_OK", int(delay.Seconds()))

	rec := newSpikeRecorder()
	logger := slog.New(&spikeLogHandler{rec: rec})

	onMessage, resultCh := newSpikeMessageWatcher(rec)
	perm := &spikeDelayedPermission{delay: delay, rec: rec}

	cli, err := NewClient(context.Background(), ClientOptions{
		Binary: "claude",
		Cwd:    tmpDir,
		Logger: logger,
		// This host's shared ~/.claude/settings.json sets
		// permissions.defaultMode=bypassPermissions (it's the ambient
		// config for the Claude Code session driving this very spike).
		// --setting-sources project,user (set unconditionally by
		// NewClient) would otherwise pick that up and let the Bash tool
		// run WITHOUT ever hitting our permission_prompt MCP tool --
		// which defeats the entire spike. Force "manual" (ask-every-time)
		// so the control_request we need to observe is guaranteed to
		// fire, matching how the real Claude tab always requests
		// permission (see client.go's PermissionMode wiring).
		PermissionMode: "manual",
	}, onMessage, nil, perm)
	if err != nil {
		t.Fatalf("tier %s: NewClient (real claude spawn) failed: %v", delay, err)
	}
	defer cli.Close()

	initCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	_, err = cli.Initialize(initCtx)
	cancel()
	if err != nil {
		t.Fatalf("tier %s: Initialize failed: %v", delay, err)
	}
	rec.record("message", "initialize complete")

	prompt := fmt.Sprintf(
		"Use the Bash tool to run exactly this one command and nothing else -- no explanation, no other tool, no reading files first: echo %s > %s",
		markerText, markerFile,
	)
	if err := cli.SendUserMessage(prompt); err != nil {
		t.Fatalf("tier %s: SendUserMessage failed: %v", delay, err)
	}
	rec.record("message", "user message sent")

	// Wait for the control_request to arrive (claude deciding to call Bash
	// and the CLI routing it through our permission_prompt MCP tool). This
	// itself does not withhold anything -- the requester goroutine is
	// already sleeping internally once RequestPermission is entered; we
	// just poll the recorder for the arrival timestamp so we know the
	// clock has started.
	arrivalDeadline := time.After(45 * time.Second)
	var arrived time.Time
waitArrival:
	for {
		select {
		case <-arrivalDeadline:
			t.Fatalf("tier %s: no permission control_request arrived within 45s -- claude did not attempt the Bash tool call. Events so far: %v", delay, rec.snapshot())
		case <-time.After(200 * time.Millisecond):
			a, _ := perm.timestamps()
			if !a.IsZero() {
				arrived = a
				break waitArrival
			}
		}
	}
	// The requester is now sleeping for `delay` before it resolves
	// (spikeDelayedPermission.RequestPermission runs autonomously on the
	// CLI's own control-request-handling goroutine; nothing further needs
	// to be triggered from here). Block for the ONE "result" envelope the
	// turn produces -- whenever it actually arrives, early or late -- with
	// a generous absolute ceiling. We deliberately do NOT race a
	// window-based guess for "early": that timestamp comparison happens
	// AFTER the fact below, against the requester's own recorded
	// resolution time, which is exact.
	var finalResult *spikeResult
	select {
	case r := <-resultCh:
		rc := r
		finalResult = &rc
	case <-time.After(delay + 90*time.Second):
		rec.record("message", "no result envelope within delay+90s budget")
	}

	// The requester's sleep(delay) always completes on its own clock
	// (nothing in this function can stall it), so resolveAt is expected to
	// be set by now; poll briefly as a safety margin against goroutine
	// scheduling jitter.
	_, resolvedAt := perm.timestamps()
	for i := 0; resolvedAt.IsZero() && i < 200; i++ {
		time.Sleep(50 * time.Millisecond)
		_, resolvedAt = perm.timestamps()
	}

	// Give the filesystem a brief moment in case the tool executed but the
	// result envelope raced the write.
	time.Sleep(300 * time.Millisecond)
	markerBytes, readErr := os.ReadFile(markerFile)
	toolExecuted := readErr == nil && strings.Contains(string(markerBytes), markerText)

	turnContinued := finalResult != nil
	resultIsError := finalResult != nil && finalResult.IsError
	// "Early" = the result envelope's OWN timestamp precedes the moment we
	// actually sent the late allow -- i.e. the turn ended (successfully or
	// not) before our response could possibly have reached the CLI. A
	// small negative-tolerance margin absorbs clock/goroutine jitter
	// around the exact resolve instant.
	observedEarly := finalResult != nil && !resolvedAt.IsZero() && finalResult.At.Before(resolvedAt.Add(-50*time.Millisecond))

	// "Accepted" is judged primarily by the ground-truth side effect (did
	// the gated Bash command actually run), corroborated by the turn
	// continuing to a non-error result and the absence of a
	// "write control response" failure log (stdin already closed).
	writeFailed := false
	for _, ev := range rec.snapshot() {
		if ev.Kind == "log" && strings.Contains(ev.Detail, "write control response") {
			writeFailed = true
		}
	}
	accepted := toolExecuted && !writeFailed && !observedEarly

	var summary string
	if finalResult != nil {
		summary = fmt.Sprintf("is_error=%v result=%q num_turns=%d", finalResult.IsError, truncate(finalResult.Result, 200), finalResult.NumTurns)
	} else {
		summary = "no result envelope observed"
	}

	notes := fmt.Sprintf(
		"arrival=%dms(rel) response_sent=%dms(rel) early_result_before_response=%v write_control_response_failed=%v marker_read_err=%v",
		rec.relMs(arrived), rec.relMs(resolvedAt), observedEarly, writeFailed, readErr,
	)

	var rawStrs []string
	for _, ev := range rec.snapshot() {
		rawStrs = append(rawStrs, fmt.Sprintf("+%dms [%s] %s", rec.relMs(ev.At), ev.Kind, ev.Detail))
	}

	tr := spikeTierResult{
		DelaySeconds:            int(delay.Seconds()),
		Accepted:                accepted,
		ToolExecuted:            toolExecuted,
		TurnContinued:           turnContinued,
		ObservedTimeoutOrCancel: observedEarly || writeFailed,
		ResultIsError:           resultIsError,
		ResultSummary:           summary,
		MarkerContent:           strings.TrimSpace(string(markerBytes)),
		RequestArrivalRelMs:     rec.relMs(arrived),
		ResponseSentRelMs:       rec.relMs(resolvedAt),
		Notes:                   notes,
		RawEvents:               rawStrs,
	}
	if finalResult != nil {
		tr.ResultRelMs = rec.relMs(finalResult.At)
	}
	return tr
}

// ─────────────────────────────────────────────────────────────────
// Entry point
// ─────────────────────────────────────────────────────────────────

// TestSpike_S862203_1_ControlRequestLateResponse is the gating spike for
// ADR-0004. See the package doc comment at the top of this file. Skipped
// unless PALMUX_SPIKE_S862203_1=1 -- this test spawns the real `claude`
// binary and consumes real API quota, so it must never run as part of
// ordinary `go test ./...`.
//
// PALMUX_SPIKE_S862203_1_TIERS optionally narrows the tiers run (comma
// separated seconds, e.g. "60" or "10,60") -- default is "10,30,60".
func TestSpike_S862203_1_ControlRequestLateResponse(t *testing.T) {
	if os.Getenv("PALMUX_SPIKE_S862203_1") == "" {
		t.Skip("real-claude spike (S862203-1, ADR-0004 gate): spawns the real `claude` binary and consumes real API quota; set PALMUX_SPIKE_S862203_1=1 to run. See docs/sprint-logs/S862203/scenario-S862203-1.json")
	}

	tierSecs := []int{10, 30, 60}
	if raw := os.Getenv("PALMUX_SPIKE_S862203_1_TIERS"); raw != "" {
		tierSecs = nil
		for _, s := range strings.Split(raw, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			n, err := strconv.Atoi(s)
			if err != nil {
				t.Fatalf("bad PALMUX_SPIKE_S862203_1_TIERS entry %q: %v", s, err)
			}
			tierSecs = append(tierSecs, n)
		}
	}
	sort.Ints(tierSecs)

	results := make([]spikeTierResult, 0, len(tierSecs))
	for _, s := range tierSecs {
		d := time.Duration(s) * time.Second
		t.Logf("=== tier %ds: withholding MCP permission response ===", s)
		r := runControlDeadlineTier(t, d)
		t.Logf("tier %ds result: accepted=%v tool_executed=%v turn_continued=%v observed_timeout_or_cancel=%v summary=%s",
			s, r.Accepted, r.ToolExecuted, r.TurnContinued, r.ObservedTimeoutOrCancel, r.ResultSummary)
		results = append(results, r)
		if !r.Accepted {
			t.Logf("tier %ds: late allow was NOT accepted -- ADR-0004 gate would HALT here. Raw events:\n%s", s, strings.Join(r.RawEvents, "\n"))
		}
	}

	writeSpikeRecord(t, results)
}

// writeSpikeRecord persists the run to
// docs/sprint-logs/S862203/spike-S862203-1-1.json in the shape the task
// mandates. Best-effort: a write failure logs but does not fail the test,
// since the in-memory results (visible in -v output) are the source of
// truth for the PASS/HALT decision either way.
func writeSpikeRecord(t *testing.T, results []spikeTierResult) {
	t.Helper()

	maxConfirmed := 0
	verdict := "HALT"
	for _, r := range results {
		if r.Accepted && r.DelaySeconds > maxConfirmed {
			maxConfirmed = r.DelaySeconds
		}
	}
	allAccepted := len(results) > 0
	for _, r := range results {
		if !r.Accepted {
			allAccepted = false
		}
	}
	if allAccepted && maxConfirmed >= 15 {
		verdict = "PASS"
	}

	doc := map[string]any{
		"task":                   "S862203-1-1",
		"goal":                   "Determine whether claude CLI's deadline for an MCP-routed permission control_request response EXCEEDS palmux2's realistic restart window (a few seconds to ~15s), per ADR-0004's revisit condition.",
		"host":                   hostDescription(),
		"method":                 "Real `claude` CLI spawned via internal/tab/claudeagent.NewClient (production wire path: --permission-prompt-tool mcp__palmux__permission_prompt, in-process mcpServer, mcp_message control_request/control_response). A user prompt forces exactly one Bash tool call; the PermissionRequester withholds the allow response for the tier's delay, then sends it. Acceptance is judged by the ground-truth side effect (did the Bash command actually write its marker file), corroborated by turn continuation (a `result` envelope) and the absence of a stdin write failure.",
		"tiers":                  results,
		"max_confirmed_delay_s":  maxConfirmed,
		"verdict":                verdict,
		"adr_0004_gate":          adrGateSummary(verdict, maxConfirmed),
		"restart_window_context": restartWindowContext(),
		"notes": []string{
			"This harness observes production-representative behaviour via the real NewClient/mcpServer path, but does NOT intercept raw control_cancel_request frames on the wire (client.go's pumpStdout drops those silently and does not forward them to onMessage) -- acceptance is instead judged by the tool's actual side effect (marker file) plus turn-continuation and stdin-write-failure signals, which are the properties that actually matter for ADR-0004's replay design.",
			"Each tier is a fresh claude CLI process / fresh session (no --resume) to keep tiers independent and avoid compounding turn history across withheld-response runs.",
			"Consumed real Anthropic API quota: one real-claude turn per tier run.",
		},
	}

	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Logf("writeSpikeRecord: marshal failed: %v", err)
		return
	}
	// Repo root is 4 levels up from internal/tab/claudeagent.
	outPath := filepath.Join("..", "..", "..", "docs", "sprint-logs", "S862203", "spike-S862203-1-1.json")
	if err := os.WriteFile(outPath, b, 0o644); err != nil {
		t.Logf("writeSpikeRecord: write %s failed: %v", outPath, err)
		return
	}
	t.Logf("wrote %s", outPath)
}

func adrGateSummary(verdict string, maxConfirmed int) string {
	if verdict == "PASS" {
		return fmt.Sprintf("clears (confirmed deadline >= %ds >= ~15s restart window) -- ADR-0004 replay design confirmed sound, proceed to Wave 2 (S862203-2)", maxConfirmed)
	}
	return fmt.Sprintf("REVISIT: late allow was not reliably accepted at/above the ~15s restart window (max confirmed accepted delay = %ds) -- HALT S862203 Wave 2/3, escalate ADR-0004 revisit per its notes (rely on claude to re-send the pending request on reconnect, or have ptyhost hold the request without auto-deny)", maxConfirmed)
}

// restartWindowContext documents the palmux2 restart-window baseline this
// spike's deadline is compared against. A fresh `make serve INSTANCE=dev`
// kill+respawn+reconnect measurement was judged out of scope for this spike
// run (it would require a full frontend+backend build cycle unrelated to
// the quota-sensitive real-claude question that gates the sprint); instead
// this records the reasoned estimate already implicit in the project's own
// framing (scenario-S862203-1.json / ADR-0004: "a few seconds to ~15s") and
// the concrete data points behind it from prior sprints.
func restartWindowContext() map[string]any {
	return map[string]any{
		"measured_this_run":   false,
		"reasoned_estimate_s": "a few seconds to ~15s",
		"basis": []string{
			"docs/DESIGN/adr/ADR-0004-agent-pipe-mode-offset-replay.json and scenario-S862203-1.json both frame the restart window as 'a few seconds to ~15s' -- this spike treats 15s as the gate threshold per that framing rather than re-deriving it.",
			"Sa8e7d0 (self-update robustness) documents the FE reconnect handshake shape (WS drop -> /health version polling -> reconnect -> toast) but not a single-number latency; S3f2658's spike-S3f2658-1-1.json confirms the ptyhost cgroup-escape/reattach mechanism itself is near-instant (kill -0 succeeds immediately post-restart) -- the dominant cost is the palmux2 Go process's own systemd restart + listener rebind + client poll interval, typically low single-digit seconds on this class of host.",
			"A fresh empirical `make serve INSTANCE=dev` measurement was not performed in this spike run to avoid an unrelated full build cycle; if a precise number is needed before Wave 2, it can be taken cheaply with `time (make serve INSTANCE=dev; until curl -sf http://127.0.0.1:$PORT/health; do sleep 0.2; done)`.",
		},
	}
}

func hostDescription() string {
	host, _ := os.Hostname()
	return host + " -- dev worktree, real `claude` binary on PATH (see `claude --version` in spike run notes)"
}
