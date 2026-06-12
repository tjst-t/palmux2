package incus

import (
	"context"
	"encoding/json"
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
type fakeCaddyAdmin struct {
	mu          sync.Mutex
	lastPostURL string
	lastBody    map[string]any
	deletedIDs  []string
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
		f.mu.Lock()
		defer f.mu.Unlock()
		f.lastPostURL = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&f.lastBody)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("DELETE /id/{id}", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.deletedIDs = append(f.deletedIDs, r.PathValue("id"))
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
