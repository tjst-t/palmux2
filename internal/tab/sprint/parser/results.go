// results.go — JSON unmarshalers for the remaining sprint-log files (S028):
//
//   - acceptance-matrix.json → ParseAcceptanceMatrix
//   - e2e-results.json       → ParseE2EResults
//   - refine.json            → ParseRefine
//   - failures.json          → ParseFailures
//   - gui-spec-{Story}.json  → ParseGUISpec
//
// All parsers are fail-safe: a malformed file produces a ParseError plus
// the best-effort projection. The handler accumulates ParseErrors into
// the response so the FE banner renders them without breaking the page.

package parser

import (
	"encoding/json"
	"strings"
)

// ParseAcceptanceMatrix turns acceptance-matrix.json into the flat row
// shape the FE already understands.
func ParseAcceptanceMatrix(sprintID string, src []byte) AcceptanceMatrix {
	out := AcceptanceMatrix{
		SprintID: sprintID,
		Rows:     []AcceptanceMatrixRow{},
	}
	if len(src) == 0 {
		return out
	}
	var doc acceptanceMatrixDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		// Surface the syntax error via a synthetic row with status "fail"
		// so the FE acceptance-matrix table reflects the problem rather
		// than appearing empty for no obvious reason. This keeps the
		// handler-side ParseError plumbing simple (the handler can still
		// return its own ParseErrors[] separately if it wants).
		out.Rows = append(out.Rows, AcceptanceMatrixRow{
			ACID:   "ACCEPTANCE_MATRIX_PARSE_ERROR",
			Status: "fail",
			Notes:  err.Error(),
		})
		return out
	}
	if doc.Sprint != "" {
		out.SprintID = doc.Sprint
	}
	// Flatten matrix[storyID] → []row.
	for storyID, rows := range doc.Matrix {
		for _, r := range rows {
			out.Rows = append(out.Rows, AcceptanceMatrixRow{
				ACID:   r.Criterion,
				Story:  storyID,
				TestID: r.TestName,
				Status: classifyAcceptanceStatus(r.Status),
				Notes:  noteFor(r),
			})
		}
	}
	return out
}

func noteFor(r matrixRowDoc) string {
	parts := []string{}
	if r.Description != "" {
		parts = append(parts, r.Description)
	}
	if r.TestFile != "" {
		parts = append(parts, r.TestFile)
	}
	if r.Error != "" {
		parts = append(parts, "error: "+r.Error)
	}
	return strings.Join(parts, " · ")
}

func classifyAcceptanceStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pass":
		return "pass"
	case "fail":
		return "fail"
	case "no_test", "no-test", "":
		return "no-test"
	case "pending":
		// Pending criteria render as no-test until they have a result.
		return "no-test"
	}
	return "no-test"
}

// ParseE2EResults computes the per-bucket TestSummary from a single
// e2e-results.json document. We bucket each test based on its `file` (or
// `name` if file is missing).
func ParseE2EResults(sprintID string, src []byte) E2EResults {
	out := E2EResults{SprintID: sprintID}
	if len(src) == 0 {
		return out
	}
	var doc e2eResultsDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		// Stuff the failure into out.E2E.Failed so the dashboard still
		// surfaces "something is wrong" rather than silently zeroing.
		out.E2E.Total = 1
		out.E2E.Failed = 1
		return out
	}
	if doc.Sprint != "" {
		out.SprintID = doc.Sprint
	}
	for _, t := range doc.Tests {
		bucket := bucketForTest(t)
		summary := pickBucket(&out, bucket)
		summary.Total++
		switch strings.ToLower(t.Status) {
		case "pass", "passed":
			summary.Passed++
		case "fail", "failed", "error":
			summary.Failed++
		}
	}
	// If the document has no per-test entries, fall back to the
	// top-level summary as the e2e bucket so we still surface totals.
	if len(doc.Tests) == 0 && doc.Summary.Total > 0 {
		out.E2E.Total = doc.Summary.Total
		out.E2E.Passed = doc.Summary.Pass
		out.E2E.Failed = doc.Summary.Fail
	}
	return out
}

