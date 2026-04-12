package handlers

import (
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/auth"
	"github.com/go-chi/chi/v5"
)

type mfaVerifyRequest struct {
	MFAToken string `json:"mfa_token"`
	Code     string `json:"code"`
}

type mfaCodeRequest struct {
	Code string `json:"code"`
}

// LoginMFA completes a two-step login by verifying a TOTP/recovery code.
func (h *Handlers) LoginMFA(w http.ResponseWriter, r *http.Request) {
	var req mfaVerifyRequest
	if err := decodeJSON(r, &req); err != nil || req.MFAToken == "" || req.Code == "" {
		writeError(w, http.StatusBadRequest, "mfa_token and code required")
		return
	}
	res, err := h.Auth.VerifyLoginMFA(r.Context(), req.MFAToken, req.Code, r.UserAgent(), clientIP(r))
	if errors.Is(err, auth.ErrInvalidCredentials) {
		if h.Audit != nil {
			h.Audit.Write(r.Context(), "", audit.ActionLoginFailed, "mfa", map[string]string{"ip": clientIP(r)})
		}
		writeError(w, http.StatusUnauthorized, "invalid code")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "mfa failed")
		return
	}
	if h.Audit != nil && res.User != nil {
		h.Audit.Write(r.Context(), res.User.ID, audit.ActionLogin, res.User.Username, map[string]string{"mfa": "true"})
	}
	writeJSON(w, http.StatusOK, res)
}

// MFAEnrollStart begins enrollment for the current user and returns the
// QR code + secret. The secret is not yet active until MFAEnrollVerify.
func (h *Handlers) MFAEnrollStart(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserID(r.Context())
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	enroll, err := h.Auth.StartTOTPEnrollment(r.Context(), uid)
	if errors.Is(err, auth.ErrMFAAlreadyEnrolled) {
		writeError(w, http.StatusConflict, "mfa already enrolled")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, enroll)
}

// MFAEnrollVerify finalizes enrollment by validating the first code and
// returns the one-time recovery codes (only once — client must save them).
func (h *Handlers) MFAEnrollVerify(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserID(r.Context())
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var req mfaCodeRequest
	if err := decodeJSON(r, &req); err != nil || req.Code == "" {
		writeError(w, http.StatusBadRequest, "code required")
		return
	}
	codes, err := h.Auth.VerifyTOTPEnrollment(r.Context(), uid, req.Code)
	switch {
	case errors.Is(err, auth.ErrMFANotEnrolled):
		writeError(w, http.StatusBadRequest, "enroll first")
		return
	case errors.Is(err, auth.ErrMFAAlreadyEnrolled):
		writeError(w, http.StatusConflict, "already verified")
		return
	case errors.Is(err, auth.ErrMFAInvalidCode):
		writeError(w, http.StatusUnauthorized, "invalid code")
		return
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "user.mfa_enroll", uid, nil)
	writeJSON(w, http.StatusOK, map[string]any{"recovery_codes": codes})
}

// MFADisable clears MFA for the current user.
func (h *Handlers) MFADisable(w http.ResponseWriter, r *http.Request) {
	uid := middleware.UserID(r.Context())
	if uid == "" {
		writeError(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	if err := h.Auth.DisableTOTP(r.Context(), uid); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "user.mfa_disable", uid, nil)
	w.WriteHeader(http.StatusNoContent)
}

// MFAReset (admin only) clears MFA for any user.
func (h *Handlers) MFAReset(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.Auth.DisableTOTP(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "user.mfa_reset", id, nil)
	w.WriteHeader(http.StatusNoContent)
}
