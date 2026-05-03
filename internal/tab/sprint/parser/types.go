// Package parser turns the JSON files emitted by the claude-skills
// `sprint` skill (ROADMAP.json + sprint-logs/{S}/{decisions,e2e-results,
// acceptance-matrix,refine,failures,gui-spec-*}.json) into typed Go
// structs the Sprint Dashboard handler can serve back over REST.
//
// History: until S028 the dashboard parsed the original Markdown files
// (`docs/ROADMAP.md` + `decisions.md` etc.) with hand-rolled regular
// expressions and dual JP/EN heading prefixes. The sprint-runner skill
// migrated to JSON-canonical data so we deleted the regex-based parsers
// and the i18n compatibility layer (S016-fix-1) — JSON has a single
// schema and unmarshaling is enough.
//
// Schema authority lives in:
//   - ~/.claude/skills/sprint/references/ROADMAP_SCHEMA.json
//   - ~/.claude/skills/sprint/references/SPRINT_LOGS_SCHEMA.json
//
// All on-disk types are unmarshaled into the `*Doc` shapes in this file,
// then projected onto the FE-facing structs (`Roadmap`, `Sprint`,
// `Story`, `DecisionEntry`, ...) which the handler returns as JSON.
package parser

// ---------------------------------------------------------------------------
// FE-facing wire types (kept stable so the React dashboard didn't need to
// be rewritten for S028). The JSON tags must match the TypeScript types
// in frontend/src/tabs/sprint/types.ts.
// ---------------------------------------------------------------------------

// Roadmap is the top-level structured view served by GET /sprint/overview
// and /sprint/dependencies.
type Roadmap struct {
	Title        string         `json:"title"`
	Description  string         `json:"description,omitempty"`
	Progress     Progress       `json:"progress"`
	ExecutionRaw string         `json:"executionRaw,omitempty"`
	Sprints      []Sprint       `json:"sprints"`
	Dependencies []Dependency   `json:"dependencies"`
	Backlog      []BacklogEntry `json:"backlog"`
	ParseErrors  []ParseError   `json:"parseErrors,omitempty"`
}

// Progress is the "{done}/{total} ({percent}%)" header on Overview.
type Progress struct {
	Total      int     `json:"total"`
	Done       int     `json:"done"`
	InProgress int     `json:"inProgress"`
	Remaining  int     `json:"remaining"`
	Percent    float64 `json:"percent"`
}

// Sprint is one entry under sprints{} in ROADMAP.json.
type Sprint struct {
	ID          string  `json:"id"`
	Title       string  `json:"title"`
	Status      string  `json:"status"`
	StatusKind  string  `json:"statusKind"`
	Description string  `json:"description,omitempty"`
	Milestone   bool    `json:"milestone,omitempty"`
	Stories     []Story `json:"stories"`
	ParseError  string  `json:"parseError,omitempty"`
	// LineRange is preserved for FE compatibility — JSON has no notion
	// of source-line ranges, so we always emit [0,0]. Keeping the field
	// avoids a breaking change in the wire protocol.
	LineRange [2]int `json:"lineRange"`
}

// Story is one entry under stories{} for a Sprint.
type Story struct {
	ID                 string       `json:"id"`
	Title              string       `json:"title"`
	Status             string       `json:"status"`
	StatusKind         string       `json:"statusKind"`
	UserStory          string       `json:"userStory,omitempty"`
	BlockedReason      string       `json:"blockedReason,omitempty"`
	AcceptanceCriteria []Acceptance `json:"acceptanceCriteria"`
	Tasks              []Task       `json:"tasks"`
}

// Acceptance is one acceptance_criteria[] entry. Status mirrors the
// schema enum (pass / fail / pending / no_test); `Done` is set true when
// status == "pass" so the existing FE rendering ("✓" / "○") keeps
// working without changes.
type Acceptance struct {
	ID          string `json:"id,omitempty"`
	Description string `json:"description,omitempty"`
	Test        string `json:"test,omitempty"`
	Status      string `json:"status,omitempty"`
	Done        bool   `json:"done"`
	// Text is a synthesized "id: description" string so the existing FE
	// (which renders {ac.text}) needs no changes.
	Text string `json:"text"`
}

