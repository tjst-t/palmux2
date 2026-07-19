package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/tjst-t/palmux2/internal/notify"
)

func TestBuildHookBody(t *testing.T) {
	const (
		repo = "r1"
		br   = "b1"
		tab  = "claude"
		name = "Claude"
	)
	reqID := "claudetui-hook-" + tab

	tests := []struct {
		name        string
		event       string
		notifType   string
		message     string
		lastAssist  string
		wantOK      bool
		wantType    string
		wantMessage string
		wantResolve bool
	}{
		{
			name: "Stop uses last assistant message", event: "Stop",
			lastAssist: "all done", wantOK: true,
			wantType: "claudetui.task_complete", wantMessage: "all done",
		},
		{
			name: "Stop falls back to default", event: "Stop",
			wantOK: true, wantType: "claudetui.task_complete",
			wantMessage: "Claude finished — your turn",
		},
		{
			name: "Notification permission", event: "Notification",
			notifType: "permission_prompt", message: "Allow Bash?",
			wantOK: true, wantType: "claudetui.permission_prompt", wantMessage: "Allow Bash?",
		},
		{
			name: "Notification idle default msg", event: "Notification",
			notifType: "idle_prompt", wantOK: true,
			wantType: "claudetui.idle", wantMessage: "Claude is waiting for your input",
		},
		{
			name: "Notification generic", event: "Notification",
			wantOK: true, wantType: "claudetui.notification",
			wantMessage: "Claude needs your attention",
		},
		{
			name: "UserPromptSubmit resolves", event: "UserPromptSubmit",
			wantOK: true, wantResolve: true,
		},
		{name: "unknown event ignored", event: "PreToolUse", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			body, ok := buildHookBody(tc.event, repo, br, tab, name, tc.notifType, tc.message, tc.lastAssist)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if !ok {
				return
			}
			// Round-trip through notify.IngestRequest to assert wire fidelity.
			raw, _ := json.Marshal(body)
			var req notify.IngestRequest
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatalf("body does not match IngestRequest: %v\n%s", err, raw)
			}
			if req.RepoID != repo || req.BranchID != br {
				t.Errorf("repo/branch = %s/%s", req.RepoID, req.BranchID)
			}
			if req.RequestID != reqID {
				t.Errorf("requestId = %q, want %q", req.RequestID, reqID)
			}
			if tc.wantResolve {
				if !req.Resolve {
					t.Errorf("expected resolve=true")
				}
				return
			}
			if req.Resolve {
				t.Errorf("did not expect resolve=true for %s", tc.event)
			}
			if req.TabID != tab {
				t.Errorf("tabId = %q, want %q", req.TabID, tab)
			}
			if req.Type != tc.wantType {
				t.Errorf("type = %q, want %q", req.Type, tc.wantType)
			}
			if req.Message != tc.wantMessage {
				t.Errorf("message = %q, want %q", req.Message, tc.wantMessage)
			}
		})
	}
}

func TestParseHookArgs(t *testing.T) {
	ev, url, agentKind := parseHookArgs([]string{"--event", "Stop", "--url", "http://x/api/notify"})
	if ev != "Stop" || url != "http://x/api/notify" || agentKind != "" {
		t.Fatalf("got event=%q url=%q agentKind=%q", ev, url, agentKind)
	}
	// Trailing flag without value must not panic.
	if ev, _, _ := parseHookArgs([]string{"--event"}); ev != "" {
		t.Fatalf("dangling --event: %q", ev)
	}
	// S2b5691: --agent=<kind> is parsed independently of --event/--url.
	if _, _, agentKind := parseHookArgs([]string{"--agent=codex"}); agentKind != "codex" {
		t.Fatalf("got agentKind=%q, want codex", agentKind)
	}
}

