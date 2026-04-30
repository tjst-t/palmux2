package claudeagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
)

// httpHandler bundles the manager + a few helper methods. It deliberately
// owns no per-request state.
type httpHandler struct {
	mgr *Manager
}

func newHTTPHandler(mgr *Manager) *httpHandler { return &httpHandler{mgr: mgr} }

// ──────────── WS endpoint ──────────────────────────────────────────────────

func (h *httpHandler) handleWS(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")

	agent, err := h.mgr.EnsureAgent(repoID, branchID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}

	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		OriginPatterns: []string{"*"},
	})
	if err != nil {
		return
	}
	c.SetReadLimit(2 << 20)
	defer c.CloseNow()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	// Stamp the auth status so the snapshot can show the setup hint.
	auth := CheckAuth(h.mgr.Config().Binary)
	agent.SetAuthStatus(auth)

	events, unsub := agent.Subscribe()
	defer unsub()

	// 1) Send the init snapshot synchronously — this is what unblocks the
	// browser's loading state.
	snap := agent.Snapshot()
	if err := writeJSONFrame(ctx, c, EvSessionInit, snap); err != nil {
		return
	}

	// 2) Pump server→client.
	go pumpAgentToWS(ctx, c, events, cancel)

	// 3) Eager-spawn the CLI in the background so the slash-command popup,
	// model list, and MCP server states populate without waiting for the
	// user's first message. The CLI doesn't burn API quota until a user
	// turn happens, so this is cheap. A failure here just delays the
	// init payload — the UI still works on the cached lastInit.
	if auth.OK {
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			_ = agent.EnsureClient(ctx)
		}()
	}

	// 4) Pump client→server. Blocks on the request goroutine.
	pumpWSToAgent(ctx, c, agent, h.mgr)
}

func writeJSONFrame(ctx context.Context, c *websocket.Conn, typ EventType, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	frame := AgentEvent{Type: string(typ), TS: time.Now().UTC(), Payload: body}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	raw, err := json.Marshal(frame)
	if err != nil {
		return err
	}
	return c.Write(wctx, websocket.MessageText, raw)
}

func pumpAgentToWS(ctx context.Context, c *websocket.Conn, events <-chan AgentEvent, cancel context.CancelFunc) {
	defer cancel()
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-pingTicker.C:
			pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
			err := c.Ping(pctx)
			pcancel()
			if err != nil {
				return
			}
		case ev, ok := <-events:
			if !ok {
				return
			}
			raw, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			wctx, wcancel := context.WithTimeout(ctx, 5*time.Second)
			err = c.Write(wctx, websocket.MessageText, raw)
			wcancel()
			if err != nil {
				return
			}
		}
	}
}

