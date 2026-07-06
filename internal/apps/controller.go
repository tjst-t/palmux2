package apps

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tjst-t/palmux2/internal/config"
)

// settleGrace bounds how long an install stays "installing" without ever having
// observed the rebuild unit report running. `systemctl start --no-block` returns
// BEFORE systemd flips the unit to ActiveState=activating, so the very first poll
// after Install can sample rebuildRunning=false while the rebuild is in fact about
// to start. We must not settle (and read the PRIOR run's terminal Result) in that
// window. A real `nixos-rebuild switch` runs far longer than this grace, so we
// virtually always observe running=true first; the grace only stops a pathological
// "never observed" case from pinning the card on "installing" forever.
const settleGrace = 30 * time.Second

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
	pendingID   string    // app id whose install kicked the current rebuild
	pendingAt   time.Time // when the pending rebuild was kicked (async-start race guard)
	pendingSeen bool      // whether we have observed the pending rebuild actually running
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
		// The effective auth path is the stored override (apps.json) if present,
		// otherwise the catalog default — so an authPath edit shadows the catalog
		// (AC-S41bdf2-4-2). GET /api/apps always returns the effective value.
		inst, installed := installedByID[e.ID]
		authPath := e.AuthPath
		if installed && strings.TrimSpace(inst.AuthPath) != "" {
			authPath = inst.AuthPath
		}
		v := AppView{
			ID: e.ID, Display: e.Display, Description: e.Description, Icon: e.Icon,
			Package: e.Package, AuthPath: authPath, Custom: custom,
			InstallBoundary: "rebuild", InstallReach: "host+containers",
			ShareBoundary: "hot", ShareReach: "containers",
		}
		v.Installed = installed
		if v.AuthPath != "" && home != "" {
			if abs, e2 := config.ExpandSharedDir(v.AuthPath, home); e2 == nil {
				v.Shared = sharedSet[abs]
			}
		}
		switch {
		case errs[e.ID] != "":
			v.State = "error"
			v.Error = errs[e.ID]
		case pending == e.ID:
			// A pending marker (set until settlePending clears it) means the install
			// rebuild is in flight — show "installing" even if THIS poll transiently
			// sampled rebuildRunning=false (the async-start race, see settlePending).
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

// settlePending resolves the pending install once its rebuild has genuinely
// finished, recording a failure if the run failed. It closes the async-start race
// (`systemctl start --no-block` returns before the unit is `activating`): while a
// pending install has NOT yet been observed running AND we are still within the
// grace window, we do NOT settle — otherwise the first post-Install poll would
// sample rebuildRunning=false and read the PRIOR run's terminal Result (clearing
// pending prematurely, or recording a spurious failure). Once we have observed the
// unit running at least once, a later rebuildRunning=false is a genuine finish.
func (c *Controller) settlePending(ctx context.Context, rebuildRunning bool) {
	if c.rebuild == nil {
		return
	}
	c.mu.Lock()
	pending := c.pendingID
	if pending == "" {
		c.mu.Unlock()
		return
	}
	if rebuildRunning {
		// Observed running → any later stop is a real finish. Don't settle yet.
		c.pendingSeen = true
		c.mu.Unlock()
		return
	}
	// rebuildRunning == false here. Hold "installing" if we have never seen the
	// unit run and the kick is still fresh (the start hasn't flipped state yet).
	if !c.pendingSeen && time.Since(c.pendingAt) < settleGrace {
		c.mu.Unlock()
		return
	}
	c.mu.Unlock()

	// Settle against the CURRENT run's terminal result.
	_, failed, err := c.rebuild.RebuildStatus(ctx)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pendingID != pending { // changed under us (new install / uninstall)
		return
	}
	c.pendingID = ""
	c.pendingSeen = false
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
		c.pendingSeen = false
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
		c.pendingAt = time.Now()
		c.pendingSeen = false
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

// SetAuthPath updates an installed app's auth folder path, persisting it as an
// override in apps.json (AC-S41bdf2-4-2). For a catalog app the override shadows the
// catalog default; for a custom app it just replaces the stored value. The path is
// $HOME-scope validated on the server (reuse Sd44947's config.ExpandSharedDir — any
// path outside $HOME is rejected, priority_rule 5/6). If the app's OLD auth folder is
// currently shared, the share follows the edit: the old path's share is removed and
// the new path's share is added in the SAME shared_dirs single source so the card and
// the generic 共有フォルダ list never diverge (priority_rule 6). Returns the number of
// running containers refreshed (0 when the app was not shared). Editing is dependent
// on install (mirrors Share's 従属 rule): an un-installed app has no override record.
func (c *Controller) SetAuthPath(ctx context.Context, id, authPath string) (string, int, error) {
	if !ValidAppID(id) {
		return "", 0, fmt.Errorf("apps: invalid app id %q", id)
	}
	authPath = strings.TrimSpace(authPath)
	if authPath == "" {
		return "", 0, fmt.Errorf("apps: 認証フォルダのパスが空です")
	}
	home, herr := os.UserHomeDir()
	if herr != nil {
		return "", 0, fmt.Errorf("apps: resolve home: %w", herr)
	}
	// $HOME-scope validate (out-of-$HOME → error → 400 at the handler).
	newAbs, verr := config.ExpandSharedDir(authPath, home)
	if verr != nil {
		return "", 0, fmt.Errorf("apps: %w", verr)
	}

	af, err := LoadApps(c.configDir)
	if err != nil {
		return "", 0, err
	}
	var rec *InstalledApp
	for i := range af.Installed {
		if af.Installed[i].ID == id {
			rec = &af.Installed[i]
			break
		}
	}
	if rec == nil {
		return "", 0, fmt.Errorf("apps: %q はインストールされていません — 認証フォルダの編集はインストール後に可能です", id)
	}

	// Resolve the OLD effective auth path (override or catalog) and whether it is
	// currently shared, so we can move the share to the new path.
	oldEff := rec.AuthPath
	if strings.TrimSpace(oldEff) == "" {
		if e, ok := catalogByID(id); ok {
			oldEff = e.AuthPath
		}
	}
	wasShared := false
	var oldAbs string
	if strings.TrimSpace(oldEff) != "" {
		if a, e := config.ExpandSharedDir(oldEff, home); e == nil {
			oldAbs = a
			for _, d := range c.shared.CurrentSharedDirs() {
				if d == oldAbs {
					wasShared = true
					break
				}
			}
		}
	}

	// Persist the override.
	rec.AuthPath = authPath
	if err := WriteApps(c.configDir, af); err != nil {
		return "", 0, err
	}

	// Follow the share to the new path when the old one was shared.
	if wasShared {
		cur := c.shared.CurrentSharedDirs()
		next := make([]string, 0, len(cur)+1)
		hasNew := false
		for _, d := range cur {
			if d == oldAbs {
				continue // drop the old share
			}
			if d == newAbs {
				hasNew = true
			}
			next = append(next, d)
		}
		if !hasNew {
			next = append(next, newAbs)
		}
		n, aerr := c.shared.ApplySharedDirs(ctx, next)
		if aerr != nil {
			return "", 0, fmt.Errorf("apps: move share to new auth path: %w", aerr)
		}
		return authPath, n, nil
	}
	return authPath, 0, nil
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
