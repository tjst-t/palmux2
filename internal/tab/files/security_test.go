package files

import (
	"errors"
	"testing"
)

func TestResolveSafePath(t *testing.T) {
	root := t.TempDir()
	cases := []struct {
		name    string
		rel     string
		wantErr bool
	}{
		{"root", "", false},
		{"dot", ".", false},
		{"sub", "src/main.go", false},
		{"slash sub rejected", "/src/main.go", true},
		{"parent traversal", "../etc/passwd", true},
		{"sneaky parent", "src/../../etc/passwd", true},
		{"absolute", "/etc/passwd", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := resolveSafePath(root, c.rel)
			if c.wantErr {
				if !errors.Is(err, ErrInvalidPath) {
					t.Errorf("expected ErrInvalidPath, got %v", err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
