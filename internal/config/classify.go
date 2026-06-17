package config

import "reflect"

// Sa53137-3: diff classification. `palmux apply` and the GUI deploy tab use
// this to decide, per changed field, whether the change is hot (in-process),
// requires a process restart, or requires a privileged system operation
// (root/Caddy). The mapping mirrors the table in docs/unified-config-design.md
// §パラメータ分類.

// ChangeClass is the reflection cost of applying a config change.
type ChangeClass string

const (
	// ClassHot is applied in-process with no restart (provider refresh / route
	// resync). Currently caddy_admin, claude_bin, claude_args.
	ClassHot ChangeClass = "hot"
	// ClassRestart requires `systemctl --user restart palmux2`: listener/router
	// rebind, token, tmux prefix, max connections, SSO secret, basic auth.
	ClassRestart ChangeClass = "restart"
	// ClassRoot requires a privileged system operation (Story 4: reconcile /
	// install.sh re-run): public domain / TLS / Cloudflare token.
	ClassRoot ChangeClass = "root"
)

// FieldChange records one differing field between two MasterConfig values.
type FieldChange struct {
	Field string      `json:"field"`
	Class ChangeClass `json:"class"`
	Old   any         `json:"-"`
	New   any         `json:"-"`
}

// fieldClass maps a master-config field key to its reflection class.
var fieldClass = map[string]ChangeClass{
	"server.addr":            ClassRestart,
	"server.base_path":       ClassRestart,
	"server.max_connections": ClassRestart,
	"server.tmux_prefix":     ClassRestart,
	"server.caddy_admin":     ClassHot,
	"server.claude_bin":      ClassHot,
	"server.claude_args":     ClassHot,
	"public.domain":          ClassRoot,
	"public.basic_auth_user": ClassRestart,
}

// SecretFieldClass classifies secrets changes (they live in secrets.env, not
// the master, but apply treats them in the same diff). Exposed so callers that
// also diff secrets reuse the same table.
var SecretFieldClass = map[string]ChangeClass{
	"secrets.sso_secret":       ClassRestart,
	"secrets.basic_auth_hash":  ClassRestart,
	"secrets.token":            ClassRestart,
	"secrets.cloudflare_token": ClassRoot,
}

// FieldClassOf returns the change class for a master-config field key, or
// ClassRestart as the conservative default for an unknown field (better to
// restart than to silently no-op a change the operator made).
func FieldClassOf(field string) ChangeClass {
	if c, ok := fieldClass[field]; ok {
		return c
	}
	if c, ok := SecretFieldClass[field]; ok {
		return c
	}
	return ClassRestart
}

// DiffMaster returns the per-field changes between old and new MasterConfig,
// each tagged with its change class.
func DiffMaster(old, neu MasterConfig) []FieldChange {
	var changes []FieldChange
	cmp := func(field string, o, n any) {
		if !reflect.DeepEqual(o, n) {
			changes = append(changes, FieldChange{Field: field, Class: FieldClassOf(field), Old: o, New: n})
		}
	}
	cmp("server.addr", old.Server.Addr, neu.Server.Addr)
	cmp("server.base_path", old.Server.BasePath, neu.Server.BasePath)
	cmp("server.max_connections", old.Server.MaxConnections, neu.Server.MaxConnections)
	cmp("server.tmux_prefix", old.Server.TmuxPrefix, neu.Server.TmuxPrefix)
	cmp("server.caddy_admin", old.Server.CaddyAdmin, neu.Server.CaddyAdmin)
	cmp("server.claude_bin", old.Server.ClaudeBin, neu.Server.ClaudeBin)
	cmp("server.claude_args", old.Server.ClaudeArgs, neu.Server.ClaudeArgs)
	cmp("public.domain", old.Public.Domain, neu.Public.Domain)
	cmp("public.basic_auth_user", old.Public.BasicAuthUser, neu.Public.BasicAuthUser)
	return changes
}

// HighestClass returns the most disruptive class present in the changes
// (root > restart > hot), or "" when there are none. Used to decide the
// headline action of an apply.
func HighestClass(changes []FieldChange) ChangeClass {
	rank := map[ChangeClass]int{ClassHot: 1, ClassRestart: 2, ClassRoot: 3}
	var best ChangeClass
	for _, c := range changes {
		if rank[c.Class] > rank[best] {
			best = c.Class
		}
	}
	return best
}
