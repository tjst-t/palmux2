package selfupdate

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PollInterval is the long version-check cadence. 6h keeps GitHub API usage
// trivial and, with GITHUB_TOKEN, far under the rate limit (decisions PD-2).
const PollInterval = 6 * time.Hour

// Publisher is called with a fresh snapshot whenever the update-available state
// transitions. Wired to store.Hub().Publish in main.go (decisions PD-2).
type Publisher func(snap Snapshot)

// Service owns the cached detection snapshot, the background poll loop, and the
// update execution path. It is the single backend object both the HTTP
// handlers and the `palmux update` CLI build on (decisions PD-5).
type Service struct {
	manifest Manifest
	probes   InstalledProbes
	publish  Publisher
	logger   *slog.Logger

	mu       sync.RWMutex
	snapshot Snapshot
	running  bool // an Update all execution is in flight (best-effort, single host)
}

// NewService builds a Service. publish may be nil (no WS broadcast, e.g. CLI).
func NewService(m Manifest, probes InstalledProbes, publish Publisher, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{manifest: m, probes: probes, publish: publish, logger: logger}
}

// Current returns the last cached snapshot (zero-value until the first poll).
func (s *Service) Current() Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot
}

// Refresh runs a synchronous detection cycle, updates the cache, and publishes
// on a state transition. On a fully-degraded cycle (every component failed to
// reach GitHub) it keeps the prior Components rather than blanking the badge
// (decisions PD-3).
func (s *Service) Refresh(ctx context.Context) Snapshot {
	snap := Detect(ctx, s.manifest, s.probes, s.nixManaged())

	s.mu.Lock()
	prev := s.snapshot
	// Graceful degrade: if GitHub was unreachable for every component this cycle
	// and we already have a prior good snapshot, keep the prior latest/available
	// state (only update CheckedAt/Degraded) so the badge doesn't flap.
	if snap.Degraded && fullyDegraded(snap) && len(prev.Components) > 0 {
		kept := prev
		kept.CheckedAt = snap.CheckedAt
		kept.Degraded = true
		kept.NixManaged = snap.NixManaged
		s.snapshot = kept
		s.mu.Unlock()
		s.logger.Warn("selfupdate: GitHub unreachable this cycle; keeping prior state")
		return kept
	}
	s.snapshot = snap
	changed := availabilityChanged(prev, snap)
	s.mu.Unlock()

	if changed && s.publish != nil {
		s.publish(snap)
	}
	return snap
}

// fullyDegraded reports whether no component resolved a latest tag.
func fullyDegraded(snap Snapshot) bool {
	for _, c := range snap.Components {
		if c.Latest != "" {
			return false
		}
	}
	return true
}

// availabilityChanged reports whether the overall or any per-component
// update-available flag changed (transition-only publish, like setDriftCached).
func availabilityChanged(prev, cur Snapshot) bool {
	if prev.Available != cur.Available {
		return true
	}
	pm := map[string]bool{}
	for _, c := range prev.Components {
		pm[c.Name] = c.Available
	}
	for _, c := range cur.Components {
		if pm[c.Name] != c.Available {
			return true
		}
		delete(pm, c.Name)
	}
	return len(pm) > 0
}

// Run starts the poll loop: an immediate detection, then every PollInterval
// until ctx is cancelled. Mirrors store.runPortScan's ticker shape.
func (s *Service) Run(ctx context.Context) {
	s.Refresh(ctx)
	ticker := time.NewTicker(PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Refresh(ctx)
		}
	}
}

// updateScriptPath returns the path to the install.sh-generated update helper.
func updateScriptPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, "update-palmux2.sh")
}

// nixManaged reports whether one-click update is possible: the
// ~/update-palmux2.sh helper exists and is executable. Manual-override installs
// (Nix-unmanaged) lack it (decisions PD-4).
func (s *Service) nixManaged() bool {
	p := updateScriptPath()
	if p == "" {
		return false
	}
	fi, err := os.Stat(p)
	if err != nil || fi.IsDir() {
		return false
	}
	return fi.Mode().Perm()&0o100 != 0
}

// NixManaged is the exported probe (CLI uses it to decide whether to attempt).
func (s *Service) NixManaged() bool { return s.nixManaged() }

// ErrNotNixManaged is returned by RunUpdate / RunUpdateForeground when the
// install is not Nix-managed (no ~/update-palmux2.sh).
var ErrNotNixManaged = errNotNixManaged{}

type errNotNixManaged struct{}

func (errNotNixManaged) Error() string {
	return "このインストール形態は手動更新です (Nix 管理外: ~/update-palmux2.sh が見つかりません)"
}

// ErrUpdateInFlight is returned by RunUpdate when an update is already running.
var ErrUpdateInFlight = errUpdateInFlight{}

type errUpdateInFlight struct{}

func (errUpdateInFlight) Error() string {
	return "更新はすでに実行中です"
}

// updateUnitName is the dedicated systemd user unit that holds the update logic
// (Sa8e7d0-1). It runs in its OWN cgroup, so it survives palmux2.service being
// stopped/restarted by the very `home-manager switch` it triggers — the
// 2026-06-20 incident root cause (the in-process/detached helper was killed
// with palmux2's cgroup). The unit is installed by the nix home-manager module.
const updateUnitName = "palmux-update.service"

