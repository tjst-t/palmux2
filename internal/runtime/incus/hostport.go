// Package incus — host-port publishing (S4c591a).
//
// This is the wildcard-DNS-less fallback to See8bd4's subdomain publishing.
// When palmux has NO public domain configured (publishConfig disabled), the
// Ports tab switches to host-port mode: each container port can be published as
// http://<hostIP>:<hostPort> via an incus *proxy device* that listens on the
// host and forwards to the container.
//
// Mechanism: reuse incusRuntime.ExposePort(HostPort>0), which already adds
//
//	incus config device add <inst> p<proto><port> proxy \
//	    listen=tcp:0.0.0.0:<hostPort> connect=tcp:<containerIP>:<containerPort>
//
// (The connect target is the container's BRIDGE IP, not 127.0.0.1: the incus
// forkproxy runs in the HOST network namespace, so 127.0.0.1 would hit the
// host's loopback. See the ExposePort note in incus.go. The S4c591a AC says
// connect=127.0.0.1 but that is incorrect for incus forkproxy; the container IP
// is the functionally correct target that satisfies the AC's external-reach
// intent.)
//
// SECURITY: host-port publishing bypasses Caddy/SSO entirely — the dev server
// is exposed UNAUTHENTICATED to anything that can reach the host's IP. This is
// intentional (the whole point of the no-DNS fallback), but the FE must warn
// the user unmissably (ports-noauth-warning-<port>). Auth is delegated to the
// dev server itself.
package incus

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// hostPortBase is the start of the auto-reassignment search range used when a
// container port collides with a host-side listener. e.g. container port 6006
// busy on host → try 16006, 16007, … (containerPort + hostPortReassignOffset).
const hostPortReassignOffset = 10000

// HostPortMode reports whether this runtime is in host-port (no public domain)
// mode. The Ports tab uses host-port toggles iff this is true. (S4c591a)
func (r *incusRuntime) HostPortMode() bool {
	return !r.pub.enabled()
}

// HostIP returns the host's primary outbound IP (cached), for building
// http://<hostIP>:<hostPort> URLs in host-port mode. (S4c591a)
func (r *incusRuntime) HostIP() string {
	return hostIPOnce()
}

// HostIP returns the host's primary outbound IPv4 address, used to build
// http://<hostIP>:<hostPort> URLs for host-port-published ports. It does not
// send any packet (UDP connect just selects a route/source addr). Falls back to
// the first non-loopback IPv4 of the host's interfaces, then "127.0.0.1".
func HostIP() string {
	if conn, err := net.Dial("udp", "192.0.2.1:80"); err == nil {
		defer conn.Close()
		if la, ok := conn.LocalAddr().(*net.UDPAddr); ok && la.IP != nil {
			if v4 := la.IP.To4(); v4 != nil {
				return v4.String()
			}
		}
	}
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() {
				if v4 := ipn.IP.To4(); v4 != nil {
					return v4.String()
				}
			}
		}
	}
	return "127.0.0.1"
}

// hostPortListenProbe is overridable in tests to avoid binding real sockets.
var hostPortListenProbe = defaultHostPortFree

// defaultHostPortFree reports whether a TCP host port is free by trying to bind
// it on all interfaces. The listener is closed immediately; the proxy device
// (added afterwards) grabs the port for real.
func defaultHostPortFree(port int) bool {
	ln, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return false
	}
	_ = ln.Close()
	return true
}

// allocHostPort picks a host-side port for a container port: the same number if
// free, otherwise the first free port from containerPort+offset upward (skipping
// ports already claimed by this runtime's other host-port mappings). Returns 0
// if nothing is available. takenMu must be held by the caller via portsMu.
func (r *incusRuntime) allocHostPort(containerPort int) int {
	taken := map[int]bool{}
	for _, st := range r.hostExposed {
		taken[st.hostPort] = true
	}
	if !taken[containerPort] && hostPortListenProbe(containerPort) {
		return containerPort
	}
	start := containerPort + hostPortReassignOffset
	if start > 65535 {
		start = 1024
	}
	for p := start; p <= 65535; p++ {
		if taken[p] {
			continue
		}
		if hostPortListenProbe(p) {
			return p
		}
	}
	return 0
}

// hostOpLock returns the per-port mutex serialising ExposePortHost /
// UnexposePortHost for one container port, so a concurrent unexpose during an
// in-flight expose's `incus config device add` cannot interleave and leak an
// unauthenticated proxy device (review finding S4c591a). The map of locks is
// itself guarded by hostOpMapMu.
func (r *incusRuntime) hostOpLock(port int) *sync.Mutex {
	r.hostOpMapMu.Lock()
	defer r.hostOpMapMu.Unlock()
	if r.hostOpLocks == nil {
		r.hostOpLocks = map[int]*sync.Mutex{}
	}
	mu, ok := r.hostOpLocks[port]
	if !ok {
		mu = &sync.Mutex{}
		r.hostOpLocks[port] = mu
	}
	return mu
}

