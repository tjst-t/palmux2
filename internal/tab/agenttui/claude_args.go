package agenttui

import "strings"

// ClaudeArgs is a []string flag value that accumulates repeated --claude-arg
// invocations.  Each use of --claude-arg appends exactly one element; spaces
// inside the argument value are preserved literally and are never split further
// (Fix 5 — replaces the PoC's strings.Fields approach).
//
// Example (as parsed by pflag or the standard flag package):
//
//	--claude-arg --foo --claude-arg "--system-prompt=You are X" --claude-arg --bar
//
// results in:
//
//	ClaudeArgs{"--foo", "--system-prompt=You are X", "--bar"}
//
// ClaudeArgs implements [flag.Value] and [pflag.Value] (pflag requires a Type
// method in addition to String/Set).
type ClaudeArgs []string

// String returns a human-readable representation of the accumulated args.
func (c *ClaudeArgs) String() string {
	if c == nil || len(*c) == 0 {
		return ""
	}
	return strings.Join(*c, " ")
}

// Set appends one argument element.  The value is stored as-is; no further
// splitting or quoting is applied.
func (c *ClaudeArgs) Set(value string) error {
	*c = append(*c, value)
	return nil
}

// Type returns the pflag type name (required by pflag.Value interface).
func (c *ClaudeArgs) Type() string {
	return "stringSlice"
}

// Slice returns a copy of the underlying []string.
func (c ClaudeArgs) Slice() []string {
	out := make([]string, len(c))
	copy(out, c)
	return out
}
