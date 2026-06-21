// Package incus — Caddy admin-API integration for publishing container ports
// as HTTPS subdomains (See8bd4-2).
//
// When palmux is configured with a public base domain (--public-domain), it
// publishes a container port by injecting a route into the running Caddy via
// the admin API (default http://localhost:2019) instead of writing a static
// conf.d snippet. The route is keyed by a stable Caddy `@id`
// (palmux-<inst>-<port>) so it can be idempotently upserted and deleted.
//
// Design (docs/sprint-logs/See8bd4/decisions.json D7):
//   - Subdomain: <port>--<workspace>--<repo>.<base>
//   - basic_auth handler is injected per-route unless the port is Public.
//   - The wildcard *.<base> TLS cert + apex auth live in the static
//     system-manager Caddyfile (reload-durable); palmux re-injects per-port
//     routes on every scan, so they survive a Caddy reload within one scan
//     interval (portman model-B style re-sync).
//
// If no public domain is configured, ExposePortPublic returns an error and the
// legacy conf.d snippet path (caddy.go) remains the local-dev behaviour.
package incus

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// publishConfig holds everything needed to build and inject a public route for
// one workspace. It is set by the Registry after constructing the runtime.
type publishConfig struct {
	baseDomain     string // e.g. "palmux-deploy-test.tjstkm.net"; "" disables publishing
	caddyAdmin     string // e.g. "http://localhost:2019"
	basicUser      string // legacy edge basic_auth username (unused with forward_auth)
	basicHash      string // legacy edge basic_auth bcrypt hash (unused with forward_auth)
	palmuxUpstream string // host:port Caddy dials for forward_auth /auth/verify (Sbe4eee)
	repoLabel      string // DNS-safe repo label for the subdomain
	wsLabel        string // DNS-safe workspace label for the subdomain
}

// enabled reports whether public-subdomain publishing is configured.
func (p *publishConfig) enabled() bool {
	return p != nil && p.baseDomain != "" && p.caddyAdmin != ""
}

// subdomain returns the public host for a port: <port>--<ws>--<repo>.<base>.
func (p *publishConfig) subdomain(port int) string {
	return fmt.Sprintf("%d--%s--%s.%s", port, p.wsLabel, p.repoLabel, p.baseDomain)
}

// publicURL returns the https URL for a port.
func (p *publishConfig) publicURL(port int) string {
	return "https://" + p.subdomain(port)
}

