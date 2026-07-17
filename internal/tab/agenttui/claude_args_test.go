package agenttui

import (
	"reflect"
	"testing"
)

// TestRepeatedClaudeArg is the Fix 5 test.
//
// Simulates three separate --claude-arg invocations; asserts that the result
// is a []string with exactly 3 elements and that spaces inside an argument
// value are preserved literally (no further splitting).
func TestRepeatedClaudeArg(t *testing.T) {
	var ca ClaudeArgs

	// Simulate three flag.Value.Set() calls (one per --claude-arg invocation).
	inputs := []string{
		"--foo",
		"--system-prompt=You are X",
		"--bar",
	}
	for _, v := range inputs {
		if err := ca.Set(v); err != nil {
			t.Fatalf("Set(%q): %v", v, err)
		}
	}

	want := []string{"--foo", "--system-prompt=You are X", "--bar"}
	if !reflect.DeepEqual([]string(ca), want) {
		t.Fatalf("ClaudeArgs = %v, want %v", ca, want)
	}
}

// TestClaudeArgsSpacePreserved ensures an argument containing a space is stored
// as a single element, not split.
func TestClaudeArgsSpacePreserved(t *testing.T) {
	var ca ClaudeArgs
	if err := ca.Set("--system-prompt=You are helpful"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if len(ca) != 1 {
		t.Fatalf("len = %d, want 1; got %v", len(ca), ca)
	}
	if ca[0] != "--system-prompt=You are helpful" {
		t.Fatalf("ca[0] = %q, want %q", ca[0], "--system-prompt=You are helpful")
	}
}

// TestClaudeArgsSlice verifies the Slice() method returns a defensive copy.
func TestClaudeArgsSlice(t *testing.T) {
	var ca ClaudeArgs
	ca.Set("--a")
	ca.Set("--b")
	s := ca.Slice()
	s[0] = "MUTATED"
	if ca[0] != "--a" {
		t.Fatalf("Slice() did not return a copy; ca[0] = %q", ca[0])
	}
}

// TestClaudeArgsString verifies String() for basic display.
func TestClaudeArgsString(t *testing.T) {
	var ca ClaudeArgs
	ca.Set("--foo")
	ca.Set("--bar")
	got := ca.String()
	if got != "--foo --bar" {
		t.Fatalf("String() = %q, want %q", got, "--foo --bar")
	}
}

// TestClaudeArgsType verifies the pflag Type() string.
func TestClaudeArgsType(t *testing.T) {
	var ca ClaudeArgs
	if got := ca.Type(); got != "stringSlice" {
		t.Fatalf("Type() = %q, want %q", got, "stringSlice")
	}
}
