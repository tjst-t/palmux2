package netns

import (
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"text/template"
)

// CaddyConfig holds the configuration for Caddy integration.
type CaddyConfig struct {
	Enabled      bool   `json:"enabled"`
	FQDNTemplate string `json:"fqdnTemplate"` // e.g. "{repo}-{branch}-{port}.example.com"
	ConfigPath   string `json:"configPath"`   // path to Caddy snippet file (palmux writes here)
	ReloadCmd    string `json:"reloadCmd"`    // command to reload Caddy; default "caddy reload --config <caddyfile>"
}

// CaddyIntegration manages Caddy snippet generation and reload.
type CaddyIntegration struct {
	mu     sync.Mutex
	cfg    CaddyConfig
	routes map[int]string // hostPort → fqdn
	logger *slog.Logger
}

// NewCaddyIntegration creates a new CaddyIntegration.
func NewCaddyIntegration(cfg CaddyConfig, logger *slog.Logger) *CaddyIntegration {
	if logger == nil {
		logger = slog.Default()
	}
	return &CaddyIntegration{
		cfg:    cfg,
		routes: map[int]string{},
		logger: logger,
	}
}

// Enabled returns true when Caddy integration is active.
func (c *CaddyIntegration) Enabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cfg.Enabled
}

// UpdateConfig replaces the Caddy configuration. Called when settings are PATCHed.
func (c *CaddyIntegration) UpdateConfig(cfg CaddyConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cfg = cfg
}

// AddRoute generates an FQDN from the template, adds a reverse_proxy directive
// to the snippet file, reloads Caddy, and returns the public URL.
func (c *CaddyIntegration) AddRoute(ws WorktreeState, pm PortMapping) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	fqdn, err := c.expandFQDN(ws, pm)
	if err != nil {
		return "", err
	}

	c.routes[pm.HostPort] = fqdn
	if err := c.writeSnippet(); err != nil {
		return "", err
	}
	if err := c.reload(); err != nil {
		c.logger.Warn("caddy: reload failed", "err", err)
		// Don't fail expose — route is in the snippet but Caddy hasn't reloaded yet.
	}
	return "https://" + fqdn, nil
}

// RemoveRoute removes the snippet entry for the given hostPort.
func (c *CaddyIntegration) RemoveRoute(hostPort int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.routes, hostPort)
	return c.writeSnippet()
}

// Reload reloads Caddy after external changes.
func (c *CaddyIntegration) Reload() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.reload()
}

func (c *CaddyIntegration) expandFQDN(ws WorktreeState, pm PortMapping) (string, error) {
	tmpl := c.cfg.FQDNTemplate
	if tmpl == "" {
		return "", fmt.Errorf("caddy: fqdnTemplate is empty")
	}

	// Extract repo/branch from worktreeID (format: repoSlug--branchSlug).
	repo, branch := splitWorktreeID(ws.WorktreeID)

	t, err := template.New("fqdn").Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("caddy: parse fqdnTemplate: %w", err)
	}
	var sb strings.Builder
	if err := t.Execute(&sb, map[string]any{
		"repo":     repo,
		"branch":   branch,
		"port":     pm.InternalPort,
		"hostPort": pm.HostPort,
	}); err != nil {
		return "", fmt.Errorf("caddy: expand fqdnTemplate: %w", err)
	}
	return sb.String(), nil
}

func splitWorktreeID(id string) (repo, branch string) {
	// worktreeID format: repoOwner--repoName--hash4_branchName--hash4
	// We use a simple heuristic: repo = first segment before the last '--'-separated hash
	parts := strings.SplitN(id, "_", 2)
	if len(parts) == 2 {
		return sanitizeLabel(parts[0]), sanitizeLabel(parts[1])
	}
	return sanitizeLabel(id), "main"
}

func sanitizeLabel(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else if r == '_' || r == '/' {
			b.WriteRune('-')
		}
	}
	result := strings.Trim(b.String(), "-")
	if len(result) > 40 {
		result = result[:40]
	}
	return result
}

func (c *CaddyIntegration) writeSnippet() error {
	if c.cfg.ConfigPath == "" {
		return fmt.Errorf("caddy: configPath is empty")
	}
	if err := os.MkdirAll(filepath.Dir(c.cfg.ConfigPath), 0o755); err != nil {
		return fmt.Errorf("caddy: mkdir snippet dir: %w", err)
	}

	var sb strings.Builder
	sb.WriteString("# Palmux auto-generated — do not edit manually\n")
	for hostPort, fqdn := range c.routes {
		fmt.Fprintf(&sb, "\n%s {\n\treverse_proxy localhost:%d\n}\n", fqdn, hostPort)
	}

	return os.WriteFile(c.cfg.ConfigPath, []byte(sb.String()), 0o644)
}

func (c *CaddyIntegration) reload() error {
	if c.cfg.ReloadCmd == "" {
		return fmt.Errorf("caddy: reloadCmd is empty")
	}
	cmd := exec.Command("sh", "-c", c.cfg.ReloadCmd)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("caddy reload: %s: %w", out, err)
	}
	c.logger.Info("caddy: reloaded", "cmd", c.cfg.ReloadCmd)
	return nil
}

// CheckCaddy returns nil if caddy binary is available, or an error otherwise.
func CheckCaddy() error {
	_, err := exec.LookPath("caddy")
	if err != nil {
		return fmt.Errorf("caddy binary not found in PATH")
	}
	return nil
}
