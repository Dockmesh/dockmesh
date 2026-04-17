package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/go-chi/chi/v5"
)

// GetPasswordPolicy returns the current password complexity + lockout
// + rotation settings. Admin-only because the numbers themselves
// indicate how secure (or not) the deployment is.
func (h *Handlers) GetPasswordPolicy(w http.ResponseWriter, r *http.Request) {
	if h.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings store not configured")
		return
	}
	writeJSON(w, http.StatusOK, auth.LoadPolicy(h.Settings))
}

// UpdatePasswordPolicy persists a new policy. Validates min-length
// and non-negative counters in the service layer.
func (h *Handlers) UpdatePasswordPolicy(w http.ResponseWriter, r *http.Request) {
	if h.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings store not configured")
		return
	}
	var p auth.PolicyConfig
	if err := decodeJSON(r, &p); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := auth.SavePolicy(r.Context(), h.Settings, p); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "auth.policy_update", "", map[string]any{
		"min_length":      p.MinLength,
		"lockout_attempts": p.LockoutMaxAttempts,
	})
	writeJSON(w, http.StatusOK, auth.LoadPolicy(h.Settings))
}

// UnlockUser clears the lockout state on a user. Used after an
// operator has verified via another channel that the real user was
// locked out (not the attacker).
func (h *Handlers) UnlockUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Auth.Unlock(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "user.unlock", id, nil)
	w.WriteHeader(http.StatusNoContent)
}
