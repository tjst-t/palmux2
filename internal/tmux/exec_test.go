package tmux

import (
	"context"
	"os/exec"
	"testing"
	"time"
)

// integration tests touch the real tmux binary. They are skipped if tmux is
// not on PATH so unit-test runs on minimal CI images don't fail.

const testSession = "_palmux2_unit_session"

func skipIfNoTmux(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not on PATH")
	}
}

func TestExecClient_SessionLifecycle(t *testing.T) {
	skipIfNoTmux(t)
	c := NewExecClient()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pre-clean in case a prior failed run left a session.
	_ = c.KillSession(ctx, testSession)

	if err := c.NewSession(ctx, NewSessionOpts{
		Name:       testSession,
		WindowName: "palmux:bash:bash",
		Cwd:        t.TempDir(),
	}); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	t.Cleanup(func() { _ = c.KillSession(context.Background(), testSession) })

	has, err := c.HasSession(ctx, testSession)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !has {
		t.Fatal("expected session to exist")
	}

	sessions, err := c.ListSessions(ctx)
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, s := range sessions {
		if s.Name == testSession {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("session %q missing from ListSessions: %v", testSession, sessions)
	}

	windows, err := c.ListWindows(ctx, testSession)
	if err != nil {
		t.Fatalf("ListWindows: %v", err)
	}
	if len(windows) == 0 || windows[0].Name != "palmux:bash:bash" {
		t.Fatalf("unexpected windows: %+v", windows)
	}

	if err := c.NewWindow(ctx, testSession, NewWindowOpts{Name: "palmux:bash:second", Cwd: t.TempDir()}); err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	idx, err := c.WindowIndexByName(ctx, testSession, "palmux:bash:second")
	if err != nil {
		t.Fatalf("WindowIndexByName: %v", err)
	}
	if idx < 0 {
		t.Fatalf("unexpected idx: %d", idx)
	}

	if err := c.RenameWindow(ctx, testSession, "palmux:bash:second", "palmux:bash:renamed"); err != nil {
		t.Fatalf("RenameWindow: %v", err)
	}
	if _, err := c.WindowIndexByName(ctx, testSession, "palmux:bash:renamed"); err != nil {
		t.Fatalf("renamed window not found: %v", err)
	}
	if _, err := c.WindowIndexByName(ctx, testSession, "palmux:bash:second"); err == nil {
		t.Fatal("old window name still resolvable after rename")
	}

	if err := c.KillWindowByName(ctx, testSession, "palmux:bash:renamed"); err != nil {
		t.Fatalf("KillWindowByName: %v", err)
	}
	if _, err := c.WindowIndexByName(ctx, testSession, "palmux:bash:renamed"); err == nil {
		t.Fatal("killed window still resolvable")
	}

	if err := c.KillSession(ctx, testSession); err != nil {
		t.Fatalf("KillSession: %v", err)
	}
	has, err = c.HasSession(ctx, testSession)
	if err != nil {
		t.Fatalf("HasSession after kill: %v", err)
	}
	if has {
		t.Fatal("session still present after KillSession")
	}
}

func TestExecClient_HasSession_NoServer(t *testing.T) {
	skipIfNoTmux(t)
	c := NewExecClient()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	has, err := c.HasSession(ctx, "_palmux2_definitely_does_not_exist_xyz")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if has {
		t.Error("expected has=false for non-existent session")
	}
}
