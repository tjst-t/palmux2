package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSettingsWatch_ReloadsOnDiskEdit(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan Settings, 4)
	w, err := s.WatchFile(func(updated Settings) { got <- updated }, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	// Write a valid settings.json directly (simulating an external editor).
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"maxClaudeTabsPerBranch": 7, "branchSortOrder": "activity"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	select {
	case updated := <-got:
		if updated.MaxClaudeTabsPerBranch != 7 {
			t.Errorf("reloaded MaxClaudeTabsPerBranch = %d, want 7", updated.MaxClaudeTabsPerBranch)
		}
		if updated.BranchSortOrder != "activity" {
			t.Errorf("reloaded BranchSortOrder = %q, want activity", updated.BranchSortOrder)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("watcher did not fire within 3s")
	}
	// In-memory store also reflects the new value.
	if s.Get().MaxClaudeTabsPerBranch != 7 {
		t.Error("store.Get() did not reflect the disk edit")
	}
}

func TestSettingsReload_KeepsPrevOnMalformed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")
	if err := os.WriteFile(path, []byte(`{"maxClaudeTabsPerBranch": 5}`), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := NewSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if s.Get().MaxClaudeTabsPerBranch != 5 {
		t.Fatalf("setup: want 5, got %d", s.Get().MaxClaudeTabsPerBranch)
	}
	// Corrupt the file, then Reload — previous value must be kept (AC-Sa53137-1-2).
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reload(); err == nil {
		t.Error("Reload on malformed file should return an error")
	}
	if s.Get().MaxClaudeTabsPerBranch != 5 {
		t.Errorf("malformed reload clobbered prev value: got %d, want 5", s.Get().MaxClaudeTabsPerBranch)
	}
}
