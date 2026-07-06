package deploy

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjst-t/palmux2/internal/config"
)

func TestParseRebuildShow(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		wantActive  string
		wantResult  string
		wantRunning bool
	}{
		{"running", "ActiveState=activating\nResult=success\n", "activating", "success", true},
		{"done", "ActiveState=inactive\nResult=success\n", "inactive", "success", false},
		{"failed", "ActiveState=failed\nResult=exit-code\n", "failed", "exit-code", false},
		{"reordered+blank", "\nResult=success\n\nActiveState=active\n", "active", "success", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := parseRebuildShow(tc.in)
			if st.Active != tc.wantActive || st.Result != tc.wantResult || st.Running != tc.wantRunning {
				t.Errorf("parseRebuildShow(%q) = %+v; want active=%s result=%s running=%v",
					tc.in, st, tc.wantActive, tc.wantResult, tc.wantRunning)
			}
		})
	}
}

// fakeSystemctl writes a tiny script as $bin/systemctl that records argv and
// emits canned `show` output, so Trigger/Query can be exercised without systemd.
func fakeSystemctl(t *testing.T, showOut string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	// %b so \n escapes in showOut expand to real newlines.
	script := "#!/bin/sh\necho \"$@\" >> " + logPath + "\n" +
		"case \"$1\" in show) printf '%b' '" + showOut + "';; esac\nexit 0\n"
	p := filepath.Join(dir, "systemctl")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := systemctlBin
	systemctlBin = p
	t.Cleanup(func() { systemctlBin = old })
	return logPath
}

func TestTriggerRebuild_StartsUnitNoBlock(t *testing.T) {
	logPath := fakeSystemctl(t, "")
	if err := TriggerRebuild(context.Background()); err != nil {
		t.Fatalf("TriggerRebuild: %v", err)
	}
	b, _ := os.ReadFile(logPath)
	got := string(b)
	if !strings.Contains(got, "reset-failed "+rebuildUnit) {
		t.Errorf("expected reset-failed call, got:\n%s", got)
	}
	if !strings.Contains(got, "start --no-block "+rebuildUnit) {
		t.Errorf("expected `start --no-block %s`, got:\n%s", rebuildUnit, got)
	}
}

func TestQueryRebuild_ParsesShow(t *testing.T) {
	fakeSystemctl(t, "ActiveState=inactive\\nResult=success\\n")
	st, err := QueryRebuild(context.Background())
	if err != nil {
		t.Fatalf("QueryRebuild: %v", err)
	}
	if st.Active != "inactive" || st.Result != "success" || st.Running {
		t.Errorf("QueryRebuild = %+v; want inactive/success/not-running", st)
	}
}

// [AC-S673a42-2-1] The version-update path starts the DISTINCT verb-limited
// update unit (palmux-rebuild-update.service), not the domain unit — so the
// privileged switch that runs `nix flake update palmux` is a separate fixed unit
// (no arbitrary command reaches root).
func TestTriggerRebuildUpdate_StartsUpdateUnit(t *testing.T) {
	logPath := fakeSystemctl(t, "")
	if err := TriggerRebuildUpdate(context.Background()); err != nil {
		t.Fatalf("TriggerRebuildUpdate: %v", err)
	}
	got := readFile(t, logPath)
	if !strings.Contains(got, "reset-failed "+rebuildUpdateUnit) {
		t.Errorf("expected reset-failed of update unit, got:\n%s", got)
	}
	if !strings.Contains(got, "start --no-block "+rebuildUpdateUnit) {
		t.Errorf("expected `start --no-block %s`, got:\n%s", rebuildUpdateUnit, got)
	}
	// It must NOT start the domain unit.
	if strings.Contains(got, "start --no-block "+rebuildUnit) {
		t.Errorf("update path must not start the domain unit %s, got:\n%s", rebuildUnit, got)
	}
}

// [AC-S673a42-2-2] The update unit's state is queryable (the FE polls it to catch
// a failure that never restarts palmux2).
func TestQueryRebuildUpdate_ParsesShow(t *testing.T) {
	logPath := fakeSystemctl(t, "ActiveState=failed\\nResult=exit-code\\n")
	st, err := QueryRebuildUpdate(context.Background())
	if err != nil {
		t.Fatalf("QueryRebuildUpdate: %v", err)
	}
	if st.Active != "failed" || st.Result != "exit-code" || st.Running {
		t.Errorf("QueryRebuildUpdate = %+v; want failed/exit-code/not-running", st)
	}
	if got := readFile(t, logPath); !strings.Contains(got, "show -p ActiveState -p Result "+rebuildUpdateUnit) {
		t.Errorf("expected show of update unit, got:\n%s", got)
	}
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, _ := os.ReadFile(p)
	return string(b)
}

// [AC] On a NixOS host, a domain change's apply message points at nixos-rebuild,
// not `palmux reconcile-system`.
func TestSaveAndClassify_NixOSMessage(t *testing.T) {
	dir := t.TempDir()
	applied := config.MasterConfig{Public: config.PublicSection{Domain: "a.example.net"}}
	c := New(dir, applied, SecretPresence{}, &fakeHot{}, nil)
	c.SetNixOSHost(true)

	neu := applied
	neu.Public.Domain = "b.example.net" // root-class

	out, err := c.SaveAndClassify(context.Background(), neu, true)
	if err != nil {
		t.Fatal(err)
	}
	if !out.NeedPrivilege {
		t.Fatal("domain change should need privilege")
	}
	if strings.Contains(out.Message, "reconcile-system") {
		t.Errorf("NixOS message must not mention reconcile-system, got: %q", out.Message)
	}
	if !strings.Contains(out.Message, "nixos-rebuild") {
		t.Errorf("NixOS message should mention nixos-rebuild, got: %q", out.Message)
	}

	// And the view advertises NixOSHost so the GUI can branch.
	if !c.CurrentView().NixOSHost {
		t.Error("CurrentView should report NixOSHost=true after SetNixOSHost(true)")
	}
}

// [AC] The Cloudflare API token written via RotateSecrets lands in secrets.env as
// CLOUDFLARE_API_TOKEN (not the palmux auth token) and flips the presence flag —
// regression for the onboarding bug that mis-sent it as `token`.
func TestRotateSecrets_CloudflareToken(t *testing.T) {
	dir := t.TempDir()
	c := New(dir, config.MasterConfig{}, SecretPresence{}, &fakeHot{}, nil)

	if _, err := c.RotateSecrets(config.Secrets{CloudflareToken: "cf-secret-123"}, nil, ""); err != nil {
		t.Fatalf("RotateSecrets: %v", err)
	}
	if !c.CurrentView().Secrets.HasCloudflareToken {
		t.Error("HasCloudflareToken should be true after rotating the CF token")
	}
	_, sec, err := config.LoadServerConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if sec.CloudflareToken != "cf-secret-123" {
		t.Errorf("CLOUDFLARE_API_TOKEN not persisted; got %q", sec.CloudflareToken)
	}
	if sec.Token != "" {
		t.Errorf("CF token must NOT leak into PALMUX_TOKEN; got %q", sec.Token)
	}
}
