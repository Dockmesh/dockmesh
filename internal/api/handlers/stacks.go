package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"strings"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/go-chi/chi/v5"
)

// mergeGlobalEnv prepends the global env vars to the stack's .env so
// compose substitution resolves ${KEY} against them, while a stack-level
// .env line with the same KEY still wins (the stack's KEY= lines come
// after globals in the resulting string and later definitions
// override earlier ones in .env resolution).
func (h *Handlers) mergeGlobalEnv(ctx context.Context, stackEnv string) string {
	if h.GlobalEnv == nil {
		return stackEnv
	}
	vars, err := h.GlobalEnv.List(ctx)
	if err != nil || len(vars) == 0 {
		return stackEnv
	}
	var b strings.Builder
	for _, v := range vars {
		fmt.Fprintf(&b, "%s=%s\n", v.Key, v.Value)
	}
	if stackEnv != "" {
		if !strings.HasSuffix(stackEnv, "\n") {
			b.WriteString("\n")
		}
		b.WriteString(stackEnv)
	}
	return b.String()
}

type stackRequest struct {
	Name    string `json:"name"`
	Compose string `json:"compose"`
	Env     string `json:"env,omitempty"`
}

// stackListEntry extends the filesystem Stack with the optional
// deployment state so the frontend can show a Host column.
type stackListEntry struct {
	*stacks.Stack
	Deployment *stacks.Deployment `json:"deployment,omitempty"`
}

