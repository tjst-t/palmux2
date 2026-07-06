package incus

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

func TestLabelDerivation(t *testing.T) {
	cases := []struct {
		repoID, branchID         string
		wantRepoLbl, wantWsLabel string
	}{
		{"tjst-t--demo-repo--ab12", "feature--cd34", "demo-repo-ab12", "feature-cd34"},
		{"owner--my.repo--ffff", "feat/x--0001", "my-repo-ffff", "feat-x-0001"},
		{"noformat", "single", "noformat", "single"},
	}
	for _, c := range cases {
		if got := repoLabelFromID(c.repoID); got != c.wantRepoLbl {
			t.Errorf("repoLabelFromID(%q)=%q want %q", c.repoID, got, c.wantRepoLbl)
		}
		if got := wsLabelFromID(c.branchID); got != c.wantWsLabel {
			t.Errorf("wsLabelFromID(%q)=%q want %q", c.branchID, got, c.wantWsLabel)
		}
	}
}

func TestPublishSubdomain(t *testing.T) {
	p := &publishConfig{baseDomain: "example.com", wsLabel: "feature", repoLabel: "demo-repo"}
	if got := p.subdomain(5173); got != "5173--feature--demo-repo.example.com" {
		t.Fatalf("subdomain = %q", got)
	}
	if got := p.publicURL(5173); got != "https://5173--feature--demo-repo.example.com" {
		t.Fatalf("publicURL = %q", got)
	}
}

// fakeCaddyAdmin emulates the subset of the Caddy admin API the client uses.
// It stores routes by @id so GET /id/<id> reflects the current config (the way
// real Caddy does), which lets tests exercise the no-op-skip idempotency guard.
type fakeCaddyAdmin struct {
	mu          sync.Mutex
	lastPostURL string
	lastBody    map[string]any
	deletedIDs  []string
	putCount    int
	routes      map[string][]byte // @id → stored raw route JSON
}

