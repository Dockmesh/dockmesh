package handlers

import (
	"log/slog"
	"net/http"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/scaling"
	"github.com/go-chi/chi/v5"
)

type scaleRequest struct {
	Replicas int  `json:"replicas"`
	Force    bool `json:"force"` // skip stateful warning
}

// ScaleService adjusts the replica count for a single service.
//
//	POST /api/v1/stacks/{name}/services/{service}/scale
//	Body: { "replicas": 3, "force": false }
//
// Safety: refuses if container_name or hard port bindings are set.
// Warns (but proceeds with force=true) if the service is stateful.
func (h *Handlers) ScaleService(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	name := chi.URLParam(r, "name")
	service := chi.URLParam(r, "service")

	var req scaleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Replicas < 0 || req.Replicas > 100 {
		writeError(w, http.StatusBadRequest, "replicas must be between 0 and 100")
		return
	}

	// Read canonical compose+env from the server filesystem.
	detail, err := h.Stacks.Get(name)
	if err != nil {
		writeStackError(w, err)
		return
	}

	// Pre-flight check.
	check, err := target.CheckScale(r.Context(), name, detail.Compose, detail.Env, service)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if req.Replicas > 1 && check.HasContainerName {
		writeError(w, http.StatusBadRequest,
			"service "+service+" has container_name set — remove it to allow scaling")
		return
	}
	if req.Replicas > 1 && check.HasHardPort {
		writeError(w, http.StatusBadRequest,
			"service "+service+" has hard-coded host port "+check.HardPortDetail+
				" — use a port range or remove the host binding")
		return
	}
	if req.Replicas > 1 && check.IsStateful && !req.Force {
		writeJSON(w, http.StatusConflict, map[string]any{
			"warning":      "stateful_service",
			"message":      "Service " + service + " looks like a database (" + check.StatefulImage + ") with mounted volumes. Scaling may cause data corruption.",
			"force_needed": true,
		})
		return
	}

	res, err := target.ScaleService(r.Context(), name, detail.Compose, detail.Env, service, req.Replicas)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackDeploy, name, map[string]any{
		"action":   "scale",
		"service":  service,
		"previous": res.Previous,
		"current":  res.Current,
		"host":     target.ID(),
	})
	slog.Info("scale service",
		"stack", name, "service", service,
		"previous", res.Previous, "current", res.Current,
		"host", target.ID())
	writeJSON(w, http.StatusOK, res)
}

// GetScale returns the current replica count + safety check for a service.
//
//	GET /api/v1/stacks/{name}/services/{service}/scale
func (h *Handlers) GetScale(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	name := chi.URLParam(r, "name")
	service := chi.URLParam(r, "service")

	detail, err := h.Stacks.Get(name)
	if err != nil {
		writeStackError(w, err)
		return
	}

	check, err := target.CheckScale(r.Context(), name, detail.Compose, detail.Env, service)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, check)
}

// ListServiceScale returns the replica count for all services in a stack.
//
//	GET /api/v1/stacks/{name}/scale
func (h *Handlers) ListServiceScale(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	name := chi.URLParam(r, "name")

	status, err := target.StackStatus(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Group by service and count.
	counts := make(map[string]int)
	for _, s := range status {
		counts[s.Service]++
	}
	type entry struct {
		Service  string `json:"service"`
		Replicas int    `json:"replicas"`
	}
	out := make([]entry, 0, len(counts))
	for svc, n := range counts {
		out = append(out, entry{Service: svc, Replicas: n})
	}
	writeJSON(w, http.StatusOK, out)
}

// GetScalingRules returns the scaling config from .dockmesh.meta.json.
//
//	GET /api/v1/stacks/{name}/scaling-rules
func (h *Handlers) GetScalingRules(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	dir, err := h.Stacks.Dir(name)
	if err != nil {
		writeStackError(w, err)
		return
	}
	cfg, err := scaling.LoadRules(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if cfg == nil {
		cfg = &scaling.ScalingConfig{}
	}
	writeJSON(w, http.StatusOK, cfg)
}

// SetScalingRules writes the scaling config to .dockmesh.meta.json.
//
//	PUT /api/v1/stacks/{name}/scaling-rules
func (h *Handlers) SetScalingRules(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	dir, err := h.Stacks.Dir(name)
	if err != nil {
		writeStackError(w, err)
		return
	}
	var cfg scaling.ScalingConfig
	if err := decodeJSON(r, &cfg); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return
	}
	if err := cfg.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// Load existing meta file to preserve other fields (migration, etc).
	meta, err := scaling.LoadMeta(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if meta == nil {
		meta = &scaling.MetaFile{}
	}
	meta.Scaling = &cfg
	if err := scaling.SaveMeta(dir, meta); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackUpdate, name, map[string]any{
		"action":     "scaling-rules",
		"rules":      len(cfg.Rules),
		"enabled":    cfg.Enabled,
	})
	writeJSON(w, http.StatusOK, cfg)
}

// DeleteScalingRules removes the scaling section from .dockmesh.meta.json.
//
//	DELETE /api/v1/stacks/{name}/scaling-rules
func (h *Handlers) DeleteScalingRules(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	dir, err := h.Stacks.Dir(name)
	if err != nil {
		writeStackError(w, err)
		return
	}
	meta, err := scaling.LoadMeta(dir)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if meta == nil || meta.Scaling == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	meta.Scaling = nil
	if err := scaling.SaveMeta(dir, meta); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackUpdate, name, map[string]any{"action": "scaling-rules-deleted"})
	w.WriteHeader(http.StatusNoContent)
}