func pumpWSToAgent(ctx context.Context, c *websocket.Conn, agent *Agent, mgr *Manager) {
	for {
		typ, data, err := c.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var frame ClientFrame
		if err := json.Unmarshal(data, &frame); err != nil {
			continue
		}
		switch frame.Type {
		case "user.message":
			var p UserMessageFrame
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				continue
			}
			if isSlashCommand(p.Content) {
				go handleSlashCommand(ctx, agent, mgr, p.Content)
				continue
			}
			go func(content string) {
				if err := agent.SendUserMessage(ctx, content); err != nil {
					if ev, e := makeEvent(EvError, ErrorPayload{Message: "Send failed", Detail: err.Error()}); e == nil {
						agent.broadcast(ev)
					}
				}
			}(p.Content)

		case "interrupt":
			go func() { _ = agent.Interrupt(ctx) }()

		case "permission.respond":
			var p PermissionRespondFrame
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				continue
			}
			// Permission frames addressed at an AskUserQuestion request
			// would route through the wrong path (allow/deny semantics
			// don't fit asking). Forwarding to AnswerAskQuestion keeps
			// older clients working when they accidentally submit
			// permission.respond instead of ask.respond.
			if agent.session.IsAskPermission(p.PermissionID) {
				if perr := agent.AnswerAskQuestion(AskRespondFrame{
					PermissionID: p.PermissionID,
					Answers:      [][]string{},
				}); perr != nil {
					if ev, e := makeEvent(EvError, ErrorPayload{Message: "Ask permission failed", Detail: perr.Error()}); e == nil {
						agent.broadcast(ev)
					}
				}
				continue
			}
			// Same idea for ExitPlanMode: a plain permission.respond
			// addressed at a plan permission_id should route through
			// AnswerPlanResponse so the CLI gets the matching shape
			// and the kind:"plan" block stamps a decision. Decision
			// "allow" → approve (no edits, no mode switch); "deny" →
			// reject. Any UpdatedInput is dropped because the schema
			// is plan-specific.
			if agent.session.IsPlanPermission(p.PermissionID) {
				decision := "approve"
				if p.Decision == "deny" {
					decision = "reject"
				}
				if perr := agent.AnswerPlanResponse(PlanRespondFrame{
					PermissionID: p.PermissionID,
					Decision:     decision,
				}); perr != nil {
					if ev, e := makeEvent(EvError, ErrorPayload{Message: "Plan permission failed", Detail: perr.Error()}); e == nil {
						agent.broadcast(ev)
					}
				}
				continue
			}
			// Scope "always" persists the rule to .claude/settings.json
			// (worktree scope) before answering.
			var perr error
			if p.Scope == "always" {
				perr = agent.AlwaysAllowTool(p)
			} else {
				perr = agent.AnswerPermission(p)
			}
			if perr != nil {
				if ev, e := makeEvent(EvError, ErrorPayload{Message: "Permission failed", Detail: perr.Error()}); e == nil {
					agent.broadcast(ev)
				}
			}

		case "ask.respond":
			var p AskRespondFrame
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				continue
			}
			if perr := agent.AnswerAskQuestion(p); perr != nil {
				if ev, e := makeEvent(EvError, ErrorPayload{Message: "Ask answer failed", Detail: perr.Error()}); e == nil {
					agent.broadcast(ev)
				}
			}

		case "plan.respond":
			var p PlanRespondFrame
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				continue
			}
			if perr := agent.AnswerPlanResponse(p); perr != nil {
				if ev, e := makeEvent(EvError, ErrorPayload{Message: "Plan answer failed", Detail: perr.Error()}); e == nil {
					agent.broadcast(ev)
				}
			}

		case "model.set":
			var p SetModelFrame
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				continue
			}
			go func() { _ = agent.SetModel(ctx, p.Model) }()

		case "effort.set":
			var p SetEffortFrame
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				continue
			}
			go func() {
				if err := agent.SetEffort(ctx, p.Effort); err != nil {
					if ev, e := makeEvent(EvError, ErrorPayload{
						Message: "Failed to set effort",
						Detail:  err.Error(),
					}); e == nil {
						agent.broadcast(ev)
					}
				}
			}()

		case "permission_mode.set":
			var p SetPermissionModeFrame
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				continue
			}
			go func() {
				if err := agent.SetPermissionMode(ctx, p.Mode); err != nil {
					if ev, e := makeEvent(EvError, ErrorPayload{
						Message: "Failed to switch permission mode",
						Detail:  err.Error(),
					}); e == nil {
						agent.broadcast(ev)
					}
				}
			}()

		case "session.clear":
			agent.Clear()

		case "session.resume":
			var p SessionResumeFrame
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				continue
			}
			go func() {
				if err := agent.ResumeSession(ctx, p.SessionID); err != nil {
					if ev, e := makeEvent(EvError, ErrorPayload{
						Message: "Failed to resume session",
						Detail:  err.Error(),
					}); e == nil {
						agent.broadcast(ev)
					}
				}
			}()

		case "session.fork":
			var p SessionForkFrame
			if err := json.Unmarshal(frame.Payload, &p); err != nil {
				continue
			}
			go func() {
				if err := agent.ForkSession(ctx, p.BaseSessionID); err != nil {
					if ev, e := makeEvent(EvError, ErrorPayload{
						Message: "Failed to fork session",
						Detail:  err.Error(),
					}); e == nil {
						agent.broadcast(ev)
					}
				}
			}()
		}
	}
}