// Task is one entry under tasks{} for a Story.
type Task struct {
	ID          string `json:"id,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	Status      string `json:"status,omitempty"`
	Done        bool   `json:"done"`
	Text        string `json:"text"`
}

// Dependency is one entry under dependencies{}. Refs are the prerequisite
// sprint IDs (depends_on); the dependent sprint is recorded in the
// from-side. Text is "{from} depends on {a, b, c}: {reason}" so the FE
// renders it without restructuring.
type Dependency struct {
	From string   `json:"from"`
	Refs []string `json:"refs"`
	Text string   `json:"text"`
}

// BacklogEntry mirrors backlog[] in ROADMAP.json.
type BacklogEntry struct {
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	AddedIn     string `json:"addedIn,omitempty"`
	Reason      string `json:"reason,omitempty"`
	// Done is always false in the JSON form (the schema has no
	// completion state for backlog items) but is preserved so the FE
	// still has a stable shape.
	Done bool `json:"done"`
	// Text is a synthesized "title — description" string for FE display.
	Text   string `json:"text"`
	Source string `json:"source,omitempty"`
}

// ParseError records a problem the parser ran into. Always section-
// scoped, fail-safe: missing files / unknown keys / partial documents
// don't take the whole response down — they just appear here.
type ParseError struct {
	Section string `json:"section"`
	Detail  string `json:"detail"`
	// Line / Column hints for JSON syntax errors. 0 when not available.
	Line   int `json:"line,omitempty"`
	Column int `json:"column,omitempty"`
}

// DecisionEntry is one decisions[].
type DecisionEntry struct {
	SprintID   string `json:"sprintId"`
	Category   string `json:"category"`
	Title      string `json:"title,omitempty"`
	Body       string `json:"body"`
	Reference  string `json:"reference,omitempty"`
	Timestamp  string `json:"timestamp,omitempty"`
	NeedsHuman bool   `json:"needsHuman,omitempty"`
}

// AcceptanceMatrix is a parsed acceptance-matrix.json.
type AcceptanceMatrix struct {
	SprintID string                `json:"sprintId"`
	Rows     []AcceptanceMatrixRow `json:"rows"`
}

// AcceptanceMatrixRow keys an AC ID to its test status.
type AcceptanceMatrixRow struct {
	ACID   string `json:"acId"`
	Story  string `json:"story,omitempty"`
	TestID string `json:"testId,omitempty"`
	Status string `json:"status"`
	Notes  string `json:"notes,omitempty"`
}

// E2EResults is a flattened summary of e2e-results.json bucketed into
// mock / e2e / acceptance to match the Sprint Detail screen.
//
// JSON canonical only has a single top-level summary, so we infer the
// bucket from each test's filename:
//   - "*.mock.spec.*"   → mock
//   - "*.e2e.spec.*"    → e2e
//   - "tests/acceptance/*", "*acceptance*"  → acceptance
//   - everything else → e2e (best-effort default).
type E2EResults struct {
	SprintID   string      `json:"sprintId"`
	Mock       TestSummary `json:"mock"`
	E2E        TestSummary `json:"e2e"`
	Acceptance TestSummary `json:"acceptance"`
}

// TestSummary is one bucket (mock / e2e / acceptance) tally.
type TestSummary struct {
	Total  int `json:"total"`
	Passed int `json:"passed"`
	Failed int `json:"failed"`
}

// RefineEntry is one refinements[] entry.
type RefineEntry struct {
	SprintID    string   `json:"sprintId"`
	Number      int      `json:"number"`
	Title       string   `json:"title,omitempty"`
	Body        string   `json:"body,omitempty"`
	Files       []string `json:"files,omitempty"`
	TestsRerun  []string `json:"testsRerun,omitempty"`
	TestsPassed bool     `json:"testsPassed,omitempty"`
}

// FailureEntry is one failures[] entry from failures.json.
type FailureEntry struct {
	SprintID   string           `json:"sprintId"`
	Story      string           `json:"story,omitempty"`
	Type       string           `json:"type,omitempty"`
	Summary    string           `json:"summary,omitempty"`
	Attempts   []FailureAttempt `json:"attempts,omitempty"`
	Resolution string           `json:"resolution,omitempty"`
}

// FailureAttempt is one attempts[] entry.
type FailureAttempt struct {
	Approach string `json:"approach,omitempty"`
	Result   string `json:"result,omitempty"`
}

// GUISpec mirrors gui-spec-{StoryID}.json.
type GUISpec struct {
	SprintID           string                  `json:"sprintId"`
	Story              string                  `json:"story,omitempty"`
	StateDiagram       string                  `json:"stateDiagram,omitempty"`
	Scenarios          map[string]any          `json:"scenarios,omitempty"`
	EndpointContracts  []GUIEndpointContract   `json:"endpointContracts,omitempty"`
	TestFiles          map[string]string       `json:"testFiles,omitempty"`
}

// GUIEndpointContract is one endpoint_contracts[] entry.
type GUIEndpointContract struct {
	Path           string         `json:"path"`
	Method         string         `json:"method"`
	Registered     bool           `json:"registered"`
	RequestFields  map[string]any `json:"requestFields,omitempty"`
	ResponseFields map[string]any `json:"responseFields,omitempty"`
}

// DecisionsLog is the parser output for one decisions.json file.
type DecisionsLog struct {
	SprintID    string          `json:"sprintId"`
	Entries     []DecisionEntry `json:"entries"`
	ParseErrors []ParseError    `json:"parseErrors,omitempty"`
}

// ---------------------------------------------------------------------------
// On-disk JSON shapes — exactly mirror the schemas. We don't expose these
// to the FE; they're only used to unmarshal and then projected.
// ---------------------------------------------------------------------------

// roadmapDoc mirrors docs/ROADMAP.json.
type roadmapDoc struct {
	Project        string                       `json:"project"`
	Description    string                       `json:"description"`
	Progress       roadmapProgressDoc           `json:"progress"`
	ExecutionOrder []string                     `json:"execution_order"`
	Sprints        map[string]roadmapSprintDoc  `json:"sprints"`
	Dependencies   map[string]roadmapDepDoc     `json:"dependencies"`
	Backlog        []roadmapBacklogDoc          `json:"backlog"`
}

type roadmapProgressDoc struct {
	CurrentSprint string  `json:"current_sprint"`
	Total         int     `json:"total"`
	Done          int     `json:"done"`
	InProgress    int     `json:"in_progress"`
	Remaining     int     `json:"remaining"`
	Percentage    float64 `json:"percentage"`
}

type roadmapSprintDoc struct {
	Title       string                      `json:"title"`
	Status      string                      `json:"status"`
	Description string                      `json:"description"`
	Milestone   bool                        `json:"milestone"`
	Stories     map[string]roadmapStoryDoc  `json:"stories"`
}

type roadmapStoryDoc struct {
	Title              string                  `json:"title"`
	Status             string                  `json:"status"`
	UserStory          string                  `json:"user_story"`
	BlockedReason      string                  `json:"blocked_reason"`
	AcceptanceCriteria []roadmapACDoc          `json:"acceptance_criteria"`
	Tasks              map[string]roadmapTaskDoc `json:"tasks"`
}

type roadmapACDoc struct {
	ID          string `json:"id"`
	Description string `json:"description"`
	Test        string `json:"test"`
	Status      string `json:"status"`
}

type roadmapTaskDoc struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Status      string `json:"status"`
}

type roadmapDepDoc struct {
	DependsOn []string `json:"depends_on"`
	Reason    string   `json:"reason"`
}

type roadmapBacklogDoc struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	AddedIn     string `json:"added_in"`
	Reason      string `json:"reason"`
}

// decisionsDoc mirrors docs/sprint-logs/{S}/decisions.json.
type decisionsDoc struct {
	Sprint    string             `json:"sprint"`
	Decisions []decisionEntryDoc `json:"decisions"`
}

type decisionEntryDoc struct {
	Timestamp string `json:"timestamp"`
	Category  string `json:"category"`
	Title     string `json:"title"`
	Detail    string `json:"detail"`
	Reference string `json:"reference"`
}

// acceptanceMatrixDoc mirrors docs/sprint-logs/{S}/acceptance-matrix.json.
type acceptanceMatrixDoc struct {
	Sprint string                  `json:"sprint"`
	Matrix map[string][]matrixRowDoc `json:"matrix"`
}

type matrixRowDoc struct {
	Criterion   string `json:"criterion"`
	Description string `json:"description"`
	TestFile    string `json:"test_file"`
	TestName    string `json:"test_name"`
	Status      string `json:"status"`
	Error       string `json:"error"`
}

// e2eResultsDoc mirrors docs/sprint-logs/{S}/e2e-results.json.
type e2eResultsDoc struct {
	Sprint        string         `json:"sprint"`
	RunAt         string         `json:"run_at"`
	ServerCommand string         `json:"server_command"`
	Summary       e2eSummaryDoc  `json:"summary"`
	Tests         []e2eTestDoc   `json:"tests"`
}

type e2eSummaryDoc struct {
	Total int `json:"total"`
	Pass  int `json:"pass"`
	Fail  int `json:"fail"`
	Skip  int `json:"skip"`
}

type e2eTestDoc struct {
	Name       string `json:"name"`
	File       string `json:"file"`
	Status     string `json:"status"`
	DurationMS *int   `json:"duration_ms"`
	Error      string `json:"error"`
}

// refineDoc mirrors docs/sprint-logs/{S}/refine.json.
type refineDoc struct {
	Sprint      string             `json:"sprint"`
	Refinements []refineEntryDoc   `json:"refinements"`
}

type refineEntryDoc struct {
	ID          int      `json:"id"`
	Feedback    string   `json:"feedback"`
	Change      string   `json:"change"`
	Files       []string `json:"files"`
	TestsRerun  []string `json:"tests_rerun"`
	TestsPassed bool     `json:"tests_passed"`
}

// failuresDoc mirrors docs/sprint-logs/{S}/failures.json.
type failuresDoc struct {
	Sprint   string             `json:"sprint"`
	Failures []failureEntryDoc  `json:"failures"`
}

type failureEntryDoc struct {
	Story      string              `json:"story"`
	Type       string              `json:"type"`
	Summary    string              `json:"summary"`
	Attempts   []failureAttemptDoc `json:"attempts"`
	Resolution string              `json:"resolution"`
}

type failureAttemptDoc struct {
	Approach string `json:"approach"`
	Result   string `json:"result"`
}

// guiSpecDoc mirrors docs/sprint-logs/{S}/gui-spec-{Story}.json.
type guiSpecDoc struct {
	Sprint            string                  `json:"sprint"`
	Story             string                  `json:"story"`
	StateDiagram      string                  `json:"state_diagram"`
	Scenarios         map[string]any          `json:"scenarios"`
	EndpointContracts []guiEndpointContractDoc `json:"endpoint_contracts"`
	TestFiles         map[string]string       `json:"test_files"`
}

type guiEndpointContractDoc struct {
	Path           string         `json:"path"`
	Method         string         `json:"method"`
	Registered     bool           `json:"registered"`
	RequestFields  map[string]any `json:"request_fields"`
	ResponseFields map[string]any `json:"response_fields"`
}
