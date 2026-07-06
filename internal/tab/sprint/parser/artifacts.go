// artifacts.go — parsers for the CURRENT sprint/autopilot skill artifact
// set (Se173ef). Until Se173ef the Sprint tab only read the pre-2026-06
// artifacts (decisions / acceptance-matrix / e2e-results / refine /
// failures). The skill has since made these the canonical trust-source
// files, and the tab was blind to all of them:
//
//   - verify-run.json          machine-authored test verdict (exit codes)
//   - verification-report.json independent verifier verdict + AC findings
//   - done-judgment.json       6(-8) guard done judgment per Story
//   - compromises.json         notify-after concessions (severity-graded)
//   - comprehension-report.md  milestone "what changed / why" narrative
//   - prototype-review.json    approved prototype screens + decisions
//   - reopen.json              Sprint re-open history (Review Mode ①)
//   - scenario-{Story}.json    non-GUI user scenarios
//   - <anything>.json          generic extra smoke logs (fail-open)
//
// Every parser is fail-safe: a malformed file yields a nil result (the
// caller omits the section) rather than crashing the request path. Schema
// authority lives in ~/.claude/skills/sprint/references/*SCHEMA*.json
// (see Story Se173ef-4 which realigns SPRINT_LOGS_SCHEMA.json).
package parser

import (
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

// ---------------------------------------------------------------------------
// verify-run.json  (machine-authored test verdict)
// ---------------------------------------------------------------------------

// VerifyRun mirrors verify-run.json. OverallMachineStatus is the headline
// the detail view renders large; each run carries its own pass/fail and a
// log path the FE can lazily expand.
type VerifyRun struct {
	Sprint               string           `json:"sprint,omitempty"`
	CommandSource        string           `json:"commandSource,omitempty"`
	Runs                 []VerifyRunEntry `json:"runs"`
	OverallMachineStatus string           `json:"overallMachineStatus"`
}

// VerifyRunEntry is one runs[] entry.
type VerifyRunEntry struct {
	Name          string       `json:"name"`
	Command       string       `json:"command,omitempty"`
	ExitCode      int          `json:"exitCode"`
	Log           string       `json:"log,omitempty"`
	MachineStatus string       `json:"machineStatus"`
	Junit         *VerifyJunit `json:"junit,omitempty"`
}

// VerifyJunit is the optional JUnit cross-check tally.
type VerifyJunit struct {
	Total   int `json:"total"`
	Passed  int `json:"passed"`
	Failed  int `json:"failed"`
	Errored int `json:"errored"`
	Skipped int `json:"skipped"`
}

type verifyRunDoc struct {
	Sprint               string `json:"sprint"`
	CommandSource        string `json:"command_source"`
	OverallMachineStatus string `json:"overall_machine_status"`
	Runs                 []struct {
		Name          string `json:"name"`
		Command       string `json:"command"`
		ExitCode      int    `json:"exit_code"`
		Log           string `json:"log"`
		MachineStatus string `json:"machine_status"`
		Junit         *struct {
			Total   int `json:"total"`
			Passed  int `json:"passed"`
			Failed  int `json:"failed"`
			Errored int `json:"errored"`
			Skipped int `json:"skipped"`
		} `json:"junit"`
	} `json:"runs"`
}

// ParseVerifyRun returns nil when the file is missing/empty/corrupt so the
// handler omits the section (backward compatibility, §2.9).
func ParseVerifyRun(src []byte) *VerifyRun {
	if len(src) == 0 {
		return nil
	}
	var doc verifyRunDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		return nil
	}
	out := &VerifyRun{
		Sprint:               doc.Sprint,
		CommandSource:        doc.CommandSource,
		OverallMachineStatus: doc.OverallMachineStatus,
		Runs:                 []VerifyRunEntry{},
	}
	for _, r := range doc.Runs {
		e := VerifyRunEntry{
			Name:          r.Name,
			Command:       r.Command,
			ExitCode:      r.ExitCode,
			Log:           r.Log,
			MachineStatus: r.MachineStatus,
		}
		if r.Junit != nil {
			e.Junit = &VerifyJunit{
				Total:   r.Junit.Total,
				Passed:  r.Junit.Passed,
				Failed:  r.Junit.Failed,
				Errored: r.Junit.Errored,
				Skipped: r.Junit.Skipped,
			}
		}
		out.Runs = append(out.Runs, e)
	}
	return out
}

