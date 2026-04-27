package claudeagent

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// transcriptDir maps a worktree to the directory the CLI writes its
// per-session .jsonl transcripts to. Claude Code uses
// `~/.claude/projects/<slug>` where the slug replaces both `/` and `.`
// in the absolute cwd with `-`.
//
// Example: /home/ubuntu/ghq/github.com/foo/bar →
//          -home-ubuntu-ghq-github-com-foo-bar
func transcriptDir(worktree string) (string, error) {
	if worktree == "" {
		return "", errors.New("claudeagent: empty worktree")
	}
	abs, err := filepath.Abs(worktree)
	if err != nil {
		return "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	slug := strings.NewReplacer("/", "-", ".", "-").Replace(abs)
	return filepath.Join(home, ".claude", "projects", slug), nil
}

// transcriptPath returns the absolute path to the .jsonl for one session.
func transcriptPath(worktree, sessionID string) (string, error) {
	dir, err := transcriptDir(worktree)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sessionID+".jsonl"), nil
}

// TranscriptSummary is the small per-session preview we surface in the
// history popup so each row is recognisable without opening it.
type TranscriptSummary struct {
	LastUserMessage      string `json:"lastUserMessage,omitempty"`
	LastAssistantSnippet string `json:"lastAssistantSnippet,omitempty"`
	FirstUserMessage     string `json:"firstUserMessage,omitempty"`
}

// transcriptEntry mirrors the .jsonl envelope.
type transcriptEntry struct {
	Type      string          `json:"type"`
	Subtype   string          `json:"subtype,omitempty"`
	Message   json.RawMessage `json:"message,omitempty"`
	IsMeta    bool            `json:"isMeta,omitempty"`
	IsSidechain bool          `json:"isSidechain,omitempty"`
	UUID      string          `json:"uuid,omitempty"`
	Timestamp string          `json:"timestamp,omitempty"`
}

const summaryReadCap = 100 // chars

// SummariseTranscript reads the .jsonl and pulls a one-line preview each
// for first user, last user, and last assistant. Skips meta / system
// envelopes and the slash-command bookkeeping rows.
func SummariseTranscript(path string) TranscriptSummary {
	f, err := os.Open(path)
	if err != nil {
		return TranscriptSummary{}
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)

	var sum TranscriptSummary
	for sc.Scan() {
		var e transcriptEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		if e.IsMeta || e.IsSidechain {
			continue
		}
		switch e.Type {
		case "user":
			text := extractMessageText(e.Message)
			text = stripCommandWrapper(text)
			if text == "" {
				continue
			}
			if sum.FirstUserMessage == "" {
				sum.FirstUserMessage = clip(text, summaryReadCap)
			}
			sum.LastUserMessage = clip(text, summaryReadCap)
		case "assistant":
			text := extractMessageText(e.Message)
			if text == "" {
				continue
			}
			sum.LastAssistantSnippet = clip(text, summaryReadCap)
		}
	}
	return sum
}

// LoadTranscriptTurns reads the transcript for `sessionID` under
// `worktree` and replays it as Palmux Turn / Block records — close enough
// to what the live stream would have produced. Used by ResumeSession so
// the user immediately sees the resumed conversation in the chat.
//
// The replay deliberately keeps the structure flat: every CLI envelope
// produces at most one turn (or extends the current one). Block ids /
// turn ids are freshly minted; UI consumers don't rely on them being
// stable across resumes.
func LoadTranscriptTurns(path string) ([]Turn, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 8<<20)

	var turns []Turn
	for sc.Scan() {
		var e transcriptEntry
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		if e.IsMeta || e.IsSidechain {
			continue
		}
		switch e.Type {
		case "user":
			turn, ok := userTurnFromTranscript(e.Message)
			if ok {
				turns = append(turns, turn)
			}
		case "assistant":
			turn, ok := assistantTurnFromTranscript(e.Message)
			if ok {
				turns = append(turns, turn)
			}
		}
	}
	return turns, nil
}

