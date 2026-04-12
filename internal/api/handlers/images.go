package handlers

import (
	"io"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/go-chi/chi/v5"
)

type pullRequest struct {
	Image string `json:"image"`
}

func (h *Handlers) ListImages(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	all := r.URL.Query().Get("all") == "true"
	images, err := h.Docker.ListImages(r.Context(), all)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, images)
}

func (h *Handlers) PullImage(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	var req pullRequest
	if err := decodeJSON(r, &req); err != nil || req.Image == "" {
		writeError(w, http.StatusBadRequest, "image required")
		return
	}
	rc, err := h.Docker.PullImage(r.Context(), req.Image)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rc.Close()
	h.audit(r, audit.ActionImagePull, req.Image, nil)
	// Stream the pull progress to the client as ndjson.
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.WriteHeader(http.StatusOK)
	if f, ok := w.(http.Flusher); ok {
		buf := make([]byte, 4096)
		for {
			n, err := rc.Read(buf)
			if n > 0 {
				_, _ = w.Write(buf[:n])
				f.Flush()
			}
			if err != nil {
				break
			}
		}
	} else {
		_, _ = io.Copy(w, rc)
	}
}

func (h *Handlers) RemoveImage(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	force := r.URL.Query().Get("force") == "true"
	deleted, err := h.Docker.RemoveImage(r.Context(), id, force)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionImageRemove, id, nil)
	writeJSON(w, http.StatusOK, deleted)
}

func (h *Handlers) PruneImages(w http.ResponseWriter, r *http.Request) {
	if h.Docker == nil {
		writeError(w, http.StatusServiceUnavailable, "docker unavailable")
		return
	}
	report, err := h.Docker.PruneImages(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionImagePrune, "", map[string]int64{"space_reclaimed": int64(report.SpaceReclaimed)})
	writeJSON(w, http.StatusOK, report)
}