// ---------------------------------------------------------------------------
// verification-report.json  (independent verifier)
// ---------------------------------------------------------------------------

// VerificationReport mirrors verification-report.json. This is the trust
// source: where it disagrees with the implementer's self-report the
// verifier wins, and overlooked_by_autopilot items are surfaced verbatim.
type VerificationReport struct {
	Sprint        string                `json:"sprint,omitempty"`
	Overall       string                `json:"overall"`
	VerifiedAt    string                `json:"verifiedAt,omitempty"`
	VerifierModel string                `json:"verifierModel,omitempty"`
	Summary       VerificationSummary   `json:"summary"`
	Stories       []VerificationStory   `json:"stories"`
	Findings      []VerificationFinding `json:"findings"`
	ADRStatus     string                `json:"adrStatus,omitempty"`
	ADRDetail     string                `json:"adrDetail,omitempty"`
}

// VerificationSummary is the summary{} block.
type VerificationSummary struct {
	ACFailures        int `json:"acFailures"`
	ACWarnings        int `json:"acWarnings"`
	ForbiddenWarnings int `json:"forbiddenWarnings"`
	ForbiddenFailures int `json:"forbiddenFailures"`
	ADRConflicts      int `json:"adrConflicts"`
	OverlookedCount   int `json:"overlookedCount"`
}

// VerificationStory is one stories{} entry.
type VerificationStory struct {
	Story                     string             `json:"story"`
	Verdict                   string             `json:"verdict"`
	ACFindings                []VerificationAC   `json:"acFindings"`
	ForbiddenCategoryFindings []VerificationFind `json:"forbiddenCategoryFindings"`
}

// VerificationAC is one ac_findings[] entry.
type VerificationAC struct {
	AC                    string `json:"ac"`
	Status                string `json:"status"`
	Evidence              string `json:"evidence,omitempty"`
	OverlookedByAutopilot bool   `json:"overlookedByAutopilot,omitempty"`
	RecommendedAction     string `json:"recommendedAction,omitempty"`
}

// VerificationFind is one forbidden_category_findings[] entry.
type VerificationFind struct {
	Category              string `json:"category,omitempty"`
	Subtype               string `json:"subtype,omitempty"`
	Verdict               string `json:"verdict"`
	Detail                string `json:"detail,omitempty"`
	OverlookedByAutopilot bool   `json:"overlookedByAutopilot,omitempty"`
	RecommendedAction     string `json:"recommendedAction,omitempty"`
}

// VerificationFinding is one top-level findings[] entry.
type VerificationFinding struct {
	Category              string `json:"category,omitempty"`
	Story                 string `json:"story,omitempty"`
	AC                    string `json:"ac,omitempty"`
	Verdict               string `json:"verdict"`
	Detail                string `json:"detail,omitempty"`
	OverlookedByAutopilot bool   `json:"overlookedByAutopilot,omitempty"`
	RecommendedAction     string `json:"recommendedAction,omitempty"`
}

