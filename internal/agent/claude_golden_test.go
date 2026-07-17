package agent

import (
	"reflect"
	"testing"
)

// This file proves the Sdec0a7-1 extraction is behavior-preserving: it
// reconstructs the exact argv/env-building formula from the pre-refactor
// internal/tab/claudetui/daemon.go (git HEAD at the start of Sdec0a7-1,
// `git show <pre-refactor-rev>:internal/tab/claudetui/daemon.go`, function
// spawnWithArgs lines ~385-465) as a reference implementation, then asserts
// ClaudeAdapter.SpawnSpec produces byte-identical Argv/Env for the same
// inputs. If this test ever needs to change, the pre-refactor formula below
// must be re-derived from git history, not "fixed" to match new behavior —
// this test's entire purpose is pinning old behavior.
//
// Reference formula (transcribed from the pre-refactor spawnWithArgs, which
// took `args []string` = either d.claudeArgs (fresh) or
// append(d.claudeArgs, "--resume", sid) (respawn)):
//
//	if inContainer {
//	    claudeBin = containerClaudeBin
//	    hookBin = containerHookBinPath  // resolved by the caller before intent
//	    notifyURL = notifyURLInContainer // resolved by the caller before intent
//	    args = ["--plugin-dir", palmuxSkillDir] + args
//	}
//	hooksAvailable = hookBin != "" && notifyURL != ""
//	settings = buildClaudeSettings(hookBin, hooksAvailable)
//	args = ["--settings", settings] + args
//	if hooksAvailable {
//	    env = hookEnv(notifyURL, token, repoID, branchID, tabID)
//	}
//	if pm != "" {
//	    args = ["--permission-mode", pm] + args
//	}
//	argv = [claudeBin] + args

// referenceSpawnArgv reproduces the pre-extraction claudetui daemon's argv
// assembly verbatim (see the derivation above), given the SAME resolved
// inputs SpawnIntent now carries.
func referenceSpawnArgv(bin string, baseArgs []string, resumeSessionID string, inContainer bool, hookBin, notifyURL, token, repoID, branchID, tabID, pm string) (argv, env []string, err error) {
	args := append([]string(nil), baseArgs...)
	if resumeSessionID != "" {
		args = append(args, "--resume", resumeSessionID)
	}

	claudeBin := bin
	if inContainer {
		claudeBin = containerClaudeBin
		args = append([]string{"--plugin-dir", palmuxSkillDir}, args...)
	}

	hooksAvailable := hookBin != "" && notifyURL != ""
	settings, serr := buildClaudeSettings(hookBin, hooksAvailable)
	if serr != nil {
		return nil, nil, serr
	}
	args = append([]string{"--settings", settings}, args...)

	if hooksAvailable {
		env = hookEnv(notifyURL, token, repoID, branchID, tabID)
	}

	if pm != "" {
		args = append([]string{"--permission-mode", pm}, args...)
	}

	argv = append([]string{claudeBin}, args...)
	return argv, env, nil
}

// TestClaudeAdapterSpawnSpecMatchesPreRefactorArgv is the golden-argv gate:
// for fresh and resume intents, host and in-container, with and without
// hooks/permission-mode, the ClaudeAdapter must produce exactly the argv/env
// the pre-Sdec0a7-1 claudetui daemon would have built for the same inputs.
func TestClaudeAdapterSpawnSpecMatchesPreRefactorArgv(t *testing.T) {
	cases := []struct {
		name            string
		bin             string
		args            []string
		resumeSessionID string
		inContainer     bool
		hookBin         string
		notifyURL       string
		token           string
		repoID          string
		branchID        string
		tabID           string
		permissionMode  string
	}{
		{
			name: "fresh, host, no hooks, no permission mode",
			bin:  "claude", args: []string{"--foo"},
		},
		{
			name: "fresh, host, with hooks and token",
			bin:  "claude", args: []string{"--foo"},
			hookBin: "/usr/local/bin/palmux", notifyURL: "http://127.0.0.1:8080/api/notify",
			token: "tok", repoID: "repo1", branchID: "branch1", tabID: "claude",
		},
		{
			name:           "fresh, host, with permission mode",
			bin:            "claude",
			permissionMode: "bypassPermissions",
		},
		{
			name: "resume, host, with hooks",
			bin:  "claude", args: []string{"--foo"},
			resumeSessionID: "ses-abc123",
			hookBin:         "/usr/local/bin/palmux", notifyURL: "http://127.0.0.1:8080/api/notify",
			repoID: "repo1", branchID: "branch1", tabID: "claude",
		},
		{
			name: "fresh, in-container, with hooks",
			bin:  "/nonexistent/host/claude", args: []string{"--foo"},
			inContainer: true,
			hookBin:     "/usr/local/bin/palmux", notifyURL: "http://10.0.0.1:8080/api/notify",
			token: "tok", repoID: "repo1", branchID: "branch1", tabID: "claude",
		},
		{
			name: "resume, in-container, with hooks and permission mode",
			bin:  "/nonexistent/host/claude", args: []string{"--foo", "--bar"},
			resumeSessionID: "ses-xyz999",
			inContainer:     true,
			hookBin:         "/usr/local/bin/palmux", notifyURL: "http://10.0.0.1:8080/api/notify",
			token: "tok", repoID: "repo1", branchID: "branch1", tabID: "claude",
			permissionMode: "acceptEdits",
		},
		{
			name:        "fresh, in-container, no hook URL (bridge unknown) — hook skipped",
			bin:         "/nonexistent/host/claude",
			inContainer: true,
			hookBin:     "/usr/local/bin/palmux", notifyURL: "", // bridge URL unresolved
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantArgv, wantEnv, err := referenceSpawnArgv(
				tc.bin, tc.args, tc.resumeSessionID, tc.inContainer,
				tc.hookBin, tc.notifyURL, tc.token, tc.repoID, tc.branchID, tc.tabID,
				tc.permissionMode,
			)
			if err != nil {
				t.Fatalf("referenceSpawnArgv: %v", err)
			}

			adapter := NewClaudeAdapter(tc.bin, tc.args)
			spec, err := adapter.SpawnSpec(SpawnIntent{
				ResumeSessionID: tc.resumeSessionID,
				InContainer:     tc.inContainer,
				Hook: HookEnv{
					NotifyURL:   tc.notifyURL,
					Token:       tc.token,
					RepoID:      tc.repoID,
					BranchID:    tc.branchID,
					TabID:       tc.tabID,
					HookBinPath: tc.hookBin,
				},
				PermissionMode: tc.permissionMode,
			})
			if err != nil {
				t.Fatalf("SpawnSpec: %v", err)
			}

			if !reflect.DeepEqual(spec.Argv, wantArgv) {
				t.Errorf("Argv mismatch:\n  got:  %#v\n  want: %#v", spec.Argv, wantArgv)
			}
			if !reflect.DeepEqual(spec.Env, wantEnv) {
				t.Errorf("Env mismatch:\n  got:  %#v\n  want: %#v", spec.Env, wantEnv)
			}
			if spec.KillPattern != containerClaudeBin {
				t.Errorf("KillPattern = %q, want %q", spec.KillPattern, containerClaudeBin)
			}
		})
	}
}
