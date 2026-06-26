package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Sa53137-2: the unified master configuration plane.
//
// palmux's server/deploy settings used to be scattered across CLI flags,
// /etc/palmux/runtime.env, /etc/palmux/flake.nix and install.sh arguments
// (design doc §現状マップ). This file introduces a single non-secret master
// (~/.config/palmux/config.toml) plus a user-owned secrets file
// (~/.config/palmux/secrets.env, 0600). The resolution chain in
// cmd/palmux/main.go layers these as: flag > env > file > default.

// ConfigFileName is the master (non-secret) config file basename.
const ConfigFileName = "config.toml"

// SecretsFileName is the user-owned secrets file basename (0600).
const SecretsFileName = "secrets.env"

// LegacyRuntimeEnvPath is the old root-owned secrets carrier (install.sh,
// pre-Sa53137). It is migrated once into SecretsFileName on first run.
const LegacyRuntimeEnvPath = "/etc/palmux/runtime.env"

// ServerSection mirrors [server] in config.toml. Empty/zero fields mean
// "unset" and fall through to flag/env/default resolution. The json tags match
// the TOML keys so the GUI deploy tab sees snake_case fields over the wire.
type ServerSection struct {
	Addr           string   `toml:"addr" json:"addr"`
	BasePath       string   `toml:"base_path" json:"base_path"`
	MaxConnections int      `toml:"max_connections" json:"max_connections"`
	TmuxPrefix     string   `toml:"tmux_prefix" json:"tmux_prefix"`
	CaddyAdmin     string   `toml:"caddy_admin" json:"caddy_admin"`
	ClaudeBin      string   `toml:"claude_bin" json:"claude_bin"`
	ClaudeArgs     []string `toml:"claude_args" json:"claude_args"`
}

// PublicSection mirrors [public] in config.toml. Domain empty disables the
// public/SSO features.
type PublicSection struct {
	Domain        string `toml:"domain" json:"domain"`
	BasicAuthUser string `toml:"basic_auth_user" json:"basic_auth_user"`
}

// MasterConfig is the parsed config.toml.
type MasterConfig struct {
	Server ServerSection `toml:"server"`
	Public PublicSection `toml:"public"`
}

// Secrets holds the values from secrets.env (never serialised back to the
// non-secret master; the GUI/API never returns these values).
type Secrets struct {
	SSOSecret       string // PALMUX_SSO_SECRET
	BasicAuthHash   string // BASIC_AUTH_HASH (bcrypt)
	Token           string // PALMUX_TOKEN (--token equivalent, optional)
	CloudflareToken string // CLOUDFLARE_API_TOKEN (Caddy DNS-01 wildcard cert)
}

// LoadServerConfig reads config.toml and secrets.env under dir. Both files are
// optional: a missing file yields a zero-value section (callers layer flag/env/
// default on top). A malformed config.toml is a hard error so a typo doesn't
// silently fall back to defaults; a malformed secrets.env line is skipped.
func LoadServerConfig(dir string) (MasterConfig, Secrets, error) {
	var mc MasterConfig
	cfgPath := filepath.Join(dir, ConfigFileName)
	if b, err := os.ReadFile(cfgPath); err == nil {
		if err := toml.Unmarshal(b, &mc); err != nil {
			return MasterConfig{}, Secrets{}, fmt.Errorf("config: parse %s: %w", cfgPath, err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return MasterConfig{}, Secrets{}, fmt.Errorf("config: read %s: %w", cfgPath, err)
	}

	sec, err := loadSecretsFile(filepath.Join(dir, SecretsFileName))
	if err != nil {
		return MasterConfig{}, Secrets{}, err
	}
	return mc, sec, nil
}

// loadSecretsFile parses a KEY=VALUE env file into Secrets. Missing file → zero
// Secrets, no error. Lines without '=' or starting with '#' are skipped.
func loadSecretsFile(path string) (Secrets, error) {
	var s Secrets
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return s, fmt.Errorf("config: read %s: %w", path, err)
	}
	defer f.Close()
	for k, v := range scanEnvFile(f) {
		switch k {
		case "PALMUX_SSO_SECRET":
			s.SSOSecret = v
		case "BASIC_AUTH_HASH":
			s.BasicAuthHash = v
		case "PALMUX_TOKEN":
			s.Token = v
		case "CLOUDFLARE_API_TOKEN":
			s.CloudflareToken = v
		}
	}
	return s, nil
}

// scanEnvFile returns a map of KEY=VALUE pairs from a simple env file. Values
// may be optionally double-quoted. Comments (#) and blank lines are ignored.
func scanEnvFile(r *os.File) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		val = strings.Trim(val, `"`)
		if key != "" {
			out[key] = val
		}
	}
	return out
}

