// roadmap.go — JSON unmarshaler for docs/ROADMAP.json (S028).
//
// Replaces the original markdown / regex parser. The on-disk format is
// strictly defined by sprint-runner's ROADMAP_SCHEMA.json, so unmarshaling
// + projection is enough; no heuristics required.
//
// Fail-safe contract: a malformed file (JSON syntax error, missing
// required field, unknown enum value) never crashes the request path.
// We surface the problem in `Roadmap.ParseErrors` and return as much of
// the document as we could decode. This keeps the dashboard usable even
// when the user is mid-edit.
package parser

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// ParseRoadmap parses ROADMAP.json bytes. Empty input → an empty Roadmap
// with a single ParseError so the FE can show "no roadmap yet" without
// special-casing nil.
//
// The function never panics and never returns an error — every problem
// is folded into Roadmap.ParseErrors.
func ParseRoadmap(src []byte) Roadmap {
	rm := emptyRoadmap()
	if len(src) == 0 {
		rm.ParseErrors = append(rm.ParseErrors, ParseError{
			Section: "ROADMAP.json",
			Detail:  "file is empty",
		})
		return rm
	}

	var doc roadmapDoc
	if err := json.Unmarshal(src, &doc); err != nil {
		rm.ParseErrors = append(rm.ParseErrors, jsonSyntaxError("ROADMAP.json", src, err))
		return rm
	}

	rm.Title = doc.Project
	rm.Description = doc.Description
	// Accept both the original schema (total/done/percentage) and the
	// post-S028 sprint-runner schema (sprints_total/sprints_done/percent).
	total := doc.Progress.Total
	done := doc.Progress.Done
	percent := doc.Progress.Percentage
	if total == 0 && doc.Progress.SprintsTotal > 0 {
		total = doc.Progress.SprintsTotal
	}
	if done == 0 && doc.Progress.SprintsDone > 0 {
		done = doc.Progress.SprintsDone
	}
	if percent == 0 && doc.Progress.Percent != 0 {
		percent = doc.Progress.Percent
	}
	rm.Progress = Progress{
		Total:      total,
		Done:       done,
		InProgress: doc.Progress.InProgress,
		Remaining:  doc.Progress.Remaining,
		Percent:    percent,
	}
	if rm.Progress.Percent == 0 && rm.Progress.Total > 0 {
		rm.Progress.Percent = float64(rm.Progress.Done) * 100 / float64(rm.Progress.Total)
	}

	if len(doc.ExecutionOrder) > 0 {
		rm.ExecutionRaw = strings.Join(doc.ExecutionOrder, " → ")
	}

	// Sprints: ordered by execution_order if available, otherwise by
	// sprint ID. We materialize keys so the FE always sees a stable
	// timeline (map iteration in Go is random).
	order := orderedSprintIDs(doc)
	for _, id := range order {
		sd, ok := doc.Sprints[id]
		if !ok {
			continue
		}
		rm.Sprints = append(rm.Sprints, projectSprint(id, sd))
	}

	// Dependencies: schema is { "{from}": { depends_on: [...], reason: "" } }.
	// FE expects per-edge entries, so we fan out one Dependency per
	// (from, to) but keep all `depends_on` IDs in Refs for compatibility
	// with the existing Mermaid edge derivation (handler reads d.Refs[0]
	// as `from` and d.Refs[1:] as prerequisites).
	rm.Dependencies = projectDependencies(doc.Dependencies)
	rm.Backlog = projectBacklog(doc.Backlog, rm.Sprints)

	return rm
}

// emptyRoadmap returns a Roadmap with every list field initialised to
// an empty slice — the FE renders `.map()` directly without null guards
// so we never want to emit `null`.
func emptyRoadmap() Roadmap {
	return Roadmap{
		Sprints:      []Sprint{},
		Dependencies: []Dependency{},
		Backlog:      []BacklogEntry{},
		ParseErrors:  []ParseError{},
	}
}

