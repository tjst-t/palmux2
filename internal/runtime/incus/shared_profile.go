package incus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/tjst-t/palmux2/internal/config"
)

// Sd44947 — profile-as-mold.
//
// Every incus-container Workspace shares the same bind-mounts (ghq, ~/.claude,
// dotfiles, gh/ssh auth, the palmux hook binary, plus any user-declared
// [workspace] shared_dirs). Previously these were added per-container in Start()
// as instance-local `incus config device add` calls, which meant the declared
// intent and the live container state could drift with no detection or repair.
//
// SharedProfileManager collapses that group into a single host-wide incus
// profile named `palmux-shared`. Containers are launched with the two profiles
// `default` + `palmux-shared`, carry NO instance-local disk devices for the
// shared group, and incus live-propagates any profile device change to every
// running container that references it. The store's 10s scan loop calls
// Reconcile() every tick so a hand-stripped profile self-heals — the same
// self-healing shape as resyncExposedRoutes for Caddy routes.
//
// There is exactly one SharedProfileManager per host (held by the Registry).

// SharedProfileName is the incus profile that carries every Workspace's shared
// bind-mounts. Do not rename — operators inspect it with `incus profile show`.
const SharedProfileName = "palmux-shared"

// deviceSpec is one disk device (bind-mount) in the shared profile.
type deviceSpec struct {
	name   string
	source string // host path
	path   string // in-container path (always == source for our mounts)
}

// legacySharedDeviceNames are the fixed instance-local device names the OLD
// per-container path added in Start(). Migration removes these from an instance
// once the profile provides the equivalent (AC-Sd44947-1-3). Kept as a list so
// a container created before Sd44947 converges without a full regenerate.
var legacySharedDeviceNames = []string{
	"ghq",
	"dot-claude",
	"dot-claude-json",
	"dot-local-share-claude",
	"dot-local-bin",
	"dot-bashrc",
	"dot-profile",
	"dot-bash-profile",
	"dot-bashrc-d",
	"dot-gitconfig",
	"dot-config-gh",
	"dot-ssh",
	"palmux-hook-bin",
}

// SharedProfileManager owns the host-wide palmux-shared incus profile. Safe for
// concurrent use (the scan loop and the deploy-apply handler both call it).
type SharedProfileManager struct {
	run runner
	log *slog.Logger

	mu         sync.Mutex
	sharedDirs []string // config-driven extra shared folders (absolute host paths)

	ensuredOnce   bool   // whether the profile has been created at least once
	lastHookInode uint64 // inode of the last-reconciled palmux hook binary (S52fc2c-5 at profile scope)
}

// NewSharedProfileManager builds the manager. run may be nil → defaultRunner.
func NewSharedProfileManager(run runner, log *slog.Logger, sharedDirs []string) *SharedProfileManager {
	if run == nil {
		run = defaultRunner
	}
	if log == nil {
		log = slog.Default()
	}
	m := &SharedProfileManager{run: run, log: log}
	m.SetSharedDirs(sharedDirs)
	return m
}

// SetSharedDirs replaces the config-driven shared folder list. The next
// Ensure/Reconcile converges the profile to include them (Story 2 live apply).
func (m *SharedProfileManager) SetSharedDirs(dirs []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(dirs))
	copy(cp, dirs)
	m.sharedDirs = cp
}

