package claudeagent

import (
	"encoding/json"
	"sync"
	"time"
)

// Session is the in-memory snapshot for one Agent. It holds the cached turns
// (replayed to clients on connect) and tracks all in-flight work — partial
// blocks during streaming, pending permission requests, and current status.
//
// Thread-safe. All mutations happen under mu.
type Session struct {
	mu sync.Mutex

	repoID, branchID string
	sessionID        string
	model            string
	permissionMode   string

	status        AgentStatus
	totalCostUSD  float64
	authStatus    AuthStatus

	turns []*Turn

	// Partial state for the current assistant turn — keyed by stream_event
	// index to support multiple concurrent blocks.
	currentTurn *Turn
	openBlocks  map[int]*Block

	// Pending permission requests — permission_id → CLI control request_id.
	// Permissions answered via UI close the loop on the corresponding request.
	pendingPermissions map[string]string
	allowList          map[string]struct{} // tool_name → allowed for this session

	createdAt time.Time
}

func NewSession(repoID, branchID, sessionID, model, permissionMode string) *Session {
	return &Session{
		repoID:            repoID,
		branchID:          branchID,
		sessionID:         sessionID,
		model:             model,
		permissionMode:    permissionMode,
		status:            StatusIdle,
		turns:             []*Turn{},
		openBlocks:        map[int]*Block{},
		pendingPermissions: map[string]string{},
		allowList:         map[string]struct{}{},
		createdAt:         time.Now().UTC(),
	}
}

// Snapshot returns a SessionInitPayload safe to ship to a freshly-connected
// client. The Turns slice is a deep-ish copy so further mutations don't race.
func (s *Session) Snapshot() SessionInitPayload {
	s.mu.Lock()
	defer s.mu.Unlock()
	turns := make([]Turn, 0, len(s.turns))
	for _, t := range s.turns {
		turns = append(turns, *t.deepCopy())
	}
	return SessionInitPayload{
		SessionID:      s.sessionID,
		BranchID:       s.branchID,
		RepoID:         s.repoID,
		Model:          s.model,
		PermissionMode: s.permissionMode,
		Status:         s.status,
		Turns:          turns,
		TotalCostUSD:   s.totalCostUSD,
		AuthOK:         s.authStatus.OK,
		AuthMessage:    s.authStatus.Message,
	}
}

// SessionID returns the latest known CLI session_id (may be "" before init).
func (s *Session) SessionID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessionID
}

// SetSessionID is called when the CLI emits its system/init line.
func (s *Session) SetSessionID(id string) (replaced bool, old string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	old = s.sessionID
	s.sessionID = id
	return old != "" && old != id, old
}

// SetAuthStatus stamps the result of a CheckAuth probe so the snapshot can
// surface the setup hint when needed.
func (s *Session) SetAuthStatus(a AuthStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authStatus = a
}

// SetStatus updates the high-level status pip and returns the new value.
func (s *Session) SetStatus(st AgentStatus) AgentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = st
	return st
}

// Status returns the current status.
func (s *Session) Status() AgentStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Model / PermissionMode getters (used by snapshot reconstruction logic).
func (s *Session) Model() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.model
}

// SetModel records the new model so future snapshots reflect the change.
func (s *Session) SetModel(model string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.model = model
}

// SetPermissionMode records the new mode.
func (s *Session) SetPermissionMode(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.permissionMode = mode
}

// PermissionMode returns the current permission mode.
func (s *Session) PermissionMode() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.permissionMode
}

// AppendUserTurn records a user message turn and returns the new turn ID.
func (s *Session) AppendUserTurn(content string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &Turn{Role: "user", ID: newID("turn"), Blocks: []Block{{
		ID:   newID("block"),
		Kind: "text",
		Text: content,
		Done: true,
	}}}
	s.turns = append(s.turns, t)
	return t.ID
}

