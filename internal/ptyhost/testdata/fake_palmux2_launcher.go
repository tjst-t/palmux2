//go:build ignore

// fake_palmux2_launcher is a tiny standalone driver used ONLY by the
// AC-S3f2658-1-3 real-machine SURVIVAL smoke
// (internal/ptyhost/survival_test.go). It plays the role of "palmux2": on
// start, it calls the REAL production ptyhost.Launcher.Launch() (the exact
// code path palmux2 itself will call once wired up in Story 2/3) to spawn a
// `palmux ptyhost` holding a cheap counter script, then idles forever —
// standing in for the long-running palmux2 daemon process so the test can
// `systemctl --user restart` or `kill -9` IT specifically, never the real
// host palmux2.
//
// It deliberately imports internal/ptyhost directly (this file lives under
// the module root so that import resolves normally) rather than
// shelling out to a hand-built systemd-run command line, so this smoke
// exercises the actual Story 1 deliverable, not a reimplementation of it.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/tjst-t/palmux2/internal/ptyhost"
)

func main() {
	palmuxBin := flag.String("palmux-bin", "", "path to the real palmux binary")
	instancePrefix := flag.String("instance-prefix", "survtest", "")
	seed := flag.String("seed", "survival-seed", "")
	socket := flag.String("socket", "", "")
	status := flag.String("status", "", "")
	flag.Parse()
	childArgv := flag.Args()

	if *palmuxBin == "" || *socket == "" || *status == "" || len(childArgv) == 0 {
		fmt.Fprintln(os.Stderr, "fake_palmux2_launcher: --palmux-bin, --socket, --status and a child argv are required")
		os.Exit(2)
	}

	l := &ptyhost.Launcher{}
	args := append([]string{"--socket", *socket, "--status", *status, "--"}, childArgv...)
	result, err := l.Launch(context.Background(), ptyhost.LaunchConfig{
		PalmuxBin:      *palmuxBin,
		InstancePrefix: *instancePrefix,
		Seed:           *seed,
		Args:           args,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake_palmux2_launcher: Launch failed:", err)
		os.Exit(1)
	}
	fmt.Printf("LAUNCH_METHOD=%s\n", result.Method)
	fmt.Printf("UNIT_NAME=%s\n", result.UnitName)

	// Idle forever, standing in for palmux2's own long-running process — the
	// test kills/restarts THIS process's containing unit, never the ptyhost
	// it just launched.
	select {}
}
