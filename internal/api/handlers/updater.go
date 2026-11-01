package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/rbac"
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
	if !h.requireContainerScope(w, r, rbac.PermContainersUpdate, "local", id) {
		return
	}
	if !h.allowStackRebuild(w, r, id) {
		return
	}
	// Detached from the request: closing the tab mid-update must not cancel
	// the pull or interrupt the container swap.
	r = r.WithContext(context.WithoutCancel(r.Context()))
	op := h.startImageUpdateOp(r, id, "", false)
	res, err := h.Updater.Update(operations.WithOp(r.Context(), op), id, middleware.UserID(r.Context()))
	op.Finish(err)
	if errors.Is(err, updater.ErrBusy) {
		writeError(w, http.StatusConflict, err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if res.Updated {
		h.UpdateWatcher.MarkUpdated(r.Context(), res.Image)
		h.audit(r, audit.ActionContainerUpdate, res.ContainerName, map[string]string{
			"image":    res.Image,
			"from":     trim(res.OldDigest),
			"to":       trim(res.NewDigest),
			"rollback": res.RollbackTag,
		})
	}
	writeJSON(w, http.StatusOK, res)
}

func (h *Handlers) RollbackContainer(w http.ResponseWriter, r *http.Request) {
	if h.Updater == nil {
		writeError(w, http.StatusServiceUnavailable, "updater not configured")
		return
	}
	id := chi.URLParam(r, "id")
	if !h.requireContainerScope(w, r, rbac.PermContainersUpdate, "local", id) {
		return
	}
	var req rollbackRequest
	if err := decodeJSON(r, &req); err != nil || req.HistoryID == 0 {
		writeError(w, http.StatusBadRequest, "history_id required")
		return
	}
	if !h.allowStackRebuild(w, r, id) {
		return
	}
	res, err := h.Updater.Rollback(r.Context(), id, req.HistoryID, middleware.UserID(r.Context()))
	if errors.Is(err, updater.ErrHistoryNotFound) {
		writeError(w, http.StatusNotFound, "history entry not found")
		return
	}
	if errors.Is(err, updater.ErrAlreadyRolledBack) {
		writeError(w, http.StatusConflict, "already rolled back")
		return
	}
	if errors.Is(err, updater.ErrRollbackImageGone) || errors.Is(err, updater.ErrBusy) {
		writeError(w, http.StatusConflict, err.Error())
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
	scopeReq := h.containerScopeReq(r.Context(), "local", id)
	if !h.checkRoleScope(r, scopeReq) {
		h.writeRoleScopeDenied(w, r, rbac.PermContainersUpdate, scopeReq, "container "+id)
		return
	}
	info, err := h.Docker.InspectContainer(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusNotFound, "container not found")
		return
	}
	name := strings.TrimPrefix(info.Name, "/")

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

// allowStackRebuild additionally requires stacks.deploy when the update
// rebuilds the container from its stack's compose.yaml, since that
// applies compose changes saved since the last deploy.
func (h *Handlers) allowStackRebuild(w http.ResponseWriter, r *http.Request, containerID string) bool {
	stack := h.Updater.ManagedStack(r.Context(), containerID)
	if stack == "" || middleware.Allowed(r.Context(), rbac.PermStacksDeploy) {
		return true
	}
	writeError(w, http.StatusForbidden, "updating a container of stack "+stack+" needs the stacks.deploy permission")
	return false
}
