package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/tjst-t/palmux2/internal/selfupdate"
	"github.com/tjst-t/palmux2/internal/store"
)

// newSelfUpdateService constructs the self-update service. When st != nil it
// broadcasts an app.updateAvailable WS event on a state transition (server use);
// when st == nil it is publish-less (the `palmux update` CLI, which has no WS
// hub). The installed-version probes reuse the existing resolveVersion()
// (binary) and installedImageVersion() (palmux-ws image). Single constructor so
// the CLI and GUI never drift in which components they check (decisions PD-5).
// (S6ab0ed)
func newSelfUpdateService(st *store.Store, configDir string) (*selfupdate.Service, error) {
	m, err := selfupdate.LoadManifest()
	if err != nil {
		return nil, err
	}
	probes := selfupdate.InstalledProbes{
		BinVersion:   func() string { return binVersionProbe(configDir) },
		ImageVersion: installedImageVersion,
	}
	var publish selfupdate.Publisher
	if st != nil {
		publish = func(snap selfupdate.Snapshot) {
			st.Hub().Publish(store.Event{Type: store.EventAppUpdateAvailable, Payload: snap})
		}
	}
	svc := selfupdate.NewService(m, probes, publish, slog.Default())
	// Wire the env-gated force-update test affordance. resolveVersion() is the
	// REAL (suffix-free) version; the force overlay appends its own "+force.N".
	svc.EnableForce(configDir, resolveVersion)
	return svc, nil
}

// binVersionProbe returns the running binary's version, with a test-only
// override. PALMUX_SELFUPDATE_FAKE_INSTALLED (E2E rig only) forces the reported
// installed version so a real GitHub poll against tjst-t/palmux2 yields an
// "update available" deterministically on a dev/dirty build (whose real version
// would conservatively never show an update). This overrides ONLY the detection
// INPUT — the GitHub poll itself stays real (Rule 7: production mode). (S6ab0ed)
//
// In force-update test mode it also appends the synthetic "+force.N" suffix
// (ForceVersionSuffix → "" unless armed-and-applied) so the probed installed
// version matches what /health reports — keeping the badge's "installed → latest"
// math and the badge-clearing-after-apply consistent.
func binVersionProbe(configDir string) string {
	base := os.Getenv("PALMUX_SELFUPDATE_FAKE_INSTALLED")
	if base == "" {
		base = resolveVersion()
	}
	return base + selfupdate.ForceVersionSuffix(configDir)
}

// runUpdate implements `palmux update [--check]`.
//
//	palmux update          — run the one-click update (same path as GUI "Update
//	                          all"): ~/update-palmux2.sh foreground, exit code =
//	                          success/fail. Nix-unmanaged → guidance + exit 1.
//	palmux update --check   — detection only: print per-component current→latest,
//	                          exit 0 (exit 2 if an update is available, for scripts).
//
// Shares internal/selfupdate with the GUI (decisions PD-5). Returns an exit code.
func runUpdate(args []string) int {
	check := false
	forceArm, forceDisarm, forceStatus := false, false, false
	configDir := defaultConfigDir()
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "--check":
			check = true
		case "--force-arm":
			forceArm = true
		case "--force-disarm":
			forceDisarm = true
		case "--force-status":
			forceStatus = true
		case "--config-dir":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "--config-dir requires a value")
				return 1
			}
			i++
			configDir = args[i]
		case "-h", "--help":
			fmt.Println("Usage: palmux update [--check | --force-arm | --force-disarm | --force-status] [--config-dir DIR]")
			fmt.Println("  (no flags)      run the one-click update (flake re-pin → home-manager switch → restart)")
			fmt.Println("  --check         detection only: print per-component current→latest")
			fmt.Println()
			fmt.Println("  Force-update test affordance (requires env PALMUX_SELFUPDATE_FORCE=1):")
			fmt.Println("  --force-arm     arm a synthetic 'update available' at the SAME real version so")
			fmt.Println("                  the full GUI update flow (badge → Update → real switch →")
			fmt.Println("                  reconnect → 更新しました → badge clears) can be verified without")
			fmt.Println("                  a real newer release. Then click 'すべてまとめて更新' in the GUI.")
			fmt.Println("  --force-disarm  cancel an armed forced update")
			fmt.Println("  --force-status  print the force-update counter / armed state")
			fmt.Println("  --config-dir    config dir holding the force counter (default: the server's)")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "Unknown flag %q. Usage: palmux update [--check | --force-arm | --force-disarm | --force-status]\n", a)
			return 1
		}
	}

	if forceArm || forceDisarm || forceStatus {
		return runForceCmd(configDir, forceArm, forceDisarm)
	}

	svc, err := newSelfUpdateService(nil, configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		return 1
	}
	ctx := context.Background()

	if check {
		return runUpdateCheck(ctx, svc)
	}
	return runUpdateExec(ctx, svc)
}

