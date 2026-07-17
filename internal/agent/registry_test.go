package agent

import (
	"strings"
	"testing"
)

func TestBuildRegistryAlwaysRegistersClaude(t *testing.T) {
	r, err := BuildRegistry("claude", nil, nil)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	a, ok := r.Get(KindClaude)
	if !ok {
		t.Fatal("BuildRegistry: claude adapter missing")
	}
	if a.Kind() != KindClaude {
		t.Errorf("Kind() = %q, want %q", a.Kind(), KindClaude)
	}
	if len(r.Kinds()) != 1 {
		t.Errorf("Kinds() = %v, want just [claude]", r.Kinds())
	}
}

func TestBuildRegistryFromFakeConfig(t *testing.T) {
	agents := map[string]AgentConfigEntry{
		"dummy": {
			Enabled:     true,
			DisplayName: "Dummy",
			Command:     "bash",
			Args:        []string{"--norc"},
			ResumeArgs:  []string{"--resume", "{session_id}"},
		},
		"disabled-agent": {
			Enabled: false,
			Command: "cat",
		},
		"no-command": {
			Enabled: true,
			// Command intentionally empty — must be skipped.
		},
	}
	r, err := BuildRegistry("claude", []string{"--foo"}, agents)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}

	kinds := r.Kinds()
	want := []Kind{KindClaude, "dummy"}
	if len(kinds) != len(want) {
		t.Fatalf("Kinds() = %v, want %v", kinds, want)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Errorf("Kinds()[%d] = %q, want %q", i, kinds[i], k)
		}
	}

	dummy, ok := r.Get("dummy")
	if !ok {
		t.Fatal("BuildRegistry: dummy adapter missing")
	}
	if dummy.DisplayName() != "Dummy" {
		t.Errorf("DisplayName() = %q, want %q", dummy.DisplayName(), "Dummy")
	}
	caps := dummy.Capabilities()
	if !caps.Resume {
		t.Error("dummy Capabilities().Resume = false, want true (resume_args declared)")
	}
	if caps.Notify != NotifyNone {
		t.Errorf("dummy Notify = %q, want none", caps.Notify)
	}

	if _, ok := r.Get("disabled-agent"); ok {
		t.Error("disabled-agent should not be registered (enabled=false)")
	}
	if _, ok := r.Get("no-command"); ok {
		t.Error("no-command should not be registered (empty command)")
	}
}

func TestBuildRegistryClaudeEntryIgnoredNotDoubleRegistered(t *testing.T) {
	// A [agents.claude] entry (used for the bin/args override at the config
	// layer) must not cause a second claude registration or attempt to spawn
	// a GenericAdapter for "claude".
	agents := map[string]AgentConfigEntry{
		"claude": {Enabled: true, Command: "claude-override", Args: []string{"--effort", "high"}},
	}
	r, err := BuildRegistry("claude", nil, agents)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if len(r.Kinds()) != 1 {
		t.Fatalf("Kinds() = %v, want exactly [claude]", r.Kinds())
	}
	a, _ := r.Get(KindClaude)
	if _, isGeneric := a.(*GenericAdapter); isGeneric {
		t.Error("claude must stay the built-in ClaudeAdapter, not a GenericAdapter")
	}
}

// TestBuildRegistryRejectsReservedKind is the Sdec0a7-2 review fix 2
// regression: a [agents.<name>] section that reuses a built-in tab type
// (files/git/bash/sprint/ports/browser/claude-tui) must return a clear
// error, NOT panic later at tab.Registry.Register. Verified for every
// reserved name, including a disabled one and one with no command (still a
// config mistake).
func TestBuildRegistryRejectsReservedKind(t *testing.T) {
	for _, name := range []string{"files", "git", "bash", "sprint", "ports", "browser", "claude-tui"} {
		agents := map[string]AgentConfigEntry{
			name: {Enabled: true, Command: "whatever"},
		}
		r, err := BuildRegistry("claude", nil, agents)
		if err == nil {
			t.Errorf("BuildRegistry with reserved name %q: want error, got nil (registry=%v)", name, r.Kinds())
			continue
		}
		if !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "reserved") {
			t.Errorf("BuildRegistry(%q) error = %q, want it to mention %q and 'reserved'", name, err.Error(), name)
		}
	}

	// Rejection fires even for a disabled reserved section (still a mistake).
	if _, err := BuildRegistry("claude", nil, map[string]AgentConfigEntry{
		"files": {Enabled: false},
	}); err == nil {
		t.Error("BuildRegistry with disabled [agents.files]: want error, got nil")
	}

	// A normal, non-reserved kind still registers fine alongside the check.
	r, err := BuildRegistry("claude", nil, map[string]AgentConfigEntry{
		"dummy": {Enabled: true, Command: "bash"},
	})
	if err != nil {
		t.Fatalf("BuildRegistry with normal kind: unexpected error %v", err)
	}
	if _, ok := r.Get("dummy"); !ok {
		t.Error("normal 'dummy' kind should still register")
	}
}

