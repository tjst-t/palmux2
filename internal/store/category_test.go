package store

import (
	"testing"
)

func TestCategorize(t *testing.T) {
	patterns := []string{".claude/worktrees/*"}

	tests := []struct {
		name         string
		branch       string
		path         string
		userOpened   []string
		patterns     []string
		wantCategory string
	}{
		{
			name:         "user wins over pattern match",
			branch:       "auto-foo",
			path:         "/home/u/repo/.claude/worktrees/auto-foo",
			userOpened:   []string{"auto-foo"},
			patterns:     patterns,
			wantCategory: BranchCategoryUser,
		},
		{
			name:         "pattern match → subagent",
			branch:       "auto-foo",
			path:         "/home/u/repo/.claude/worktrees/auto-foo",
			userOpened:   nil,
			patterns:     patterns,
			wantCategory: BranchCategorySubagent,
		},
		{
			name:         "no match → unmanaged",
			branch:       "feature/x",
			path:         "/home/u/repo-feature-x",
			userOpened:   nil,
			patterns:     patterns,
			wantCategory: BranchCategoryUnmanaged,
		},
		{
			name:         "user opened explicit",
			branch:       "main",
			path:         "/home/u/repo",
			userOpened:   []string{"main", "feature/y"},
			patterns:     patterns,
			wantCategory: BranchCategoryUser,
		},
		{
			name:         "subagent with deeper auto path",
			branch:       "S015",
			path:         "/srv/projects/foo/.claude/worktrees/S015",
			userOpened:   []string{"main"},
			patterns:     patterns,
			wantCategory: BranchCategorySubagent,
		},
		{
			name:         "no patterns → unmanaged when not user",
			branch:       "tmp",
			path:         "/x/.claude/worktrees/tmp",
			userOpened:   nil,
			patterns:     nil,
			wantCategory: BranchCategoryUnmanaged,
		},
		{
			name:         "custom pattern",
			branch:       "exp",
			path:         "/x/experiments/exp",
			userOpened:   nil,
			patterns:     []string{"experiments/*"},
			wantCategory: BranchCategorySubagent,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := categorize(tt.branch, tt.path, tt.userOpened, tt.patterns)
			if got != tt.wantCategory {
				t.Errorf("categorize(%q, %q, %v, %v) = %q, want %q",
					tt.branch, tt.path, tt.userOpened, tt.patterns, got, tt.wantCategory)
			}
		})
	}
}

func TestMatchesAnyPattern(t *testing.T) {
	cases := []struct {
		path     string
		patterns []string
		want     bool
	}{
		{"/a/b/.claude/worktrees/x", []string{".claude/worktrees/*"}, true},
		// Substring glob: any 3-segment window matching the pattern is enough.
		// "/a/b/.claude/worktrees/x/y" contains the window
		// `.claude/worktrees/x` so this matches.
		{"/a/b/.claude/worktrees/x/y", []string{".claude/worktrees/*"}, true},
		{"/a/b/c", []string{".claude/worktrees/*"}, false},
		{"/foo/autopilot/S001", []string{"autopilot/*"}, true},
		{"", []string{".claude/worktrees/*"}, false},
		{"/x", []string{""}, false},
		{"/x/y/z", []string{"*/*/z"}, true},
	}
	for _, c := range cases {
		got := matchesAnyPattern(c.path, c.patterns)
		if got != c.want {
			t.Errorf("matchesAnyPattern(%q, %v) = %v, want %v", c.path, c.patterns, got, c.want)
		}
	}
}