type verificationReportDoc struct {
	Sprint        string `json:"sprint"`
	Overall       string `json:"overall"`
	VerifiedAt    string `json:"verified_at"`
	VerifierModel string `json:"verifier_model"`
	Stories       map[string]struct {
		Verdict    string `json:"verdict"`
		ACFindings []struct {
			AC                    string `json:"ac"`
			Status                string `json:"status"`
			Evidence              string `json:"evidence"`
			OverlookedByAutopilot bool   `json:"overlooked_by_autopilot"`
			RecommendedAction     string `json:"recommended_action"`
		} `json:"ac_findings"`
		ForbiddenCategoryFindings []struct {
			Category              string `json:"category"`
			Subtype               string `json:"subtype"`
			Verdict               string `json:"verdict"`
			Detail                string `json:"detail"`
			OverlookedByAutopilot bool   `json:"overlooked_by_autopilot"`
			RecommendedAction     string `json:"recommended_action"`
		} `json:"forbidden_category_findings"`
	} `json:"stories"`
	Findings []struct {
		Category              string `json:"category"`
		Story                 string `json:"story"`
		AC                    string `json:"ac"`
		Verdict               string `json:"verdict"`
		Detail                string `json:"detail"`
		OverlookedByAutopilot bool   `json:"overlooked_by_autopilot"`
		RecommendedAction     string `json:"recommended_action"`
	} `json:"findings"`
	ADRConformance *struct {
		Status string `json:"status"`
		Detail string `json:"detail"`
	} `json:"adr_conformance"`
	Summary *struct {
		ACFailures        int `json:"ac_failures"`
		ACWarnings        int `json:"ac_warnings"`
		ForbiddenWarnings int `json:"forbidden_warnings"`
		ForbiddenFailures int `json:"forbidden_failures"`
		ADRConflicts      int `json:"adr_conflicts"`
		OverlookedCount   int `json:"overlooked_count"`
	} `json:"summary"`
}

