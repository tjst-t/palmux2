package main

import "testing"

// [AC-Sa8e7d0-2-1] The completion guard fails ONLY when the installed image is
// provably OLDER than expected (a half-done update where the image did not
// advance). It must NOT reject a current image just because the baked version
// is git-describe-shaped or the expected tag is a non-version label like
// "workspace-image" — doing so would re-create the very failure loop the Sprint
// fixes.
func TestImageIsStrictlyOlder(t *testing.T) {
	cases := []struct {
		installed, expected string
		want                bool // true = strictly older = guard should FAIL the install
	}{
		// The incident: image stuck at an older version → guard must catch it.
		{"v0.11.1", "v0.11.3", true},
		{"0.11.1", "v0.11.3", true}, // leading-v tolerance
		// Current image at expected → not older → pass.
		{"v0.11.3", "v0.11.3", false},
		// Image AHEAD of expected (e.g. a git-describe build vX.Y.Z-N) → not older.
		{"v0.11.4", "v0.11.3", false},
		// git-describe baked version that is current → must NOT be flagged older.
		// (parseVersion strips the -N-gSHA suffix, so v0.11.3-3-gabc == v0.11.3.)
		{"v0.11.3-3-gabc1234", "v0.11.3", false},
		// Non-version expected tag (workspace-image) → cannot prove older → pass.
		{"v0.11.3", "workspace-image", false},
		// Empty / unknown installed → never a provable regression (do not fail).
		{"", "v0.11.3", false},
		{"  ", "v0.11.3", false},
		// Empty expected → nothing to compare → pass.
		{"v0.11.3", "", false},
	}
	for _, c := range cases {
		if got := imageIsStrictlyOlder(c.installed, c.expected); got != c.want {
			t.Errorf("imageIsStrictlyOlder(%q,%q)=%v want %v", c.installed, c.expected, got, c.want)
		}
	}
}
