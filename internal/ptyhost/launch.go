package ptyhost

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Launch method identifiers, reported in [LaunchResult.Method] for logging.
const (
	MethodSystemdRun = "systemd-run"
	MethodSetsid     = "setsid"
)

// launchProbeWindow is how long a launch waits to observe an EARLY exit of
// the process it just started, so a launch that "didn't take" (e.g.
// systemd-run failing on an unreachable D-Bus user session, exit 1) is
// detected and reported as an error — which is what lets [Launcher.Launch]
// correctly fall through to the setsid fallback (ADR-0003). A genuinely
// launched, long-lived process (systemd-run --scope stays alive for the
// child's whole session; a ptyhost runs until its child exits) does NOT exit
// within this window, so the common path just costs this one bounded wait,
// never the whole session. It is a package var so tests can shorten it.
var launchProbeWindow = 500 * time.Millisecond

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
// Mechanism (see docs/sprint-logs/S3f2658/spike-S3f2658-1-1.json):
// `systemd-run --user --scope` does NOT self-detach — it is a synchronous
// foreground wrapper whose own top-level process stays alive for the entire
// lifetime of the command it launches, relaying stdio and forwarding
// signals. What escapes palmux2's cgroup is the process systemd-run FORKS to
// exec the target: systemd migrates THAT pid into the new scope's cgroup via
// the D-Bus transient-unit call, not systemd-run's own top-level pid. Once
// that fork+migration has happened (milliseconds — the D-Bus round trip),
// the forked target (`palmux ptyhost`) is in its own isolated scope cgroup
// and is independent of systemd-run's own process — if systemd-run is later
// killed by any means (including a cgroup-wide kill of palmux2's own unit),
// the already-migrated ptyhost is unaffected (Linux does not cascade-kill
// children on parent death, and ptyhost owns its own PTY/socket, not
// systemd-run's stdio relay).
//
// Because systemd-run stays in the foreground, we must NOT block Launch()
// on its full lifetime. Instead we start it and hand off to
// [startDetachedReaping], which waits only a bounded [launchProbeWindow] to
// catch an EARLY failure exit (e.g. "Failed to connect to bus" when the
// D-Bus user session is unreachable — the exact case ADR-0003's setsid
// fallback exists for). An early non-zero exit is surfaced as an error so
// [Launcher.Launch] falls through to setsid; otherwise the launch is
// considered good and a background goroutine keeps the child reaped (no
// zombie, and no Release() — Release without Wait leaked a zombie per tab).
func RunSystemdScope(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("ptyhost: RunSystemdScope: empty argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:noctx // detached on purpose; ctx must NOT cancel the child (it outlives the launch — see doc comment)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	// New session: avoid any SIGHUP surprises from the caller's controlling
	// terminal while systemd-run does its synchronous register work.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	ensureUserBusEnv(cmd)
	_ = ctx // ctx must not bound the launched child's lifetime; see doc comment.
	return startDetachedReaping(cmd, "systemd-run scope")
}

// RunSetsidFallback is the real (non-test) implementation of the setsid-
// detach fallback launch, used when systemd-run is unavailable (no D-Bus
// user session / non-systemd host, e.g. `make serve` dev rigs). It starts
// argv as a new session leader (the classic setsid daemonizing idiom without
// the external setsid binary) and hands off to [startDetachedReaping].
//
// As confirmed by the S3f2658-1-1 spike, this genuinely detaches (new
// session, reparented away from any controlling terminal) but — unlike the
// systemd-run path — does NOT escape an existing systemd cgroup; its
// restart-survival guarantee is therefore scoped to non-systemd deployments,
// matching ADR-0003. The bounded probe also catches a launch that never took
// (e.g. a PalmuxBin that execs but exits non-zero immediately) so a bad spawn
// is reported rather than silently claimed successful.
func RunSetsidFallback(ctx context.Context, argv []string) error {
	if len(argv) == 0 {
		return fmt.Errorf("ptyhost: RunSetsidFallback: empty argv")
	}
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:noctx // detached on purpose; ctx must NOT cancel the child (it outlives the launch)
	cmd.Stdin = nil
	cmd.Stdout = nil
	cmd.Stderr = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	_ = ctx // ctx must not bound the launched child's lifetime.
	return startDetachedReaping(cmd, "setsid fallback")
}

// startDetachedReaping starts cmd, then waits a bounded [launchProbeWindow]
// to observe whether it exits early:
//
//   - Exits within the window with a non-zero/error status → the launch did
//     not take; return an error (so [Launcher.Launch] falls through to the
//     fallback). The process is already reaped by the Wait() that observed
//     the exit, so no zombie.
//   - Exits within the window cleanly (exit 0) → treated as a successful
//     hand-off (avoids a spurious double-spawn if a systemd-run variant
//     registers-then-exits-0); already reaped, so no zombie.
//   - Still running after the window → launched OK. The wait goroutine is
//     left running to Wait()-reap the process when it eventually exits.
//     This is what fixes the zombie leak: every started child is eventually
//     reaped by exactly one Wait(), and we NEVER call Release() (Release
//     without Wait was leaving a permanent zombie in palmux2's process table
//     for each launched process).
//
// The bounded window guarantees this never blocks the caller for more than
// launchProbeWindow, so it does not re-introduce the "block for the whole
// session" bug.
func startDetachedReaping(cmd *exec.Cmd, what string) error {
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("ptyhost: %s start failed: %w", what, err)
	}
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	select {
	case werr := <-waitCh:
		if werr != nil {
			return fmt.Errorf("ptyhost: %s exited during launch probe (launch did not take): %w", what, werr)
		}
		// Clean immediate exit: treat as a successful hand-off. Already
		// reaped by the Wait() above.
		return nil
	case <-time.After(launchProbeWindow):
		// Still running → launched OK. The wait goroutine above stays alive
		// and will reap the process when it eventually exits (no zombie).
		return nil
	}
}
