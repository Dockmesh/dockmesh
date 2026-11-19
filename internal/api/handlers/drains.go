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

// drainDenial is a right the caller lacks for one stack of a drain.
type drainDenial struct {
	perm  rbac.Perm
	res   rbac.Resource
	label string
}

func (d *drainDenial) Error() string { return "missing " + string(d.perm) + " for " + d.label }

func (h *Handlers) writeDrainDenial(w http.ResponseWriter, r *http.Request, d *drainDenial) {
	h.writeScopeDenied(w, r, d.perm, d.res, d.label)
}

// checkDrainHooksReadable refuses a drain whose stacks run hook commands
// the caller may not read: they are shown for confirmation and can hold
// the same secrets as the stack's .env.
func (h *Handlers) checkDrainHooksReadable(w http.ResponseWriter, r *http.Request, hostID string, entries []migration.PlanEntry) bool {
	if d := h.drainHooksDenial(r, hostID, entries); d != nil {
		h.writeDrainDenial(w, r, d)
		return false
	}
	return true
}

func (h *Handlers) drainHooksDenial(r *http.Request, hostID string, entries []migration.PlanEntry) *drainDenial {
	for _, e := range entries {
		if e.Hooks == nil {
			continue
		}
		res := h.stackScopeReq(hostID, e.StackName)
		if !middleware.AllowedOn(r.Context(), rbac.PermStacksDeploy, res) && !middleware.AllowedOn(r.Context(), rbac.PermContainersEnv, res) {
			return &drainDenial{rbac.PermStacksDeploy, res, "hook commands of stack " + e.StackName}
		}
	}
	return nil
}

// drainMovesDenial finds a stack of the drain the caller could not
// migrate by hand: Migrate on it here, Migrate and Deploy at its target.
func (h *Handlers) drainMovesDenial(r *http.Request, hostID string, entries []migration.PlanEntry) *drainDenial {
	for _, e := range entries {
		src := h.stackScopeReq(hostID, e.StackName)
		if !middleware.AllowedOn(r.Context(), rbac.PermStacksMigrate, src) {
			return &drainDenial{rbac.PermStacksMigrate, src, "stack " + e.StackName}
		}
		if e.TargetHostID == "" {
			continue
		}
		dst := h.stackScopeReq(e.TargetHostID, e.StackName)
		for _, p := range []rbac.Perm{rbac.PermStacksMigrate, rbac.PermStacksDeploy} {
			if !middleware.AllowedOn(r.Context(), p, dst) {
				return &drainDenial{p, dst, "stack " + e.StackName + " on host " + e.TargetHostID}
			}
		}
	}
	return nil
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
	// Checked on the plan that runs: targets and stacks can change
	// between two plans.
	authorize := func(entries []migration.PlanEntry) error {
		if d := h.drainHooksDenial(r, hostID, entries); d != nil {
			return d
		}
		if d := h.drainMovesDenial(r, hostID, entries); d != nil {
			return d
		}
		return nil
	}
	d, err := h.Drains.Execute(r.Context(), hostID, userID, req.ConfirmHooks, authorize)
	var denied *drainDenial
	if errors.As(err, &denied) {
		h.writeDrainDenial(w, r, denied)
		return
	}
	var blocked *migration.DrainBlockedError
	if errors.As(err, &blocked) {
		// Without the hook commands, like every 409 of this route.
		entries := make([]migration.PlanEntry, len(blocked.Plan.Entries))
		for i, e := range blocked.Plan.Entries {
			e.Hooks = nil
			entries[i] = e
		}
		writeJSON(w, http.StatusConflict, map[string]any{
			"error":   err.Error(),
			"code":    "drain_blocked",
			"entries": entries,
		})
		return
	}
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
	d, ok := h.hostDrain(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, d)
}

// hostDrain loads drain {drain_id} and requires it to drain host {id}.
func (h *Handlers) hostDrain(w http.ResponseWriter, r *http.Request) (*migration.Drain, bool) {
	d, err := h.Drains.Get(r.Context(), chi.URLParam(r, "drain_id"))
	if err != nil || d.SourceHostID != chi.URLParam(r, "id") {
		writeError(w, http.StatusNotFound, "drain not found")
		return nil, false
	}
	return d, true
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
	d, ok := h.hostDrain(w, r)
	if !ok {
		return
	}
	if err := h.Drains.PauseDrain(d.ID); err != nil {
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
	d, ok := h.hostDrain(w, r)
	if !ok {
		return
	}
	if err := h.Drains.ResumeDrain(d.ID); err != nil {
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
	d, ok := h.hostDrain(w, r)
	if !ok {
		return
	}
	if err := h.Drains.AbortDrain(d.ID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	h.audit(r, audit.ActionHostDrainAbort, chi.URLParam(r, "id"), map[string]any{
		"action":   "drain-abort",
		"drain_id": d.ID,
	})
	writeJSON(w, http.StatusOK, map[string]string{"status": "aborted"})
}

// requireDrainAccess allows drains only with Hosts: Manage on everything:
// a drain moves stacks from one host onto any other.
func (h *Handlers) requireDrainAccess(w http.ResponseWriter, r *http.Request) bool {
	if middleware.AccessOf(r.Context()).AllowsGlobally(rbac.PermHostsUpdate) {
		return true
	}
	h.denyPerm(w, r, rbac.PermHostsUpdate, "draining a host moves its stacks to other hosts and needs Hosts: Manage on everything")
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
