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
	if !IsPalmuxSession("_palmux_x_y") {
		t.Error("expected IsPalmuxSession true")
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
