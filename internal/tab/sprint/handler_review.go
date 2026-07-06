// handler_review.go — Se173ef cross-sprint aggregation endpoints that back
// the Option-A "Review" and "Milestones" tabs, plus the standalone
// "/backlog" feed and a "/log" fetcher for verify-run-*.log expansion.
//
// These are strictly additive to the original five endpoints (overview /
// sprints/{id} / dependencies / decisions / refine) — no existing response
// shape changes (DESIGN_PRINCIPLES: 既存 API レスポンス破壊的変更禁止). The
// tab remains a pure mirror of the local docs/sprint-logs data (priority_
// rule 1); it holds no state of its own.
package sprint

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tjst-t/palmux2/internal/tab/sprint/parser"
)

// ----------------------------------------------------------------------
// Review queue (cross-sprint)
// ----------------------------------------------------------------------

// ReviewResponse aggregates everything that needs a human decision across
// all sprints — the input to `autopilot review`.
type ReviewResponse struct {
	Counts          ReviewCounts        `json:"counts"`
	NeedsUserReview []StoryRef          `json:"needsUserReview"`
	Blocked         []StoryRef          `json:"blocked"`
	Compromises     []CompromiseRef     `json:"compromises"`
	Overlooked      []OverlookedRef     `json:"overlooked"`
	Reopens         []ReopenRef         `json:"reopens"`
	ParseErrors     []parser.ParseError `json:"parseErrors,omitempty"`
}

// ReviewCounts is the summary-count strip at the top of the Review tab.
type ReviewCounts struct {
	NeedsUserReview int `json:"needsUserReview"`
	Blocked         int `json:"blocked"`
	HighCompromise  int `json:"highCompromise"`
	Overlooked      int `json:"overlooked"`
	Reopen          int `json:"reopen"`
}

// StoryRef points at a Story that needs attention.
type StoryRef struct {
	SprintID     string `json:"sprintId"`
	StoryID      string `json:"storyId"`
	Title        string `json:"title,omitempty"`
	Status       string `json:"status"`
	ReviewReason string `json:"reviewReason,omitempty"`
	Detail       string `json:"detail,omitempty"`
}