// TestLastNonFlagHookArg_ValueAware is the code-review-cycle-1 regression
// test for lastNonFlagHookArg: it must be aware that a known flag (--event,
// --url) consumes the FOLLOWING argv element as its value, not just that the
// flag token itself starts with "--". A naive backward scan for "the last
// element not starting with --" would mistake a flag's value (e.g.
// "http://x/api/notify", which itself doesn't start with "--") for codex's
// JSON payload if that flag+value pair were ever placed after the payload —
// silently dropping the real notification. This is currently unreachable in
// production (codex always appends the JSON payload strictly last with no
// trailing flags — see internal/agent/codex.go's SpawnSpec), but the parser
// must not depend on that ordering to stay correct.
func TestLastNonFlagHookArg_ValueAware(t *testing.T) {
	payload := `{"type":"agent-turn-complete"}`

	tests := []struct {
		name string
		args []string
	}{
		{"payload last (production shape)", []string{"--agent=codex", payload}},
		{"flag+value AFTER the payload", []string{"--agent=codex", payload, "--url", "http://x/api/notify"}},
		{"flag+value BEFORE the payload", []string{"--url", "http://x/api/notify", "--agent=codex", payload}},
		{"--event with value interleaved", []string{"--event", "Stop", payload, "--url", "http://y/api/notify"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := lastNonFlagHookArg(tc.args); got != payload {
				t.Errorf("lastNonFlagHookArg(%v) = %q, want %q", tc.args, got, payload)
			}
		})
	}

	// A flag's value that isn't the JSON payload (no payload present at all)
	// must never be picked up as one just because it doesn't start with "--".
	if got := lastNonFlagHookArg([]string{"--url", "http://x/api/notify"}); got != "" {
		t.Errorf("lastNonFlagHookArg with only a flag+value = %q, want empty (the value must not be mistaken for a payload)", got)
	}
}

// TestScenario3_CodexHookDispatch_FlagValueAfterPayload is the end-to-end
// (runHook, not just the lastNonFlagHookArg unit) regression test for the
// same review finding: a --url flag+value placed AFTER the JSON payload
// must not cause the notification to be silently dropped.
func TestScenario3_CodexHookDispatch_FlagValueAfterPayload(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	t.Setenv("PALMUX_REPO_ID", "r")
	t.Setenv("PALMUX_BRANCH_ID", "b")
	t.Setenv("PALMUX_TAB_ID", "codex:codex")

	withStdin(t, ``, func() {
		payload := `{"type":"agent-turn-complete","last-assistant-message":"done"}`
		// --url (with its value) trails the JSON payload here, unlike codex's
		// real invocation shape — proves the dispatch is robust to argv order.
		if code := runHook([]string{"--agent=codex", payload, "--url", srv.URL}); code != 0 {
			t.Fatalf("runHook exit = %d, want 0", code)
		}
	})

	if len(got) == 0 {
		t.Fatal("stub received no POST — a trailing --url flag+value caused the payload to be dropped")
	}
	var req notify.IngestRequest
	if err := json.Unmarshal(got, &req); err != nil {
		t.Fatalf("posted body invalid: %v\n%s", err, got)
	}
	if req.Type != "claudetui.task_complete" || req.Message != "done" {
		t.Errorf("type/message = %q/%q, want claudetui.task_complete/done", req.Type, req.Message)
	}
}

// TestRunHookPostsNotification drives runHook end-to-end: stdin payload + env →
// HTTP POST whose body is a valid IngestRequest.
func TestRunHookPostsNotification(t *testing.T) {
	var got []byte
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		auth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	t.Setenv("PALMUX_REPO_ID", "r1")
	t.Setenv("PALMUX_BRANCH_ID", "b1")
	t.Setenv("PALMUX_TAB_ID", "claude")
	t.Setenv("PALMUX_TOKEN", "secret")

	withStdin(t, `{"hook_event_name":"Stop","last_assistant_message":"done"}`, func() {
		if code := runHook([]string{"--url", srv.URL}); code != 0 {
			t.Fatalf("runHook exit = %d, want 0", code)
		}
	})

	if len(got) == 0 {
		t.Fatal("server received no POST body")
	}
	var req notify.IngestRequest
	if err := json.Unmarshal(got, &req); err != nil {
		t.Fatalf("posted body invalid: %v\n%s", err, got)
	}
	if req.RepoID != "r1" || req.BranchID != "b1" || req.TabID != "claude" {
		t.Errorf("ids = %s/%s/%s", req.RepoID, req.BranchID, req.TabID)
	}
	if req.Type != "claudetui.task_complete" || req.Message != "done" {
		t.Errorf("type/message = %q/%q", req.Type, req.Message)
	}
	if req.RequestID != "claudetui-hook-claude" {
		t.Errorf("requestId = %q", req.RequestID)
	}
	if auth != "Bearer secret" {
		t.Errorf("Authorization = %q, want Bearer secret", auth)
	}
}

