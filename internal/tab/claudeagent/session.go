package claudeagent

import (
	"encoding/json"
	"sync"
	"time"
)

// InitInfo is the subset of the initialize control_response we surface to
// the frontend. The CLI returns a much larger payload (account, plugins,
// available skills, etc.) — we keep only what the UI actually consumes.
type InitInfo struct {
	Commands []SlashCommand `json:"commands,omitempty"`
	Agents   []NamedItem    `json:"agents,omitempty"`
	Models   []ModelDescriptor `json:"models,omitempty"`
	OutputStyle           string   `json:"outputStyle,omitempty"`
	AvailableOutputStyles []string `json:"availableOutputStyles,omitempty"`
}

// SlashCommand describes one CLI-provided slash command.
type SlashCommand struct {
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	ArgumentHint string   `json:"argumentHint,omitempty"`
	Aliases      []string `json:"aliases,omitempty"`
}

type NamedItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Model       string `json:"model,omitempty"`
}

type ModelDescriptor struct {
	Value                  string   `json:"value"`
	DisplayName            string   `json:"displayName,omitempty"`
	Description            string   `json:"description,omitempty"`
	SupportsEffort         bool     `json:"supportsEffort,omitempty"`
	SupportedEffortLevels  []string `json:"supportedEffortLevels,omitempty"`
	SupportsAdaptiveThinking bool   `json:"supportsAdaptiveThinking,omitempty"`
	SupportsAutoMode       bool     `json:"supportsAutoMode,omitempty"`
}

// parseInitInfo extracts the bits we care about from the CLI's initialize
// response. Unknown / future fields are ignored.
func parseInitInfo(raw json.RawMessage) InitInfo {
	var v struct {
		Commands              []SlashCommand    `json:"commands"`
		Agents                []NamedItem       `json:"agents"`
		Models                []ModelDescriptor `json:"models"`
		OutputStyle           string            `json:"output_style"`
		AvailableOutputStyles []string          `json:"available_output_styles"`
	}
	_ = json.Unmarshal(raw, &v)
	return InitInfo{
		Commands:              v.Commands,
		Agents:                v.Agents,
		Models:                v.Models,
		OutputStyle:           v.OutputStyle,
		AvailableOutputStyles: v.AvailableOutputStyles,
	}
}

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
	effort           string

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

	// CLI-reported capabilities (commands, agents, models). Populated from
	// the initialize control_response.
	initInfo InitInfo

	// MCP server statuses, populated from system/init.
	mcpServers []MCPServerInfo

	// planToolUseIDs is the set of tool_use IDs that we've re-tagged from
	// "tool_use" → "plan" (ExitPlanMode). When the matching tool_result
	// envelope arrives we suppress it instead of rendering a redundant
	// "result" block underneath the plan. Bounded — entries are cleaned
	// up on Reset / when the corresponding tool_result is consumed.
	planToolUseIDs map[string]struct{}

	// askToolUseIDs mirrors planToolUseIDs for AskUserQuestion. The CLI
	// emits a tool_result with the chosen answer text; the AskQuestion
	// block in the UI already conveys the decision, so we drop it.
	askToolUseIDs map[string]struct{}

	// askPermissions tracks active AskUserQuestion permission requests
	// keyed by the canonical permission_id. This indexes back into the
	// pendingPermissions / permWaiters maps owned by the Agent so the
	// `ask.respond` handler can short-circuit the generic permission
	// resolution path. The value is the tool_use_id (= upstream
	// Anthropic id) whose tool_result we'll suppress.
	askPermissions map[string]string

	// planPermissions mirrors askPermissions for ExitPlanMode. The CLI
	// routes ExitPlanMode through the same MCP permission_prompt path as
	// any other tool, but its UI is the kind:"plan" block — Allow/Deny
	// buttons would be misleading. This map lets the `plan.respond`
	// handler resolve the right waiter and (separately) consume the
	// tool_result emitted on approve/reject.
	planPermissions map[string]string

	// currentParentToolUseID is the parent_tool_use_id field carried by
	// the envelope currently being processed. Set by the normalize layer
	// at the top of each processStreamMessage call and copied onto any new
	// Turn the message creates. Empty when the message belongs to the
	// top-level conversation. See protocol.go for wire details.
	currentParentToolUseID string

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
		planToolUseIDs:    map[string]struct{}{},
		askToolUseIDs:     map[string]struct{}{},
		askPermissions:    map[string]string{},
		planPermissions:   map[string]string{},
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
		Effort:         s.effort,
		PermissionMode: s.permissionMode,
		Status:         s.status,
		Turns:          turns,
		TotalCostUSD:   s.totalCostUSD,
		AuthOK:         s.authStatus.OK,
		AuthMessage:    s.authStatus.Message,
		InitInfo:       s.initInfo,
		MCPServers:     append([]MCPServerInfo(nil), s.mcpServers...),
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

