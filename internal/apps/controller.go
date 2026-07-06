package apps

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"

	"github.com/tjst-t/palmux2/internal/config"
)

// SharedDirsPort is the single-source binding to Sd44947 shared_dirs. The share
// toggle reads/writes the SAME [workspace].shared_dirs the generic 共有フォルダ
// list uses (AC-S41bdf2-2-1) so the two never diverge; Apply live-propagates to
// running containers (hot). Implemented by deploy.Controller.
type SharedDirsPort interface {
	// CurrentSharedDirs returns the currently-shared absolute host paths.
	CurrentSharedDirs() []string
	// ApplySharedDirs persists + live-propagates a new shared-dir set, returning
	// the number of running containers refreshed.
	ApplySharedDirs(ctx context.Context, dirs []string) (int, error)
}

// RebuildPort is the S673a42 GUI-kick rebuild path (reused, not duplicated —
// priority_rule 6). Implemented in server wiring over deploy.Trigger/QueryRebuild.
type RebuildPort interface {
	NixOSHost() bool
	TriggerRebuild(ctx context.Context) error
	// RebuildStatus reports whether a rebuild is in progress and whether the last
	// finished run failed.
	RebuildStatus(ctx context.Context) (running bool, failed bool, err error)
}

// Controller owns the app install store, the drop-in generation, and the wiring
// to the shared-dirs + rebuild ports. One per server.
type Controller struct {
	configDir string
	dropinDir string // <flakeDir>/local — where 20-apps.nix is written
	shared    SharedDirsPort
	rebuild   RebuildPort
	logger    *slog.Logger

	mu          sync.Mutex
	pendingID   string // app id whose install kicked the current rebuild
	lastErrByID map[string]string
}

// New builds the controller. dropinDir is the on-appliance flake local/ dir.
func New(configDir, dropinDir string, shared SharedDirsPort, rebuild RebuildPort, logger *slog.Logger) *Controller {
	if logger == nil {
		logger = slog.Default()
	}
	return &Controller{
		configDir:   configDir,
		dropinDir:   dropinDir,
		shared:      shared,
		rebuild:     rebuild,
		logger:      logger,
		lastErrByID: map[string]string{},
	}
}

// AppView is one card in GET /api/apps.
type AppView struct {
	ID          string `json:"id"`
	Display     string `json:"display"`
	Description string `json:"description"`
	Icon        string `json:"icon"`
	Package     string `json:"package"`
	AuthPath    string `json:"authPath"` // ~-form candidate ("" = no share row)
	Installed   bool   `json:"installed"`
	Shared      bool   `json:"shared"`
	Custom      bool   `json:"custom"`
	// State ∈ available|installing|installed|shared|error (drives the card visuals).
	State string `json:"state"`
	Error string `json:"error,omitempty"`
	// Rebuild-boundary + reach metadata (AC-S41bdf2-1-2 / 3-2). Static per row but
	// returned so the FE never hardcodes the wording.
	InstallBoundary string `json:"installBoundary"` // "rebuild"
	InstallReach    string `json:"installReach"`    // "host+containers"
	ShareBoundary   string `json:"shareBoundary"`   // "hot"
	ShareReach      string `json:"shareReach"`      // "containers"
}

// ListView is the GET /api/apps response.
type ListView struct {
	Apps           []AppView `json:"apps"`
	NixOSHost      bool      `json:"nixOSHost"`
	RebuildRunning bool      `json:"rebuildRunning"`
	Home           string    `json:"home"`
}

