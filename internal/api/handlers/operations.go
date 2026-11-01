package handlers

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/operations"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/updater"
)

const (
	opImageUpdate   = operations.KindImageUpdate
	opBackupRun     = operations.KindBackupRun
	opBackupRestore = operations.KindBackupRestore
	opProxySite     = operations.KindProxySite
)

// opViewPerm is the permission that shows an operation of kind; the
// caller's scope narrows it further in operationVisible.
func opViewPerm(kind string) rbac.Perm {
	switch {
	case strings.HasPrefix(kind, "image."):
		return rbac.PermContainersView
	case strings.HasPrefix(kind, "backup."):
		return rbac.PermBackupsView
	case strings.HasPrefix(kind, "proxy."):
		return rbac.PermProxyView
	}
	return ""
}

// opActionPerm is the permission of the action that started an
// operation of kind; cancelling it needs the same.
func opActionPerm(kind string) rbac.Perm {
	switch kind {
	case opImageUpdate:
		return rbac.PermContainersUpgrade
	case opBackupRun:
		return rbac.PermBackupsRun
	case opBackupRestore:
		return rbac.PermBackupsRestore
	case opProxySite:
		return rbac.PermProxyCreate
	}
	return ""
}

// operationVisible applies the same rules as the resource the operation
// works on: a caller sees an image update only on containers in scope,
// a backup only on hosts in scope (system backups only as admin).
func (h *Handlers) operationVisible(r *http.Request, op *operations.Operation) bool {
	perm := opViewPerm(op.Kind)
	if perm == "" || !middleware.Allowed(r.Context(), perm) {
		return false
	}
	r = r.WithContext(middleware.WithRoutePerm(r.Context(), perm))
	if op.AdminOnly {
		return h.callerIsAdmin(r)
	}
	hostID := op.HostID
	if hostID == "" {
		hostID = "local"
	}
	switch {
	case strings.HasPrefix(op.Kind, "image."):
		return h.inReadScope(r, hostID, op.Stack)
	case strings.HasPrefix(op.Kind, "backup."):
		if !h.inReadScope(r, hostID, "") {
			return false
		}
		if op.Kind == opBackupRestore {
			return h.restoreOriginVisible(r, op.Ref)
		}
		return true
	case strings.HasPrefix(op.Kind, "proxy."):
		return true
	}
	return false
}

// restoreOriginVisible checks the host a restored run was taken on; a
// restore also shows what that host's archive held.
func (h *Handlers) restoreOriginVisible(r *http.Request, ref string) bool {
	id, err := strconv.ParseInt(strings.TrimPrefix(ref, operations.BackupRunPrefix), 10, 64)
	if err != nil || h.Backups == nil {
		return false
	}
	origin, err := h.Backups.RunHost(r.Context(), id)
	if err != nil {
		return false
	}
	return h.inReadScope(r, origin, "")
}

// opActor names who started an operation: the user, the owner of the API
// token, or the token itself when nobody owns it.
func opActor(ctx context.Context) string {
	if id := middleware.UserID(ctx); id != "" {
		return id
	}
	if id := middleware.TokenOwnerID(ctx); id != "" {
		return id
	}
	if id := middleware.APITokenID(ctx); id != 0 {
		return "api-token:" + strconv.FormatInt(id, 10)
	}
	return ""
}

// operationView is an operation as the API returns it.
type operationView struct {
	operations.Operation
	ActorName string `json:"actor_name,omitempty"`
}

// viewOperations names who started each operation for callers who may
// see users, and for the actor themselves.
func (h *Handlers) viewOperations(ctx context.Context, ops []operations.Operation) []operationView {
	names := map[string]string{}
	seeUsers := middleware.Allowed(ctx, rbac.PermUsersView)
	self := middleware.UserID(ctx)
	out := make([]operationView, len(ops))
	for i, op := range ops {
		out[i] = operationView{Operation: op}
		if op.Actor == "" || h.DB == nil || (!seeUsers && op.Actor != self) {
			continue
		}
		name, ok := names[op.Actor]
		if !ok {
			_ = h.DB.QueryRowContext(ctx, `SELECT username FROM users WHERE id = ?`, op.Actor).Scan(&name)
			names[op.Actor] = name
		}
		out[i].ActorName = name
	}
	return out
}

