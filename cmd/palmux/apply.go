package main

// Sa53137-3: `palmux apply` — re-read the master config.toml, classify the diff
// against the running server's applied config, and apply it: hot changes
// in-process (via the running server's /api/deploy/apply endpoint), restart
// changes via `systemctl --user restart palmux2`, and root/Caddy changes by
// printing the privileged-path guidance (Story 4).
//
// The running server is the authority on "what config is currently live", so
// apply drives the same /api/deploy/apply endpoint the GUI deploy tab uses. The
// endpoint returns the classified diff; apply then fires the restart locally
// when needed (the server cannot restart itself cleanly mid-request).

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"time"

	"github.com/tjst-t/palmux2/internal/selfupdate"
)

// applyResult mirrors the JSON returned by POST /api/deploy/apply.
type applyResult struct {
	Changes []struct {
		Field string `json:"field"`
		Class string `json:"class"`
	} `json:"changes"`
	HotApplied       bool   `json:"hotApplied"`
	NeedRestart      bool   `json:"needRestart"`
	NeedPrivilege    bool   `json:"needPrivilege"`
	WorkspaceApplied bool   `json:"workspaceApplied"`
	Message          string `json:"message"`
}

func runApply(args []string) int {
	configDir := defaultConfigDir()
	var dryRun bool
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--config-dir":
			if i+1 < len(args) {
				configDir = args[i+1]
				i++
			}
		case "--dry-run":
			dryRun = true
		case "-h", "--help":
			fmt.Println("Usage: palmux apply [--config-dir DIR] [--dry-run]")
			fmt.Println("Re-reads config.toml and applies the diff (hot / restart / privileged).")
			return 0
		}
	}

	// Resolve the running server's local URL from the env file palmux writes.
	baseURL, tokenHdr := localServerURL(configDir)
	if baseURL == "" {
		fmt.Fprintln(os.Stderr, "apply: cannot locate a running palmux server (no env.<port> in config dir). Is palmux running?")
		return 1
	}

	url := baseURL + "/api/deploy/apply"
	body, _ := json.Marshal(map[string]any{"dryRun": dryRun})
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply: %v\n", err)
		return 1
	}
	req.Header.Set("Content-Type", "application/json")
	if tokenHdr != "" {
		req.Header.Set("Authorization", "Bearer "+tokenHdr)
	}
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		fmt.Fprintf(os.Stderr, "apply: contacting server: %v\n", err)
		return 1
	}
	defer resp.Body.Close()
	rb, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintf(os.Stderr, "apply: server returned %d: %s\n", resp.StatusCode, string(rb))
		return 1
	}
	var res applyResult
	if err := json.Unmarshal(rb, &res); err != nil {
		fmt.Fprintf(os.Stderr, "apply: bad server response: %v\n", err)
		return 1
	}

	if len(res.Changes) == 0 {
		fmt.Println("apply: no changes — config already matches the running server.")
		return 0
	}
	fmt.Println("apply: classified changes:")
	for _, c := range res.Changes {
		fmt.Printf("  - %-26s %s\n", c.Field, c.Class)
	}
	if res.HotApplied {
		fmt.Println("apply: hot changes applied in-process.")
	}
	if res.WorkspaceApplied {
		// Sd44947: shared-folder changes rewrite the palmux-shared incus profile
		// in-process and incus live-propagates them to running containers.
		fmt.Println("apply: " + res.Message)
	}
	if res.NeedPrivilege {
		fmt.Println()
		fmt.Println("apply: some changes require a privileged system operation (public domain / TLS / Cloudflare token).")
		if selfupdate.IsNixOSHost() {
			// NixOS appliance: Caddy is declarative; reconcile-system is the wrong
			// (Ubuntu) verb. The switch is kickable without a password via the
			// polkit-authorized palmux-rebuild unit, or from the GUI deploy panel.
			fmt.Println("       NixOS appliance: apply with a generation switch (atomic + rollback):")
			fmt.Println("         systemctl start palmux-rebuild.service        # no sudo — polkit-authorized for the palmux user")
			fmt.Println("       or click “適用 (nixos-rebuild)” in the GUI deploy panel, or run as root:")
			fmt.Println("         cd /persist/palmux/nixos && nixos-rebuild switch --flake .#appliance")
		} else {
			fmt.Println("       Run:  sudo palmux reconcile-system    (renders /etc/caddy/Caddyfile + reloads caddy)")
			fmt.Println("       or re-run scripts/install.sh with the new DOMAIN / CLOUDFLARE_API_TOKEN.")
		}
	}
	if res.NeedRestart {
		if dryRun {
			fmt.Println("apply: --dry-run — would run `systemctl --user restart palmux2` for restart-class changes.")
			return 0
		}
		fmt.Println("apply: restart-class changes detected — running `systemctl --user restart palmux2`...")
		cmd := exec.Command("systemctl", "--user", "restart", "palmux2")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "apply: restart failed: %v\n", err)
			fmt.Fprintln(os.Stderr, "       run it manually: systemctl --user restart palmux2")
			return 1
		}
		fmt.Println("apply: palmux2 restarted.")
	}
	return 0
}

// localServerURL reads the env.<port> file palmux writes under configDir to
// find the loopback URL + token of the running server.
func localServerURL(configDir string) (string, string) {
	entries, err := os.ReadDir(configDir)
	if err != nil {
		return "", ""
	}
	for _, e := range entries {
		name := e.Name()
		if len(name) < 5 || name[:4] != "env." {
			continue
		}
		f, err := os.Open(configDir + "/" + name)
		if err != nil {
			continue
		}
		kv := scanSimpleEnv(f)
		f.Close()
		if u := kv["PALMUX_URL"]; u != "" {
			return u, kv["PALMUX_TOKEN"]
		}
	}
	return "", ""
}

// scanSimpleEnv parses a KEY=VALUE file into a map (local helper to avoid an
// internal import in the CLI binary's apply path).
func scanSimpleEnv(f *os.File) map[string]string {
	out := map[string]string{}
	dec := bytes.Split(readAllOrEmpty(f), []byte("\n"))
	for _, line := range dec {
		s := bytes.TrimSpace(line)
		if len(s) == 0 || s[0] == '#' {
			continue
		}
		eq := bytes.IndexByte(s, '=')
		if eq < 0 {
			continue
		}
		out[string(bytes.TrimSpace(s[:eq]))] = string(bytes.TrimSpace(s[eq+1:]))
	}
	return out
}

func readAllOrEmpty(f *os.File) []byte {
	b, err := io.ReadAll(f)
	if err != nil {
		return nil
	}
	return b
}