// StartAssistantTurn opens a new assistant turn and returns its ID.
func (s *Session) StartAssistantTurn() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &Turn{Role: "assistant", ID: newID("turn"), Blocks: []Block{}}
	s.turns = append(s.turns, t)
	s.currentTurn = t
	s.openBlocks = map[int]*Block{}
	return t.ID
}

// CloseTurn finalizes the current assistant turn, accumulates cost and turn
// count, and returns the turn ID.
func (s *Session) CloseTurn(costUSD float64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := ""
	if s.currentTurn != nil {
		id = s.currentTurn.ID
	}
	s.totalCostUSD += costUSD
	s.currentTurn = nil
	s.openBlocks = map[int]*Block{}
	return id
}

// OpenBlock records a new block in the current assistant turn. If no turn is
// open, one is started implicitly (to handle stream_event arriving before any
// `assistant` envelope).
func (s *Session) OpenBlock(index int, kind string) (turnID, blockID string, openedTurn bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentTurn == nil {
		t := &Turn{Role: "assistant", ID: newID("turn"), Blocks: []Block{}}
		s.turns = append(s.turns, t)
		s.currentTurn = t
		openedTurn = true
	}
	b := &Block{ID: newID("block"), Kind: kind, Index: index}
	s.openBlocks[index] = b
	s.currentTurn.Blocks = append(s.currentTurn.Blocks, *b)
	return s.currentTurn.ID, b.ID, openedTurn
}

// AppendBlockText is the merge for text/thinking deltas. Returns the blockId
// and the new accumulated text so callers can ship it back to the client.
func (s *Session) AppendBlockText(index int, kind, text string) (turnID, blockID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentTurn == nil {
		return "", ""
	}
	b, ok := s.openBlocks[index]
	if !ok {
		nb := &Block{ID: newID("block"), Kind: kind, Index: index}
		s.openBlocks[index] = nb
		s.currentTurn.Blocks = append(s.currentTurn.Blocks, *nb)
		b = nb
	}
	b.Text += text
	for i := range s.currentTurn.Blocks {
		if s.currentTurn.Blocks[i].ID == b.ID {
			s.currentTurn.Blocks[i].Text = b.Text
			break
		}
	}
	return s.currentTurn.ID, b.ID
}

// SetBlockToolUse fills in tool_use metadata on an open block (called on
// content_block_start for tool_use, or directly from a complete assistant
// envelope).
func (s *Session) SetBlockToolUse(index int, name string, input json.RawMessage) (turnID, blockID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentTurn == nil {
		return "", ""
	}
	b, ok := s.openBlocks[index]
	if !ok {
		nb := &Block{ID: newID("block"), Kind: "tool_use", Index: index}
		s.openBlocks[index] = nb
		s.currentTurn.Blocks = append(s.currentTurn.Blocks, *nb)
		b = nb
	}
	b.Name = name
	b.Input = input
	for i := range s.currentTurn.Blocks {
		if s.currentTurn.Blocks[i].ID == b.ID {
			s.currentTurn.Blocks[i].Kind = "tool_use"
			s.currentTurn.Blocks[i].Name = name
			s.currentTurn.Blocks[i].Input = input
			break
		}
	}
	return s.currentTurn.ID, b.ID
}

// AppendToolInputPartial concatenates a partial_json delta onto the open
// tool_use block.
func (s *Session) AppendToolInputPartial(index int, partial string) (turnID, blockID, accum string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentTurn == nil {
		return "", "", ""
	}
	b, ok := s.openBlocks[index]
	if !ok {
		nb := &Block{ID: newID("block"), Kind: "tool_use", Index: index}
		s.openBlocks[index] = nb
		s.currentTurn.Blocks = append(s.currentTurn.Blocks, *nb)
		b = nb
	}
	b.Text += partial // we reuse Text as the partial accumulator until parse
	for i := range s.currentTurn.Blocks {
		if s.currentTurn.Blocks[i].ID == b.ID {
			s.currentTurn.Blocks[i].Text = b.Text
			break
		}
	}
	return s.currentTurn.ID, b.ID, b.Text
}