func TestIsReservedKind(t *testing.T) {
	for _, n := range []string{"files", "git", "bash", "sprint", "ports", "browser", "claude-tui"} {
		if !IsReservedKind(n) {
			t.Errorf("IsReservedKind(%q) = false, want true", n)
		}
	}
	// claude is NOT reserved here — it is the built-in override, handled
	// separately.
	if IsReservedKind("claude") {
		t.Error("IsReservedKind(\"claude\") = true, want false (claude is the override, not a collision)")
	}
	if IsReservedKind("dummy") {
		t.Error("IsReservedKind(\"dummy\") = true, want false")
	}
}

// TestBuildRegistryCodexBuiltIn is the S339021-1 counterpart of
// TestBuildRegistryClaudeEntryIgnoredNotDoubleRegistered: an enabled
// [agents.codex] section must construct the real built-in *CodexAdapter
// (turn-end notify + real resume), not a template-driven *GenericAdapter.
func TestBuildRegistryCodexBuiltIn(t *testing.T) {
	agents := map[string]AgentConfigEntry{
		"codex": {Enabled: true, Command: "codex", Args: []string{"--model", "o3"}},
	}
	r, err := BuildRegistry("claude", nil, agents)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	a, ok := r.Get(KindCodex)
	if !ok {
		t.Fatal("BuildRegistry: codex adapter missing")
	}
	if _, isGeneric := a.(*GenericAdapter); isGeneric {
		t.Error("codex must be the built-in CodexAdapter, not a GenericAdapter")
	}
	if _, isCodex := a.(*CodexAdapter); !isCodex {
		t.Errorf("codex adapter has type %T, want *CodexAdapter", a)
	}
	caps := a.Capabilities()
	if !caps.Resume || caps.Notify != NotifyTurnEnd {
		t.Errorf("codex Capabilities = %+v, want Resume=true Notify=turn_end", caps)
	}

	kinds := r.Kinds()
	want := []Kind{KindClaude, KindCodex}
	if len(kinds) != len(want) || kinds[0] != want[0] || kinds[1] != want[1] {
		t.Errorf("Kinds() = %v, want %v", kinds, want)
	}
}

// TestBuildRegistryCodexDisabledOrNoCommandSkipped mirrors the generic-agent
// enabled/command gating (TestBuildRegistryFromFakeConfig) for the codex
// special-case branch specifically.
func TestBuildRegistryCodexDisabledOrNoCommandSkipped(t *testing.T) {
	r, err := BuildRegistry("claude", nil, map[string]AgentConfigEntry{
		"codex": {Enabled: false, Command: "codex"},
	})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := r.Get(KindCodex); ok {
		t.Error("disabled [agents.codex] should not register")
	}

	r, err = BuildRegistry("claude", nil, map[string]AgentConfigEntry{
		"codex": {Enabled: true},
	})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := r.Get(KindCodex); ok {
		t.Error("[agents.codex] with no command should not register")
	}
}

func TestIsReservedKindCodexNotReserved(t *testing.T) {
	if IsReservedKind("codex") {
		t.Error("IsReservedKind(\"codex\") = true, want false (codex is a built-in override, not a collision)")
	}
}

