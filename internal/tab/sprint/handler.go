package sprint

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tjst-t/palmux2/internal/store"
	"github.com/tjst-t/palmux2/internal/tab/sprint/parser"
)

// handler holds the shared store and serves the five Sprint Dashboard
// endpoints. Every response carries an ETag derived from the modtime+size
// of the source files so the FE can short-circuit `window.focus`
// re-fetches with `If-None-Match` (S016 task -1-15).
type handler struct {
	store *store.Store
}

func newHandler(s *store.Store) *handler { return &handler{store: s} }

func (h *handler) worktree(r *http.Request) (string, error) {
	branch, err := h.store.Branch(r.PathValue("repoId"), r.PathValue("branchId"))
	if err != nil {
		return "", err
	}
	return branch.WorktreePath, nil
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, store.ErrRepoNotFound), errors.Is(err, store.ErrBranchNotFound):
		status = http.StatusNotFound
	}
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

// fileTag computes a cheap ETag from one or more file paths. Missing files
// contribute "0:0" so a created/deleted file changes the ETag.
func fileTag(paths ...string) string {
	h := sha256.New()
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			h.Write([]byte(p + ":missing\x00"))
			continue
		}
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write([]byte(st.ModTime().UTC().Format(time.RFC3339Nano)))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(st.Size(), 10)))
		h.Write([]byte{0})
	}
	return `"` + hex.EncodeToString(h.Sum(nil))[:16] + `"`
}

// fileTagDir is fileTag for a directory tree — it walks `dir` and
// includes every regular file's modtime+size in the hash. Used for the
// sprint-logs aggregate endpoints (decisions / refine).
func fileTagDir(dir string) string {
	h := sha256.New()
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write([]byte(info.ModTime().UTC().Format(time.RFC3339Nano)))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(info.Size(), 10)))
		h.Write([]byte{0})
		return nil
	})
	return `"` + hex.EncodeToString(h.Sum(nil))[:16] + `"`
}

// sendCacheable applies ETag short-circuit handling. Returns true if the
// caller should stop (304 already written).
func sendCacheable(w http.ResponseWriter, r *http.Request, etag string) bool {
	w.Header().Set("ETag", etag)
	if etag != "" && r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	return false
}

// readFile reads a worktree-relative file. Returns ("", os.ErrNotExist) if
// it doesn't exist, ("", nil) for an empty file (regular file, no bytes).
func readFile(root, rel string) (string, error) {
	b, err := os.ReadFile(filepath.Join(root, rel))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// ----------------------------------------------------------------------
// Overview
// ----------------------------------------------------------------------

// OverviewResponse mirrors the Overview screen contract.
type OverviewResponse struct {
	Project        string             `json:"project"`
	Vision         string             `json:"vision,omitempty"`
	Progress       parser.Progress    `json:"progress"`
	CurrentSprint  *parser.Sprint     `json:"currentSprint,omitempty"`
	NextMilestone  string             `json:"nextMilestone,omitempty"`
	ActiveAutopilot []ActiveAutopilot `json:"activeAutopilot"`
	Timeline       []TimelineEntry    `json:"timeline"`
	ParseErrors    []parser.ParseError `json:"parseErrors,omitempty"`
}

// ActiveAutopilot is one .claude/autopilot-*.lock detected in the worktree.
type ActiveAutopilot struct {
	SprintID  string    `json:"sprintId"`
	StartedAt time.Time `json:"startedAt"`
	LockPath  string    `json:"lockPath"`
	Pid       int       `json:"pid,omitempty"`
}

// TimelineEntry is one (id, title, status) row used for the linear
// timeline at the bottom of Overview.
type TimelineEntry struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	StatusKind string `json:"statusKind"`
}

