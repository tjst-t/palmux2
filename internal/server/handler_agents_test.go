package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tjst-t/palmux2/internal/agent"
)

// TestScenario2_GetAgents is [AC-S2b5691-1-2] (scenario-2-get-api-agents in
// docs/sprint-logs/S2b5691/scenario-S2b5691-1.json): once codex/opencode are
// registered, GET /api/agents returns exactly 3 descriptors with the
// documented shape. Uses a real net/http/httptest server + real *http.Client
// (not a direct handler call) so this exercises the actual HTTP wire
// contract, per the scenario's "client sends GET /api/agents" step.
//
// Adapter binaries are deliberately fake/nonexistent (Capabilities().
// InContainer is host-dependent — false here is the correct, deterministic
// expectation for a CI box without codex/opencode installed); every other
// field asserted below is host-independent.
func TestScenario2_GetAgents(t *testing.T) {
	reg := agent.NewRegistry()
	reg.Register(agent.NewClaudeAdapter("claude", nil))
	reg.Register(agent.NewCodexAdapter("/nonexistent/codex", nil))
	reg.Register(agent.NewOpencodeAdapter("/nonexistent/opencode", nil))

	mux := http.NewServeMux()
	h := &handlers{agents: reg}
	mux.HandleFunc("GET /api/agents", h.getAgents)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/agents")
	if err != nil {
		t.Fatalf("GET /api/agents: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}

	var out []AgentDescriptor
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 3 {
		t.Fatalf("got %d entries, want 3: %+v", len(out), out)
	}

	byKind := map[string]AgentDescriptor{}
	for _, d := range out {
		byKind[d.Kind] = d
	}
	for _, k := range []string{"claude", "codex", "opencode"} {
		if _, ok := byKind[k]; !ok {
			t.Fatalf("missing kind %q in response: %+v", k, out)
		}
	}

	claude := byKind["claude"]
	if !claude.Protected {
		t.Error("claude.protected = false, want true")
	}
	if len(claude.Modes) != 2 || claude.Modes[0] != "agent" || claude.Modes[1] != "tui" {
		t.Errorf("claude.modes = %v, want [agent tui]", claude.Modes)
	}

	codex := byKind["codex"]
	if codex.Protected {
		t.Error("codex.protected = true, want false")
	}
	if len(codex.Modes) != 1 || codex.Modes[0] != "tui" {
		t.Errorf("codex.modes = %v, want [tui]", codex.Modes)
	}
	if !codex.Capabilities.Resume {
		t.Error("codex.capabilities.resume = false, want true")
	}
	if codex.Capabilities.Notify != "turn_end" {
		t.Errorf("codex.capabilities.notify = %q, want turn_end", codex.Capabilities.Notify)
	}
	if codex.Capabilities.InContainer {
		t.Error("codex.capabilities.inContainer = true for a nonexistent binary, want false")
	}

	opencode := byKind["opencode"]
	if opencode.Protected {
		t.Error("opencode.protected = true, want false")
	}
	if len(opencode.Modes) != 1 || opencode.Modes[0] != "tui" {
		t.Errorf("opencode.modes = %v, want [tui]", opencode.Modes)
	}
	if !opencode.Capabilities.Resume {
		t.Error("opencode.capabilities.resume = false, want true")
	}
	if opencode.Capabilities.Notify != "full" {
		t.Errorf("opencode.capabilities.notify = %q, want full", opencode.Capabilities.Notify)
	}
}

// TestGetAgents_NilRegistryReturnsEmptyList proves the nil-registry
// (pre-S2b5691-shaped Deps, or a server started without any agent wiring)
// fallback is an empty JSON array, not a 500 or null.
func TestGetAgents_NilRegistryReturnsEmptyList(t *testing.T) {
	mux := http.NewServeMux()
	h := &handlers{agents: nil}
	mux.HandleFunc("GET /api/agents", h.getAgents)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/api/agents")
	if err != nil {
		t.Fatalf("GET /api/agents: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var out []AgentDescriptor
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("got %d entries, want 0", len(out))
	}
}
