package claudetui

import (
	"os"
	"path/filepath"
	"testing"
)

// TestStoreRoundtrip verifies SetActive → LoadActive → ClearActive → LoadActive.
func TestStoreRoundtrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	const (
		repoID    = "test-repo"
		branchID  = "test-branch"
		tabID     = "claude:claude"
		sessionID = "aaaabbbb-cccc-dddd-eeee-ffffffffffff"
	)

	// Initially nothing stored.
	if got, ok := s.LoadActive(repoID, branchID, tabID); ok {
		t.Fatalf("unexpected initial session: %q", got)
	}

	// SetActive.
	if err := s.SetActive(repoID, branchID, tabID, sessionID); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	// LoadActive returns the stored value.
	got, ok := s.LoadActive(repoID, branchID, tabID)
	if !ok {
		t.Fatal("LoadActive returned ok=false after SetActive")
	}
	if got != sessionID {
		t.Errorf("LoadActive = %q, want %q", got, sessionID)
	}

	// ClearActive removes it.
	if err := s.ClearActive(repoID, branchID, tabID); err != nil {
		t.Fatalf("ClearActive: %v", err)
	}
	if _, ok := s.LoadActive(repoID, branchID, tabID); ok {
		t.Fatal("LoadActive should return ok=false after ClearActive")
	}
}

// TestStoreCorruptionRecovery verifies that a corrupt JSON file causes
// LoadActive to return ok=false rather than panic or return an error.
func TestStoreCorruptionRecovery(t *testing.T) {
	dir := t.TempDir()
	// Write garbage to the sessions file before opening the store.
	if err := os.WriteFile(
		filepath.Join(dir, tuiSessionsFileName),
		[]byte("THIS IS NOT JSON }{!@#$"),
		0o600,
	); err != nil {
		t.Fatalf("writing corrupt file: %v", err)
	}

	// NewSessionStore must succeed (corruption is handled silently).
	s, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore with corrupt file: %v", err)
	}

	// LoadActive must return ok=false without panicking.
	if _, ok := s.LoadActive("any-repo", "any-branch", "claude:claude"); ok {
		t.Error("LoadActive should return ok=false on a freshly recovered store")
	}
}

// TestStorePersistenceAcrossReopen verifies that data written by one
// SessionStore instance is visible to a second instance loaded from the same
// file (simulating a server restart).
func TestStorePersistenceAcrossReopen(t *testing.T) {
	dir := t.TempDir()
	const (
		repoID    = "r1"
		branchID  = "b1"
		tabID     = "claude:claude"
		sessionID = "12345678-1234-1234-1234-123456789abc"
	)

	// First instance: write.
	s1, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore (1): %v", err)
	}
	if err := s1.SetActive(repoID, branchID, tabID, sessionID); err != nil {
		t.Fatalf("SetActive: %v", err)
	}

	// Second instance: read back.
	s2, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore (2): %v", err)
	}
	got, ok := s2.LoadActive(repoID, branchID, tabID)
	if !ok {
		t.Fatal("LoadActive returned ok=false; data not persisted")
	}
	if got != sessionID {
		t.Errorf("got %q, want %q", got, sessionID)
	}
}

// TestStoreSetActiveEmptyError verifies that SetActive rejects an empty session ID.
func TestStoreSetActiveEmptyError(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}
	if err := s.SetActive("r", "b", "claude:claude", ""); err == nil {
		t.Error("SetActive with empty sessionID should return error")
	}
}

// TestStoreMultipleKeys verifies that multiple (repoID, branchID, tabID)
// tuples are stored independently. Sadf90e: tabID is now part of the key so
// two Claude(tui) tabs on the same branch persist independently.
func TestStoreMultipleKeys(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSessionStore(dir)
	if err != nil {
		t.Fatalf("NewSessionStore: %v", err)
	}

	pairs := []struct {
		repo, branch, tab, session string
	}{
		{"r1", "b1", "claude:claude", "aaaaaaaa-0000-0000-0000-000000000001"},
		{"r1", "b1", "claude:second", "dddddddd-0000-0000-0000-000000000004"},
		{"r1", "b2", "claude:claude", "bbbbbbbb-0000-0000-0000-000000000002"},
		{"r2", "b1", "claude:claude", "cccccccc-0000-0000-0000-000000000003"},
	}
	for _, p := range pairs {
		if err := s.SetActive(p.repo, p.branch, p.tab, p.session); err != nil {
			t.Fatalf("SetActive(%q,%q,%q): %v", p.repo, p.branch, p.tab, err)
		}
	}
	for _, p := range pairs {
		got, ok := s.LoadActive(p.repo, p.branch, p.tab)
		if !ok {
			t.Fatalf("LoadActive(%q,%q,%q) = ok=false", p.repo, p.branch, p.tab)
		}
		if got != p.session {
			t.Errorf("LoadActive(%q,%q,%q) = %q, want %q", p.repo, p.branch, p.tab, got, p.session)
		}
	}
}