// TestRunHookFailsOpenWithoutIdentity verifies the hook never posts (and never
// errors) when required identity env is missing — a misconfigured palmux must
// not disrupt claude.
func TestRunHookFailsOpenWithoutIdentity(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	// PALMUX_REPO_ID / BRANCH_ID intentionally unset.
	t.Setenv("PALMUX_REPO_ID", "")
	t.Setenv("PALMUX_BRANCH_ID", "")

	withStdin(t, `{"hook_event_name":"Stop"}`, func() {
		if code := runHook([]string{"--url", srv.URL}); code != 0 {
			t.Fatalf("runHook exit = %d, want 0 (fail-open)", code)
		}
	})
	if hit {
		t.Error("hook posted despite missing identity env")
	}
}

// withStdin redirects os.Stdin to a pipe carrying content for the duration of
// fn, then restores it.
func withStdin(t *testing.T, content string, fn func()) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	defer func() {
		os.Stdin = orig
		_ = r.Close()
	}()
	go func() {
		_, _ = io.WriteString(w, content)
		_ = w.Close()
	}()
	fn()
}

// TestScenario3_CodexHookDispatch is step 1 of scenario-3-hook-agent-dispatch
// (docs/sprint-logs/S2b5691/scenario-S2b5691-1.json, [AC-S2b5691-1-2] /
// [AC-S2b5691-1-3]): `palmux hook --agent=codex '<json>'` — codex's real
// notify wire format passes its JSON payload as the LAST argv element, never
// stdin (matching internal/agent/codex.go's `-c notify=[...]` injection) —
// must be parsed and POSTed as a claudetui.task_complete notification
// referencing the codex tab.
func TestScenario3_CodexHookDispatch(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	t.Setenv("PALMUX_NOTIFY_URL", srv.URL)
	t.Setenv("PALMUX_REPO_ID", "r")
	t.Setenv("PALMUX_BRANCH_ID", "b")
	t.Setenv("PALMUX_TAB_ID", "codex:codex")

	// stdin is empty — codex never writes to it for notify; the payload
	// travels as the last (non-flag) argv element.
	withStdin(t, ``, func() {
		payload := `{"type":"agent-turn-complete","last-assistant-message":"done"}`
		if code := runHook([]string{"--agent=codex", payload}); code != 0 {
			t.Fatalf("runHook exit = %d, want 0", code)
		}
	})

	if len(got) == 0 {
		t.Fatal("stub received no POST — codex hook dispatch produced nothing")
	}
	var req notify.IngestRequest
	if err := json.Unmarshal(got, &req); err != nil {
		t.Fatalf("posted body invalid: %v\n%s", err, got)
	}
	if req.Type != "claudetui.task_complete" {
		t.Errorf("type = %q, want claudetui.task_complete (agent-neutral turn-end)", req.Type)
	}
	if req.Message != "done" {
		t.Errorf("message = %q, want it to contain the turn's last-assistant-message (%q)", req.Message, "done")
	}
	if req.TabID != "codex:codex" {
		t.Errorf("tabId = %q, want codex:codex", req.TabID)
	}
}

