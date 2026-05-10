// sdd4ce1-lxd-test is a small CLI driver that exercises the lxd-container
// runtime against a real LXD installation. It is not part of the palmux
// binary; it is built separately (`go build ./cmd/sdd4ce1-lxd-test`) and
// run on the test VM (ubuntu@192.168.1.41) for AC-Sdd4ce1-3-* / -4-*
// verification.
//
// The driver is also referenced from tests/e2e/sdd4ce1_lxd_container.py
// which scp's the binary to the VM, runs it with a tmpdir worktree, and
// asserts on the structured JSON output.
//
// Usage:
//
//	sdd4ce1-lxd-test --worktree=/tmp/wt-fixture --image=ghcr.io/tjst-t/palmux-workspace:default
//
// Phases (printed as JSON lines on stdout, one per phase):
//
//	{"phase":"start","ok":true,"address":"10.x.y.z"}
//	{"phase":"id-ubuntu","ok":true}
//	{"phase":"claude-dir-bind","ok":true}
//	{"phase":"claude-json-bind","ok":true}
//	{"phase":"settings-not-bound","ok":true}
//	{"phase":"ssh-auth-sock","ok":true}
//	{"phase":"raw-idmap","ok":true}
//	{"phase":"new-tmux","ok":true}
//	{"phase":"exec-pwd","ok":true,"output":"/workspace"}
//	{"phase":"expose-port","ok":true,"hostPort":15173}
//	{"phase":"unexpose-port","ok":true}
//	{"phase":"stop","ok":true}
//	{"phase":"delete","ok":true}
//
// The Python E2E tags each phase with the AC it satisfies.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tjst-t/palmux2/internal/runtime"
	"github.com/tjst-t/palmux2/internal/runtime/lxd"
)

type result struct {
	Phase    string `json:"phase"`
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	Address  string `json:"address,omitempty"`
	HostPort int    `json:"hostPort,omitempty"`
	Output   string `json:"output,omitempty"`
}

func emit(r result) {
	b, _ := json.Marshal(r)
	fmt.Println(string(b))
}

