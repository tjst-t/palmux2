package config

import (
	"strings"
	"testing"
)

func TestValidateDomain(t *testing.T) {
	valid := []string{
		"palmux.example.net",
		"palmux-deploy-test.tjstkm.net",
		"a.b.c.example.org",
		"x1.example.io",
	}
	for _, d := range valid {
		if err := ValidateDomain(d); err != nil {
			t.Errorf("ValidateDomain(%q) = %v, want nil", d, err)
		}
	}

	// Injection / malformed attempts must all be rejected (AC-Sa53137-4-3).
	invalid := []string{
		"",
		"localhost",                              // single label
		"example.net {\n  reverse_proxy evil}",   // directive injection
		"example.net\nreverse_proxy 1.1.1.1:80",  // newline injection
		"exam ple.net",                            // space
		"example.net; rm -rf",                     // semicolon
		"example.net\"",                           // quote
		"-bad.example.net",                        // leading hyphen label
		"example.net}",                            // brace
		"{env.X}.example.net",                     // env placeholder injection
		strings.Repeat("a", 300) + ".net",        // too long
	}
	for _, d := range invalid {
		if err := ValidateDomain(d); err == nil {
			t.Errorf("ValidateDomain(%q) = nil, want error (injection/malformed)", d)
		}
	}
}

func TestRenderCaddyfile_NoInjection(t *testing.T) {
	// A valid domain renders the expected apex + wildcard structure.
	out, err := RenderCaddyfile("palmux.example.net", "127.0.0.1:8080", "", true)
	if err != nil {
		t.Fatalf("RenderCaddyfile: %v", err)
	}
	for _, want := range []string{
		"palmux.example.net {",
		"*.palmux.example.net {",
		"forward_auth 127.0.0.1:8080",
		"uri /auth/verify",
		"dns cloudflare {env.CLOUDFLARE_API_TOKEN}",
		"admin localhost:2019",
		"encode zstd gzip", // parity with install.sh default Caddyfile (compression)
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered Caddyfile missing %q\n---\n%s", want, out)
		}
	}

	// A malformed domain must be refused before any rendering.
	if _, err := RenderCaddyfile("evil {\nreverse_proxy bad}", "127.0.0.1:8080", "", true); err == nil {
		t.Error("RenderCaddyfile accepted an injection domain")
	}

	// requireAuth=false omits forward_auth.
	plain, err := RenderCaddyfile("plain.example.net", "127.0.0.1:8080", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "forward_auth") {
		t.Error("requireAuth=false should not emit forward_auth")
	}
}