func isSlashCommand(s string) bool {
	if len(s) == 0 || s[0] != '/' {
		return false
	}
	// Plain "/" is just a forward slash, not a command.
	for i := 1; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' {
			return true
		}
	}
	return len(s) > 1
}

// handleSlashCommand interprets the small set of MVP slash commands. Unknown
// commands fall through to the CLI as plain user messages so power users can
// still use CLI-native commands like /compact.
func handleSlashCommand(ctx context.Context, agent *Agent, _ *Manager, content string) {
	cmd, _ := splitOnce(content[1:])
	switch cmd {
	case "clear", "new":
		agent.Clear()
		return
	case "model":
		_, model := splitOnce(content[1:])
		if model == "" {
			if ev, err := makeEvent(EvError, ErrorPayload{Message: "Usage: /model <name>"}); err == nil {
				agent.broadcast(ev)
			}
			return
		}
		if err := agent.SetModel(ctx, model); err != nil {
			if ev, e := makeEvent(EvError, ErrorPayload{Message: "set_model failed", Detail: err.Error()}); e == nil {
				agent.broadcast(ev)
			}
		}
		return
	}
	// Unknown — pass through.
	_ = agent.SendUserMessage(ctx, content)
}

func splitOnce(s string) (head, rest string) {
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' || s[i] == '\n' {
			return s[:i], s[i+1:]
		}
	}
	return s, ""
}

// ──────────── REST endpoints ───────────────────────────────────────────────

func (h *httpHandler) handleAuthStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, CheckAuth(h.mgr.Config().Binary))
}

func (h *httpHandler) handleModes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, DetectPermissionModes(h.mgr.Config().Binary))
}