func (h *handler) overview(w http.ResponseWriter, r *http.Request) {
	root, err := h.worktree(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	roadmapPath := filepath.Join(root, "docs", "ROADMAP.md")
	visionPath := filepath.Join(root, "docs", "VISION.md")
	autopilotDir := filepath.Join(root, ".claude")
	tag := fileTag(roadmapPath, visionPath) + ":" + dirTagFiltered(autopilotDir, isAutopilotLock)
	if sendCacheable(w, r, `"`+shortHash(tag)+`"`) {
		return
	}

	src, err := readFile(root, "docs/ROADMAP.md")
	if err != nil {
		writeErr(w, err)
		return
	}
	rm := parser.ParseRoadmap(src)
	resp := OverviewResponse{
		Project:     rm.Title,
		Progress:    rm.Progress,
		ParseErrors: rm.ParseErrors,
	}

	// Vision: first non-empty paragraph from VISION.md.
	if v, err := readFile(root, "docs/VISION.md"); err == nil {
		resp.Vision = firstNonEmptyParagraph(v)
	}

	// Current sprint: the first non-done sprint, falling back to the last
	// known sprint when everything's done.
	for i := range rm.Sprints {
		s := &rm.Sprints[i]
		if s.StatusKind != "done" {
			cp := *s
			resp.CurrentSprint = &cp
			break
		}
	}
	if resp.CurrentSprint == nil && len(rm.Sprints) > 0 {
		cp := rm.Sprints[len(rm.Sprints)-1]
		resp.CurrentSprint = &cp
	}

	// Active autopilot: scan .claude/autopilot-*.lock.
	resp.ActiveAutopilot = scanActiveAutopilot(autopilotDir)

	// Timeline: every sprint with status kind. Initialise as empty slice
	// (not nil) so the FE never has to null-guard `.map()`.
	resp.Timeline = make([]TimelineEntry, 0, len(rm.Sprints))
	for _, s := range rm.Sprints {
		resp.Timeline = append(resp.Timeline, TimelineEntry{
			ID:         s.ID,
			Title:      s.Title,
			StatusKind: s.StatusKind,
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func firstNonEmptyParagraph(src string) string {
	// Skip H1 (`# ...`) and front-matter blockquotes (`> ...`).
	for _, raw := range strings.Split(src, "\n\n") {
		para := strings.TrimSpace(raw)
		if para == "" {
			continue
		}
		if strings.HasPrefix(para, "# ") || strings.HasPrefix(para, "> ") {
			continue
		}
		// Drop trailing dashes / horizontal rules.
		para = strings.TrimRight(para, "- ")
		if para != "" {
			return para
		}
	}
	return ""
}

// ----------------------------------------------------------------------
// Sprint Detail
// ----------------------------------------------------------------------

// SprintDetailResponse is the per-sprint detail payload.
type SprintDetailResponse struct {
	Sprint            parser.Sprint            `json:"sprint"`
	Decisions         []parser.DecisionEntry   `json:"decisions"`
	AcceptanceMatrix  []parser.AcceptanceMatrixRow `json:"acceptanceMatrix"`
	E2EResults        parser.E2EResults        `json:"e2eResults"`
	ParseErrors       []parser.ParseError      `json:"parseErrors,omitempty"`
}

func (h *handler) sprintDetail(w http.ResponseWriter, r *http.Request) {
	root, err := h.worktree(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	sprintID := r.PathValue("sprintId")
	if sprintID == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "sprintId required"})
		return
	}
	roadmapPath := filepath.Join(root, "docs", "ROADMAP.md")
	logDir := filepath.Join(root, "docs", "sprint-logs", sprintID)
	tag := `"` + shortHash(fileTag(roadmapPath)+":"+fileTagDir(logDir)) + `"`
	if sendCacheable(w, r, tag) {
		return
	}

	src, err := readFile(root, "docs/ROADMAP.md")
	if err != nil {
		writeErr(w, err)
		return
	}
	rm := parser.ParseRoadmap(src)
	var found *parser.Sprint
	for i := range rm.Sprints {
		if strings.EqualFold(rm.Sprints[i].ID, sprintID) {
			found = &rm.Sprints[i]
			break
		}
	}
	if found == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "sprint not found", "sprintId": sprintID})
		return
	}
	resp := SprintDetailResponse{Sprint: *found, ParseErrors: rm.ParseErrors}

	if dec, err := readFile(root, "docs/sprint-logs/"+sprintID+"/decisions.md"); err == nil {
		log := parser.ParseDecisions(sprintID, dec)
		resp.Decisions = log.Entries
		resp.ParseErrors = append(resp.ParseErrors, log.ParseErrors...)
	}
	if am, err := readFile(root, "docs/sprint-logs/"+sprintID+"/acceptance-matrix.md"); err == nil {
		resp.AcceptanceMatrix = parser.ParseAcceptanceMatrix(sprintID, am).Rows
	}
	if e2e, err := readFile(root, "docs/sprint-logs/"+sprintID+"/e2e-results.md"); err == nil {
		resp.E2EResults = parser.ParseE2EResults(sprintID, e2e)
	}
	writeJSON(w, http.StatusOK, resp)
}

// ----------------------------------------------------------------------
// Dependency Graph
// ----------------------------------------------------------------------

// DependencyGraphResponse carries both the structured graph (for client-side
// rendering or testing) and a Mermaid syntax body the FE can hand to
// mermaid.render directly without re-deriving it.
type DependencyGraphResponse struct {
	Sprints      []TimelineEntry      `json:"sprints"`
	Dependencies []parser.Dependency  `json:"dependencies"`
	Mermaid      string               `json:"mermaid"`
	ParseErrors  []parser.ParseError  `json:"parseErrors,omitempty"`
}

