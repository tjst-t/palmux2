package claudeagent

import "testing"

// Sprint S004 — MCP server status round-trip.
//
// The CLI advertises connected MCP servers in its `system/init` line
// (mcp_servers[]). normalize.go calls Session.SetMCPServers; the
// Snapshot is what we ship to the freshly-connected client. This test
// locks both halves so future refactors don't silently drop the field.

func TestSystemInit_PopulatesMCPServersOnSnapshot(t *testing.T) {
	s := NewSession("repo", "branch", "", "sonnet", "default")
	body := `{
		"type":"system",
		"subtype":"init",
		"session_id":"sid-1",
		"model":"opus",
		"mcp_servers":[
			{"name":"palmux","status":"connected"},
			{"name":"github","status":"failed"},
			{"name":"linear","status":"connecting"}
		]
	}`
	processStreamMessage(s, parse(t, body))

	snap := s.Snapshot()
	if got, want := len(snap.MCPServers), 3; got != want {
		t.Fatalf("snapshot mcpServers count = %d, want %d", got, want)
	}
	want := []MCPServerInfo{
		{Name: "palmux", Status: "connected"},
		{Name: "github", Status: "failed"},
		{Name: "linear", Status: "connecting"},
	}
	for i, srv := range snap.MCPServers {
		if srv != want[i] {
			t.Fatalf("snapshot.MCPServers[%d] = %+v, want %+v", i, srv, want[i])
		}
	}
}

func TestSnapshot_MCPServers_IsADefensiveCopy(t *testing.T) {
	s := NewSession("repo", "branch", "", "sonnet", "default")
	s.SetMCPServers([]MCPServerInfo{{Name: "palmux", Status: "connected"}})

	snap := s.Snapshot()
	if len(snap.MCPServers) != 1 {
		t.Fatalf("baseline len = %d, want 1", len(snap.MCPServers))
	}
	// Mutating the snapshot must not poison the live session.
	snap.MCPServers[0].Status = "MUTATED"

	again := s.Snapshot()
	if got := again.MCPServers[0].Status; got != "connected" {
		t.Fatalf("session leaked mutation: status = %q, want connected", got)
	}
}

func TestSnapshot_NoMCPServers_EmptyByDefault(t *testing.T) {
	s := NewSession("repo", "branch", "", "sonnet", "default")
	snap := s.Snapshot()
	if len(snap.MCPServers) != 0 {
		t.Fatalf("default snapshot should have no MCP servers, got %v", snap.MCPServers)
	}
}