// runForceCmd handles `palmux update --force-arm|--force-disarm|--force-status`.
// All require PALMUX_SELFUPDATE_FORCE to be set (else the mechanism is inert and
// we say so loudly, so the operator isn't left wondering why the badge never
// appears).
func runForceCmd(configDir string, arm, disarm bool) int {
	if !selfupdate.ForceEnabled() {
		fmt.Fprintf(os.Stderr,
			"force-update mode is OFF. Set PALMUX_SELFUPDATE_FORCE=1 on the palmux2 process\n"+
				"(and on this CLI) first. This is a deliberate test affordance; it is inert in production.\n")
		return 1
	}
	switch {
	case arm:
		target, err := selfupdate.ArmForce(configDir, resolveVersion())
		if err != nil {
			fmt.Fprintf(os.Stderr, "arm failed: %v\n", err)
			return 1
		}
		fmt.Printf("armed forced update: %s → %s\n", resolveVersion()+selfupdate.ForceVersionSuffix(configDir), target)
		fmt.Println("Now open the GUI: the '更新あり' badge should appear. Click 'すべてまとめて更新'.")
		fmt.Println("The REAL update machinery runs and palmux2 restarts; the page reconnects and shows '更新しました'.")
		return 0
	case disarm:
		if err := selfupdate.DisarmForce(configDir); err != nil {
			fmt.Fprintf(os.Stderr, "disarm failed: %v\n", err)
			return 1
		}
		fmt.Println("disarmed forced update.")
		return 0
	default: // status
		fmt.Printf("force-update: enabled=true armed=%v version=%s\n",
			selfupdate.ForceArmed(configDir), resolveVersion()+selfupdate.ForceVersionSuffix(configDir))
		return 0
	}
}

func runUpdateCheck(ctx context.Context, svc *selfupdate.Service) int {
	snap := svc.Refresh(ctx)
	fmt.Println("管理対象コンポーネント:")
	for _, c := range snap.Components {
		installed := c.Installed
		if installed == "" {
			installed = "(未インストール/不明)"
		}
		latest := c.Latest
		if latest == "" {
			latest = "(取得不可)"
		}
		marker := "  "
		suffix := ""
		switch {
		case c.Available:
			marker = "↑ "
			suffix = "  [更新あり]"
		case !c.Fetchable:
			// Sa8e7d0-2-2: source has no resolvable latest (e.g. gwq has no
			// releases). Not an update candidate — label it 取得不可, never 更新あり.
			suffix = "  [取得不可]"
		}
		fmt.Printf("  %s%-16s %s → %s%s\n", marker, c.Display, installed, latest, suffix)
	}
	if snap.Degraded {
		fmt.Println("\n注意: 一部コンポーネントの latest を GitHub から取得できませんでした (レート制限/到達不可)。GITHUB_TOKEN を設定すると緩和されます。")
	}
	if !snap.NixManaged {
		fmt.Println("\nこのインストール形態は手動更新です (Nix 管理外)。`palmux update` での一括更新はできません。")
	}
	if snap.Available {
		fmt.Println("\n更新があります。`palmux update` で一括更新できます。")
		return 2 // distinct exit code so scripts can detect "update available"
	}
	// If NO component could resolve a latest tag, we couldn't actually check —
	// don't report "up to date" (exit 0) and mask the failure from scripts.
	if allLatestUnresolved(snap) {
		fmt.Println("\n最新バージョンを確認できませんでした (GitHub 到達不可/レート制限)。")
		return 3
	}
	fmt.Println("\nすべて最新です。")
	return 0
}

// allLatestUnresolved reports whether no component resolved a latest tag (a
// total GitHub-unreachable cycle), so `--check` can return a distinct exit code.
func allLatestUnresolved(snap selfupdate.Snapshot) bool {
	for _, c := range snap.Components {
		if c.Latest != "" {
			return false
		}
	}
	return true
}

func runUpdateExec(ctx context.Context, svc *selfupdate.Service) int {
	if !svc.NixManaged() {
		// AC-S6ab0ed-2-5: Nix-unmanaged → guidance + non-zero exit.
		fmt.Fprintln(os.Stderr, selfupdate.ErrNotNixManaged.Error())
		fmt.Fprintln(os.Stderr, "手動で更新するには、インストール時の install.sh を再実行してください。")
		return 1
	}
	fmt.Println("更新を実行します (~/update-palmux2.sh: flake 再pin → home-manager switch → 再起動)...")
	// CLI runs the helper in the FOREGROUND and waits — unlike the server, the
	// CLI does not restart itself, so it can report success/fail by exit code
	// (decisions PD-7).
	err := svc.RunUpdateForeground(ctx, os.Stdout, os.Stderr)
	if err != nil {
		if errors.Is(err, selfupdate.ErrNotNixManaged) {
			fmt.Fprintln(os.Stderr, err.Error())
			return 1
		}
		fmt.Fprintf(os.Stderr, "更新に失敗しました: %v\n", err)
		fmt.Fprintln(os.Stderr, "home-manager 世代でロールバックされます (旧バージョン維持)。")
		return 1
	}
	fmt.Println("更新が完了しました。")
	return 0
}
