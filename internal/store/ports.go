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
type portPublisher interface {
	PortsView() []runtime.PortView
	ExposePortPublic(ctx context.Context, port int, public bool) (string, error)
	UnexposePortPublic(ctx context.Context, port int) error
}

// WorkspacePorts is the response shape for GET .../ports.
type WorkspacePorts struct {
	RuntimeKind string             `json:"runtimeKind"`
	Ports       []runtime.PortView `json:"ports"`
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
	return WorkspacePorts{RuntimeKind: kind, Ports: ports}
}

// ExposeWorkspacePort publishes a container port as an HTTPS subdomain and
// returns the public URL. (See8bd4-2/-3)
func (s *Store) ExposeWorkspacePort(ctx context.Context, repoID, branchID string, port int, public bool) (string, error) {
	pp, err := s.portPublisherFor(repoID, branchID)
	if err != nil {
		return "", err
	}
	url, err := pp.ExposePortPublic(ctx, port, public)
	if err != nil {
		return "", err
	}
	s.publishPortsChanged(repoID, branchID, pp)
	return url, nil
}

// UnexposeWorkspacePort removes a published port's route. (See8bd4-2/-3)
func (s *Store) UnexposeWorkspacePort(ctx context.Context, repoID, branchID string, port int) error {
	pp, err := s.portPublisherFor(repoID, branchID)
	if err != nil {
		return err
	}
	if err := pp.UnexposePortPublic(ctx, port); err != nil {
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
		Payload: map[string]any{
			"runtimeKind": "incus-container",
			"ports":       pp.PortsView(),
		},
	})
}
