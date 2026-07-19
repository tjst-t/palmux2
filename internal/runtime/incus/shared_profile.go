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
	"time"

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

// attachmentDevice is the profile device name for the attachment upload root
// bind-mount (Ctrl+V-pasted images). A fixed human name so operators recognise
// it in `incus profile show`.
const attachmentDevice = "palmux-uploads"

// gwqWorktreesDevice bind-mounts the gwq worktree base dir (where linked,
// gwq-created worktrees live — default ~/worktrees, OUTSIDE ~/ghq) same-path
// into every container. Without it a Claude/Bash tab opened on a linked
// worktree has no matching cwd in the container: claude falls back to `/`,
// bash to `~`, and claude's resume history (keyed by the absolute worktree
// path) is orphaned. Mirrors the ~/ghq mount. A fixed human name for
// `incus profile show`.
const gwqWorktreesDevice = "gwq-worktrees"

// S41bdf2-1-4: shared /nix/store + Nix system bin dir. On a NixOS appliance an
// app installed via the GUI lands in the host's Nix profile (systemPackages →
// /run/current-system/sw/bin, backed by /nix/store). To make that binary run
// INSIDE the Ubuntu Workspace containers WITHOUT re-installing it there, we
// bind-mount the host /nix/store (read-only) plus the resolved system bin dir into
// every container and prepend that bin dir to the container PATH. Nix binaries are
// patchelf'd with an absolute interpreter + RPATH into their own /nix/store
// closure, so a read-only /nix/store share is enough for them to run under the
// container's Ubuntu userland. These devices are added ONLY when /nix/store exists
// (i.e. on the NixOS appliance) — on Ubuntu hosts (dev / deploy-test) they are
// absent and container behaviour is unchanged.
const (
	// nixStoreDevice mounts the immutable, content-addressed host store read-only.
	nixStoreDevice = "nix-store"
	// nixSysbinDevice mounts the RESOLVED /run/current-system/sw/bin (a generation-
	// specific store path) so a post-rebuild generation change flips the device
	// source string → the existing reconcile source-diff replaces the device →
	// new packages hotplug into running containers (the §8.4 2-phase self-heal).
	nixSysbinDevice = "nix-sysbin"
	// nixBinContainerPath is where the system bin dir is mounted inside containers.
	// A dedicated path (not /run, which is the container's tmpfs) avoids clobbering
	// container-managed runtime dirs. The symlinks it contains point into
	// /nix/store (absolute), resolved via the read-only store mount above.
	nixBinContainerPath = "/opt/palmux-nix-bin"
	// nixContainerPATH is the profile-level PATH prepended with the Nix bin dir so
	// `incus exec` (and shells it starts) resolve GUI-installed apps. The design
	// doc §6 future form prescribes exactly this `environment.PATH` approach.
	nixContainerPATH = nixBinContainerPath + ":/home/ubuntu/.local/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"
)

// nixStorePath / nixSysbinSource are the host paths shared into every container.
// They are vars (not consts) only so tests can point them at a fixture; production
// always uses the fixed /nix/store + /run/current-system/sw/bin.
var (
	nixStorePath    = "/nix/store"
	nixSysbinSource = "/run/current-system/sw/bin"
)

