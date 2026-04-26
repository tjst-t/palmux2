// Package claude provides the Claude Code tab — a singleton, protected,
// terminal-backed tab that auto-starts the `claude` CLI in the branch's
// worktree.
package claude

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tjst-t/palmux2/internal/domain"
	"github.com/tjst-t/palmux2/internal/tab"
	"github.com/tjst-t/palmux2/internal/tmux"
)

// Backend is the slice of *store.Store the Restart/Resume handlers need.
// Defined as a local interface to avoid an import cycle (store_test imports
// claude, and claude would otherwise import store).
type Backend interface {
	Branch(repoID, branchID string) (*domain.Branch, error)
	Tmux() tmux.Client
}

// TabType is the stable provider identifier.
const TabType = "claude"

// WindowName is the canonical tmux window name. Per spec, the Claude tab is
// always at this exact window name, never indexed.
const WindowName = "palmux:claude:claude"

// Options configures the claude command.
type Options struct {
	Model string // optional --model
}

// Provider implements tab.Provider for Claude.
type Provider struct {
	opts    Options
	backend Backend
}

// New returns a Provider with the given options. The backend reference is
// optional — call SetBackend once the Store is available so the Restart /
// Resume endpoints can resolve the tmux session for the requested branch.
func New(opts Options) *Provider { return &Provider{opts: opts} }

// SetBackend wires the live Store into the provider. main.go calls it
// after store.New so Restart/Resume work without a chicken-and-egg
// between provider registration and store hydration.
func (p *Provider) SetBackend(b Backend) { p.backend = b }

func (p *Provider) Type() string         { return TabType }
func (p *Provider) DisplayName() string  { return "Claude" }
func (p *Provider) Protected() bool      { return true }
func (p *Provider) Multiple() bool       { return false }
func (p *Provider) NeedsTmuxWindow() bool { return true }

func (p *Provider) OnBranchOpen(_ context.Context, params tab.OpenParams) (tab.ProviderResult, error) {
	cmd := buildCommand(p.opts, params.Resume)
	return tab.ProviderResult{
		Tabs: []domain.Tab{{
			ID:         TabType,
			Type:       TabType,
			Name:       p.DisplayName(),
			Protected:  true,
			Multiple:   false,
			WindowName: WindowName,
		}},
		Windows: []tab.WindowSpec{{
			Name:    WindowName,
			Command: cmd,
		}},
	}, nil
}

func (p *Provider) OnBranchClose(_ context.Context, _ tab.CloseParams) error {
	return nil
}

// RegisterRoutes adds /restart and /resume to the per-tab API.
//   POST <prefix>/branches/{branchId}/restart  body: {"model"?: string}
//   POST <prefix>/branches/{branchId}/resume
//
// Both kill the current process inside the Claude tab's tmux window and
// respawn `claude` (with --model / --resume as requested), so the WS
// already attached to that window simply re-renders the new program.
func (p *Provider) RegisterRoutes(mux *http.ServeMux, prefix string) {
	if p.backend == nil {
		return
	}
	// `prefix` is `/api/repos/{repoId}/branches/{branchId}/{type}` — we
	// hang per-tab actions off `<prefix>/restart` etc.
	mux.HandleFunc("POST "+prefix+"/restart", p.handleRestart)
	mux.HandleFunc("POST "+prefix+"/resume", p.handleResume)
}

type restartRequest struct {
	Model  string `json:"model,omitempty"`
	Resume bool   `json:"resume,omitempty"`
}

type restartResponse struct {
	Command string `json:"command"`
}

func (p *Provider) handleRestart(w http.ResponseWriter, r *http.Request) {
	var req restartRequest
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req)
	}
	cmd, err := p.respawn(r.Context(), r.PathValue("repoId"), r.PathValue("branchId"), req.Model, req.Resume)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSONOK(w, restartResponse{Command: cmd})
}

func (p *Provider) handleResume(w http.ResponseWriter, r *http.Request) {
	cmd, err := p.respawn(r.Context(), r.PathValue("repoId"), r.PathValue("branchId"), p.opts.Model, true)
	if err != nil {
		writeJSONError(w, err)
		return
	}
	writeJSONOK(w, restartResponse{Command: cmd})
}

func (p *Provider) respawn(ctx context.Context, repoID, branchID, model string, resume bool) (string, error) {
	branch, err := p.backend.Branch(repoID, branchID)
	if err != nil {
		return "", err
	}
	cmd := buildCommand(Options{Model: model}, resume)
	if err := p.backend.Tmux().RespawnWindow(ctx, branch.TabSet.TmuxSession, WindowName, cmd); err != nil {
		return "", err
	}
	return cmd, nil
}

func writeJSONOK(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// buildCommand assembles the `claude` invocation for tmux. Phase 1 supports
// Resume + Model; Phase 10 will surface Restart with model selection from the
// UI.
func buildCommand(opts Options, resume bool) string {
	parts := []string{"claude"}
	if resume {
		parts = append(parts, "--resume")
	}
	if opts.Model != "" {
		parts = append(parts, "--model", opts.Model)
	}
	return strings.Join(parts, " ")
}
