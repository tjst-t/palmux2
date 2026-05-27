package claudetui

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/notify"
)

// TestScanPermissionPrompt verifies the regex detects known claude TUI patterns.
func TestScanPermissionPrompt(t *testing.T) {
	tests := []struct {
		name    string
		rowText string
		wantHit bool
	}{
		{
			name:    "Do you want me to",
			rowText: "Do you want me to read /etc/passwd?",
			wantHit: true,
		},
		{
			name:    "Allow ...? pattern",
			rowText: "Allow this tool to execute code?",
			wantHit: true,
		},
		{
			name:    "box-drawing border prefix",
			rowText: "│ Allow bash to run in the background?",
			wantHit: true,
		},
		{
			name:    "normal output line",
			rowText: "fake_claude started",
			wantHit: false,
		},
		{
			name:    "empty line",
			rowText: "",
			wantHit: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Build a Grid with the text in the last row.
			g := Grid{
				Cols: len([]rune(tc.rowText)) + 5,
				Rows: 3,
			}
			// Row 0 + 1 are empty; row 2 holds the test text.
			g.Lines = make([]GridRow, 3)
			for y := range g.Lines {
				g.Lines[y] = GridRow{Y: y, Cells: make([]GridCell, g.Cols)}
				for _, c := range g.Lines[y].Cells {
					c.Ch = ' '
				}
			}
			// Write the text into row 2.
			for x, r := range []rune(tc.rowText) {
				if x < g.Cols {
					g.Lines[2].Cells[x] = GridCell{Ch: r}
				}
			}
			got := scanPermissionPrompt(g)
			if tc.wantHit && got == "" {
				t.Errorf("expected match in %q, got empty", tc.rowText)
			}
			if !tc.wantHit && got != "" {
				t.Errorf("unexpected match in %q: %q", tc.rowText, got)
			}
		})
	}
}

// TestMaybeEmitPermissionPrompt verifies dedup logic.
func TestMaybeEmitPermissionPrompt(t *testing.T) {
	hub := notify.New(nil, nil)

	buildGrid := func(text string) Grid {
		g := Grid{Cols: max(len(text)+5, 10), Rows: 3}
		g.Lines = make([]GridRow, 3)
		for y := range g.Lines {
			g.Lines[y] = GridRow{Y: y, Cells: make([]GridCell, g.Cols)}
		}
		for x, r := range []rune(text) {
			if x < g.Cols {
				g.Lines[2].Cells[x] = GridCell{Ch: r}
			}
		}
		return g
	}

	question := "Do you want me to run tests?"
	last := ""

	// First call: should emit.
	last = maybeEmitPermissionPrompt(hub, "repo1", "branch1", "claude:claude", buildGrid(question), last)
	if last != question {
		t.Fatalf("expected lastQuestion=%q, got %q", question, last)
	}
	snap := hub.Snapshot("repo1", "branch1")
	if snap.UnreadCount != 1 {
		t.Fatalf("expected 1 unread after first emit, got %d", snap.UnreadCount)
	}

	// Second call with same question: should NOT emit (dedup).
	last = maybeEmitPermissionPrompt(hub, "repo1", "branch1", "claude:claude", buildGrid(question), last)
	snap = hub.Snapshot("repo1", "branch1")
	if snap.UnreadCount != 1 {
		t.Fatalf("expected still 1 unread after dedup, got %d", snap.UnreadCount)
	}

	// Third call with different question: should emit.
	q2 := "Do you want me to delete files?"
	last = maybeEmitPermissionPrompt(hub, "repo1", "branch1", "claude:claude", buildGrid(q2), last)
	if last != q2 {
		t.Fatalf("expected lastQuestion=%q after second emit, got %q", q2, last)
	}
	snap = hub.Snapshot("repo1", "branch1")
	if snap.UnreadCount != 2 {
		t.Fatalf("expected 2 unread after second emit, got %d", snap.UnreadCount)
	}
}

