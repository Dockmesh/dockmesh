package handlers

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/go-chi/chi/v5"
)

// Session is what the current user sees for each of their active
// login sessions. No secrets — just enough for them to recognise
// "is this my phone? is this a browser I forgot to log out of?".
type Session struct {
	FamilyID  string     `json:"family_id"`
	UserAgent string     `json:"user_agent,omitempty"`
	IP        string     `json:"ip,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	ExpiresAt time.Time  `json:"expires_at"`
	RevokedAt *time.Time `json:"revoked_at,omitempty"`
	IsCurrent bool       `json:"is_current"`
}

// ListMySessions returns the caller's sessions. By default only active
// ones are included — the UI rarely wants to see long-dead sessions,
// and including them in an unbounded list made the panel unreadable
// for any user who's been around for more than a few days.
//
// Pass `?include_revoked=1` to get the full history, e.g. for a
// "what happened to me lately" audit view. Regardless of the flag,
// results are capped at 200 rows newest-first.
func (h *Handlers) ListMySessions(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserID(r.Context())
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	includeRevoked := r.URL.Query().Get("include_revoked") == "1"
	query := `
		SELECT family_id, user_agent, ip, created_at, expires_at, revoked_at
		  FROM sessions
		 WHERE user_id = ?
		   AND revoked_at IS NULL
		   AND expires_at > CURRENT_TIMESTAMP
		 ORDER BY created_at DESC
		 LIMIT 200`
	if includeRevoked {
		query = `
		SELECT family_id, user_agent, ip, created_at, expires_at, revoked_at
		  FROM sessions
		 WHERE user_id = ?
		 ORDER BY created_at DESC
		 LIMIT 200`
	}
	rows, err := h.DB.QueryContext(r.Context(), query, uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var s Session
		var ua, ip sql.NullString
		var revoked sql.NullTime
		if err := rows.Scan(&s.FamilyID, &ua, &ip, &s.CreatedAt, &s.ExpiresAt, &revoked); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if ua.Valid {
			s.UserAgent = ua.String
		}
		if ip.Valid {
			s.IP = ip.String
		}
		if revoked.Valid {
			t := revoked.Time
			s.RevokedAt = &t
		}
		out = append(out, s)
	}
	writeJSON(w, http.StatusOK, out)
}

// RevokeMySession marks one of the caller's sessions as revoked. The
// session can be the current one — handy for "log out everywhere".
// Returns 204 even if the session was already revoked so the UI can
// call this idempotently.
func (h *Handlers) RevokeMySession(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserID(r.Context())
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	familyID := chi.URLParam(r, "family_id")
	res, err := h.DB.ExecContext(r.Context(), `
		UPDATE sessions
		   SET revoked_at = CURRENT_TIMESTAMP
		 WHERE family_id = ? AND user_id = ? AND revoked_at IS NULL`,
		familyID, uid)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// If the WHERE matched nothing, treat as not-your-session rather
	// than already-revoked — we don't leak which case it was.
	if n, _ := res.RowsAffected(); n == 0 {
		writeError(w, http.StatusNotFound, "session not found")
		return
	}
	h.audit(r, "auth.session_revoke", familyID, nil)
	w.WriteHeader(http.StatusNoContent)
}
