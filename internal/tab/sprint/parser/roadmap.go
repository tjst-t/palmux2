// Package parser turns the markdown files written by the claude-skills
// `sprint` skill (ROADMAP.md, decisions.md, refine.md, etc.) into typed Go
// structs the Sprint Dashboard can serve as JSON.
//
// The format is fixed by sprint-runner — see references/ROADMAP_TEMPLATE.md
// in the skill — so a regex / line-walker is sufficient. Each parser is
// section-fail-safe: a malformed entry is replaced with an Unparsed marker
// rather than aborting the whole document.
package parser

import (
	"bufio"
	"regexp"
	"strconv"
	"strings"
)

// Roadmap is the top-level structured view of docs/ROADMAP.md.
type Roadmap struct {
	Title        string         `json:"title"`
	Vision       string         `json:"vision,omitempty"`
	Progress     Progress       `json:"progress"`
	ExecutionRaw string         `json:"executionRaw,omitempty"`
	Sprints      []Sprint       `json:"sprints"`
	Dependencies []Dependency   `json:"dependencies"`
	Backlog      []BacklogEntry `json:"backlog"`
	ParseErrors  []ParseError   `json:"parseErrors,omitempty"`
}

// Progress captures the "## 進捗" header — total / done / in-progress / left.
type Progress struct {
	Total      int     `json:"total"`
	Done       int     `json:"done"`
	InProgress int     `json:"inProgress"`
	Remaining  int     `json:"remaining"`
	Percent    float64 `json:"percent"`
}

// Sprint is a single "## スプリント Sxxx: ..." block.
type Sprint struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Status      string   `json:"status"` // "[ ]" / "[x]" / "[DONE]" / "[IN PROGRESS]" / etc.
	StatusKind  string   `json:"statusKind"`
	Description string   `json:"description,omitempty"`
	Stories     []Story  `json:"stories"`
	RawBody     string   `json:"-"` // unexported helper for tests
	LineRange   [2]int   `json:"lineRange"` // 1-based [start,end]
	ParseError  string   `json:"parseError,omitempty"`
}

// Story is "### ストーリー Sxxx-N: ..." inside a Sprint.
type Story struct {
	ID                 string         `json:"id"`
	Title              string         `json:"title"`
	Status             string         `json:"status"`
	StatusKind         string         `json:"statusKind"`
	UserStory          string         `json:"userStory,omitempty"`
	AcceptanceCriteria []Acceptance   `json:"acceptanceCriteria"`
	Tasks              []Task         `json:"tasks"`
}

// Acceptance is a `- [ ]` / `- [x]` AC line.
type Acceptance struct {
	Done bool   `json:"done"`
	Text string `json:"text"`
}

// Task is a `- [ ]` / `- [x]` task line under "**タスク:**".
type Task struct {
	ID   string `json:"id,omitempty"`
	Done bool   `json:"done"`
	Text string `json:"text"`
}

// Dependency is a single bullet inside the "## 依存関係" section.
type Dependency struct {
	Text string `json:"text"`
	// Refs lists Sprint IDs mentioned in Text (best-effort regex extract).
	Refs []string `json:"refs,omitempty"`
}

// BacklogEntry is a single bullet inside "## バックログ".
type BacklogEntry struct {
	Done   bool   `json:"done"`
	Text   string `json:"text"`
	Source string `json:"source,omitempty"` // e.g. "S013 由来"
}

// ParseError records a section that failed structural parsing.
type ParseError struct {
	Section string `json:"section"`
	Detail  string `json:"detail"`
}

var (
	reSprint = regexp.MustCompile(`^## スプリント\s+([A-Za-z0-9-]+)\s*:\s*(.+?)\s*\[(.+)\]\s*$`)
	// Hotfix-style heading "### Hotfix Sxxx-fix-N: title [x]" mirrors the
	// regular Story line so we surface them under the sprint they belong
	// to. Matched by reHotfix (Story-only).
	reHotfix = regexp.MustCompile(`^### Hotfix\s+([A-Za-z0-9-]+)\s*:\s*(.+?)\s*\[(.+)\]\s*$`)
	reStory  = regexp.MustCompile(`^### ストーリー\s+([A-Za-z0-9-]+)\s*:\s*(.+?)\s*\[(.+)\]\s*$`)
	reTask   = regexp.MustCompile(`^- \[( |x|X)\]\s+\*\*タスク\s+([A-Za-z0-9-]+)\*\*\s*:\s*(.*)$`)
	reCheck  = regexp.MustCompile(`^- \[( |x|X)\]\s+(.*)$`)
	reID     = regexp.MustCompile(`\bS\d{3}(?:-[A-Za-z0-9]+)*\b`)
)