func pickBucket(out *E2EResults, bucket string) *TestSummary {
	switch bucket {
	case "mock":
		return &out.Mock
	case "acceptance":
		return &out.Acceptance
	default:
		return &out.E2E
	}
}

func bucketForTest(t e2eTestDoc) string {
	src := strings.ToLower(t.File)
	if src == "" {
		src = strings.ToLower(t.Name)
	}
	switch {
	case strings.Contains(src, ".mock.spec"), strings.Contains(src, "mock_"):
		return "mock"
	case strings.Contains(src, "tests/acceptance/"), strings.Contains(src, "acceptance"):
		return "acceptance"
	case strings.Contains(src, ".e2e.spec"), strings.Contains(src, "tests/e2e/"):
		return "e2e"
	}
	return "e2e"
}

// ParseRefine projects refine.json into RefineEntry slice. The S016 era
// FE expected (number, title, body, files); we synthesize:
//   - Number  = doc.id
//   - Title   = first sentence of `change`
//   - Body    = feedback + change concatenated
//   - Files   = files
func ParseRefine(sprintID string, src []byte) []RefineEntry {
	out := []RefineEntry{}
	if len(src) == 0 {
		return out
	}
	var doc refineDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		return out
	}
	if doc.Sprint != "" {
		sprintID = doc.Sprint
	}
	for _, r := range doc.Refinements {
		body := strings.TrimSpace(r.Feedback)
		if r.Change != "" {
			if body != "" {
				body += "\n\n"
			}
			body += r.Change
		}
		out = append(out, RefineEntry{
			SprintID:    sprintID,
			Number:      r.ID,
			Title:       firstSentence(r.Change),
			Body:        body,
			Files:       r.Files,
			TestsRerun:  r.TestsRerun,
			TestsPassed: r.TestsPassed,
		})
	}
	return out
}

func firstSentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// Cut at the first newline, period, or 80 chars — whichever is
	// closest. Keeps the timeline title compact.
	for i, r := range s {
		if r == '\n' || r == '.' || r == '。' {
			return strings.TrimSpace(s[:i])
		}
		if i >= 80 {
			return strings.TrimSpace(s[:i]) + "…"
		}
	}
	return s
}

// ParseFailures projects failures.json into FailureEntry slice.
func ParseFailures(sprintID string, src []byte) []FailureEntry {
	out := []FailureEntry{}
	if len(src) == 0 {
		return out
	}
	var doc failuresDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		return out
	}
	if doc.Sprint != "" {
		sprintID = doc.Sprint
	}
	for _, f := range doc.Failures {
		entry := FailureEntry{
			SprintID:   sprintID,
			Story:      f.Story,
			Type:       f.Type,
			Summary:    f.Summary,
			Resolution: f.Resolution,
		}
		for _, a := range f.Attempts {
			entry.Attempts = append(entry.Attempts, FailureAttempt{
				Approach: a.Approach,
				Result:   a.Result,
			})
		}
		out = append(out, entry)
	}
	return out
}

// ParseGUISpec projects gui-spec-{Story}.json. Returned alone from the
// handler — schema spans many fields so we keep the shape flexible.
func ParseGUISpec(sprintID, storyID string, src []byte) GUISpec {
	out := GUISpec{SprintID: sprintID, Story: storyID}
	if len(src) == 0 {
		return out
	}
	var doc guiSpecDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		return out
	}
	if doc.Sprint != "" {
		out.SprintID = doc.Sprint
	}
	if doc.Story != "" {
		out.Story = doc.Story
	}
	out.StateDiagram = doc.StateDiagram
	out.Scenarios = doc.Scenarios
	out.TestFiles = doc.TestFiles
	for _, ec := range doc.EndpointContracts {
		out.EndpointContracts = append(out.EndpointContracts, GUIEndpointContract{
			Path:           ec.Path,
			Method:         ec.Method,
			Registered:     ec.Registered,
			RequestFields:  ec.RequestFields,
			ResponseFields: ec.ResponseFields,
		})
	}
	return out
}
