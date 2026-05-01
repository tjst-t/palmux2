package parser

import (
	"bufio"
	"regexp"
	"strings"
	"time"
)

// DecisionEntry is one bullet inside docs/sprint-logs/{Sprint}/decisions.md.
type DecisionEntry struct {
	SprintID  string    `json:"sprintId"`
	Category  string    `json:"category"` // "planning" | "implementation" | "review" | "backlog" | "needs_human"
	Title     string    `json:"title,omitempty"`
	Body      string    `json:"body"`
	NeedsHuman bool     `json:"needsHuman,omitempty"`
	Timestamp time.Time `json:"timestamp,omitempty"`
}

// DecisionsLog is a parsed decisions.md.
type DecisionsLog struct {
	SprintID    string          `json:"sprintId"`
	Header      string          `json:"header,omitempty"`
	Entries     []DecisionEntry `json:"entries"`
	ParseErrors []ParseError    `json:"parseErrors,omitempty"`
}

var (
	reDecisionCategory = regexp.MustCompile(`^##\s+(.+?)\s*$`)
	// Bullet entry: `- **Title**: body...` (multi-line continuation handled
	// separately).
	reDecisionBullet = regexp.MustCompile(`^- \*\*(.+?)\*\*\s*[:：]\s*(.*)$`)
	// Plain bullet: `- body` (no bold title).
	rePlainBullet = regexp.MustCompile(`^- (.+)$`)
)

// ParseDecisions scans one decisions.md file for the given Sprint ID. It
// is fail-safe: any unparseable bullet is skipped (and recorded in
// ParseErrors) but the rest of the document is still served.
func ParseDecisions(sprintID, src string) DecisionsLog {
	out := DecisionsLog{SprintID: sprintID}
	scanner := bufio.NewScanner(strings.NewReader(src))
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	// Optional H1 / front-matter as Header.
	headerEnd := 0
	for i, l := range lines {
		if strings.HasPrefix(l, "## ") {
			headerEnd = i
			break
		}
	}
	if headerEnd > 0 {
		out.Header = strings.TrimSpace(strings.Join(lines[:headerEnd], "\n"))
	}

	category := ""
	var current *DecisionEntry
	flush := func() {
		if current == nil {
			return
		}
		current.Body = strings.TrimSpace(current.Body)
		if strings.Contains(strings.ToUpper(current.Body), "NEEDS_HUMAN") ||
			strings.Contains(strings.ToUpper(current.Title), "NEEDS_HUMAN") {
			current.NeedsHuman = true
		}
		out.Entries = append(out.Entries, *current)
		current = nil
	}

	for _, l := range lines {
		if m := reDecisionCategory.FindStringSubmatch(l); m != nil {
			flush()
			category = classifyDecisionCategory(strings.TrimSpace(m[1]))
			continue
		}
		if category == "" {
			continue
		}
		if strings.HasPrefix(l, "- **") {
			flush()
			if m := reDecisionBullet.FindStringSubmatch(l); m != nil {
				current = &DecisionEntry{
					SprintID: sprintID,
					Category: category,
					Title:    strings.TrimSpace(m[1]),
					Body:     strings.TrimSpace(m[2]),
				}
				continue
			}
			// Bold-without-colon — fall through to plain handling.
		}
		if strings.HasPrefix(l, "- ") && current == nil {
			if m := rePlainBullet.FindStringSubmatch(l); m != nil {
				current = &DecisionEntry{
					SprintID: sprintID,
					Category: category,
					Body:     strings.TrimSpace(m[1]),
				}
				continue
			}
		}
		// Continuation of current bullet (indented or blank-aware).
		if current != nil {
			trim := strings.TrimSpace(l)
			if trim == "" {
				continue
			}
			if strings.HasPrefix(l, "  ") || strings.HasPrefix(l, "\t") {
				current.Body += " " + trim
			}
		}
	}
	flush()
	return out
}

// classifyDecisionCategory maps the section heading text to the canonical
// category slug used by the FE filter UI.
func classifyDecisionCategory(s string) string {
	low := strings.ToLower(s)
	switch {
	case strings.Contains(low, "planning"):
		return "planning"
	case strings.Contains(low, "implementation") || strings.Contains(low, "implement"):
		return "implementation"
	case strings.Contains(low, "review"):
		return "review"
	case strings.Contains(low, "backlog"):
		return "backlog"
	case strings.Contains(low, "needs_human") || strings.Contains(low, "needs human"):
		return "needs_human"
	default:
		return "other"
	}
}
