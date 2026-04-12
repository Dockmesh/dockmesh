package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/convert"
)

type convertRequest struct {
	Command string `json:"command"`
}

// ConvertRunToCompose accepts a `docker run …` command line and returns a
// compose YAML fragment. Concept §1.2.
func (h *Handlers) ConvertRunToCompose(w http.ResponseWriter, r *http.Request) {
	var req convertRequest
	if err := decodeJSON(r, &req); err != nil || req.Command == "" {
		writeError(w, http.StatusBadRequest, "command required")
		return
	}
	res, err := convert.Run(req.Command)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, res)
}
