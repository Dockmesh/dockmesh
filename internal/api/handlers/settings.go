package handlers

import (
	"net/http"
)

// ListSettings returns all DB-backed settings for the System tab.
//
//	GET /api/v1/settings
func (h *Handlers) ListSettings(w http.ResponseWriter, r *http.Request) {
	if h.Settings == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	writeJSON(w, http.StatusOK, h.Settings.All())
}

// UpdateSettings writes one or more settings.
//
//	PUT /api/v1/settings
//	Body: [{"key":"proxy_enabled","value":"true"}, ...]
func (h *Handlers) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	if h.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings store unavailable")
		return
	}
	var entries []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	}
	if err := decodeJSON(r, &entries); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	for _, e := range entries {
		if err := h.Settings.Set(r.Context(), e.Key, e.Value); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, h.Settings.All())
}
