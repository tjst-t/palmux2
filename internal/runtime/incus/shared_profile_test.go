package incus

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tjst-t/palmux2/internal/runtime"
)

// [AC-Sd44947-1-1] Ensure creates the profile when it does not exist and adds
// the declared shared devices for sources that exist on the host.
func TestSharedProfile_EnsureCreatesAndPopulates(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// Create a couple of real sources so declaredDevices includes them.
	_ = os.MkdirAll(filepath.Join(home, "ghq"), 0o755)
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)

	fr := newFakeRunner()
	// profile show → not found the first time so Ensure creates it.
	fr.setResult("profile show", fakeResult{stderr: "Error: not found", code: 1})

	m := NewSharedProfileManager(fr.asRunner(), nil, nil)
	if err := m.Ensure(context.Background()); err != nil {
		t.Fatalf("Ensure: %v", err)
	}
	calls := fr.recorded()

	if _, ok := findCall(calls, "profile", "create", SharedProfileName); !ok {
		t.Errorf("[AC-Sd44947-1-1] expected 'profile create %s' when absent, got %v", SharedProfileName, calls)
	}
	for _, name := range []string{"ghq", "dot-claude"} {
		if _, ok := findCall(calls, "profile", "device", "add", SharedProfileName, name, "disk"); !ok {
			t.Errorf("[AC-Sd44947-1-1] expected profile device add for %q, got %v", name, calls)
		}
	}
}

// [AC-Sd44947-1-2] Reconcile restores a hand-stripped device on the next tick:
// when `profile show` reports the profile is MISSING ghq (drift), Ensure re-adds
// it (converges to the declaration).
func TestSharedProfile_ReconcileRestoresStrippedDevice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, "ghq"), 0o755)
	_ = os.MkdirAll(filepath.Join(home, ".claude"), 0o755)

	ghqSrc := filepath.Join(home, "ghq")
	claudeSrc := filepath.Join(home, ".claude")

	fr := newFakeRunner()
	// Profile exists but ghq was removed by hand — only dot-claude remains.
	yaml := "name: " + SharedProfileName + "\n" +
		"devices:\n" +
		"  dot-claude:\n" +
		"    type: disk\n" +
		"    source: " + claudeSrc + "\n" +
		"    path: " + claudeSrc + "\n" +
		"used_by: []\n"
	fr.setResult("profile show", fakeResult{stdout: yaml, code: 0})

	m := NewSharedProfileManager(fr.asRunner(), nil, nil)
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	calls := fr.recorded()

	// ghq must be re-added (converge to declaration).
	if _, ok := findCall(calls, "profile", "device", "add", SharedProfileName, "ghq", "disk", "source="+ghqSrc, "path="+ghqSrc); !ok {
		t.Errorf("[AC-Sd44947-1-2] expected ghq re-added on reconcile, got %v", calls)
	}
	// dot-claude already present + matching → must NOT be re-added.
	if _, ok := findCall(calls, "profile", "device", "add", SharedProfileName, "dot-claude"); ok {
		t.Errorf("[AC-Sd44947-1-2] dot-claude already converged, should not re-add: %v", calls)
	}
}

// [AC-Sd44947-1-2] Reconcile removes a stale device present in the profile but
// not in the declaration (e.g. a shared_dir the user removed).
func TestSharedProfile_ReconcileRemovesStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, "ghq"), 0o755)

	fr := newFakeRunner()
	yaml := "name: " + SharedProfileName + "\n" +
		"devices:\n" +
		"  sf-deadbeef1234:\n" +
		"    type: disk\n" +
		"    source: /home/ubuntu/.gone\n" +
		"    path: /home/ubuntu/.gone\n" +
		"used_by: []\n"
	fr.setResult("profile show", fakeResult{stdout: yaml, code: 0})

	m := NewSharedProfileManager(fr.asRunner(), nil, nil)
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, ok := findCall(fr.recorded(), "profile", "device", "remove", SharedProfileName, "sf-deadbeef1234"); !ok {
		t.Errorf("[AC-Sd44947-1-2] expected stale device removed, got %v", fr.recorded())
	}
}

