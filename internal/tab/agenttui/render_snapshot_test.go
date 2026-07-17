package agenttui

import (
	"os"
	"strings"
	"testing"
)

// feedFixture builds an emulator of the given size, drains its response pipe,
// and feeds it the named testdata byte capture. Skips if the fixture is absent.
func feedFixture(t *testing.T, name string, cols, rows int) *Emulator {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Skipf("no fixture %s: %v", name, err)
	}
	e := NewEmulator(cols, rows, nil, "repo", "branch")
	t.Cleanup(e.Close)
	go func() {
		buf := make([]byte, 4096)
		for {
			if _, rerr := e.Read(buf); rerr != nil {
				return
			}
		}
	}()
	e.Feed(raw)
	return e
}

// TestRenderSnapshotIsFramedAndClean feeds a real captured claude cold-start
// byte stream (the concatenation of many in-place repaint frames — claude does
// NOT use the alternate screen) and asserts RenderSnapshot() produces a single
// clean, framed reconstruction rather than the stacked repaint history. This is
// the fix for "scroll up shows broken logs": the attach replay sends this
// snapshot instead of the raw byte ring.
func TestRenderSnapshotIsFramedAndClean(t *testing.T) {
	raw, _ := os.ReadFile("testdata/claude_coldstart.bin")
	e := feedFixture(t, "claude_coldstart.bin", 109, 33)

	snap := e.RenderSnapshot()
	s := string(snap)

	if !strings.HasPrefix(s, "\x1b[?25l\x1b[H\x1b[2J") {
		t.Errorf("snapshot must start with hide-cursor + home + clear; got %q", s[:min(20, len(s))])
	}
	if !strings.HasSuffix(s, "\x1b[?25h") {
		t.Errorf("snapshot must end by showing the cursor")
	}
	// The collapsed snapshot is far smaller than the raw repaint history.
	if len(raw) > 0 && len(snap) >= len(raw) {
		t.Errorf("snapshot (%d B) not smaller than raw history (%d B)", len(snap), len(raw))
	}
	if !strings.Contains(s, "Claude") {
		t.Errorf("snapshot does not contain expected banner text")
	}
}

// TestRenderSnapshotIncludesScrollback feeds a real long claude response (which
// scrolls genuine output off the top of the screen) and asserts the snapshot
// carries that scrollback history, so a freshly-attached client can scroll up
// into the earlier output — not just the current screen.
func TestRenderSnapshotIncludesScrollback(t *testing.T) {
	e := feedFixture(t, "claude_long.bin", 110, 21)

	sbLen := e.em.ScrollbackLen()
	if sbLen == 0 {
		t.Skip("fixture produced no scrollback; nothing to assert")
	}

	snap := string(e.RenderSnapshot())
	// The snapshot must contain more CRLF-separated rows than the screen height
	// (i.e. scrollback rows were prepended). Screen alone would be height-1
	// separators; with scrollback it's height-1 + scrollbackLen.
	crlf := strings.Count(snap, "\r\n")
	if crlf <= e.em.Height()-1 {
		t.Errorf("snapshot has %d CRLF rows, want > %d (screen height) — scrollback not included",
			crlf, e.em.Height()-1)
	}
	// Spot-check: an early scrollback line's text appears in the snapshot.
	// The captured prompt echoes "output the numbers"; assert it survived.
	if !strings.Contains(snap, "output the numbers") {
		t.Errorf("snapshot missing an expected early scrollback line")
	}
	t.Logf("scrollbackLen=%d, snapshot CRLF rows=%d, height=%d", sbLen, crlf, e.em.Height())
}
