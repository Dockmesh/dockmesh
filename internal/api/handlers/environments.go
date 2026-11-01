package handlers

import (
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/scaling"
	"github.com/go-chi/chi/v5"
)

// resolveEnvOverride picks the environment override for a deploy:
//   - ?environment=<name> query param wins (explicit deploy-time choice)
//   - otherwise falls back to .dockmesh.meta.json's active_environment
//   - empty means no override — the base compose.yaml is used as-is
//
// Returns the override name it picked and, when an override was applied,
// the merged YAML. The merged YAML is non-empty only when an override
// fired; callers should use detail.Compose untouched otherwise.
//
// Errors surface cleanly to the handler: invalid name / missing overlay
// file / compose merge errors all become 422.
// P.12.8.
func (h *Handlers) resolveEnvOverride(r *http.Request, name, composeYAML, envContent string) (string, string, error) {
	requested := r.URL.Query().Get("environment")
	if requested == "" {
		// Fall back to meta file.
		dir, err := h.Stacks.Dir(name)
		if err != nil {
			return "", "", nil // stack missing from meta discovery is the stack handler's problem, not ours
		}
		meta, err := scaling.LoadMeta(dir)
		if err == nil && meta != nil {
			requested = meta.ActiveEnvironment
		}
	}
	if requested == "" {
		return "", "", nil
	}

	dir, err := h.Stacks.Dir(name)
	if err != nil {
		return "", "", err
	}
	_, merged, err := compose.MergeEnvironment(r.Context(), dir, name, envContent, requested)
	if err != nil {
		return "", "", err
	}
	_ = composeYAML // base passed in for symmetry; the merge reads compose.yaml directly from dir
	return requested, merged, nil
}

// ListEnvironments returns every compose.<name>.yaml overlay found in
// the stack directory, plus the currently-active default from the
// meta file.
//
//	GET /api/v1/stacks/{name}/environments
//
// P.12.8.
func (h *Handlers) ListEnvironments(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	dir, err := h.Stacks.Dir(name)
	if err != nil {
		writeStackError(w, err)
		return
	}
	available, err := compose.DiscoverEnvironments(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	var active string
	if meta, err := scaling.LoadMeta(dir); err == nil && meta != nil {
		active = meta.ActiveEnvironment
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"stack_name": name,
		"active":     active,
		"available":  available,
	})
}

type setActiveEnvironmentRequest struct {
	// Name of the overlay file without prefix/suffix — e.g. "prod" for
	// compose.prod.yaml. Empty string clears the default (deploys use
	// the base compose.yaml unless ?environment= overrides).
	Active string `json:"active"`
}

// SetActiveEnvironment writes `active_environment` into the stack's
// .dockmesh.meta.json. The value is validated against the naming rule
// (lowercase letters, digits, dashes, underscores) but NOT against the
// list of existing overlay files on disk — operators legitimately set
// the default ahead of adding the overlay file (git-synced stacks land
// the files later in the sync cycle).
//
//	PUT /api/v1/stacks/{name}/environments/active
//	Body: { "active": "prod" }
//
// P.12.8.
func (h *Handlers) SetActiveEnvironment(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	dir, err := h.Stacks.Dir(name)
	if err != nil {
		writeStackError(w, err)
		return
	}
	var req setActiveEnvironmentRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if err := compose.ValidateEnvironmentName(req.Active); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	meta, err := scaling.LoadMeta(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if meta == nil {
		meta = &scaling.MetaFile{}
	}
	meta.ActiveEnvironment = req.Active
	if err := scaling.SaveMeta(dir, meta); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackUpdate, name, map[string]any{
		"action": "set-active-environment",
		"active": req.Active,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"stack_name": name,
		"active":     req.Active,
	})
}

