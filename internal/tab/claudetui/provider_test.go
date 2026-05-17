package claudetui

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
)

// testTimeout is the default deadline for state-transition polls in provider tests.
const testTimeout = 10 * time.Second

// newTestProvider returns a Provider backed by a Manager using the fake_claude
// binary so tests that spawn subprocesses don't require a real claude binary.
func newTestProvider(t *testing.T) *Provider {
	t.Helper()
	bin := fakeBin(t)
	mgr := NewManager(ManagerConfig{
		ClaudeBin: bin,
		RingSize:  1 << 16,
	})
	p := New(mgr)
	t.Cleanup(func() { mgr.ShutdownAll(context.Background()) })
	return p
}

// --- TestProviderShape --------------------------------------------------------

// TestProviderShape verifies the Provider's static interface method contracts.
func TestProviderShape(t *testing.T) {
	p := newTestProvider(t)

	if got := p.Type(); got != "claude-tui" {
		t.Errorf("Type() = %q, want %q", got, "claude-tui")
	}
	if got := p.DisplayName(); got != "Claude (TUI)" {
		t.Errorf("DisplayName() = %q, want %q", got, "Claude (TUI)")
	}
	if !p.Protected() {
		t.Error("Protected() should be true")
	}
	if p.Multiple() {
		t.Error("Multiple() should be false")
	}
	if p.NeedsTmuxWindow() {
		t.Error("NeedsTmuxWindow() should be false")
	}
	if p.Conditional() {
		t.Error("Conditional() should be false")
	}

	limits := p.Limits(nil)
	if limits.Min != 1 || limits.Max != 1 {
		t.Errorf("Limits() = {Min:%d, Max:%d}, want {1, 1}", limits.Min, limits.Max)
	}

	// Verify it satisfies the tab.Provider interface at compile time.
	var _ tab.Provider = p
}

// --- TestOnBranchOpen ---------------------------------------------------------

// TestOnBranchOpen verifies that opening a branch registers a Daemon in the
// Manager (Len goes 0→1) but does NOT spawn the subprocess (PID == 0).
func TestOnBranchOpen(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	if p.manager.Len() != 0 {
		t.Fatalf("manager.Len() = %d before open, want 0", p.manager.Len())
	}

	branch := &domain.Branch{
		ID:     "branch-1",
		RepoID: "repo-1",
		Name:   "main",
	}
	result, err := p.OnBranchOpen(ctx, tab.OpenParams{Branch: branch})
	if err != nil {
		t.Fatalf("OnBranchOpen: %v", err)
	}

	// Manager should now track exactly one daemon.
	if p.manager.Len() != 1 {
		t.Fatalf("manager.Len() = %d after open, want 1", p.manager.Len())
	}

	// The daemon must NOT have been spawned yet (lazy spawn — priority_rule 4).
	d := p.manager.Get("repo-1", "branch-1")
	if d == nil {
		t.Fatal("Get returned nil after OnBranchOpen")
	}
	stats := d.CurrentStats()
	if stats.PID != 0 {
		t.Errorf("PID = %d after OnBranchOpen, want 0 (no spawn yet)", stats.PID)
	}
	if stats.Alive {
		t.Error("daemon should not be alive (spawned) after OnBranchOpen")
	}
	if stats.State != "idle" {
		t.Errorf("state = %q, want %q", stats.State, "idle")
	}

	// The result should include exactly one claude-tui tab.
	if len(result.Tabs) != 1 {
		t.Fatalf("Tabs len = %d, want 1", len(result.Tabs))
	}
	tab := result.Tabs[0]
	if tab.Type != TabType {
		t.Errorf("tab.Type = %q, want %q", tab.Type, TabType)
	}
	if !tab.Protected {
		t.Error("tab.Protected should be true")
	}
}

// TestOnBranchOpenNilBranch ensures OnBranchOpen handles a nil Branch safely.
func TestOnBranchOpenNilBranch(t *testing.T) {
	p := newTestProvider(t)
	result, err := p.OnBranchOpen(context.Background(), tab.OpenParams{Branch: nil})
	if err != nil {
		t.Fatalf("OnBranchOpen(nil branch): unexpected error: %v", err)
	}
	if len(result.Tabs) != 0 {
		t.Errorf("expected 0 tabs for nil branch, got %d", len(result.Tabs))
	}
}

// --- TestOnBranchClose --------------------------------------------------------

