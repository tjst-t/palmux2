package ptyhost

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// TestBuildSystemdRunArgv asserts the exact argv shape mandated by
// AC-S3f2658-1-2: systemd-run --user --scope --collect --unit
// palmux-agent-<instancePrefix>-<hash> -- <palmuxBin> ptyhost <args...>.
func TestBuildSystemdRunArgv(t *testing.T) {
	argv := BuildSystemdRunArgv("dev", "repo/branch/tab", "/usr/local/bin/palmux", []string{"--socket", "/run/x.sock", "--", "claude", "--foo"})

	want := []string{
		"systemd-run", "--user", "--scope", "--collect", "--unit",
		ScopeUnitName("dev", "repo/branch/tab"),
		"--",
		"/usr/local/bin/palmux", "ptyhost",
		"--socket", "/run/x.sock", "--", "claude", "--foo",
	}
	if len(argv) != len(want) {
		t.Fatalf("argv = %v (len %d), want %v (len %d)", argv, len(argv), want, len(want))
	}
	for i := range want {
		if argv[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (full argv=%v)", i, argv[i], want[i], argv)
		}
	}
}

// TestScopeUnitName_EmbedsInstancePrefix asserts the scope unit name embeds
// instancePrefix and differs between prefixes for the same seed, so
// INSTANCE=dev and the host instance never collide/GC each other.
func TestScopeUnitName_EmbedsInstancePrefix(t *testing.T) {
	hostName := ScopeUnitName("palmux", "same-seed")
	devName := ScopeUnitName("pmx_dev", "same-seed")

	if !strings.Contains(hostName, "palmux") {
		t.Fatalf("host unit name %q does not embed instance prefix", hostName)
	}
	if !strings.Contains(devName, "pmx_dev") {
		t.Fatalf("dev unit name %q does not embed instance prefix", devName)
	}
	if hostName == devName {
		t.Fatalf("host and dev unit names must differ: both = %q", hostName)
	}
	if !strings.HasPrefix(hostName, "palmux-agent-") || !strings.HasPrefix(devName, "palmux-agent-") {
		t.Fatalf("unit names must start with palmux-agent-: host=%q dev=%q", hostName, devName)
	}
}

// TestScopeUnitName_TrimsTmuxPrefixUnderscores asserts the tmux-prefix
// convention's leading/trailing underscores (e.g. "_palmux_", "_pmx_dev_")
// are normalized so the systemd unit name reads naturally, while empty
// falls back to "palmux".
func TestScopeUnitName_TrimsTmuxPrefixUnderscores(t *testing.T) {
	if got := ScopeUnitName("_palmux_", "seed"); !strings.Contains(got, "palmux-agent-palmux-") {
		t.Fatalf("ScopeUnitName(_palmux_) = %q, want to contain palmux-agent-palmux-", got)
	}
	if got := ScopeUnitName("_pmx_dev_", "seed"); !strings.Contains(got, "palmux-agent-pmx_dev-") {
		t.Fatalf("ScopeUnitName(_pmx_dev_) = %q, want to contain palmux-agent-pmx_dev-", got)
	}
	if got := ScopeUnitName("", "seed"); !strings.Contains(got, "palmux-agent-palmux-") {
		t.Fatalf("ScopeUnitName(\"\") = %q, want to default to palmux-agent-palmux-", got)
	}
}

