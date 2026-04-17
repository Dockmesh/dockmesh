package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/dockmesh/dockmesh/internal/registries"
	"github.com/go-chi/chi/v5"
)

// ListRegistries returns the saved registry credentials without exposing
// passwords. has_password tells the UI whether a password is stored, so
// the edit dialog can show "saved — leave blank to keep" instead of an
// always-empty field.
func (h *Handlers) ListRegistries(w http.ResponseWriter, r *http.Request) {
	if h.Registries == nil {
		writeJSON(w, http.StatusOK, []registries.Registry{})
		return
	}
	list, err := h.Registries.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *Handlers) CreateRegistry(w http.ResponseWriter, r *http.Request) {
	if h.Registries == nil {
		writeError(w, http.StatusServiceUnavailable, "registries service not configured")
		return
	}
	var in registries.Input
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	reg, err := h.Registries.Create(r.Context(), in)
	if errors.Is(err, registries.ErrDuplicate) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "registry.create", reg.URL, map[string]string{"name": reg.Name})
	writeJSON(w, http.StatusCreated, reg)
}

func (h *Handlers) UpdateRegistry(w http.ResponseWriter, r *http.Request) {
	if h.Registries == nil {
		writeError(w, http.StatusServiceUnavailable, "registries service not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in registries.Input
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	reg, err := h.Registries.Update(r.Context(), id, in)
	if errors.Is(err, registries.ErrDuplicate) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, "registry.update", reg.URL, map[string]string{"name": reg.Name})
	writeJSON(w, http.StatusOK, reg)
}

func (h *Handlers) DeleteRegistry(w http.ResponseWriter, r *http.Request) {
	if h.Registries == nil {
		writeError(w, http.StatusServiceUnavailable, "registries service not configured")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.Registries.Delete(r.Context(), id); err != nil {
		if errors.Is(err, registries.ErrNotFound) {
			writeError(w, http.StatusNotFound, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "registry.delete", strconv.FormatInt(id, 10), nil)
	w.WriteHeader(http.StatusNoContent)
}

// TestRegistry verifies credentials by asking the local docker daemon to
// authenticate against the registry. We persist the result so the UI
// can show "verified X minutes ago" without forcing an online call on
// every page load.
func (h *Handlers) TestRegistry(w http.ResponseWriter, r *http.Request) {
	if h.Registries == nil {
		writeError(w, http.StatusServiceUnavailable, "registries service not configured")
		return
	}
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable — cannot test")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	auth, err := h.Registries.PlaintextAuth(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	res, loginErr := h.Docker.Raw().RegistryLogin(ctx, *auth)
	ok := loginErr == nil
	var errMsg string
	if !ok {
		errMsg = loginErr.Error()
	}
	_ = h.Registries.RecordTest(r.Context(), id, ok, errMsg)
	h.audit(r, "registry.test", strconv.FormatInt(id, 10), map[string]any{"ok": ok})
	if !ok {
		writeJSON(w, http.StatusBadGateway, map[string]any{
			"ok":    false,
			"error": errMsg,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"status":   res.Status,
		"identity": res.IdentityToken != "",
	})
}