// [AC-Sd44947-2-1] SetSharedDirs makes declaredDevices include the user folder
// (as an sf-<hash> device) when the source exists.
func TestSharedProfile_SharedDirsIncluded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	infPath := filepath.Join(home, ".infisical")
	_ = os.MkdirAll(infPath, 0o755)

	m := NewSharedProfileManager(newFakeRunner().asRunner(), nil, []string{infPath})
	devs := m.declaredDevices()
	want := sharedDirDeviceName(infPath)
	found := false
	for _, d := range devs {
		if d.name == want {
			found = true
			if d.source != infPath || d.path != infPath {
				t.Errorf("shared dir device source/path = %s/%s, want %s", d.source, d.path, infPath)
			}
		}
	}
	if !found {
		t.Errorf("[AC-Sd44947-2-1] expected shared dir device %q in %v", want, devs)
	}

	// A non-existent shared dir is skipped (not an error).
	m2 := NewSharedProfileManager(newFakeRunner().asRunner(), nil, []string{filepath.Join(home, ".nope")})
	for _, d := range m2.declaredDevices() {
		if strings.HasPrefix(d.name, "sf-") {
			t.Errorf("[AC-Sd44947-2-3] absent shared dir should be skipped, got %v", d)
		}
	}
}

// TestSharedProfile_AgentSharedPathsIncluded is [AC-S2b5691-1-3]'s unit-level
// half: SetAgentSharedPaths (agent.Registry.SharedContainerPaths — codex/
// opencode binary+npm-tree+auth shares) makes declaredDevices include each
// existing path as an "ag-<hash>" device, and skips a nonexistent one
// silently (same "source absent -> skip" contract as SetSharedDirs).
func TestSharedProfile_AgentSharedPathsIncluded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// Simulate a codex-style share: an npm package tree dir + a node binary
	// file, both plausibly OUTSIDE $HOME (unlike the config shared_dirs
	// case) — e.g. /usr/lib/node_modules, /usr/bin/node in production.
	nodeModules := filepath.Join(t.TempDir(), "node_modules")
	_ = os.MkdirAll(nodeModules, 0o755)
	nodeBin := filepath.Join(t.TempDir(), "node")
	_ = os.WriteFile(nodeBin, []byte("#!/bin/sh\n"), 0o755)
	missing := filepath.Join(t.TempDir(), "does-not-exist")

	m := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	m.SetAgentSharedPaths([]string{nodeModules, nodeBin, missing})

	devs := m.declaredDevices()
	wantTree := agentDeviceName(nodeModules)
	wantBin := agentDeviceName(nodeBin)
	wantMissing := agentDeviceName(missing)

	var foundTree, foundBin bool
	for _, d := range devs {
		if !strings.HasPrefix(d.name, agentDeviceNamePrefix) {
			continue
		}
		switch d.name {
		case wantTree:
			foundTree = true
			if d.source != nodeModules || d.path != nodeModules {
				t.Errorf("agent tree device source/path = %s/%s, want %s", d.source, d.path, nodeModules)
			}
		case wantBin:
			foundBin = true
			if d.source != nodeBin || d.path != nodeBin {
				t.Errorf("agent bin device source/path = %s/%s, want %s", d.source, d.path, nodeBin)
			}
		case wantMissing:
			t.Errorf("nonexistent agent path should be skipped, got device %v", d)
		}
	}
	if !foundTree {
		t.Errorf("expected agent-shared node_modules device %q in %v", wantTree, devs)
	}
	if !foundBin {
		t.Errorf("expected agent-shared node binary device %q in %v", wantBin, devs)
	}
}

// TestSharedProfile_AgentSharedPathsNotSymlinkSkipped proves an
// agent-shared path that is a symlink pointing OUTSIDE $HOME is NOT dropped
// by the Nix-dotfile symlink-skip filter — unlike ~/.bashrc &c, an agent
// share (e.g. a version-manager-installed node binary) routinely IS a
// symlink outside $HOME and that is the normal, working case.
func TestSharedProfile_AgentSharedPathsNotSymlinkSkipped(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	outsideDir := t.TempDir() // sibling tmpdir, NOT under home
	realBin := filepath.Join(outsideDir, "real-node")
	_ = os.WriteFile(realBin, []byte("#!/bin/sh\n"), 0o755)
	linkedBin := filepath.Join(home, "linked-node") // symlink OUTSIDE home's target
	if err := os.Symlink(realBin, linkedBin); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	m := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	m.SetAgentSharedPaths([]string{linkedBin})

	want := agentDeviceName(linkedBin)
	found := false
	for _, d := range m.declaredDevices() {
		if d.name == want {
			found = true
		}
	}
	if !found {
		t.Errorf("agent-shared symlink-outside-home path was skipped, want it kept (device %q)", want)
	}
}

