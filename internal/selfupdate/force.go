package selfupdate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Force-update test affordance (committed, env-gated).
//
// The full "old→new" GUI self-update flow — badge "更新あり" → "すべてまとめて更新"
// → real ~/update-palmux2.sh (flake re-pin + home-manager switch) via the
// independent palmux-update.service → palmux2 restart → WS-drop → /health
// version reconnect handshake → "更新しました" toast → badge clears — can only be
// exercised end-to-end when a GitHub release is STRICTLY NEWER than what is
// installed. On a host already at latest there is no such release, so this whole
// chain (the riskiest, least-integration-tested part) stayed manual-smoke-only,
// gated on "wait for the next release" (S6ab0ed MS-1 / Sa8e7d0 backlog).
//
// This force mode closes that gap WITHOUT a real release. When the operator arms
// it, detection synthesizes an "update available" at the SAME real version, and
// the run drives the REAL machinery; a persisted, self-advancing synthetic
// version suffix (+force.N) provides the version DELTA the reconnect handshake
// needs, so the full happy path (incl. the success toast and badge clearing) is
// reachable at one fixed release.
//
// Safety: the entire mechanism is inert unless PALMUX_SELFUPDATE_FORCE is set
// truthy. In production (env unset) Enabled() is false and every entry point
// below short-circuits — the state file is never read or written, the version
// suffix is always "", and detection is never overlaid. It is a deliberate,
// opt-in test affordance, not a runtime feature.

// forceEnvVar gates the entire force mechanism. Unset/empty/falsey → inert.
const forceEnvVar = "PALMUX_SELFUPDATE_FORCE"

// forceStateFile is the per-host persisted counter, under the config dir so it
// survives the palmux2 restart the forced update performs (the suffix MUST be
// stable across the restart for the handshake to read the advanced value).
const forceStateFile = "selfupdate-force.json"

// forceState is the persisted synthetic-version counter.
//
//	Current — the applied counter. 0 = no synthetic suffix yet. The /health
//	          version and the installed-version probe append "+force.<Current>"
//	          when Current > 0.
//	Armed   — an update is armed (target = Current+1). While armed, detection
//	          synthesizes "update available". A forced run APPLIES (Current++)
//	          and disarms, so the badge clears after exactly one cycle.
type forceState struct {
	Current int  `json:"current"`
	Armed   bool `json:"armed"`
}

// ForceEnabled reports whether the force mechanism is switched on for this
// process (PALMUX_SELFUPDATE_FORCE truthy). Everything else no-ops when false.
func ForceEnabled() bool {
	v := strings.TrimSpace(strings.ToLower(os.Getenv(forceEnvVar)))
	switch v {
	case "", "0", "false", "no", "off":
		return false
	default:
		return true
	}
}

func forceStatePath(dir string) string {
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, forceStateFile)
}

// loadForceState reads the counter file. A missing/garbled file is the zero
// state (Current=0, Armed=false), never an error — this is a best-effort test
// affordance.
func loadForceState(dir string) forceState {
	p := forceStatePath(dir)
	if p == "" {
		return forceState{}
	}
	b, err := os.ReadFile(p) //nolint:gosec // fixed filename under the trusted config dir
	if err != nil {
		return forceState{}
	}
	var st forceState
	if json.Unmarshal(b, &st) != nil {
		return forceState{}
	}
	return st
}

func saveForceState(dir string, st forceState) error {
	p := forceStatePath(dir)
	if p == "" {
		return nil
	}
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o644) //nolint:gosec // non-secret test counter
}

// ForceVersionSuffix returns the synthetic version suffix to append to the real
// version for BOTH /health and the installed-version probe, e.g. "+force.2".
// Empty when force mode is off or no forced update has been applied yet. Keeping
// the real version as the prefix (e.g. "v0.12.3+force.2") stays honest — the
// operator still sees the genuine release.
func ForceVersionSuffix(dir string) string {
	if !ForceEnabled() {
		return ""
	}
	st := loadForceState(dir)
	if st.Current <= 0 {
		return ""
	}
	return "+force." + strconv.Itoa(st.Current)
}

// ForceArmed reports whether a forced update is currently armed.
func ForceArmed(dir string) bool {
	if !ForceEnabled() {
		return false
	}
	return loadForceState(dir).Armed
}

// ArmForce arms a forced update (target = Current+1) so the next detection
// synthesizes "update available" and the badge lights at the same real version.
// Returns the human-readable target version label (real base + the armed
// suffix) for CLI/echo. Errors if force mode is off (caller misuse).
func ArmForce(dir, base string) (string, error) {
	if !ForceEnabled() {
		return "", errForceDisabled
	}
	st := loadForceState(dir)
	st.Armed = true
	if err := saveForceState(dir, st); err != nil {
		return "", err
	}
	return base + "+force." + strconv.Itoa(st.Current+1), nil
}

// DisarmForce clears an armed forced update without applying it (operator
// cancel). No-op when not armed.
func DisarmForce(dir string) error {
	if !ForceEnabled() {
		return errForceDisabled
	}
	st := loadForceState(dir)
	st.Armed = false
	return saveForceState(dir, st)
}

// applyForce advances the counter (Current++) and disarms, returning true iff it
// was armed (i.e. a forced run should proceed with the synthetic bump). Called by
// the forced RunUpdate path BEFORE the restart, so the post-restart /health
// reports the advanced suffix and the reconnect handshake sees a version change.
func applyForce(dir string) bool {
	if !ForceEnabled() {
		return false
	}
	st := loadForceState(dir)
	if !st.Armed {
		return false
	}
	st.Current++
	st.Armed = false
	_ = saveForceState(dir, st) //nolint:errcheck // best-effort; a save failure just means the suffix stays — handshake then times out to "failed", which is a visible, non-silent outcome
	return true
}

// overlayForce mutates a freshly-Detect'd snapshot to reflect an armed forced
// update: it marks the core-binary component "update available" at the synthetic
// target version. No-op unless force mode is on AND armed. base is the REAL
// version (no suffix) so the synthesized installed/latest stay consistent with
// /health.
func overlayForce(dir, base string, snap *Snapshot) {
	if !ForceEnabled() || snap == nil {
		return
	}
	st := loadForceState(dir)
	if !st.Armed {
		return
	}
	installed := base + ForceVersionSuffix(dir) // base + "+force.<Current>" (or base when Current==0)
	target := base + "+force." + strconv.Itoa(st.Current+1)
	for i := range snap.Components {
		if snap.Components[i].Kind != string(KindCoreBinary) {
			continue
		}
		snap.Components[i].Installed = installed
		snap.Components[i].Latest = target
		snap.Components[i].Fetchable = true
		// Synthetic availability: UpdateAvailable() ignores the +force.N build
		// suffix (it compares numeric cores), so it would report false here. The
		// overlay asserts availability directly — that is the whole point.
		snap.Components[i].Available = true
		snap.Available = true
	}
	snap.Forced = true
}

type forceDisabledError struct{}

func (forceDisabledError) Error() string {
	return "force-update mode is off (set " + forceEnvVar + "=1 to enable the test affordance)"
}

// errForceDisabled is returned by Arm/Disarm when force mode is not enabled.
var errForceDisabled = forceDisabledError{}