// systemctlUserBin is the systemctl binary used to drive the update unit; a var
// (not a const) so tests can point it at a stub.
var systemctlUserBin = "systemctl"

// updateUnitAvailable reports whether the dedicated palmux-update unit is known
// to the user systemd manager. Returns false on an install that has not yet
// re-run install.sh / home-manager switch to gain the unit, so RunUpdate can
// fall back to the legacy detached-helper path (graceful migration). It is a var
// so tests can stub the unit-present/absent decision without a real systemctl.
var updateUnitAvailable = func() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// `systemctl --user cat <unit>` exits 0 iff the unit is loadable.
	cmd := exec.CommandContext(ctx, systemctlUserBin, "--user", "cat", updateUnitName) //nolint:gosec // fixed unit name
	cmd.Stdin = nil
	return cmd.Run() == nil
}

// RunUpdate triggers the update via the dedicated palmux-update systemd user
// unit and returns immediately. Used by the GUI "Update all" path.
//
// Sa8e7d0-1: instead of running ~/update-palmux2.sh as a (detached) child of
// palmux2 — which `home-manager switch` would kill when it stops
// palmux2.service — we `systemctl --user start palmux-update.service`. That unit
// has its own cgroup and lifecycle, so the in-flight update is NOT killed when
// palmux2 is stopped/restarted mid-switch; it completes (switch + image install
// + palmux2 restart). The GUI observes completion via the WS-drop → /health
// version reconnect handshake (unchanged: the unit still restarts palmux2 at the
// end). On installs that predate the unit (have ~/update-palmux2.sh but no
// palmux-update.service yet), fall back to the legacy detached-helper launch.
func (s *Service) RunUpdate(ctx context.Context) error {
	if !s.nixManaged() {
		return ErrNotNixManaged
	}
	// Concurrent-run guard: two overlapping runs (each doing a home-manager
	// switch + restart) would race. Reject a second trigger while one is in
	// flight.
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrUpdateInFlight
	}
	s.running = true
	s.mu.Unlock()

	clearRunning := func() {
		s.mu.Lock()
		s.running = false
		s.mu.Unlock()
	}

	if updateUnitAvailable() {
		// Independent-unit path (Sa8e7d0-1). Reset any prior failed state, then
		// `systemctl --user start --no-block`: enqueue the oneshot and return; the
		// unit runs in its own cgroup and survives the palmux2 restart the update
		// itself triggers.
		_ = exec.Command(systemctlUserBin, "--user", "reset-failed", updateUnitName).Run() //nolint:gosec,errcheck
		start := exec.Command(systemctlUserBin, "--user", "start", "--no-block", updateUnitName) //nolint:gosec // fixed unit name
		start.Stdin = nil
		if err := start.Run(); err != nil {
			clearRunning()
			return fmt.Errorf("start %s: %w", updateUnitName, err)
		}
		// The unit restarts palmux2 near the end, which usually kills us before the
		// guard matters. But `--no-block` returns 0 even if the unit later FAILS
		// without restarting us (e.g. a half-done switch that aborts) — without a
		// watcher, s.running would wedge every retry for the full fallback window.
		// So actively watch the unit and clear the guard when it ends.
		//
		// IMPORTANT: the watcher must NOT use the caller's ctx. RunUpdate is called
		// with the HTTP request context (r.Context()), which is cancelled within
		// milliseconds once this function returns and the handler responds. Using it
		// would make the watcher exit immediately and never clear the guard — the
		// exact wedge it exists to prevent. Use a fresh background context with the
		// watcher's own internal deadline as the hard backstop.
		go s.watchUpdateUnit(context.Background(), clearRunning)
		s.logger.Info("selfupdate: started independent update unit", "unit", updateUnitName)
		return nil
	}

	// ── Fallback: legacy detached-helper launch (pre-Sa8e7d0 install with the
	// helper but no palmux-update.service). Detach into a new session so the
	// switch+restart survives our exit — best-effort; the dedicated unit is the
	// robust path. ────────────────────────────────────────────────────────────
	p := updateScriptPath()
	logPath := filepath.Join(os.TempDir(), "palmux-selfupdate.log")
	logFile, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) //nolint:gosec
	if logErr != nil {
		s.logger.Warn("selfupdate: could not open update log; running without it", "path", logPath, "err", logErr)
	}
	cmd := exec.Command("bash", p) //nolint:gosec // p is a fixed ~/update-palmux2.sh path, not user input
	if logFile != nil {
		cmd.Stdout = logFile
		cmd.Stderr = logFile
	}
	detachProcess(cmd)
	if err := cmd.Start(); err != nil {
		if logFile != nil {
			_ = logFile.Close()
		}
		clearRunning()
		return err
	}
	go func() {
		_ = cmd.Process.Release()
		if logFile != nil {
			_ = logFile.Close()
		}
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Minute):
			clearRunning()
		}
	}()
	s.logger.Info("selfupdate: launched ~/update-palmux2.sh detached (legacy fallback; palmux-update unit absent)", "log", logPath)
	return nil
}

