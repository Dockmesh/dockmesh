package handlers

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/clusters"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
)

// ListClusters returns the clusters the caller holds hosts.view on (a
// global or cluster scope) with their linked hosts. A host scope on a
// node never shows its cluster.
//
//	GET /api/v1/clusters
func (h *Handlers) ListClusters(w http.ResponseWriter, r *http.Request) {
	if h.Clusters == nil {
		writeJSON(w, http.StatusOK, []clusters.Cluster{})
		return
	}
	list, err := h.Clusters.List(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	access := middleware.AccessOf(r.Context())
	out := []clusters.Cluster{}
	for _, c := range list {
		if access.AllowsOn(rbac.PermHostsView, rbac.Resource{ClusterID: c.ID}) {
			out = append(out, c)
		}
	}
	writeJSON(w, http.StatusOK, out)
}

// ListClusterCandidates lists Swarms that have a manager host but no
// cluster yet.
//
//	GET /api/v1/clusters/candidates
func (h *Handlers) ListClusterCandidates(w http.ResponseWriter, r *http.Request) {
	if h.Clusters == nil {
		writeJSON(w, http.StatusOK, []clusters.Candidate{})
		return
	}
	list, err := h.Clusters.Candidates(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, list)
}

type createClusterRequest struct {
	HostID string `json:"host_id"`
	Name   string `json:"name"`
}

// CreateCluster makes the Swarm that host_id manages a cluster.
//
//	POST /api/v1/clusters
func (h *Handlers) CreateCluster(w http.ResponseWriter, r *http.Request) {
	if h.Clusters == nil {
		writeError(w, http.StatusServiceUnavailable, "clusters are not available")
		return
	}
	var req createClusterRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return
	}
	req.HostID = strings.TrimSpace(req.HostID)
	req.Name = strings.TrimSpace(req.Name)
	if req.HostID == "" {
		writeError(w, http.StatusBadRequest, "host_id is required")
		return
	}
	c, err := h.Clusters.Create(r.Context(), req.HostID, req.Name, actorID(r))
	switch {
	case err == nil:
	case errors.Is(err, clusters.ErrExists), errors.Is(err, clusters.ErrNameTaken), errors.Is(err, clusters.ErrConflict):
		writeError(w, http.StatusConflict, err.Error())
		return
	case errors.Is(err, clusters.ErrBadName), errors.Is(err, clusters.ErrNotManager):
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, host.ErrUnknownHost), errors.Is(err, agents.ErrNotFound):
		writeError(w, http.StatusNotFound, "host not found")
		return
	case errors.Is(err, host.ErrAgentOffline), errors.Is(err, host.ErrNoDocker), errors.Is(err, host.ErrAgentNoSwarmNodes):
		writeError(w, http.StatusServiceUnavailable, err.Error())
		return
	default:
		slog.Warn("create cluster", "host", req.HostID, "err", err)
		writeError(w, http.StatusBadGateway, "cannot read the host's Swarm state")
		return
	}
	h.audit(r, audit.ActionClusterCreate, c.ID, map[string]any{"name": c.Name, "host": req.HostID, "members": len(c.Members)})
	writeJSON(w, http.StatusCreated, c)
}

// DeleteCluster removes a cluster and its memberships; the Swarm itself
// is not touched.
//
//	DELETE /api/v1/clusters/{id}
func (h *Handlers) DeleteCluster(w http.ResponseWriter, r *http.Request) {
	if h.Clusters == nil {
		writeError(w, http.StatusServiceUnavailable, "clusters are not available")
		return
	}
	id := chi.URLParam(r, "id")
	c, err := h.Clusters.Delete(r.Context(), id)
	if errors.Is(err, clusters.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hosts := make([]string, 0, len(c.Members))
	for _, m := range c.Members {
		hosts = append(hosts, m.HostID)
	}
	h.audit(r, audit.ActionClusterDelete, id, map[string]any{"name": c.Name, "origin": c.OriginHost, "members": hosts})
	w.WriteHeader(http.StatusNoContent)
}
