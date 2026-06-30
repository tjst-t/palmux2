package store

import (
	"context"
	"testing"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tab/bash"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// vanishingTmux models a host tmux where the FIRST session create "succeeds" but
// the session immediately disappears — its first window's command exited,
// closing the window → session → (last session) → server — so HasSession is
// false and ListWindows reports the server gone. The 2nd create persists. This
// is exactly the edge that made an incus→host runtime switch abort with a
// cryptic "ensureSession: list-windows ... no server".
type vanishingTmux struct {
	*tmux.MockClient
	creates int
}

func (v *vanishingTmux) NewSession(ctx context.Context, opts tmux.NewSessionOpts) error {
	v.creates++
	if v.creates == 1 {
		return nil // "created", then vanished — deliberately NOT recorded
	}
	return v.MockClient.NewSession(ctx, opts)
}

func (v *vanishingTmux) HasSession(ctx context.Context, name string) (bool, error) {
	if v.creates <= 1 {
		return false, nil
	}
	return v.MockClient.HasSession(ctx, name)
}

func (v *vanishingTmux) ListWindows(ctx context.Context, session string) ([]tmux.Window, error) {
	if v.creates <= 1 {
		// The fixed ListWindows turns a "no server" into empty, not an error.
		return nil, nil
	}
	return v.MockClient.ListWindows(ctx, session)
}

// TestEnsureSession_RecoversWhenFirstWindowVanishes verifies the hardening: when
// the freshly-created session disappears (first window's process exits), the
// final guarantee re-establishes a live session instead of returning the cryptic
// list-windows error that failed an incus→host runtime switch.
func TestEnsureSession_RecoversWhenFirstWindowVanishes(t *testing.T) {
	dir := t.TempDir()
	repoStore, err := config.NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	settings, err := config.NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("NewSettingsStore: %v", err)
	}
	registry := tab.NewRegistry()
	registry.Register(bash.New())

	vt := &vanishingTmux{MockClient: tmux.NewMockClient()}
	s, err := New(Deps{Tmux: vt, RepoStore: repoStore, Settings: settings, Registry: registry, GHQRoot: dir})
	if err != nil {
		t.Fatalf("New store: %v", err)
	}

	repoID := "tjst-t--demo--abcd"
	branchID := injectBranch(t, s, repoID, "/tmp/repo-demo", "main", true)

	var branch *domain.Branch
	s.mu.RLock()
	for _, b := range s.repos[repoID].OpenBranches {
		if b.ID == branchID {
			branch = b
		}
	}
	s.mu.RUnlock()
	if branch == nil {
		t.Fatal("injected branch not found")
	}

	windows := []tab.WindowSpec{{Name: domain.WindowName("bash", "bash")}}
	if err := s.ensureSession(context.Background(), branch, windows); err != nil {
		t.Fatalf("ensureSession must recover from a vanished first window, got: %v", err)
	}
	if vt.creates < 2 {
		t.Fatalf("expected the session to be re-created after it vanished; creates=%d", vt.creates)
	}
	if live, _ := vt.MockClient.HasSession(context.Background(), branch.TabSet.TmuxSession); !live {
		t.Fatal("expected a live session to remain after recovery")
	}
}