// FinalizeBlock flips the Done bit on the block at index — called from
// content_block_stop. If the block was a tool_use accumulating partial JSON
// in Text, parse it and move it into Input.
func (s *Session) FinalizeBlock(index int) (turnID, blockID string, finalInput json.RawMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentTurn == nil {
		return "", "", nil
	}
	b, ok := s.openBlocks[index]
	if !ok {
		return s.currentTurn.ID, "", nil
	}
	b.Done = true
	if b.Kind == "tool_use" && len(b.Input) == 0 && b.Text != "" {
		if json.Valid([]byte(b.Text)) {
			b.Input = json.RawMessage(b.Text)
			b.Text = ""
		}
	}
	for i := range s.currentTurn.Blocks {
		if s.currentTurn.Blocks[i].ID == b.ID {
			s.currentTurn.Blocks[i] = *b
			break
		}
	}
	delete(s.openBlocks, index)
	if b.Kind == "tool_use" && len(b.Input) > 0 {
		finalInput = b.Input
	}
	return s.currentTurn.ID, b.ID, finalInput
}

// ApplyAssistantMessage merges a complete assistant envelope into the
// current turn's blocks (used as a fallback when partial deltas are
// missing). Returns the turn ID.
func (s *Session) ApplyAssistantMessage(blocks []contentBlock) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentTurn == nil {
		t := &Turn{Role: "assistant", ID: newID("turn"), Blocks: []Block{}}
		s.turns = append(s.turns, t)
		s.currentTurn = t
	}
	for i, cb := range blocks {
		switch cb.Type {
		case "text":
			s.upsertCompleteBlock(i, Block{ID: newID("block"), Kind: "text", Index: i, Text: cb.Text, Done: true})
		case "thinking":
			s.upsertCompleteBlock(i, Block{ID: newID("block"), Kind: "thinking", Index: i, Text: cb.Thinking, Done: true})
		case "tool_use":
			s.upsertCompleteBlock(i, Block{ID: newID("block"), Kind: "tool_use", Index: i, Name: cb.Name, Input: cb.Input, Done: true})
		}
	}
	return s.currentTurn.ID
}

// upsertCompleteBlock either replaces an open block at index, or appends.
// Caller must hold s.mu.
func (s *Session) upsertCompleteBlock(index int, b Block) {
	if existing, ok := s.openBlocks[index]; ok {
		// Preserve the existing ID so any UI references stay valid.
		b.ID = existing.ID
		for i := range s.currentTurn.Blocks {
			if s.currentTurn.Blocks[i].ID == existing.ID {
				s.currentTurn.Blocks[i] = b
				break
			}
		}
		delete(s.openBlocks, index)
		return
	}
	s.currentTurn.Blocks = append(s.currentTurn.Blocks, b)
}

// AppendToolResult records a tool_result that the CLI shipped inside a user
// envelope. Tool results are stored as their own user-side turn with a
// single tool_result block, keeping them visually adjacent to the tool_use
// they answer.
func (s *Session) AppendToolResult(toolUseID, output string, isError bool) (turnID, blockID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &Turn{Role: "tool", ID: newID("turn"), Blocks: []Block{{
		ID:      newID("block"),
		Kind:    "tool_result",
		Output:  output,
		IsError: isError,
		Done:    true,
	}}}
	s.turns = append(s.turns, t)
	_ = toolUseID
	return t.ID, t.Blocks[0].ID
}

