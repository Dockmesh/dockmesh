package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/go-chi/chi/v5"
)

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
	// Always read the canonical compose+env from the central server's
	// filesystem (where stacks live). The host abstraction takes the
	// content as parameters so local + remote share the same call shape.
	detail, err := h.Stacks.Get(name)
	if err != nil {
		writeStackError(w, err)
		return
	}
	res, err := target.DeployStack(r.Context(), name, detail.Compose, detail.Env)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
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
		if _, err := h.DeployHistory.Record(r.Context(), name, target.ID(), detail.Compose, "", middleware.UserID(r.Context()), services); err != nil {
			slog.Warn("record deploy history", "stack", name, "err", err)
		}
	}
	// Compose-file mirroring (P.7): push canonical files to the agent
	// so it retains a local copy for disaster recovery. Fire-and-forget
	// — a sync failure must not block the deploy response.
	h.syncStackToAgent(r.Context(), target.ID(), name, detail.Compose, detail.Env)
	h.audit(r, audit.ActionStackDeploy, name, map[string]any{
		"services": len(res.Services),
		"host":     target.ID(),
	})
	writeJSON(w, http.StatusOK, res)
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
