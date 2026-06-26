// Package deploy implements the Sa53137 unified-config deploy plane: it holds
// the master config the running server was launched with ("applied"), serves it
// (with secrets masked) to the GUI deploy tab, and applies edits — hot ones
// in-process, restart-class ones by signalling "needs restart", root-class ones
// by signalling "needs privileged reconcile".
package deploy

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/tjst-t/palmux2/internal/config"
)

// HotApplier is the subset of provider-refresh capability the deploy controller
// needs to apply hot changes without a restart. Implemented in cmd/palmux
// wiring over the incus registry + claude managers. A nil HotApplier means the
// hot path degrades to "needs restart".
type HotApplier interface {
	// SetCaddyAdmin updates the Caddy admin endpoint used for per-port route
	// injection (hot).
	SetCaddyAdmin(addr string)
	// SetBasicAuthDefaults refreshes the per-port basic-auth / route defaults
	// (hot — routes self-heal via the 10s resync loop).
	SetBasicAuthDefaults(user, hash string)
	// SetClaudeBin / SetClaudeArgs swap the claude binary / extra args used for
	// freshly-spawned claude tabs (hot — existing daemons keep their binary).
	SetClaudeBin(bin string)
	SetClaudeArgs(args []string)
}

// Controller is held by the server. It is the single source of truth for the
// running server's applied master config and the presence of secrets.
type Controller struct {
	mu        sync.RWMutex
	configDir string
	applied   config.MasterConfig // what the server is currently running with
	// secret presence (never the values).
	hasSSOSecret bool
	hasBasicHash bool
	hasToken     bool
	hasCFToken   bool
	hot          HotApplier
	logger       *slog.Logger
	// nixOSHost is true when palmux2 runs on a NixOS appliance (Sb14caa). On NixOS
	// the privileged "apply public domain / TLS" step is NOT `palmux reconcile-system`
	// (an Ubuntu/install.sh verb that the non-root, password-less, non-wheel palmux
	// user cannot even run) — it is `nixos-rebuild switch`, which the GUI can KICK via
	// the palmux-rebuild systemd unit (POST /api/deploy/rebuild). Set from main.go.
	nixOSHost bool
}

// SetNixOSHost records whether this is a NixOS appliance, switching the privileged
// apply path from `reconcile-system` to the GUI-kickable nixos-rebuild unit.
func (c *Controller) SetNixOSHost(b bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nixOSHost = b
}

// New constructs a Controller seeded with the config the server launched with
// and the presence of its secrets.
func New(configDir string, applied config.MasterConfig, sec SecretPresence, hot HotApplier, logger *slog.Logger) *Controller {
	if logger == nil {
		logger = slog.Default()
	}
	return &Controller{
		configDir:    configDir,
		applied:      applied,
		hasSSOSecret: sec.SSOSecret,
		hasBasicHash: sec.BasicHash,
		hasToken:     sec.Token,
		hasCFToken:   sec.CloudflareToken,
		hot:          hot,
		logger:       logger,
	}
}

// SecretPresence carries booleans only — the controller never sees secret
// values except through the explicit write-only RotateSecrets path.
type SecretPresence struct {
	SSOSecret       bool
	BasicHash       bool
	Token           bool
	CloudflareToken bool
}

// View is the masked deploy state returned to the GUI. Secrets are reported as
// presence booleans, never values.
type View struct {
	Server  config.ServerSection `json:"server"`
	Public  config.PublicSection `json:"public"`
	Secrets struct {
		HasSSOSecret       bool `json:"hasSsoSecret"`
		HasBasicAuthHash   bool `json:"hasBasicAuthHash"`
		HasToken           bool `json:"hasToken"`
		HasCloudflareToken bool `json:"hasCloudflareToken"`
	} `json:"secrets"`
	// Configured is false on a fresh, unconfigured install (no config.toml
	// written and no public domain). Drives the onboarding wizard (Sa53137-3-4).
	Configured bool `json:"configured"`
	// NixOSHost is true on a NixOS appliance. The GUI uses it to swap the privileged
	// apply path: on NixOS the wizard/deploy panel offer a "nixos-rebuild で適用"
	// button (POST /api/deploy/rebuild) instead of the `sudo palmux reconcile-system`
	// instruction (which is an Ubuntu-only verb and unusable as the palmux user).
	NixOSHost bool `json:"nixOSHost"`
}