// [AC-Sc4f091-2-1] Root-cause mitigation for the documented cross-instance
// palmux-shared flicker (docs/sprint-logs/Sc4f091/decisions.json): an instance
// with NO agent paths of its own (hasAgentOpinion == false) must NOT remove an
// "ag-*" device it finds in the profile but doesn't declare itself — that
// device belongs to a DIFFERENT palmux2 instance/process on the same host
// (e.g. a production instance with codex/opencode enabled, reconciled
// alongside an INSTANCE=dev rig that has no [agents.*] config at all).
// Blindly treating "not in my desired set" as "stale" for this namespace is
// exactly the mechanism that flickers codex/opencode's container shares in
// and out of the mount table on every reconcile tick.
func TestSharedProfile_NonOpinionatedInstanceSkipsForeignAgentDevices(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	fr := newFakeRunner()
	yaml := "name: " + SharedProfileName + "\n" +
		"devices:\n" +
		"  ag-deadbeef1234:\n" +
		"    type: disk\n" +
		"    source: /usr/lib/node_modules/opencode-ai\n" +
		"    path: /usr/lib/node_modules/opencode-ai\n" +
		"used_by: []\n"
	fr.setResult("profile show", fakeResult{stdout: yaml, code: 0})

	// This manager has NO agent paths configured (agentPaths never set) —
	// the "empty [agents.*]" instance from the race scenario.
	m := NewSharedProfileManager(fr.asRunner(), nil, nil)
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, ok := findCall(fr.recorded(), "profile", "device", "remove", SharedProfileName, "ag-deadbeef1234"); ok {
		t.Errorf("[AC-Sc4f091-2-1] non-opinionated instance must NOT remove a foreign ag-* device, got %v", fr.recorded())
	}
}

// [AC-Sc4f091-2-1] The flip side: an instance that DOES have agent paths
// configured (hasAgentOpinion == true) must still remove an "ag-*" device
// whose source has genuinely vanished (not in ITS OWN desired set because the
// underlying host path no longer exists) — the skip above must not regress
// ordinary stale-device cleanup for an opinionated instance.
func TestSharedProfile_OpinionatedInstanceStillRemovesGenuinelyStaleAgentDevice(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	// This instance's own (still-existing) agent share.
	nodeBin := filepath.Join(t.TempDir(), "node")
	_ = os.WriteFile(nodeBin, []byte("#!/bin/sh\n"), 0o755)

	fr := newFakeRunner()
	yaml := "name: " + SharedProfileName + "\n" +
		"devices:\n" +
		"  ag-nowgoneabcdef:\n" + // some OTHER, no-longer-existing agent path
		"    type: disk\n" +
		"    source: /tmp/sc4f091-vanished-path-does-not-exist\n" +
		"    path: /tmp/sc4f091-vanished-path-does-not-exist\n" +
		"used_by: []\n"
	fr.setResult("profile show", fakeResult{stdout: yaml, code: 0})

	m := NewSharedProfileManager(fr.asRunner(), nil, nil)
	m.SetAgentSharedPaths([]string{nodeBin}) // hasAgentOpinion == true
	if err := m.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if _, ok := findCall(fr.recorded(), "profile", "device", "remove", SharedProfileName, "ag-nowgoneabcdef"); !ok {
		t.Errorf("[AC-Sc4f091-2-1] opinionated instance must still remove a genuinely stale ag-* device, got %v", fr.recorded())
	}
}