// ListOperations returns the operations the caller may see, newest
// first, without their logs.
//
//	GET /api/v1/operations?kind=&ref=&group=&active=1&limit=
func (h *Handlers) ListOperations(w http.ResponseWriter, r *http.Request) {
	if h.Operations == nil {
		writeJSON(w, http.StatusOK, []operationView{})
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	f := operations.Filter{
		Kind:       q.Get("kind"),
		Ref:        q.Get("ref"),
		GroupID:    q.Get("group"),
		ActiveOnly: q.Get("active") == "1" || q.Get("active") == "true",
		Limit:      limit,
	}
	ops, err := h.Operations.List(r.Context(), f)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	visible := ops[:0]
	for i := range ops {
		if h.operationVisible(r, &ops[i]) {
			visible = append(visible, ops[i])
		}
	}
	writeJSON(w, http.StatusOK, h.viewOperations(r.Context(), visible))
}

// GetOperation returns one operation with its log. Operations the caller
// may not see are reported as missing.
//
//	GET /api/v1/operations/{id}
func (h *Handlers) GetOperation(w http.ResponseWriter, r *http.Request) {
	op, ok := h.operationForAccess(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, h.viewOperations(r.Context(), []operations.Operation{*op})[0])
}

// CancelOperation cancels a queued operation. It needs the permission of
// the action that started it.
//
//	POST /api/v1/operations/{id}/cancel
func (h *Handlers) CancelOperation(w http.ResponseWriter, r *http.Request) {
	op, ok := h.operationForAccess(w, r)
	if !ok {
		return
	}
	perm := opActionPerm(op.Kind)
	hostID := op.HostID
	if hostID == "" {
		hostID = "local"
	}
	res := h.stackScopeReq(r.Context(), hostID, op.Stack)
	if perm == "" || !middleware.AllowedOn(r.Context(), perm, res) {
		writeError(w, http.StatusForbidden, "cancelling this needs the "+string(perm)+" permission")
		return
	}
	if op.Kind == opImageUpdate && op.Stack != "" && !middleware.AllowedOn(r.Context(), rbac.PermStacksDeploy, res) {
		writeError(w, http.StatusForbidden, "cancelling an update of stack "+op.Stack+" needs the stacks.deploy permission")
		return
	}
	if err := h.Operations.Cancel(op.ID); errors.Is(err, operations.ErrNotCancellable) {
		writeError(w, http.StatusConflict, "only queued operations can be cancelled")
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, "operation.cancel", op.Kind+":"+op.Target, map[string]string{"operation": op.ID})
	got, err := h.Operations.Get(r.Context(), op.ID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.viewOperations(r.Context(), []operations.Operation{*got})[0])
}

func (h *Handlers) operationForAccess(w http.ResponseWriter, r *http.Request) (*operations.Operation, bool) {
	if h.Operations == nil {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	op, err := h.Operations.Get(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, operations.ErrNotFound) || (err == nil && !h.operationVisible(r, op)) {
		writeError(w, http.StatusNotFound, "not found")
		return nil, false
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return nil, false
	}
	return op, true
}

// startImageUpdateOp records an image update of a local container.
func (h *Handlers) startImageUpdateOp(r *http.Request, containerID, group string, queued bool) *operations.Op {
	if h.Operations == nil {
		return nil
	}
	target := containerID
	if h.Docker != nil {
		if info, err := h.Docker.InspectContainer(r.Context(), containerID); err == nil {
			target = strings.TrimPrefix(info.Name, "/")
		}
	}
	stack := h.Updater.ManagedStack(r.Context(), containerID)
	return h.Operations.Start(r.Context(), operations.Spec{
		Kind:    opImageUpdate,
		Target:  target,
		Ref:     "container:" + containerID,
		GroupID: group,
		HostID:  "local",
		Stack:   stack,
		Actor:   opActor(r.Context()),
		Steps:   updater.UpdateSteps,
		Queued:  queued,
		Secrets: h.stackEnvValues(stack),
	})
}

type bulkUpdateRequest struct {
	ContainerIDs []string `json:"container_ids"`
}

type bulkUpdateResponse struct {
	GroupID    string          `json:"group_id"`
	Operations []operationView `json:"operations"`
}

const maxBulkUpdates = 100

// BulkUpdateContainers queues image updates for several containers and
// runs them one after another in the background, each as its own
// operation, so a bulk update does not depend on the request staying
// open. Every container is checked like a single update first; one
// denial rejects the whole request.
//
//	POST /api/v1/containers/updates
func (h *Handlers) BulkUpdateContainers(w http.ResponseWriter, r *http.Request) {
	if h.Updater == nil || h.Operations == nil {
		writeError(w, http.StatusServiceUnavailable, "updater not configured")
		return
	}
	var req bulkUpdateRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	seen := map[string]bool{}
	var ids []string
	for _, id := range req.ContainerIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		writeError(w, http.StatusBadRequest, "container_ids required")
		return
	}
	if len(ids) > maxBulkUpdates {
		writeError(w, http.StatusBadRequest, "at most "+strconv.Itoa(maxBulkUpdates)+" containers per request")
		return
	}
	for i, id := range ids {
		// Pin the full id, so a name reused before the item runs cannot
		// redirect the update to another container.
		if h.Docker != nil {
			info, err := h.Docker.InspectContainer(r.Context(), id)
			if err != nil {
				writeError(w, http.StatusNotFound, "container "+id+" not found")
				return
			}
			id, ids[i] = info.ID, info.ID
		}
		if !h.requireContainerScope(w, r, rbac.PermContainersUpgrade, "local", id) {
			return
		}
		if !h.allowStackRebuild(w, r, id) {
			return
		}
	}

	group := operations.NewGroupID()
	type queued struct {
		id    string
		op    *operations.Op
		state *atomic.Int32 // 0 queued, 1 started, 2 cancelled
	}
	list := make([]queued, len(ids))
	snaps := make([]operations.Operation, len(ids))
	for i, id := range ids {
		st := &atomic.Int32{}
		op := h.startImageUpdateOp(r, id, group, true)
		op.SetCancel(func() bool { return st.CompareAndSwap(0, 2) })
		list[i] = queued{id: id, op: op, state: st}
		if got, err := h.Operations.Get(r.Context(), op.ID()); err == nil {
			snaps[i] = *got
		}
	}

	ctx := context.WithoutCancel(r.Context())
	uid := middleware.UserID(ctx)
	go func() {
		for _, q := range list {
			if !q.state.CompareAndSwap(0, 1) {
				continue
			}
			// Each item is authorised again when it starts: the queue can
			// run long after the request, and the caller may have been
			// signed out or lost the permission or scope in between.
			ictx, err := h.refreshIdentity(ctx)
			rr := r.WithContext(ictx)
			if err == nil {
				if why := h.imageUpdateDenied(rr, q.id); why != "" {
					err = errors.New(why)
				}
			}
			if err != nil {
				q.op.Finish(fmt.Errorf("not started: %w", err))
				continue
			}
			q.op.Begin()
			res, err := h.Updater.Update(operations.WithOp(ictx, q.op), q.id, uid)
			q.op.Finish(err)
			if err == nil && res.Updated {
				h.UpdateWatcher.MarkUpdated(ictx, res.Image)
				h.audit(rr, audit.ActionContainerUpdate, res.ContainerName, map[string]string{
					"image":    res.Image,
					"from":     trim(res.OldDigest),
					"to":       trim(res.NewDigest),
					"rollback": res.RollbackTag,
				})
			}
		}
	}()
	writeJSON(w, http.StatusAccepted, bulkUpdateResponse{GroupID: group, Operations: h.viewOperations(r.Context(), snaps)})
}