// declaredDevices computes the desired device set from current host state +
// the config-driven shared dirs. This reuses the EXACT existence check +
// Nix-store-symlink-skip logic the old Start() mounts[] loop used
// (priority_rule 6 — reuse the mold, do not reinvent it). A source that does
// not exist is silently skipped; a dotfile symlinking outside $HOME (e.g.
// Nix/home-manager → /nix/store) is skipped so the container shell isn't
// broken by a dangling link.
func (m *SharedProfileManager) declaredDevices() []deviceSpec {
	home, err := os.UserHomeDir()
	if err != nil {
		m.log.Warn("incus shared profile: cannot resolve home dir", "err", err)
		return nil
	}
	mj := func(p ...string) string { return filepath.Join(append([]string{home}, p...)...) }

	candidates := []deviceSpec{
		{"ghq", mj("ghq"), mj("ghq")},
		{"dot-claude", mj(".claude"), mj(".claude")},
		{"dot-claude-json", mj(".claude.json"), mj(".claude.json")},
		{"dot-local-share-claude", mj(".local", "share", "claude"), mj(".local", "share", "claude")},
		{"dot-local-bin", mj(".local", "bin"), mj(".local", "bin")},
		{"dot-bashrc", mj(".bashrc"), mj(".bashrc")},
		{"dot-profile", mj(".profile"), mj(".profile")},
		{"dot-bash-profile", mj(".bash_profile"), mj(".bash_profile")},
		{"dot-bashrc-d", mj(".bashrc.d"), mj(".bashrc.d")},
		{"dot-gitconfig", mj(".gitconfig"), mj(".gitconfig")},
		{"dot-config-gh", mj(".config", "gh"), mj(".config", "gh")},
		{"dot-ssh", mj(".ssh"), mj(".ssh")},
	}

	// palmux hook binary → /usr/local/bin/palmux (in-container `palmux hook`).
	// Host-wide identical, so it belongs in the shared profile. Resolve any
	// symlink so a Nix/home-manager path change is picked up on the next
	// reconcile (the source string changes → the profile device is replaced).
	if palmuxBin, perr := os.Executable(); perr == nil && palmuxBin != "" {
		if resolved, rerr := filepath.EvalSymlinks(palmuxBin); rerr == nil && resolved != "" {
			palmuxBin = resolved
		}
		candidates = append(candidates, deviceSpec{"palmux-hook-bin", palmuxBin, "/usr/local/bin/palmux"})
	}

	// Config-driven shared folders (Story 2). Same-path mount; skipped if absent.
	m.mu.Lock()
	dirs := append([]string(nil), m.sharedDirs...)
	m.mu.Unlock()
	for _, d := range dirs {
		// Defensive re-validation: only accept $HOME-scoped paths even if the
		// config was hand-edited. Handles a leading ~ too.
		abs, verr := config.ExpandSharedDir(d, home)
		if verr != nil {
			m.log.Warn("incus shared profile: skipping invalid shared dir", "dir", d, "err", verr)
			continue
		}
		candidates = append(candidates, deviceSpec{sharedDirDeviceName(abs), abs, abs})
	}

	out := make([]deviceSpec, 0, len(candidates))
	for _, c := range candidates {
		if _, statErr := os.Stat(c.source); os.IsNotExist(statErr) {
			continue // source absent — skip silently
		}
		// Skip dotfiles that symlink OUTSIDE home (Nix store). ghq / claude /
		// hook-bin / user shared dirs are exempt — real dirs we always want.
		if isSkippableSymlinkDotfile(c.name) {
			if tgt, lerr := filepath.EvalSymlinks(c.source); lerr == nil {
				if rel, rerr := filepath.Rel(home, tgt); rerr != nil || strings.HasPrefix(rel, "..") {
					m.log.Info("incus shared profile: skipping dotfile that symlinks outside home",
						"source", c.source, "target", tgt)
					continue
				}
			}
		}
		out = append(out, c)
	}
	return out
}

// isSkippableSymlinkDotfile mirrors the exemption list from the old Start loop:
// ghq, palmux-hook-bin, dot-claude*, dot-local* and the user shared dirs (sf-*)
// are never symlink-skipped.
func isSkippableSymlinkDotfile(name string) bool {
	if name == "ghq" || name == "palmux-hook-bin" {
		return false
	}
	if strings.HasPrefix(name, "dot-claude") || strings.HasPrefix(name, "dot-local") || strings.HasPrefix(name, "sf-") {
		return false
	}
	return true
}

// sharedDirDeviceName derives a stable, incus-legal device name for a user
// shared dir from its absolute path. Fixed devices keep human names; user dirs
// get sf-<hash> to avoid collisions and illegal characters.
func sharedDirDeviceName(abs string) string {
	sum := sha256.Sum256([]byte(abs))
	return "sf-" + hex.EncodeToString(sum[:])[:12]
}

