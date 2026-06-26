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