// deviceSpec is one disk device (bind-mount) in the shared profile.
type deviceSpec struct {
	name     string
	source   string // host path
	path     string // in-container path (== source for most of our mounts)
	readonly bool   // S41bdf2-1-4: read-only bind (the shared /nix/store + sys bin)
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

	mu            sync.Mutex
	sharedDirs    []string // config-driven extra shared folders (absolute host paths)
	attachmentDir string   // resolved attachment upload ROOT (settings.AttachmentUploadDir)
	agentPaths    []string // S2b5691: agent.Registry.SharedContainerPaths() — codex/opencode binary+auth shares

	// worktreeBasedirFn resolves the gwq worktree base dir (injected so this
	// package stays decoupled from internal/gwq; nil ⇒ feature off). resolvedBasedir
	// caches the first success — the base dir is stable global gwq config, so we
	// avoid spawning `gwq` every reconcile tick; a change needs a palmux restart.
	worktreeBasedirFn func(context.Context) (string, error)
	resolvedBasedir   string

	ensuredOnce   bool   // whether the profile has been created at least once
	lastHookInode uint64 // inode of the last-reconciled palmux hook binary (S52fc2c-5 at profile scope)

	// reconcileMu (Sc4f091-2) serializes the ENTIRE Ensure() read-modify-write
	// critical section (list current devices -> diff -> remove/add via `incus`
	// CLI calls) against itself, within THIS process only. Ensure() has at
	// least 3 call sites sharing one SharedProfileManager (Start(), the
	// per-container applySharedProfile fast path, and the ~10s scan-loop
	// ReconcileShared tick) — without this lock two of them firing close
	// together (e.g. a new Workspace's Start() landing mid-tick) can interleave
	// their own currentDevices()/profileDeviceAdd()/profileDeviceRemove() calls
	// against the same profile. m.mu (above) only ever guards small field
	// reads/writes, never the multi-step incus-CLI sequence itself — this is a
	// separate, coarser lock scoped to exactly that sequence so normal field
	// access (SetSharedDirs, SetAgentSharedPaths, etc.) is never blocked by an
	// in-flight reconcile.
	reconcileMu sync.Mutex
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

// SetWorktreeBasedirFunc injects the resolver for the gwq worktree base dir.
// Set once at wiring time (Registry) so declaredDevices can mount it. nil-safe:
// with no resolver the gwq worktrees mount is simply omitted.
func (m *SharedProfileManager) SetWorktreeBasedirFunc(fn func(context.Context) (string, error)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.worktreeBasedirFn = fn
}

// worktreeBasedir returns the resolved gwq worktree base dir (absolute host
// path), or "" if unavailable. The first success is cached; failures are not,
// so a transiently-unavailable gwq is retried on the next reconcile.
func (m *SharedProfileManager) worktreeBasedir() string {
	m.mu.Lock()
	cached, fn := m.resolvedBasedir, m.worktreeBasedirFn
	m.mu.Unlock()
	if cached != "" {
		return cached
	}
	if fn == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	dir, err := fn(ctx)
	if err != nil || dir == "" {
		m.log.Debug("incus shared profile: gwq worktree basedir unresolved", "err", err)
		return ""
	}
	m.mu.Lock()
	m.resolvedBasedir = dir
	m.mu.Unlock()
	return dir
}

// SetAttachmentDir records the attachment upload ROOT (settings.AttachmentUploadDir)
// so it is bind-mounted at the same host path inside every container. Without this,
// an image pasted via Ctrl+V lands under `/tmp/palmux-uploads/...` on the HOST and
// the absolute path the composer/terminal injects is unreadable by in-container
// Claude (the container has no such file). Sharing the root at the identical path
// makes the injected path resolve in both host and container runtimes. Empty ⇒ no
// attachment mount. The next Ensure/Reconcile converges it (live-propagated to
// running containers).
func (m *SharedProfileManager) SetAttachmentDir(dir string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.attachmentDir = strings.TrimRight(dir, "/")
}

// SetAgentSharedPaths replaces the agent-adapter-driven shared path list
// (S2b5691) — every absolute host path (directory OR standalone file:
// `incus profile device add ... disk` bind-mounts either) returned by
// agent.Registry.SharedContainerPaths(), which aggregates each registered
// codex/opencode-style [agent.InContainerProvider] adapter's own binary +
// npm-package-tree + auth/config dir shares (see
// internal/agent/incontainer.go's doc comment for why the whole npm package
// tree AND the node runtime are shared, not just the leaf binary — the
// D5/"npm-global wrapper" failure mode). Unlike sharedDirs (Sd44947, always
// $HOME-scoped, validated via config.ExpandSharedDir), these paths already
// come from resolveHostBinary/npmGlobalRoot and are NOT $HOME-scoped by
// contract — a global npm install commonly lives under /usr/lib/node_modules
// or ~/.nvm, and the node runtime itself is typically /usr/bin/node or
// /usr/local/bin/node. declaredDevices' existing "source absent → skip"
// filter still applies; the Nix-symlink-skip filter does NOT (see
// isSkippableSymlinkDotfile's "ag-" exemption below) since a legitimate
// agent share is routinely a symlink pointing outside $HOME (e.g. a
// version-manager-installed node binary) — that is normal, not the broken
// dangling-dotfile case the skip guards against.
//
// The next Ensure/Reconcile converges the profile to include them
// (live-propagated to already-running containers, same as SetSharedDirs).
func (m *SharedProfileManager) SetAgentSharedPaths(paths []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := make([]string, len(paths))
	copy(cp, paths)
	m.agentPaths = cp
}

// agentDeviceNamePrefix distinguishes an agent-adapter-driven device
// (S2b5691) from a user-declared [workspace] shared_dirs device ("sf-" —
// Sd44947) in `incus profile show` output, purely for operator legibility;
// both are content-hash-named (sharedDirDeviceName) since neither has a
// human-friendly fixed name the way ghq/dot-claude/etc. do.
const agentDeviceNamePrefix = "ag-"

// agentDeviceName derives a stable, incus-legal device name for an
// agent-adapter-shared path (S2b5691), mirroring sharedDirDeviceName's
// content-hash scheme but with its own prefix (see agentDeviceNamePrefix).
func agentDeviceName(abs string) string {
	sum := sha256.Sum256([]byte(abs))
	return agentDeviceNamePrefix + hex.EncodeToString(sum[:])[:12]
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
		{name: "ghq", source: mj("ghq"), path: mj("ghq")},
		{name: "dot-claude", source: mj(".claude"), path: mj(".claude")},
		{name: "dot-claude-json", source: mj(".claude.json"), path: mj(".claude.json")},
		{name: "dot-local-share-claude", source: mj(".local", "share", "claude"), path: mj(".local", "share", "claude")},
		{name: "dot-local-bin", source: mj(".local", "bin"), path: mj(".local", "bin")},
		{name: "dot-bashrc", source: mj(".bashrc"), path: mj(".bashrc")},
		{name: "dot-profile", source: mj(".profile"), path: mj(".profile")},
		{name: "dot-bash-profile", source: mj(".bash_profile"), path: mj(".bash_profile")},
		{name: "dot-bashrc-d", source: mj(".bashrc.d"), path: mj(".bashrc.d")},
		{name: "dot-gitconfig", source: mj(".gitconfig"), path: mj(".gitconfig")},
		{name: "dot-config-gh", source: mj(".config", "gh"), path: mj(".config", "gh")},
		{name: "dot-ssh", source: mj(".ssh"), path: mj(".ssh")},
	}

	// gwq worktree base dir (default ~/worktrees, OUTSIDE ~/ghq). Mount it
	// same-path so a Claude/Bash tab opened on a linked (gwq-created) worktree
	// finds its cwd inside the container — otherwise claude starts at `/`, bash
	// at `~`, and claude's resume history (keyed by the absolute worktree path)
	// is orphaned. Mirrors the ~/ghq mount. Skipped when unresolved, when it is
	// the home dir itself, or when it coincides with the ghq mount (a duplicate
	// device path is an incus error). The source-absent filter below still
	// applies (a base dir with no worktrees yet is simply not mounted).
	if bd := m.worktreeBasedir(); bd != "" && bd != home && bd != mj("ghq") {
		candidates = append(candidates, deviceSpec{name: gwqWorktreesDevice, source: bd, path: bd})
	}

	// palmux hook binary → /usr/local/bin/palmux (in-container `palmux hook`).
	// Host-wide identical, so it belongs in the shared profile. Resolve any
	// symlink so a Nix/home-manager path change is picked up on the next
	// reconcile (the source string changes → the profile device is replaced).
	if palmuxBin, perr := os.Executable(); perr == nil && palmuxBin != "" {
		if resolved, rerr := filepath.EvalSymlinks(palmuxBin); rerr == nil && resolved != "" {
			palmuxBin = resolved
		}
		candidates = append(candidates, deviceSpec{name: "palmux-hook-bin", source: palmuxBin, path: "/usr/local/bin/palmux"})
	}

	// S41bdf2-1-4: shared /nix/store + system bin (NixOS appliance only). Gated on
	// /nix/store existing so Ubuntu hosts are unaffected. The sysbin source is the
	// RESOLVED store path (generation-specific) so a rebuild's generation change
	// replaces the device via the source-diff reconcile.
	if _, statErr := os.Stat(nixStorePath); statErr == nil {
		candidates = append(candidates, deviceSpec{nixStoreDevice, nixStorePath, nixStorePath, true})
		sysbin := nixSysbinSource
		if resolved, rerr := filepath.EvalSymlinks(nixSysbinSource); rerr == nil && resolved != "" {
			sysbin = resolved
		}
		candidates = append(candidates, deviceSpec{nixSysbinDevice, sysbin, nixBinContainerPath, true})
	}

	// Attachment upload root → same-path mount so Ctrl+V-pasted images (saved on
	// the host under this root) are readable by in-container Claude at the exact
	// absolute path the composer/terminal injects. Lives outside $HOME (default
	// /tmp/palmux-uploads), so it is exempt from the symlink-skip below.
	m.mu.Lock()
	attach := m.attachmentDir
	dirs := append([]string(nil), m.sharedDirs...)
	agentPaths := append([]string(nil), m.agentPaths...)
	m.mu.Unlock()
	if attach != "" {
		candidates = append(candidates, deviceSpec{name: attachmentDevice, source: attach, path: attach})
	}

	// Config-driven shared folders (Story 2). Same-path mount; skipped if absent.
	for _, d := range dirs {
		// Defensive re-validation: only accept $HOME-scoped paths even if the
		// config was hand-edited. Handles a leading ~ too.
		abs, verr := config.ExpandSharedDir(d, home)
		if verr != nil {
			m.log.Warn("incus shared profile: skipping invalid shared dir", "dir", d, "err", verr)
			continue
		}
		candidates = append(candidates, deviceSpec{name: sharedDirDeviceName(abs), source: abs, path: abs})
	}

	// S2b5691: agent-adapter-driven shares (codex/opencode binary + npm
	// package tree + node runtime + auth/config dirs — see
	// SetAgentSharedPaths' doc comment). Already absolute, resolved host
	// paths — no $HOME scoping or ~ expansion applies (unlike the config
	// shared_dirs loop above).
	for _, abs := range agentPaths {
		if abs == "" {
			continue
		}
		candidates = append(candidates, deviceSpec{name: agentDeviceName(abs), source: abs, path: abs})
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
	if name == "ghq" || name == gwqWorktreesDevice || name == "palmux-hook-bin" || name == attachmentDevice {
		return false
	}
	// S41bdf2-1-4: the shared /nix devices intentionally live OUTSIDE $HOME (host
	// /nix/store, /run/current-system). They must never be symlink-skipped.
	if name == nixStoreDevice || name == nixSysbinDevice {
		return false
	}
	if strings.HasPrefix(name, "dot-claude") || strings.HasPrefix(name, "dot-local") || strings.HasPrefix(name, "sf-") {
		return false
	}
	// S2b5691: agent-adapter-shared paths (codex/opencode binary/npm-tree/node
	// runtime) routinely live OUTSIDE $HOME (e.g. a version-manager-installed
	// node, or a system npm -g root) — that is the normal, working case for
	// these paths, not the broken dangling-Nix-dotfile case this skip guards
	// against, so they are never symlink-skipped (mirrors "sf-"'s exemption).
	if strings.HasPrefix(name, agentDeviceNamePrefix) {
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
//
// Sc4f091-2: the whole method runs under reconcileMu so at most one
// list-diff-mutate cycle is in flight per PROCESS at a time (see reconcileMu's
// doc comment on the struct). This does not, by itself, solve cross-PROCESS
// contention — palmux-shared is still a true host-wide singleton with no
// per-instance namespacing (see the agent-share skip below and the backlog
// item this Story files for the full fix) — but it removes one genuine
// self-inflicted race (two goroutines in the SAME process interleaving their
// own incus CLI calls against the same profile).
func (m *SharedProfileManager) Ensure(ctx context.Context) error {
	m.reconcileMu.Lock()
	defer m.reconcileMu.Unlock()

	desired := m.declaredDevices()
	desiredByName := make(map[string]deviceSpec, len(desired))
	for _, d := range desired {
		desiredByName[d.name] = d
	}

	m.mu.Lock()
	hasAgentOpinion := len(m.agentPaths) > 0
	m.mu.Unlock()

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
	// declaration is stale — EXCEPT agent-adapter-shared ("ag-*", S2b5691)
	// devices when THIS instance has no [agents.*] paths of its own
	// (hasAgentOpinion == false).
	//
	// Root cause (Sc4f091-2, confirmed by live reproduction — see
	// docs/sprint-logs/Sc4f091/decisions.json): palmux-shared is a HOST-WIDE
	// singleton profile, not scoped per palmux2 instance/process. An instance
	// with no codex/opencode configured has an empty agentPaths and therefore
	// an empty `desired` set for the "ag-*" namespace — under the OLD blind
	// "not in my desired set -> stale, remove" rule, that instance's own
	// routine ~10s reconcile tick would strip ANOTHER instance's live
	// codex/opencode container shares (binary/npm-tree/auth dirs) out from
	// under it, then that other instance's next tick re-adds them, forever,
	// for as long as both processes run (exactly the flicker this Story's
	// acceptance test's wait_for_agent_share() doc comment already describes
	// having reproduced live). Bind-mount removal is briefly visible INSIDE
	// already-running containers (the profile live-propagates), so an
	// in-container opencode/codex process doing a "create if missing"
	// filesystem operation during that window sees a stub/absent/wrong-owner
	// path — reproduced directly in this Story with a synthetic busy-writer +
	// device-toggle loop, yielding the SAME symptom text as the historical
	// failures (`mkdir: ... Permission denied`, `No such file or directory`)
	// at a comparable failure rate.
	//
	// An instance that HAS agents configured continues to manage "ag-*"
	// devices exactly as before (add missing, remove ones whose source
	// genuinely vanished, replace on drift) — only a non-opinionated instance
	// defers. This does not fully solve the singleton-profile design (two
	// instances that BOTH have different agents.* configured could still
	// disagree — see backlog for the full per-instance-namespacing fix), but
	// it eliminates the common, documented, and highly disruptive "one
	// instance has agents enabled, others (e.g. an INSTANCE=dev rig) don't"
	// pattern without requiring any cross-instance coordination.
	for name := range current {
		if _, ok := desiredByName[name]; !ok {
			if !hasAgentOpinion && strings.HasPrefix(name, agentDeviceNamePrefix) {
				continue
			}
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
	// S41bdf2-1-4: converge the profile-level PATH so `incus exec` (and shells it
	// starts) resolve GUI-installed Nix apps from the shared system bin dir. Only
	// on a NixOS appliance (/nix/store present); Ubuntu hosts keep the default PATH.
	if _, statErr := os.Stat(nixStorePath); statErr == nil {
		m.setEnvPath(ctx, nixContainerPATH)
	} else {
		m.setEnvPath(ctx, "") // unset (idempotent) on non-Nix hosts
	}

	m.mu.Lock()
	m.ensuredOnce = true
	m.mu.Unlock()
	return nil
}

// setEnvPath converges the profile's `environment.PATH` config key to want
// (empty = unset). It reads the current value first and only writes on a diff so
// running containers aren't churned every scan tick. Best-effort (logged).
func (m *SharedProfileManager) setEnvPath(ctx context.Context, want string) {
	cur, _ := m.currentEnvPath(ctx)
	if cur == want {
		return
	}
	if want == "" {
		if _, stderr, code, err := m.run(ctx, "profile", "unset", SharedProfileName, "environment.PATH"); err != nil || (code != 0 && !strings.Contains(strings.ToLower(stderr), "not")) {
			m.log.Warn("incus shared profile: environment.PATH unset failed", "code", code, "err", err)
		}
		return
	}
	if _, stderr, code, err := m.run(ctx, "profile", "set", SharedProfileName, "environment.PATH="+want); err != nil || code != 0 {
		m.log.Warn("incus shared profile: environment.PATH set failed", "code", code, "stderr", strings.TrimSpace(stderr), "err", err)
	}
}

// currentEnvPath reads config.environment.PATH from `incus profile show`.
func (m *SharedProfileManager) currentEnvPath(ctx context.Context) (string, bool) {
	stdout, _, code, err := m.run(ctx, "profile", "show", SharedProfileName)
	if err != nil || code != 0 {
		return "", false
	}
	inConfig := false
	for _, ln := range strings.Split(stdout, "\n") {
		if !strings.HasPrefix(ln, " ") && strings.TrimRight(ln, " \t\r") != "" {
			inConfig = strings.HasPrefix(strings.TrimRight(ln, " \t\r"), "config:")
			continue
		}
		if !inConfig {
			continue
		}
		content := strings.TrimSpace(ln)
		if v, ok := strings.CutPrefix(content, "environment.PATH:"); ok {
			return strings.TrimSpace(v), true
		}
	}
	return "", false
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
	args := []string{
		"profile", "device", "add", SharedProfileName,
		d.name, "disk",
		"source=" + d.source,
		"path=" + d.path,
	}
	if d.readonly {
		args = append(args, "readonly=true")
	}
	_, stderr, code, err := m.run(ctx, args...)
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
