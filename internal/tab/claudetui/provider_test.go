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

// providerTestTabID is the tabID consistently used across the route /
// branch-close tests so that the URL path segment ({tabId}) and the
// Manager key both refer to the same daemon.
const providerTestTabID = "claude:claude"

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
//
// Sadf90e collapsed the previous Claude(TUI) tab into a service-only Provider:
//   - it no longer surfaces a tab from OnBranchOpen
//   - Limits is {0, 0}
//   - Conditional() is still true so the lifecycle hooks run during
//     store.recomputeTabs.
func TestProviderShape(t *testing.T) {
	p := newTestProvider(t)

	if got := p.Type(); got != "claude-tui" {
		t.Errorf("Type() = %q, want %q", got, "claude-tui")
	}
	if got := p.DisplayName(); got == "" {
		t.Error("DisplayName() should be non-empty (diagnostic label)")
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
	if !p.Conditional() {
		// S7ce250-fix-2: must be true so store.recomputeTabs() invokes
		// OnBranchOpen and the lifecycle hooks run.
		t.Error("Conditional() should be true")
	}

	limits := p.Limits(nil)
	if limits.Min != 0 || limits.Max != 0 {
		t.Errorf("Limits() = {Min:%d, Max:%d}, want {0, 0} — Provider creates 0 tabs since Sadf90e",
			limits.Min, limits.Max)
	}

	// Verify it satisfies the tab.Provider interface at compile time.
	var _ tab.Provider = p
}

// --- TestOnBranchOpen ---------------------------------------------------------

// TestOnBranchOpenReturnsNoTabs verifies the Sadf90e invariant: the Provider
// no longer creates a tab from OnBranchOpen. The visible Claude tab is owned
// by the claudeagent Provider; this Provider only hosts the TUI runtime
// endpoints and the daemon is created lazily on first WS attach.
func TestOnBranchOpenReturnsNoTabs(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	branch := &domain.Branch{ID: "branch-1", RepoID: "repo-1", Name: "main"}
	result, err := p.OnBranchOpen(ctx, tab.OpenParams{Branch: branch})
	if err != nil {
		t.Fatalf("OnBranchOpen: %v", err)
	}
	if len(result.Tabs) != 0 {
		t.Errorf("Tabs len = %d, want 0 (Sadf90e — Provider no longer surfaces tabs)", len(result.Tabs))
	}
	// And no daemon is registered eagerly — spawn is fully lazy.
	if p.manager.Len() != 0 {
		t.Errorf("manager.Len() = %d, want 0 (lazy spawn)", p.manager.Len())
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

// TestOnBranchCloseReapsBranchDaemons verifies that OnBranchClose reaps every
// daemon belonging to the closing branch via Manager.CloseBranchDaemons —
// the Sadf90e replacement for per-tab close.
func TestOnBranchCloseReapsBranchDaemons(t *testing.T) {
	p := newTestProvider(t)
	ctx := context.Background()

	branch := &domain.Branch{ID: "branch-close", RepoID: "repo-close", Name: "feat"}

	// Two daemons on the closing branch + one on a different branch that must
	// survive.
	d1, err := p.manager.EnsureDaemon(ctx, branch.RepoID, branch.ID, "tab-A", "")
	if err != nil {
		t.Fatalf("EnsureDaemon tab-A: %v", err)
	}
	if _, err := p.manager.EnsureDaemon(ctx, branch.RepoID, branch.ID, "tab-B", ""); err != nil {
		t.Fatalf("EnsureDaemon tab-B: %v", err)
	}
	if _, err := p.manager.EnsureDaemon(ctx, "repo-other", "branch-other", providerTestTabID, ""); err != nil {
		t.Fatalf("EnsureDaemon other: %v", err)
	}
	if p.manager.Len() != 3 {
		t.Fatalf("Len() = %d after setup, want 3", p.manager.Len())
	}

	// Spawn one of them so we can verify the subprocess dies on close.
	if err := d1.EnsureStarted(ctx); err != nil {
		t.Fatalf("EnsureStarted: %v", err)
	}
	waitForState(t, d1, StateRunning, testTimeout)

	// Close the branch.
	if err := p.OnBranchClose(ctx, tab.CloseParams{Branch: branch}); err != nil {
		t.Fatalf("OnBranchClose: %v", err)
	}

	// Only the unrelated daemon should remain.
	if p.manager.Len() != 1 {
		t.Fatalf("Len() = %d after close, want 1 (sibling branch only)", p.manager.Len())
	}
	if got := p.manager.Get("repo-other", "branch-other", providerTestTabID); got == nil {
		t.Fatal("sibling branch's daemon should not have been reaped")
	}

	// The previously-spawned daemon must transition to StateShutdown.
	waitForState(t, d1, StateShutdown, testTimeout)
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
// expected paths (Sadf90e: keyed by {tabId}, not the fixed "claude-tui"
// segment) and that they respond (≠ 404 / method-not-allowed).
func TestRouteRegistration(t *testing.T) {
	p := newTestProvider(t)
	mux := http.NewServeMux()
	p.RegisterRoutes(mux, "")

	ctx := context.Background()
	// Pre-create the daemon under the same tabID the URLs will reference so
	// resize() (which uses Manager.Get) finds it.
	if _, err := p.manager.EnsureDaemon(ctx, "r1", "b1", providerTestTabID, ""); err != nil {
		t.Fatalf("EnsureDaemon: %v", err)
	}

	base := "/api/repos/r1/branches/b1/tabs/" + providerTestTabID + "/tui"

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
