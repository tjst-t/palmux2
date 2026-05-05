package server

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/tjst-t/palmux2/internal/netns"
)

// exposePortRequest is the JSON body for POST /ports/expose.
type exposePortRequest struct {
	InternalPort int `json:"internalPort"`
	HostPort     int `json:"hostPort,omitempty"` // 0 = auto-allocate
}

// exposePortResponse is the JSON response for POST /ports/expose.
type exposePortResponse struct {
	HostPort     int    `json:"hostPort"`
	InternalPort int    `json:"internalPort"`
	PublicURL    string `json:"publicUrl,omitempty"`
}

// portHandlers groups port-management HTTP handlers (S034).
type portHandlers struct {
	netns *netns.Manager
}

// exposePort handles POST /api/repos/{r}/branches/{b}/ports/expose.
func (ph *portHandlers) exposePort(w http.ResponseWriter, r *http.Request) {
	branchID := r.PathValue("branchId")

	var req exposePortRequest
	if err := decodeJSON(r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: err.Error()})
		return
	}
	if req.InternalPort <= 0 || req.InternalPort > 65535 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "internalPort must be 1–65535"})
		return
	}

	hostPort := req.HostPort
	if hostPort == 0 {
		var err error
		hostPort, err = ph.netns.AllocateHostPort()
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, errorResponse{Error: err.Error()})
			return
		}
	} else if ph.netns.IsHostPortInUse(hostPort) {
		writeJSON(w, http.StatusConflict, errorResponse{Error: fmt.Sprintf("host port %d is already in use", hostPort)})
		return
	}

	pm, err := ph.netns.AddPortMapping(r.Context(), branchID, req.InternalPort, hostPort)
	if err != nil {
		// Return 503 when the worktree is not found (isolation not active) or
		// 500 for other errors (slirp socket errors etc.).
		status := http.StatusInternalServerError
		if isNetnsNotFoundErr(err) {
			status = http.StatusServiceUnavailable
		}
		writeJSON(w, status, errorResponse{Error: err.Error()})
		return
	}

	writeJSON(w, http.StatusCreated, exposePortResponse{
		HostPort:     pm.HostPort,
		InternalPort: pm.InternalPort,
		PublicURL:    pm.PublicURL,
	})
}

// unexposePort handles DELETE /api/repos/{r}/branches/{b}/ports/{hostPort}.
func (ph *portHandlers) unexposePort(w http.ResponseWriter, r *http.Request) {
	branchID := r.PathValue("branchId")
	portStr := r.PathValue("hostPort")
	hostPort, err := strconv.Atoi(portStr)
	if err != nil || hostPort <= 0 {
		writeJSON(w, http.StatusBadRequest, errorResponse{Error: "invalid hostPort"})
		return
	}

	if err := ph.netns.RemovePortMapping(r.Context(), branchID, hostPort); err != nil {
		writeJSON(w, http.StatusInternalServerError, errorResponse{Error: err.Error()})
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listPorts handles GET /api/repos/{r}/branches/{b}/ports.
func (ph *portHandlers) listPorts(w http.ResponseWriter, r *http.Request) {
	branchID := r.PathValue("branchId")
	ports, err := ph.netns.GetPorts(branchID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, errorResponse{Error: err.Error()})
		return
	}
	if ports == nil {
		ports = []netns.PortMapping{}
	}
	writeJSON(w, http.StatusOK, ports)
}

// isNetnsNotFoundErr returns true when the error indicates the worktree
// is not tracked in the netns state (isolation not active for this worktree).
func isNetnsNotFoundErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return len(msg) > 0 && (contains(msg, "not found") || contains(msg, "no slirp socket"))
}

func contains(s, sub string) bool {
	if len(sub) == 0 {
		return true
	}
	if len(s) < len(sub) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// listListeners handles GET /api/repos/{r}/branches/{b}/listeners.
func (ph *portHandlers) listListeners(w http.ResponseWriter, r *http.Request) {
	branchID := r.PathValue("branchId")
	listeners, ok := ph.netns.GetListeners(branchID)
	if !ok {
		// Isolation is off or worktree not found — return empty array.
		writeJSON(w, http.StatusOK, []netns.Listener{})
		return
	}
	if listeners == nil {
		listeners = []netns.Listener{}
	}
	writeJSON(w, http.StatusOK, listeners)
}
