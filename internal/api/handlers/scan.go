package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/go-chi/chi/v5"
)

// ScanImage runs the scanner against the image and stores the result.
func (h *Handlers) ScanImage(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	if h.Scanner == nil || h.ScanStore == nil {
		writeError(w, http.StatusServiceUnavailable, "scanner not configured")
		return
	}
	if err := h.Scanner.Ready(); err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}

	id := chi.URLParam(r, "id")
	// Resolve image id → repo:tag the scanner understands. Fall back to id.
	ref := id
	info, err := h.Docker.InspectImage(r.Context(), id)
	if err == nil && len(info.RepoTags) > 0 {
		ref = info.RepoTags[0]
	}

	rep, err := h.Scanner.Scan(r.Context(), ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := h.ScanStore.Save(r.Context(), rep); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionImageScan, ref, map[string]int{
		"critical": rep.Summary.Critical,
		"high":     rep.Summary.High,
		"total":    rep.Summary.Total(),
	})
	writeJSON(w, http.StatusOK, rep)
}

// GetScan returns the cached scan result for an image, or 404.
func (h *Handlers) GetScan(w http.ResponseWriter, r *http.Request) {
	if h.ScanStore == nil {
		writeError(w, http.StatusServiceUnavailable, "scanner not configured")
		return
	}
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	ref := id
	info, err := h.Docker.InspectImage(r.Context(), id)
	if err == nil && len(info.RepoTags) > 0 {
		ref = info.RepoTags[0]
	}
	rep, err := h.ScanStore.Get(r.Context(), ref)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if rep == nil {
		writeError(w, http.StatusNotFound, "no scan cached")
		return
	}
	writeJSON(w, http.StatusOK, rep)
}
