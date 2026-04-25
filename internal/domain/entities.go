package domain

import "time"

// Repository represents a ghq-managed repository that is currently Open in
// Palmux (i.e. recorded in repos.json).
type Repository struct {
	ID           string    `json:"id"`
	GHQPath      string    `json:"ghqPath"`  // "github.com/tjst-t/palmux"
	FullPath     string    `json:"fullPath"` // absolute path on disk
	Starred      bool      `json:"starred"`
	OpenBranches []*Branch `json:"openBranches"`
}

// Branch represents an open branch — by definition a branch with a worktree
// inside a Repository that has been Open'd.
type Branch struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`         // git branch name
	WorktreePath string    `json:"worktreePath"` // absolute
	RepoID       string    `json:"repoId"`
	IsPrimary    bool      `json:"isPrimary"` // holds the .git/ directory
	TabSet       TabSet    `json:"tabSet"`
	LastActivity time.Time `json:"lastActivity"`
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
