package handlers

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
)

// errNotFound aliases sql.ErrNoRows so handler packages can check without
// importing database/sql just for that.
var errNotFound = sql.ErrNoRows

// audit writes an entry attributed to the caller: the signed-in user, or
// the owner of the API token in use with the token id in the details.
func (h *Handlers) audit(r *http.Request, action, target string, details any) {
	if h.Audit == nil {
		return
	}
	h.Audit.Write(r.Context(), actorID(r), action, target, withTokenID(r.Context(), details))
}

// auditDetached writes an audit entry that must survive the request being
// cancelled, e.g. at the end of a WebSocket session.
func (h *Handlers) auditDetached(r *http.Request, action, target string, details any) {
	if h.Audit == nil {
		return
	}
	ctx := context.WithoutCancel(r.Context())
	h.Audit.Write(ctx, actorID(r), action, target, withTokenID(ctx, details))
}

// withTokenID adds the API token of the request to details, so entries of
// a token and of its owner's own sessions can be told apart. Details that
// are not a JSON object are kept under "details".
func withTokenID(ctx context.Context, details any) any {
	tid := middleware.APITokenID(ctx)
	if tid == 0 {
		return details
	}
	out := map[string]any{}
	if details != nil {
		b, err := json.Marshal(details)
		if err != nil || json.Unmarshal(b, &out) != nil {
			out = map[string]any{"details": details}
		}
	}
	if out == nil {
		out = map[string]any{}
	}
	out["api_token_id"] = tid
	return out
}

func (h *Handlers) ListAudit(w http.ResponseWriter, r *http.Request) {
	if h.Audit == nil {
		writeError(w, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	f := audit.ListFilter{Limit: 100}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			f.Limit = n
		}
	}
	f.Action = r.URL.Query().Get("action")
	f.UserID = r.URL.Query().Get("user_id")
	f.Category = r.URL.Query().Get("category")
	if f.Category != "" && !audit.ValidCategory(f.Category) {
		writeError(w, http.StatusBadRequest, "category must be one of: "+strings.Join(audit.Categories, ", "))
		return
	}
	entries, err := h.Audit.ListFiltered(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// VerifyAudit walks the hash chain and reports any tampering (§15.10).
func (h *Handlers) VerifyAudit(w http.ResponseWriter, r *http.Request) {
	if h.Audit == nil {
		writeError(w, http.StatusServiceUnavailable, "audit unavailable")
		return
	}
	report, err := h.Audit.Verify(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, report)
}

// ensureErrorsImport silences the unused-import warning in case errors is
// unused after future edits. Kept for the user-lookup guard above.
var _ = errors.New
