// `palmux port` subcommand. Implements a portman-CLI-compatible interface
// (exec / alloc / list / free) so existing scripts can `s/portman/palmux
// port/` and keep working. The allocator state is in
// `~/.config/palmux/ports.json`; `--config-dir` overrides the parent dir
// (mirrors the server flag of the same name).
//
// Bootstrap rule (§6.5): this command MUST work whether or not `palmux
// serve` is running. We get there by talking directly to ports.json with
// flock — no RPC fast path today.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"

	"github.com/spf13/pflag"

	"github.com/tjst-t/palmux2/internal/port"
)

// runPortCommand is the entry point invoked by main when argv[1] == "port".
// argv is the remaining tokens *after* the leading `port`.
//
// Subcommands:
//
//	exec --name N [--scope S] [--project P] [--worktree W] -- CMD ARGS...
//	alloc --name N [--scope S] [--project P] [--worktree W]
//	list [--scope S] [--json]
//	free --name N [--scope S]
//
// The default scope is "global", matching the host palmux2 / palmux2-api /
// palmux2-frontend services in the existing Makefile.
func runPortCommand(argv []string) int {
	if len(argv) == 0 {
		printPortUsage(os.Stderr)
		return 2
	}
	sub := argv[0]
	rest := argv[1:]

	switch sub {
	case "exec":
		return runPortExec(rest)
	case "alloc", "lease":
		return runPortAlloc(rest)
	case "list", "ls":
		return runPortList(rest)
	case "free", "release":
		return runPortFree(rest)
	case "help", "-h", "--help":
		printPortUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "palmux port: unknown subcommand %q\n", sub)
		printPortUsage(os.Stderr)
		return 2
	}
}

func printPortUsage(w *os.File) {
	fmt.Fprintln(w, `palmux port — built-in port allocator (portman-compatible CLI)

Usage:
  palmux port exec  --name NAME [--scope SCOPE] -- CMD [ARGS...]
  palmux port alloc --name NAME [--scope SCOPE]
  palmux port list  [--scope SCOPE] [--json]
  palmux port free  --name NAME [--scope SCOPE]

Flags accepted by every subcommand:
  --config-dir PATH    Config dir (default ~/.config/palmux). ports.json
                       lives at <config-dir>/ports.json.

exec flags:
  --name NAME          Lease name (required).
  --scope SCOPE        Lease scope (default "global").
  --project PROJECT    Repo identifier (metadata; ports.json record only).
  --worktree NAME      Worktree name (metadata).
  --hostname HOST      Hostname for reverse proxy (metadata).
  --expose             Mark as exposed (metadata; informational).
  --keep               Do NOT free the lease when the child exits (default
                       is to keep — same as portman exec).
  --free-on-exit       Free the lease when the child exits.

In the command tail, every literal "{}" is replaced with the chosen port.
The chosen port is also exported as $PORT.

Examples:
  palmux port exec --name palmux2 -- ./bin/palmux --addr :{}
  palmux port alloc --name palmux2-api
  palmux port list --json
  palmux port free --name palmux2`)
}

// portCommonFlags parses the flags that every port subcommand shares.
// Returns a fresh allocator pointing at the right ports.json.
func portCommonFlags(fs *pflag.FlagSet) (configDir, scope *string) {
	configDir = fs.String("config-dir", "", "config dir (default ~/.config/palmux). ports.json lives at <dir>/ports.json")
	scope = fs.String("scope", port.ScopeGlobal, "lease scope (\"global\" or a workspace ID)")
	return
}

func newAllocator(configDir string) *port.Allocator {
	if configDir == "" {
		return port.New("")
	}
	return port.New(filepath.Join(configDir, "ports.json"))
}

// splitArgvAtDoubleDash divides argv into the flag portion and the command
// portion, with `--` as the delimiter. portman exec uses the same convention.
func splitArgvAtDoubleDash(argv []string) (flags, cmd []string) {
	for i, a := range argv {
		if a == "--" {
			return argv[:i], argv[i+1:]
		}
	}
	return argv, nil
}