func (h *Handlers) ListStacks(w http.ResponseWriter, r *http.Request) {
	list := h.Stacks.List()
	// Enrich with deployment info when available.
	var deps map[string]*stacks.Deployment
	if h.Deployments != nil {
		var err error
		deps, err = h.Deployments.All(r.Context())
		if err != nil {
			slog.Warn("list stacks: deployment query", "err", err)
		}
	}
	// Resolve host names for each deployment.
	var hostNames map[string]string
	if h.Hosts != nil && len(deps) > 0 {
		if infos, err := h.Hosts.List(r.Context()); err == nil {
			hostNames = make(map[string]string, len(infos))
			for _, info := range infos {
				hostNames[info.ID] = info.Name
			}
		}
	}
	out := make([]stackListEntry, 0, len(list))
	for _, s := range list {
		entry := stackListEntry{Stack: s}
		if d, ok := deps[s.Name]; ok {
			if hostNames != nil {
				d.HostName = hostNames[d.HostID]
			}
			entry.Deployment = d
		}
		out = append(out, entry)
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *Handlers) GetStack(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	d, err := h.Stacks.Get(name)
	if err != nil {
		writeStackError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *Handlers) CreateStack(w http.ResponseWriter, r *http.Request) {
	var req stackRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Compose == "" {
		writeError(w, http.StatusBadRequest, "compose required")
		return
	}
	d, err := h.Stacks.Create(req.Name, req.Compose, req.Env)
	if err != nil {
		writeStackError(w, err)
		return
	}
	h.audit(r, audit.ActionStackCreate, req.Name, nil)
	writeJSON(w, http.StatusCreated, d)
}

func (h *Handlers) UpdateStack(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	var req stackRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	if req.Compose == "" {
		writeError(w, http.StatusBadRequest, "compose required")
		return
	}
	d, err := h.Stacks.Update(name, req.Compose, req.Env)
	if err != nil {
		writeStackError(w, err)
		return
	}
	h.audit(r, audit.ActionStackUpdate, name, nil)
	writeJSON(w, http.StatusOK, d)
}

func (h *Handlers) DeleteStack(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	// Look up which host this stack was deployed to BEFORE deleting,
	// so we can tell the agent to remove its local copy.
	var deployHostID string
	if h.Deployments != nil {
		if d, err := h.Deployments.Get(r.Context(), name); err == nil && d != nil {
			deployHostID = d.HostID
		}
	}
	if err := h.Stacks.Delete(name); err != nil {
		writeStackError(w, err)
		return
	}
	// Remove deployment row (no-op if none exists).
	if h.Deployments != nil {
		if err := h.Deployments.Delete(r.Context(), name); err != nil {
			slog.Warn("delete stack deployment row", "stack", name, "err", err)
		}
	}
	// Remove every dependency edge this stack participates in (P.12.7)
	// so the graph doesn't drag around references to a stack that no
	// longer exists. Other stacks that depended on this one will now
	// fail-fast on their next deploy with "dependency X missing on disk",
	// which is the right signal — the operator has to decide whether
	// to drop the edge or restore the dep.
	if h.Dependencies != nil {
		if err := h.Dependencies.DeleteAll(r.Context(), name); err != nil {
			slog.Warn("delete stack dependency edges", "stack", name, "err", err)
		}
	}
	// Tell the agent to drop its local copy (P.7 compose-file mirroring).
	if deployHostID != "" {
		h.deleteStackFromAgent(r.Context(), deployHostID, name)
	}
	h.audit(r, audit.ActionStackDelete, name, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) DeployStack(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	name := chi.URLParam(r, "name")

	// Dependency resolution (P.12.7). For any declared prerequisite
	// stack whose containers aren't already running, deploy it first
	// on the same target host. Walks the full transitive graph in
	// topo order so a deep chain (api -> postgres -> consul) deploys
	// bottom-up.
	//
	// Policy: all prerequisites land on the current target host. We
	// don't try to honour each dep's own preferred host — that's a
	// future slice. Homelab users deploy everything locally anyway,
	// and operators with specific placements can deploy deps manually
	// first.
	var depsDeployed []string
	if h.Dependencies != nil {
		order, terr := h.Dependencies.TopoOrder(r.Context(), name)
		if terr != nil {
			if errors.Is(terr, stacks.ErrDependencyCycle) {
				writeError(w, http.StatusUnprocessableEntity, terr.Error())
				return
			}
			writeError(w, http.StatusInternalServerError, "resolve dependencies: "+terr.Error())
			return
		}
		// Last element is name itself; everything before is a prerequisite.
		for _, depName := range order[:len(order)-1] {
			satisfied, sErr := h.stackRunning(r.Context(), target, depName)
			if sErr != nil {
				// Logging is enough — a status read failing on a dep
				// shouldn't abort a deploy the operator explicitly asked for.
				slog.Warn("dependency status check", "stack", depName, "err", sErr)
			}
			if satisfied {
				continue
			}
			depDetail, dErr := h.Stacks.Get(depName)
			if dErr != nil {
				// Operator declared a dep on a stack that isn't on disk yet.
				// Don't block the main deploy for a mis-declared edge;
				// surface it clearly instead.
				writeError(w, http.StatusFailedDependency,
					"dependency "+depName+" is declared but missing on disk: "+dErr.Error())
				return
			}
			if err := h.deployStackOnce(r, target, depName, depDetail); err != nil {
				writeError(w, http.StatusFailedDependency,
					"deploy dependency "+depName+": "+err.Error())
				return
			}
			depsDeployed = append(depsDeployed, depName)
		}
	}

	detail, err := h.Stacks.Get(name)
	if err != nil {
		writeStackError(w, err)
		return
	}
	// Resolve environment override (P.12.8). Query param wins; falls
	// back to .dockmesh.meta.json's active_environment; falls back to
	// no override. The empty-string fallback makes this a no-op for
	// stacks that don't use the overlay pattern at all.
	composeYAML := detail.Compose
	envOverride, mergedYAML, envErr := h.resolveEnvOverride(r, name, detail.Compose, detail.Env)
	if envErr != nil {
		writeError(w, http.StatusUnprocessableEntity, envErr.Error())
		return
	}
	if mergedYAML != "" {
		composeYAML = mergedYAML
	}

	mergedEnv := h.mergeGlobalEnv(r.Context(), detail.Env)
	res, err := target.DeployStack(r.Context(), name, composeYAML, mergedEnv)
	if err != nil {
		writeError(w, deployErrorStatus(err), friendlyDeployError(err))
		return
	}
	// Record the deployment association (P.7).
	if h.Deployments != nil {
		if err := h.Deployments.Set(r.Context(), name, target.ID(), "deployed"); err != nil {
			slog.Warn("set stack deployment", "stack", name, "host", target.ID(), "err", err)
		}
	}
	// Deploy history (P.12.6) — snapshot compose + resolved images per
	// service so operators can roll back to this exact point. Env is
	// deliberately not captured so secrets stay under the at-rest age
	// encryption the stacks manager owns.
	if h.DeployHistory != nil {
		services := make([]stacks.DeployHistoryService, 0, len(res.Services))
		for _, s := range res.Services {
			services = append(services, stacks.DeployHistoryService{Service: s.Name, Image: s.Image})
		}
		// History stores the MERGED compose (what was actually deployed)
		// so rollback can reproduce the deploy without needing the
		// override file to still be present at the same contents.
		note := ""
		if envOverride != "" {
			note = "env: " + envOverride
		}
		if _, err := h.DeployHistory.Record(r.Context(), name, target.ID(), composeYAML, note, middleware.UserID(r.Context()), services); err != nil {
			slog.Warn("record deploy history", "stack", name, "err", err)
		}
	}
	// Compose-file mirroring (P.7): push canonical files to the agent
	// so it retains a local copy for disaster recovery. Fire-and-forget
	// — a sync failure must not block the deploy response.
	h.syncStackToAgent(r.Context(), target.ID(), name, detail.Compose, detail.Env)
	h.audit(r, audit.ActionStackDeploy, name, map[string]any{
		"services":       len(res.Services),
		"host":           target.ID(),
		"deps_deployed": depsDeployed,
	})
	out := map[string]any{
		"stack":    res.Stack,
		"services": res.Services,
		"networks": res.Networks,
		"volumes":  res.Volumes,
	}
	if len(depsDeployed) > 0 {
		out["dependencies_deployed"] = depsDeployed
	}
	writeJSON(w, http.StatusOK, out)
}

// deployErrorStatus classifies a deploy failure into the right HTTP
// status so operators don't see a blanket 500 for problems they can
// fix (port in use, image pull auth, compose syntax). "Unknown" errors
// stay 500 because that's where server-side stack traces belong.
func deployErrorStatus(err error) int {
	if err == nil {
		return http.StatusInternalServerError
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "port is already allocated"),
		strings.Contains(msg, "address already in use"),
		strings.Contains(msg, "bind: permission denied"),
		strings.Contains(msg, "no such image"),
		strings.Contains(msg, "manifest unknown"),
		strings.Contains(msg, "pull access denied"),
		strings.Contains(msg, "unauthorized"),
		strings.Contains(msg, "yaml:"),
		strings.Contains(msg, "invalid compose"):
		return http.StatusUnprocessableEntity
	}
	return http.StatusInternalServerError
}

// friendlyDeployError translates gnarly docker / compose error strings
// into something a UI can display without making operators grep through
// an opaque stack trace. Falls back to the raw error when we have no
// friendlier mapping, so nothing is hidden — it's still diagnosable.
func friendlyDeployError(err error) string {
	raw := err.Error()
	lc := strings.ToLower(raw)
	switch {
	case strings.Contains(lc, "port is already allocated") ||
		strings.Contains(lc, "address already in use"):
		return "port already in use on the target host — pick a different host port (see compose.yaml ports:) or stop whatever is bound to it"
	case strings.Contains(lc, "pull access denied") || strings.Contains(lc, "manifest unknown"):
		return "could not pull image — check the tag exists and, for private registries, that credentials are configured: " + raw
	case strings.Contains(lc, "unauthorized"):
		return "registry authentication failed — configure credentials under Settings → Registries: " + raw
	case strings.Contains(lc, "yaml:") || strings.Contains(lc, "invalid compose"):
		return "compose file could not be parsed: " + raw
	}
	return raw
}

// stackRunning returns true when every service container for the
// named stack is in state=running on the given host. "Every" covers
// the realistic homelab case where a stack with no services (not yet
// deployed) returns zero rows — that's NOT running. P.12.7.
func (h *Handlers) stackRunning(ctx context.Context, target interface {
	StackStatus(context.Context, string) ([]compose.StatusEntry, error)
}, stackName string) (bool, error) {
	status, err := target.StackStatus(ctx, stackName)
	if err != nil {
		return false, err
	}
	if len(status) == 0 {
		return false, nil
	}
	for _, s := range status {
		if s.State != "running" {
			return false, nil
		}
	}
	return true, nil
}

// deployStackOnce is the single-stack slice of DeployStack, factored
// out so the dependency-resolution path can reuse it without the
// audit / history / sync side-effects for every transitive dep (those
// still run via a direct DeployStack call later if the operator
// deploys the dep explicitly). Dependency-driven deploys get one
// audit entry at the end under the main stack, not one per dep. P.12.7.
func (h *Handlers) deployStackOnce(r *http.Request, target host.Host, name string, detail *stacks.Detail) error {
	mergedEnv := h.mergeGlobalEnv(r.Context(), detail.Env)
	res, err := target.DeployStack(r.Context(), name, detail.Compose, mergedEnv)
	if err != nil {
		return err
	}
	if h.Deployments != nil {
		_ = h.Deployments.Set(r.Context(), name, target.ID(), "deployed")
	}
	if h.DeployHistory != nil {
		services := make([]stacks.DeployHistoryService, 0, len(res.Services))
		for _, s := range res.Services {
			services = append(services, stacks.DeployHistoryService{Service: s.Name, Image: s.Image})
		}
		_, _ = h.DeployHistory.Record(r.Context(), name, target.ID(), detail.Compose,
			"auto-deployed as dependency", middleware.UserID(r.Context()), services)
	}
	h.syncStackToAgent(r.Context(), target.ID(), name, detail.Compose, detail.Env)
	return nil
}

func (h *Handlers) StopStack(w http.ResponseWriter, r *http.Request) {
	target, err := h.pickHost(r)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	}
	if !h.requireHostAccess(w, r, target.ID()) {
		return
	}
	name := chi.URLParam(r, "name")
	if err := target.StopStack(r.Context(), name); err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	// Mark as stopped but keep the row so we remember which host it was on.
	if h.Deployments != nil {
		if err := h.Deployments.Set(r.Context(), name, target.ID(), "stopped"); err != nil {
			slog.Warn("set stack deployment stopped", "stack", name, "err", err)
		}
	}
	h.audit(r, audit.ActionStackStop, name, map[string]string{"host": target.ID()})
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handlers) StackStatus(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, status)
}