// ParseVerificationReport returns nil for a missing/corrupt file.
func ParseVerificationReport(src []byte) *VerificationReport {
	if len(src) == 0 {
		return nil
	}
	var doc verificationReportDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		return nil
	}
	out := &VerificationReport{
		Sprint:        doc.Sprint,
		Overall:       doc.Overall,
		VerifiedAt:    doc.VerifiedAt,
		VerifierModel: doc.VerifierModel,
		Stories:       []VerificationStory{},
		Findings:      []VerificationFinding{},
	}
	// Stories map → deterministic slice.
	storyKeys := make([]string, 0, len(doc.Stories))
	for k := range doc.Stories {
		storyKeys = append(storyKeys, k)
	}
	sort.Strings(storyKeys)
	for _, k := range storyKeys {
		s := doc.Stories[k]
		vs := VerificationStory{Story: k, Verdict: s.Verdict}
		for _, ac := range s.ACFindings {
			vs.ACFindings = append(vs.ACFindings, VerificationAC{
				AC:                    ac.AC,
				Status:                ac.Status,
				Evidence:              ac.Evidence,
				OverlookedByAutopilot: ac.OverlookedByAutopilot,
				RecommendedAction:     ac.RecommendedAction,
			})
		}
		for _, f := range s.ForbiddenCategoryFindings {
			vs.ForbiddenCategoryFindings = append(vs.ForbiddenCategoryFindings, VerificationFind{
				Category:              f.Category,
				Subtype:               f.Subtype,
				Verdict:               f.Verdict,
				Detail:                f.Detail,
				OverlookedByAutopilot: f.OverlookedByAutopilot,
				RecommendedAction:     f.RecommendedAction,
			})
		}
		out.Stories = append(out.Stories, vs)
	}
	for _, f := range doc.Findings {
		out.Findings = append(out.Findings, VerificationFinding{
			Category:              f.Category,
			Story:                 f.Story,
			AC:                    f.AC,
			Verdict:               f.Verdict,
			Detail:                f.Detail,
			OverlookedByAutopilot: f.OverlookedByAutopilot,
			RecommendedAction:     f.RecommendedAction,
		})
	}
	if doc.ADRConformance != nil {
		out.ADRStatus = doc.ADRConformance.Status
		out.ADRDetail = doc.ADRConformance.Detail
	}
	if doc.Summary != nil {
		out.Summary = VerificationSummary{
			ACFailures:        doc.Summary.ACFailures,
			ACWarnings:        doc.Summary.ACWarnings,
			ForbiddenWarnings: doc.Summary.ForbiddenWarnings,
			ForbiddenFailures: doc.Summary.ForbiddenFailures,
			ADRConflicts:      doc.Summary.ADRConflicts,
			OverlookedCount:   doc.Summary.OverlookedCount,
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// done-judgment.json  (6-8 guard done judgment)
// ---------------------------------------------------------------------------

// DoneJudgment mirrors done-judgment.json. The guard keys are freeform in
// the wild (guard_1_not_user_review_required, guard1_user_review_required_
// not_done, ...) so we normalize each into {num, label, status, detail}.
type DoneJudgment struct {
	Sprint       string              `json:"sprint,omitempty"`
	Precondition *DonePrecondition   `json:"precondition,omitempty"`
	Stories      []DoneJudgmentStory `json:"stories"`
}

// DonePrecondition is the rolling-wave coarse-sprint guard block.
type DonePrecondition struct {
	DetailLevel     string `json:"detailLevel,omitempty"`
	StoriesNonempty bool   `json:"storiesNonempty"`
	OK              bool   `json:"ok"`
}

// DoneJudgmentStory is one stories{} entry.
type DoneJudgmentStory struct {
	Story   string  `json:"story"`
	Guards  []Guard `json:"guards"`
	Overall string  `json:"overall"`
	Note    string  `json:"note,omitempty"`
}

// Guard is a single normalized guard result.
type Guard struct {
	Num    int    `json:"num"`
	Key    string `json:"key"`
	Label  string `json:"label"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

var guardNumRe = regexp.MustCompile(`^guard[_]?(\d+)`)

// ParseDoneJudgment returns nil for a missing/corrupt file.
func ParseDoneJudgment(src []byte) *DoneJudgment {
	if len(src) == 0 {
		return nil
	}
	// Decode loosely: stories values are objects of freeform guard keys.
	var doc struct {
		Sprint       string `json:"sprint"`
		Precondition *struct {
			DetailLevel     string `json:"detail_level"`
			StoriesNonempty bool   `json:"stories_nonempty"`
			OK              bool   `json:"ok"`
		} `json:"precondition"`
		Stories map[string]map[string]json.RawMessage `json:"stories"`
	}
	if err := json.Unmarshal(src, &doc); err != nil {
		return nil
	}
	out := &DoneJudgment{Sprint: doc.Sprint, Stories: []DoneJudgmentStory{}}
	if doc.Precondition != nil {
		out.Precondition = &DonePrecondition{
			DetailLevel:     doc.Precondition.DetailLevel,
			StoriesNonempty: doc.Precondition.StoriesNonempty,
			OK:              doc.Precondition.OK,
		}
	}
	storyKeys := make([]string, 0, len(doc.Stories))
	for k := range doc.Stories {
		storyKeys = append(storyKeys, k)
	}
	sort.Strings(storyKeys)
	for _, sk := range storyKeys {
		fields := doc.Stories[sk]
		st := DoneJudgmentStory{Story: sk, Guards: []Guard{}}
		for key, raw := range fields {
			switch key {
			case "overall":
				st.Overall = jsonString(raw)
				continue
			case "note":
				st.Note = jsonString(raw)
				continue
			}
			if !strings.HasPrefix(key, "guard") {
				continue
			}
			val := jsonString(raw)
			g := Guard{
				Key:    key,
				Label:  guardLabel(key),
				Status: leadingStatus(val),
				Detail: val,
			}
			if m := guardNumRe.FindStringSubmatch(key); m != nil {
				g.Num = atoiSafe(m[1])
			}
			st.Guards = append(st.Guards, g)
		}
		sort.SliceStable(st.Guards, func(i, j int) bool {
			if st.Guards[i].Num != st.Guards[j].Num {
				return st.Guards[i].Num < st.Guards[j].Num
			}
			return st.Guards[i].Key < st.Guards[j].Key
		})
		out.Stories = append(out.Stories, st)
	}
	return out
}

// guardLabel strips the "guard_N_" / "guardN_" prefix and de-snakes the rest.
func guardLabel(key string) string {
	s := guardNumRe.ReplaceAllString(key, "")
	s = strings.TrimPrefix(s, "_")
	s = strings.ReplaceAll(s, "_", " ")
	return strings.TrimSpace(s)
}

// leadingStatus extracts the pass/fail/warn/n-a token that guard values
// begin with (e.g. "pass — roadmap did not…" → "pass").
func leadingStatus(s string) string {
	low := strings.ToLower(strings.TrimSpace(s))
	switch {
	case strings.HasPrefix(low, "pass"):
		return "pass"
	case strings.HasPrefix(low, "fail"):
		return "fail"
	case strings.HasPrefix(low, "warn"):
		return "warn"
	case strings.HasPrefix(low, "n/a"), strings.HasPrefix(low, "n-a"), strings.HasPrefix(low, "na "):
		return "n/a"
	case low == "":
		return ""
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// compromises.json
// ---------------------------------------------------------------------------

// Compromises mirrors compromises.json. Real files put the arrays at the
// top level; the schema example nests them under milestone_summary{}. We
// accept both.
type Compromises struct {
	StoppedAt    string        `json:"stoppedAt,omitempty"`
	Compromises  []Compromise  `json:"compromises"`
	Blockers     []Blocker     `json:"blockers"`
	ScopeChanges []ScopeChange `json:"scopeChanges"`
}

// Compromise is one compromises[] entry.
type Compromise struct {
	Type                  string `json:"type,omitempty"`
	Severity              string `json:"severity,omitempty"`
	Story                 string `json:"story,omitempty"`
	File                  string `json:"file,omitempty"`
	Rationale             string `json:"rationale,omitempty"`
	DiffSummary           string `json:"diffSummary,omitempty"`
	RecommendedAction     string `json:"recommendedAction,omitempty"`
	ADRRef                string `json:"adrRef,omitempty"`
	OverlookedByAutopilot bool   `json:"overlookedByAutopilot,omitempty"`
}

// Blocker is one blockers_encountered[] entry.
type Blocker struct {
	Type       string `json:"type,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Resolution string `json:"resolution,omitempty"`
}

// ScopeChange is one scope_changes[] entry.
type ScopeChange struct {
	Type       string `json:"type,omitempty"`
	Severity   string `json:"severity,omitempty"`
	Detail     string `json:"detail,omitempty"`
	Resolution string `json:"resolution,omitempty"`
}

type compromiseEntryDoc struct {
	Type                  string `json:"type"`
	Severity              string `json:"severity"`
	Story                 string `json:"story"`
	File                  string `json:"file"`
	Rationale             string `json:"rationale"`
	DiffSummary           string `json:"diff_summary"`
	RecommendedAction     string `json:"recommended_action"`
	ADRRef                string `json:"adr_ref"`
	OverlookedByAutopilot bool   `json:"overlooked_by_autopilot"`
}

type blockerDoc struct {
	Type       string `json:"type"`
	Severity   string `json:"severity"`
	Detail     string `json:"detail"`
	Resolution string `json:"resolution"`
}

type compromiseBody struct {
	StoppedAt    string               `json:"stopped_at"`
	Compromises  []compromiseEntryDoc `json:"compromises"`
	Blockers     []blockerDoc         `json:"blockers_encountered"`
	ScopeChanges []blockerDoc         `json:"scope_changes"`
}

type compromisesDoc struct {
	compromiseBody
	MilestoneSummary *compromiseBody `json:"milestone_summary"`
}

// ParseCompromises returns nil for a missing/corrupt file.
func ParseCompromises(src []byte) *Compromises {
	if len(src) == 0 {
		return nil
	}
	var doc compromisesDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		return nil
	}
	body := doc.compromiseBody
	// If arrays only appear under milestone_summary, prefer that.
	if len(body.Compromises) == 0 && len(body.Blockers) == 0 && len(body.ScopeChanges) == 0 && doc.MilestoneSummary != nil {
		body = *doc.MilestoneSummary
	}
	out := &Compromises{
		StoppedAt:    body.StoppedAt,
		Compromises:  []Compromise{},
		Blockers:     []Blocker{},
		ScopeChanges: []ScopeChange{},
	}
	for _, c := range body.Compromises {
		out.Compromises = append(out.Compromises, Compromise{
			Type:                  c.Type,
			Severity:              c.Severity,
			Story:                 c.Story,
			File:                  c.File,
			Rationale:             c.Rationale,
			DiffSummary:           c.DiffSummary,
			RecommendedAction:     c.RecommendedAction,
			ADRRef:                c.ADRRef,
			OverlookedByAutopilot: c.OverlookedByAutopilot,
		})
	}
	for _, b := range body.Blockers {
		out.Blockers = append(out.Blockers, Blocker{Type: b.Type, Severity: b.Severity, Detail: b.Detail, Resolution: b.Resolution})
	}
	for _, s := range body.ScopeChanges {
		out.ScopeChanges = append(out.ScopeChanges, ScopeChange{Type: s.Type, Severity: s.Severity, Detail: s.Detail, Resolution: s.Resolution})
	}
	return out
}

// ---------------------------------------------------------------------------
// reopen.json
// ---------------------------------------------------------------------------

// Reopen mirrors reopen.json (a single object; we return it as a 0/1 slice
// so callers can uniformly render a list).
type Reopen struct {
	SprintID                   string       `json:"sprintId,omitempty"`
	ReopenedAt                 string       `json:"reopenedAt,omitempty"`
	TriggeredBy                string       `json:"triggeredBy,omitempty"`
	Milestone                  string       `json:"milestone,omitempty"`
	Reason                     string       `json:"reason,omitempty"`
	AffectedAcceptanceCriteria []string     `json:"affectedAcceptanceCriteria,omitempty"`
	AddedTasks                 []ReopenTask `json:"addedTasks,omitempty"`
}

// ReopenTask is one added_tasks[] entry.
type ReopenTask struct {
	Story       string `json:"story,omitempty"`
	TaskID      string `json:"taskId,omitempty"`
	Description string `json:"description,omitempty"`
}

type reopenDoc struct {
	SprintID                   string   `json:"sprint_id"`
	ReopenedAt                 string   `json:"reopened_at"`
	TriggeredBy                string   `json:"triggered_by"`
	Milestone                  string   `json:"milestone"`
	Reason                     string   `json:"reason"`
	AffectedAcceptanceCriteria []string `json:"affected_acceptance_criteria"`
	AddedTasks                 []struct {
		Story       string `json:"story"`
		TaskID      string `json:"task_id"`
		Description string `json:"description"`
	} `json:"added_tasks"`
}

// ParseReopen returns nil for a missing/corrupt file.
func ParseReopen(src []byte) *Reopen {
	if len(src) == 0 {
		return nil
	}
	var doc reopenDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		return nil
	}
	out := &Reopen{
		SprintID:                   doc.SprintID,
		ReopenedAt:                 doc.ReopenedAt,
		TriggeredBy:                doc.TriggeredBy,
		Milestone:                  doc.Milestone,
		Reason:                     doc.Reason,
		AffectedAcceptanceCriteria: doc.AffectedAcceptanceCriteria,
	}
	for _, t := range doc.AddedTasks {
		out.AddedTasks = append(out.AddedTasks, ReopenTask{Story: t.Story, TaskID: t.TaskID, Description: t.Description})
	}
	return out
}

