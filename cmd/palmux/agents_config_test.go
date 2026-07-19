package main

import (
	"testing"

	"github.com/tjst-t/palmux2/internal/agent"
	"github.com/tjst-t/palmux2/internal/config"
)

// TestScenario1_ConfigAndRegistry is the unit-level half of
// [AC-S2b5691-1-1] (scenario-1-config-and-registry in
// docs/sprint-logs/S2b5691/scenario-S2b5691-1.json): an operator's
// config.toml [agents.codex]/[agents.opencode] sections, translated by
// translateAgentConfig and fed to agent.BuildRegistry exactly as run() does,
// must produce a Registry containing "claude", "codex", "opencode" in that
// order, with codex/opencode dispatched to the real *agent.CodexAdapter /
// *agent.OpencodeAdapter (D3: built-in dispatch, never a GenericAdapter).
func TestScenario1_ConfigAndRegistry(t *testing.T) {
	enabled := true
	sections := map[string]config.AgentSection{
		"codex":    {Enabled: &enabled, Command: "codex"},
		"opencode": {Enabled: &enabled, Command: "opencode"},
	}

	reg, err := agent.BuildRegistry("claude", nil, translateAgentConfig(sections))
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}

	gotKinds := reg.Kinds()
	wantKinds := []agent.Kind{agent.KindClaude, agent.KindCodex, agent.KindOpencode}
	if len(gotKinds) != len(wantKinds) {
		t.Fatalf("Kinds() = %v, want %v", gotKinds, wantKinds)
	}
	for i, k := range wantKinds {
		if gotKinds[i] != k {
			t.Errorf("Kinds()[%d] = %q, want %q (order matters — GET /api/agents relies on it)", i, gotKinds[i], k)
		}
	}

	codexAdapter, ok := reg.Get(agent.KindCodex)
	if !ok {
		t.Fatal("registry has no codex entry")
	}
	if _, ok := codexAdapter.(*agent.CodexAdapter); !ok {
		t.Errorf("codex adapter is %T, want *agent.CodexAdapter (D3: built-in dispatch, not GenericAdapter)", codexAdapter)
	}

	opencodeAdapter, ok := reg.Get(agent.KindOpencode)
	if !ok {
		t.Fatal("registry has no opencode entry")
	}
	if _, ok := opencodeAdapter.(*agent.OpencodeAdapter); !ok {
		t.Errorf("opencode adapter is %T, want *agent.OpencodeAdapter (D3: built-in dispatch, not GenericAdapter)", opencodeAdapter)
	}
}

// TestScenario1_NoAgentsSectionStaysClaudeOnly proves the default-unchanged
// half of the AC: an empty/absent config.toml [agents.*] surface (nil map,
// mirroring translateAgentConfig's own "len==0 -> nil" contract) keeps the
// registry claude-only — codex/opencode are never auto-enabled by binary
// presence (D3, priority 7 明示的>暗黙的).
func TestScenario1_NoAgentsSectionStaysClaudeOnly(t *testing.T) {
	reg, err := agent.BuildRegistry("claude", nil, translateAgentConfig(nil))
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	kinds := reg.Kinds()
	if len(kinds) != 1 || kinds[0] != agent.KindClaude {
		t.Fatalf("Kinds() = %v, want [claude] only (no config.toml [agents.*] section present)", kinds)
	}
}

// TestTranslateAgentConfig_MapsFields proves translateAgentConfig correctly
// carries every AgentSection field into agent.AgentConfigEntry, including
// the Enabled=false / BinOrCommand synonym resolution.
func TestTranslateAgentConfig_MapsFields(t *testing.T) {
	disabled := false
	sections := map[string]config.AgentSection{
		"codex":   {Command: "codex", Args: []string{"--foo"}},
		"myagent": {Bin: "myagent-bin", DisplayName: "My Agent", ResumeArgs: []string{"--resume", "{session_id}"}, ContainerCommand: "/usr/bin/myagent"},
		"off":     {Enabled: &disabled, Command: "off-bin"},
	}
	got := translateAgentConfig(sections)

	codex := got["codex"]
	if !codex.Enabled || codex.Command != "codex" || len(codex.Args) != 1 || codex.Args[0] != "--foo" {
		t.Errorf("codex entry = %+v, unexpected", codex)
	}

	my := got["myagent"]
	if !my.Enabled || my.Command != "myagent-bin" || my.DisplayName != "My Agent" ||
		my.ContainerCommand != "/usr/bin/myagent" || len(my.ResumeArgs) != 2 {
		t.Errorf("myagent entry = %+v, unexpected (BinOrCommand should read Bin when Command is empty)", my)
	}

	off := got["off"]
	if off.Enabled {
		t.Errorf("off entry Enabled = true, want false (explicit enabled=false)")
	}
}