// SetInitInfo records CLI capabilities (commands list, models, etc.).
func (s *Session) SetInitInfo(i InitInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initInfo = i
}

// InitInfo returns a copy of the cached CLI capabilities.
func (s *Session) InitInfo() InitInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.initInfo
}

// SetMCPServers records the MCP server statuses from system/init.
func (s *Session) SetMCPServers(servers []MCPServerInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.mcpServers = append(s.mcpServers[:0], servers...)
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

// Effort returns the current --effort value (or "" if unset).
func (s *Session) Effort() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.effort
}

// SetEffort updates the effort level.
func (s *Session) SetEffort(e string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.effort = e
}

// SetCurrentParentToolUseID stamps the parent_tool_use_id of the envelope
// currently being processed. Subsequent calls that create new Turns will
// copy this value onto the new Turn. Pass "" to clear (top-level message).
//
// Called by processStreamMessage at the top of every envelope.
func (s *Session) SetCurrentParentToolUseID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.currentParentToolUseID = id
}

// CurrentParentToolUseID returns the parent_tool_use_id stamped by
// SetCurrentParentToolUseID. Used by the normalize layer to populate
// turn.start payloads so the frontend can attach new mid-stream turns
// to the correct sub-agent ancestor.
func (s *Session) CurrentParentToolUseID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.currentParentToolUseID
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
	}}, ParentToolUseID: s.currentParentToolUseID}
	s.turns = append(s.turns, t)
	return t.ID
}

