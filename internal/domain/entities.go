package domain

import "time"

// Repository represents a ghq-managed repository that is currently Open in
// Palmux (i.e. recorded in repos.json).
//
// LastActiveBranch (S023) carries the persisted "remembered branch" that
// the Drawer uses to navigate-and-expand a collapsed repo on a single
// click of the header. Empty string means "no remembered branch" (first
// open, or the previous branch was reconciled away). Stored as a branch
// **name** (not ID) so the persisted value survives a hash regeneration.
type Repository struct {
	ID               string    `json:"id"`
	GHQPath          string    `json:"ghqPath"`  // "github.com/tjst-t/palmux"
	FullPath         string    `json:"fullPath"` // absolute path on disk
	Starred          bool      `json:"starred"`
	OpenBranches     []*Branch `json:"openBranches"`
	LastActiveBranch string    `json:"lastActiveBranch,omitempty"`
}

// Branch represents an open branch — by definition a branch with a worktree
// inside a Repository that has been Open'd.
//
// Category (S015) classifies the branch for the Drawer:
//   - "user"      — recorded in repos.json#userOpenedBranches (the user
//                   opened it through Palmux or promoted it explicitly).
//   - "subagent"  — worktree path matches `autoWorktreePathPatterns`
//                   (claude-skills sub-agent / autopilot output).
//   - "unmanaged" — exists on disk (e.g. via `git worktree add` directly)
//                   but neither user-opened nor pattern-matched.
//
// The Drawer reads this to render three sections; the FE re-labels "user"
// as "my".
type Branch struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`         // git branch name
	WorktreePath string    `json:"worktreePath"` // absolute
	RepoID       string    `json:"repoId"`
	IsPrimary    bool      `json:"isPrimary"` // holds the .git/ directory
	TabSet       TabSet    `json:"tabSet"`
	LastActivity time.Time `json:"lastActivity"`
	Category     string    `json:"category,omitempty"` // S015

	// Runtime (Sdd4ce1) reports the *resolved* runtime kind + state for the
	// Workspace, surfaced via the API to drive the Header chip and the Drawer
	// state badges (AC-Sdd4ce1-5-4 / AC-Sdd4ce1-5-5). Resolved means the
	// priority-chain output (per-Workspace → per-repo → global → host
	// fallback). The state is best-effort — host runtime always reports
	// "ready"; lxd-* surfaces real container state when implemented.
	Runtime *BranchRuntimeView `json:"runtime,omitempty"`
}

// BranchRuntimeView is the public shape of Branch.Runtime exposed to the FE.
// It deliberately mirrors only what the UI needs — image/network/etc are
// reachable via /api/repos/{repoId}/branches/{branchId}/runtime when needed.
type BranchRuntimeView struct {
	// Kind is the runtime kind (host / lxd-container / lxd-vm / ...).
	Kind string `json:"kind"`
	// State is one of "stopped" / "starting" / "ready" / "stopping" /
	// "failed". host runtime always reports "ready" once Open.
	State string `json:"state"`
	// Address is best-effort — "localhost" for host, container IP otherwise.
	Address string `json:"address,omitempty"`
	// Error carries the most recent failure reason for State=="failed".
	Error string `json:"error,omitempty"`
}

// TabSet is the collection of tabs for one branch.
type TabSet struct {
	TmuxSession string `json:"tmuxSession"`
	Tabs        []Tab  `json:"tabs"`
}

// Tab is the unified API/store representation of any tab type. Provider
// implementations construct these in OnBranchOpen.
type Tab struct {
	ID         string `json:"id"`                   // "claude", "bash:bash", ...
	Type       string `json:"type"`                 // provider.Type()
	Name       string `json:"name"`                 // display name
	Protected  bool   `json:"protected"`            // user cannot delete
	Multiple   bool   `json:"multiple"`             // multiple instances allowed
	WindowName string `json:"windowName,omitempty"` // tmux window name (terminal-backed only)
}

// Notification is a single Activity Inbox entry.
type Notification struct {
	ID         string               `json:"id"`
	RepoID     string               `json:"repoId"`
	BranchID   string               `json:"branchId"`
	BranchName string               `json:"branchName"` // display: "owner/repo / branch"
	Type       NotificationType     `json:"type"`
	Message    string               `json:"message"`
	Detail     string               `json:"detail,omitempty"`
	Actions    []NotificationAction `json:"actions,omitempty"`
	CreatedAt  time.Time            `json:"createdAt"`
	Read       bool                 `json:"read"`
}

// NotificationType matches the UI categories in 04-ui-requirements.md.
type NotificationType string

const (
	NotificationUrgent  NotificationType = "urgent"
	NotificationWarning NotificationType = "warning"
	NotificationInfo    NotificationType = "info"
)

// NotificationAction is an inline button on a notification.
type NotificationAction struct {
	Label  string `json:"label"`  // "Yes (y)"
	Action string `json:"action"` // "yes" / "no" / "resume"
}

// Connection represents one client attached to a branch's terminal. The store
// uses these to clean up tmux session-groups on disconnect.
type Connection struct {
	ID        string    `json:"id"`
	RepoID    string    `json:"repoId"`
	BranchID  string    `json:"branchId"`
	TabID     string    `json:"tabId"`
	StartedAt time.Time `json:"startedAt"`
}