// CurrentView returns the masked deploy config. It reflects the on-disk master
// (the editable source of truth) when present, overlaid on the running-server
// snapshot — so a pending root/restart change (e.g. a domain set via the GUI
// that needs reconcile) is shown to the operator while still being flagged as
// needing an out-of-band step. Fields absent from the file fall back to the
// running snapshot.
func (c *Controller) CurrentView() View {
	c.mu.RLock()
	defer c.mu.RUnlock()
	var v View
	v.Server = c.applied.Server
	v.Public = c.applied.Public
	// Overlay the on-disk master so the GUI shows what the user last saved.
	if mc, _, err := config.LoadServerConfig(c.configDir); err == nil {
		overlayServer(&v.Server, mc.Server)
		overlayPublic(&v.Public, mc.Public)
	}
	v.Secrets.HasSSOSecret = c.hasSSOSecret
	v.Secrets.HasBasicAuthHash = c.hasBasicHash
	v.Secrets.HasToken = c.hasToken
	v.Secrets.HasCloudflareToken = c.hasCFToken
	v.Configured = c.isConfiguredLocked()
	v.NixOSHost = c.nixOSHost
	return v
}

// overlayServer copies non-empty fields from file over dst (the file is the
// editable source of truth; empty file fields keep the running snapshot value).
func overlayServer(dst *config.ServerSection, file config.ServerSection) {
	if file.Addr != "" {
		dst.Addr = file.Addr
	}
	if file.BasePath != "" {
		dst.BasePath = file.BasePath
	}
	if file.MaxConnections != 0 {
		dst.MaxConnections = file.MaxConnections
	}
	if file.TmuxPrefix != "" {
		dst.TmuxPrefix = file.TmuxPrefix
	}
	if file.CaddyAdmin != "" {
		dst.CaddyAdmin = file.CaddyAdmin
	}
	if file.ClaudeBin != "" {
		dst.ClaudeBin = file.ClaudeBin
	}
	if file.ClaudeArgs != nil {
		dst.ClaudeArgs = file.ClaudeArgs
	}
}

// overlayPublic mirrors overlayServer for the [public] section.
func overlayPublic(dst *config.PublicSection, file config.PublicSection) {
	if file.Domain != "" {
		dst.Domain = file.Domain
	}
	if file.BasicAuthUser != "" {
		dst.BasicAuthUser = file.BasicAuthUser
	}
}

// isConfiguredLocked reports whether the install has been configured (either via
// install.sh or a prior GUI onboarding). Heuristic: a config.toml exists on disk
// OR a public domain is set. The onboarding wizard is shown only when this is
// false (a fresh install with no master and no public domain). NOTE: the server
// addr is NOT part of this check — it always resolves to a non-empty default, so
// it would make every install look "configured" and suppress the wizard.
func (c *Controller) isConfiguredLocked() bool {
	if config.MasterExists(c.configDir) {
		return true
	}
	return c.applied.Public.Domain != ""
}

// ApplyOutcome is the classified result of an apply.
type ApplyOutcome struct {
	Changes       []config.FieldChange `json:"changes"`
	HotApplied    bool                 `json:"hotApplied"`
	NeedRestart   bool                 `json:"needRestart"`
	NeedPrivilege bool                 `json:"needPrivilege"`
	Message       string               `json:"message"`
}