// StartAssistantTurn opens a new assistant turn and returns its ID.
func (s *Session) StartAssistantTurn() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	t := &Turn{Role: "assistant", ID: newID("turn"), Blocks: []Block{}, ParentToolUseID: s.currentParentToolUseID}
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
		t := &Turn{Role: "assistant", ID: newID("turn"), Blocks: []Block{}, ParentToolUseID: s.currentParentToolUseID}
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
// envelope). The block's existing Kind is preserved when it has been
// re-tagged (e.g. "plan" for ExitPlanMode); otherwise we set it to
// "tool_use".
//
// toolUseID is the upstream Anthropic tool_use_id; recorded so sub-agent
// turns can match against it via parent_tool_use_id.
func (s *Session) SetBlockToolUse(index int, name, toolUseID string, input json.RawMessage) (turnID, blockID string) {
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
	if toolUseID != "" {
		b.ToolUseID = toolUseID
	}
	if b.Kind == "" {
		b.Kind = "tool_use"
	}
	for i := range s.currentTurn.Blocks {
		if s.currentTurn.Blocks[i].ID == b.ID {
			if s.currentTurn.Blocks[i].Kind == "" || s.currentTurn.Blocks[i].Kind == "tool_use" {
				s.currentTurn.Blocks[i].Kind = b.Kind
			}
			s.currentTurn.Blocks[i].Name = name
			s.currentTurn.Blocks[i].Input = input
			if toolUseID != "" {
				s.currentTurn.Blocks[i].ToolUseID = toolUseID
			}
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
	if (b.Kind == "tool_use" || b.Kind == "plan" || b.Kind == "ask") && len(b.Input) == 0 && b.Text != "" {
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
	if (b.Kind == "tool_use" || b.Kind == "plan" || b.Kind == "ask") && len(b.Input) > 0 {
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
		t := &Turn{Role: "assistant", ID: newID("turn"), Blocks: []Block{}, ParentToolUseID: s.currentParentToolUseID}
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
			kind := "tool_use"
			if isPlanToolName(cb.Name) {
				kind = "plan"
				if cb.ID != "" {
					if s.planToolUseIDs == nil {
						s.planToolUseIDs = map[string]struct{}{}
					}
					s.planToolUseIDs[cb.ID] = struct{}{}
				}
			} else if isAskQuestionToolName(cb.Name) {
				kind = "ask"
				if cb.ID != "" {
					if s.askToolUseIDs == nil {
						s.askToolUseIDs = map[string]struct{}{}
					}
					s.askToolUseIDs[cb.ID] = struct{}{}
				}
			}
			s.upsertCompleteBlock(i, Block{ID: newID("block"), Kind: kind, Index: i, Name: cb.Name, Input: cb.Input, Done: true, ToolUseID: cb.ID})
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
	}}, ParentToolUseID: s.currentParentToolUseID}
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
		t := &Turn{Role: "assistant", ID: newID("turn"), Blocks: []Block{}, ParentToolUseID: s.currentParentToolUseID}
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

// RegisterPendingPermission registers a pending permission and returns the
// minted permission_id, but does NOT add a kind:"permission" block to the
// current turn. Used by the bypass paths (ExitPlanMode → kind:"plan",
// AskUserQuestion → kind:"ask") where the block-of-record is the
// re-tagged tool block, not a generic permission card. Adding a
// kind:"permission" block here would render as a duplicate UI underneath
// the plan/ask block (Allow / Deny buttons next to the proper Approve
// row) — exactly the bug S001-refine was filed to fix.
func (s *Session) RegisterPendingPermission(cliRequestID string) (permissionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	permissionID = newID("perm")
	s.pendingPermissions[permissionID] = cliRequestID
	return permissionID
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
		t := &Turn{Role: "assistant", ID: newID("turn"), Blocks: []Block{}, ParentToolUseID: s.currentParentToolUseID}
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

// MarkPlanToolUse remembers a tool_use_id whose block has been re-tagged
// to kind:"plan". The matching tool_result envelope (which the CLI emits
// for ExitPlanMode regardless of approve/reject) will be suppressed by
// ConsumePlanToolResult so the UI doesn't show a stray "result" line
// underneath the plan block.
func (s *Session) MarkPlanToolUse(toolUseID string) {
	if toolUseID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.planToolUseIDs == nil {
		s.planToolUseIDs = map[string]struct{}{}
	}
	s.planToolUseIDs[toolUseID] = struct{}{}
}

// ConsumePlanToolResult reports whether the given tool_use_id was
// previously marked as a plan tool. If so the entry is removed (one-shot)
// so the caller can suppress its tool_result.
func (s *Session) ConsumePlanToolResult(toolUseID string) bool {
	if toolUseID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.planToolUseIDs[toolUseID]; !ok {
		return false
	}
	delete(s.planToolUseIDs, toolUseID)
	return true
}

// MarkAskToolUse remembers a tool_use_id whose block was re-tagged to
// kind:"ask" (AskUserQuestion). Mirrors MarkPlanToolUse — the matching
// tool_result will be suppressed by ConsumeAskToolResult so the UI
// doesn't show a stray result line under the question.
func (s *Session) MarkAskToolUse(toolUseID string) {
	if toolUseID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.askToolUseIDs == nil {
		s.askToolUseIDs = map[string]struct{}{}
	}
	s.askToolUseIDs[toolUseID] = struct{}{}
}

// ConsumeAskToolResult is the ask-side counterpart of
// ConsumePlanToolResult: one-shot check + remove.
func (s *Session) ConsumeAskToolResult(toolUseID string) bool {
	if toolUseID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.askToolUseIDs[toolUseID]; !ok {
		return false
	}
	delete(s.askToolUseIDs, toolUseID)
	return true
}

// RegisterAskPermission ties a permission_id (issued by the generic
// PermissionRequester path) to the AskUserQuestion tool_use_id whose
// answer it carries. The Agent calls this when a permission_prompt
// MCP request is detected to be for AskUserQuestion, so the
// `ask.respond` handler can look the right waiter up by permission_id
// and so the corresponding tool_result is suppressed.
func (s *Session) RegisterAskPermission(permissionID, toolUseID string) {
	if permissionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.askPermissions == nil {
		s.askPermissions = map[string]string{}
	}
	s.askPermissions[permissionID] = toolUseID
	// Also remember the tool_use_id for tool_result suppression. (Some
	// CLI flows take the permission_prompt path *without* having shipped
	// a prior content_block_start tool_use — e.g. when AskUserQuestion
	// is the first block in the turn.)
	if toolUseID != "" {
		if s.askToolUseIDs == nil {
			s.askToolUseIDs = map[string]struct{}{}
		}
		s.askToolUseIDs[toolUseID] = struct{}{}
	}
}

// ConsumeAskPermission reports whether the given permission_id was
// registered as an AskUserQuestion permission. If so it returns the
// associated tool_use_id and removes the entry. Used by the
// `ask.respond` frame handler.
func (s *Session) ConsumeAskPermission(permissionID string) (toolUseID string, ok bool) {
	if permissionID == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	toolUseID, ok = s.askPermissions[permissionID]
	if !ok {
		return "", false
	}
	delete(s.askPermissions, permissionID)
	return toolUseID, true
}

// IsAskPermission reports whether the given permission_id corresponds
// to an active AskUserQuestion request without consuming it.
func (s *Session) IsAskPermission(permissionID string) bool {
	if permissionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.askPermissions[permissionID]
	return ok
}

// MarkAskBlockDecided stamps the chosen answers onto the ask block
// keyed by the given permission_id, and flips Done so the UI can
// switch to the decided view. Returns the turn id + block id of the
// updated block (empty when not found).
func (s *Session) MarkAskBlockDecided(permissionID string, answers json.RawMessage) (turnID, blockID string) {
	if permissionID == "" {
		return "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.turns {
		for i := range t.Blocks {
			if t.Blocks[i].PermissionID == permissionID && t.Blocks[i].Kind == "ask" {
				t.Blocks[i].Done = true
				t.Blocks[i].AskAnswers = answers
				return t.ID, t.Blocks[i].ID
			}
		}
	}
	return "", ""
}

// AttachAskPermission stamps the freshly-issued permission_id onto the
// most recent kind:"ask" block whose toolUseID matches. This is called
// when the permission_prompt MCP request arrives — the ask block was
// usually created moments earlier by content_block_start, but the
// permission_id wasn't known then. Returns the (turnID, blockID) of
// the stamped block, or empty strings when not found.
func (s *Session) AttachAskPermission(toolUseID, permissionID string) (turnID, blockID string) {
	if permissionID == "" {
		return "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Walk newest-first — the live block we want is at the tail.
	for i := len(s.turns) - 1; i >= 0; i-- {
		t := s.turns[i]
		for j := range t.Blocks {
			b := &t.Blocks[j]
			if b.Kind != "ask" {
				continue
			}
			if toolUseID != "" && b.ToolUseID != toolUseID {
				continue
			}
			if b.PermissionID == "" {
				b.PermissionID = permissionID
			}
			return t.ID, b.ID
		}
	}
	return "", ""
}

// RegisterPlanPermission ties a permission_id (issued by the generic
// PermissionRequester path) to the ExitPlanMode tool_use_id whose
// approval it gates. Mirrors RegisterAskPermission. The Agent calls
// this when a permission_prompt MCP request is detected to be for
// ExitPlanMode, so `plan.respond` handlers can look up the waiter by
// permission_id and so the corresponding tool_result is suppressed
// (the kind:"plan" block already conveys the outcome visually).
func (s *Session) RegisterPlanPermission(permissionID, toolUseID string) {
	if permissionID == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.planPermissions == nil {
		s.planPermissions = map[string]string{}
	}
	s.planPermissions[permissionID] = toolUseID
	if toolUseID != "" {
		if s.planToolUseIDs == nil {
			s.planToolUseIDs = map[string]struct{}{}
		}
		s.planToolUseIDs[toolUseID] = struct{}{}
	}
}

// ConsumePlanPermission reports whether the given permission_id was
// registered as an ExitPlanMode permission. If so it returns the
// associated tool_use_id and removes the entry. Mirrors
// ConsumeAskPermission.
func (s *Session) ConsumePlanPermission(permissionID string) (toolUseID string, ok bool) {
	if permissionID == "" {
		return "", false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	toolUseID, ok = s.planPermissions[permissionID]
	if !ok {
		return "", false
	}
	delete(s.planPermissions, permissionID)
	return toolUseID, true
}

// IsPlanPermission reports whether the given permission_id corresponds
// to an active ExitPlanMode request without consuming it.
func (s *Session) IsPlanPermission(permissionID string) bool {
	if permissionID == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.planPermissions[permissionID]
	return ok
}

// AttachPlanPermission stamps the freshly-issued permission_id onto the
// most recent kind:"plan" block whose toolUseID matches. Returns
// (turnID, blockID) of the stamped block, or empty strings when not
// found. Mirrors AttachAskPermission.
func (s *Session) AttachPlanPermission(toolUseID, permissionID string) (turnID, blockID string) {
	if permissionID == "" {
		return "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.turns) - 1; i >= 0; i-- {
		t := s.turns[i]
		for j := range t.Blocks {
			b := &t.Blocks[j]
			if b.Kind != "plan" {
				continue
			}
			if toolUseID != "" && b.ToolUseID != toolUseID {
				continue
			}
			if b.PermissionID == "" {
				b.PermissionID = permissionID
			}
			return t.ID, b.ID
		}
	}
	return "", ""
}

// MarkPlanBlockDecided stamps the user's decision onto the kind:"plan"
// block keyed by the given permission_id, and flips Done so the UI
// switches to the post-decision label. Mirrors MarkAskBlockDecided.
// `decision` is "approved" or "rejected"; `targetMode` is the new
// permission mode for an approval (may be "" when the user chose not to
// switch modes).
func (s *Session) MarkPlanBlockDecided(permissionID, decision, targetMode string) (turnID, blockID string) {
	if permissionID == "" {
		return "", ""
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.turns {
		for i := range t.Blocks {
			if t.Blocks[i].PermissionID == permissionID && t.Blocks[i].Kind == "plan" {
				t.Blocks[i].Done = true
				t.Blocks[i].PlanDecision = decision
				t.Blocks[i].PlanTargetMode = targetMode
				return t.ID, t.Blocks[i].ID
			}
		}
	}
	return "", ""
}

// UpdatePlanBlockText replaces the markdown body of the kind:"plan"
// block keyed by permission_id with the user's edited version. The
// block's Input is regenerated as `{"plan": editedText}` so re-snapshots
// (session.init on reconnect) ship the edited text instead of the
// agent's original draft. No-op when permissionID doesn't match.
func (s *Session) UpdatePlanBlockText(permissionID, editedPlan string) {
	if permissionID == "" || editedPlan == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, t := range s.turns {
		for i := range t.Blocks {
			if t.Blocks[i].PermissionID == permissionID && t.Blocks[i].Kind == "plan" {
				body := map[string]any{"plan": editedPlan}
				if raw, err := json.Marshal(body); err == nil {
					t.Blocks[i].Input = raw
					t.Blocks[i].Text = ""
				}
				return
			}
		}
	}
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

// SetTurns wholesale-replaces the turn list. Used when replaying a
// transcript on resume so the UI immediately shows the past
// conversation. Any open block / partial state is wiped because the
// replay produces only fully-completed turns.
func (s *Session) SetTurns(turns []Turn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*Turn, len(turns))
	for i := range turns {
		t := turns[i]
		out[i] = &t
	}
	s.turns = out
	s.currentTurn = nil
	s.openBlocks = map[int]*Block{}
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
	s.planToolUseIDs = map[string]struct{}{}
	s.askToolUseIDs = map[string]struct{}{}
	s.askPermissions = map[string]string{}
	s.planPermissions = map[string]string{}
	s.currentParentToolUseID = ""
	s.totalCostUSD = 0
	s.status = StatusIdle
	return old
}

// deepCopy returns a value copy of the turn safe for emission outside the
// session lock.
func (t *Turn) deepCopy() *Turn {
	out := &Turn{Role: t.Role, ID: t.ID, ParentToolUseID: t.ParentToolUseID, Blocks: make([]Block, len(t.Blocks))}
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