// ---------------------------------------------------------------------------
// comprehension-report.md
// ---------------------------------------------------------------------------

// Comprehension is the raw markdown of comprehension-report.md plus its
// extracted section headings (## / ###) for a quick table of contents.
type Comprehension struct {
	Markdown string   `json:"markdown"`
	Headings []string `json:"headings"`
}

var mdHeadingRe = regexp.MustCompile(`(?m)^#{1,3}\s+(.+?)\s*$`)

// ParseComprehension returns nil for empty input.
func ParseComprehension(src []byte) *Comprehension {
	if len(src) == 0 {
		return nil
	}
	md := string(src)
	out := &Comprehension{Markdown: md, Headings: []string{}}
	for _, m := range mdHeadingRe.FindAllStringSubmatch(md, -1) {
		h := strings.TrimSpace(m[1])
		// Skip the top-level title line and empty headings.
		if h == "" {
			continue
		}
		out.Headings = append(out.Headings, h)
	}
	return out
}

// ---------------------------------------------------------------------------
// prototype-review.json
// ---------------------------------------------------------------------------

// PrototypeReview mirrors prototype-review.json.
type PrototypeReview struct {
	SprintRange     []string          `json:"sprintRange,omitempty"`
	Screens         []PrototypeScreen `json:"screens"`
	DesignDecisions []string          `json:"designDecisions,omitempty"`
	ApprovedByUser  bool              `json:"approvedByUser"`
	ApprovedAt      string            `json:"approvedAt,omitempty"`
}

