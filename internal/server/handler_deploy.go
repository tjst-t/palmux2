package server

import (
	"net/http"

	"golang.org/x/crypto/bcrypt"

	"github.com/tjst-t/palmux2/internal/config"
	"github.com/tjst-t/palmux2/internal/deploy"
)

// Sa53137-2/3: deploy plane HTTP surface.
//
//	GET   /api/deploy           — masked view of the applied master + secret presence
//	POST  /api/deploy/apply     — persist+classify a master edit (hot in-process, restart/root signalled)
//	POST  /api/deploy/secrets   — write-only secret rotation (SSO secret / login password / token)
//
// Secrets are never returned. The deploy view reports presence booleans only.

func (h *handlers) getDeploy(w http.ResponseWriter, _ *http.Request) {
	if h.deploy == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "deploy plane unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, h.deploy.CurrentView())
}

// deployApplyRequest is the body for POST /api/deploy/apply. The GUI sends the
// full server/public sections; the CLI sends {dryRun:true|false} only and the
// server diffs the on-disk master.
type deployApplyRequest struct {
	Server *config.ServerSection `json:"server"`
	Public *config.PublicSection `json:"public"`
	DryRun bool                  `json:"dryRun"`
}

func (h *handlers) postDeployApply(w http.ResponseWriter, r *http.Request) {
	if h.deploy == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "deploy plane unavailable"})
		return
	}
	var req deployApplyRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}

	// Build the target master. If the request omits sections (CLI apply), read
	// the on-disk master so apply reflects a file edit.
	target := h.deploy.CurrentView()
	neu := config.MasterConfig{Server: target.Server, Public: target.Public}
	if req.Server == nil && req.Public == nil {
		mc, _, err := config.LoadServerConfig(h.deployConfigDir)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
			return
		}
		neu = mc
	} else {
		if req.Server != nil {
			neu.Server = *req.Server
		}
		if req.Public != nil {
			neu.Public = *req.Public
		}
	}

	// Validate a public domain before persisting so the GUI gets a 400 with a
	// precise message rather than a later reconcile failure.
	if neu.Public.Domain != "" {
		if err := config.ValidateDomain(neu.Public.Domain); err != nil {
			writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid public.domain: " + err.Error()})
			return
		}
	}

	out, err := h.deploy.SaveAndClassify(r.Context(), neu, req.DryRun)
	if err != nil {
		writeErr(w, err)
		return
	}
	// Shape the response to match the apply CLI's applyResult.
	type changeDTO struct {
		Field string `json:"field"`
		Class string `json:"class"`
	}
	resp := struct {
		Changes       []changeDTO `json:"changes"`
		HotApplied    bool        `json:"hotApplied"`
		NeedRestart   bool        `json:"needRestart"`
		NeedPrivilege bool        `json:"needPrivilege"`
		Message       string      `json:"message"`
	}{
		HotApplied:    out.HotApplied,
		NeedRestart:   out.NeedRestart,
		NeedPrivilege: out.NeedPrivilege,
		Message:       out.Message,
	}
	for _, c := range out.Changes {
		resp.Changes = append(resp.Changes, changeDTO{Field: c.Field, Class: string(c.Class)})
	}
	writeJSON(w, http.StatusOK, resp)
}

// deploySecretsRequest is the write-only secret rotation body. All fields are
// optional; empty fields leave the secret unchanged.
type deploySecretsRequest struct {
	SSOSecret       string `json:"ssoSecret"`
	Password        string `json:"password"` // plaintext; bcrypt-hashed server-side
	Token           string `json:"token"`
	CloudflareToken string `json:"cloudflareToken"` // CLOUDFLARE_API_TOKEN (Caddy DNS-01)
}

func (h *handlers) postDeploySecrets(w http.ResponseWriter, r *http.Request) {
	if h.deploy == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "deploy plane unavailable"})
		return
	}
	var req deploySecretsRequest
	if err := decodeJSON(r, &req); err != nil {
		writeErr(w, err)
		return
	}
	hashFn := func(pw string) (string, error) {
		b, err := bcrypt.GenerateFromPassword([]byte(pw), 8)
		return string(b), err
	}
	changedRestart, err := h.deploy.RotateSecrets(config.Secrets{
		SSOSecret:       req.SSOSecret,
		Token:           req.Token,
		CloudflareToken: req.CloudflareToken,
	}, hashFn, req.Password)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":          true,
		"needRestart": changedRestart,
		"message":     "secrets updated (write-only). Restart palmux to take effect.",
	})
}

// postDeployRebuild kicks `nixos-rebuild switch` on a NixOS appliance to apply
// privileged (public domain / TLS) changes the GUI can't hot-apply. It is the
// NixOS counterpart of `palmux reconcile-system`: palmux2 (non-root palmux user)
// starts the root palmux-rebuild.service over the system bus (authorized by a
// polkit rule), which runs the switch in its own cgroup so restarting palmux2
// mid-switch doesn't kill it. Returns 202 immediately; the GUI polls GET .../rebuild.
func (h *handlers) postDeployRebuild(w http.ResponseWriter, r *http.Request) {
	if h.deploy == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "deploy plane unavailable"})
		return
	}
	if !h.deploy.CurrentView().NixOSHost {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "nixos-rebuild trigger is for the NixOS appliance only; on this host apply via `sudo palmux reconcile-system`"})
		return
	}
	if err := deploy.TriggerRebuild(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{
		"ok":      true,
		"status":  "triggered",
		"message": "nixos-rebuild switch started; palmux2 will restart when the new config activates",
	})
}

// getDeployRebuild reports palmux-rebuild.service state so the GUI can show
// progress (activating → active / failed). NixOS-appliance only.
func (h *handlers) getDeployRebuild(w http.ResponseWriter, r *http.Request) {
	if h.deploy == nil {
		writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: "deploy plane unavailable"})
		return
	}
	if !h.deploy.CurrentView().NixOSHost {
		writeJSON(w, http.StatusConflict, errorResponse{Error: "nixos-rebuild trigger is for the NixOS appliance only"})
		return
	}
	st, err := deploy.QueryRebuild(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, st)
}

// ensure deploy is referenced for the import even when nil in tests.
var _ = deploy.View{}
