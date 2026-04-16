package handlers

import (
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/globalenv"
	"github.com/go-chi/chi/v5"
)

func (h *Handlers) ListGlobalEnv(w http.ResponseWriter, r *http.Request) {
	if h.GlobalEnv == nil {
		writeJSON(w, http.StatusOK, []any{})
		return
	}
	vars, err := h.GlobalEnv.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if vars == nil {
		vars = []globalenv.Var{}
	}
	writeJSON(w, http.StatusOK, vars)
}

func (h *Handlers) CreateGlobalEnv(w http.ResponseWriter, r *http.Request) {
	if h.GlobalEnv == nil {
		writeError(w, http.StatusServiceUnavailable, "global env unavailable")
		return
	}
	var in globalenv.VarInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	v, err := h.GlobalEnv.Create(r.Context(), in)
	if err != nil {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, v)
}

func (h *Handlers) UpdateGlobalEnv(w http.ResponseWriter, r *http.Request) {
	if h.GlobalEnv == nil {
		writeError(w, http.StatusServiceUnavailable, "global env unavailable")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	var in globalenv.VarInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	v, err := h.GlobalEnv.Update(r.Context(), id, in)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, v)
}

func (h *Handlers) DeleteGlobalEnv(w http.ResponseWriter, r *http.Request) {
	if h.GlobalEnv == nil {
		writeError(w, http.StatusServiceUnavailable, "global env unavailable")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := h.GlobalEnv.Delete(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) ListGlobalEnvGroups(w http.ResponseWriter, r *http.Request) {
	if h.GlobalEnv == nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	groups, err := h.GlobalEnv.Groups(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, groups)
}