func userTurnFromTranscript(raw json.RawMessage) (Turn, bool) {
	if len(raw) == 0 {
		return Turn{}, false
	}
	var msg chatMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return Turn{}, false
	}
	// User messages can be a plain string or an array of blocks (e.g.
	// tool_result blocks the CLI emits as user-side).
	var blocks []Block
	for _, raw := range msg.Content {
		var cb contentBlock
		if err := json.Unmarshal(raw, &cb); err != nil {
			continue
		}
		switch cb.Type {
		case "text":
			text := stripCommandWrapper(cb.Text)
			if text == "" {
				continue
			}
			blocks = append(blocks, Block{
				ID:   newID("block"),
				Kind: "text",
				Text: text,
				Done: true,
			})
		case "tool_result":
			out := decodeToolResultContent(cb.Content)
			blocks = append(blocks, Block{
				ID:      newID("block"),
				Kind:    "tool_result",
				Output:  out,
				IsError: cb.IsError,
				Done:    true,
			})
		}
	}
	if len(blocks) == 0 {
		return Turn{}, false
	}
	role := "user"
	// Pure-tool-result entries get rendered with the same layout as live
	// tool results (no user-bubble); flag them via role="tool".
	allToolResult := true
	for _, b := range blocks {
		if b.Kind != "tool_result" {
			allToolResult = false
			break
		}
	}
	if allToolResult {
		role = "tool"
	}
	return Turn{Role: role, ID: newID("turn"), Blocks: blocks}, true
}

func assistantTurnFromTranscript(raw json.RawMessage) (Turn, bool) {
	if len(raw) == 0 {
		return Turn{}, false
	}
	var msg chatMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return Turn{}, false
	}
	var blocks []Block
	for i, raw := range msg.Content {
		var cb contentBlock
		if err := json.Unmarshal(raw, &cb); err != nil {
			continue
		}
		switch cb.Type {
		case "text":
			if cb.Text == "" {
				continue
			}
			blocks = append(blocks, Block{
				ID: newID("block"), Kind: "text", Text: cb.Text, Index: i, Done: true,
			})
		case "thinking":
			if cb.Thinking == "" {
				continue
			}
			blocks = append(blocks, Block{
				ID: newID("block"), Kind: "thinking", Text: cb.Thinking, Index: i, Done: true,
			})
		case "tool_use":
			blocks = append(blocks, Block{
				ID: newID("block"), Kind: "tool_use",
				Name: cb.Name, Input: cb.Input, Index: i, Done: true,
			})
		}
	}
	if len(blocks) == 0 {
		return Turn{}, false
	}
	return Turn{Role: "assistant", ID: newID("turn"), Blocks: blocks}, true
}

// extractMessageText pulls a flat text representation out of a
// chatMessage's content for previews. tool_use blocks get the tool name;
// thinking blocks are skipped.
func extractMessageText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var msg chatMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return ""
	}
	var parts []string
	for _, raw := range msg.Content {
		var cb contentBlock
		if err := json.Unmarshal(raw, &cb); err != nil {
			continue
		}
		switch cb.Type {
		case "text":
			if cb.Text != "" {
				parts = append(parts, cb.Text)
			}
		case "tool_use":
			parts = append(parts, fmt.Sprintf("[%s]", cb.Name))
		}
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// stripCommandWrapper drops the <command-name>/<command-message>/
// <local-command-caveat> envelopes the CLI persists for /clear-style
// slash commands, leaving the actual user prose if any.
func stripCommandWrapper(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<local-command-caveat>") {
		// Caveat-only entries are bookkeeping; treat as empty.
		if !strings.Contains(s, "</local-command-caveat>") {
			return ""
		}
	}
	if strings.HasPrefix(s, "<command-name>") {
		// /clear etc. — strip wrapper for a readable preview.
		end := strings.Index(s, "</command-name>")
		if end > 0 {
			name := s[len("<command-name>"):end]
			return strings.TrimSpace(name)
		}
	}
	if strings.HasPrefix(s, "<command-message>") {
		return ""
	}
	return s
}

func clip(s string, n int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	for {
		next := strings.ReplaceAll(s, "  ", " ")
		if next == s {
			break
		}
		s = next
	}
	if len(s) > n {
		return s[:n-1] + "…"
	}
	return s
}
