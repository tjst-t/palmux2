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
	ev, url := parseHookArgs([]string{"--event", "Stop", "--url", "http://x/api/notify"})
	if ev != "Stop" || url != "http://x/api/notify" {
		t.Fatalf("got event=%q url=%q", ev, url)
	}
	// Trailing flag without value must not panic.
	if ev, _ := parseHookArgs([]string{"--event"}); ev != "" {
		t.Fatalf("dangling --event: %q", ev)
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
