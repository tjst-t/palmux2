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

	// 3) Pump client→server. Blocks on the request goroutine.
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
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": h.mgr.Store().List(repoID, branchID),
	})
}

func (h *httpHandler) handleListAllSessions(w http.ResponseWriter, r *http.Request) {
	repoID := r.URL.Query().Get("repo")
	branchID := r.URL.Query().Get("branch")
	writeJSON(w, http.StatusOK, map[string]any{
		"sessions": h.mgr.Store().List(repoID, branchID),
	})
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