// ParseRoadmap parses the full ROADMAP.md text. It never returns an error
// — sections that fail to parse are recorded in Roadmap.ParseErrors and
// the rest of the document is still served.
func ParseRoadmap(src string) Roadmap {
	rm := Roadmap{}
	scanner := bufio.NewScanner(strings.NewReader(src))
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024)
	lines := []string{}
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}

	// Title (first H1).
	for _, l := range lines {
		if strings.HasPrefix(l, "# ") {
			rm.Title = strings.TrimSpace(strings.TrimPrefix(l, "# "))
			break
		}
	}

	// Section indices.
	type sec struct {
		title string
		start int // index of the heading line in lines
		end   int // exclusive
	}
	var sections []sec
	for i, l := range lines {
		if strings.HasPrefix(l, "## ") {
			sections = append(sections, sec{title: strings.TrimSpace(strings.TrimPrefix(l, "## ")), start: i})
		}
	}
	for i := range sections {
		if i+1 < len(sections) {
			sections[i].end = sections[i+1].start
		} else {
			sections[i].end = len(lines)
		}
	}

	for _, s := range sections {
		switch {
		case strings.HasPrefix(s.title, "進捗"):
			rm.Progress = parseProgress(lines[s.start:s.end])
		case strings.HasPrefix(s.title, "実行順序"):
			rm.ExecutionRaw = strings.TrimSpace(strings.Join(lines[s.start+1:s.end], "\n"))
		case strings.HasPrefix(s.title, "スプリント "):
			sp, perr := parseSprintSection(s.title, lines[s.start:s.end], s.start+1)
			if perr != "" {
				sp.ParseError = perr
				rm.ParseErrors = append(rm.ParseErrors, ParseError{Section: s.title, Detail: perr})
			}
			rm.Sprints = append(rm.Sprints, sp)
		case strings.HasPrefix(s.title, "依存関係"):
			rm.Dependencies = parseDependencies(lines[s.start+1 : s.end])
		case strings.HasPrefix(s.title, "バックログ"):
			rm.Backlog = parseBacklog(lines[s.start+1 : s.end])
		}
	}
	return rm
}

func parseProgress(block []string) Progress {
	var pr Progress
	reTotals := regexp.MustCompile(`合計\s*:\s*(\d+).*?完了\s*:\s*(\d+).*?進行中\s*:\s*(\d+).*?残り\s*:\s*(\d+)`)
	rePercent := regexp.MustCompile(`(\d+(?:\.\d+)?)%`)
	for _, l := range block {
		if m := reTotals.FindStringSubmatch(l); m != nil {
			pr.Total, _ = strconv.Atoi(m[1])
			pr.Done, _ = strconv.Atoi(m[2])
			pr.InProgress, _ = strconv.Atoi(m[3])
			pr.Remaining, _ = strconv.Atoi(m[4])
		}
		if m := rePercent.FindStringSubmatch(l); m != nil && pr.Percent == 0 {
			f, _ := strconv.ParseFloat(m[1], 64)
			pr.Percent = f
		}
	}
	if pr.Percent == 0 && pr.Total > 0 {
		pr.Percent = float64(pr.Done) * 100 / float64(pr.Total)
	}
	return pr
}

// parseSprintSection takes the lines between two `## スプリント` headings.
// `startLine` is the 1-based line number of the heading itself in the
// original file; emitted LineRange is computed off that.
//
// Defensive: empty / malformed sprint sections never panic. Whatever the
// regex extracts is returned as-is and the caller flags Unparsed entries.
func parseSprintSection(title string, block []string, startLine int) (Sprint, string) {
	sp := Sprint{LineRange: [2]int{startLine, startLine + len(block) - 1}}
	if len(block) == 0 {
		return sp, "empty section"
	}
	headerLine := "## " + title
	m := reSprint.FindStringSubmatch(headerLine)
	if m == nil {
		// Title prefix matched in caller, but full-line regex didn't —
		// keep the title as-is and continue parsing stories so the FE
		// can show "Unparsed sprint" rather than crashing.
		sp.Title = title
		return sp, "sprint header did not match expected format"
	}
	sp.ID = strings.TrimSpace(m[1])
	sp.Title = strings.TrimSpace(m[2])
	sp.Status = strings.TrimSpace(m[3])
	sp.StatusKind = classifySprintStatus(sp.Status)

	// Walk story headings.
	type storyStart struct {
		heading string
		idx     int // index in `block`
		hotfix  bool
	}
	var stories []storyStart
	for i, l := range block[1:] {
		if reStory.MatchString(l) {
			stories = append(stories, storyStart{heading: l, idx: i + 1, hotfix: false})
		} else if reHotfix.MatchString(l) {
			stories = append(stories, storyStart{heading: l, idx: i + 1, hotfix: true})
		}
	}
	// Description is everything between the sprint header and the first
	// story (or the end of the section).
	endOfDesc := len(block)
	if len(stories) > 0 {
		endOfDesc = stories[0].idx
	}
	if endOfDesc > 1 {
		sp.Description = strings.TrimSpace(strings.Join(block[1:endOfDesc], "\n"))
	}

	for i, ss := range stories {
		end := len(block)
		if i+1 < len(stories) {
			end = stories[i+1].idx
		}
		st := parseStory(ss.heading, block[ss.idx:end])
		sp.Stories = append(sp.Stories, st)
	}
	return sp, ""
}