// SaveAndClassify persists the new master to disk, classifies the diff against
// the applied config, applies hot changes in-process, and reports which classes
// remain (restart / privilege). The caller (CLI / GUI) performs the actual
// restart; the server cannot cleanly restart itself mid-request.
//
// dryRun skips both the disk write and the hot apply — it only classifies.
func (c *Controller) SaveAndClassify(ctx context.Context, neu config.MasterConfig, dryRun bool) (ApplyOutcome, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	changes := config.DiffMaster(c.applied, neu)
	var out ApplyOutcome
	out.Changes = changes
	if len(changes) == 0 {
		out.Message = "no changes"
		return out, nil
	}

	if !dryRun {
		if err := config.WriteMaster(c.configDir, neu); err != nil {
			return out, fmt.Errorf("deploy: persist master: %w", err)
		}
	}

	for _, ch := range changes {
		switch ch.Class {
		case config.ClassHot:
			if !dryRun {
				c.applyHotLocked(ch, neu)
				// Only a field that was actually hot-applied advances the
				// in-memory "applied" baseline. Restart/root fields take effect
				// out-of-band (restart / reconcile), so their baseline must NOT
				// advance here — otherwise a second apply before the restart
				// would diff "new vs new" → "no changes" and silently hide that
				// a restart is still pending.
				c.advanceAppliedFieldLocked(ch.Field, neu)
			}
			out.HotApplied = true
		case config.ClassRestart:
			out.NeedRestart = true
		case config.ClassRoot:
			out.NeedPrivilege = true
		}
	}

	switch {
	case out.NeedPrivilege && c.nixOSHost:
		// On a NixOS appliance reconcile-system is the wrong (Ubuntu) verb and the
		// palmux user can't sudo. The privileged apply is `nixos-rebuild switch`,
		// kickable from the GUI via POST /api/deploy/rebuild (palmux-rebuild.service).
		out.Message = "public domain / TLS changes apply via `nixos-rebuild switch` — use “適用 (nixos-rebuild)” or POST /api/deploy/rebuild"
	case out.NeedPrivilege:
		out.Message = "some changes require `sudo palmux reconcile-system` (public domain / TLS)"
	case out.NeedRestart:
		out.Message = "restart-class changes require `systemctl --user restart palmux2`"
	default:
		out.Message = "applied in-process"
	}
	return out, nil
}

func (c *Controller) applyHotLocked(ch config.FieldChange, neu config.MasterConfig) {
	if c.hot == nil {
		return
	}
	switch ch.Field {
	case "server.caddy_admin":
		c.hot.SetCaddyAdmin(neu.Server.CaddyAdmin)
	case "server.claude_bin":
		c.hot.SetClaudeBin(neu.Server.ClaudeBin)
	case "server.claude_args":
		c.hot.SetClaudeArgs(neu.Server.ClaudeArgs)
	}
}

// advanceAppliedFieldLocked moves the in-memory applied baseline forward for a
// single field that was just hot-applied. Restart/root fields are intentionally
// excluded — their baseline only advances after the out-of-band step (a server
// restart re-reads the master from scratch, and reconcile re-renders Caddy).
func (c *Controller) advanceAppliedFieldLocked(field string, neu config.MasterConfig) {
	switch field {
	case "server.caddy_admin":
		c.applied.Server.CaddyAdmin = neu.Server.CaddyAdmin
	case "server.claude_bin":
		c.applied.Server.ClaudeBin = neu.Server.ClaudeBin
	case "server.claude_args":
		c.applied.Server.ClaudeArgs = neu.Server.ClaudeArgs
	}
}

// RotateSecrets writes new secret values (write-only — the GUI never reads them
// back). Empty fields are left unchanged. SSO secret / basic-auth password are
// restart-class; the caller restarts. Returns which secret classes changed.
func (c *Controller) RotateSecrets(s config.Secrets, bcryptHashFn func(string) (string, error), plaintextPassword string) (changedRestart bool, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Load existing to merge (preserve unset fields).
	_, existing, lerr := config.LoadServerConfig(c.configDir)
	if lerr != nil {
		return false, fmt.Errorf("deploy: load secrets: %w", lerr)
	}
	merged := existing
	if s.SSOSecret != "" {
		merged.SSOSecret = s.SSOSecret
		c.hasSSOSecret = true
		changedRestart = true
	}
	if plaintextPassword != "" {
		hash, herr := bcryptHashFn(plaintextPassword)
		if herr != nil {
			return false, fmt.Errorf("deploy: hash password: %w", herr)
		}
		merged.BasicAuthHash = hash
		c.hasBasicHash = true
		changedRestart = true
	}
	if s.Token != "" {
		merged.Token = s.Token
		c.hasToken = true
		changedRestart = true
	}
	if s.CloudflareToken != "" {
		merged.CloudflareToken = s.CloudflareToken
		c.hasCFToken = true
		changedRestart = true
	}
	if err := config.WriteSecrets(c.configDir, merged); err != nil {
		return changedRestart, fmt.Errorf("deploy: write secrets: %w", err)
	}
	return changedRestart, nil
}