// TestBuildRegistryOpencodeBuiltIn is the S339021-2 counterpart of
// TestBuildRegistryCodexBuiltIn: an enabled [agents.opencode] section must
// construct the real built-in *OpencodeAdapter (full notify + real cwd-match
// resume), not a template-driven *GenericAdapter.
func TestBuildRegistryOpencodeBuiltIn(t *testing.T) {
	agents := map[string]AgentConfigEntry{
		"opencode": {Enabled: true, Command: "opencode", Args: []string{"--model", "anthropic/claude"}},
	}
	r, err := BuildRegistry("claude", nil, agents)
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	a, ok := r.Get(KindOpencode)
	if !ok {
		t.Fatal("BuildRegistry: opencode adapter missing")
	}
	if _, isGeneric := a.(*GenericAdapter); isGeneric {
		t.Error("opencode must be the built-in OpencodeAdapter, not a GenericAdapter")
	}
	if _, isOpencode := a.(*OpencodeAdapter); !isOpencode {
		t.Errorf("opencode adapter has type %T, want *OpencodeAdapter", a)
	}
	caps := a.Capabilities()
	if !caps.Resume || caps.Notify != NotifyFull {
		t.Errorf("opencode Capabilities = %+v, want Resume=true Notify=full", caps)
	}

	kinds := r.Kinds()
	want := []Kind{KindClaude, KindOpencode}
	if len(kinds) != len(want) || kinds[0] != want[0] || kinds[1] != want[1] {
		t.Errorf("Kinds() = %v, want %v", kinds, want)
	}
}

// TestBuildRegistryOpencodeDisabledOrNoCommandSkipped mirrors
// TestBuildRegistryCodexDisabledOrNoCommandSkipped for the opencode
// special-case branch.
func TestBuildRegistryOpencodeDisabledOrNoCommandSkipped(t *testing.T) {
	r, err := BuildRegistry("claude", nil, map[string]AgentConfigEntry{
		"opencode": {Enabled: false, Command: "opencode"},
	})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := r.Get(KindOpencode); ok {
		t.Error("disabled [agents.opencode] should not register")
	}

	r, err = BuildRegistry("claude", nil, map[string]AgentConfigEntry{
		"opencode": {Enabled: true},
	})
	if err != nil {
		t.Fatalf("BuildRegistry: %v", err)
	}
	if _, ok := r.Get(KindOpencode); ok {
		t.Error("[agents.opencode] with no command should not register")
	}
}

func TestIsReservedKindOpencodeNotReserved(t *testing.T) {
	if IsReservedKind("opencode") {
		t.Error("IsReservedKind(\"opencode\") = true, want false (opencode is a built-in override, not a collision)")
	}
}

func TestRegistryAllPreservesOrder(t *testing.T) {
	r := NewRegistry()
	r.Register(NewGenericAdapter("b", GenericConfig{Command: "b"}))
	r.Register(NewGenericAdapter("a", GenericConfig{Command: "a"}))
	all := r.All()
	if len(all) != 2 || all[0].Kind() != "b" || all[1].Kind() != "a" {
		t.Errorf("All() = %v, want registration order [b, a]", all)
	}
}

// TestRegistrySharedContainerPaths (S339021) verifies aggregation: only
// InContainerProvider adapters whose ContainerBinary() resolves contribute
// paths; GenericAdapter (no InContainerProvider) and a codex/opencode
// adapter with an unresolvable binary both contribute nothing; claude (no
// InContainerProvider either, its own binary rides the static
// dot-local-bin/dot-claude shared devices unconditionally) contributes
// nothing here either.
func TestRegistrySharedContainerPaths(t *testing.T) {
	r := NewRegistry()
	r.Register(NewClaudeAdapter("claude", nil))
	r.Register(NewGenericAdapter("mygen", GenericConfig{Command: "mygen", ContainerCommand: "/opt/mygen"}))

	codexA := NewCodexAdapter("codex", nil)
	codexA.binResolver = func(string) (string, bool) { return "/resolved/codex", true }
	r.Register(codexA)

	unresolvableOpencode := NewOpencodeAdapter("opencode", nil)
	unresolvableOpencode.binResolver = func(string) (string, bool) { return "", false }
	r.Register(unresolvableOpencode)

	got := r.SharedContainerPaths()
	// codexA resolves -> contributes SharedContainerPaths() (binary share,
	// no ~/.codex on a throwaway HOME so just the binary share).
	found := false
	for _, p := range got {
		if p == "/resolved/codex" {
			found = true
		}
	}
	if !found {
		t.Errorf("SharedContainerPaths() = %v, want it to include the resolved codex binary share", got)
	}
	for _, p := range got {
		if p == "/opt/mygen" {
			t.Errorf("SharedContainerPaths() = %v, must NOT include GenericAdapter's container_command (not an InContainerProvider)", got)
		}
	}
}