func parseStory(heading string, body []string) Story {
	var st Story
	if m := reStory.FindStringSubmatch(heading); m != nil {
		st.ID = strings.TrimSpace(m[1])
		st.Title = strings.TrimSpace(m[2])
		st.Status = strings.TrimSpace(m[3])
	} else if m := reHotfix.FindStringSubmatch(heading); m != nil {
		st.ID = strings.TrimSpace(m[1])
		st.Title = strings.TrimSpace(m[2])
		st.Status = strings.TrimSpace(m[3])
	}
	st.StatusKind = classifyStoryStatus(st.Status)

	// Walk: pick the user story (between **ユーザーストーリー:** and the
	// next **bold-bracketed section**), then acceptance criteria lines,
	// then tasks.
	section := ""
	for _, l := range body {
		trim := strings.TrimSpace(l)
		switch {
		case strings.HasPrefix(trim, "**ユーザーストーリー"):
			section = "user"
			continue
		case strings.HasPrefix(trim, "**受け入れ条件"):
			section = "ac"
			continue
		case strings.HasPrefix(trim, "**タスク"):
			section = "tasks"
			continue
		case strings.HasPrefix(trim, "**") && strings.HasSuffix(trim, ":**"):
			section = ""
			continue
		}

		switch section {
		case "user":
			if trim != "" {
				if st.UserStory != "" {
					st.UserStory += " "
				}
				st.UserStory += trim
			}
		case "ac":
			if m := reCheck.FindStringSubmatch(trim); m != nil {
				st.AcceptanceCriteria = append(st.AcceptanceCriteria, Acceptance{
					Done: m[1] != " ",
					Text: strings.TrimSpace(m[2]),
				})
			}
		case "tasks":
			if m := reTask.FindStringSubmatch(trim); m != nil {
				st.Tasks = append(st.Tasks, Task{
					ID:   strings.TrimSpace(m[2]),
					Done: m[1] != " ",
					Text: strings.TrimSpace(m[3]),
				})
			} else if m := reCheck.FindStringSubmatch(trim); m != nil {
				st.Tasks = append(st.Tasks, Task{
					Done: m[1] != " ",
					Text: strings.TrimSpace(m[2]),
				})
			}
		}
	}
	return st
}

func parseDependencies(block []string) []Dependency {
	var out []Dependency
	for _, l := range block {
		t := strings.TrimSpace(l)
		if !strings.HasPrefix(t, "- ") {
			continue
		}
		text := strings.TrimSpace(strings.TrimPrefix(t, "- "))
		refs := uniqIDs(reID.FindAllString(text, -1))
		out = append(out, Dependency{Text: text, Refs: refs})
	}
	return out
}

func parseBacklog(block []string) []BacklogEntry {
	var out []BacklogEntry
	var current *BacklogEntry
	for _, l := range block {
		if m := reCheck.FindStringSubmatch(strings.TrimLeft(l, " ")); m != nil && strings.HasPrefix(strings.TrimLeft(l, " "), "- [") {
			if current != nil {
				out = append(out, *current)
			}
			text := strings.TrimSpace(m[2])
			source := ""
			if i := strings.Index(text, "由来)"); i >= 0 {
				if open := strings.LastIndex(text[:i+len("由来)")], "("); open >= 0 {
					source = text[open+1 : i+len("由来)")-1]
				}
			}
			current = &BacklogEntry{Done: m[1] != " ", Text: text, Source: source}
		} else if current != nil && strings.HasPrefix(l, "  ") {
			// continuation indented under bullet
			current.Text += " " + strings.TrimSpace(l)
		}
	}
	if current != nil {
		out = append(out, *current)
	}
	return out
}

func classifySprintStatus(raw string) string {
	r := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case r == "x" || r == "done" || strings.HasPrefix(r, "done"):
		return "done"
	case strings.Contains(r, "in progress"):
		return "in-progress"
	case r == " ":
		return "pending"
	default:
		if strings.Contains(r, "needs_human") {
			return "needs-human"
		}
		if strings.Contains(r, "blocked") {
			return "blocked"
		}
		return "pending"
	}
}

func classifyStoryStatus(raw string) string {
	r := strings.ToLower(strings.TrimSpace(raw))
	switch r {
	case "x", "done":
		return "done"
	case "":
		return "pending"
	case " ":
		return "pending"
	}
	if strings.Contains(r, "needs_human") {
		return "needs-human"
	}
	return "pending"
}

func uniqIDs(in []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, s := range in {
		if _, ok := seen[s]; ok {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}
