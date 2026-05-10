package runtime_test

import (
	"encoding/json"
	"testing"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// TestKindIsValid covers AC-Sdd4ce1-1-1: the Kind constants enumerate the
// supported runtime backends and IsValid recognises them all.
//
// [AC-Sdd4ce1-1-1]
func TestKindIsValid(t *testing.T) {
	t.Parallel()
	cases := []struct {
		k    runtime.Kind
		want bool
	}{
		{runtime.KindHost, true},
		{runtime.KindLXDContainer, true},
		{runtime.KindLXDVM, true},
		{runtime.KindLXDRemote, true},
		{runtime.KindSSHRemote, true},
		{runtime.Kind(""), false},
		{runtime.Kind("docker"), false},
		{runtime.Kind("podman"), false},
	}
	for _, tc := range cases {
		if got := tc.k.IsValid(); got != tc.want {
			t.Errorf("Kind(%q).IsValid() = %v, want %v", tc.k, got, tc.want)
		}
	}
}

// TestConfigJSONRoundTrip covers AC-Sdd4ce1-1-1: Config serialises through
// json correctly with the exact field names design §14.5 specifies.
//
// [AC-Sdd4ce1-1-1]
func TestConfigJSONRoundTrip(t *testing.T) {
	t.Parallel()
	in := runtime.Config{
		Kind:  runtime.KindLXDContainer,
		Image: "ghcr.io/tjst-t/palmux-workspace:default",
		Network: runtime.NetworkPolicy{
			Mode: "bridged",
		},
		Resources: runtime.Resources{
			MemoryMiB: 4096,
			CPUCount:  2,
		},
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	// Spot-check the wire format matches the design doc's example.
	want := `{"kind":"lxd-container","image":"ghcr.io/tjst-t/palmux-workspace:default","network":{"mode":"bridged"},"resources":{"memory_mib":4096,"cpu_count":2}}`
	if string(b) != want {
		t.Errorf("Marshal: got %s want %s", b, want)
	}
	var out runtime.Config
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out != in {
		t.Errorf("round-trip mismatch:\n got=%+v\nwant=%+v", out, in)
	}
}

// TestConfigEmptyKindOmitsBackwardCompat covers AC-Sdd4ce1-1-2: a Config
// decoded from a repos.json entry that has no `runtime` field has Kind="",
// which callers must coerce to KindHost via the priority chain.
//
// [AC-Sdd4ce1-1-2]
func TestConfigEmptyKindOmitsBackwardCompat(t *testing.T) {
	t.Parallel()
	// Pre-Phase-A repos.json has no runtime field — decode an empty object.
	var c runtime.Config
	if err := json.Unmarshal([]byte(`{}`), &c); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if c.Kind != "" {
		t.Errorf("empty config: Kind = %q, want \"\"", c.Kind)
	}
	if !c.IsZero() {
		t.Errorf("empty config: IsZero() = false, want true")
	}
	// The priority-chain coercion is performed by WithDefaults — passing
	// the host default fills the empty Kind without touching anything else.
	got := c.WithDefaults(runtime.Config{Kind: runtime.KindHost})
	if got.Kind != runtime.KindHost {
		t.Errorf("WithDefaults host: Kind = %q, want %q", got.Kind, runtime.KindHost)
	}
}

// TestConfigWithDefaults verifies the priority-chain merge that
// docs/workspace-runtime-design.md §9.6 requires.
//
// [AC-Sdd4ce1-1-1]
func TestConfigWithDefaults(t *testing.T) {
	t.Parallel()
	perWorkspace := runtime.Config{
		Kind:    runtime.KindLXDContainer,
		Network: runtime.NetworkPolicy{Mode: "bridged"},
	}
	perRepo := runtime.Config{
		Kind:  runtime.KindLXDVM,
		Image: "ghcr.io/tjst-t/palmux-workspace:gpu",
	}
	global := runtime.Config{
		Kind:  runtime.KindLXDContainer,
		Image: "ghcr.io/tjst-t/palmux-workspace:default",
		Network: runtime.NetworkPolicy{
			Mode:           "tailnet",
			TailnetAuthKey: "tskey-default",
		},
	}

	// Apply most → least specific: per-workspace wins everything it sets,
	// per-repo fills the holes, global fills the rest.
	got := perWorkspace.WithDefaults(perRepo).WithDefaults(global)

	if got.Kind != runtime.KindLXDContainer {
		t.Errorf("Kind = %q, want %q (most specific wins)", got.Kind, runtime.KindLXDContainer)
	}
	if got.Image != "ghcr.io/tjst-t/palmux-workspace:gpu" {
		t.Errorf("Image = %q, want %q (per-repo fills hole)", got.Image, "ghcr.io/tjst-t/palmux-workspace:gpu")
	}
	if got.Network.Mode != "bridged" {
		t.Errorf("Network.Mode = %q, want %q (per-workspace wins)", got.Network.Mode, "bridged")
	}
	if got.Network.TailnetAuthKey != "tskey-default" {
		t.Errorf("TailnetAuthKey = %q, want %q (global fills hole)", got.Network.TailnetAuthKey, "tskey-default")
	}
}

// TestConfigWithDefaultsZero ensures that merging into a zero Config yields
// the defaults (priority chain bottom-out).
func TestConfigWithDefaultsZero(t *testing.T) {
	t.Parallel()
	var zero runtime.Config
	defaults := runtime.Config{
		Kind:  runtime.KindHost,
		Image: "ignored-for-host",
	}
	got := zero.WithDefaults(defaults)
	if got != defaults {
		t.Errorf("zero.WithDefaults(d) = %+v, want %+v", got, defaults)
	}
}

// TestStatusZeroValue documents that the zero Status reports State="" so
// the store can distinguish "never started" from "explicitly stopped".
func TestStatusZeroValue(t *testing.T) {
	t.Parallel()
	var s runtime.Status
	if s.State != "" {
		t.Errorf("zero Status.State = %q, want \"\"", s.State)
	}
}
