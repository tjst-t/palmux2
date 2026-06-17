package selfupdate

import (
	"context"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
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

// RunUpdate launches the update helper DETACHED and returns immediately. Used by
// the GUI "Update all" path: ~/update-palmux2.sh restarts palmux itself, so the
// HTTP handler that triggered it cannot wait synchronously — it would be killed
// mid-request. The GUI observes completion via the WS-drop → /health-version
// reconnect handshake (decisions PD-6/PD-7).
func (s *Service) RunUpdate(ctx context.Context) error {
	p := updateScriptPath()
	if !s.nixManaged() {
		return ErrNotNixManaged
	}
	// Concurrent-run guard: ~/update-palmux2.sh runs a home-manager switch +
	// service restart; two overlapping runs would race. Reject a second trigger
	// while one is in flight. (The detached child usually restarts/kills us, so
	// this guard mainly defends against a rapid double-POST before that happens,
	// or a script that exits without restarting.)
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

	// Detached: new session, output to a log file, no parent wait. The script
	// performs flake re-pin → home-manager switch → `systemctl --user restart
	// palmux2`, which terminates this process. Running it as a child of palmux
	// would kill it; we detach so the switch+restart survives our exit.
	logPath := filepath.Join(os.TempDir(), "palmux-selfupdate.log")
	logFile, logErr := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644) //nolint:gosec
	if logErr != nil {
		// Non-fatal: proceed without a log file (output goes to /dev/null), but
		// surface it so a missing diagnostic trail is explainable.
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
	// Release the child; do not Wait (it will outlive us / restart us). If the
	// script ever exits WITHOUT restarting palmux, reset the guard so a retry is
	// possible (best-effort — we can't Wait on a released process, so we clear
	// after a grace period covering a normal switch+restart).
	go func() {
		_ = cmd.Process.Release()
		if logFile != nil {
			_ = logFile.Close()
		}
		// If we're still alive after the switch+restart window, the update did
		// not restart us; clear the guard so the user can retry.
		select {
		case <-ctx.Done():
		case <-time.After(10 * time.Minute):
			clearRunning()
		}
	}()
	s.logger.Info("selfupdate: launched ~/update-palmux2.sh detached", "log", logPath)
	return nil
}

// RunUpdateForeground runs the update helper SYNCHRONOUSLY and returns its exit
// status. Used by the `palmux update` CLI, which (unlike the server) does not
// restart itself — it can wait for the helper to finish and report success/fail
// via the exit code (decisions PD-7). Output is streamed to stdout/stderr.
func (s *Service) RunUpdateForeground(ctx context.Context, stdout, stderr *os.File) error {
	p := updateScriptPath()
	if !s.nixManaged() {
		return ErrNotNixManaged
	}
	cmd := exec.CommandContext(ctx, "bash", p) //nolint:gosec // fixed path
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}
