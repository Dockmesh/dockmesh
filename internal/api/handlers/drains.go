package handlers

import (
	"errors"
	"io"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/migration"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/go-chi/chi/v5"
)

// PlanDrain generates a bin-packing plan without executing.
//
//	POST /api/v1/hosts/{id}/drain/plan
func (h *Handlers) PlanDrain(w http.ResponseWriter, r *http.Request) {
	if !h.requireDrainAccess(w, r) {
		return
	}
	if h.Drains == nil {
		writeError(w, http.StatusServiceUnavailable, "drain service unavailable")
		return
	}
	hostID := chi.URLParam(r, "id")
	plan, err := h.Drains.Plan(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.checkDrainHooksReadable(w, r, hostID, plan.Entries) {
		return
	}
	writeJSON(w, http.StatusOK, plan)
}

// checkDrainHooksReadable refuses a drain whose stacks run hook commands
// the caller may not read: they are shown for confirmation and can hold
// the same secrets as the stack's .env.
func (h *Handlers) checkDrainHooksReadable(w http.ResponseWriter, r *http.Request, hostID string, entries []migration.PlanEntry) bool {
	for _, e := range entries {
		if e.Hooks == nil {
			continue
		}
		res := h.stackScopeReq(hostID, e.StackName)
		if !middleware.AllowedOn(r.Context(), rbac.PermStacksDeploy, res) && !middleware.AllowedOn(r.Context(), rbac.PermContainersEnv, res) {
			h.writeScopeDenied(w, r, rbac.PermStacksDeploy, res, "hook commands of stack "+e.StackName)
			return false
		}
	}
	return true
}

// ExecuteDrain runs a drain plan.
//
//	POST /api/v1/hosts/{id}/drain/execute
func (h *Handlers) ExecuteDrain(w http.ResponseWriter, r *http.Request) {
	if !h.requireDrainAccess(w, r) {
		return
	}
	if h.Drains == nil {
		writeError(w, http.StatusServiceUnavailable, "drain service unavailable")
		return
	}
	hostID := chi.URLParam(r, "id")
	scopeReq := h.hostScopeReq(hostID)
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermHostsUpdate, scopeReq, "host "+hostID)
		return
	}
	plan, err := h.Drains.Plan(r.Context(), hostID)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if !h.checkDrainHooksReadable(w, r, hostID, plan.Entries) {
		return
	}
	var req struct {
		ConfirmHooks map[string]string `json:"confirm_hooks"`
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}
	userID := middleware.UserID(r.Context())
	d, err := h.Drains.Execute(r.Context(), hostID, userID, req.ConfirmHooks)
	var unconfirmed *migration.DrainHooksUnconfirmedError
	if errors.As(err, &unconfirmed) {
		if !h.checkDrainHooksReadable(w, r, hostID, unconfirmed.Entries) {
			return
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   err.Error(),
			"code":    "hooks_unconfirmed",
			"entries": unconfirmed.Entries,
		})
		return
	}
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionHostDrain, hostID, map[string]any{
		"action":       "drain",
		"drain_id":     d.ID,
		"stacks":       len(d.Plan),
		"hooks_digest": confirmedDigests(d.Plan),
	})
	writeJSON(w, http.StatusAccepted, d)
}

// GetDrain returns the current state of a drain.
//
//	GET /api/v1/hosts/{id}/drain/{drain_id}
func (h *Handlers) GetDrain(w http.ResponseWriter, r *http.Request) {
	if !h.requireDrainAccess(w, r) {
		return
	}
	if h.Drains == nil {
		writeError(w, http.StatusServiceUnavailable, "drain service unavailable")
		return
	}
	drainID := chi.URLParam(r, "drain_id")
	d, err := h.Drains.Get(r.Context(), drainID)
	if err != nil {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// PauseDrain pauses a running drain between stacks.
//
//	POST /api/v1/hosts/{id}/drain/{drain_id}/pause
func (h *Handlers) PauseDrain(w http.ResponseWriter, r *http.Request) {
	if !h.requireDrainAccess(w, r) {
		return
	}
	if h.Drains == nil {
		writeError(w, http.StatusServiceUnavailable, "drain service unavailable")
		return
	}
	drainID := chi.URLParam(r, "drain_id")
	if err := h.Drains.PauseDrain(drainID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "paused"})
}

// ResumeDrain resumes a paused drain.
//
//	POST /api/v1/hosts/{id}/drain/{drain_id}/resume
func (h *Handlers) ResumeDrain(w http.ResponseWriter, r *http.Request) {
	if !h.requireDrainAccess(w, r) {
		return
	}
	if h.Drains == nil {
		writeError(w, http.StatusServiceUnavailable, "drain service unavailable")
		return
	}
	drainID := chi.URLParam(r, "drain_id")
	if err := h.Drains.ResumeDrain(drainID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "resumed"})
}

// AbortDrain cancels pending stacks in a drain.
//
//	POST /api/v1/hosts/{id}/drain/{drain_id}/abort
func (h *Handlers) AbortDrain(w http.ResponseWriter, r *http.Request) {
	if !h.requireDrainAccess(w, r) {
		return
	}
	if h.Drains == nil {
		writeError(w, http.StatusServiceUnavailable, "drain service unavailable")
		return
	}
	drainID := chi.URLParam(r, "drain_id")
	if err := h.Drains.AbortDrain(drainID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionHostDrainAbort, chi.URLParam(r, "id"), map[string]any{
		"action":   "drain-abort",
		"drain_id": drainID,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "aborted"})
}

// requireDrainAccess allows drains only with Hosts: Manage on everything:
// a drain moves stacks from one host onto any other.
func (h *Handlers) requireDrainAccess(w http.ResponseWriter, r *http.Request) bool {
	if middleware.AccessOf(r.Context()).AllowsGlobally(rbac.PermHostsUpdate) {
		return true
	}
	writeError(w, http.StatusForbidden, "draining a host moves its stacks to other hosts and needs Hosts: Manage on everything")
	return false
}

// confirmedDigests maps each stack of a started drain with hook commands
// to the digest that was checked.
func confirmedDigests(plan []migration.PlanEntry) map[string]string {
	out := map[string]string{}
	for _, e := range plan {
		if e.Hooks != nil {
			out[e.StackName] = e.Hooks.Digest
		}
	}
	return out
}
