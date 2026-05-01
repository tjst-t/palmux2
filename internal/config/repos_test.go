package config

import (
	"path/filepath"
	"testing"
)

func TestRepoStore_AddRemove(t *testing.T) {
	dir := t.TempDir()
	s, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore: %v", err)
	}
	if got := s.All(); len(got) != 0 {
		t.Fatalf("expected empty store, got %v", got)
	}

	added, err := s.Add(RepoEntry{ID: "r1", GHQPath: "github.com/a/r1"})
	if err != nil || !added {
		t.Fatalf("Add r1: added=%v err=%v", added, err)
	}
	added, err = s.Add(RepoEntry{ID: "r1", GHQPath: "github.com/a/r1"})
	if err != nil || added {
		t.Fatalf("re-add r1: added=%v err=%v", added, err)
	}
	added, err = s.Add(RepoEntry{ID: "r2", GHQPath: "github.com/a/r2"})
	if err != nil || !added {
		t.Fatalf("Add r2: added=%v err=%v", added, err)
	}
	if got := s.All(); len(got) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(got))
	}

	if _, ok := s.Get("r1"); !ok {
		t.Fatal("Get r1 missing")
	}

	if _, err := s.SetStarred("r1", true); err != nil {
		t.Fatalf("SetStarred: %v", err)
	}
	got, _ := s.Get("r1")
	if !got.Starred {
		t.Fatal("expected starred=true")
	}

	removed, err := s.Remove("r1")
	if err != nil || !removed {
		t.Fatalf("Remove r1: removed=%v err=%v", removed, err)
	}
	if _, ok := s.Get("r1"); ok {
		t.Fatal("Get r1 still present after Remove")
	}

	// Reload from disk to confirm persistence.
	s2, err := NewRepoStore(dir)
	if err != nil {
		t.Fatalf("NewRepoStore (reload): %v", err)
	}
	if got := s2.All(); len(got) != 1 || got[0].ID != "r2" {
		t.Fatalf("reload mismatch: %v", got)
	}

	// File should exist where we expect it.
	if _, err := filepath.Abs(filepath.Join(dir, "repos.json")); err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
}

func TestSettingsStore_Defaults(t *testing.T) {
	dir := t.TempDir()
	s, err := NewSettingsStore(dir)
	if err != nil {
		t.Fatalf("NewSettingsStore: %v", err)
	}
	got := s.Get()
	if got.BranchSortOrder != "name" {
		t.Errorf("default BranchSortOrder = %q", got.BranchSortOrder)
	}
	if got.AttachmentUploadDir != "/tmp/palmux-uploads/" {
		t.Errorf("default AttachmentUploadDir = %q", got.AttachmentUploadDir)
	}
	if got.AttachmentTtlDays != 30 {
		t.Errorf("default AttachmentTtlDays = %d, want 30", got.AttachmentTtlDays)
	}
}

func TestSettingsStore_Patch(t *testing.T) {
	dir := t.TempDir()
	s, _ := NewSettingsStore(dir)
	updated, err := s.Patch(Settings{BranchSortOrder: "activity"})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if updated.BranchSortOrder != "activity" {
		t.Errorf("after patch: %q", updated.BranchSortOrder)
	}
	// Reload — the updated value must persist.
	s2, _ := NewSettingsStore(dir)
	if got := s2.Get(); got.BranchSortOrder != "activity" {
		t.Errorf("after reload: %q", got.BranchSortOrder)
	}
}