// PrototypeScreen is one screens[] entry.
type PrototypeScreen struct {
	File           string `json:"file"`
	Story          string `json:"story,omitempty"`
	FeedbackRounds int    `json:"feedbackRounds"`
	Approved       bool   `json:"approved"`
}

type prototypeReviewDoc struct {
	SprintRange []string `json:"sprint_range"`
	Screens     []struct {
		File           string `json:"file"`
		Story          string `json:"story"`
		FeedbackRounds int    `json:"feedback_rounds"`
		Approved       bool   `json:"approved"`
	} `json:"screens"`
	DesignDecisions []string `json:"design_decisions"`
	ApprovedByUser  bool     `json:"approved_by_user"`
	ApprovedAt      string   `json:"approved_at"`
}

// ParsePrototypeReview returns nil for a missing/corrupt file.
func ParsePrototypeReview(src []byte) *PrototypeReview {
	if len(src) == 0 {
		return nil
	}
	var doc prototypeReviewDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		return nil
	}
	out := &PrototypeReview{
		SprintRange:     doc.SprintRange,
		Screens:         []PrototypeScreen{},
		DesignDecisions: doc.DesignDecisions,
		ApprovedByUser:  doc.ApprovedByUser,
		ApprovedAt:      doc.ApprovedAt,
	}
	for _, s := range doc.Screens {
		out.Screens = append(out.Screens, PrototypeScreen{File: s.File, Story: s.Story, FeedbackRounds: s.FeedbackRounds, Approved: s.Approved})
	}
	return out
}

