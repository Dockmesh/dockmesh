package handlers

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/swarm"

	"github.com/dockmesh/dockmesh/internal/clusters"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/swarmconvert"
)

type swarmPreviewResponse struct {
	Stack       string `json:"stack"`
	ClusterID   string `json:"cluster_id"`
	Environment string `json:"environment,omitempty"`
	Deployable  bool   `json:"deployable"`
	// PlanHash names what a deploy would write and prune; the deploy
	// needs it as the confirmation. Empty when the stack is not
	// deployable or the cluster can't be read.
	PlanHash string `json:"plan_hash,omitempty"`
	// Prune lists what the cluster runs for the stack that the file no
	// longer has; a deploy removes it only when asked to.
	Prune            *swarmPrune             `json:"prune,omitempty"`
	PruneUnknown     string                  `json:"prune_unknown,omitempty"`
	Refusals         []swarmconvert.Finding  `json:"refusals"`
	Warnings         []swarmconvert.Finding  `json:"warnings"`
	Services         []swarm.ServiceSpec     `json:"services"`
	Networks         []network.CreateRequest `json:"networks"`
	ExternalNetworks []string                `json:"external_networks"`
	ModifiedOutside  []string                `json:"modified_outside"`
}

// SwarmPreview converts a stack's files into the Swarm specs a deploy to
// cluster would send and lists what it refuses and warns about. Nothing
// is written. It builds from the same snapshot bytes and global env the
// deploy uses. The specs carry the stack's env values, so the caller needs
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
		slog.Error("swarm preview: load cluster", "cluster", id, "err", err)
		writeError(w, http.StatusInternalServerError, "load cluster failed")
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
	b, err := h.buildSwarm(r.Context(), name, snap, r.URL.Query().Get("environment"))
	if err != nil {
		writeSwarmBuildError(w, err)
		return
	}
	plan := b.plan
	out := swarmPreviewResponse{
		Stack:            name,
		ClusterID:        id,
		Environment:      b.env,
		Deployable:       len(plan.Refusals) == 0,
		Refusals:         nonNil(plan.Refusals),
		Warnings:         nonNil(plan.Warnings),
		Services:         []swarm.ServiceSpec{},
		Networks:         []network.CreateRequest{},
		ExternalNetworks: []string{},
		ModifiedOutside:  nonNil(snap.Changed),
	}
	if out.Deployable {
		if prune, err := h.pruneCandidates(r.Context(), id, b.req); err != nil {
			out.PruneUnknown = errPruneUnknown.Error()
		} else {
			out.Prune, out.PlanHash = &prune, planHash(b.req, prune)
		}
		for _, s := range b.req.Services {
			out.Services = append(out.Services, s.Spec)
		}
		out.Networks = nonNil(b.req.Networks)
		out.ExternalNetworks = nonNil(b.req.ExternalNetworks)
	} else if plan.Result != nil {
		out.Services = nonNil(plan.Result.Services)
		out.Networks = nonNil(plan.Result.Networks)
		out.ExternalNetworks = nonNil(plan.Result.ExternalNetworks)
	}
	writeJSON(w, http.StatusOK, out)
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}
