package ptyhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
)

// Launch method identifiers, reported in [LaunchResult.Method] for logging.
const (
	MethodSystemdRun = "systemd-run"
	MethodSetsid     = "setsid"
)

// LaunchConfig describes a ptyhost spawn request. Args is the flag/argv tail
// of the `palmux ptyhost ...` invocation (everything after "ptyhost") —
// already fully built by the caller (socket/status paths, env, cwd, and the
// opaque child argv after a "--"). The launcher does not interpret Args; it
// only prepends PalmuxBin + "ptyhost".
type LaunchConfig struct {
	// PalmuxBin is the path to the palmux binary to re-invoke as `<PalmuxBin>
	// ptyhost ...`. Required.
	PalmuxBin string
	// InstancePrefix isolates concurrent palmux instances (host vs
	// INSTANCE=dev rigs) so their scope units / launches never collide. See
	// domain.PalmuxSessionPrefix for the equivalent tmux-side concept.
	// Empty defaults to "palmux".
	InstancePrefix string
	// Seed is hashed into the scope unit name for readability/stability
	// (e.g. repoId__branchId__tabId). Required for a meaningful unit name,
	// but an empty seed still produces a valid (if less legible) name.
	Seed string
	// Args is the argv tail passed to `palmux ptyhost`. Required (non-empty).
	Args []string
}

// LaunchResult reports which spawn path succeeded.
type LaunchResult struct {
	Method   string // MethodSystemdRun or MethodSetsid
	UnitName string // set only for MethodSystemdRun
	Argv     []string
}

// Launcher runs the ADR-0003 spawn path: try `systemd-run --user --scope
// --collect`, and fall back to a setsid-detached process if systemd-run
// fails for any reason (not found, non-zero exit, no D-Bus user session).
// The two "Run*" fields are injectable so tests can observe the built argv
// and drive both branches without actually invoking systemd-run or forking a
// real detached process; both default to the real implementations.
type Launcher struct {
	// RunSystemdScope attempts to launch argv (argv[0] == "systemd-run", full
	// argv built by [BuildSystemdRunArgv]) and returns once the scope has
	// been handed off, or a non-nil error to trigger the setsid fallback.
	RunSystemdScope func(ctx context.Context, argv []string) error
	// RunSetsid performs the setsid-detach fallback launch of argv (argv[0]
	// == PalmuxBin, argv[1] == "ptyhost", ...).
	RunSetsid func(ctx context.Context, argv []string) error
}

// Launch runs the spawn path described on [Launcher].
func (l *Launcher) Launch(ctx context.Context, cfg LaunchConfig) (LaunchResult, error) {
	if cfg.PalmuxBin == "" {
		return LaunchResult{}, fmt.Errorf("ptyhost: launch: PalmuxBin is empty")
	}
	if len(cfg.Args) == 0 {
		return LaunchResult{}, fmt.Errorf("ptyhost: launch: Args is empty")
	}

	runSystemd := l.RunSystemdScope
	if runSystemd == nil {
		runSystemd = RunSystemdScope
	}
	runSetsid := l.RunSetsid
	if runSetsid == nil {
		runSetsid = RunSetsidFallback
	}

	unitName := ScopeUnitName(cfg.InstancePrefix, cfg.Seed)
	systemdArgv := BuildSystemdRunArgv(cfg.InstancePrefix, cfg.Seed, cfg.PalmuxBin, cfg.Args)

	if err := runSystemd(ctx, systemdArgv); err == nil {
		return LaunchResult{Method: MethodSystemdRun, UnitName: unitName, Argv: systemdArgv}, nil
	}

	plainArgv := BuildPlainArgv(cfg.PalmuxBin, cfg.Args)
	if err := runSetsid(ctx, plainArgv); err != nil {
		return LaunchResult{}, fmt.Errorf("ptyhost: launch failed via both systemd-run and setsid fallback: %w", err)
	}
	return LaunchResult{Method: MethodSetsid, Argv: plainArgv}, nil
}

// ScopeUnitName returns the transient scope unit name
// "palmux-agent-<instancePrefix>-<hash8>" for the given instancePrefix/seed.
// instancePrefix isolation guarantees INSTANCE=dev and the host instance
// never collide (AC-S3f2658-1-2).
func ScopeUnitName(instancePrefix, seed string) string {
	prefix := sanitizeInstancePrefix(instancePrefix)
	h := sha256.Sum256([]byte(seed))
	return fmt.Sprintf("palmux-agent-%s-%s", prefix, hex.EncodeToString(h[:])[:8])
}

// sanitizeInstancePrefix trims the leading/trailing underscores used by the
// tmux-prefix convention (e.g. "_palmux_" -> "palmux", "_pmx_dev_" ->
// "pmx_dev") since systemd unit name prefixes read more naturally without
// them; a genuinely empty prefix falls back to "palmux".
func sanitizeInstancePrefix(raw string) string {
	p := strings.Trim(raw, "_")
	if p == "" {
		p = "palmux"
	}
	return p
}