func (h *httpHandler) handleListBranchSessions(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	sessions := h.listMergedSessions(repoID, branchID)
	h.enrichWithTranscriptSummary(sessions)
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

func (h *httpHandler) handleListAllSessions(w http.ResponseWriter, r *http.Request) {
	repoID := r.URL.Query().Get("repo")
	branchID := r.URL.Query().Get("branch")
	sessions := h.listMergedSessions(repoID, branchID)
	h.enrichWithTranscriptSummary(sessions)
	writeJSON(w, http.StatusOK, map[string]any{"sessions": sessions})
}

// listMergedSessions returns the union of:
//   - sessions Palmux booked into sessions.json (have title/cost/turn count)
//   - .jsonl transcripts found on disk under the worktree's projects dir
//     (so sessions started via raw `claude` CLI also show up)
//
// Disk-discovered entries don't have cost / turn count from us, but we
// fill LastActivityAt from the file mtime so they sort sensibly. Entries
// found in both sources are merged with the Palmux record taking
// precedence. Disk discovery only fires when a single (repoID,branchID)
// is targeted — the global "all" case skips it because we'd have to
// walk every worktree.
func (h *httpHandler) listMergedSessions(repoID, branchID string) []SessionMeta {
	out := h.mgr.Store().List(repoID, branchID)
	if repoID == "" || branchID == "" {
		return out
	}
	worktree, err := h.mgr.Branches().WorktreePath(repoID, branchID)
	if err != nil || worktree == "" {
		return out
	}
	known := make(map[string]int, len(out))
	for i, s := range out {
		known[s.ID] = i
	}
	for _, d := range DiscoverTranscripts(worktree) {
		if _, ok := known[d.SessionID]; ok {
			// Refresh the activity timestamp if the transcript is newer
			// than what sessions.json recorded — common when the user
			// resumed the session outside Palmux.
			i := known[d.SessionID]
			if d.LastActivityAt.After(out[i].LastActivityAt) {
				out[i].LastActivityAt = d.LastActivityAt
			}
			continue
		}
		out = append(out, SessionMeta{
			ID:             d.SessionID,
			RepoID:         repoID,
			BranchID:       branchID,
			LastActivityAt: d.LastActivityAt,
		})
	}
	// Re-sort most-recent first since we may have appended.
	for i := 1; i < len(out); i++ {
		j := i
		for j > 0 && out[j].LastActivityAt.After(out[j-1].LastActivityAt) {
			out[j], out[j-1] = out[j-1], out[j]
			j--
		}
	}
	return out
}

// enrichWithTranscriptSummary reads each session's transcript file (if
// any) and fills the preview fields in place. Bounded to the most-recent
// 50 entries so a huge directory doesn't blow the budget.
func (h *httpHandler) enrichWithTranscriptSummary(sessions []SessionMeta) {
	const maxToEnrich = 50
	for i := range sessions {
		if i >= maxToEnrich {
			break
		}
		s := &sessions[i]
		worktree, err := h.mgr.Branches().WorktreePath(s.RepoID, s.BranchID)
		if err != nil || worktree == "" {
			continue
		}
		path, err := transcriptPath(worktree, s.ID)
		if err != nil {
			continue
		}
		sum := SummariseTranscript(path)
		s.FirstUserMessage = sum.FirstUserMessage
		s.LastUserMessage = sum.LastUserMessage
		s.LastAssistantSnippet = sum.LastAssistantSnippet
	}
}

func (h *httpHandler) handleGetSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sessionId")
	meta, ok := h.mgr.Store().Get(id)
	if !ok {
		http.Error(w, "session not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func (h *httpHandler) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sessionId")
	if err := h.mgr.Store().Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// permissionAnswerRequest is the body for the out-of-band Inbox-driven
// answer endpoint. Lets a non-active tab (or an Inbox row) resolve a
// pending permission without opening the Claude WS.
type permissionAnswerRequest struct {
	Decision     string          `json:"decision"`              // "allow" | "deny"
	Scope        string          `json:"scope,omitempty"`       // "once" | "session"
	UpdatedInput json.RawMessage `json:"updatedInput,omitempty"`
	Reason       string          `json:"reason,omitempty"`
}

func (h *httpHandler) handleAnswerPermission(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	permID := r.PathValue("permissionId")
	if permID == "" {
		http.Error(w, "missing permissionId", http.StatusBadRequest)
		return
	}
	agent := h.mgr.Get(repoID, branchID)
	if agent == nil {
		http.Error(w, "agent not running for this branch", http.StatusNotFound)
		return
	}
	var body permissionAnswerRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body)
	}
	if body.Decision != "allow" && body.Decision != "deny" {
		http.Error(w, "decision must be allow or deny", http.StatusBadRequest)
		return
	}
	scope := body.Scope
	if scope == "" {
		scope = "once"
	}
	if err := agent.AnswerPermission(PermissionRespondFrame{
		PermissionID: permID,
		Decision:     body.Decision,
		Scope:        scope,
		UpdatedInput: body.UpdatedInput,
		Reason:       body.Reason,
	}); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ──────────── Settings (.claude/settings.json) endpoints ──────────────────

// handleGetSettings reads the project + user settings.json and returns a
// structured bundle. Lazy in the sense that nothing is created — a missing
// file is reported as Exists=false so the UI can render an empty state
// without provoking a write.
func (h *httpHandler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	worktree, err := h.mgr.Branches().WorktreePath(repoID, branchID)
	if err != nil || worktree == "" {
		http.Error(w, "branch not found", http.StatusNotFound)
		return
	}
	bundle, err := loadSettingsBundle(worktree)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, bundle)
}

