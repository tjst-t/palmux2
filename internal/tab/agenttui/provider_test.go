package agenttui

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

// TestParticipantShape verifies the post-ADR-0012 contract: agenttui is a
// SERVICE PARTICIPANT, not a tab.Provider.
//
// History: Sadf90e collapsed the Claude(TUI) tab into a service-only Provider
// that returned zero tabs but declared Conditional()==true purely so
// store.recomputeTabs would call its lifecycle hook. ADR-0012 removed that
// masquerade — a visibility flag was doing the job of a subscription
// mechanism. There is now no way for this type to contribute a tab, which is
// stronger than the old "returns an empty slice" invariant: it is enforced by
// the type, not by the implementation remembering to return nothing.
func TestParticipantShape(t *testing.T) {
	p := newTestProvider(t)

	if got := p.Type(); got != "claude-tui" {
		t.Errorf("Type() = %q, want %q", got, "claude-tui")
	}

	// Compile-time: it is a Participant, and it is NOT a tab.Provider.
	var _ tab.Participant = p
	if _, isProvider := any(p).(tab.Provider); isProvider {
		t.Error("agenttui must NOT satisfy tab.Provider — it contributes no tabs (ADR-0012)")
	}
}

// TestRegisterServiceKeepsItOutOfTabDerivation is the behavioural half of the
// contract: a service participant never reaches the Store's tab-set
// derivation, which only ever iterates Providers().
func TestRegisterServiceKeepsItOutOfTabDerivation(t *testing.T) {
	p := newTestProvider(t)
	r := tab.NewRegistry()
	r.RegisterService(p)

	if n := len(r.Providers()); n != 0 {
		t.Errorf("Providers() = %d, want 0 — a service must not appear in tab derivation", n)
	}
	if n := len(r.Participants()); n != 1 {
		t.Errorf("Participants() = %d, want 1 — lifecycle dispatch must still reach it", n)
	}
	if got := r.Get("claude-tui"); got != nil {
		t.Error("Registry.Get must not resolve a service as a tab Provider")
	}
	// Spawn stays lazy: registration alone must not create a daemon.
	if p.manager.Len() != 0 {
		t.Errorf("manager.Len() = %d, want 0 (lazy spawn)", p.manager.Len())
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