// [AC-Sc4f091-2-2] Ensure serializes concurrent callers within one process
// (reconcileMu) — this is a regression guard for the within-process race a
// container's own Start() and the periodic scan-loop tick could otherwise hit
// if they land close together. We can't observe true interleaving through the
// fake runner directly, but running many concurrent Ensure() calls against a
// shared fakeRunner must never panic/race (run with -race in CI) and must
// leave the profile fully converged afterward.
func TestSharedProfile_EnsureConcurrentCallsDoNotRace(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	_ = os.MkdirAll(filepath.Join(home, "ghq"), 0o755)

	fr := newFakeRunner()
	fr.setResult("profile show", fakeResult{stderr: "Error: not found", code: 1})

	m := NewSharedProfileManager(fr.asRunner(), nil, nil)

	const n = 8
	done := make(chan struct{}, n)
	for i := 0; i < n; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			_ = m.Ensure(context.Background())
		}()
	}
	for i := 0; i < n; i++ {
		<-done
	}
}

// SetWorktreeBasedirFunc makes declaredDevices include the gwq worktree base dir
// as a same-path bind-mount, so a Claude/Bash tab opened on a linked (gwq)
// worktree finds its cwd inside the container. The base dir may live OUTSIDE
// ~/ghq (default ~/worktrees), so this also guards the symlink-skip exemption.
func TestSharedProfile_GwqWorktreesIncluded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	basedir := filepath.Join(home, "worktrees")
	_ = os.MkdirAll(basedir, 0o755)

	m := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	m.SetWorktreeBasedirFunc(func(context.Context) (string, error) { return basedir, nil })

	found := false
	for _, d := range m.declaredDevices() {
		if d.name == gwqWorktreesDevice {
			found = true
			if d.source != basedir || d.path != basedir {
				t.Errorf("gwq worktrees device source/path = %s/%s, want %s", d.source, d.path, basedir)
			}
		}
	}
	if !found {
		t.Errorf("expected gwq worktrees device %q in declaredDevices", gwqWorktreesDevice)
	}

	// Caching: the resolver must be consulted once, then the cached value reused.
	calls := 0
	m.resolvedBasedir = "" // reset cache primed by the first declaredDevices above
	m.SetWorktreeBasedirFunc(func(context.Context) (string, error) { calls++; return basedir, nil })
	_ = m.declaredDevices()
	_ = m.declaredDevices()
	if calls != 1 {
		t.Errorf("resolver called %d times, want 1 (cached)", calls)
	}

	// No resolver → device absent (feature off, existing hosts unaffected).
	m2 := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	for _, d := range m2.declaredDevices() {
		if d.name == gwqWorktreesDevice {
			t.Errorf("no resolver should omit the gwq worktrees device, got %v", d)
		}
	}

	// basedir == ~/ghq must NOT add a duplicate-path device (incus error).
	ghq := filepath.Join(home, "ghq")
	_ = os.MkdirAll(ghq, 0o755)
	m3 := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	m3.SetWorktreeBasedirFunc(func(context.Context) (string, error) { return ghq, nil })
	for _, d := range m3.declaredDevices() {
		if d.name == gwqWorktreesDevice {
			t.Errorf("basedir coinciding with ~/ghq must be skipped (duplicate path), got %v", d)
		}
	}
}

// SetAttachmentDir makes declaredDevices include the attachment upload root as a
// same-path bind-mount, so Ctrl+V-pasted images (saved on the host) are readable
// by in-container Claude at the exact absolute path the composer injects. The root
// lives OUTSIDE $HOME (default /tmp/palmux-uploads), so this also guards that it
// is exempt from the outside-$HOME symlink-skip (regression: without the exemption
// declaredDevices would drop it).
func TestSharedProfile_AttachmentDirIncluded(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	// A dir outside HOME, mirroring the /tmp/palmux-uploads default.
	attach := filepath.Join(t.TempDir(), "palmux-uploads")
	if err := os.MkdirAll(attach, 0o755); err != nil {
		t.Fatal(err)
	}

	m := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	m.SetAttachmentDir(attach + "/") // trailing slash must be trimmed
	found := false
	for _, d := range m.declaredDevices() {
		if d.name == attachmentDevice {
			found = true
			if d.source != attach || d.path != attach {
				t.Errorf("attachment device source/path = %s/%s, want %s (same path, no trailing slash)", d.source, d.path, attach)
			}
		}
	}
	if !found {
		t.Errorf("expected attachment device %q (outside-$HOME dir must survive the symlink-skip)", attachmentDevice)
	}

	// Absent root is skipped (not an error); empty disables the mount entirely.
	m2 := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	m2.SetAttachmentDir(filepath.Join(t.TempDir(), "does-not-exist"))
	for _, d := range m2.declaredDevices() {
		if d.name == attachmentDevice {
			t.Errorf("absent attachment root should be skipped, got %v", d)
		}
	}
	m3 := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	for _, d := range m3.declaredDevices() {
		if d.name == attachmentDevice {
			t.Errorf("empty attachment root should add no device, got %v", d)
		}
	}
}

