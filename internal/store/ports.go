// Package store — workspace port publishing (See8bd4-3).
//
// These methods back the Ports tab REST endpoints. They resolve the workspace's
// runtime and delegate to the incus runtime's port-publishing methods. For host
// runtimes they return an empty list with runtimeKind="host" so the FE can show
// the host-notice.
package store

import (
	"context"
	"fmt"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// portPublisher is the subset of the incus Runtime that the Ports tab drives.
// Implemented by *incus.incusRuntime; host runtimes do not implement it.
//
// Two publishing modes exist (S4c591a), selected by whether a public domain is
// configured:
//   - subdomain mode (HostPortMode()==false): ExposePortPublic → HTTPS subdomain
//     behind SSO (See8bd4).
//   - host-port mode  (HostPortMode()==true):  ExposePortHost → http://<hostIP>:<port>
//     via an incus proxy device, UNAUTHENTICATED.
type portPublisher interface {
	PortsView() []runtime.PortView
	ExposePortPublic(ctx context.Context, port int, public bool) (string, error)
	UnexposePortPublic(ctx context.Context, port int) error
	ExposePortHost(ctx context.Context, port int) (string, error)
	UnexposePortHost(ctx context.Context, port int) error
	// HostPortMode reports whether the runtime is in host-port (no public
	// domain) fallback mode.
	HostPortMode() bool
	// HostIP returns the host's primary outbound IP for building host-port URLs.
	HostIP() string
}

// WorkspacePorts is the response shape for GET .../ports.
type WorkspacePorts struct {
	RuntimeKind string             `json:"runtimeKind"`
	Ports       []runtime.PortView `json:"ports"`
	// PublicDomainConfigured is false when no wildcard-DNS public domain is set;
	// the FE then shows host-port mode (S4c591a) instead of subdomain publishing.
	PublicDomainConfigured bool `json:"publicDomainConfigured"`
	// HostIP is the host's primary IP, used to render http://<hostIP>:<port>
	// URLs in host-port mode. Empty in subdomain mode.
	HostIP string `json:"hostIP,omitempty"`
}

// ErrPortsUnsupported is returned when the workspace runtime cannot publish
// ports (e.g. host runtime, or no public domain configured).
var ErrPortsUnsupported = fmt.Errorf("port publishing is only available for incus-container workspaces")

// WorkspacePortsView returns the listening ports + exposure state for a
// workspace. For host runtimes it returns runtimeKind="host" with no ports so
// the Ports tab can show the host-notice. (See8bd4-3)
func (s *Store) WorkspacePortsView(repoID, branchID string) WorkspacePorts {
	if s.deps.RuntimeRegistry == nil {
		return WorkspacePorts{RuntimeKind: "host"}
	}
	rt := s.deps.RuntimeRegistry.Get(repoID, branchID)
	if rt == nil {
		return WorkspacePorts{RuntimeKind: "host"}
	}
	kind := string(rt.Kind())
	pp, ok := rt.(portPublisher)
	if !ok {
		return WorkspacePorts{RuntimeKind: kind}
	}
	ports := pp.PortsView()
	if ports == nil {
		ports = []runtime.PortView{}
	}
	out := WorkspacePorts{
		RuntimeKind:            kind,
		Ports:                  ports,
		PublicDomainConfigured: !pp.HostPortMode(),
	}
	if pp.HostPortMode() {
		out.HostIP = pp.HostIP()
	}
	return out
}

// ExposeWorkspacePort publishes a container port and returns its public URL.
// In subdomain mode (public domain configured) it injects an SSO-protected
// HTTPS subdomain route (See8bd4). In host-port mode (no public domain) it adds
// an incus proxy device exposing http://<hostIP>:<port> UNAUTHENTICATED
// (S4c591a). The mode is the runtime's, not a per-call choice.
func (s *Store) ExposeWorkspacePort(ctx context.Context, repoID, branchID string, port int, public bool) (string, error) {
	pp, err := s.portPublisherFor(repoID, branchID)
	if err != nil {
		return "", err
	}
	var url string
	if pp.HostPortMode() {
		url, err = pp.ExposePortHost(ctx, port)
	} else {
		url, err = pp.ExposePortPublic(ctx, port, public)
	}
	if err != nil {
		return "", err
	}
	s.publishPortsChanged(repoID, branchID, pp)
	return url, nil
}

// UnexposeWorkspacePort removes a published port (subdomain route or host-port
// proxy device, depending on mode). (See8bd4-2/-3, S4c591a)
func (s *Store) UnexposeWorkspacePort(ctx context.Context, repoID, branchID string, port int) error {
	pp, err := s.portPublisherFor(repoID, branchID)
	if err != nil {
		return err
	}
	if pp.HostPortMode() {
		err = pp.UnexposePortHost(ctx, port)
	} else {
		err = pp.UnexposePortPublic(ctx, port)
	}
	if err != nil {
		return err
	}
	s.publishPortsChanged(repoID, branchID, pp)
	return nil
}

func (s *Store) portPublisherFor(repoID, branchID string) (portPublisher, error) {
	if s.deps.RuntimeRegistry == nil {
		return nil, ErrPortsUnsupported
	}
	rt := s.deps.RuntimeRegistry.Get(repoID, branchID)
	if rt == nil {
		return nil, ErrPortsUnsupported
	}
	pp, ok := rt.(portPublisher)
	if !ok {
		return nil, ErrPortsUnsupported
	}
	return pp, nil
}

// publishPortsChanged broadcasts the updated Ports view after an expose/unexpose
// so all connected clients refresh without waiting for the next scan.
func (s *Store) publishPortsChanged(repoID, branchID string, pp portPublisher) {
	s.hub.Publish(Event{
		Type:     EventBranchPortsChanged,
		RepoID:   repoID,
		BranchID: branchID,
		Payload:  PortsChangedPayload(string(runtime.KindIncusContainer), pp),
	})
}

// PortsChangedPayload builds the branch.portsChanged WS payload. It is the
// SINGLE source of the payload shape, shared by both emit sites (expose/unexpose
// in publishPortsChanged AND the periodic scan loop in scanPorts) so the two can
// never diverge. The FE replaces (not merges) its branchPorts slice on every
// such event, so every emit MUST carry publicDomainConfigured / hostIP or the
// Ports tab flips out of host-port mode (dropping the unauth warning). (S4c591a)
func PortsChangedPayload(kind string, pv PortViewProvider) map[string]any {
	payload := map[string]any{
		"runtimeKind":            kind,
		"ports":                  pv.PortsView(),
		"publicDomainConfigured": !pv.HostPortMode(),
	}
	if pv.HostPortMode() {
		payload["hostIP"] = pv.HostIP()
	}
	return payload
}

// PortViewProvider is the minimal capability needed to build a portsChanged
// payload: the ports view + the host-port-mode signal. Both *incusRuntime
// (portPublisher) and the scan-loop's portViewer satisfy it.
type PortViewProvider interface {
	PortsView() []runtime.PortView
	HostPortMode() bool
	HostIP() string
}
