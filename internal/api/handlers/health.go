package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/pkg/version"
)

func (h *Handlers) Health(w http.ResponseWriter, r *http.Request) {
	dockerOK := h.Docker != nil
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "ok",
		"version": version.Version,
		"docker":  dockerOK,
	})
}
