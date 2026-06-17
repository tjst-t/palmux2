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
func newSelfUpdateService(st *store.Store) (*selfupdate.Service, error) {
	m, err := selfupdate.LoadManifest()
	if err != nil {
		return nil, err
	}
	probes := selfupdate.InstalledProbes{
		BinVersion:   binVersionProbe,
		ImageVersion: installedImageVersion,
	}
	var publish selfupdate.Publisher
	if st != nil {
		publish = func(snap selfupdate.Snapshot) {
			st.Hub().Publish(store.Event{Type: store.EventAppUpdateAvailable, Payload: snap})
		}
	}
	return selfupdate.NewService(m, probes, publish, slog.Default()), nil
}

// binVersionProbe returns the running binary's version, with a test-only
// override. PALMUX_SELFUPDATE_FAKE_INSTALLED (E2E rig only) forces the reported
// installed version so a real GitHub poll against tjst-t/palmux2 yields an
// "update available" deterministically on a dev/dirty build (whose real version
// would conservatively never show an update). This overrides ONLY the detection
// INPUT — the GitHub poll itself stays real (Rule 7: production mode). (S6ab0ed)
func binVersionProbe() string {
	if v := os.Getenv("PALMUX_SELFUPDATE_FAKE_INSTALLED"); v != "" {
		return v
	}
	return resolveVersion()
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
	for _, a := range args {
		switch a {
		case "--check":
			check = true
		case "-h", "--help":
			fmt.Println("Usage: palmux update [--check]")
			fmt.Println("  (no flags)  run the one-click update (flake re-pin → home-manager switch → restart)")
			fmt.Println("  --check     detection only: print per-component current→latest")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "Unknown flag %q. Usage: palmux update [--check]\n", a)
			return 1
		}
	}

	svc, err := newSelfUpdateService(nil)
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
		if c.Available {
			marker = "↑ "
		}
		fmt.Printf("  %s%-16s %s → %s%s\n", marker, c.Display, installed, latest,
			map[bool]string{true: "  [更新あり]", false: ""}[c.Available])
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
