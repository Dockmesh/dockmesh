package handlers

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/notices"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

func (h *Handlers) noticeViewer(r *http.Request) notices.Viewer {
	ctx := r.Context()
	return notices.Viewer{
		UserID: middleware.UserID(ctx),
		Allowed: func(perm string) bool {
			return middleware.AccessOf(ctx).AllowsGlobally(rbac.Perm(perm))
		},
	}
}

// ListNotices returns the open action-required notices the caller is in the
// audience of.
//
//	GET /api/v1/notices
func (h *Handlers) ListNotices(w http.ResponseWriter, r *http.Request) {
	if h.Notices == nil {
		writeJSON(w, http.StatusOK, []notices.Notice{})
		return
	}
	list, err := h.Notices.ForViewer(r.Context(), h.noticeViewer(r))
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

// DismissNotice hides a non-critical notice from the caller's banner.
//
//	POST /api/v1/notices/{id}/dismiss
func (h *Handlers) DismissNotice(w http.ResponseWriter, r *http.Request) {
	if h.Notices == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	id, err := strconv.ParseInt(chi.URLParam(r, "id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid id")
		return
	}
	err = h.Notices.Dismiss(r.Context(), h.noticeViewer(r), id)
	switch {
	case errors.Is(err, notices.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, notices.ErrNotDismissible):
		writeError(w, http.StatusConflict, err.Error())
	case err != nil:
		writeError(w, http.StatusInternalServerError, err.Error())
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