// Ensure creates palmux-shared if missing and converges its device set to the
// current declaration (adds missing, removes stale, refreshes a device whose
// source path changed — e.g. the palmux binary after a Nix update). Idempotent
// and safe to call every scan tick. It is the single reconcile primitive used
// by both Reconcile() (scan loop) and Start() (container launch).
func (m *SharedProfileManager) Ensure(ctx context.Context) error {
	desired := m.declaredDevices()
	desiredByName := make(map[string]deviceSpec, len(desired))
	for _, d := range desired {
		desiredByName[d.name] = d
	}

	// List current profile devices; create the profile if it does not exist.
	current, exists, err := m.currentDevices(ctx)
	if err != nil {
		return err
	}
	if !exists {
		if cerr := m.createProfile(ctx); cerr != nil {
			return cerr
		}
		current = map[string]deviceSpec{}
	}

	// Remove devices present in the profile but not desired (drift or a removed
	// shared dir). The profile is palmux-owned, so any device not in the
	// declaration is stale.
	for name := range current {
		if _, ok := desiredByName[name]; !ok {
			if rerr := m.profileDeviceRemove(ctx, name); rerr != nil {
				m.log.Warn("incus shared profile: device remove failed", "device", name, "err", rerr)
			}
		}
	}

	// Detect an in-place inode change of the palmux hook binary (same source
	// path, new inode — e.g. a `go build` overwrite). The bind-mount pins the
	// old inode, so force a device replace even though the source string is
	// unchanged (S52fc2c-5, ported to profile scope).
	hookInodeChanged := false
	for _, d := range desired {
		if d.name != "palmux-hook-bin" {
			continue
		}
		if ino, ok := statInode(d.source); ok {
			m.mu.Lock()
			// Force a replace when the inode differs from the last reconcile. We do
			// NOT guard on lastHookInode != 0: after a palmux restart the manager is
			// fresh (lastHookInode == 0) but a container launched before this process
			// started may still pin the OLD binary inode via the profile bind-mount.
			// Treating the first post-restart reconcile as "changed" force-replaces
			// the device so incus re-hotplugs the current binary to every container
			// (S52fc2c-5-3, ported to profile scope). This only fires when the device
			// is already present (the replace branch below), so a first-ever add is
			// unaffected.
			if m.lastHookInode != ino {
				hookInodeChanged = true
			}
			m.lastHookInode = ino
			m.mu.Unlock()
		}
	}

	// Add missing devices, and replace any whose source/path drifted.
	for _, d := range desired {
		cur, present := current[d.name]
		converged := present && cur.source == d.source && cur.path == d.path
		if d.name == "palmux-hook-bin" && hookInodeChanged {
			converged = false // force replace so incus re-hotplugs the new binary
		}
		if converged {
			continue
		}
		if present {
			// source/path changed (e.g. Nix path change for the hook binary) —
			// remove then re-add so incus re-hotplugs the new source.
			if rerr := m.profileDeviceRemove(ctx, d.name); rerr != nil {
				m.log.Warn("incus shared profile: device replace-remove failed", "device", d.name, "err", rerr)
			}
		}
		if aerr := m.profileDeviceAdd(ctx, d); aerr != nil {
			m.log.Warn("incus shared profile: device add failed", "device", d.name, "err", aerr)
		}
	}
	m.mu.Lock()
	m.ensuredOnce = true
	m.mu.Unlock()
	return nil
}

// Reconcile is the scan-loop entry point (self-heal). Same as Ensure; named
// separately so the store's optional-capability interface reads clearly.
func (m *SharedProfileManager) Reconcile(ctx context.Context) error { return m.Ensure(ctx) }

// InstanceProfiles is the profile list a Workspace container is launched with.
func (m *SharedProfileManager) InstanceProfiles() []string {
	return []string{"default", SharedProfileName}
}

// UsedByCount returns how many instances reference the palmux-shared profile
// (its `used_by:` list). Used for the apply message ("N containers refreshed").
// Best-effort: any error yields 0.
func (m *SharedProfileManager) UsedByCount(ctx context.Context) int {
	stdout, _, code, err := m.run(ctx, "profile", "show", SharedProfileName)
	if err != nil || code != 0 {
		return 0
	}
	count := 0
	inUsedBy := false
	for _, ln := range strings.Split(stdout, "\n") {
		trimmed := strings.TrimSpace(ln)
		if strings.HasPrefix(trimmed, "- ") {
			if inUsedBy {
				count++
			}
			continue
		}
		// A non-list line at column 0 is a top-level key. It enters/exits the
		// used_by block. (incus emits used_by sequence items at column 0.)
		if !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "\t") {
			inUsedBy = strings.HasPrefix(strings.TrimRight(ln, " \t\r"), "used_by:")
		}
	}
	return count
}

// createProfile creates the palmux-shared profile (idempotent — "already
// exists" is not an error).
func (m *SharedProfileManager) createProfile(ctx context.Context) error {
	_, stderr, code, err := m.run(ctx, "profile", "create", SharedProfileName)
	if err != nil {
		return fmt.Errorf("incus profile create %s: %w", SharedProfileName, err)
	}
	if code != 0 && !strings.Contains(stderr, "already exists") {
		return fmt.Errorf("incus profile create %s: code=%d stderr=%s", SharedProfileName, code, strings.TrimSpace(stderr))
	}
	// Best-effort description so operators know not to hand-edit it.
	_, _, _, _ = m.run(ctx, "profile", "set", SharedProfileName, "description",
		"palmux shared bind-mounts (managed by palmux — reconciled every scan; edits are reverted)")
	return nil
}

