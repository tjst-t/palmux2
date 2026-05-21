package main

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// TestProbeMode_WithEcho runs probe mode against /bin/bash with -c 'echo hello'
// which exits immediately after printing.  This validates that runProbe exits
// without error when the subprocess returns at least one byte.
func TestProbeMode_WithEcho(t *testing.T) {
	t.Parallel()

	// echo prints "hello" and exits; probe should receive the bytes quickly.
	err := runProbe("/bin/bash", []string{"-c", "echo hello"}, "")
	if err != nil {
		t.Fatalf("runProbe returned error: %v", err)
	}
}

// TestProbeMode_WithCat tests that runProbe works with cat (which echoes stdin).
// cat stays alive waiting for more input, so probe terminates by inactivity timeout.
// We use -c 'cat' explicitly to test the inactivity-timeout path.
func TestProbeMode_WithCat(t *testing.T) {
	t.Parallel()

	// cat echoes "testcat\n" back, then waits; probe terminates by inactivity timeout.
	err := runProbe("/bin/bash", []string{"-c", "cat"}, "testcat\n")
	if err != nil {
		t.Fatalf("runProbe with cat returned error: %v", err)
	}
}

// TestSplitArgs verifies the argument splitter.
func TestSplitArgs(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"-c cat", []string{"-c", "cat"}},
		{"a b  c", []string{"a", "b", "c"}},
	}
	for _, tc := range cases {
		got := splitArgs(tc.in)
		if len(got) != len(tc.want) {
			t.Errorf("splitArgs(%q): got %v want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("splitArgs(%q)[%d]: got %q want %q", tc.in, i, got[i], tc.want[i])
			}
		}
	}
}

// TestProbeOutput_ContainsEcho verifies that probe stdout contains the echoed bytes.
// Calls runProbe directly (same package test) with /bin/bash -c 'cat'.
func TestProbeOutput_ContainsEcho(t *testing.T) {
	t.Parallel()
	// Capture stdout — runProbe writes to fmt.Printf which goes to os.Stdout.
	// We can't intercept os.Stdout easily here, so we call via go run in a
	// subprocess to capture its combined output.
	cmd := exec.Command("go", "run", ".",
		"--probe",
		"--claude-bin=/bin/bash",
		"--claude-args=-c cat",
		"--probe-prompt=testprobe123\n",
	)
	cmd.Dir = "." // already in the package directory
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("go run probe: %v\noutput: %s", err, out)
	}
	if !bytes.Contains(out, []byte("pty: ok")) {
		t.Fatalf("probe output missing 'pty: ok': %s", out)
	}
	if !strings.Contains(string(out), "recv") {
		t.Fatalf("probe output missing 'recv': %s", out)
	}
}