// TestLauncher_SystemdRunSucceeds_NoFallback asserts that when the injected
// systemd-run runner succeeds, the launcher reports MethodSystemdRun and
// never invokes the setsid fallback.
func TestLauncher_SystemdRunSucceeds_NoFallback(t *testing.T) {
	var systemdArgvSeen []string
	setsidCalled := false

	l := &Launcher{
		RunSystemdScope: func(_ context.Context, argv []string) error {
			systemdArgvSeen = append([]string(nil), argv...)
			return nil
		},
		RunSetsid: func(_ context.Context, _ []string) error {
			setsidCalled = true
			return nil
		},
	}

	result, err := l.Launch(context.Background(), LaunchConfig{
		PalmuxBin:      "/usr/local/bin/palmux",
		InstancePrefix: "palmux",
		Seed:           "seed-1",
		Args:           []string{"--socket", "/run/x.sock", "--status", "/run/x.json", "--", "claude"},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if result.Method != MethodSystemdRun {
		t.Fatalf("Method = %q, want %q", result.Method, MethodSystemdRun)
	}
	if setsidCalled {
		t.Fatal("setsid fallback should not be called when systemd-run succeeds")
	}
	if systemdArgvSeen[0] != "systemd-run" {
		t.Fatalf("systemd argv[0] = %q, want systemd-run", systemdArgvSeen[0])
	}
	wantUnit := ScopeUnitName("palmux", "seed-1")
	if result.UnitName != wantUnit {
		t.Fatalf("UnitName = %q, want %q", result.UnitName, wantUnit)
	}
}

// TestLauncher_SystemdRunFails_FallsBackToSetsid asserts that when the
// injected systemd-run runner fails (simulating a missing D-Bus user session
// / non-systemd host), the launcher falls back to the setsid path and the
// resulting argv contains no "systemd-run" (AC-S3f2658-1-2).
func TestLauncher_SystemdRunFails_FallsBackToSetsid(t *testing.T) {
	var setsidArgvSeen []string

	l := &Launcher{
		RunSystemdScope: func(_ context.Context, _ []string) error {
			return errors.New("Failed to connect to bus: No medium found")
		},
		RunSetsid: func(_ context.Context, argv []string) error {
			setsidArgvSeen = append([]string(nil), argv...)
			return nil
		},
	}

	result, err := l.Launch(context.Background(), LaunchConfig{
		PalmuxBin: "/usr/local/bin/palmux",
		Seed:      "seed-2",
		Args:      []string{"--socket", "/run/y.sock", "--status", "/run/y.json", "--", "claude"},
	})
	if err != nil {
		t.Fatalf("Launch: %v", err)
	}
	if result.Method != MethodSetsid {
		t.Fatalf("Method = %q, want %q", result.Method, MethodSetsid)
	}
	for _, a := range setsidArgvSeen {
		if a == "systemd-run" {
			t.Fatalf("setsid fallback argv must not contain systemd-run: %v", setsidArgvSeen)
		}
	}
	want := []string{"/usr/local/bin/palmux", "ptyhost", "--socket", "/run/y.sock", "--status", "/run/y.json", "--", "claude"}
	if len(setsidArgvSeen) != len(want) {
		t.Fatalf("setsid argv = %v, want %v", setsidArgvSeen, want)
	}
	for i := range want {
		if setsidArgvSeen[i] != want[i] {
			t.Fatalf("setsid argv[%d] = %q, want %q", i, setsidArgvSeen[i], want[i])
		}
	}
}

// TestLauncher_BothFail_ReturnsError asserts a clear error when neither path
// works (should never happen in practice, but must not panic/hang).
func TestLauncher_BothFail_ReturnsError(t *testing.T) {
	l := &Launcher{
		RunSystemdScope: func(_ context.Context, _ []string) error { return errors.New("no dbus") },
		RunSetsid:       func(_ context.Context, _ []string) error { return errors.New("fork failed") },
	}
	_, err := l.Launch(context.Background(), LaunchConfig{
		PalmuxBin: "/usr/local/bin/palmux",
		Args:      []string{"--socket", "/run/z.sock"},
	})
	if err == nil {
		t.Fatal("expected an error when both launch paths fail")
	}
}

// TestLauncher_RequiresPalmuxBinAndArgs asserts basic input validation.
func TestLauncher_RequiresPalmuxBinAndArgs(t *testing.T) {
	l := &Launcher{}
	if _, err := l.Launch(context.Background(), LaunchConfig{Args: []string{"x"}}); err == nil {
		t.Fatal("expected error for empty PalmuxBin")
	}
	if _, err := l.Launch(context.Background(), LaunchConfig{PalmuxBin: "/bin/palmux"}); err == nil {
		t.Fatal("expected error for empty Args")
	}
}
