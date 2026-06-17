// Package store — regression test for the branch.portsChanged payload shape.
// Both emit sites (expose/unexpose + the 10s scan loop) build the payload via
// PortsChangedPayload, which MUST carry publicDomainConfigured / hostIP so the
// FE (which replaces, not merges, its branchPorts slice on every event) never
// flips out of host-port mode and drops the unauth warning. (S4c591a review)
package store

import (
	"testing"

	"github.com/tjst-t/palmux2/internal/runtime"
)

type fakePortView struct {
	hostMode bool
	hostIP   string
}

func (f fakePortView) PortsView() []runtime.PortView {
	return []runtime.PortView{{Port: 5173, Proto: "tcp"}}
}
func (f fakePortView) HostPortMode() bool { return f.hostMode }
func (f fakePortView) HostIP() string     { return f.hostIP }

func TestPortsChangedPayload_HostPortMode(t *testing.T) {
	p := PortsChangedPayload("incus-container", fakePortView{hostMode: true, hostIP: "192.168.1.40"})
	if v, ok := p["publicDomainConfigured"].(bool); !ok || v {
		t.Fatalf("host-port mode: publicDomainConfigured must be false, got %v", p["publicDomainConfigured"])
	}
	if p["hostIP"] != "192.168.1.40" {
		t.Fatalf("host-port mode: hostIP must be carried, got %v", p["hostIP"])
	}
	if p["ports"] == nil {
		t.Fatalf("ports missing from payload")
	}
}

func TestPortsChangedPayload_SubdomainMode(t *testing.T) {
	p := PortsChangedPayload("incus-container", fakePortView{hostMode: false})
	if v, ok := p["publicDomainConfigured"].(bool); !ok || !v {
		t.Fatalf("subdomain mode: publicDomainConfigured must be true, got %v", p["publicDomainConfigured"])
	}
	if _, has := p["hostIP"]; has {
		t.Fatalf("subdomain mode: hostIP must be omitted, got %v", p["hostIP"])
	}
}
