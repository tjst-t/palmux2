package selfupdate

import (
	"regexp"
	"strconv"
	"strings"
)

// semverRe extracts the leading X.Y.Z (or X.Y, or X) numeric core from a
// version string, ignoring a leading "v", any pre-release/build suffix
// (-rc1, -3-g0943bc8-dirty, +meta), and surrounding noise like "gwq version
// 0.8.0".
var semverRe = regexp.MustCompile(`(\d+)(?:\.(\d+))?(?:\.(\d+))?`)

// parsedVersion is a normalized numeric version core plus a "clean" flag that
// is false for dev/dirty builds (which must never be treated as "up to date"
// nor as "older" in a way that triggers spurious update prompts).
type parsedVersion struct {
	major, minor, patch int
	ok                  bool // a numeric core was found
	dirty               bool // build carried -dirty / -dev markers
}

func parseVersion(s string) parsedVersion {
	s = strings.TrimSpace(s)
	pv := parsedVersion{}
	if s == "" {
		return pv
	}
	low := strings.ToLower(s)
	if strings.Contains(low, "dirty") || strings.HasPrefix(low, "dev") || strings.Contains(low, "devel") {
		pv.dirty = true
	}
	m := semverRe.FindStringSubmatch(s)
	if m == nil {
		return pv
	}
	pv.ok = true
	pv.major = atoiSafe(m[1])
	pv.minor = atoiSafe(m[2])
	pv.patch = atoiSafe(m[3])
	return pv
}

func atoiSafe(s string) int {
	if s == "" {
		return 0
	}
	n, _ := strconv.Atoi(s)
	return n
}

// UpdateAvailable reports whether `latest` is a strictly-newer release than
// `installed`. It is conservative: if either version cannot be parsed into a
// numeric core, or the installed build is a dirty/dev build, it returns false
// so we never nag a developer build or on garbled input (decisions PD-3).
func UpdateAvailable(installed, latest string) bool {
	in := parseVersion(installed)
	lt := parseVersion(latest)
	if !in.ok || !lt.ok {
		return false
	}
	if in.dirty {
		// dev/dirty build: don't claim an update is available (it may be ahead
		// of the latest release).
		return false
	}
	return less(in, lt)
}

func less(a, b parsedVersion) bool {
	if a.major != b.major {
		return a.major < b.major
	}
	if a.minor != b.minor {
		return a.minor < b.minor
	}
	return a.patch < b.patch
}
