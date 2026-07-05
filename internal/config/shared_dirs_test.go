package config

import (
	"path/filepath"
	"testing"
)

// [AC-Sd44947-2-1] [AC-Sd44947-2-3] shared_dirs expansion + $HOME-scope validation.
func TestExpandSharedDir(t *testing.T) {
	home := "/home/ubuntu"
	cases := []struct {
		name    string
		in      string
		want    string
		wantErr bool
	}{
		{"tilde slash", "~/.infisical", "/home/ubuntu/.infisical", false},
		{"tilde only", "~", "/home/ubuntu", false},
		{"absolute under home", "/home/ubuntu/.config/op", "/home/ubuntu/.config/op", false},
		{"home itself", "/home/ubuntu", "/home/ubuntu", false},
		{"trailing space trimmed", "  ~/.foo  ", "/home/ubuntu/.foo", false},
		{"nested under home", "~/a/b/c", "/home/ubuntu/a/b/c", false},
		{"outside home absolute", "/etc/passwd", "", true},
		{"outside home root", "/", "", true},
		{"escape via dotdot", "~/../root/.ssh", "", true},
		{"other user home", "~alice/.ssh", "", true},
		{"relative path", "relative/path", "", true},
		{"empty", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ExpandSharedDir(c.in, home)
			if c.wantErr {
				if err == nil {
					t.Fatalf("ExpandSharedDir(%q) = %q, want error", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ExpandSharedDir(%q) unexpected error: %v", c.in, err)
			}
			if got != filepath.Clean(c.want) {
				t.Errorf("ExpandSharedDir(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func TestExpandSharedDirs_DedupAndError(t *testing.T) {
	home := "/home/ubuntu"
	// dedup: same path via ~ and absolute collapses to one.
	out, err := ExpandSharedDirs([]string{"~/.infisical", "/home/ubuntu/.infisical", "~/.config/op"}, home)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(out) != 2 {
		t.Errorf("expected 2 deduped entries, got %v", out)
	}
	// one bad entry aborts the whole list.
	if _, err := ExpandSharedDirs([]string{"~/ok", "/etc/shadow"}, home); err == nil {
		t.Errorf("expected error for out-of-home entry in list")
	}
}

// [AC-Sd44947-2-2] workspace.shared_dirs is classified as the workspace class
// (live in-process apply, not restart/root).
func TestDiffMaster_WorkspaceClass(t *testing.T) {
	old := MasterConfig{}
	neu := MasterConfig{Workspace: WorkspaceSection{SharedDirs: []string{"/home/ubuntu/.infisical"}}}
	changes := DiffMaster(old, neu)
	found := false
	for _, ch := range changes {
		if ch.Field == "workspace.shared_dirs" {
			found = true
			if ch.Class != ClassWorkspace {
				t.Errorf("workspace.shared_dirs class = %q, want %q", ch.Class, ClassWorkspace)
			}
		}
	}
	if !found {
		t.Errorf("expected a workspace.shared_dirs change, got %v", changes)
	}
}
