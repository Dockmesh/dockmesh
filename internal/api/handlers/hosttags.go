package handlers

import (
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/hosttags"
	"github.com/go-chi/chi/v5"
)

// ListHostTags returns the tag list for one host.
//
//	GET /api/v1/hosts/{id}/tags
func (h *Handlers) ListHostTags(w http.ResponseWriter, r *http.Request) {
	if h.HostTags == nil {
		writeError(w, http.StatusServiceUnavailable, "host tags unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	tags := h.HostTags.Tags(id)
	writeJSON(w, http.StatusOK, tags)
}

// SetHostTagsInput is the PUT body.
type SetHostTagsInput struct {
	Tags []string `json:"tags"`
}

// SetHostTags replaces the tag list for a host. Validates tag syntax
// and the 20-tags-per-host cap.
//
//	PUT /api/v1/hosts/{id}/tags
func (h *Handlers) SetHostTags(w http.ResponseWriter, r *http.Request) {
	if h.HostTags == nil {
		writeError(w, http.StatusServiceUnavailable, "host tags unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	var in SetHostTagsInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	canon, err := h.HostTags.Set(r.Context(), id, in.Tags)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, hosttags.ErrTooManyTags) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, err.Error())
		return
	}
	h.audit(r, "host.tags_set", id, map[string]any{"tags": canon})
	writeJSON(w, http.StatusOK, canon)
}

// AddHostTagInput is the POST body for adding a single tag.
type AddHostTagInput struct {
	Tag string `json:"tag"`
}

// AddHostTag grants a single tag to a host.
//
//	POST /api/v1/hosts/{id}/tags
func (h *Handlers) AddHostTag(w http.ResponseWriter, r *http.Request) {
	if h.HostTags == nil {
		writeError(w, http.StatusServiceUnavailable, "host tags unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	var in AddHostTagInput
	if err := decodeJSON(r, &in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := h.HostTags.Add(r.Context(), id, in.Tag); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, hosttags.ErrTooManyTags) {
			status = http.StatusUnprocessableEntity
		}
		writeError(w, status, err.Error())
		return
	}
	h.audit(r, "host.tag_add", id, map[string]any{"tag": in.Tag})
	writeJSON(w, http.StatusOK, h.HostTags.Tags(id))
}

// RemoveHostTag revokes a single tag from a host.
//
//	DELETE /api/v1/hosts/{id}/tags/{tag}
func (h *Handlers) RemoveHostTag(w http.ResponseWriter, r *http.Request) {
	if h.HostTags == nil {
		writeError(w, http.StatusServiceUnavailable, "host tags unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	tag := chi.URLParam(r, "tag")
	if err := h.HostTags.Remove(r.Context(), id, tag); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "host.tag_remove", id, map[string]any{"tag": tag})
	w.WriteHeader(http.StatusNoContent)
}

// ListAllTags returns the global distinct set of tags. Used by the UI
// to suggest existing tags in the chip-input autocomplete.
//
//	GET /api/v1/hosts/tags/all
func (h *Handlers) ListAllTags(w http.ResponseWriter, r *http.Request) {
	if h.HostTags == nil {
		writeJSON(w, http.StatusOK, []string{})
		return
	}
	writeJSON(w, http.StatusOK, h.HostTags.AllTags())
}
