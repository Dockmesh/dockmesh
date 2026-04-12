package handlers

import (
	"database/sql"
	"errors"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
)

// errNotFound aliases sql.ErrNoRows so handler packages can check without
// importing database/sql just for that.
var errNotFound = sql.ErrNoRows

// audit is a convenience wrapper that enriches the call with the
// authenticated user ID from the request context.
func (h *Handlers) audit(r *http.Request, action, target string, details any) {
	if h.Audit == nil {
		return
	}
	uid := middleware.UserID(r.Context())
	h.Audit.Write(r.Context(), uid, action, target, details)
}

func (h *Handlers) ListAudit(w http.ResponseWriter, r *http.Request) {
	if h.Audit == nil {
		writeError(w, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			limit = n
		}
	}
	entries, err := h.Audit.List(r.Context(), limit)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// ensureErrorsImport silences the unused-import warning in case errors is
// unused after future edits. Kept for the user-lookup guard above.
var _ = errors.New