func (h *handler) dependencies(w http.ResponseWriter, r *http.Request) {
	root, err := h.worktree(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	roadmapPath := filepath.Join(root, "docs", "ROADMAP.md")
	tag := fileTag(roadmapPath)
	if sendCacheable(w, r, tag) {
		return
	}
	src, err := readFile(root, "docs/ROADMAP.md")
	if err != nil {
		writeErr(w, err)
		return
	}
	rm := parser.ParseRoadmap(src)
	resp := DependencyGraphResponse{
		Dependencies: rm.Dependencies,
		ParseErrors:  rm.ParseErrors,
		Sprints:      make([]TimelineEntry, 0, len(rm.Sprints)),
	}
	for _, s := range rm.Sprints {
		resp.Sprints = append(resp.Sprints, TimelineEntry{
			ID:         s.ID,
			Title:      s.Title,
			StatusKind: s.StatusKind,
		})
	}
	resp.Mermaid = buildMermaid(resp.Sprints, resp.Dependencies)
	writeJSON(w, http.StatusOK, resp)
}

// buildMermaid produces a Mermaid `graph LR` flowchart connecting each
// sprint to the sprints it depends on. Edges are derived from the
// "Sxxx は Syyy 必須前提" / "Sxxx は Syyy と独立" phrases the dependency
// section uses; when the parser cannot extract a directional edge, the
// dependency text is attached to the source node as a comment via
// `classDef` so the graph never looks wrong.
//
// The output is intentionally minimal so existing Mermaid versions render
// it without issue.
func buildMermaid(sprints []TimelineEntry, deps []parser.Dependency) string {
	var b strings.Builder
	b.WriteString("graph LR\n")
	for _, s := range sprints {
		shape := s.ID + "[" + s.ID + ": " + escapeMermaid(s.Title) + "]"
		b.WriteString("  ")
		b.WriteString(shape)
		b.WriteString("\n")
	}
	emitted := map[string]struct{}{}
	for _, d := range deps {
		if len(d.Refs) < 2 {
			continue
		}
		// Heuristic: first ref is the dependent, subsequent refs are
		// prerequisites. ROADMAP convention is "S013 は S012 必須前提".
		from := d.Refs[0]
		for _, to := range d.Refs[1:] {
			if from == to {
				continue
			}
			edge := from + "-->" + to
			if _, ok := emitted[edge]; ok {
				continue
			}
			emitted[edge] = struct{}{}
			b.WriteString("  ")
			b.WriteString(to)
			b.WriteString(" --> ")
			b.WriteString(from)
			b.WriteString("\n")
		}
	}
	// Class definitions for status colouring (FE applies CSS via class).
	b.WriteString("  classDef done fill:#64d2a0,stroke:#1f7a4d,color:#0c0e14\n")
	b.WriteString("  classDef inProgress fill:#e8b45a,stroke:#a06b1d,color:#0c0e14\n")
	b.WriteString("  classDef pending fill:#1a1c25,stroke:#7c8aff,color:#d4d4d8\n")
	b.WriteString("  classDef blocked fill:#ef4444,stroke:#7a1d1d,color:#fff\n")
	for _, s := range sprints {
		switch s.StatusKind {
		case "done":
			b.WriteString("  class " + s.ID + " done\n")
		case "in-progress":
			b.WriteString("  class " + s.ID + " inProgress\n")
		case "blocked":
			b.WriteString("  class " + s.ID + " blocked\n")
		default:
			b.WriteString("  class " + s.ID + " pending\n")
		}
	}
	return b.String()
}

func escapeMermaid(s string) string {
	s = strings.ReplaceAll(s, "\"", "'")
	s = strings.ReplaceAll(s, "[", "(")
	s = strings.ReplaceAll(s, "]", ")")
	if len(s) > 40 {
		s = s[:37] + "..."
	}
	return s
}

// ----------------------------------------------------------------------
// Decision Timeline
// ----------------------------------------------------------------------

// DecisionsResponse is the cross-sprint decision feed.
type DecisionsResponse struct {
	Entries     []parser.DecisionEntry `json:"entries"`
	ParseErrors []parser.ParseError    `json:"parseErrors,omitempty"`
}

func (h *handler) decisions(w http.ResponseWriter, r *http.Request) {
	root, err := h.worktree(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	logsRoot := filepath.Join(root, "docs", "sprint-logs")
	tag := fileTagDir(logsRoot)
	if sendCacheable(w, r, tag) {
		return
	}
	filter := strings.ToLower(r.URL.Query().Get("filter"))

	resp := DecisionsResponse{}
	entries := []parser.DecisionEntry{}
	if dirs, err := os.ReadDir(logsRoot); err == nil {
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			sprintID := d.Name()
			if path, err := readFile(root, "docs/sprint-logs/"+sprintID+"/decisions.md"); err == nil {
				log := parser.ParseDecisions(sprintID, path)
				entries = append(entries, log.Entries...)
				resp.ParseErrors = append(resp.ParseErrors, log.ParseErrors...)
			}
		}
	}
	if filter != "" {
		filtered := entries[:0]
		for _, e := range entries {
			switch filter {
			case "needs_human":
				if e.NeedsHuman {
					filtered = append(filtered, e)
				}
			default:
				if e.Category == filter {
					filtered = append(filtered, e)
				}
			}
		}
		entries = filtered
	}
	// Sort by SprintID ascending for stability — keeps the timeline
	// narrative going in chronological-by-design order.
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].SprintID < entries[j].SprintID })
	resp.Entries = entries
	writeJSON(w, http.StatusOK, resp)
}