// syncStackToAgent pushes the compose+env to the agent for local
// caching. No-op for local or when agent is unavailable.
func (h *Handlers) syncStackToAgent(ctx context.Context, hostID, name, compose, env string) {
	if hostID == "" || hostID == "local" || h.Agents == nil {
		return
	}
	ag := h.Agents.GetConnected(hostID)
	if ag == nil {
		return
	}
	// Read optional .dockmesh.meta.json from the stack dir.
	var meta string
	if dir, err := h.Stacks.Dir(name); err == nil {
		if b, err := os.ReadFile(filepath.Join(dir, ".dockmesh.meta.json")); err == nil {
			meta = string(b)
		}
	}
	req := agents.StackSyncReq{Name: name, Compose: compose, Env: env, Meta: meta}
	go func() {
		if _, err := ag.Request(ctx, agents.Frame{
			Type:    agents.FrameReqStackSync,
			Payload: mustJSON(req),
		}); err != nil {
			slog.Warn("stack sync to agent", "stack", name, "agent", hostID, "err", err)
		}
	}()
}

// deleteStackFromAgent tells the agent to remove its local copy.
func (h *Handlers) deleteStackFromAgent(ctx context.Context, hostID, name string) {
	if hostID == "" || hostID == "local" || h.Agents == nil {
		return
	}
	ag := h.Agents.GetConnected(hostID)
	if ag == nil {
		return
	}
	req := agents.StackNameReq{Name: name}
	go func() {
		if _, err := ag.Request(ctx, agents.Frame{
			Type:    agents.FrameReqStackDelete,
			Payload: mustJSON(req),
		}); err != nil {
			slog.Warn("stack delete from agent", "stack", name, "agent", hostID, "err", err)
		}
	}()
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func writeStackError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, stacks.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, stacks.ErrExists):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, stacks.ErrInvalidName),
		errors.Is(err, stacks.ErrReserved),
		errors.Is(err, stacks.ErrPathEscape):
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}
