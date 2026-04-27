package claudeagent

import (
	"os"
	"os/exec"
	"path/filepath"
)

// AuthStatus is the result of probing the host for working Claude CLI auth.
// We do not validate tokens — just look for the surface signals so the UI can
// show a setup hint when none of them are present.
type AuthStatus struct {
	OK      bool   `json:"ok"`
	Source  string `json:"source,omitempty"`  // "oauth_token" | "api_key" | "credentials_file" | ""
	Message string `json:"message,omitempty"` // human-readable hint when not OK
}

const authHelp = "Claude Code is not authenticated. " +
	"On the server, run one of:\n" +
	"  claude  (browser login on a workstation)\n" +
	"  claude setup-token  →  export CLAUDE_CODE_OAUTH_TOKEN=…  (headless server)\n" +
	"  export ANTHROPIC_API_KEY=…  (pay-as-you-go)\n" +
	"…then restart Palmux."

// CheckAuth probes the canonical Claude Code auth surfaces — one is enough.
// CLI binary missing is a separate, harder failure: surface it explicitly so
// the UI can suggest a different remediation.
func CheckAuth(binary string) AuthStatus {
	if binary == "" {
		binary = "claude"
	}
	if _, err := exec.LookPath(binary); err != nil {
		return AuthStatus{
			OK:      false,
			Message: "Claude Code CLI (`" + binary + "`) is not on PATH. Install it from https://docs.claude.com/claude-code first.",
		}
	}
	if v := os.Getenv("CLAUDE_CODE_OAUTH_TOKEN"); v != "" {
		return AuthStatus{OK: true, Source: "oauth_token"}
	}
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		return AuthStatus{OK: true, Source: "api_key"}
	}
	if home, err := os.UserHomeDir(); err == nil {
		credPath := filepath.Join(home, ".claude", ".credentials.json")
		if _, err := os.Stat(credPath); err == nil {
			return AuthStatus{OK: true, Source: "credentials_file"}
		}
	}
	return AuthStatus{OK: false, Message: authHelp}
}