// TestScenario3_CodexHookDispatch_IgnoresOtherEventTypes proves only
// agent-turn-complete is mapped today — an unrecognized codex notify event
// type fails open (no POST), matching runCodexHook's doc comment.
func TestScenario3_CodexHookDispatch_IgnoresOtherEventTypes(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	t.Setenv("PALMUX_NOTIFY_URL", srv.URL)
	t.Setenv("PALMUX_REPO_ID", "r")
	t.Setenv("PALMUX_BRANCH_ID", "b")
	t.Setenv("PALMUX_TAB_ID", "codex:codex")

	withStdin(t, ``, func() {
		payload := `{"type":"some-future-event"}`
		if code := runHook([]string{"--agent=codex", payload}); code != 0 {
			t.Fatalf("runHook exit = %d, want 0 (fail-open)", code)
		}
	})
	if hit {
		t.Error("hook posted for an unrecognized codex event type")
	}
}

// TestScenario3_OpencodeHookDispatch_TurnEnd is step 2 of
// scenario-3-hook-agent-dispatch: `echo '{"type":"session.idle",...}' |
// palmux hook --agent=opencode` — opencode's notify plugin writes its JSON
// payload to the hook subprocess's STDIN (unlike codex's argv), matching
// internal/agent/opencode.go's embedded palmux-notify.js. session.idle must
// produce a turn-end-shaped notification.
func TestScenario3_OpencodeHookDispatch_TurnEnd(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	t.Setenv("PALMUX_NOTIFY_URL", srv.URL)
	t.Setenv("PALMUX_REPO_ID", "r")
	t.Setenv("PALMUX_BRANCH_ID", "b")
	t.Setenv("PALMUX_TAB_ID", "opencode:opencode")

	withStdin(t, `{"type":"session.idle","sessionID":"abc"}`, func() {
		if code := runHook([]string{"--agent=opencode"}); code != 0 {
			t.Fatalf("runHook exit = %d, want 0", code)
		}
	})

	if len(got) == 0 {
		t.Fatal("stub received no POST — opencode session.idle dispatch produced nothing")
	}
	var req notify.IngestRequest
	if err := json.Unmarshal(got, &req); err != nil {
		t.Fatalf("posted body invalid: %v\n%s", err, got)
	}
	if req.Type != "claudetui.task_complete" {
		t.Errorf("type = %q, want claudetui.task_complete (turn-end-shaped)", req.Type)
	}
	if req.TabID != "opencode:opencode" {
		t.Errorf("tabId = %q, want opencode:opencode", req.TabID)
	}
}

// TestScenario3_OpencodeHookDispatch_Permission is step 3 of
// scenario-3-hook-agent-dispatch: a permission.asked event must produce a
// permission-wait-shaped notification carrying the message text.
func TestScenario3_OpencodeHookDispatch_Permission(t *testing.T) {
	var got []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()

	t.Setenv("PALMUX_NOTIFY_URL", srv.URL)
	t.Setenv("PALMUX_REPO_ID", "r")
	t.Setenv("PALMUX_BRANCH_ID", "b")
	t.Setenv("PALMUX_TAB_ID", "opencode:opencode")

	withStdin(t, `{"type":"permission.asked","sessionID":"abc","message":"opencode wants to run: rm -rf"}`, func() {
		if code := runHook([]string{"--agent=opencode"}); code != 0 {
			t.Fatalf("runHook exit = %d, want 0", code)
		}
	})

	if len(got) == 0 {
		t.Fatal("stub received no POST — opencode permission.asked dispatch produced nothing")
	}
	var req notify.IngestRequest
	if err := json.Unmarshal(got, &req); err != nil {
		t.Fatalf("posted body invalid: %v\n%s", err, got)
	}
	if req.Type != "claudetui.permission_prompt" {
		t.Errorf("type = %q, want claudetui.permission_prompt (permission-wait-shaped)", req.Type)
	}
	if req.Message != "opencode wants to run: rm -rf" {
		t.Errorf("message = %q, want the permission-ask message text", req.Message)
	}
}
