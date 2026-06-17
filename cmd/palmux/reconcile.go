package main

// Sa53137-4: `sudo palmux reconcile-system` — the single privileged verb.
//
// It reads the user-owned master config.toml, strictly validates the public
// domain, renders /etc/caddy/Caddyfile from a FIXED template (no directive
// injection possible — see internal/config/caddyfile.go), and reloads system
// Caddy. It takes NO free-form input: only --config-dir (where to find the
// master) and the master's typed [public].domain. The sudoers drop-in
// (install.sh) grants NOPASSWD for exactly this verb and nothing else.

import (
	"fmt"
	"os"
	"os/exec"
	"os/user"

	"github.com/tjst-t/palmux2/internal/config"
)

const caddyfilePath = "/etc/caddy/Caddyfile"

func runReconcileSystem(args []string) int {
	// Default to the invoking (sudo) user's config dir. Under sudo, HOME is
	// often /root, so honour SUDO_USER to find the real master.
	configDir := reconcileConfigDir()
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config-dir":
			if i+1 < len(args) {
				configDir = args[i+1]
				i++
			}
		case "-h", "--help":
			fmt.Println("Usage: sudo palmux reconcile-system [--config-dir DIR]")
			fmt.Println("Renders /etc/caddy/Caddyfile from the user-owned config.toml [public].domain")
			fmt.Println("and reloads system Caddy. The only privileged palmux verb; takes no free-form input.")
			return 0
		}
	}

	mc, sec, err := config.LoadServerConfig(configDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile-system: %v\n", err)
		return 1
	}
	domain := mc.Public.Domain
	if err := config.ValidateDomain(domain); err != nil {
		fmt.Fprintf(os.Stderr, "reconcile-system: refusing to render — %v\n", err)
		return 2
	}

	// requireAuth when basic auth / SSO is configured (a hash exists). We do
	// not read the hash value, only whether one is present.
	requireAuth := sec.BasicAuthHash != "" || mc.Public.BasicAuthUser != ""

	content, err := config.RenderCaddyfile(domain, "127.0.0.1:8080", os.Getenv("ACME_EMAIL"), requireAuth)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reconcile-system: %v\n", err)
		return 2
	}

	if os.Geteuid() != 0 {
		fmt.Fprintln(os.Stderr, "reconcile-system: must run as root (use: sudo palmux reconcile-system).")
		fmt.Fprintln(os.Stderr, "Rendered Caddyfile (dry preview):")
		fmt.Fprintln(os.Stderr, "----")
		fmt.Fprint(os.Stderr, content)
		fmt.Fprintln(os.Stderr, "----")
		return 1
	}

	tmp := caddyfilePath + ".palmux.tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "reconcile-system: write %s: %v\n", tmp, err)
		return 1
	}
	if err := os.Rename(tmp, caddyfilePath); err != nil {
		fmt.Fprintf(os.Stderr, "reconcile-system: install %s: %v\n", caddyfilePath, err)
		return 1
	}
	fmt.Printf("reconcile-system: wrote %s for domain %s\n", caddyfilePath, domain)

	cmd := exec.Command("systemctl", "reload", "caddy")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// reload can fail if Caddy is stopped; try restart as a fallback.
		fmt.Fprintf(os.Stderr, "reconcile-system: caddy reload failed (%v); trying restart...\n", err)
		r := exec.Command("systemctl", "restart", "caddy")
		r.Stdout = os.Stdout
		r.Stderr = os.Stderr
		if err := r.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "reconcile-system: caddy restart failed: %v\n", err)
			return 1
		}
	}
	fmt.Println("reconcile-system: caddy reloaded.")
	return 0
}

// reconcileConfigDir resolves the master config dir for the real user even when
// invoked via sudo (HOME=/root). Falls back to defaultConfigDir().
func reconcileConfigDir() string {
	if su := os.Getenv("SUDO_USER"); su != "" {
		if u, err := user.Lookup(su); err == nil && u.HomeDir != "" {
			return u.HomeDir + "/.config/palmux"
		}
	}
	return defaultConfigDir()
}