// MasterExists reports whether config.toml is present under dir. Used by the
// onboarding heuristic (Sa53137-3-4): an install configured via install.sh
// writes config.toml, so its presence suppresses the first-launch wizard.
func MasterExists(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, ConfigFileName)); err == nil {
		return true
	}
	return false
}

// MigrateLegacySecrets performs the one-time copy from the root-owned legacy
// /etc/palmux/runtime.env into the user-owned secrets.env when the latter does
// not yet exist and the former is readable (Sa53137-2-3, PD-5). It returns true
// when a migration was performed. Idempotent: once secrets.env exists this is a
// no-op. PALMUX_PUBLIC_DOMAIN / BASIC_AUTH_USER from runtime.env are NOT secret
// and are not migrated here — install.sh writes them into config.toml [public].
func MigrateLegacySecrets(dir string) (bool, error) {
	dst := filepath.Join(dir, SecretsFileName)
	if _, err := os.Stat(dst); err == nil {
		return false, nil // already present, do not clobber
	}
	f, err := os.Open(LegacyRuntimeEnvPath)
	if err != nil {
		return false, nil // legacy file absent/unreadable → nothing to migrate
	}
	defer f.Close()
	kv := scanEnvFile(f)
	var b strings.Builder
	b.WriteString("# palmux secrets (user-owned, 0600). Migrated from " + LegacyRuntimeEnvPath + ".\n")
	wrote := false
	for _, k := range []string{"PALMUX_SSO_SECRET", "BASIC_AUTH_HASH", "PALMUX_TOKEN", "CLOUDFLARE_API_TOKEN"} {
		if v, ok := kv[k]; ok && v != "" {
			fmt.Fprintf(&b, "%s=%s\n", k, v)
			wrote = true
		}
	}
	if !wrote {
		return false, nil
	}
	if err := os.WriteFile(dst, []byte(b.String()), 0o600); err != nil {
		return false, fmt.Errorf("config: write %s: %w", dst, err)
	}
	return true, nil
}

// WriteSecrets persists Secrets to secrets.env (0600), preserving any keys not
// managed here is out of scope — palmux owns this file. Empty fields are
// omitted so a partial update (e.g. only rotating the SSO secret) does not blank
// the others; callers should load → mutate → WriteMergedSecrets instead when
// preserving is required. This raw form is used by `palmux apply`/the write-only
// secrets endpoint after merging.
func WriteSecrets(dir string, s Secrets) error {
	var b strings.Builder
	b.WriteString("# palmux secrets (user-owned, 0600). Managed by palmux.\n")
	if s.SSOSecret != "" {
		fmt.Fprintf(&b, "PALMUX_SSO_SECRET=%s\n", s.SSOSecret)
	}
	if s.BasicAuthHash != "" {
		fmt.Fprintf(&b, "BASIC_AUTH_HASH=%s\n", s.BasicAuthHash)
	}
	if s.Token != "" {
		fmt.Fprintf(&b, "PALMUX_TOKEN=%s\n", s.Token)
	}
	if s.CloudflareToken != "" {
		fmt.Fprintf(&b, "CLOUDFLARE_API_TOKEN=%s\n", s.CloudflareToken)
	}
	dst := filepath.Join(dir, SecretsFileName)
	tmp := dst + ".tmp"
	if err := os.WriteFile(tmp, []byte(b.String()), 0o600); err != nil {
		return fmt.Errorf("config: write %s: %w", tmp, err)
	}
	return os.Rename(tmp, dst)
}

// WriteMaster persists the non-secret master config.toml (0600 — it is
// user-owned per design doc §セキュリティ).
func WriteMaster(dir string, mc MasterConfig) error {
	dst := filepath.Join(dir, ConfigFileName)
	tmp := dst + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return fmt.Errorf("config: create %s: %w", tmp, err)
	}
	if err := toml.NewEncoder(f).Encode(mc); err != nil {
		f.Close()
		return fmt.Errorf("config: encode %s: %w", tmp, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("config: close %s: %w", tmp, err)
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return fmt.Errorf("config: chmod %s: %w", tmp, err)
	}
	return os.Rename(tmp, dst)
}
