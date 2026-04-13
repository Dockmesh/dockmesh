package handlers

import (
	"net/http"
)

// ListHosts returns the local docker daemon plus every registered agent
// (online or offline) for the frontend host switcher.
func (h *Handlers) ListHosts(w http.ResponseWriter, r *http.Request) {
	if h.Hosts == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	list, err := h.Hosts.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}
