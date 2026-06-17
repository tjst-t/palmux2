package deploy

import (
	"context"
	"testing"

	"github.com/tjst-t/palmux2/internal/config"
)

type fakeHot struct {
	caddyAdmin string
	claudeBin  string
}

func (f *fakeHot) SetCaddyAdmin(a string)            { f.caddyAdmin = a }
func (f *fakeHot) SetBasicAuthDefaults(u, h string)  {}
func (f *fakeHot) SetClaudeBin(b string)             { f.claudeBin = b }
func (f *fakeHot) SetClaudeArgs(a []string)          {}

func TestCurrentView_MasksSecrets(t *testing.T) {
	dir := t.TempDir()
	applied := config.MasterConfig{Public: config.PublicSection{Domain: "d.example.net"}}
	c := New(dir, applied, SecretPresence{SSOSecret: true, BasicHash: false}, nil, nil)
	v := c.CurrentView()
	if !v.Secrets.HasSSOSecret {
		t.Error("expected HasSSOSecret true")
	}
	if v.Secrets.HasBasicAuthHash {
		t.Error("expected HasBasicAuthHash false")
	}
	if !v.Configured {
		t.Error("a configured (domain set) install should report Configured=true")
	}
}

func TestConfigured_FalseOnFreshInstall(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, config.MasterConfig{}, SecretPresence{}, nil, nil)
	if c.CurrentView().Configured {
		t.Error("fresh install (no master, no domain, no addr) should report Configured=false")
	}
}

func TestSaveAndClassify_HotApplied(t *testing.T) {
	dir := t.TempDir()
	applied := config.MasterConfig{Server: config.ServerSection{CaddyAdmin: "http://localhost:2019", ClaudeBin: "claude"}}
	hot := &fakeHot{}
	c := New(dir, applied, SecretPresence{}, hot, nil)

	neu := applied
	neu.Server.CaddyAdmin = "http://localhost:3000"
	neu.Server.ClaudeBin = "/opt/claude"

	out, err := c.SaveAndClassify(context.Background(), neu, false)
	if err != nil {
		t.Fatalf("SaveAndClassify: %v", err)
	}
	if !out.HotApplied || out.NeedRestart || out.NeedPrivilege {
		t.Errorf("expected hot-only apply, got %+v", out)
	}
	if hot.caddyAdmin != "http://localhost:3000" {
		t.Errorf("caddy admin not hot-applied: %q", hot.caddyAdmin)
	}
	if hot.claudeBin != "/opt/claude" {
		t.Errorf("claude bin not hot-applied: %q", hot.claudeBin)
	}
	// master persisted on disk
	if !config.MasterExists(dir) {
		t.Error("master should be written on apply")
	}
}

func TestSaveAndClassify_RestartAndRoot(t *testing.T) {
	dir := t.TempDir()
	applied := config.MasterConfig{Server: config.ServerSection{Addr: "127.0.0.1:8080"}, Public: config.PublicSection{Domain: "a.example.net"}}
	c := New(dir, applied, SecretPresence{}, &fakeHot{}, nil)

	neu := applied
	neu.Server.Addr = "0.0.0.0:9090"    // restart
	neu.Public.Domain = "b.example.net" // root

	out, err := c.SaveAndClassify(context.Background(), neu, false)
	if err != nil {
		t.Fatal(err)
	}
	if !out.NeedRestart {
		t.Error("addr change should need restart")
	}
	if !out.NeedPrivilege {
		t.Error("domain change should need privilege")
	}
}

func TestSaveAndClassify_RestartFieldNotPrematurelyAdvanced(t *testing.T) {
	// Regression (code-review): a restart-class field (basic_auth_user) must NOT
	// advance the in-memory applied baseline on apply — otherwise a second apply
	// before the operator restarts would see "no changes" and hide the pending
	// restart.
	dir := t.TempDir()
	applied := config.MasterConfig{Public: config.PublicSection{BasicAuthUser: "admin"}}
	c := New(dir, applied, SecretPresence{}, &fakeHot{}, nil)

	neu := applied
	neu.Public.BasicAuthUser = "newadmin"

	out, err := c.SaveAndClassify(context.Background(), neu, false)
	if err != nil {
		t.Fatal(err)
	}
	if !out.NeedRestart {
		t.Fatal("basic_auth_user change should need restart")
	}
	// A second apply with the same target must STILL report the change as
	// pending (the baseline did not advance because no restart happened).
	out2, err := c.SaveAndClassify(context.Background(), neu, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(out2.Changes) == 0 || !out2.NeedRestart {
		t.Errorf("second apply should still show the pending restart-class change, got %+v", out2)
	}
}

func TestSaveAndClassify_DryRunNoWrite(t *testing.T) {
	dir := t.TempDir()
	applied := config.MasterConfig{Server: config.ServerSection{CaddyAdmin: "a"}}
	hot := &fakeHot{}
	c := New(dir, applied, SecretPresence{}, hot, nil)
	neu := applied
	neu.Server.CaddyAdmin = "b"
	_, err := c.SaveAndClassify(context.Background(), neu, true)
	if err != nil {
		t.Fatal(err)
	}
	if config.MasterExists(dir) {
		t.Error("dry-run must not write the master")
	}
	if hot.caddyAdmin != "" {
		t.Error("dry-run must not hot-apply")
	}
}
