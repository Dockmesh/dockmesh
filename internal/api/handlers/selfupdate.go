package handlers

import (
	"net/http"
)

// GetUpdateStatus returns the cached self-update check result for the
// topbar banner + Settings → System tab.
//
//	GET /api/v1/system/update-status
func (h *Handlers) GetUpdateStatus(w http.ResponseWriter, r *http.Request) {
	if h.SelfUpdate == nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"enabled":          false,
			"update_available": false,
		})
		return
	}
	writeJSON(w, http.StatusOK, h.SelfUpdate.Status())
}

// CheckUpdateNow forces an immediate GitHub Releases lookup. Wired to
// the "Check now" button in Settings. Returns the refreshed status
// synchronously so the UI can render the new result without a second
// round-trip.
//
//	POST /api/v1/system/update-check
func (h *Handlers) CheckUpdateNow(w http.ResponseWriter, r *http.Request) {
	if h.SelfUpdate == nil {
		writeError(w, http.StatusServiceUnavailable, "update checker unavailable")
		return
	}
	if err := h.SelfUpdate.CheckNow(r.Context()); err != nil {
		// The checker records the error in its status; return 200 with
		// the body so the UI can show the message inline rather than a
		// toast from a 5xx.
		writeJSON(w, http.StatusOK, h.SelfUpdate.Status())
		return
	}
	h.audit(r, "system.update_check", "", nil)
	writeJSON(w, http.StatusOK, h.SelfUpdate.Status())
}