// TestOnBranchClose verifies that closing a branch removes the daemon from the
// Manager and any spawned subprocess exits.
func TestOnBranchClose(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	branch := &domain.Branch{
		ID:     "branch-close",
		RepoID: "repo-close",
		Name:   "feat",
	}
	if _, err := p.OnBranchOpen(ctx, tab.OpenParams{Branch: branch}); err != nil {
		t.Fatalf("OnBranchOpen: %v", err)
	}
	if p.manager.Len() != 1 {
		t.Fatalf("Len() = %d after open, want 1", p.manager.Len())
	}

	// Spawn the subprocess so we can verify it dies on close.
	d := p.manager.Get("repo-close", "branch-close")
	if err := d.EnsureStarted(ctx); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d, StateRunning, testTimeout)
	if !d.CurrentStats().Alive {
		t.Fatal("daemon should be alive before close")
	}

	// Close the branch.
	if err := p.OnBranchClose(ctx, tab.CloseParams{Branch: branch}); err != nil {
		t.Fatalf("OnBranchClose: %v", err)
	}

	// Daemon must be removed from the Manager.
	if p.manager.Len() != 0 {
		t.Fatalf("Len() = %d after close, want 0", p.manager.Len())
	}

	// The previously-returned daemon should now be in StateShutdown.
	waitForState(t, d, StateShutdown, testTimeout)
}

// TestOnBranchCloseNilBranch ensures OnBranchClose handles a nil Branch safely.
func TestOnBranchCloseNilBranch(t *testing.T) {
	p := newTestProvider(t)
	if err := p.OnBranchClose(context.Background(), tab.CloseParams{Branch: nil}); err != nil {
		t.Fatalf("OnBranchClose(nil branch): unexpected error: %v", err)
	}
}

// --- TestRouteRegistration ----------------------------------------------------

// TestRouteRegistration verifies that RegisterRoutes registers the three
// expected paths and that they respond (≠ 404 / method-not-allowed).
func TestRouteRegistration(t *testing.T) {
	p := newTestProvider(t)
	mux := http.NewServeMux()
	p.RegisterRoutes(mux, "")

	ctx := context.Background()
	branch := &domain.Branch{ID: "b1", RepoID: "r1", Name: "main"}
	if _, err := p.OnBranchOpen(ctx, tab.OpenParams{Branch: branch}); err != nil {
		t.Fatalf("OnBranchOpen: %v", err)
	}

	base := "/api/repos/r1/branches/b1/tabs/claude-tui"

	// GET …/stats should return 200 (daemon exists, stats are served).
	t.Run("stats_200", func(t *testing.T) {
		req := httptest.NewRequest("GET", base+"/stats", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("GET /stats: status = %d, want 200", rec.Code)
		}
	})

	// POST …/resize before the subprocess is started should return 404 if no
	// daemon OR 500 if daemon exists but PTY not started.  Either way, we
	// verify the route IS registered (not a 404 "page not found").
	t.Run("resize_registered", func(t *testing.T) {
		body := bytes.NewBufferString(`{"cols":80,"rows":24}`)
		req := httptest.NewRequest("POST", base+"/resize", body)
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		// We expect 500 (daemon exists but PTY not started → Resize returns error).
		// The key assertion is that it is NOT a 404 "page not found".
		if rec.Code == http.StatusNotFound && strings.Contains(rec.Body.String(), "page not found") {
			t.Errorf("POST /resize not registered (got 404 page not found)")
		}
	})

	// GET …/attach is a WebSocket upgrade; we only verify the route is
	// registered (non-404) — a plain HTTP GET will get a 400 Bad Request
	// from the WS library when the upgrade headers are missing.
	t.Run("attach_registered", func(t *testing.T) {
		req := httptest.NewRequest("GET", base+"/attach", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code == http.StatusNotFound && strings.Contains(rec.Body.String(), "page not found") {
			t.Errorf("GET /attach not registered (got 404 page not found)")
		}
	})

	// Verify the stats response body is valid JSON with expected fields.
	t.Run("stats_json_shape", func(t *testing.T) {
		req := httptest.NewRequest("GET", base+"/stats", nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		var stats Stats
		if err := json.NewDecoder(rec.Body).Decode(&stats); err != nil {
			t.Fatalf("stats body is not valid Stats JSON: %v", err)
		}
		if stats.State == "" {
			t.Error("stats.State should not be empty")
		}
	})
}