// CompromiseRef is a high-severity compromise or blocker.
type CompromiseRef struct {
	SprintID string `json:"sprintId"`
	Kind     string `json:"kind"` // compromise | blocker
	Severity string `json:"severity"`
	Type     string `json:"type,omitempty"`
	Story    string `json:"story,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// OverlookedRef is a verifier finding the implementer missed.
type OverlookedRef struct {
	SprintID string `json:"sprintId"`
	Story    string `json:"story,omitempty"`
	AC       string `json:"ac,omitempty"`
	Category string `json:"category,omitempty"`
	Verdict  string `json:"verdict,omitempty"`
	Detail   string `json:"detail,omitempty"`
}

// ReopenRef is one re-open record.
type ReopenRef struct {
	SprintID    string `json:"sprintId"`
	ReopenedAt  string `json:"reopenedAt,omitempty"`
	TriggeredBy string `json:"triggeredBy,omitempty"`
	Reason      string `json:"reason,omitempty"`
}

func (h *handler) review(w http.ResponseWriter, r *http.Request) {
	root, err := h.worktree(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	roadmapPath := filepath.Join(root, "docs", "ROADMAP.json")
	logsRoot := filepath.Join(root, "docs", "sprint-logs")
	tag := `"` + shortHash(fileTag(roadmapPath)+":"+fileTagDir(logsRoot)+":review=v1") + `"`
	if sendCacheable(w, r, tag) {
		return
	}

	resp := ReviewResponse{
		NeedsUserReview: []StoryRef{},
		Blocked:         []StoryRef{},
		Compromises:     []CompromiseRef{},
		Overlooked:      []OverlookedRef{},
		Reopens:         []ReopenRef{},
	}

	// ROADMAP-driven: needs_user_review / blocked / needs_human stories.
	if src, err := readFileBytes(root, "docs/ROADMAP.json"); err == nil {
		rm := parser.ParseRoadmap(src)
		resp.ParseErrors = append(resp.ParseErrors, rm.ParseErrors...)
		for _, s := range rm.Sprints {
			for _, st := range s.Stories {
				ref := StoryRef{
					SprintID:     s.ID,
					StoryID:      st.ID,
					Title:        st.Title,
					Status:       st.StatusKind,
					ReviewReason: st.ReviewReason,
				}
				switch st.StatusKind {
				case "needs-user-review":
					if st.BlockedReason != "" {
						ref.Detail = st.BlockedReason
					}
					resp.NeedsUserReview = append(resp.NeedsUserReview, ref)
				case "blocked", "needs-human":
					if st.NeedsHuman != "" {
						ref.Detail = st.NeedsHuman
					} else if st.BlockedReason != "" {
						ref.Detail = st.BlockedReason
					}
					resp.Blocked = append(resp.Blocked, ref)
				}
			}
		}
	}

	// sprint-logs-driven: compromises / overlooked / reopen.
	for _, sprintID := range h.sprintLogDirs(root) {
		if cm := parser.ParseCompromises(h.readLog(root, sprintID, "compromises.json")); cm != nil {
			for _, c := range cm.Compromises {
				if strings.EqualFold(c.Severity, "high") {
					resp.Compromises = append(resp.Compromises, CompromiseRef{
						SprintID: sprintID, Kind: "compromise", Severity: c.Severity,
						Type: c.Type, Story: c.Story, Detail: firstNonEmpty(c.Rationale, c.DiffSummary),
					})
				}
			}
			for _, b := range cm.Blockers {
				if strings.EqualFold(b.Severity, "high") || strings.EqualFold(b.Severity, "medium") {
					resp.Compromises = append(resp.Compromises, CompromiseRef{
						SprintID: sprintID, Kind: "blocker", Severity: b.Severity,
						Type: b.Type, Detail: firstNonEmpty(b.Detail, b.Resolution),
					})
				}
			}
		}
		if vr := parser.ParseVerificationReport(h.readLog(root, sprintID, "verification-report.json")); vr != nil {
			collectOverlooked(vr, sprintID, &resp.Overlooked)
		}
		if rp := parser.ParseReopen(h.readLog(root, sprintID, "reopen.json")); rp != nil {
			sid := rp.SprintID
			if sid == "" {
				sid = sprintID
			}
			resp.Reopens = append(resp.Reopens, ReopenRef{
				SprintID: sid, ReopenedAt: rp.ReopenedAt, TriggeredBy: rp.TriggeredBy, Reason: rp.Reason,
			})
		}
	}

	highCompromise := 0
	for _, c := range resp.Compromises {
		if strings.EqualFold(c.Severity, "high") && c.Kind == "compromise" {
			highCompromise++
		}
	}
	resp.Counts = ReviewCounts{
		NeedsUserReview: len(resp.NeedsUserReview),
		Blocked:         len(resp.Blocked),
		HighCompromise:  highCompromise,
		Overlooked:      len(resp.Overlooked),
		Reopen:          len(resp.Reopens),
	}
	writeJSON(w, http.StatusOK, resp)
}

func collectOverlooked(vr *parser.VerificationReport, sprintID string, out *[]OverlookedRef) {
	for _, s := range vr.Stories {
		for _, ac := range s.ACFindings {
			if ac.OverlookedByAutopilot {
				*out = append(*out, OverlookedRef{
					SprintID: sprintID, Story: s.Story, AC: ac.AC,
					Category: "acceptance_criteria", Verdict: ac.Status, Detail: ac.Evidence,
				})
			}
		}
		for _, f := range s.ForbiddenCategoryFindings {
			if f.OverlookedByAutopilot {
				*out = append(*out, OverlookedRef{
					SprintID: sprintID, Story: s.Story,
					Category: firstNonEmpty(f.Category, "forbidden_categories"), Verdict: f.Verdict, Detail: f.Detail,
				})
			}
		}
	}
	for _, f := range vr.Findings {
		if f.OverlookedByAutopilot {
			*out = append(*out, OverlookedRef{
				SprintID: sprintID, Story: f.Story, AC: f.AC,
				Category: f.Category, Verdict: f.Verdict, Detail: f.Detail,
			})
		}
	}
}

// ----------------------------------------------------------------------
// Milestones (cross-sprint)
// ----------------------------------------------------------------------

// MilestonesResponse lists the milestone sprints with their comprehension
// report + compromises + verification verdict.
type MilestonesResponse struct {
	Milestones  []MilestoneEntry    `json:"milestones"`
	ParseErrors []parser.ParseError `json:"parseErrors,omitempty"`
}

// MilestoneEntry is one milestone sprint.
type MilestoneEntry struct {
	SprintID         string                `json:"sprintId"`
	Title            string                `json:"title"`
	Phase            string                `json:"phase,omitempty"`
	Status           string                `json:"status"`
	StatusKind       string                `json:"statusKind"`
	Comprehension    *parser.Comprehension `json:"comprehension,omitempty"`
	Compromises      *parser.Compromises   `json:"compromises,omitempty"`
	VerifyRunOverall string                `json:"verifyRunOverall,omitempty"`
	VerifierOverall  string                `json:"verifierOverall,omitempty"`
}

func (h *handler) milestones(w http.ResponseWriter, r *http.Request) {
	root, err := h.worktree(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	roadmapPath := filepath.Join(root, "docs", "ROADMAP.json")
	logsRoot := filepath.Join(root, "docs", "sprint-logs")
	tag := `"` + shortHash(fileTag(roadmapPath)+":"+fileTagDir(logsRoot)+":milestones=v1") + `"`
	if sendCacheable(w, r, tag) {
		return
	}
	resp := MilestonesResponse{Milestones: []MilestoneEntry{}}
	src, err := readFileBytes(root, "docs/ROADMAP.json")
	if err != nil {
		writeErr(w, err)
		return
	}
	rm := parser.ParseRoadmap(src)
	resp.ParseErrors = rm.ParseErrors
	// Most-recent milestone first (reverse execution order).
	for i := len(rm.Sprints) - 1; i >= 0; i-- {
		s := rm.Sprints[i]
		if !s.Milestone {
			continue
		}
		e := MilestoneEntry{
			SprintID:   s.ID,
			Title:      s.Title,
			Phase:      s.Phase,
			Status:     s.Status,
			StatusKind: s.StatusKind,
		}
		e.Comprehension = parser.ParseComprehension(h.readLog(root, s.ID, "comprehension-report.md"))
		e.Compromises = parser.ParseCompromises(h.readLog(root, s.ID, "compromises.json"))
		if vr := parser.ParseVerifyRun(h.readLog(root, s.ID, "verify-run.json")); vr != nil {
			e.VerifyRunOverall = vr.OverallMachineStatus
		}
		if rep := parser.ParseVerificationReport(h.readLog(root, s.ID, "verification-report.json")); rep != nil {
			e.VerifierOverall = rep.Overall
		}
		resp.Milestones = append(resp.Milestones, e)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ----------------------------------------------------------------------
// Backlog (standalone feed — also folded into Overview for Option A)
// ----------------------------------------------------------------------

// BacklogResponse is the standalone backlog feed with promotion tracking.
type BacklogResponse struct {
	Items       []parser.BacklogEntry `json:"items"`
	Total       int                   `json:"total"`
	Unpromoted  int                   `json:"unpromoted"`
	Promoted    int                   `json:"promoted"`
	ParseErrors []parser.ParseError   `json:"parseErrors,omitempty"`
}

func (h *handler) backlog(w http.ResponseWriter, r *http.Request) {
	root, err := h.worktree(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	roadmapPath := filepath.Join(root, "docs", "ROADMAP.json")
	tag := `"` + shortHash(fileTag(roadmapPath)+":backlog=v1") + `"`
	if sendCacheable(w, r, tag) {
		return
	}
	src, err := readFileBytes(root, "docs/ROADMAP.json")
	if err != nil {
		writeErr(w, err)
		return
	}
	rm := parser.ParseRoadmap(src)
	resp := BacklogResponse{Items: rm.Backlog, Total: len(rm.Backlog), ParseErrors: rm.ParseErrors}
	if resp.Items == nil {
		resp.Items = []parser.BacklogEntry{}
	}
	for _, b := range rm.Backlog {
		if b.Promoted {
			resp.Promoted++
		} else {
			resp.Unpromoted++
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ----------------------------------------------------------------------
// Log fetcher (verify-run-*.log expansion)
// ----------------------------------------------------------------------

// maxLogBytes caps the served log tail so a runaway log can't blow up the
// response.
const maxLogBytes = 256 * 1024

func (h *handler) sprintLog(w http.ResponseWriter, r *http.Request) {
	root, err := h.worktree(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	sprintID := r.PathValue("sprintId")
	file := r.URL.Query().Get("file")
	if sprintID == "" || file == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sprintId and file required"})
		return
	}
	// Path safety for sprintID (SEVERE path-traversal fix): Go 1.22 ServeMux
	// binds a percent-encoded wildcard (e.g. `..%2f..%2f`) to a single path
	// segment WITHOUT redirecting, so `sprintId` can carry `..`/slashes and
	// filepath.Join would escape the worktree (readable host .log files in
	// open-access mode). Reject anything that is not a clean single segment,
	// then additionally gate on a real ROADMAP sprint-ID match — the same
	// guard that makes sprintDetail safe (real IDs never contain `..`/slashes;
	// a miss is a 404).
	if !isCleanSegment(sprintID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid sprintId"})
		return
	}
	if !h.roadmapHasSprint(root, sprintID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sprint not found", "sprintId": sprintID})
		return
	}
	// Path safety: base name only, must be a .log under the sprint-log dir.
	base := filepath.Base(file)
	if base != file || !strings.HasSuffix(strings.ToLower(base), ".log") || strings.Contains(base, "..") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid log file"})
		return
	}
	full := filepath.Join(root, "docs", "sprint-logs", sprintID, base)
	info, err := os.Stat(full)
	if err != nil || info.IsDir() {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "log not found"})
		return
	}
	data, err := os.ReadFile(full)
	if err != nil {
		writeErr(w, err)
		return
	}
	truncated := false
	if len(data) > maxLogBytes {
		data = data[len(data)-maxLogBytes:]
		truncated = true
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("ETag", fileTag(full))
	if truncated {
		w.Header().Set("X-Log-Truncated", "true")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

// ----------------------------------------------------------------------
// shared helpers
// ----------------------------------------------------------------------

// sprintLogDirs lists the sprint IDs that have a docs/sprint-logs/{id}
// directory, sorted.
func (h *handler) sprintLogDirs(root string) []string {
	out := []string{}
	entries, err := os.ReadDir(filepath.Join(root, "docs", "sprint-logs"))
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

// readLog reads a sprint-log file, returning nil on any error (fail-open).
func (h *handler) readLog(root, sprintID, name string) []byte {
	b, err := readFileBytes(root, "docs/sprint-logs/"+sprintID+"/"+name)
	if err != nil {
		return nil
	}
	return b
}

// isCleanSegment reports whether s is a single, clean path segment safe to
// use as a directory name under docs/sprint-logs — no slashes (either kind),
// no "..", and equal to its own filepath.Base. Empty is rejected.
func isCleanSegment(s string) bool {
	if s == "" {
		return false
	}
	if strings.ContainsAny(s, `/\`) || strings.Contains(s, "..") {
		return false
	}
	return filepath.Base(s) == s
}

// roadmapHasSprint reports whether sprintID is a real sprint in the worktree's
// ROADMAP.json (case-insensitive), mirroring sprintDetail's found-or-404 gate.
// Returns false on any read/parse failure (fail-closed).
func (h *handler) roadmapHasSprint(root, sprintID string) bool {
	src, err := readFileBytes(root, "docs/ROADMAP.json")
	if err != nil {
		return false
	}
	rm := parser.ParseRoadmap(src)
	for i := range rm.Sprints {
		if strings.EqualFold(rm.Sprints[i].ID, sprintID) {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