// dnsLabel sanitises s into a DNS-label-safe token: lowercase, [a-z0-9-], no
// leading/trailing/dup hyphens, and crucially no "--" runs (which are the
// subdomain field separator). Empty input yields "x".
func dnsLabel(s string) string {
	var b strings.Builder
	prev := byte('-')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z':
			c += 'a' - 'A'
			b.WriteByte(c)
			prev = c
		case (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9'):
			b.WriteByte(c)
			prev = c
		default:
			if prev != '-' {
				b.WriteByte('-')
				prev = '-'
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "x"
	}
	return out
}

// repoLabelFromID derives a DNS-safe repo label from a repoID of the form
// "owner--repo--hash4". It drops only the owner and KEEPS the path-derived
// hash ("repo-hash4") so two different repos that share a repo name (e.g.
// alice/myapp vs bob/myapp) get distinct subdomains — the hash is derived from
// the repo path, which differs. Falls back to the sanitised whole ID.
func repoLabelFromID(repoID string) string {
	parts := strings.Split(repoID, "--")
	if len(parts) >= 3 {
		return dnsLabel(strings.Join(parts[1:], "-"))
	}
	return dnsLabel(repoID)
}

// wsLabelFromID derives a DNS-safe workspace label from a branchID of the form
// "slug--hash4". It KEEPS the path-derived hash ("slug-hash4") so two
// workspaces with the same slug in different worktrees never collide — the
// hash is derived from the worktree path. dnsLabel collapses the "--" run to a
// single "-".
func wsLabelFromID(branchID string) string {
	return dnsLabel(branchID)
}

// routeID returns the stable Caddy @id for a workspace port route.
func (r *incusRuntime) routeID(port int) string {
	return fmt.Sprintf("palmux-%s-%d", r.inst, port)
}

// caddyAdminClient is a thin HTTP client for the Caddy admin API.
type caddyAdminClient struct {
	base string // e.g. http://localhost:2019
	http *http.Client
}

func newCaddyAdminClient(base string) *caddyAdminClient {
	return &caddyAdminClient{base: strings.TrimRight(base, "/"), http: &http.Client{Timeout: 8 * time.Second}}
}

// serverName discovers the name of the HTTP server that listens on :443 (the
// one the Caddyfile adapter creates for the apex + wildcard sites). Falls back
// to "srv0" if discovery fails.
func (c *caddyAdminClient) serverName(ctx context.Context) string {
	const fallback = "srv0"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/config/apps/http/servers", nil)
	if err != nil {
		return fallback
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fallback
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fallback
	}
	var servers map[string]struct {
		Listen []string `json:"listen"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&servers); err != nil {
		return fallback
	}
	// Iterate names in deterministic (sorted) order so the choice is stable
	// across calls even in a degraded (no :443) config.
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		for _, l := range servers[name].Listen {
			if strings.Contains(l, ":443") {
				return name
			}
		}
	}
	if len(names) > 0 {
		return names[0]
	}
	return fallback
}

// caddyRoute is the JSON shape palmux injects per published port.
type caddyRoute struct {
	ID       string            `json:"@id"`
	Match    []caddyMatch      `json:"match"`
	Handle   []json.RawMessage `json:"handle"`
	Terminal bool              `json:"terminal"`
}

type caddyMatch struct {
	Host []string `json:"host"`
}

// upsertRoute idempotently installs a route for host → upstream. When
// requireAuth is true a Caddy forward_auth gate (→ palmuxUpstream /auth/verify)
// is prepended: on a 2xx it proceeds to the backend, otherwise palmux's 302 to
// the login page is copied back to the browser (Sbe4eee SSO). When false the
// route is a plain reverse_proxy (Public=true ports). It first deletes any
// existing route with the same @id, then front-inserts the fresh route.
func (c *caddyAdminClient) upsertRoute(ctx context.Context, id, host, upstream, palmuxUpstream string, requireAuth bool) error {
	// Delete any prior route with this @id (ignore not-found).
	_ = c.deleteRoute(ctx, id)

	backend := fmt.Sprintf(`{"handler":"reverse_proxy","upstreams":[{"dial":%q}]}`, upstream)

	var handlers []json.RawMessage
	if requireAuth && palmuxUpstream != "" {
		// forward_auth subroute (shape from `caddy adapt` of a forward_auth
		// Caddyfile): a reverse_proxy to /auth/verify whose 2xx response falls
		// through (vars no-op) to the backend; non-2xx (302 login) is returned.
		fa := fmt.Sprintf(`{"handler":"subroute","routes":[`+
			`{"handle":[{"handle_response":[{"match":{"status_code":[2]},"routes":[{"handle":[{"handler":"vars"}]}]}],`+
			`"handler":"reverse_proxy",`+
			`"headers":{"request":{"set":{"X-Forwarded-Method":["{http.request.method}"],"X-Forwarded-Uri":["{http.request.uri}"]}}},`+
			`"rewrite":{"method":"GET","uri":"/auth/verify"},`+
			`"upstreams":[{"dial":%q}]}]},`+
			`{"handle":[%s]}]}`, palmuxUpstream, backend)
		handlers = []json.RawMessage{json.RawMessage(fa)}
	} else {
		handlers = []json.RawMessage{json.RawMessage(backend)}
	}

	route := caddyRoute{
		ID:       id,
		Match:    []caddyMatch{{Host: []string{host}}},
		Handle:   handlers,
		Terminal: true,
	}
	body, err := json.Marshal(route)
	if err != nil {
		return fmt.Errorf("caddy admin: marshal route: %w", err)
	}

	srv := c.serverName(ctx)
	// Insert at index 0 (PUT to .../routes/0) so our specific per-port host
	// routes are matched BEFORE any static wildcard catch-all (e.g. the
	// `*.<base>` "no upstream" 502 route) which lives later in the array.
	// Appending would put us after the catch-all and every port would 502.
	url := fmt.Sprintf("%s/config/apps/http/servers/%s/routes/0", c.base, srv)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("caddy admin: new request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("caddy admin: POST route: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("caddy admin: POST route returned %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}

// caddyClient lazily constructs the admin client from the publish config.
// sync.Once makes the lazy init data-race-free (ExposePortPublic /
// UnexposePortPublic / unpublishAll can be called from concurrent goroutines).
func (r *incusRuntime) caddyClient() *caddyAdminClient {
	if r.pub == nil || r.pub.caddyAdmin == "" {
		return nil
	}
	r.caddyOnce.Do(func() {
		r.caddy = newCaddyAdminClient(r.pub.caddyAdmin)
	})
	return r.caddy
}

// resyncExposedRoutes re-injects every currently-exposed port's route. Called
// from the scan loop in publish mode so routes self-heal after a Caddy reload
// (admin-API routes are not persisted to the Caddyfile and are dropped on a
// Caddy restart). Idempotent: upsertRoute deletes-then-adds by @id.
func (r *incusRuntime) resyncExposedRoutes(ctx context.Context) {
	c := r.caddyClient()
	if c == nil {
		return
	}
	addr := r.Status().Address
	if addr == "" || addr == "pending" {
		return
	}
	type ent struct {
		port   int
		public bool
	}
	r.portsMu.RLock()
	ents := make([]ent, 0, len(r.exposed))
	for p, st := range r.exposed {
		ents = append(ents, ent{p, st.public})
	}
	r.portsMu.RUnlock()
	for _, e := range ents {
		host := r.pub.subdomain(e.port)
		upstream := fmt.Sprintf("%s:%d", addr, e.port)
		if err := c.upsertRoute(ctx, r.routeID(e.port), host, upstream, r.pub.palmuxUpstream, !e.public); err != nil {
			r.log.Warn("incus: resync route failed (non-fatal)", "inst", r.inst, "port", e.port, "err", err)
		}
	}
}

// recordPorts stores the latest scan result for the Ports tab view.
func (r *incusRuntime) recordPorts(ports []runtime.ListeningPort) {
	r.portsMu.Lock()
	r.lastPorts = ports
	r.portsMu.Unlock()
}

// palmuxInternalPorts are palmux's OWN Browser-tab stack ports (not user dev
// servers): the noVNC display + the chromium CDP. Kept here (not imported from
// internal/tab/browser) to avoid an import cycle; mirrors browser.VNCPort /
// browser.CDPPort.
var palmuxInternalPorts = map[int]bool{
	5900: true, // x11vnc / noVNC Browser tab  (browser.VNCPort)
	9222: true, // chromium CDP + in-container relay (browser.CDPPort)
}

// systemPorts are well-known OS/infra services that are almost never a user's
// dev server (DNS resolver stub, ssh, dhcp, ntp, mDNS, …). Deliberately a small,
// conservative denylist — common web-dev ports like 80/443/3000/8080 are NOT
// here, so they stay in the "user" category.
var systemPorts = map[int]bool{
	22: true, 25: true, 53: true, 67: true, 68: true, 111: true,
	123: true, 137: true, 138: true, 139: true, 323: true, 445: true,
	631: true, 5353: true,
}

// portCategory classifies a port for the Ports view: "palmux" (palmux's browser
// stack), "system" (OS/infra), or "user" (everything else — the dev servers the
// user cares about). The UI shows "user" by default and reveals system/palmux
// behind a toggle. Port-number based because the in-container ss scan does not
// reliably carry process names.
func portCategory(port int) string {
	switch {
	case palmuxInternalPorts[port]:
		return "palmux"
	case systemPorts[port]:
		return "system"
	default:
		return "user"
	}
}

// PortsView returns the user-facing view of the container's listening ports,
// merged with the current exposure state. (See8bd4-3)
func (r *incusRuntime) PortsView() []runtime.PortView {
	r.portsMu.RLock()
	defer r.portsMu.RUnlock()
	// Aggregate the scan snapshot by port number: a service listening on several
	// addresses (e.g. 0.0.0.0:80 AND [::]:80, or 127.0.0.1:9222 AND <bridge>:9222)
	// is ONE port, not one row per bind address. A port is localhost-only only
	// when EVERY bind for it is loopback (then it needs the in-container relay to
	// be reachable). No port-number filter — every listening port is shown,
	// including well-known ones like 80/443 (a dev nginx is a real dev server).
	type portAgg struct {
		proto, bindAddr, process string
		localhostOnly            bool
	}
	byPort := make(map[int]*portAgg)
	order := make([]int, 0, len(r.lastPorts))
	for _, p := range r.lastPorts {
		if a := byPort[p.Port]; a != nil {
			a.localhostOnly = a.localhostOnly && isLoopbackBind(p.BindAddr)
			// Prefer a non-loopback bind address for display (the reachable one).
			if isLoopbackBind(a.bindAddr) && !isLoopbackBind(p.BindAddr) {
				a.bindAddr = p.BindAddr
			}
			if a.process == "" {
				a.process = p.Process
			}
			continue
		}
		byPort[p.Port] = &portAgg{
			proto:         p.Proto,
			bindAddr:      p.BindAddr,
			process:       p.Process,
			localhostOnly: isLoopbackBind(p.BindAddr),
		}
		order = append(order, p.Port)
	}
	sort.Ints(order)

	out := make([]runtime.PortView, 0, len(order))
	seen := make(map[int]bool, len(order))
	for _, port := range order {
		a := byPort[port]
		seen[port] = true
		st, exposed := r.exposed[port]
		hst, hostPublished := r.hostExposed[port]
		hostURL := ""
		if hostPublished {
			hostURL = r.hostURL(hst.hostPort)
		}
		out = append(out, runtime.PortView{
			Port:          port,
			Proto:         a.proto,
			BindAddr:      a.bindAddr,
			Process:       a.process,
			Category:      portCategory(port),
			LocalhostOnly: a.localhostOnly,
			Public:        st.public,
			Exposed:       exposed,
			PublicURL:     st.url,
			HostPublished: hostPublished,
			HostPort:      hst.hostPort,
			HostURL:       hostURL,
		})
	}
	// Include ports that are exposed (subdomain or host-port) but absent from
	// the latest scan snapshot — e.g. a just-exposed port the next scan hasn't
	// captured yet, so the expose readback + WS event reflect them immediately
	// instead of reporting a zero-valued (unpublished) row. (S4c591a)
	for port, hst := range r.hostExposed {
		if seen[port] {
			continue
		}
		st, exposed := r.exposed[port]
		out = append(out, runtime.PortView{
			Port:          port,
			Proto:         "tcp",
			Category:      portCategory(port),
			Public:        st.public,
			Exposed:       exposed,
			PublicURL:     st.url,
			HostPublished: true,
			HostPort:      hst.hostPort,
			HostURL:       r.hostURL(hst.hostPort),
		})
	}
	return out
}

// ExposePortPublic publishes a container port as an HTTPS subdomain via the
// Caddy admin API. public=true omits edge basic_auth. Returns the public URL.
// Requires a configured public domain (--public-domain). (See8bd4-2)
func (r *incusRuntime) ExposePortPublic(ctx context.Context, port int, public bool) (string, error) {
	if !r.pub.enabled() {
		return "", fmt.Errorf("public domain not configured (set --public-domain to publish ports)")
	}
	c := r.caddyClient()
	if c == nil {
		return "", fmt.Errorf("caddy admin client unavailable")
	}

	// Ensure the port is reachable on the container IP. localhost-only binds
	// need the in-container relay; global binds are already reachable.
	bind := r.bindAddrFor(port)
	if isLocalhostBind(bind) {
		if _, err := r.ExposePort(ctx, runtime.PortSpec{Internal: port, Proto: "tcp", Public: public}); err != nil {
			return "", fmt.Errorf("expose port %d: ensure relay: %w", port, err)
		}
	}

	addr := r.Status().Address
	if addr == "" || addr == "pending" {
		if ip, err := r.containerIP(ctx); err == nil && ip != "" {
			addr = ip
		} else {
			return "", fmt.Errorf("expose port %d: container IP not resolved", port)
		}
	}

	host := r.pub.subdomain(port)
	upstream := fmt.Sprintf("%s:%d", addr, port)
	if err := c.upsertRoute(ctx, r.routeID(port), host, upstream, r.pub.palmuxUpstream, !public); err != nil {
		return "", fmt.Errorf("expose port %d: %w", port, err)
	}

	url := r.pub.publicURL(port)
	r.portsMu.Lock()
	r.exposed[port] = exposeState{public: public, url: url}
	r.portsMu.Unlock()
	r.log.Info("incus: published port", "inst", r.inst, "port", port, "url", url, "public", public)
	return url, nil
}

// UnexposePortPublic removes a published port's Caddy route. (See8bd4-2)
func (r *incusRuntime) UnexposePortPublic(ctx context.Context, port int) error {
	if c := r.caddyClient(); c != nil {
		if err := c.deleteRoute(ctx, r.routeID(port)); err != nil {
			return fmt.Errorf("unexpose port %d: %w", port, err)
		}
	}
	r.portsMu.Lock()
	delete(r.exposed, port)
	r.portsMu.Unlock()
	r.log.Info("incus: unpublished port", "inst", r.inst, "port", port)
	return nil
}

// unpublishAll removes every published route for this workspace. Called on Stop
// so closing a workspace tears down its public subdomains. (See8bd4-2)
func (r *incusRuntime) unpublishAll(ctx context.Context) {
	c := r.caddyClient()
	if c == nil {
		return
	}
	r.portsMu.Lock()
	ports := make([]int, 0, len(r.exposed))
	for p := range r.exposed {
		ports = append(ports, p)
	}
	r.exposed = map[int]exposeState{}
	r.portsMu.Unlock()
	for _, p := range ports {
		_ = c.deleteRoute(ctx, r.routeID(p))
	}
}

// bindAddrFor returns the last-observed bind address for a port ("" if unknown).
func (r *incusRuntime) bindAddrFor(port int) string {
	r.portsMu.RLock()
	defer r.portsMu.RUnlock()
	for _, p := range r.lastPorts {
		if p.Port == port {
			return p.BindAddr
		}
	}
	return ""
}

// deleteRoute removes a route by @id. A 404/missing id is not an error.
func (c *caddyAdminClient) deleteRoute(ctx context.Context, id string) error {
	url := fmt.Sprintf("%s/id/%s", c.base, id)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, url, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("caddy admin: DELETE route: %w", err)
	}
	defer resp.Body.Close()
	// Caddy returns 200 on success; 4xx/5xx when the id doesn't exist — both fine.
	io.Copy(io.Discard, io.LimitReader(resp.Body, 2048)) //nolint:errcheck
	return nil
}