// ExposePortHost publishes a container port on the host via an incus proxy
// device and returns http://<hostIP>:<hostPort>. The host port is the same as
// the container port when free, else auto-reassigned to avoid collision. This
// is the wildcard-DNS-less fallback (S4c591a). Idempotent: re-exposing an
// already-published port returns its existing URL.
func (r *incusRuntime) ExposePortHost(ctx context.Context, port int) (string, error) {
	opMu := r.hostOpLock(port)
	opMu.Lock()
	defer opMu.Unlock()

	r.portsMu.Lock()
	if st, ok := r.hostExposed[port]; ok {
		r.portsMu.Unlock()
		return r.hostURL(st.hostPort), nil
	}
	hostPort := r.allocHostPort(port)
	if hostPort == 0 {
		r.portsMu.Unlock()
		return "", fmt.Errorf("expose host port %d: no free host port available", port)
	}
	// Reserve the slot under lock so concurrent calls don't race on the same
	// host port; ExposePort itself does the actual device add (outside the lock
	// to avoid holding portsMu across an incus exec).
	r.hostExposed[port] = hostExposeState{hostPort: hostPort}
	r.portsMu.Unlock()

	m, err := r.ExposePort(ctx, runtime.PortSpec{
		Internal: port,
		Proto:    "tcp",
		Public:   true, // host-port = no edge auth; exposure is unauthenticated by design
		HostPort: hostPort,
	})
	if err != nil {
		r.portsMu.Lock()
		delete(r.hostExposed, port)
		r.portsMu.Unlock()
		return "", fmt.Errorf("expose host port %d: %w", port, err)
	}

	r.portsMu.Lock()
	r.hostExposed[port] = hostExposeState{hostPort: hostPort, mappingID: m.ID}
	r.portsMu.Unlock()

	url := r.hostURL(hostPort)
	r.log.Info("incus: host-port published (UNAUTHENTICATED)",
		"inst", r.inst, "port", port, "hostPort", hostPort, "url", url)
	return url, nil
}

// UnexposePortHost removes the host-port proxy device for a container port.
// Serialised against ExposePortHost for the same port (hostOpLock) so it cannot
// run between an expose's reservation and its device-add.
func (r *incusRuntime) UnexposePortHost(ctx context.Context, port int) error {
	opMu := r.hostOpLock(port)
	opMu.Lock()
	defer opMu.Unlock()

	r.portsMu.Lock()
	st, ok := r.hostExposed[port]
	delete(r.hostExposed, port)
	r.portsMu.Unlock()
	if !ok {
		return nil // already removed / never published — idempotent
	}
	if st.mappingID != "" {
		if err := r.UnexposePort(ctx, st.mappingID); err != nil {
			return fmt.Errorf("unexpose host port %d: %w", port, err)
		}
	}
	r.log.Info("incus: host-port unpublished", "inst", r.inst, "port", port, "hostPort", st.hostPort)
	return nil
}

// hostURL builds the unauthenticated host-port URL.
func (r *incusRuntime) hostURL(hostPort int) string {
	return fmt.Sprintf("http://%s:%d", hostIPOnce(), hostPort)
}

// hostIPOnce caches the resolved host IP for the process lifetime (the host's
// LAN IP does not change while palmux runs).
var (
	hostIPCache   string
	hostIPCacheMu sync.Once
)

func hostIPOnce() string {
	hostIPCacheMu.Do(func() { hostIPCache = HostIP() })
	return hostIPCache
}

// PortViewFor returns the PortView for a single container port from the current
// scan+exposure state, or nil if the port is not in the last scan. When the
// port is host-published it is synthesised even if absent from lastPorts (so a
// freshly-exposed port reflects immediately before the next scan).
func (r *incusRuntime) PortViewFor(port int) *runtime.PortView {
	for _, pv := range r.PortsView() {
		if pv.Port == port {
			return &pv
		}
	}
	// Fall back to host-exposure state if the port hasn't been scanned yet.
	r.portsMu.RLock()
	hst, ok := r.hostExposed[port]
	r.portsMu.RUnlock()
	if !ok {
		return nil
	}
	return &runtime.PortView{
		Port:          port,
		HostPublished: true,
		HostPort:      hst.hostPort,
		HostURL:       r.hostURL(hst.hostPort),
	}
}

// unpublishAllHost removes every host-port proxy device for this workspace and
// clears the in-memory state. Called on Stop so closing/switching a workspace
// tears down its host exposures explicitly (defense-in-depth alongside
// `incus delete --force`, which also drops the container's devices). (S4c591a)
func (r *incusRuntime) unpublishAllHost(ctx context.Context) {
	r.portsMu.Lock()
	states := make([]hostExposeState, 0, len(r.hostExposed))
	for _, st := range r.hostExposed {
		states = append(states, st)
	}
	r.hostExposed = map[int]hostExposeState{}
	r.portsMu.Unlock()
	for _, st := range states {
		if st.mappingID != "" {
			_ = r.UnexposePort(ctx, st.mappingID)
		}
	}
}