// AddTodoBlock appends or replaces a TodoWrite block in the current turn.
// Per spec, repeat invocations within one turn replace rather than append.
func (s *Session) AddTodoBlock(todos json.RawMessage) (turnID, blockID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.currentTurn == nil {
		t := &Turn{Role: "assistant", ID: newID("turn"), Blocks: []Block{}}
		s.turns = append(s.turns, t)
		s.currentTurn = t
	}
	for i := range s.currentTurn.Blocks {
		if s.currentTurn.Blocks[i].Kind == "todo" {
			s.currentTurn.Blocks[i].Todos = todos
			return s.currentTurn.ID, s.currentTurn.Blocks[i].ID
		}
	}
	b := Block{ID: newID("block"), Kind: "todo", Todos: todos, Done: true}
	s.currentTurn.Blocks = append(s.currentTurn.Blocks, b)
	return s.currentTurn.ID, b.ID
}

// AddPermissionRequest registers a pending permission and returns the
// permission_id assigned to it. Both the CLI request_id and the desired UI
// payload are stored.
func (s *Session) AddPermissionRequest(cliRequestID, toolName string, input json.RawMessage) (permissionID, turnID, blockID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	permissionID = newID("perm")
	s.pendingPermissions[permissionID] = cliRequestID
	if s.currentTurn == nil {
		t := &Turn{Role: "assistant", ID: newID("turn"), Blocks: []Block{}}
		s.turns = append(s.turns, t)
		s.currentTurn = t
	}
	b := Block{
		ID:           newID("block"),
		Kind:         "permission",
		Done:         false,
		PermissionID: permissionID,
		ToolName:     toolName,
		Input:        input,
	}
	s.currentTurn.Blocks = append(s.currentTurn.Blocks, b)
	return permissionID, s.currentTurn.ID, b.ID
}

// ResolvePermission marks the given permission as decided. Returns the CLI
// request_id (so the caller can ship a control_response) and a snapshot of
// the block for re-emission. Returns "", false if unknown.
func (s *Session) ResolvePermission(permissionID, decision string) (cliRequestID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cliRequestID, ok = s.pendingPermissions[permissionID]
	if !ok {
		return "", false
	}
	delete(s.pendingPermissions, permissionID)
	for _, t := range s.turns {
		for i := range t.Blocks {
			if t.Blocks[i].PermissionID == permissionID {
				t.Blocks[i].Decision = decision
				t.Blocks[i].Done = true
				return cliRequestID, true
			}
		}
	}
	return cliRequestID, true
}

// ToolNameForPermission returns the tool_name attached to the permission
// block for the given permission_id, or "" if unknown.
func (s *Session) ToolNameForPermission(permissionID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.turns {
		for _, b := range t.Blocks {
			if b.PermissionID == permissionID {
				return b.ToolName
			}
		}
	}
	return ""
}

// AddSessionAllow whitelists a tool for the rest of this session.
func (s *Session) AddSessionAllow(toolName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.allowList[toolName] = struct{}{}
}

// IsAllowedThisSession reports whether AddSessionAllow has been called for
// this tool.
func (s *Session) IsAllowedThisSession(toolName string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.allowList[toolName]
	return ok
}

// Reset clears the in-memory state for /clear semantics. The caller is
// responsible for spawning a fresh Client afterwards.
func (s *Session) Reset() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	old := s.sessionID
	s.sessionID = ""
	s.turns = []*Turn{}
	s.currentTurn = nil
	s.openBlocks = map[int]*Block{}
	s.pendingPermissions = map[string]string{}
	s.allowList = map[string]struct{}{}
	s.totalCostUSD = 0
	s.status = StatusIdle
	return old
}

// deepCopy returns a value copy of the turn safe for emission outside the
// session lock.
func (t *Turn) deepCopy() *Turn {
	out := &Turn{Role: t.Role, ID: t.ID, Blocks: make([]Block, len(t.Blocks))}
	for i, b := range t.Blocks {
		out.Blocks[i] = b
		if len(b.Input) > 0 {
			out.Blocks[i].Input = append(json.RawMessage(nil), b.Input...)
		}
		if len(b.Todos) > 0 {
			out.Blocks[i].Todos = append(json.RawMessage(nil), b.Todos...)
		}
	}
	return out
}
