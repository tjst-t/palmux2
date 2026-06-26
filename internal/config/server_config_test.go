package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadServerConfig_MissingFiles(t *testing.T) {
	dir := t.TempDir()
	mc, sec, err := LoadServerConfig(dir)
	if err != nil {
		t.Fatalf("LoadServerConfig with no files: %v", err)
	}
	if mc.Server.Addr != "" || mc.Public.Domain != "" {
		t.Errorf("expected zero MasterConfig, got %+v", mc)
	}
	if sec.SSOSecret != "" || sec.BasicAuthHash != "" {
		t.Errorf("expected zero Secrets, got %+v", sec)
	}
}

func TestLoadServerConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	want := MasterConfig{
		Server: ServerSection{Addr: "127.0.0.1:9090", BasePath: "/p/", MaxConnections: 4, TmuxPrefix: "_palmux_", CaddyAdmin: "http://localhost:2019", ClaudeBin: "claude", ClaudeArgs: []string{"--foo"}},
		Public: PublicSection{Domain: "x.example.net", BasicAuthUser: "admin"},
	}
	if err := WriteMaster(dir, want); err != nil {
		t.Fatalf("WriteMaster: %v", err)
	}
	// Ensure master is 0600 (user-owned).
	info, _ := os.Stat(filepath.Join(dir, ConfigFileName))
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("config.toml perm = %o, want 600", perm)
	}
	got, _, err := LoadServerConfig(dir)
	if err != nil {
		t.Fatalf("LoadServerConfig: %v", err)
	}
	if got.Server.Addr != want.Server.Addr || got.Public.Domain != want.Public.Domain || len(got.Server.ClaudeArgs) != 1 {
		t.Errorf("round-trip mismatch: %+v", got)
	}
}

func TestLoadServerConfig_MalformedIsError(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte("this is = not valid toml ]["), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadServerConfig(dir); err == nil {
		t.Error("expected error for malformed config.toml, got nil")
	}
}

func TestSecrets_RoundTripAndPerm(t *testing.T) {
	dir := t.TempDir()
	if err := WriteSecrets(dir, Secrets{SSOSecret: "abc", BasicAuthHash: "$2a$hash", Token: "tok", CloudflareToken: "cf-xyz"}); err != nil {
		t.Fatalf("WriteSecrets: %v", err)
	}
	info, _ := os.Stat(filepath.Join(dir, SecretsFileName))
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("secrets.env perm = %o, want 600", perm)
	}
	_, sec, err := LoadServerConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	if sec.SSOSecret != "abc" || sec.BasicAuthHash != "$2a$hash" || sec.Token != "tok" || sec.CloudflareToken != "cf-xyz" {
		t.Errorf("secrets mismatch: %+v", sec)
	}
}

func TestMigrateLegacySecrets_Idempotent(t *testing.T) {
	dir := t.TempDir()
	// secrets.env already present → no migration.
	if err := WriteSecrets(dir, Secrets{SSOSecret: "existing"}); err != nil {
		t.Fatal(err)
	}
	migrated, err := MigrateLegacySecrets(dir)
	if err != nil {
		t.Fatal(err)
	}
	if migrated {
		t.Error("expected no migration when secrets.env already exists")
	}
}

func TestMasterExists(t *testing.T) {
	dir := t.TempDir()
	if MasterExists(dir) {
		t.Error("MasterExists should be false on empty dir")
	}
	_ = WriteMaster(dir, MasterConfig{})
	if !MasterExists(dir) {
		t.Error("MasterExists should be true after WriteMaster")
	}
}