func orderedSprintIDs(doc roadmapDoc) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(doc.Sprints))
	for _, id := range doc.ExecutionOrder {
		if _, dup := seen[id]; dup {
			continue
		}
		if _, ok := doc.Sprints[id]; ok {
			out = append(out, id)
			seen[id] = struct{}{}
		}
	}
	// Append any sprints not in execution_order (defensive: roadmap may
	// have orphans that haven't been scheduled yet).
	leftover := []string{}
	for id := range doc.Sprints {
		if _, ok := seen[id]; ok {
			continue
		}
		leftover = append(leftover, id)
	}
	sort.Strings(leftover)
	return append(out, leftover...)
}

func projectSprint(id string, sd roadmapSprintDoc) Sprint {
	detail := strings.ToLower(strings.TrimSpace(sd.DetailLevel))
	if detail == "" {
		// Absent ⇒ "detailed" (back-compat, pre-rolling-wave roadmaps).
		detail = "detailed"
	}
	sp := Sprint{
		ID:           id,
		Title:        sd.Title,
		Status:       sd.Status,
		StatusKind:   classifySprintStatus(sd.Status),
		Description:  sd.Description,
		Milestone:    sd.Milestone,
		DetailLevel:  detail,
		Phase:        sd.Phase,
		ReviewReason: sd.ReviewReason,
		Coarse:       detail == "coarse",
		Stories:      []Story{},
	}
	// Order stories by story ID (S001-1, S001-2, ...). Map iteration is
	// random in Go so we sort deterministically.
	keys := make([]string, 0, len(sd.Stories))
	for k := range sd.Stories {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, sid := range keys {
		st := sd.Stories[sid]
		sp.Stories = append(sp.Stories, projectStory(sid, st))
	}
	return sp
}

func projectStory(id string, st roadmapStoryDoc) Story {
	story := Story{
		ID:                 id,
		Title:              st.Title,
		Status:             st.Status,
		StatusKind:         classifyStoryStatus(st.Status),
		UserStory:          st.UserStory,
		BlockedReason:      st.BlockedReason,
		ReviewReason:       st.ReviewReason,
		UserReviewRequired: st.UserReviewRequired,
		AddedInReview:      st.AddedInReview,
		ReopenedAt:         st.ReopenedAt,
		NeedsHuman:         st.NeedsHuman,
		DependsOn:          parseStringOrArray(st.DependsOn),
		AcceptanceCriteria: []Acceptance{},
		Tasks:              []Task{},
	}
	for _, ac := range st.AcceptanceCriteria {
		text := ac.Description
		if ac.ID != "" {
			text = ac.ID + ": " + ac.Description
		}
		story.AcceptanceCriteria = append(story.AcceptanceCriteria, Acceptance{
			ID:          ac.ID,
			Description: ac.Description,
			Test:        ac.Test,
			Status:      ac.Status,
			Done:        ac.Status == "pass",
			Text:        text,
			ReopenedAt:  ac.ReopenedAt,
		})
	}
	keys := make([]string, 0, len(st.Tasks))
	for k := range st.Tasks {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, tid := range keys {
		t := st.Tasks[tid]
		story.Tasks = append(story.Tasks, Task{
			ID:          tid,
			Title:       t.Title,
			Description: t.Description,
			Status:      t.Status,
			Done:        t.Status == "done",
			Text:        joinIfNonempty(t.Title, t.Description),
		})
	}
	return story
}

// parseStringOrArray decodes a JSON field that may be either a string (a
// dependency note) or an array of strings (prerequisite story/sprint IDs).
// Story-level depends_on is written both ways in the wild.
func parseStringOrArray(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var arr []string
	if err := json.Unmarshal(raw, &arr); err == nil {
		out := arr[:0]
		for _, s := range arr {
			if strings.TrimSpace(s) != "" {
				out = append(out, s)
			}
		}
		if len(out) == 0 {
			return nil
		}
		return out
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if strings.TrimSpace(s) == "" {
			return nil
		}
		return []string{s}
	}
	return nil
}

func joinIfNonempty(parts ...string) string {
	out := []string{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, ": ")
}

func projectDependencies(deps map[string]roadmapDepDoc) []Dependency {
	out := []Dependency{}
	keys := make([]string, 0, len(deps))
	for k := range deps {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, from := range keys {
		d := deps[from]
		// Always emit one Dependency entry per `from`, even when
		// depends_on is empty — that keeps the FE list stable. Refs is
		// `[from, to1, to2, ...]` so existing handler code (Refs[0] is
		// the dependent, Refs[1:] are prereqs) keeps working.
		refs := append([]string{from}, d.DependsOn...)
		text := from
		if len(d.DependsOn) > 0 {
			text += " depends on " + strings.Join(d.DependsOn, ", ")
		} else {
			text += " (no dependencies)"
		}
		if d.Reason != "" {
			text += ": " + d.Reason
		}
		out = append(out, Dependency{
			From:   from,
			Refs:   refs,
			Text:   text,
			Reason: d.Reason,
		})
	}
	return out
}

func projectBacklog(items []roadmapBacklogDoc, sprints []Sprint) []BacklogEntry {
	out := []BacklogEntry{}
	for _, b := range items {
		text := b.Title
		if b.Description != "" {
			if text != "" {
				text += " — " + b.Description
			} else {
				text = b.Description
			}
		}
		source := b.Source
		if source == "" {
			source = b.AddedIn
		}
		if source == "" && strings.Contains(b.Description, "由来)") {
			// Pre-migration backlog items folded the source into the
			// description as "(Sxxx 由来)". Keep extracting it so older
			// entries still surface a Source tag.
			if i := strings.Index(b.Description, "由来)"); i >= 0 {
				if open := strings.LastIndex(b.Description[:i+len("由来)")], "("); open >= 0 {
					source = b.Description[open+1 : i+len("由来)")-1]
				}
			}
		}
		entry := BacklogEntry{
			Title:       b.Title,
			Description: b.Description,
			AddedIn:     b.AddedIn,
			Reason:      b.Reason,
			Done:        false,
			Text:        text,
			Source:      source,
			Priority:    b.Priority,
			Status:      b.Status,
		}
		if to := detectPromotion(b.Title, sprints); to != "" {
			entry.Promoted = true
			entry.PromotedTo = to
		}
		out = append(out, entry)
	}
	return out
}

// sectionRefRe matches "§8.3", "§ 8", "ADR-0014" style anchors that make a
// strong, low-false-positive promotion signal.
var sectionRefRe = regexp.MustCompile(`§\s*\d+(?:\.\d+)*|ADR-\d+`)

// detectPromotion is a conservative heuristic (AC-Se173ef-2-4): a backlog
// item counts as "promoted" only when it shares a *distinctive* anchor with
// an existing Sprint title — a §-section / ADR reference, or a CJK run that
// makes up *nearly the whole* backlog title (near-full-title containment).
//
// Bug fix (Se173ef fixup): a bare shared contiguous CJK run of length ≥ 6 is
// NOT distinctive enough on its own. Common domain phrases like
// "パフォーマンス改善" / "共有フォルダ" / "セルフアップデート" recur across
// unrelated backlog items and sprint titles, and a shared generic sub-phrase
// used to wrongly flag an item Promoted — dropping it from the unpromoted
// count and hiding it behind the "promoted" filter though it was never
// scheduled. We now require the CJK anchor to cover a *dominant* fraction of
// the backlog title's total CJK content, so only a title that is essentially
// wholly present in a sprint title matches. Generic word overlap is still
// deliberately NOT used. Returns the promoting SprintID or "".
func detectPromotion(title string, sprints []Sprint) string {
	anchors := promotionAnchors(title)
	if len(anchors) == 0 {
		return ""
	}
	for _, sp := range sprints {
		st := sp.Title
		for _, a := range anchors {
			if strings.Contains(st, a) {
				return sp.ID
			}
		}
	}
	return ""
}

// cjkAnchorMinFraction is the share of a backlog title's total CJK runes that
// a single contiguous CJK run must cover before it is treated as a distinctive
// promotion anchor. A generic sub-phrase (a minority of the title) falls below
// this bar; only a run that is essentially the whole title qualifies.
const cjkAnchorMinFraction = 0.7

// promotionAnchors extracts distinctive anchors from a backlog title:
//   - §-section / ADR references (always distinctive), and
//   - a contiguous CJK run of length ≥ 6 that ALSO covers ≥ cjkAnchorMinFraction
//     of the title's total CJK runes (near-full-title containment). A shared
//     generic phrase that is only part of a longer title is intentionally
//     rejected — see detectPromotion.
func promotionAnchors(title string) []string {
	anchors := []string{}
	for _, m := range sectionRefRe.FindAllString(title, -1) {
		anchors = append(anchors, strings.ReplaceAll(m, " ", ""))
		anchors = append(anchors, m)
	}
	// Total CJK runes across the whole title (the denominator for the
	// near-full-title containment test).
	totalCJK := 0
	for _, r := range title {
		if isCJK(r) {
			totalCJK++
		}
	}
	minRun := int(cjkAnchorMinFraction*float64(totalCJK) + 0.999999) // ceil
	if minRun < 6 {
		minRun = 6
	}
	var run []rune
	flush := func() {
		if len(run) >= minRun {
			anchors = append(anchors, string(run))
		}
		run = run[:0]
	}
	for _, r := range title {
		if isCJK(r) {
			run = append(run, r)
		} else {
			flush()
		}
	}
	flush()
	return anchors
}

func isCJK(r rune) bool {
	return (r >= 0x3040 && r <= 0x30ff) || // hiragana + katakana
		(r >= 0x4e00 && r <= 0x9fff) // CJK unified ideographs
}

// jsonSyntaxError converts a json.Unmarshal error into a ParseError with
// line/column hints. encoding/json reports a byte offset in
// SyntaxError.Offset; we walk `src` to translate that to (line, col).
func jsonSyntaxError(section string, src []byte, err error) ParseError {
	pe := ParseError{Section: section, Detail: err.Error()}
	var se *json.SyntaxError
	if errors.As(err, &se) {
		line, col := offsetToLineCol(src, int(se.Offset))
		pe.Line = line
		pe.Column = col
		pe.Detail = fmt.Sprintf("JSON syntax error at line %d col %d: %s", line, col, err.Error())
	}
	var ute *json.UnmarshalTypeError
	if errors.As(err, &ute) {
		line, col := offsetToLineCol(src, int(ute.Offset))
		pe.Line = line
		pe.Column = col
		pe.Detail = fmt.Sprintf("JSON type error at line %d col %d: cannot unmarshal %s into field %s of type %s",
			line, col, ute.Value, ute.Field, ute.Type.String())
	}
	return pe
}

// offsetToLineCol converts a byte offset into 1-based (line, column).
// The offset reported by encoding/json points one byte past the bad
// token; we clamp into [0, len(src)] and walk.
func offsetToLineCol(src []byte, offset int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(src) {
		offset = len(src)
	}
	line, col := 1, 1
	for i := 0; i < offset; i++ {
		if src[i] == '\n' {
			line++
			col = 1
		} else {
			col++
		}
	}
	return line, col
}

func classifySprintStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "done":
		return "done"
	case "in_progress", "in-progress":
		return "in-progress"
	case "blocked":
		return "blocked"
	case "needs_human", "needs-human":
		return "needs-human"
	case "pending", "":
		return "pending"
	}
	return "pending"
}

func classifyStoryStatus(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "done":
		return "done"
	case "blocked":
		return "blocked"
	case "needs_human", "needs-human":
		return "needs-human"
	case "needs_user_review", "needs-user-review":
		return "needs-user-review"
	case "in_progress", "in-progress":
		return "in-progress"
	case "pending", "":
		return "pending"
	}
	return "pending"
}
