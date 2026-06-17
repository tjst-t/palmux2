package config

import "testing"

func TestDiffMaster_Classes(t *testing.T) {
	old := MasterConfig{
		Server: ServerSection{Addr: "127.0.0.1:8080", CaddyAdmin: "http://localhost:2019", ClaudeBin: "claude"},
		Public: PublicSection{Domain: "old.example.net", BasicAuthUser: "admin"},
	}
	neu := old
	neu.Server.Addr = "0.0.0.0:9090"        // restart
	neu.Server.CaddyAdmin = "http://x:2019" // hot
	neu.Server.ClaudeBin = "/usr/local/bin/claude"
	neu.Public.Domain = "new.example.net" // root

	changes := DiffMaster(old, neu)
	got := map[string]ChangeClass{}
	for _, c := range changes {
		got[c.Field] = c.Class
	}
	if got["server.addr"] != ClassRestart {
		t.Errorf("server.addr class = %v, want restart", got["server.addr"])
	}
	if got["server.caddy_admin"] != ClassHot {
		t.Errorf("server.caddy_admin class = %v, want hot", got["server.caddy_admin"])
	}
	if got["server.claude_bin"] != ClassHot {
		t.Errorf("server.claude_bin class = %v, want hot", got["server.claude_bin"])
	}
	if got["public.domain"] != ClassRoot {
		t.Errorf("public.domain class = %v, want root", got["public.domain"])
	}
	if HighestClass(changes) != ClassRoot {
		t.Errorf("HighestClass = %v, want root", HighestClass(changes))
	}
}

func TestDiffMaster_NoChange(t *testing.T) {
	m := MasterConfig{Server: ServerSection{Addr: "x"}}
	if c := DiffMaster(m, m); len(c) != 0 {
		t.Errorf("identical configs should diff to nothing, got %v", c)
	}
}