func runPortExec(argv []string) int {
	flagArgs, cmdArgs := splitArgvAtDoubleDash(argv)

	fs := pflag.NewFlagSet("port exec", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configDir, scope := portCommonFlags(fs)
	name := fs.String("name", "", "lease name (required)")
	project := fs.String("project", "", "project (repo) identifier — metadata")
	worktree := fs.String("worktree", "", "worktree name — metadata")
	hostname := fs.String("hostname", "", "reverse-proxy hostname — metadata")
	expose := fs.Bool("expose", false, "mark exposed — metadata")
	keep := fs.Bool("keep", true, "keep the lease after the child exits (matches portman exec)")
	freeOnExit := fs.Bool("free-on-exit", false, "free the lease when the child exits (negates --keep)")
	if err := fs.Parse(flagArgs); err != nil {
		return 2
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "palmux port exec: --name is required")
		return 2
	}
	if len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "palmux port exec: missing command after `--`")
		return 2
	}
	free := !*keep
	if *freeOnExit {
		free = true
	}

	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	exitCode, err := port.ExecCommand(ctx, port.ExecOptions{
		Allocator:  newAllocator(*configDir),
		Scope:      *scope,
		Name:       *name,
		Project:    *project,
		Worktree:   *worktree,
		Hostname:   *hostname,
		Expose:     *expose,
		Argv:       cmdArgs,
		FreeOnExit: free,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "palmux port exec: %v\n", err)
		return 1
	}
	return exitCode
}

func runPortAlloc(argv []string) int {
	fs := pflag.NewFlagSet("port alloc", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configDir, scope := portCommonFlags(fs)
	name := fs.String("name", "", "lease name (required)")
	project := fs.String("project", "", "project — metadata")
	worktree := fs.String("worktree", "", "worktree name — metadata")
	hostname := fs.String("hostname", "", "reverse-proxy hostname — metadata")
	expose := fs.Bool("expose", false, "mark exposed — metadata")
	asJSON := fs.Bool("json", false, "emit the lease as JSON instead of a bare port number")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if *name == "" {
		fmt.Fprintln(os.Stderr, "palmux port alloc: --name is required")
		return 2
	}

	a := newAllocator(*configDir)
	m, err := a.Allocate(context.Background(), port.Request{
		Scope:    *scope,
		Name:     *name,
		Project:  *project,
		Worktree: *worktree,
		Hostname: *hostname,
		Expose:   *expose,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "palmux port alloc: %v\n", err)
		return 1
	}
	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(m)
	} else {
		fmt.Println(m.Port)
	}
	return 0
}

func runPortList(argv []string) int {
	fs := pflag.NewFlagSet("port list", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configDir, scope := portCommonFlags(fs)
	asJSON := fs.Bool("json", false, "emit JSON instead of a table")
	all := fs.Bool("all", false, "list every scope (overrides --scope)")
	if err := fs.Parse(argv); err != nil {
		return 2
	}

	a := newAllocator(*configDir)
	listScope := *scope
	if *all {
		listScope = ""
	}
	leases, err := a.List(context.Background(), listScope)
	if err != nil {
		fmt.Fprintf(os.Stderr, "palmux port list: %v\n", err)
		return 1
	}
	sort.Slice(leases, func(i, j int) bool {
		if leases[i].Scope != leases[j].Scope {
			return leases[i].Scope < leases[j].Scope
		}
		return leases[i].Name < leases[j].Name
	})
	if *asJSON {
		// Encode an empty array, not null, so consumers can iterate
		// unconditionally.
		if leases == nil {
			leases = []port.Mapping{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(leases)
		return 0
	}
	if err := port.PrintListTable(os.Stdout, leases); err != nil {
		fmt.Fprintf(os.Stderr, "palmux port list: %v\n", err)
		return 1
	}
	return 0
}

func runPortFree(argv []string) int {
	fs := pflag.NewFlagSet("port free", pflag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configDir, scope := portCommonFlags(fs)
	name := fs.String("name", "", "lease name (required)")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	target := *name
	// Allow positional shorthand: `palmux port free NAME` (matches
	// `portman release NAME`).
	if target == "" {
		positional := fs.Args()
		if len(positional) >= 1 {
			target = positional[0]
		}
	}
	if target == "" {
		fmt.Fprintln(os.Stderr, "palmux port free: --name (or positional NAME) is required")
		return 2
	}

	a := newAllocator(*configDir)
	if err := a.Free(context.Background(), *scope, target); err != nil {
		fmt.Fprintf(os.Stderr, "palmux port free: %v\n", err)
		return 1
	}
	return 0
}

// dispatchPortCommand is called from main() before pflag.Parse if argv[1] is
// "port". It returns the exit code to pass to os.Exit.
func dispatchPortCommand() (handled bool, exitCode int) {
	args := os.Args[1:]
	if len(args) == 0 {
		return false, 0
	}
	if args[0] != "port" {
		return false, 0
	}
	return true, runPortCommand(args[1:])
}

// silence unused warnings when nothing in this file references these helpers
// outside of debug builds.
var (
	_ = strings.HasPrefix
	_ = errors.New
)
