package commands

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestScanMakefile(t *testing.T) {
	dir := t.TempDir()
	body := "# comment\n" +
		"build: deps\n" +
		"\tgo build ./...\n" +
		"deps:\n" +
		"\tgo mod download\n" +
		".PHONY: build deps\n"
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := scanMakefile(dir)
	want := map[string]bool{"build": true, "deps": true}
	if len(got) != len(want) {
		t.Fatalf("got %d targets, want %d: %+v", len(got), len(want), got)
	}
	for _, c := range got {
		if !want[c.Name] {
			t.Errorf("unexpected target %q", c.Name)
		}
		if c.Source != "make" {
			t.Errorf("source = %q, want make", c.Source)
		}
	}
}

func TestScanPackageJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "pnpm-lock.yaml"), []byte("lockfileVersion: 6.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	body := `{"scripts": {"dev": "vite", "build": "vite build"}}`
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got := scanPackageJSON(dir)
	if len(got) != 2 {
		t.Fatalf("got %d scripts, want 2", len(got))
	}
	for _, c := range got {
		if c.Source != "pnpm" {
			t.Errorf("source = %q, want pnpm", c.Source)
		}
	}
}

func TestDetectorCache(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("build:\n\techo ok\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	d := New()
	first, err := d.Detect(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	// Mutate file — cache should serve the original until invalidated.
	if err := os.WriteFile(filepath.Join(dir, "Makefile"), []byte("build:\n\techo ok\ntest:\n\techo t\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	second, err := d.Detect(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != len(second) {
		t.Fatalf("cache miss: first=%d second=%d", len(first), len(second))
	}
	d.Invalidate(dir)
	third, err := d.Detect(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(third) <= len(second) {
		t.Errorf("expected more after invalidate, got %d -> %d", len(second), len(third))
	}
}