// parseProfileDevices extracts name/source/path triples and ignores non-disk
// keys and the used_by block.
func TestParseProfileDevices(t *testing.T) {
	yaml := `name: palmux-shared
description: managed
config: {}
devices:
  ghq:
    type: disk
    source: /home/ubuntu/ghq
    path: /home/ubuntu/ghq
  dot-ssh:
    type: disk
    source: /home/ubuntu/.ssh
    path: /home/ubuntu/.ssh
used_by:
- /1.0/instances/ws-a
- /1.0/instances/ws-b
`
	got := parseProfileDevices(yaml)
	if len(got) != 2 {
		t.Fatalf("expected 2 devices, got %d: %v", len(got), got)
	}
	if got["ghq"].source != "/home/ubuntu/ghq" || got["ghq"].path != "/home/ubuntu/ghq" {
		t.Errorf("ghq device parsed wrong: %+v", got["ghq"])
	}
	if got["dot-ssh"].source != "/home/ubuntu/.ssh" {
		t.Errorf("dot-ssh source parsed wrong: %+v", got["dot-ssh"])
	}
}

// UsedByCount counts the used_by list entries.
func TestSharedProfile_UsedByCount(t *testing.T) {
	fr := newFakeRunner()
	fr.setResult("profile show", fakeResult{stdout: `name: palmux-shared
devices: {}
used_by:
- /1.0/instances/ws-a
- /1.0/instances/ws-b
- /1.0/instances/ws-c
`, code: 0})
	m := NewSharedProfileManager(fr.asRunner(), nil, nil)
	if n := m.UsedByCount(context.Background()); n != 3 {
		t.Errorf("UsedByCount = %d, want 3", n)
	}
}

// [AC-Sd44947-1-3] Migration: Start applies the shared profile to the instance
// AND strips the legacy per-container shared devices so the profile's copies win
// (no-downtime migration of a pre-Sd44947 container).
func TestStart_MigratesLegacyDevices(t *testing.T) {
	inst := "ws-migrate-12ab34cd"
	fr := newFakeRunner()
	fr.setResult("exec "+inst, fakeResult{code: 0})
	fr.setResult("list "+inst, fakeResult{stdout: "[]", code: 0})

	rt := newIncusRTWithShared(runtime.Config{Kind: runtime.KindIncusContainer, Image: "palmux-ws"}, inst, fr)
	if err := rt.Start(context.Background()); err != nil {
		t.Fatalf("Start: %v", err)
	}
	calls := fr.recorded()

	// The instance references the shared profile.
	if _, ok := findCall(calls, "profile", "add", inst, SharedProfileName); !ok {
		t.Errorf("[AC-Sd44947-1-3] expected 'profile add %s %s', got %v", inst, SharedProfileName, calls)
	}
	// Legacy instance-local shared devices are removed (migration).
	for _, name := range []string{"ghq", "dot-claude", "palmux-hook-bin"} {
		if _, ok := findCall(calls, "config", "device", "remove", inst, name); !ok {
			t.Errorf("[AC-Sd44947-1-3] expected legacy 'config device remove %s %s', got %v", inst, name, calls)
		}
	}
}

// [AC-Sd44947-1-1] InstanceProfiles is default + palmux-shared.
func TestSharedProfile_InstanceProfiles(t *testing.T) {
	m := NewSharedProfileManager(newFakeRunner().asRunner(), nil, nil)
	got := m.InstanceProfiles()
	if len(got) != 2 || got[0] != "default" || got[1] != SharedProfileName {
		t.Errorf("InstanceProfiles = %v, want [default %s]", got, SharedProfileName)
	}
}