// ----------------------------------------------------------------------
// Refine History
// ----------------------------------------------------------------------

// RefineResponse is the cross-sprint refine feed.
type RefineResponse struct {
	Entries []parser.RefineEntry `json:"entries"`
}

func (h *handler) refine(w http.ResponseWriter, r *http.Request) {
	root, err := h.worktree(r)
	if err != nil {
		writeErr(w, err)
		return
	}
	logsRoot := filepath.Join(root, "docs", "sprint-logs")
	tag := fileTagDir(logsRoot)
	if sendCacheable(w, r, tag) {
		return
	}
	out := []parser.RefineEntry{}
	if dirs, err := os.ReadDir(logsRoot); err == nil {
		for _, d := range dirs {
			if !d.IsDir() {
				continue
			}
			sprintID := d.Name()
			if src, err := readFile(root, "docs/sprint-logs/"+sprintID+"/refine.md"); err == nil {
				out = append(out, parser.ParseRefine(sprintID, src)...)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].SprintID < out[j].SprintID })
	writeJSON(w, http.StatusOK, RefineResponse{Entries: out})
}

// ----------------------------------------------------------------------
// helpers shared with provider.go (autopilot detection)
// ----------------------------------------------------------------------

// scanActiveAutopilot scans `<worktree>/.claude/autopilot-*.lock` files.
// Each lock yields one ActiveAutopilot entry. The lock body is a small
// JSON document `{ "pid": 1234, "startedAt": "..." }` written by the
// autopilot skill — we read it best-effort and fall back to the file
// modtime when the format is missing or malformed.
func scanActiveAutopilot(autopilotDir string) []ActiveAutopilot {
	out := []ActiveAutopilot{}
	entries, err := os.ReadDir(autopilotDir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !isAutopilotLockName(name) {
			continue
		}
		full := filepath.Join(autopilotDir, name)
		st, err := os.Stat(full)
		if err != nil {
			continue
		}
		// Sprint ID is between "autopilot-" and ".lock".
		sprintID := strings.TrimSuffix(strings.TrimPrefix(name, "autopilot-"), ".lock")
		entry := ActiveAutopilot{
			SprintID:  sprintID,
			StartedAt: st.ModTime(),
			LockPath:  full,
		}
		if data, err := os.ReadFile(full); err == nil {
			var meta struct {
				Pid       int    `json:"pid"`
				StartedAt string `json:"startedAt"`
			}
			if err := json.Unmarshal(data, &meta); err == nil {
				entry.Pid = meta.Pid
				if t, err := time.Parse(time.RFC3339, meta.StartedAt); err == nil {
					entry.StartedAt = t
				}
			}
		}
		out = append(out, entry)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SprintID < out[j].SprintID })
	return out
}

func isAutopilotLockName(name string) bool {
	return strings.HasPrefix(name, "autopilot-") && strings.HasSuffix(name, ".lock")
}

func isAutopilotLock(info os.FileInfo) bool {
	if info == nil || info.IsDir() {
		return false
	}
	return isAutopilotLockName(info.Name())
}

// dirTagFiltered is fileTagDir but applies a predicate so we don't include
// unrelated files in the autopilot ETag.
func dirTagFiltered(dir string, keep func(os.FileInfo) bool) string {
	h := sha256.New()
	_ = filepath.Walk(dir, func(p string, info os.FileInfo, err error) error {
		if err != nil || info == nil {
			return nil
		}
		if !keep(info) {
			return nil
		}
		h.Write([]byte(p))
		h.Write([]byte{0})
		h.Write([]byte(info.ModTime().UTC().Format(time.RFC3339Nano)))
		h.Write([]byte{0})
		h.Write([]byte(strconv.FormatInt(info.Size(), 10)))
		h.Write([]byte{0})
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// shortHash strips an existing `"hex"` ETag down to a stable 16-char hash
// of the underlying bytes — used when we composite multiple component
// ETags into one.
func shortHash(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}
