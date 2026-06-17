package config

import (
	"fmt"
	"regexp"
	"strings"
)

// Sa53137-4: fixed-template Caddyfile rendering for `palmux reconcile-system`.
//
// SECURITY: the master config is treated as UNTRUSTED input (it is writable by
// the palmux user process and, via the GUI, by anyone who can reach the web
// UI). The domain is the ONLY interpolation point and is validated against a
// strict RFC1123 hostname regex before it ever reaches the template. The
// template is a fixed string with no other user-controlled values — there is no
// path where a Caddy directive could be injected. This mirrors the install.sh
// Caddyfile exactly (apex forward_auth SSO + wildcard 502 catch-all + Cloudflare
// DNS-01 TLS) so reconcile reproduces the same edge as a fresh install.

// hostnameRe matches a valid DNS hostname (RFC1123 labels, 1-63 chars each,
// no leading/trailing hyphen, at least two labels). Deliberately strict: no
// spaces, braces, newlines, or Caddy directive characters can pass.
var hostnameRe = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,63}$`)

// ValidateDomain returns an error if domain is not a syntactically valid,
// reasonably-bounded public hostname. Empty is rejected (reconcile requires a
// domain). The total length cap (253) is the DNS maximum.
func ValidateDomain(domain string) error {
	d := strings.TrimSpace(domain)
	if d == "" {
		return fmt.Errorf("domain is empty")
	}
	if len(d) > 253 {
		return fmt.Errorf("domain too long (%d > 253)", len(d))
	}
	// Defence in depth: reject any character that could break out of the
	// template even though the regex below already excludes them.
	if strings.ContainsAny(d, " \t\r\n{}\"'\\;#") {
		return fmt.Errorf("domain contains forbidden characters")
	}
	if !hostnameRe.MatchString(d) {
		return fmt.Errorf("invalid hostname %q (must be a dotted DNS name like palmux.example.net)", d)
	}
	return nil
}

// RenderCaddyfile produces the /etc/caddy/Caddyfile content for the given
// public domain and palmux upstream (host:port). domain MUST have already
// passed ValidateDomain. requireAuth toggles the apex forward_auth SSO block
// (true when basic auth / SSO is configured). acmeEmail is optional.
//
// The output is byte-compatible with the install.sh template so a reconcile and
// a fresh install converge on the same edge.
func RenderCaddyfile(domain, upstream, acmeEmail string, requireAuth bool) (string, error) {
	if err := ValidateDomain(domain); err != nil {
		return "", fmt.Errorf("reconcile: %w", err)
	}
	if upstream == "" {
		upstream = "127.0.0.1:8080"
	}
	// upstream is palmux-internal (loopback host:port), never user-typed via
	// the master; still keep it constrained to host:port shape.
	if strings.ContainsAny(upstream, " \t\r\n{}\"'\\;#") {
		return "", fmt.Errorf("reconcile: invalid upstream")
	}

	var b strings.Builder
	b.WriteString("# Managed by palmux reconcile-system. Do not edit by hand.\n")
	b.WriteString("{\n")
	b.WriteString("\tadmin localhost:2019\n")
	if e := strings.TrimSpace(acmeEmail); e != "" && hostnameSafeEmail(e) {
		fmt.Fprintf(&b, "\temail %s\n", e)
	}
	b.WriteString("}\n\n")

	// Apex vhost.
	fmt.Fprintf(&b, "%s {\n", domain)
	if requireAuth {
		b.WriteString("\t@palmux_auth path /auth/*\n")
		fmt.Fprintf(&b, "\thandle @palmux_auth {\n\t\treverse_proxy %s\n\t}\n", upstream)
		b.WriteString("\thandle {\n")
		fmt.Fprintf(&b, "\t\tforward_auth %s {\n\t\t\turi /auth/verify\n\t\t}\n", upstream)
		fmt.Fprintf(&b, "\t\treverse_proxy %s\n", upstream)
		b.WriteString("\t}\n")
	} else {
		fmt.Fprintf(&b, "\treverse_proxy %s\n", upstream)
	}
	b.WriteString("\ttls {\n\t\tdns cloudflare {env.CLOUDFLARE_API_TOKEN}\n\t}\n")
	b.WriteString("\tencode zstd gzip\n")
	b.WriteString("}\n\n")

	// Wildcard vhost — per-port routes are injected by palmux via the admin
	// API; the static catch-all returns 502 until a route is present.
	fmt.Fprintf(&b, "*.%s {\n", domain)
	b.WriteString("\ttls {\n\t\tdns cloudflare {env.CLOUDFLARE_API_TOKEN}\n\t}\n")
	b.WriteString("\tencode zstd gzip\n")
	b.WriteString("\trespond \"no upstream\" 502\n")
	b.WriteString("}\n")
	return b.String(), nil
}

// hostnameSafeEmail does a minimal sanity check on an ACME contact email so a
// malformed value can't inject into the template. Optional field.
func hostnameSafeEmail(e string) bool {
	if strings.ContainsAny(e, " \t\r\n{}\"'\\;#") {
		return false
	}
	at := strings.IndexByte(e, '@')
	return at > 0 && at < len(e)-1
}
