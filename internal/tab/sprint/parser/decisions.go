// decisions.go — JSON unmarshaler for docs/sprint-logs/{S}/decisions.json
// (S028). Replaces the markdown bullet parser.
//
// Each decision is normalized into a DecisionEntry. We keep `Title` and
// `Body` separate (Title from the schema's title field, Body from
// `detail`) because the existing FE renders them distinctly. NEEDS_HUMAN
// detection is preserved — the schema doesn't have a dedicated field, so
// we look at category and detail text.

package parser

import (
	"encoding/json"
	"strings"
)

// ParseDecisions parses one decisions.json file. SprintID is taken from
// the document if present and falls back to the caller-supplied value
// (which is the directory name under docs/sprint-logs/).
//
// Fail-safe: a malformed file yields a single ParseError plus an empty
// Entries slice.
func ParseDecisions(sprintID string, src []byte) DecisionsLog {
	out := DecisionsLog{
		SprintID:    sprintID,
		Entries:     []DecisionEntry{},
		ParseErrors: []ParseError{},
	}
	if len(src) == 0 {
		return out
	}
	var doc decisionsDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		out.ParseErrors = append(out.ParseErrors, jsonSyntaxError("decisions.json", src, err))
		return out
	}
	if doc.Sprint != "" {
		out.SprintID = doc.Sprint
	}
	for _, d := range doc.Decisions {
		entry := DecisionEntry{
			SprintID:  out.SprintID,
			Category:  classifyDecisionCategory(d.Category),
			Title:     d.Title,
			Body:      d.Detail,
			Reference: d.Reference,
			Timestamp: d.Timestamp,
		}
		// NEEDS_HUMAN detection — preserved from the markdown era.
		// Either an explicit "needs_human" category or the substring in
		// title/body sets the flag.
		if entry.Category == "needs_human" ||
			containsNeedsHuman(entry.Title) ||
			containsNeedsHuman(entry.Body) {
			entry.NeedsHuman = true
		}
		out.Entries = append(out.Entries, entry)
	}
	return out
}

func classifyDecisionCategory(s string) string {
	low := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.Contains(low, "planning"):
		return "planning"
	case strings.Contains(low, "implementation") || strings.Contains(low, "implement"):
		return "implementation"
	case strings.Contains(low, "review"):
		return "review"
	case strings.Contains(low, "backlog"):
		return "backlog"
	case strings.Contains(low, "needs_human") || strings.Contains(low, "needs-human") || strings.Contains(low, "needs human"):
		return "needs_human"
	case low == "":
		return "other"
	default:
		return low
	}
}

func containsNeedsHuman(s string) bool {
	up := strings.ToUpper(s)
	return strings.Contains(up, "NEEDS_HUMAN") || strings.Contains(up, "NEEDS HUMAN")
}
