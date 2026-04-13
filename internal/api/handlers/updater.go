package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/updater"
	"github.com/go-chi/chi/v5"
)

type rollbackRequest struct {
	HistoryID int64 `json:"history_id"`
}

func (h *Handlers) UpdateContainer(w http.ResponseWriter, r *http.Request) {
	if h.Updater == nil {
		writeError(w, http.StatusServiceUnavailable, "updater not configured")
		return
	}
	id := chi.URLParam(r, "id")
	res, err := h.Updater.Update(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res.Updated {
		h.audit(r, audit.ActionContainerUpdate, res.ContainerName, map[string]string{
			"image":     res.Image,
			"from":      trim(res.OldDigest),
			"to":        trim(res.NewDigest),
			"rollback":  res.RollbackTag,
		})
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) RollbackContainer(w http.ResponseWriter, r *http.Request) {
	if h.Updater == nil {
		writeError(w, http.StatusServiceUnavailable, "updater not configured")
		return
	}
	var req rollbackRequest
	if err := decodeJSON(r, &req); err != nil || req.HistoryID == 0 {
		writeError(w, http.StatusBadRequest, "history_id required")
		return
	}
	res, err := h.Updater.Rollback(r.Context(), req.HistoryID)
	if errors.Is(err, updater.ErrHistoryNotFound) {
		writeError(w, http.StatusNotFound, "history entry not found")
		return
	}
	if errors.Is(err, updater.ErrAlreadyRolledBack) {
		writeError(w, http.StatusConflict, "already rolled back")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionContainerRollback, res.ContainerName, map[string]int64{"history_id": req.HistoryID})
	writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) PreviewUpdate(w http.ResponseWriter, r *http.Request) {
	if h.Updater == nil {
		writeError(w, http.StatusServiceUnavailable, "updater not configured")
		return
	}
	id := chi.URLParam(r, "id")
	preview, err := h.Updater.Preview(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, preview)
}

func (h *Handlers) UpdateHistory(w http.ResponseWriter, r *http.Request) {
	if h.Updater == nil {
		writeError(w, http.StatusServiceUnavailable, "updater not configured")
		return
	}
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	// Resolve id → name.
	info, err := h.Docker.InspectContainer(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}
	name := strings.TrimPrefix(info.Name, "/")
	if v := r.URL.Query().Get("name"); v != "" {
		name = v
	}
	// Also accept ?id with numeric history id ... no, skip.
	_ = strconv.Itoa // ensure strconv is referenced even if unused

	entries, err := h.Updater.History(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// trim shortens a sha256:... digest so audit details stay compact.
func trim(s string) string {
	if strings.HasPrefix(s, "sha256:") && len(s) > 19 {
		return s[:19]
	}
	return s
}
