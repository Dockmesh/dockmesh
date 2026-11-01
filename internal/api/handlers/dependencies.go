package handlers

import (
	"errors"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/go-chi/chi/v5"
)

// GetStackDependencies returns the list of stacks this stack depends
// on (direct edges only) and, separately, the stacks that depend on
// it (reverse edges) so the UI can show both directions at a glance.
//
//	GET /api/v1/stacks/{name}/dependencies
//
// P.12.7.
func (h *Handlers) GetStackDependencies(w http.ResponseWriter, r *http.Request) {
	if h.Dependencies == nil {
		writeError(w, http.StatusServiceUnavailable, "dependency store not configured")
		return
	}
	name := chi.URLParam(r, "name")
	deps, err := h.Dependencies.Get(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	dependents, err := h.Dependencies.Dependents(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stack_name":  name,
		"depends_on":  deps,
		"dependents":  dependents,
	})
}

type setStackDependenciesRequest struct {
	DependsOn []string `json:"depends_on"`
}

// SetStackDependencies replaces the full list of dependencies for a
// stack. Cycle detection runs against the full graph; a request that
// would close a loop is rejected with 422. Unknown stack names in the
// list aren't rejected here — operators legitimately create edges
// before the depending stack exists (e.g. declaring a future `worker`
// stack that depends on `postgres` up front).
//
//	PUT /api/v1/stacks/{name}/dependencies
//	Body: { "depends_on": ["postgres", "redis"] }
//
// P.12.7.
func (h *Handlers) SetStackDependencies(w http.ResponseWriter, r *http.Request) {
	if h.Dependencies == nil {
		writeError(w, http.StatusServiceUnavailable, "dependency store not configured")
		return
	}
	name := chi.URLParam(r, "name")
	var req setStackDependenciesRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := h.Dependencies.Set(r.Context(), name, req.DependsOn); err != nil {
		if errors.Is(err, stacks.ErrDependencyCycle) {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackUpdate, name, map[string]any{
		"action":     "dependencies-set",
		"depends_on": req.DependsOn,
	})
	// Re-read so the response reflects the post-dedup canonical set.
	deps, _ := h.Dependencies.Get(r.Context(), name)
	writeJSON(w, http.StatusOK, map[string]any{
		"stack_name": name,
		"depends_on": deps,
	})
}