// List returns the merged catalog ∪ installed(custom) cards with live state.
func (c *Controller) List(ctx context.Context) (ListView, error) {
	af, err := LoadApps(c.configDir)
	if err != nil {
		return ListView{}, err
	}
	home, _ := os.UserHomeDir()

	installedByID := map[string]InstalledApp{}
	for _, a := range af.Installed {
		installedByID[a.ID] = a
	}
	sharedSet := map[string]bool{}
	for _, d := range c.shared.CurrentSharedDirs() {
		sharedSet[d] = true
	}

	rebuildRunning := false
	if c.rebuild != nil {
		if running, _, qerr := c.rebuild.RebuildStatus(ctx); qerr == nil {
			rebuildRunning = running
		}
	}

	// If a rebuild finished, resolve the pending install into installed/error
	// BEFORE snapshotting so a just-settled failure shows on this same response.
	c.settlePending(ctx, rebuildRunning)

	c.mu.Lock()
	pending := c.pendingID
	errs := make(map[string]string, len(c.lastErrByID))
	for k, v := range c.lastErrByID {
		errs[k] = v
	}
	c.mu.Unlock()

	var out []AppView
	emit := func(e CatalogEntry, custom bool) {
		v := AppView{
			ID: e.ID, Display: e.Display, Description: e.Description, Icon: e.Icon,
			Package: e.Package, AuthPath: e.AuthPath, Custom: custom,
			InstallBoundary: "rebuild", InstallReach: "host+containers",
			ShareBoundary: "hot", ShareReach: "containers",
		}
		_, v.Installed = installedByID[e.ID]
		if v.AuthPath != "" && home != "" {
			if abs, e2 := config.ExpandSharedDir(e.AuthPath, home); e2 == nil {
				v.Shared = sharedSet[abs]
			}
		}
		switch {
		case errs[e.ID] != "":
			v.State = "error"
			v.Error = errs[e.ID]
		case rebuildRunning && pending == e.ID:
			v.State = "installing"
		case v.Installed && v.Shared:
			v.State = "shared"
		case v.Installed:
			v.State = "installed"
		default:
			v.State = "available"
		}
		out = append(out, v)
	}

	seen := map[string]bool{}
	for _, e := range catalog {
		emit(e, false)
		seen[e.ID] = true
	}
	// Custom installed apps not in the catalog.
	for _, a := range af.Installed {
		if seen[a.ID] {
			continue
		}
		emit(CatalogEntry{
			ID: a.ID, Display: displayOr(a.Display, a.ID), Description: "ユーザ定義アプリ",
			Icon: iconOr(a.Icon, "📦"), Package: a.Package, AuthPath: a.AuthPath,
		}, true)
		seen[a.ID] = true
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return ListView{Apps: out, NixOSHost: c.rebuildNixOS(), RebuildRunning: rebuildRunning, Home: home}, nil
}

func (c *Controller) rebuildNixOS() bool { return c.rebuild != nil && c.rebuild.NixOSHost() }

// settlePending clears an install pending-marker once the rebuild is no longer
// running, recording a failure against the app if the run failed.
func (c *Controller) settlePending(ctx context.Context, rebuildRunning bool) {
	if rebuildRunning || c.rebuild == nil {
		return
	}
	c.mu.Lock()
	pending := c.pendingID
	c.mu.Unlock()
	if pending == "" {
		return
	}
	_, failed, err := c.rebuild.RebuildStatus(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pendingID != pending { // changed under us
		return
	}
	c.pendingID = ""
	if err == nil && failed {
		c.lastErrByID[pending] = "nixos-rebuild switch が失敗しました（旧世代を維持）。ログ: journalctl -u palmux-rebuild"
	}
}

// InstallResult is returned by Install/Uninstall.
type InstallResult struct {
	OK            bool   `json:"ok"`
	Installed     bool   `json:"installed"`
	RebuildKicked bool   `json:"rebuildKicked"`
	NeedsRebuild  bool   `json:"needsRebuild"`
	Message       string `json:"message"`
}

// Install marks an app installed, regenerates the drop-in, and kicks the rebuild.
// For a catalog app, id is enough. For a custom app, pkg (validated attr) and
// optional authPath are supplied. On a non-NixOS host the intent is persisted and
// needsRebuild is reported (mirrors the domain root-class UX) but no rebuild runs.
func (c *Controller) Install(ctx context.Context, id, pkg, authPath string) (InstallResult, error) {
	if !ValidAppID(id) {
		return InstallResult{}, fmt.Errorf("apps: invalid app id %q", id)
	}
	rec := InstalledApp{ID: id}
	if e, ok := catalogByID(id); ok {
		rec.Package = e.Package
		rec.AuthPath = e.AuthPath
		rec.Display = e.Display
		rec.Icon = e.Icon
	} else {
		// Custom app — require a validated package attr.
		if !ValidPackageAttr(pkg) {
			return InstallResult{}, fmt.Errorf("apps: invalid package attr %q", pkg)
		}
		rec.Package = pkg
		rec.Custom = true
		if strings.TrimSpace(authPath) != "" {
			// $HOME-scope validate the auth path (reuse Sd44947 rule).
			if home, herr := os.UserHomeDir(); herr == nil {
				if _, verr := config.ExpandSharedDir(authPath, home); verr != nil {
					return InstallResult{}, fmt.Errorf("apps: %w", verr)
				}
			}
			rec.AuthPath = strings.TrimSpace(authPath)
		}
	}

	af, err := LoadApps(c.configDir)
	if err != nil {
		return InstallResult{}, err
	}
	replaced := false
	for i := range af.Installed {
		if af.Installed[i].ID == id {
			af.Installed[i] = rec
			replaced = true
			break
		}
	}
	if !replaced {
		af.Installed = append(af.Installed, rec)
	}
	if err := WriteApps(c.configDir, af); err != nil {
		return InstallResult{}, err
	}
	// Clear any prior error for this app on a fresh install attempt.
	c.mu.Lock()
	delete(c.lastErrByID, id)
	c.mu.Unlock()

	return c.regenAndRebuild(ctx, af, id, true)
}

// Uninstall removes an app from the store, regenerates the drop-in, and rebuilds.
// It does NOT touch the share state — an operator can unshare separately; but a
// removed app whose auth folder is still shared keeps the folder shared until they
// toggle it off (the share is a distinct, independently-valid intent).
func (c *Controller) Uninstall(ctx context.Context, id string) (InstallResult, error) {
	af, err := LoadApps(c.configDir)
	if err != nil {
		return InstallResult{}, err
	}
	kept := af.Installed[:0]
	for _, a := range af.Installed {
		if a.ID != id {
			kept = append(kept, a)
		}
	}
	af.Installed = kept
	if err := WriteApps(c.configDir, af); err != nil {
		return InstallResult{}, err
	}
	c.mu.Lock()
	delete(c.lastErrByID, id)
	if c.pendingID == id {
		c.pendingID = ""
	}
	c.mu.Unlock()
	return c.regenAndRebuild(ctx, af, id, false)
}

// regenAndRebuild writes the drop-in and (on NixOS) kicks the rebuild.
func (c *Controller) regenAndRebuild(ctx context.Context, af AppsFile, id string, installing bool) (InstallResult, error) {
	res := InstallResult{OK: true, Installed: installing}
	nixos := c.rebuildNixOS()
	if !nixos {
		res.NeedsRebuild = true
		res.Message = "アプリ設定を保存しました。反映は NixOS アプライアンス上の nixos-rebuild で行われます。"
		return res, nil
	}
	if err := WriteDropin(c.dropinDir, af); err != nil {
		return InstallResult{}, fmt.Errorf("apps: write drop-in: %w", err)
	}
	if err := c.rebuild.TriggerRebuild(ctx); err != nil {
		return InstallResult{}, fmt.Errorf("apps: kick rebuild: %w", err)
	}
	if installing {
		c.mu.Lock()
		c.pendingID = id
		c.mu.Unlock()
	}
	res.RebuildKicked = true
	res.NeedsRebuild = true
	res.Message = "nixos-rebuild switch を開始しました（ホスト + 全コンテナに反映されます）"
	return res, nil
}

// Share toggles the app's auth folder in the shared-dirs single source. on=true
// adds the (installed) app's auth path; on=false removes it. Sharing an app that
// is not installed is refused (従属ルール, AC-S41bdf2-2-2) — the server enforces
// what the greyed-out toggle expresses.
func (c *Controller) Share(ctx context.Context, id string, on bool) (int, error) {
	home, herr := os.UserHomeDir()
	if herr != nil {
		return 0, fmt.Errorf("apps: resolve home: %w", herr)
	}
	af, err := LoadApps(c.configDir)
	if err != nil {
		return 0, err
	}
	var rec *InstalledApp
	for i := range af.Installed {
		if af.Installed[i].ID == id {
			rec = &af.Installed[i]
			break
		}
	}
	if rec == nil {
		return 0, fmt.Errorf("apps: %q is not installed — install before sharing", id)
	}
	authPath := rec.AuthPath
	if authPath == "" {
		if e, ok := catalogByID(id); ok {
			authPath = e.AuthPath
		}
	}
	if strings.TrimSpace(authPath) == "" {
		return 0, fmt.Errorf("apps: %q has no auth folder to share", id)
	}
	abs, verr := config.ExpandSharedDir(authPath, home)
	if verr != nil {
		return 0, fmt.Errorf("apps: %w", verr)
	}

	cur := c.shared.CurrentSharedDirs()
	next := make([]string, 0, len(cur)+1)
	present := false
	for _, d := range cur {
		if d == abs {
			present = true
			if on {
				next = append(next, d) // keep
			}
			// on=false → drop
			continue
		}
		next = append(next, d)
	}
	if on && !present {
		next = append(next, abs)
	}
	return c.shared.ApplySharedDirs(ctx, next)
}

// Validate checks a user-defined nixpkgs package name (S41bdf2-1-5, no rebuild).
func (c *Controller) Validate(ctx context.Context, pkg string) ValidateResult {
	return ValidatePackage(ctx, pkg)
}

func displayOr(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}

func iconOr(s, fallback string) string {
	if strings.TrimSpace(s) != "" {
		return s
	}
	return fallback
}
