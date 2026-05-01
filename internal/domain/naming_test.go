package domain

import "testing"

func TestSessionNameRoundTrip(t *testing.T) {
	repoID := "tjst-t--palmux--a1b2"
	branchID := "feature--new-ui--7a8b"
	s := SessionName(repoID, branchID)
	if s != "_palmux_tjst-t--palmux--a1b2_feature--new-ui--7a8b" {
		t.Errorf("SessionName = %q", s)
	}
	r, b, ok := ParseSessionName(s)
	if !ok || r != repoID || b != branchID {
		t.Errorf("ParseSessionName = (%q,%q,%v)", r, b, ok)
	}
	// With group suffix
	g := GroupSessionName(s, "conn-xyz")
	r2, b2, ok := ParseSessionName(g)
	if !ok || r2 != repoID || b2 != branchID {
		t.Errorf("ParseSessionName(group) = (%q,%q,%v)", r2, b2, ok)
	}
}

func TestParseSessionName_NonPalmux(t *testing.T) {
	if _, _, ok := ParseSessionName("dev-server"); ok {
		t.Error("expected ok=false for non-palmux session")
	}
	// S009-fix-3: post-prefix repoID must contain `--` (slug+hash). A
	// hand-rolled `_palmux_x_y` lacks that — reject so the host
	// instance's sync_tmux loop doesn't accidentally claim an
	// instance-suffixed peer's `_palmux_dev_<repo>_<branch>` session.
	if IsPalmuxSession("_palmux_x_y") {
		t.Error("_palmux_x_y has no `--` in repoID — not ours")
	}
	if !IsPalmuxSession("_palmux_repo--abcd_main--1234") {
		t.Error("expected IsPalmuxSession true for canonical session")
	}
	if IsPalmuxSession("_palmux_dev_repo--abcd_main--1234") {
		t.Error("default-prefix host should NOT see peer-instance session as ours")
	}
	if IsPalmuxSession("dev-server") {
		t.Error("expected IsPalmuxSession false")
	}
}

func TestWindowNameRoundTrip(t *testing.T) {
	cases := []struct {
		typ, name, want string
	}{
		{"claude", "", "palmux:claude:claude"},
		{"bash", "bash", "palmux:bash:bash"},
		{"bash", "my-server", "palmux:bash:my-server"},
	}
	for _, c := range cases {
		got := WindowName(c.typ, c.name)
		if got != c.want {
			t.Errorf("WindowName(%q,%q) = %q, want %q", c.typ, c.name, got, c.want)
		}
		typ, name, ok := ParseWindowName(got)
		if !ok || typ != c.typ {
			t.Errorf("ParseWindowName(%q) = (%q,%q,%v)", got, typ, name, ok)
		}
	}
}

func TestConfigurePrefix(t *testing.T) {
	// Save and restore the global so we don't leak state into other
	// tests in this package.
	saved := PalmuxSessionPrefix
	t.Cleanup(func() { PalmuxSessionPrefix = saved })

	Configure("_palmux_dev_")
	if got := SessionName("repo--abcd", "main--1234"); got != "_palmux_dev_repo--abcd_main--1234" {
		t.Errorf("SessionName under custom prefix = %q", got)
	}
	r, b, ok := ParseSessionName("_palmux_dev_repo--abcd_main--1234")
	if !ok || r != "repo--abcd" || b != "main--1234" {
		t.Errorf("ParseSessionName under custom prefix = (%q,%q,%v)", r, b, ok)
	}
	// A session with the default prefix is no longer "ours" once the
	// custom prefix is configured.
	if IsPalmuxSession("_palmux_other--abcd_main--1234") {
		t.Error("default-prefix session should not match custom prefix")
	}
	// Custom-prefix session with a repoID-shaped first segment matches.
	if !IsPalmuxSession("_palmux_dev_repo--abcd_main--1234") {
		t.Error("custom-prefix canonical session should match")
	}
	// `_palmux_dev_anything` lacks `--` in `anything` — reject under
	// the same rule that protects the default prefix from peers.
	if IsPalmuxSession("_palmux_dev_anything") {
		t.Error("custom-prefix session without `--` in repoID should not match")
	}

	// Auto-append trailing underscore so callers can pass the human-
	// friendly form (`_palmux_dev`).
	Configure("_palmux_alt")
	if PalmuxSessionPrefix != "_palmux_alt_" {
		t.Errorf("Configure should normalise trailing _, got %q", PalmuxSessionPrefix)
	}

	// Empty string resets to default.
	Configure("")
	if PalmuxSessionPrefix != DefaultPalmuxSessionPrefix {
		t.Errorf("Configure(\"\") should reset to default, got %q", PalmuxSessionPrefix)
	}
}

func TestNextBashWindowName(t *testing.T) {
	if got := NextBashWindowName(map[string]bool{}); got != "palmux:bash:bash" {
		t.Errorf("empty map = %q", got)
	}
	got := NextBashWindowName(map[string]bool{"palmux:bash:bash": true})
	if got != "palmux:bash:bash-2" {
		t.Errorf("got %q, want palmux:bash:bash-2", got)
	}
	got = NextBashWindowName(map[string]bool{
		"palmux:bash:bash":   true,
		"palmux:bash:bash-2": true,
		"palmux:bash:bash-3": true,
	})
	if got != "palmux:bash:bash-4" {
		t.Errorf("got %q, want palmux:bash:bash-4", got)
	}
}
