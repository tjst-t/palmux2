package agent

import (
	"reflect"
	"testing"
)

func TestGenericAdapterCapabilitiesDerivation(t *testing.T) {
	cases := []struct {
		name string
		cfg  GenericConfig
		want Capabilities
	}{
		{
			name: "bare command only",
			cfg:  GenericConfig{Command: "bash"},
			want: Capabilities{Resume: false, Notify: NotifyNone, InContainer: false, PermissionMode: false},
		},
		{
			name: "resume_args declared",
			cfg:  GenericConfig{Command: "bash", ResumeArgs: []string{"--resume", "{session_id}"}},
			want: Capabilities{Resume: true, Notify: NotifyNone, InContainer: false, PermissionMode: false},
		},
		{
			name: "container_command declared",
			cfg:  GenericConfig{Command: "bash", ContainerCommand: "/usr/bin/bash"},
			want: Capabilities{Resume: false, Notify: NotifyNone, InContainer: true, PermissionMode: false},
		},
		{
			name: "both declared",
			cfg: GenericConfig{
				Command:          "my-agent",
				ResumeArgs:       []string{"--resume", "{session_id}"},
				ContainerCommand: "/usr/local/bin/my-agent",
			},
			want: Capabilities{Resume: true, Notify: NotifyNone, InContainer: true, PermissionMode: false},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := NewGenericAdapter("dummy", tc.cfg)
			got := a.Capabilities()
			if got != tc.want {
				t.Errorf("Capabilities() = %+v, want %+v", got, tc.want)
			}
			// Notify is unconditionally None regardless of declared fields.
			if got.Notify != NotifyNone {
				t.Errorf("Notify = %q, want %q (generic agents never declare notify capability)", got.Notify, NotifyNone)
			}
		})
	}
}

func TestGenericAdapterDisplayNameFallsBackToKind(t *testing.T) {
	a := NewGenericAdapter("dummy", GenericConfig{Command: "bash"})
	if got := a.DisplayName(); got != "dummy" {
		t.Errorf("DisplayName() = %q, want %q (fallback to kind)", got, "dummy")
	}
	b := NewGenericAdapter("dummy", GenericConfig{Command: "bash", DisplayName: "Dummy Agent"})
	if got := b.DisplayName(); got != "Dummy Agent" {
		t.Errorf("DisplayName() = %q, want %q", got, "Dummy Agent")
	}
}

func TestGenericAdapterSpawnSpecFresh(t *testing.T) {
	a := NewGenericAdapter("dummy", GenericConfig{Command: "bash", Args: []string{"--norc"}})
	spec, err := a.SpawnSpec(SpawnIntent{})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"bash", "--norc"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
}

func TestGenericAdapterSpawnSpecResumeSubstitutesSessionID(t *testing.T) {
	a := NewGenericAdapter("dummy", GenericConfig{
		Command:    "my-agent",
		Args:       []string{"--interactive"},
		ResumeArgs: []string{"--resume", "{session_id}", "--tag={session_id}-r"},
	})
	spec, err := a.SpawnSpec(SpawnIntent{ResumeSessionID: "abc-123"})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"my-agent", "--interactive", "--resume", "abc-123", "--tag=abc-123-r"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
}

func TestGenericAdapterSpawnSpecFreshWhenNoResumeArgsDeclared(t *testing.T) {
	// Even if the caller passes a ResumeSessionID, an adapter with no
	// resume_args declared must not append anything — Capabilities.Resume
	// is false and nothing should be appended (mirrors "no resume args
	// declared, no resume attempted").
	a := NewGenericAdapter("dummy", GenericConfig{Command: "bash"})
	spec, err := a.SpawnSpec(SpawnIntent{ResumeSessionID: "abc-123"})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"bash"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
}

func TestGenericAdapterSpawnSpecInContainer(t *testing.T) {
	a := NewGenericAdapter("dummy", GenericConfig{
		Command:          "bash",
		ContainerCommand: "/usr/bin/bash",
	})
	spec, err := a.SpawnSpec(SpawnIntent{InContainer: true})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	if spec.Argv[0] != "/usr/bin/bash" {
		t.Errorf("Argv[0] = %q, want container command", spec.Argv[0])
	}
}

func TestGenericAdapterSpawnSpecInContainerWithoutContainerCommandErrors(t *testing.T) {
	// Defense-in-depth (D12): the primary enforcement is in
	// agenttui.Daemon.spawnWithArgs, but the adapter itself must never
	// silently fall back to the host bin path when asked to run
	// in-container without a declared container_command.
	a := NewGenericAdapter("dummy", GenericConfig{Command: "bash"})
	_, err := a.SpawnSpec(SpawnIntent{InContainer: true})
	if err == nil {
		t.Fatal("SpawnSpec: want error when InContainer requested without container_command, got nil")
	}
}

func TestGenericAdapterConfigurable(t *testing.T) {
	a := NewGenericAdapter("dummy", GenericConfig{Command: "bash", Args: []string{"-x"}})
	a.SetBin("zsh")
	a.SetArgs([]string{"-y"})
	spec, err := a.SpawnSpec(SpawnIntent{})
	if err != nil {
		t.Fatalf("SpawnSpec: %v", err)
	}
	want := []string{"zsh", "-y"}
	if !reflect.DeepEqual(spec.Argv, want) {
		t.Errorf("Argv = %v, want %v", spec.Argv, want)
	}
	// Empty bin is a no-op (mirrors ClaudeAdapter.SetBin).
	a.SetBin("")
	spec2, _ := a.SpawnSpec(SpawnIntent{})
	if spec2.Argv[0] != "zsh" {
		t.Errorf("SetBin(\"\") should be a no-op, Argv[0] = %q", spec2.Argv[0])
	}
}

func TestGenericAdapterSpawnSpecNoCommandErrors(t *testing.T) {
	a := NewGenericAdapter("dummy", GenericConfig{})
	if _, err := a.SpawnSpec(SpawnIntent{}); err == nil {
		t.Fatal("SpawnSpec: want error when no command configured, got nil")
	}
}