// ---------------------------------------------------------------------------
// scenario-{Story}.json
// ---------------------------------------------------------------------------

// ScenarioDoc is a lightweight projection of scenario-{Story}.json.
type ScenarioDoc struct {
	Story      string   `json:"story"`
	StoryType  string   `json:"storyType,omitempty"`
	UserRole   string   `json:"userRole,omitempty"`
	EntryPoint string   `json:"entryPoint,omitempty"`
	LinkedACs  []string `json:"linkedAcs,omitempty"`
	Count      int      `json:"count"`
}

type scenarioFileDoc struct {
	Story      string `json:"story"`
	StoryType  string `json:"story_type"`
	UserRole   string `json:"user_role"`
	EntryPoint string `json:"entry_point"`
	Scenarios  []struct {
		LinkedAC []string `json:"linked_ac"`
	} `json:"scenarios"`
}

// ParseScenario returns nil for a missing/corrupt file.
func ParseScenario(src []byte) *ScenarioDoc {
	if len(src) == 0 {
		return nil
	}
	var doc scenarioFileDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		return nil
	}
	out := &ScenarioDoc{
		Story:      doc.Story,
		StoryType:  doc.StoryType,
		UserRole:   doc.UserRole,
		EntryPoint: doc.EntryPoint,
		Count:      len(doc.Scenarios),
	}
	seen := map[string]struct{}{}
	for _, s := range doc.Scenarios {
		for _, ac := range s.LinkedAC {
			if _, dup := seen[ac]; dup {
				continue
			}
			seen[ac] = struct{}{}
			out.LinkedACs = append(out.LinkedACs, ac)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// generic additional smoke logs (AC-Se173ef-1-5)
// ---------------------------------------------------------------------------

// SmokeLog is a heuristic projection of an unrecognized *.json artifact in
// the sprint-log directory (e.g. deploy-test-smoke.json). We surface the
// filename plus a best-effort pass/fail/unknown verdict so future artifacts
// never silently vanish from the tab.
type SmokeLog struct {
	File    string `json:"file"`
	Kind    string `json:"kind,omitempty"`
	Overall string `json:"overall"` // pass | fail | unknown
}

// ClassifySmokeLog inspects the top-level "overall"/"status"/"result"/
// "verdict" string field of an arbitrary JSON doc and maps it to pass/fail/
// unknown. It never errors — an undecodable file is "unknown".
func ClassifySmokeLog(file string, src []byte) SmokeLog {
	out := SmokeLog{File: file, Overall: "unknown"}
	if len(src) == 0 {
		return out
	}
	var doc map[string]any
	if err := json.Unmarshal(src, &doc); err != nil {
		return out
	}
	if k, ok := doc["kind"].(string); ok {
		out.Kind = k
	}
	for _, key := range []string{"overall", "overall_machine_status", "status", "result", "verdict"} {
		if v, ok := doc[key].(string); ok {
			out.Overall = classifyPassFail(v)
			if out.Overall != "unknown" {
				return out
			}
		}
	}
	return out
}

func classifyPassFail(s string) string {
	up := strings.ToUpper(strings.TrimSpace(s))
	switch {
	case up == "PASS" || up == "PASSED" || up == "OK" || up == "SUCCESS" || strings.HasPrefix(up, "ALL PASS"):
		return "pass"
	case up == "FAIL" || up == "FAILED" || up == "ERROR":
		return "fail"
	}
	return "unknown"
}

// ---------------------------------------------------------------------------
// AC findings — the replacement for the defunct acceptance-matrix.json
// (AC-Se173ef-1-3). Derived from the ROADMAP AC status overlaid with the
// independent verifier's per-AC verdict + evidence. The verifier wins for
// display where the two disagree (trust-source-first).
// ---------------------------------------------------------------------------

// ACFindingRow is one row of the Stories & AC table.
type ACFindingRow struct {
	Story                 string `json:"story"`
	AC                    string `json:"ac"`
	Description           string `json:"description,omitempty"`
	Status                string `json:"status"` // pass | fail | warn | pending | no_test
	RoadmapStatus         string `json:"roadmapStatus,omitempty"`
	VerifierStatus        string `json:"verifierStatus,omitempty"`
	Evidence              string `json:"evidence,omitempty"`
	OverlookedByAutopilot bool   `json:"overlookedByAutopilot,omitempty"`
	RecommendedAction     string `json:"recommendedAction,omitempty"`
	ReopenedAt            string `json:"reopenedAt,omitempty"`
}

// BuildACFindings merges the ROADMAP Sprint's AC status with the verifier's
// per-AC findings. vr may be nil (older Sprints) — then it is ROADMAP-only.
func BuildACFindings(sprint Sprint, vr *VerificationReport) []ACFindingRow {
	// Index verifier ac_findings by AC id.
	vidx := map[string]VerificationAC{}
	if vr != nil {
		for _, s := range vr.Stories {
			for _, ac := range s.ACFindings {
				if ac.AC != "" {
					vidx[ac.AC] = ac
				}
			}
		}
	}
	rows := []ACFindingRow{}
	for _, st := range sprint.Stories {
		for _, ac := range st.AcceptanceCriteria {
			row := ACFindingRow{
				Story:         st.ID,
				AC:            ac.ID,
				Description:   ac.Description,
				Status:        normalizeACStatus(ac.Status),
				RoadmapStatus: normalizeACStatus(ac.Status),
				ReopenedAt:    ac.ReopenedAt,
			}
			if v, ok := vidx[ac.ID]; ok {
				row.VerifierStatus = strings.ToLower(strings.TrimSpace(v.Status))
				row.Evidence = v.Evidence
				row.OverlookedByAutopilot = v.OverlookedByAutopilot
				row.RecommendedAction = v.RecommendedAction
				// Verifier wins for the displayed status when it is
				// fail/warn (a passing test name never overrides a verifier
				// fail). A verifier "pass" corroborates the ROADMAP status.
				switch row.VerifierStatus {
				case "fail":
					row.Status = "fail"
				case "warn":
					if row.Status != "fail" {
						row.Status = "warn"
					}
				}
			}
			rows = append(rows, row)
		}
	}
	return rows
}

func normalizeACStatus(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "pass", "passed":
		return "pass"
	case "fail", "failed":
		return "fail"
	case "warn":
		return "warn"
	case "no_test", "no-test":
		return "no_test"
	case "pending", "":
		return "pending"
	}
	return "pending"
}

// ---------------------------------------------------------------------------
// tiny helpers
// ---------------------------------------------------------------------------

func jsonString(raw json.RawMessage) string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s
	}
	return strings.TrimSpace(string(raw))
}

func atoiSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return n
		}
		n = n*10 + int(r-'0')
	}
	return n
}
