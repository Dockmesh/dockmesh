package handlers

import (
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"

	"github.com/dockmesh/dockmesh/internal/clusters"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/swarmconvert"
)

type swarmPreviewResponse struct {
	Stack            string                  `json:"stack"`
	ClusterID        string                  `json:"cluster_id"`
	Environment      string                  `json:"environment,omitempty"`
	Deployable       bool                    `json:"deployable"`
	Refusals         []swarmconvert.Finding  `json:"refusals"`
	Warnings         []swarmconvert.Finding  `json:"warnings"`
	Services         []swarm.ServiceSpec     `json:"services"`
	Networks         []network.CreateRequest `json:"networks"`
	ExternalNetworks []string                `json:"external_networks"`
	ModifiedOutside  []string                `json:"modified_outside"`
}

// SwarmPreview converts a stack's files into the Swarm specs a deploy to
// cluster would send and lists what it refuses and warns about. Nothing
// is written. The specs carry the stack's env values, so the caller needs
// deploy rights on the stack on that cluster and the right to read its
// env where it is now.
//
//	GET /api/v1/stacks/{name}/swarm-preview?cluster=<id>[&environment=<env>]
func (h *Handlers) SwarmPreview(w http.ResponseWriter, r *http.Request) {
	if h.Clusters == nil {
		writeError(w, http.StatusServiceUnavailable, "clusters are not available")
		return
	}
	name := chi.URLParam(r, "name")
	id := r.URL.Query().Get("cluster")
	if id == "" {
		writeError(w, http.StatusBadRequest, "cluster is required")
		return
	}
	target := h.stackTargetReq(stacks.Target{Kind: stacks.TargetCluster, ID: id}, name)
	if !h.allowedOn(r, target) {
		h.writeScopeDenied(w, r, rbac.PermStacksDeploy, target, "stack "+name)
		return
	}
	if !h.canReadStackEnv(r, name) {
		h.writeScopeDenied(w, r, rbac.PermContainersEnv, h.stackRes(r, name), "env of stack "+name)
		return
	}
	if _, err := h.Clusters.Get(r.Context(), id); errors.Is(err, clusters.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, "load cluster: "+err.Error())
		return
	}
	if !h.Stacks.Has(name) {
		writeStackError(w, stacks.ErrNotFound)
		return
	}
	snap, err := h.Stacks.Snapshot(name)
	if err != nil {
		writeStackError(w, err)
		return
	}
	dir, err := h.Stacks.Dir(name)
	if err != nil {
		writeStackError(w, err)
		return
	}
	env := r.URL.Query().Get("environment")
	if env == "" {
		env = snap.ActiveOverride
	}
	project, _, err := compose.MergeEnvironment(r.Context(), dir, name, snap.Detail.Env, env, false)
	if err != nil {
		writeComposeLoadError(w, err)
		return
	}
	unresolved, _, err := compose.MergeEnvironment(r.Context(), dir, name, snap.Detail.Env, env, true)
	if err != nil {
		writeComposeLoadError(w, err)
		return
	}
	plan := swarmconvert.NewPlan(project, unresolved, name)
	out := swarmPreviewResponse{
		Stack:            name,
		ClusterID:        id,
		Environment:      env,
		Deployable:       len(plan.Refusals) == 0,
		Refusals:         nonNil(plan.Refusals),
		Warnings:         nonNil(plan.Warnings),
		Services:         []swarm.ServiceSpec{},
		Networks:         []network.CreateRequest{},
		ExternalNetworks: []string{},
		ModifiedOutside:  nonNil(snap.Changed),
	}
	if plan.Result != nil {
		out.Services = nonNil(plan.Result.Services)
		out.Networks = nonNil(plan.Result.Networks)
		out.ExternalNetworks = nonNil(plan.Result.ExternalNetworks)
	}
	writeJSON(w, http.StatusOK, out)
}

func writeComposeLoadError(w http.ResponseWriter, err error) {
	if errors.Is(err, compose.ErrEnvironmentNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	}
	writeError(w, http.StatusUnprocessableEntity, err.Error())
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
