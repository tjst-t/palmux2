package main

import "testing"

// withStubBridgeIP temporarily replaces lookupIncusBridgeIP for the duration
// of the test, restoring the original afterward. Lets these tests exercise
// both "incus bridge present" and "incus bridge absent" deterministically,
// regardless of whether the machine running `go test` actually has an
// incusbr0 interface.
func withStubBridgeIP(t *testing.T, ip string) {
	t.Helper()
	orig := lookupIncusBridgeIP
	lookupIncusBridgeIP = func() string { return ip }
	t.Cleanup(func() { lookupIncusBridgeIP = orig })
}

// Sc4f091-1: wildcard main --addr + an incus bridge present must still
// resolve to a real, non-empty, reachable notify URL — this is the bug fix
// itself. Before the fix, bridgeNotifyURL delegated to incusBridgeListenAddr
// (which intentionally returns "" for a wildcard bind, to avoid an extra
// listener colliding with it) and so ALSO returned "", silently disabling
// in-container agent notify hooks on every palmuxOS appliance with no public
// domain configured (nixos/modules/appliance.nix's bindAddr default).
func TestBridgeNotifyURL_WildcardAddr_IncusPresent(t *testing.T) {
	withStubBridgeIP(t, "10.215.187.1")

	for _, addr := range []string{"0.0.0.0:7683", ":7683", "[::]:7683"} {
		got := bridgeNotifyURL(addr, "/")
		want := "http://10.215.187.1:7683/api/notify"
		if got != want {
			t.Errorf("bridgeNotifyURL(%q) = %q, want %q (must not be empty/suppressed just because the main listener is wildcard-bound)", addr, got, want)
		}
	}
}

// Sc4f091-1: wildcard main --addr + NO incus bridge at all (no incus runtime
// configured on this host) must fall back sensibly — i.e. still return "" so
// the caller's existing "skip the hook" behavior kicks in, rather than
// panicking or fabricating an unreachable/broken URL.
func TestBridgeNotifyURL_WildcardAddr_NoIncus(t *testing.T) {
	withStubBridgeIP(t, "")

	for _, addr := range []string{"0.0.0.0:7683", ":7683", "[::]:7683"} {
		got := bridgeNotifyURL(addr, "/")
		if got != "" {
			t.Errorf("bridgeNotifyURL(%q) = %q, want \"\" (no incus bridge available, must not fabricate a broken URL)", addr, got)
		}
	}
}

// Sc4f091-1 regression guard (AC-Sc4f091-1-3): the NON-wildcard case (a
// specific host in --addr, e.g. 127.0.0.1 — the production default, or any
// public-domain-configured deployment per Sbe4eee/See8bd4) must be completely
// unaffected by this fix. This is the exact behavior bridgeNotifyURL had
// before the change: same bridge IP, same port, same URL.
func TestBridgeNotifyURL_NonWildcardAddr_Unaffected(t *testing.T) {
	withStubBridgeIP(t, "10.215.187.1")

	got := bridgeNotifyURL("127.0.0.1:7683", "/")
	want := "http://10.215.187.1:7683/api/notify"
	if got != want {
		t.Errorf("bridgeNotifyURL(127.0.0.1:7683) = %q, want %q", got, want)
	}

	// And with no incus bridge, the non-wildcard case must also still
	// gracefully return "" (unchanged pre-existing behavior — no incus means
	// no in-container notify path regardless of bind address).
	withStubBridgeIP(t, "")
	got = bridgeNotifyURL("127.0.0.1:7683", "/")
	if got != "" {
		t.Errorf("bridgeNotifyURL(127.0.0.1:7683) with no incus bridge = %q, want \"\"", got)
	}
}

// bridgeNotifyURL must never derive a port-less or malformed URL; if --addr
// itself is malformed (no port), it must degrade to "" rather than panic.
func TestBridgeNotifyURL_NoPort(t *testing.T) {
	withStubBridgeIP(t, "10.215.187.1")
	got := bridgeNotifyURL("not-a-valid-addr", "/")
	if got != "" {
		t.Errorf("bridgeNotifyURL(no port) = %q, want \"\"", got)
	}
}

// incusBridgeListenAddr's OWN behavior (the "do we need an extra listener?"
// decision) must remain byte-identical to before this fix — this is the
// function still used at the http.Server extra-listener call site in
// serve(), and reintroducing a listener on a wildcard bind would collide
// with the main listener ("address already in use"), which is exactly the
// S7364e3-era bug this function was originally written to avoid.
func TestIncusBridgeListenAddr_StillSuppressesOnWildcard(t *testing.T) {
	withStubBridgeIP(t, "10.215.187.1")

	for _, addr := range []string{"0.0.0.0:7683", ":7683", "[::]:7683"} {
		if got := incusBridgeListenAddr(addr); got != "" {
			t.Errorf("incusBridgeListenAddr(%q) = %q, want \"\" (extra listener must stay suppressed on wildcard bind, else it collides with the main listener)", addr, got)
		}
	}

	// Non-wildcard: extra listener IS still wanted (unchanged).
	got := incusBridgeListenAddr("127.0.0.1:7683")
	want := "10.215.187.1:7683"
	if got != want {
		t.Errorf("incusBridgeListenAddr(127.0.0.1:7683) = %q, want %q", got, want)
	}
}

func TestIncusBridgeListenAddr_NoIncus(t *testing.T) {
	withStubBridgeIP(t, "")
	if got := incusBridgeListenAddr("127.0.0.1:7683"); got != "" {
		t.Errorf("incusBridgeListenAddr with no incus bridge = %q, want \"\"", got)
	}
}
