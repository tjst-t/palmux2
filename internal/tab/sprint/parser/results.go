package parser

import (
	"bufio"
	"regexp"
	"strings"
)

// RefineEntry is one numbered item in docs/sprint-logs/{S}/refine.md.
type RefineEntry struct {
	SprintID string   `json:"sprintId"`
	Number   int      `json:"number"`
	Title    string   `json:"title,omitempty"`
	Body     string   `json:"body,omitempty"`
	Files    []string `json:"files,omitempty"`
}

// AcceptanceMatrix represents a parsed acceptance-matrix.md table. The
// caller (handler) maps it into the per-AC pass/fail/no-test view used by
// the Sprint Detail screen.
type AcceptanceMatrix struct {
	SprintID string                 `json:"sprintId"`
	Rows     []AcceptanceMatrixRow `json:"rows"`
}

// AcceptanceMatrixRow keys an AC ID (e.g. AC-S016-1-3) to its test status.
type AcceptanceMatrixRow struct {
	ACID    string `json:"acId"`
	Story   string `json:"story,omitempty"`
	TestID  string `json:"testId,omitempty"`
	Status  string `json:"status"` // "pass" | "fail" | "no-test"
	Notes   string `json:"notes,omitempty"`
}

// E2EResults is a parsed e2e-results.md tally — used to populate the test
// summary on the Sprint Detail screen.
type E2EResults struct {
	SprintID string `json:"sprintId"`
	Mock     TestSummary `json:"mock"`
	E2E      TestSummary `json:"e2e"`
	Acceptance TestSummary `json:"acceptance"`
}

// TestSummary is one row (mock / e2e / acceptance).
type TestSummary struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

var (
	reRefineHeader = regexp.MustCompile(`^##\s+(\d+)\.?\s*(.*)$`)
	reTotalsLine   = regexp.MustCompile(`(?i)total\s*:\s*(\d+).*?pass(?:ed)?\s*:\s*(\d+).*?fail(?:ed)?\s*:\s*(\d+)`)
	reStatusBadge  = regexp.MustCompile(`(?i)\b(pass|fail|no[-\s]?test)\b`)
)

// ParseRefine extracts numbered "## N. title" entries from refine.md.
// Plain documents that don't follow the numbered convention degrade to a
// single Entry with the whole body.
func ParseRefine(sprintID, src string) []RefineEntry {
	scanner := bufio.NewScanner(strings.NewReader(src))
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	var out []RefineEntry
	current := RefineEntry{SprintID: sprintID}
	hasHeader := false
	flush := func() {
		if !hasHeader && strings.TrimSpace(current.Body) == "" {
			return
		}
		current.Body = strings.TrimSpace(current.Body)
		out = append(out, current)
		current = RefineEntry{SprintID: sprintID}
	}
	for _, l := range lines {
		if m := reRefineHeader.FindStringSubmatch(strings.TrimLeft(l, " ")); m != nil {
			flush()
			n := 0
			for _, c := range m[1] {
				n = n*10 + int(c-'0')
			}
			current = RefineEntry{SprintID: sprintID, Number: n, Title: strings.TrimSpace(m[2])}
			hasHeader = true
			continue
		}
		// "Files:" inline list under entry.
		if strings.HasPrefix(strings.TrimSpace(l), "Files:") || strings.HasPrefix(strings.TrimSpace(l), "files:") {
			tail := strings.TrimSpace(strings.SplitN(l, ":", 2)[1])
			for _, p := range strings.Split(tail, ",") {
				p = strings.TrimSpace(p)
				if p != "" {
					current.Files = append(current.Files, p)
				}
			}
			continue
		}
		current.Body += l + "\n"
	}
	flush()
	return out
}

// ParseAcceptanceMatrix reads a markdown table or AC-tagged bulleted list
// and produces a flat row set. The format is intentionally lenient — we
// just need rough pass/fail signals. Unknown lines are ignored.
func ParseAcceptanceMatrix(sprintID, src string) AcceptanceMatrix {
	out := AcceptanceMatrix{SprintID: sprintID}
	scanner := bufio.NewScanner(strings.NewReader(src))
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	reACTagged := regexp.MustCompile(`\[AC-([A-Za-z0-9-]+)\]`)
	for scanner.Scan() {
		l := scanner.Text()
		if !strings.Contains(l, "AC-") {
			continue
		}
		m := reACTagged.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		row := AcceptanceMatrixRow{ACID: "AC-" + m[1]}
		// Status — first hit wins.
		if s := reStatusBadge.FindString(l); s != "" {
			normalized := strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(s, " ", "-"), "_", "-"))
			if normalized == "no-test" {
				row.Status = "no-test"
			} else {
				row.Status = strings.ToLower(s)
			}
		} else {
			row.Status = "no-test"
		}
		row.Notes = strings.TrimSpace(l)
		out.Rows = append(out.Rows, row)
	}
	return out
}

// ParseE2EResults reads a free-form e2e-results.md and extracts per-bucket
// totals when a "Total: N, Passed: M, Failed: K" line is present. Missing
// buckets remain zero — the FE renders "no results".
func ParseE2EResults(sprintID, src string) E2EResults {
	out := E2EResults{SprintID: sprintID}
	scanner := bufio.NewScanner(strings.NewReader(src))
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	bucket := ""
	for scanner.Scan() {
		l := scanner.Text()
		low := strings.ToLower(l)
		switch {
		case strings.Contains(low, "mock"):
			bucket = "mock"
		case strings.Contains(low, "e2e"):
			bucket = "e2e"
		case strings.Contains(low, "acceptance"):
			bucket = "acceptance"
		}
		if m := reTotalsLine.FindStringSubmatch(l); m != nil {
			ts := TestSummary{}
			for i, ptr := range []*int{&ts.Total, &ts.Passed, &ts.Failed} {
				v := 0
				for _, c := range m[i+1] {
					v = v*10 + int(c-'0')
				}
				*ptr = v
			}
			switch bucket {
			case "mock":
				out.Mock = ts
			case "e2e":
				out.E2E = ts
			case "acceptance":
				out.Acceptance = ts
			}
		}
	}
	return out
}