// handleDeleteSettingsAllow removes one entry from `permissions.allow` of
// the requested scope (project or user). Idempotent — deleting a missing
// entry returns 204.
//
//	DELETE …/tabs/claude/settings/permissions/allow?scope=project|user&pattern=Bash(ls)
//
// We require the explicit `scope` query parameter so the FE always
// confirms which file gets touched (DESIGN_PRINCIPLES「責務越境最小」:
// 触る対象を曖昧にしない).
func (h *httpHandler) handleDeleteSettingsAllow(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	scopeRaw := r.URL.Query().Get("scope")
	pattern := r.URL.Query().Get("pattern")
	if pattern == "" {
		http.Error(w, "missing pattern", http.StatusBadRequest)
		return
	}
	var scope settingsScope
	switch scopeRaw {
	case "project":
		scope = scopeProject
	case "user":
		scope = scopeUser
	default:
		http.Error(w, "scope must be project or user", http.StatusBadRequest)
		return
	}
	worktree, err := h.mgr.Branches().WorktreePath(repoID, branchID)
	if err != nil || worktree == "" {
		http.Error(w, "branch not found", http.StatusNotFound)
		return
	}
	if err := removeFromAllowList(scope, worktree, pattern); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// branchPrefsView is the public shape for GET/PATCH …/tabs/claude/prefs.
// The persisted BranchPrefs struct includes model/effort/permissionMode
// — those already have their own send paths (SetModel / SetEffort /
// SetPermissionMode WS frames). The prefs endpoint exists for the
// handful of toggles (currently just hook events) that don't fit a
// session-scoped frame because they require a CLI respawn.
type branchPrefsView struct {
	IncludeHookEvents bool `json:"includeHookEvents"`
}

// handleGetBranchPrefs returns the persisted prefs for a branch. Read
// straight off sessions.json — never spawns a CLI. Used by the
// Settings popup to render the "Include hook events" toggle in its
// current state.
func (h *httpHandler) handleGetBranchPrefs(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	prefs := h.mgr.Store().BranchPrefs(repoID, branchID)
	writeJSON(w, http.StatusOK, branchPrefsView{
		IncludeHookEvents: prefs.IncludeHookEvents,
	})
}

// handlePatchBranchPrefs updates the per-branch prefs and respawns the
// CLI if the change requires it (today: only IncludeHookEvents, which is
// a startup-only flag). When no agent exists yet (lazy spawn pre-first
// message) the new value is just persisted; the next spawn picks it up.
func (h *httpHandler) handlePatchBranchPrefs(w http.ResponseWriter, r *http.Request) {
	repoID := r.PathValue("repoId")
	branchID := r.PathValue("branchId")
	var p branchPrefsView
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			http.Error(w, "bad json: "+err.Error(), http.StatusBadRequest)
			return
		}
	}
	if a := h.mgr.Get(repoID, branchID); a != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
		defer cancel()
		if err := a.SetIncludeHookEvents(ctx, p.IncludeHookEvents); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, branchPrefsView{
			IncludeHookEvents: a.IncludeHookEvents(),
		})
		return
	}
	// No live agent: persist directly to the store so the next EnsureAgent
	// reads the new value.
	prefs := h.mgr.Store().BranchPrefs(repoID, branchID)
	prefs.IncludeHookEvents = p.IncludeHookEvents
	if err := h.mgr.Store().SetBranchPrefs(repoID, branchID, prefs); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, branchPrefsView{
		IncludeHookEvents: prefs.IncludeHookEvents,
	})
}

type sessionPatch struct {
	Title string `json:"title,omitempty"`
}

func (h *httpHandler) handlePatchSession(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("sessionId")
	var p sessionPatch
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&p)
	}
	if err := h.mgr.Store().UpdateMeta(id, func(m *SessionMeta) {
		if p.Title != "" {
			m.Title = p.Title
		}
	}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if meta, ok := h.mgr.Store().Get(id); ok {
		writeJSON(w, http.StatusOK, meta)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ──────────── helpers ──────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// errBranchNotFound is returned by BranchResolver implementations.
var errBranchNotFound = errors.New("claudeagent: branch not found")