// TestMaybeEmitTaskComplete verifies BEL detection and 2-second throttle.
func TestMaybeEmitTaskComplete(t *testing.T) {
	hub := notify.New(nil, nil)

	var zeroTime time.Time

	// BEL in chunk: should emit.
	after := maybeEmitTaskComplete(hub, "repo2", "branch2", "claude:claude", []byte("\x07"), zeroTime)
	if after.IsZero() {
		t.Fatal("expected non-zero lastBEL after BEL emit")
	}
	snap := hub.Snapshot("repo2", "branch2")
	if snap.UnreadCount != 1 {
		t.Fatalf("expected 1 unread after BEL, got %d", snap.UnreadCount)
	}

	// Immediate second BEL: throttle should prevent emission.
	after2 := maybeEmitTaskComplete(hub, "repo2", "branch2", "claude:claude", []byte("\x07"), after)
	if !after2.Equal(after) {
		t.Fatalf("expected throttle to leave lastBEL unchanged; got %v vs %v", after2, after)
	}
	snap = hub.Snapshot("repo2", "branch2")
	if snap.UnreadCount != 1 {
		t.Fatalf("expected still 1 unread after throttled BEL, got %d", snap.UnreadCount)
	}

	// No BEL in chunk: should not emit.
	before := after
	after3 := maybeEmitTaskComplete(hub, "repo2", "branch2", "claude:claude", []byte("heartbeat\n"), before)
	if !after3.Equal(before) {
		t.Fatalf("expected no change without BEL, got %v vs %v", after3, before)
	}
}

// TestDaemonEmitsPermissionPrompt is an integration test: fake_claude emits a
// permission prompt line; the daemon's detect loop should publish a notification.
func TestDaemonEmitsPermissionPrompt(t *testing.T) {
	hub := notify.New(nil, nil)

	bin := fakeBin(t)
	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		ClaudeArgs:    []string{"--emit-permission-prompt", "read /etc/passwd"},
		RingSize:      1 << 16,
		ResumeOnDeath: false,
		NotifyHub:     hub,
		RepoID:        "repo-perm",
		BranchID:      "branch-perm",
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	// Wait for the permission notification to arrive.
	deadline := time.After(10 * time.Second)
	for {
		snap := hub.Snapshot("repo-perm", "branch-perm")
		if snap.UnreadCount > 0 && snap.LastType == "claudetui.permission_prompt" {
			t.Logf("permission_prompt notification received: msg=%q", snap.LastMessage)
			// Verify the question text is in the notification.
			if !strings.Contains(snap.LastMessage, "read /etc/passwd") {
				t.Errorf("expected message to contain 'read /etc/passwd', got %q", snap.LastMessage)
			}
			return
		}
		// Also check the ring to make sure the line was actually emitted.
		ringData := d.ring.Bytes()
		select {
		case <-deadline:
			t.Fatalf(
				"timed out waiting for permission_prompt notification; "+
					"hub.Snapshot=%+v ring=%q",
				snap, ringData[:min(len(ringData), 200)],
			)
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
}

// TestDaemonEmitsBELTaskComplete is an integration test: fake_claude emits BEL;
// the daemon should publish a task_complete notification.
func TestDaemonEmitsBELTaskComplete(t *testing.T) {
	hub := notify.New(nil, nil)

	bin := fakeBin(t)
	d := NewDaemon(DaemonConfig{
		ClaudeBin:     bin,
		ClaudeArgs:    []string{"--emit-bel"},
		RingSize:      1 << 16,
		ResumeOnDeath: false,
		NotifyHub:     hub,
		RepoID:        "repo-bel",
		BranchID:      "branch-bel",
	})
	t.Cleanup(func() { d.Shutdown() })

	if err := d.EnsureStarted(context.Background()); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, 5*time.Second)

	// Wait until the BEL byte lands in the ring.
	ringDeadline := time.After(5 * time.Second)
	for {
		if bytes.IndexByte(d.ring.Bytes(), 0x07) >= 0 {
			break
		}
		select {
		case <-ringDeadline:
			t.Fatalf("BEL byte never appeared in ring; ring=%q", d.ring.Bytes())
		default:
			time.Sleep(20 * time.Millisecond)
		}
	}

	// Wait for the task_complete notification.
	deadline := time.After(10 * time.Second)
	for {
		snap := hub.Snapshot("repo-bel", "branch-bel")
		if snap.UnreadCount > 0 && snap.LastType == "claudetui.task_complete" {
			t.Logf("task_complete notification received: msg=%q", snap.LastMessage)
			return
		}
		select {
		case <-deadline:
			t.Fatalf(
				"timed out waiting for task_complete notification; hub.Snapshot=%+v ring=%q",
				snap, d.ring.Bytes()[:min(len(d.ring.Bytes()), 200)],
			)
		default:
			time.Sleep(50 * time.Millisecond)
		}
	}
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