func (f *fakeCaddyAdmin) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /config/apps/http/servers", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"srv0": map[string]any{"listen": []string{":443"}},
			"srvX": map[string]any{"listen": []string{":80"}},
		})
	})
	// palmux inserts at the front: PUT .../routes/0
	mux.HandleFunc("PUT /config/apps/http/servers/{srv}/routes/{idx}", func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastPostURL = r.URL.Path
		f.putCount++
		f.lastBody = nil
		_ = json.Unmarshal(raw, &f.lastBody)
		if id, ok := f.lastBody["@id"].(string); ok {
			if f.routes == nil {
				f.routes = map[string][]byte{}
			}
			f.routes[id] = raw
		}
		w.WriteHeader(http.StatusOK)
	})
	// GET /id/<id> — return the stored route (200) or 404, like real Caddy.
	mux.HandleFunc("GET /id/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		raw, ok := f.routes[r.PathValue("id")]
		f.mu.Unlock()
		if !ok {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
	})
	mux.HandleFunc("DELETE /id/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		f.mu.Lock()
		f.deletedIDs = append(f.deletedIDs, id)
		delete(f.routes, id)
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func TestUpsertRoute_WithAuthTargetsSrv443(t *testing.T) {
	fake := &fakeCaddyAdmin{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newCaddyAdminClient(srv.URL)
	err := c.upsertRoute(context.Background(), "palmux-inst-5173",
		"5173--feature--demo-repo.example.com", "10.0.0.5:5173", "127.0.0.1:8080", true)
	if err != nil {
		t.Fatalf("upsertRoute: %v", err)
	}

	// It must insert into the :443 server (srv0) at the front, not srvX.
	if !strings.Contains(fake.lastPostURL, "/servers/srv0/routes/0") {
		t.Fatalf("route insert went to %q, want srv0 index 0", fake.lastPostURL)
	}
	// It must first DELETE the existing @id (idempotent upsert).
	if len(fake.deletedIDs) != 1 || fake.deletedIDs[0] != "palmux-inst-5173" {
		t.Fatalf("expected delete of @id palmux-inst-5173, got %v", fake.deletedIDs)
	}

	// Body assertions: @id, host match, forward_auth subroute (→ /auth/verify on
	// palmux), and the backend reverse_proxy to the container.
	body := fake.lastBody
	if body["@id"] != "palmux-inst-5173" {
		t.Errorf("@id = %v", body["@id"])
	}
	raw, _ := json.Marshal(body)
	s := string(raw)
	for _, want := range []string{
		`"host":["5173--feature--demo-repo.example.com"]`,
		`"handler":"subroute"`,
		`"uri":"/auth/verify"`,
		`"X-Forwarded-Uri"`,
		`"dial":"127.0.0.1:8080"`, // forward_auth → palmux
		`"dial":"10.0.0.5:5173"`,  // backend → container
		`"terminal":true`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("route body missing %s\nbody=%s", want, s)
		}
	}
	if strings.Contains(s, "http_basic") {
		t.Errorf("forward_auth route must NOT contain basic_auth\nbody=%s", s)
	}
}

func TestUpsertRoute_PublicOmitsAuth(t *testing.T) {
	fake := &fakeCaddyAdmin{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newCaddyAdminClient(srv.URL)
	if err := c.upsertRoute(context.Background(), "palmux-inst-8080",
		"8080--x--y.example.com", "10.0.0.5:8080", "127.0.0.1:8080", false); err != nil {
		t.Fatalf("upsertRoute: %v", err)
	}
	raw, _ := json.Marshal(fake.lastBody)
	if strings.Contains(string(raw), "subroute") || strings.Contains(string(raw), "/auth/verify") {
		t.Errorf("public route must NOT contain forward_auth\nbody=%s", raw)
	}
	if !strings.Contains(string(raw), `"handler":"reverse_proxy"`) || !strings.Contains(string(raw), `"dial":"10.0.0.5:8080"`) {
		t.Errorf("public route must reverse_proxy straight to the container\nbody=%s", raw)
	}
}

// TestUpsertRoute_SkipsNoOpRePut is the regression guard for the 10s Caddy
// full-reload cycle that dropped every in-flight WebSocket (incl. Vite HMR):
// the scan loop re-injects each route every ~10s, and an unconditional
// delete+PUT reloads Caddy each time. When the stored route is byte-identical,
// upsertRoute must skip the delete+PUT entirely.
func TestUpsertRoute_SkipsNoOpRePut(t *testing.T) {
	fake := &fakeCaddyAdmin{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	c := newCaddyAdminClient(srv.URL)

	up := func(upstream string) {
		t.Helper()
		if err := c.upsertRoute(context.Background(), "palmux-inst-8203",
			"8203--loamium--loamium.ndev.example", upstream, "127.0.0.1:8080", true); err != nil {
			t.Fatalf("upsertRoute: %v", err)
		}
	}

	// 1st call: route absent → must PUT (self-heal / initial inject). This one
	// also DELETEs by @id (belt-and-braces before the front-insert).
	up("10.0.0.9:8203")
	if fake.putCount != 1 {
		t.Fatalf("first upsert: putCount=%d want 1", fake.putCount)
	}
	deletesAfterInject := len(fake.deletedIDs)

	// 2nd & 3rd calls: identical desired route already stored → must SKIP,
	// issuing NO further PUT and NO further DELETE → Caddy is NOT reloaded.
	up("10.0.0.9:8203")
	up("10.0.0.9:8203")
	if fake.putCount != 1 {
		t.Fatalf("no-op re-upsert PUT'd again: putCount=%d want 1 (would reload Caddy every tick)", fake.putCount)
	}
	if len(fake.deletedIDs) != deletesAfterInject {
		t.Fatalf("no-op re-upsert issued extra DELETE(s): %v (grew past %d → reloads Caddy)", fake.deletedIDs, deletesAfterInject)
	}

	// 4th call: upstream changed (container IP moved) → must re-inject (PUT).
	up("10.0.0.42:8203")
	if fake.putCount != 2 {
		t.Fatalf("changed upstream did not re-inject: putCount=%d want 2", fake.putCount)
	}
}

// TestUpsertRoute_ReinjectsAfterCaddyDroppedIt proves self-heal survives the
// idempotency guard: if Caddy dropped the route (reload/restart), GET /id 404s
// and upsertRoute must PUT again.
func TestUpsertRoute_ReinjectsAfterCaddyDroppedIt(t *testing.T) {
	fake := &fakeCaddyAdmin{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()
	c := newCaddyAdminClient(srv.URL)

	up := func() {
		if err := c.upsertRoute(context.Background(), "palmux-inst-3000",
			"3000--a--b.ndev.example", "10.0.0.9:3000", "127.0.0.1:8080", false); err != nil {
			t.Fatalf("upsertRoute: %v", err)
		}
	}
	up()
	if fake.putCount != 1 {
		t.Fatalf("initial putCount=%d want 1", fake.putCount)
	}
	// Simulate Caddy dropping the admin-API route on reload.
	fake.mu.Lock()
	delete(fake.routes, "palmux-inst-3000")
	fake.mu.Unlock()

	up()
	if fake.putCount != 2 {
		t.Fatalf("did not re-inject after route dropped: putCount=%d want 2", fake.putCount)
	}
}

func TestDeleteRoute(t *testing.T) {
	fake := &fakeCaddyAdmin{}
	srv := httptest.NewServer(fake.handler())
	defer srv.Close()

	c := newCaddyAdminClient(srv.URL)
	if err := c.deleteRoute(context.Background(), "palmux-inst-3000"); err != nil {
		t.Fatalf("deleteRoute: %v", err)
	}
	if len(fake.deletedIDs) != 1 || fake.deletedIDs[0] != "palmux-inst-3000" {
		t.Fatalf("deletedIDs = %v", fake.deletedIDs)
	}
}
