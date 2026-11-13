package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/scaling"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
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
	name := chi.URLParam(r, "name")
	target, err := h.pickStackHost(r, name)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if h.refuseOtherHost(w, r, name, target) {
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	scopeReq := h.stackScopeReq(target.ID(), name)
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermStacksDeploy, scopeReq, "stack "+name)
		return
	}
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

	release, ok := h.lockStack(w, target.ID(), name, stacklock.OpScale)
	if !ok {
		return
	}
	defer release()
	detail, ok := h.verifiedStack(w, r, name)
	if !ok {
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

// rollingUpdateRequest is the body for POST /rolling-update. All fields
// are optional — omitted fields fall back to the compose-spec
// `deploy.update_config` on the service, and from there to the engine
// defaults (parallelism=1, order=stop-first, failure_action=pause).
type rollingUpdateRequest struct {
	Parallelism   int    `json:"parallelism,omitempty"`
	DelaySeconds  int    `json:"delay_seconds,omitempty"`
	Order         string `json:"order,omitempty"`
	FailureAction string `json:"failure_action,omitempty"`
}

// RollingUpdateService performs a rolling replacement of all replicas
// of a single service in a stack.
//
//	POST /api/v1/stacks/{name}/services/{service}/rolling-update
//	Body (all optional): {
//	  "parallelism":   1,
//	  "delay_seconds": 0,
//	  "order":         "start-first" | "stop-first",
//	  "failure_action":"pause" | "continue" | "rollback"
//	}
//
// Behaviour:
//   - Loads compose+env from the stacks filesystem
//   - Merges body overrides on top of `deploy.update_config` in compose
//   - Calls the host's RollingReplace (Local runs the engine; Remote
//     returns 501 since the agent protocol doesn't carry a rolling-
//     update frame type yet)
//
// P.12.5b.
func (h *Handlers) RollingUpdateService(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	target, err := h.pickStackHost(r, name)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if h.refuseOtherHost(w, r, name, target) {
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	scopeReq := h.stackScopeReq(target.ID(), name)
	if !h.allowedOn(r, scopeReq) {
		h.writeScopeDenied(w, r, rbac.PermStacksDeploy, scopeReq, "stack "+name)
		return
	}
	service := chi.URLParam(r, "service")

	var req rollingUpdateRequest
	// Body is optional — treat missing body as "use compose defaults".
	if r.ContentLength > 0 {
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid body")
			return
		}
	}

	switch req.Order {
	case "", "stop-first", "start-first":
	default:
		writeError(w, http.StatusBadRequest, `invalid order: must be "stop-first" or "start-first"`)
		return
	}
	switch req.FailureAction {
	case "", "pause", "continue", "rollback":
	default:
		writeError(w, http.StatusBadRequest, `invalid failure_action: must be "pause", "continue", or "rollback"`)
		return
	}
	if req.Parallelism < 0 {
		writeError(w, http.StatusBadRequest, "parallelism must be >= 0")
		return
	}
	if req.DelaySeconds < 0 {
		writeError(w, http.StatusBadRequest, "delay_seconds must be >= 0")
		return
	}
	release, ok := h.lockStack(w, target.ID(), name, stacklock.OpRolling)
	if !ok {
		return
	}
	defer release()
	detail, ok := h.verifiedStack(w, r, name)
	if !ok {
		return
	}

	opts := compose.RollingOptions{
		Parallelism:   req.Parallelism,
		Order:         compose.UpdateOrder(req.Order),
		FailureAction: compose.FailureAction(req.FailureAction),
	}
	if req.DelaySeconds > 0 {
		opts.Delay = time.Duration(req.DelaySeconds) * time.Second
	}
	// Zero fields bubble into the engine, which fills them from the
	// service's compose `deploy.update_config` and then applies defaults.

	res, err := target.RollingReplace(r.Context(), name, detail.Compose, detail.Env, service, opts)
	if err != nil {
		// User-correctable precondition failures are 422, not 500.
		status := http.StatusInternalServerError
		if errors.Is(err, compose.ErrRollingStartFirstUnsafe) {
			status = http.StatusUnprocessableEntity
		}
		if res == nil {
			writeError(w, status, err.Error())
			return
		}
		writeJSON(w, status, map[string]any{
			"error":  err.Error(),
			"result": res,
		})
		return
	}
	h.audit(r, audit.ActionStackDeploy, name, map[string]any{
		"action":      "rolling-update",
		"service":     service,
		"updated":     res.Updated,
		"failed":      res.Failed,
		"rolled_back": res.RolledBack,
		"host":        target.ID(),
	})
	slog.Info("rolling update",
		"stack", name, "service", service,
		"updated", res.Updated, "failed", res.Failed,
		"rolled_back", res.RolledBack, "host", target.ID())
	writeJSON(w, http.StatusOK, res)
}

// GetScale returns the current replica count + safety check for a service.
//
//	GET /api/v1/stacks/{name}/services/{service}/scale
func (h *Handlers) GetScale(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickStackHost(r, chi.URLParam(r, "name"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	name := chi.URLParam(r, "name")
	service := chi.URLParam(r, "service")
	if !h.requireStackScope(w, r, rbac.PermStacksDeploy, target.ID(), name) {
		return
	}

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
	target, err := h.pickStackHost(r, chi.URLParam(r, "name"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	name := chi.URLParam(r, "name")
	if !h.requireStackScope(w, r, rbac.PermStacksDeploy, target.ID(), name) {
		return
	}

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
	// Show the unit stored rules are evaluated in.
	for i := range cfg.Rules {
		cfg.Rules[i].CPUUnit = cfg.Rules[i].Unit()
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
	// A rule saved without a unit is new; it gets the host share the charts
	// show. Stored rules without one stay per core (see Rule.Unit).
	for i := range cfg.Rules {
		if cfg.Rules[i].CPUUnit == "" {
			cfg.Rules[i].CPUUnit = scaling.CPUUnitHost
		}
	}
	if err := h.updateMeta(name, dir, func(meta *scaling.MetaFile) bool {
		meta.Scaling = &cfg
		return true
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.audit(r, audit.ActionStackUpdate, name, map[string]any{
		"action":  "scaling-rules",
		"rules":   len(cfg.Rules),
		"enabled": cfg.Enabled,
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
	removed := false
	if err := h.updateMeta(name, dir, func(meta *scaling.MetaFile) bool {
		removed = meta.Scaling != nil
		meta.Scaling = nil
		return removed
	}); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if removed {
		h.audit(r, audit.ActionStackUpdate, name, map[string]any{"action": "scaling-rules-deleted"})
	}
	w.WriteHeader(http.StatusNoContent)
}

// updateMeta changes the stack's meta file, keeping the fields change
// doesn't touch, and records Dockmesh's write of it. change reports
// whether there is anything to write.
func (h *Handlers) updateMeta(name, dir string, change func(*scaling.MetaFile) bool) error {
	return h.Stacks.WriteFile(name, stacks.MetaFileName, func() error {
		meta, err := scaling.LoadMeta(dir)
		if err != nil {
			return err
		}
		if meta == nil {
			meta = &scaling.MetaFile{}
		}
		if !change(meta) {
			return nil
		}
		return scaling.SaveMeta(dir, meta)
	})
}