// watchUpdateUnit polls the palmux-update unit's ActiveState and clears the
// in-flight guard as soon as the unit ENDS (inactive/failed) without having
// restarted us — so a unit that fails or exits-without-restart does not wedge
// retries. If the unit restarts palmux2 first, this process is killed before the
// watcher matters (that's the normal path). ctx must be a BACKGROUND context
// (NOT the HTTP request ctx, which dies immediately after RunUpdate returns).
//
// The "inactive" race (a poll landing before the oneshot activates) is bounded
// by a short grace window: only after `grace` has elapsed do we trust an
// "inactive"/"failed" reading as "the unit genuinely ended". This caps the
// worst-case clear latency at ~grace + one poll even for a unit that runs and
// finishes entirely between two polls, instead of waiting the 10-minute backstop.
func (s *Service) watchUpdateUnit(ctx context.Context, clearRunning func()) {
	const pollEvery = 1 * time.Second
	const grace = 3 * time.Second // pre-activation window before "inactive" counts as "ended"
	started := time.Now()
	ticker := time.NewTicker(pollEvery)
	defer ticker.Stop()
	deadline := time.After(10 * time.Minute) // hard backstop
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline:
			clearRunning()
			return
		case <-ticker.C:
			switch unitActiveState() {
			case "active", "activating", "reloading", "deactivating":
				// running / transitioning — keep watching.
			case "failed":
				// A failed oneshot is unambiguous regardless of timing.
				s.logger.Warn("selfupdate: update unit failed; clearing in-flight guard", "unit", updateUnitName)
				clearRunning()
				return
			case "inactive":
				// "inactive" before the grace window may just precede activation;
				// after it, the unit has genuinely ended (clean exit without
				// restarting us, or it already ran-and-finished between polls).
				if time.Since(started) >= grace {
					clearRunning()
					return
				}
			}
		}
	}
}

// unitActiveState returns the palmux-update unit's systemd ActiveState
// (active/activating/inactive/failed/…), or "" if it can't be read.
func unitActiveState() string {
	out, err := exec.Command(systemctlUserBin, "--user", "show", "-p", "ActiveState", "--value", updateUnitName).Output() //nolint:gosec // fixed unit name
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// RunUpdateForeground runs the update SYNCHRONOUSLY and returns its exit status.
// Used by the `palmux update` CLI, which (unlike the server) does not restart
// itself — it waits for completion and reports success/fail via the exit code.
//
// Sa8e7d0-1: drives the same dedicated palmux-update unit via `systemctl --user
// start --wait`, which blocks until the oneshot finishes and propagates a unit
// failure as a non-zero exit. The CLI process is its own cgroup AND the unit is
// its own cgroup, so the palmux2 restart the update performs does not kill
// either. `systemctl --wait` itself is silent, so we follow the unit's journal
// in the background for live progress, and on failure append the journal tail so
// the user sees WHY it failed. Falls back to the legacy direct
// `bash ~/update-palmux2.sh` (which streams its output natively) when the unit is
// absent.
func (s *Service) RunUpdateForeground(ctx context.Context, stdout, stderr *os.File) error {
	if !s.nixManaged() {
		return ErrNotNixManaged
	}
	if updateUnitAvailable() {
		// Reset any prior unit state so a previous failed run doesn't block this
		// one.
		_ = exec.Command(systemctlUserBin, "--user", "reset-failed", updateUnitName).Run() //nolint:gosec,errcheck

		// Follow the unit's journal for live progress while --wait blocks silently.
		// `--since now` avoids replaying old runs; the follower is killed when the
		// run completes (its ctx is cancelled below).
		followCtx, stopFollow := context.WithCancel(ctx)
		defer stopFollow()
		follow := exec.CommandContext(followCtx, "journalctl", "--user", "-u", updateUnitName, "-f", "--since", "now", "--no-pager") //nolint:gosec // fixed unit name
		follow.Stdout = stdout
		follow.Stderr = stderr
		_ = follow.Start() // best-effort: journalctl may be absent; --wait still works

		cmd := exec.CommandContext(ctx, systemctlUserBin, "--user", "start", "--wait", updateUnitName) //nolint:gosec // fixed unit name
		runErr := cmd.Run()
		stopFollow()
		_ = follow.Wait() //nolint:errcheck // follower is killed by ctx; its error is expected

		if runErr != nil {
			// `start --wait` returns non-zero when the oneshot failed. Append a
			// journal tail so a non-following terminal still shows the cause.
			tail, _ := exec.Command("journalctl", "--user", "-u", updateUnitName, "-n", "40", "--no-pager").Output() //nolint:gosec,errcheck
			if len(tail) > 0 {
				fmt.Fprintf(stderr, "\n--- %s journal (last lines) ---\n%s\n", updateUnitName, tail)
			}
			return fmt.Errorf("update unit %s failed: %w", updateUnitName, runErr)
		}
		return nil
	}
	// Fallback: legacy direct run (streams the helper's output natively).
	p := updateScriptPath()
	cmd := exec.CommandContext(ctx, "bash", p) //nolint:gosec // fixed path
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