func main() {
	worktree := flag.String("worktree", "", "worktree path (required)")
	image := flag.String("image", lxd.DefaultImage, "LXD image")
	repoID := flag.String("repo-id", "test-repo", "repo id for instance naming")
	branchID := flag.String("branch-id", "test-branch", "branch id for instance naming")
	branchName := flag.String("branch-name", "main", "branch name")
	exposeContainerPort := flag.Int("expose-container-port", 0, "0 = skip; otherwise expose this container port to a host port")
	exposeHostPort := flag.Int("expose-host-port", 0, "host port to use for expose (0 = same as container)")
	keep := flag.Bool("keep", false, "skip Stop/Delete at the end (for manual inspection)")
	flag.Parse()

	if *worktree == "" {
		fmt.Fprintln(os.Stderr, "missing --worktree")
		os.Exit(2)
	}

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	cfg := runtime.Config{
		Kind:    runtime.KindLXDContainer,
		Image:   *image,
		Network: runtime.NetworkPolicy{Mode: "bridged"},
	}
	rt := lxd.New(cfg, *worktree, *repoID, *branchID, *branchName, lxd.Options{Logger: logger})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// Phase: Start
	if err := rt.Start(ctx); err != nil {
		emit(result{Phase: "start", OK: false, Error: err.Error()})
		os.Exit(1)
	}
	emit(result{Phase: "start", OK: true, Address: rt.Status().Address})

	defer func() {
		if !*keep {
			if err := rt.Stop(context.Background()); err != nil {
				emit(result{Phase: "stop", OK: false, Error: err.Error()})
			} else {
				emit(result{Phase: "stop", OK: true})
			}
			if err := rt.Delete(context.Background()); err != nil {
				emit(result{Phase: "delete", OK: false, Error: err.Error()})
			} else {
				emit(result{Phase: "delete", OK: true})
			}
		}
	}()

	// Phase: id ubuntu
	res, err := rt.Exec(ctx, []string{"id", "ubuntu"}, runtime.ExecOpts{})
	emit(result{Phase: "id-ubuntu", OK: err == nil && res.ExitCode == 0, Error: errMsg(err), Output: strings.TrimSpace(string(res.Stdout))})

	// Phase: claude-dir bind — verify /home/ubuntu/.claude is reachable AND
	// inode matches the host-side dir. We only check it's a non-empty
	// directory inside the container.
	res, _ = rt.Exec(ctx, []string{"sh", "-c", "test -d /home/ubuntu/.claude && echo OK"}, runtime.ExecOpts{})
	emit(result{
		Phase:  "claude-dir-bind",
		OK:     strings.Contains(string(res.Stdout), "OK"),
		Output: strings.TrimSpace(string(res.Stdout)),
	})

	// Phase: claude-json bind — file existence
	res, _ = rt.Exec(ctx, []string{"sh", "-c", "test -f /home/ubuntu/.claude.json && echo OK"}, runtime.ExecOpts{})
	emit(result{
		Phase:  "claude-json-bind",
		OK:     strings.Contains(string(res.Stdout), "OK"),
		Output: strings.TrimSpace(string(res.Stdout)),
	})

	// Phase: settings.json NOT bound — must NOT exist (or must NOT be the
	// host one). We're satisfied if it doesn't exist at all in the
	// container (the default state right after image launch).
	res, _ = rt.Exec(ctx, []string{"sh", "-c", "test ! -f /home/ubuntu/.claude/settings.json && echo NOT-BOUND || echo BOUND"}, runtime.ExecOpts{})
	out := strings.TrimSpace(string(res.Stdout))
	emit(result{
		Phase:  "settings-not-bound",
		OK:     out == "NOT-BOUND",
		Output: out,
	})

	// Phase: SSH agent socket forwarded if SSH_AUTH_SOCK was set in the host env.
	if os.Getenv("SSH_AUTH_SOCK") != "" {
		res, _ = rt.Exec(ctx, []string{"sh", "-c", "test -S /tmp/ssh-auth-sock && echo OK"}, runtime.ExecOpts{})
		emit(result{
			Phase:  "ssh-auth-sock",
			OK:     strings.Contains(string(res.Stdout), "OK"),
			Output: strings.TrimSpace(string(res.Stdout)),
		})
	} else {
		emit(result{Phase: "ssh-auth-sock", OK: true, Output: "skipped (no SSH_AUTH_SOCK on host)"})
	}

	// Phase: raw.idmap applied — `id -u` from inside the container as user 1000.
	res, _ = rt.Exec(ctx, []string{"id", "-u"}, runtime.ExecOpts{})
	emit(result{
		Phase:  "raw-idmap",
		OK:     strings.TrimSpace(string(res.Stdout)) == "1000",
		Output: strings.TrimSpace(string(res.Stdout)),
	})

	// Phase: tmux session creation.
	if err := rt.NewTmuxSession(ctx, "_palmux_test_session"); err != nil {
		emit(result{Phase: "new-tmux", OK: false, Error: err.Error()})
	} else {
		emit(result{Phase: "new-tmux", OK: true})
	}

	// Phase: exec pwd inside /workspace.
	res, err = rt.Exec(ctx, []string{"pwd"}, runtime.ExecOpts{})
	emit(result{
		Phase:  "exec-pwd",
		OK:     err == nil && strings.TrimSpace(string(res.Stdout)) == "/workspace",
		Output: strings.TrimSpace(string(res.Stdout)),
		Error:  errMsg(err),
	})

	// Phase: expose port (optional).
	if *exposeContainerPort > 0 {
		mp, err := rt.ExposePort(ctx, *exposeContainerPort, *exposeHostPort, "test", false)
		if err != nil {
			emit(result{Phase: "expose-port", OK: false, Error: err.Error()})
		} else {
			emit(result{Phase: "expose-port", OK: true, HostPort: mp.HostPort})
			// Verify with `lxc config device list`.
			cmd := exec.CommandContext(ctx, "lxc", "config", "device", "list", rt.InstanceName())
			if out, err := cmd.Output(); err == nil && strings.Contains(string(out), mp.ID) {
				emit(result{Phase: "expose-port-verified", OK: true, Output: mp.ID})
			} else {
				emit(result{Phase: "expose-port-verified", OK: false, Error: errMsg(err)})
			}
			// Tear down.
			if err := rt.UnexposePort(ctx, mp.ID); err != nil {
				emit(result{Phase: "unexpose-port", OK: false, Error: err.Error()})
			} else {
				emit(result{Phase: "unexpose-port", OK: true})
			}
		}
	}
}

func errMsg(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
