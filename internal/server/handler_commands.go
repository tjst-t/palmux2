package server

import (
	"net/http"

	"github.com/tjst-t/palmux2/internal/commands"
)

func (h *handlers) listCommands(w http.ResponseWriter, r *http.Request) {
	branch, err := h.store.Branch(r.PathValue("repoId"), r.PathValue("branchId"))
	if err != nil {
		writeErr(w, err)
		return
	}
	if h.commands == nil {
		writeJSON(w, http.StatusOK, []commands.Command{})
		return
	}
	cmds, err := h.commands.Detect(r.Context(), branch.WorktreePath)
	if err != nil {
		writeErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, cmds)
}
