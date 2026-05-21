package claudetui

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// TranscriptBubble is the structured chat-bubble shape exposed to the mobile
// frontend.  It is derived from a Claude Code .jsonl transcript so the mobile
// UI can render the full conversation history (which the grid-based view
// cannot recover because the TUI uses alternate screen).
type TranscriptBubble struct {
	ID      string `json:"id"`
	Speaker string `json:"speaker"` // "user" or "assistant"
	Text    string `json:"text"`
}

// transcriptEntry mirrors the relevant fields of one .jsonl envelope.
type transcriptEntry struct {
	Type        string          `json:"type"`
	Message     json.RawMessage `json:"message,omitempty"`
	IsMeta      bool            `json:"isMeta,omitempty"`
	IsSidechain bool            `json:"isSidechain,omitempty"`
}

// transcriptMessage is the message body.
type transcriptMessage struct {
	Role    string          `json:"role"`
	Content rawContentList  `json:"content"`
}

// rawContentList accepts both string and array content (the CLI sometimes
// emits user text as a plain string, sometimes as a content-block array).
type rawContentList []json.RawMessage

func (r *rawContentList) UnmarshalJSON(b []byte) error {
	if len(b) > 0 && b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		obj := struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}{Type: "text", Text: s}
		raw, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		*r = rawContentList{raw}
		return nil
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(b, &arr); err != nil {
		return err
	}
	*r = arr
	return nil
}

// transcriptBlock is just enough of a content block to dispatch on type.
type transcriptBlock struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Thinking string `json:"thinking,omitempty"`
	Name     string `json:"name,omitempty"` // for tool_use
}

// ReadTranscriptBubbles parses a Claude Code .jsonl transcript file and
// returns the chat history as an ordered list of bubbles. Tool-use and
// thinking blocks are summarised as "[tool_name]" and skipped respectively
// so the bubble list stays focused on what the user actually said and what
// claude actually replied.
//
// Returns an empty slice (not an error) when the file does not exist — this
// is the expected state before claude has written any turn for this session.
func ReadTranscriptBubbles(path string) ([]TranscriptBubble, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("claudetui transcript: open %s: %w", path, err)
	}
	defer f.Close()

	bubbles := make([]TranscriptBubble, 0, 16)
	id := 0

	scanner := bufio.NewScanner(f)
	// Transcript lines can be large (long claude responses); raise the buffer.
	scanner.Buffer(make([]byte, 0, 1<<16), 4<<20)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e transcriptEntry
		if err := json.Unmarshal(line, &e); err != nil {
			continue // skip malformed lines
		}
		if e.IsMeta || e.IsSidechain {
			continue
		}

		switch e.Type {
		case "user":
			text := extractTranscriptText(e.Message)
			text = stripCommandWrapper(text)
			if text == "" {
				continue
			}
			id++
			bubbles = append(bubbles, TranscriptBubble{
				ID:      fmt.Sprintf("t-u-%d", id),
				Speaker: "user",
				Text:    text,
			})
		case "assistant":
			text := extractTranscriptText(e.Message)
			if text == "" {
				continue
			}
			id++
			bubbles = append(bubbles, TranscriptBubble{
				ID:      fmt.Sprintf("t-a-%d", id),
				Speaker: "assistant",
				Text:    text,
			})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("claudetui transcript: scan %s: %w", path, err)
	}
	return bubbles, nil
}

// extractTranscriptText pulls a flat text representation out of a message's
// content. text blocks contribute their text; tool_use blocks become a short
// "[name]" tag; thinking blocks are skipped.
func extractTranscriptText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var msg transcriptMessage
	if err := json.Unmarshal(raw, &msg); err != nil {
		return ""
	}
	var parts []string
	for _, rawBlock := range msg.Content {
		var b transcriptBlock
		if err := json.Unmarshal(rawBlock, &b); err != nil {
			continue
		}
		switch b.Type {
		case "text":
			if b.Text != "" {
				parts = append(parts, b.Text)
			}
		case "tool_use":
			if b.Name != "" {
				parts = append(parts, "["+b.Name+"]")
			}
		}
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

// stripCommandWrapper drops the <command-name>/<command-message>/
// <local-command-caveat> envelopes that the CLI persists for /clear-style
// slash commands, leaving the actual user prose (if any) behind.
func stripCommandWrapper(s string) string {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(s, "<local-command-caveat>") {
		// Discard the entire wrapped command meta-entry.
		return ""
	}
	for _, tag := range []string{"<command-name>", "<command-message>", "<command-args>"} {
		for strings.Contains(s, tag) {
			start := strings.Index(s, tag)
			closeTag := "</" + strings.TrimPrefix(tag, "<")
			end := strings.Index(s, closeTag)
			if end < 0 {
				break
			}
			end += len(closeTag)
			s = strings.TrimSpace(s[:start] + s[end:])
		}
	}
	return s
}
