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
	rm.Progress = Progress{
		Total:      doc.Progress.Total,
		Done:       doc.Progress.Done,
		InProgress: doc.Progress.InProgress,
		Remaining:  doc.Progress.Remaining,
		Percent:    doc.Progress.Percentage,
	}
	if doc.Progress.Percentage == 0 && doc.Progress.Total > 0 {
		rm.Progress.Percent = float64(doc.Progress.Done) * 100 / float64(doc.Progress.Total)
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
	rm.Backlog = projectBacklog(doc.Backlog)

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
	sp := Sprint{
		ID:          id,
		Title:       sd.Title,
		Status:      sd.Status,
		StatusKind:  classifySprintStatus(sd.Status),
		Description: sd.Description,
		Milestone:   sd.Milestone,
		Stories:     []Story{},
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
			From: from,
			Refs: refs,
			Text: text,
		})
	}
	return out
}

func projectBacklog(items []roadmapBacklogDoc) []BacklogEntry {
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
		source := b.AddedIn
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
		out = append(out, BacklogEntry{
			Title:       b.Title,
			Description: b.Description,
			AddedIn:     b.AddedIn,
			Reason:      b.Reason,
			Done:        false,
			Text:        text,
			Source:      source,
		})
	}
	return out
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
	case "in_progress", "in-progress":
		return "in-progress"
	case "pending", "":
		return "pending"
	}
	return "pending"
}