// BuildSystemdRunArgv builds the full argv for the systemd-run cgroup-escape
// path:
//
//	systemd-run --user --scope --collect --unit <unitName> -- <palmuxBin> ptyhost <args...>
//
// This is a pure function (no execution) so it can be unit tested directly
// (AC-S3f2658-1-2).
func BuildSystemdRunArgv(instancePrefix, seed, palmuxBin string, args []string) []string {
	unit := ScopeUnitName(instancePrefix, seed)
	argv := []string{
		"systemd-run", "--user", "--scope", "--collect", "--unit", unit, "--",
		palmuxBin, "ptyhost",
	}
	return append(argv, args...)
}

// BuildPlainArgv builds the argv for a direct (non-systemd-run) invocation:
// <palmuxBin> ptyhost <args...>. Used by the setsid fallback path.
func BuildPlainArgv(palmuxBin string, args []string) []string {
	argv := []string{palmuxBin, "ptyhost"}
	return append(argv, args...)
}

// RunSystemdScope is the real (non-test) implementation of the systemd-run
// cgroup-escape launch.
//
// IMPORTANT (corrected understanding vs. the S3f2658-1-1 spike's first
// pass): `systemd-run --user --scope` does NOT self-detach. It is a
// synchronous foreground wrapper — its own top-level process stays alive for
// the entire lifetime of the command it launches, relaying stdio and
// forwarding signals (confirmed by direct re-measurement: a plain foreground
// `systemd-run --scope -- sleep N` blocks for the full N seconds and
// forwards SIGTERM to the child). What actually escapes palmux2's cgroup is
// the process systemd-run FORKS to exec the target — systemd migrates THAT
// pid into the new scope's cgroup via the D-Bus transient-unit call, not
// systemd-run's own top-level pid.
//
// This means WE must not wait for systemd-run to return (that would block
// palmux2 for the entire agent session, defeating the purpose) — we start it
// and immediately release our handle, exactly like [RunSetsidFallback]. Once
// the fork has happened (a matter of milliseconds — the D-Bus round trip),
// the forked target (our `palmux ptyhost` process) is in its own isolated
// scope cgroup and is functionally independent of systemd-run's own process
// from that point on: if systemd-run itself is later killed by any means
// (including a cgroup-wide kill of palmux2's own unit, since systemd-run's
// own top-level pid never migrates out of the cgroup it was started in), the
// already-forked-and-migrated ptyhost process is unaffected — Linux does not
// cascade-kill children when a parent dies, and ptyhost does not depend on
// systemd-run's stdio relay for anything (it opens its own PTY device and
// socket independently). This was re-verified against the actual production
// scenario in AC-S3f2658-1-3's SURVIVAL smoke, not just this launcher's
// synchronous-wrapper behavior in isolation. See
// docs/sprint-logs/S3f2658/spike-S3f2658-1-1.json for the corrected record.
func RunSystemdScope(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("ptyhost: RunSystemdScope: empty argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:noctx // fire-and-forget on purpose; ctx would cancel the wrong lifetime (see doc comment)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	// New session: avoid any SIGHUP surprises from the caller's controlling
	// terminal while systemd-run is doing its (brief but real) synchronous
	// fork+register+wait work, independent of whether the eventual scope
	// child itself ends up isolated (that's systemd's cgroup migration, not
	// this).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	ensureUserBusEnv(cmd)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ptyhost: systemd-run scope start failed: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("ptyhost: systemd-run scope release failed: %w", err)
	}
	return nil
}

// RunSetsidFallback is the real (non-test) implementation of the setsid-
// detach fallback launch, used when systemd-run is unavailable (no D-Bus
// user session / non-systemd host, e.g. `make serve` dev rigs). It starts
// argv as a new session leader (equivalent to the classic setsid(1) +
// double-fork daemonizing idiom, without needing the external setsid binary)
// and immediately releases palmux2's process handle — ptyhost is
// self-reporting (socket + status file) and is never wait()'d by its
// launcher (ADR-0002: thin holder, palmux2-side rediscovery, not a
// parent/child wait relationship).
//
// As confirmed by the S3f2658-1-1 spike, this genuinely detaches (new
// session, reparented to init) but — unlike the systemd-run path — does NOT
// escape an existing systemd cgroup; its restart-survival guarantee is
// therefore scoped to non-systemd deployments, matching ADR-0003.
func RunSetsidFallback(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("ptyhost: RunSetsidFallback: empty argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:noctx // detached on purpose; ctx would cancel the wrong lifetime
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ptyhost: setsid fallback start: %w", err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("ptyhost: setsid fallback release: %w", err)
	}
	return nil
}
