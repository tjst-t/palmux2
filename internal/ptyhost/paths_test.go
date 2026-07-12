package ptyhost

import (
	"os"
	"strings"
	"testing"
)

// TestRunDir_EmbedsInstancePrefix is the socket-directory half of
// AC-S3f2658-3-3 (ScopeUnitName's half is already covered by
// TestScopeUnitName_EmbedsInstancePrefix in launch_test.go): the host
// default and an INSTANCE=dev-style prefix must resolve to DIFFERENT run
// directories, so discovery/orphan-GC scoped to one instancePrefix can never
// see the other's sockets.
func TestRunDir_EmbedsInstancePrefix(t *testing.T) {
	// Force the XDG_RUNTIME_DIR branch so this test is deterministic
	// regardless of the host's actual environment.
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/9999")

	host := RunDir("palmux")     // default --tmux-prefix derivation
	dev := RunDir("pmx_dev")     // INSTANCE=dev derivation (S009-fix-3)
	other := RunDir("pmx_other") // a third, arbitrary instance

	if host == dev || host == other || dev == other {
		t.Fatalf("RunDir did not separate by instancePrefix: host=%q dev=%q other=%q", host, dev, other)
	}
	if !strings.Contains(host, "palmux") {
		t.Errorf("RunDir(%q) = %q, want it to embed the sanitized prefix", "palmux", host)
	}
	if !strings.Contains(dev, "pmx_dev") {
		t.Errorf("RunDir(%q) = %q, want it to embed the sanitized prefix", "pmx_dev", dev)
	}

	// The underscore-wrapped tmux-prefix convention (e.g. "_palmux_",
	// "_pmx_dev_") must sanitize to the SAME directory as its bare form —
	// RunDir and ScopeUnitName must agree on instance identity (see
	// sanitizeInstancePrefix's doc comment).
	if RunDir("_palmux_") != host {
		t.Errorf("RunDir(_palmux_) = %q, want it to equal RunDir(palmux) = %q", RunDir("_palmux_"), host)
	}
	if RunDir("_pmx_dev_") != dev {
		t.Errorf("RunDir(_pmx_dev_) = %q, want it to equal RunDir(pmx_dev) = %q", RunDir("_pmx_dev_"), dev)
	}
}

// TestRunDir_TempDirFallback_EmbedsInstancePrefix is the same guarantee for
// hosts without XDG_RUNTIME_DIR set (e.g. a non-systemd `make serve` dev
// rig).
func TestRunDir_TempDirFallback_EmbedsInstancePrefix(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	// Some CI/dev shells still have it set in os.Environ() even after
	// Setenv(""); ensure it's genuinely unset for this test.
	_ = os.Unsetenv("XDG_RUNTIME_DIR")

	host := RunDir("palmux")
	dev := RunDir("pmx_dev")
	if host == dev {
		t.Fatalf("RunDir (TempDir fallback) did not separate by instancePrefix: host=%q dev=%q", host, dev)
	}
}