// currentDevices returns the profile's current disk devices keyed by name, and
// whether the profile exists at all. Uses `incus profile show` (YAML) and a
// tolerant line parser (no YAML dependency; incus show output is stable and
// simple for our disk-only devices).
func (m *SharedProfileManager) currentDevices(ctx context.Context) (map[string]deviceSpec, bool, error) {
	stdout, stderr, code, err := m.run(ctx, "profile", "show", SharedProfileName)
	if err != nil {
		return nil, false, fmt.Errorf("incus profile show %s: %w", SharedProfileName, err)
	}
	if code != 0 {
		low := strings.ToLower(stderr)
		if strings.Contains(low, "not found") || strings.Contains(low, "no such") {
			return nil, false, nil // profile absent
		}
		return nil, false, fmt.Errorf("incus profile show %s: code=%d stderr=%s", SharedProfileName, code, strings.TrimSpace(stderr))
	}
	return parseProfileDevices(stdout), true, nil
}

// parseProfileDevices extracts disk device name/source/path triples from
// `incus profile show` YAML output. Only the `devices:` block is read; each
// device is a 2-space-indented key with `source:`/`path:` children.
func parseProfileDevices(yaml string) map[string]deviceSpec {
	out := map[string]deviceSpec{}
	lines := strings.Split(yaml, "\n")
	inDevices := false
	cur := ""
	for _, ln := range lines {
		trimmedRight := strings.TrimRight(ln, " \t\r")
		if trimmedRight == "" {
			continue
		}
		// Top-level key (no leading space) ends the devices block.
		if !strings.HasPrefix(ln, " ") {
			inDevices = strings.HasPrefix(trimmedRight, "devices:")
			cur = ""
			continue
		}
		if !inDevices {
			continue
		}
		indent := len(ln) - len(strings.TrimLeft(ln, " "))
		content := strings.TrimSpace(ln)
		switch indent {
		case 2: // device name:  "  ghq:"
			cur = strings.TrimSuffix(content, ":")
			cur = strings.TrimSpace(cur)
			if _, ok := out[cur]; !ok {
				out[cur] = deviceSpec{name: cur}
			}
		case 4: // a key of the current device: "    source: /home/..."
			if cur == "" {
				continue
			}
			d := out[cur]
			if v, ok := strings.CutPrefix(content, "source:"); ok {
				d.source = strings.TrimSpace(v)
			} else if v, ok := strings.CutPrefix(content, "path:"); ok {
				d.path = strings.TrimSpace(v)
			}
			out[cur] = d
		}
	}
	// Only keep disk-style devices (those with a source). Non-disk devices in
	// the profile (there shouldn't be any) are ignored for our diff.
	for name, d := range out {
		if d.source == "" {
			delete(out, name)
		}
	}
	return out
}

// statInode returns the inode number of path, or (0,false) if it can't be
// resolved. Symlinks are followed so a Nix-wrapped binary reports its real inode.
func statInode(path string) (uint64, bool) {
	resolved := path
	if r, err := filepath.EvalSymlinks(path); err == nil && r != "" {
		resolved = r
	}
	fi, err := os.Stat(resolved)
	if err != nil {
		return 0, false
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return st.Ino, true
	}
	return 0, false
}

func (m *SharedProfileManager) profileDeviceAdd(ctx context.Context, d deviceSpec) error {
	_, stderr, code, err := m.run(ctx,
		"profile", "device", "add", SharedProfileName,
		d.name, "disk",
		"source="+d.source,
		"path="+d.path,
	)
	if err != nil {
		return fmt.Errorf("profile device add %s: %w", d.name, err)
	}
	if code != 0 && !strings.Contains(stderr, "already exists") {
		return fmt.Errorf("profile device add %s: code=%d stderr=%s", d.name, code, strings.TrimSpace(stderr))
	}
	return nil
}

func (m *SharedProfileManager) profileDeviceRemove(ctx context.Context, name string) error {
	_, stderr, code, err := m.run(ctx, "profile", "device", "remove", SharedProfileName, name)
	if err != nil {
		return fmt.Errorf("profile device remove %s: %w", name, err)
	}
	low := strings.ToLower(stderr)
	if code != 0 && !strings.Contains(low, "not found") && !strings.Contains(low, "doesn't have") && !strings.Contains(low, "does not have") {
		return fmt.Errorf("profile device remove %s: code=%d stderr=%s", name, code, strings.TrimSpace(stderr))
	}
	return nil
}
