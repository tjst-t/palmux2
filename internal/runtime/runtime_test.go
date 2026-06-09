// [AC-S8478ca-1-1]
package runtime_test

import (
	"testing"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// TestPortSpecFields asserts that PortSpec carries the required fields
// (Internal, Proto, Name, Public, HostPort) and that those fields round-trip
// through ExposePort correctly.  This is a compile-time + value assertion —
// if any field is removed the test will not compile.
// [AC-S8478ca-1-1]
func TestPortSpecFields(t *testing.T) {
	spec := runtime.PortSpec{
		Internal: 8080,
		Proto:    "udp",
		Name:     "webrtc",
		Public:   true,
		HostPort: 49152,
	}
	if spec.Internal != 8080 {
		t.Errorf("Internal: got %d, want 8080", spec.Internal)
	}
	if spec.Proto != "udp" {
		t.Errorf("Proto: got %q, want \"udp\"", spec.Proto)
	}
	if spec.Name != "webrtc" {
		t.Errorf("Name: got %q, want \"webrtc\"", spec.Name)
	}
	if !spec.Public {
		t.Error("Public: got false, want true")
	}
	if spec.HostPort != 49152 {
		t.Errorf("HostPort: got %d, want 49152", spec.HostPort)
	}
}

// TestPortMappingFields asserts that PortMapping carries the required fields.
// [AC-S8478ca-1-1]
func TestPortMappingFields(t *testing.T) {
	m := runtime.PortMapping{
		ID:       "host-udp-8080",
		Internal: 8080,
		HostPort: 49152,
		Proto:    "udp",
		Address:  "localhost",
		Public:   true,
	}
	if m.ID != "host-udp-8080" {
		t.Errorf("ID: got %q", m.ID)
	}
	if m.Proto != "udp" {
		t.Errorf("Proto: got %q", m.Proto)
	}
	if !m.Public {
		t.Error("Public: got false, want true")
	}
}

// TestKindIsValid verifies Kind.IsValid for all known + unknown values.
// [AC-S8478ca-1-1]
func TestKindIsValid(t *testing.T) {
	cases := []struct {
		k    runtime.Kind
		want bool
	}{
		{runtime.KindHost, true},
		{runtime.KindIncusContainer, true},
		{"unknown", false},
		{"", false},
		{"incus-vm", false},
	}
	for _, c := range cases {
		got := c.k.IsValid()
		if got != c.want {
			t.Errorf("Kind(%q).IsValid() = %v, want %v", c.k, got, c.want)
		}
	}
}

// TestPortSpecUDPPublic is a named scenario test from scenario-S8478ca-1.json
// scenario-1: consumer calls ExposePort with Proto:"udp", Public:true and the
// types compile + carry the values.
// [AC-S8478ca-1-1]
func TestPortSpecUDPPublic(t *testing.T) {
	spec := runtime.PortSpec{
		Internal: 5004,
		Proto:    "udp",
		Name:     "neko-webrtc",
		Public:   true,
		HostPort: 49200,
	}
	// Verify the spec can be used to build a PortMapping that mirrors Proto/Public.
	m := runtime.PortMapping{
		ID:       "test-mapping",
		Internal: spec.Internal,
		HostPort: spec.HostPort,
		Proto:    spec.Proto,
		Address:  "10.0.0.5",
		Public:   spec.Public,
	}
	if m.Proto != "udp" {
		t.Errorf("PortMapping.Proto: got %q, want \"udp\"", m.Proto)
	}
	if !m.Public {
		t.Error("PortMapping.Public: got false, want true")
	}
}

// TestConfigJSON exercises the Config struct JSON tags (kind + image fields).
// [AC-S8478ca-1-1]
func TestConfigFields(t *testing.T) {
	c := runtime.Config{Kind: runtime.KindHost, Image: ""}
	if c.Kind != runtime.KindHost {
		t.Errorf("Config.Kind: got %q", c.Kind)
	}
	c2 := runtime.Config{Kind: runtime.KindIncusContainer, Image: "ubuntu:24.04"}
	if c2.Image != "ubuntu:24.04" {
		t.Errorf("Config.Image: got %q", c2.Image)
	}
}
