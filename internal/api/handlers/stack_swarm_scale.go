package handlers

import (
	"context"
	"errors"
	"math"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/scaling"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

type swarmScaleResponse struct {
	compose.ScaleResult
	ClusterID string `json:"cluster_id"`
	Manager   string `json:"manager"`
	// FileReplicas is set when the compose file sets deploy.replicas: the
	// next deploy sets the service back to it.
	FileReplicas *uint64 `json:"file_replicas,omitempty"`
}

// scaleOnCluster answers a scale of a service of a stack deployed to a
// Swarm cluster and reports whether the stack is one. The host-stack
// guards for container_name and host ports don't apply to a Swarm
// service; the 0..100 limit, the stateful warning and the migration
// guard do.
func (h *Handlers) scaleOnCluster(w http.ResponseWriter, r *http.Request) bool {
	if h.Deployments == nil || h.Clusters == nil {
		return false
	}
	name, service := chi.URLParam(r, "name"), chi.URLParam(r, "service")
	d, err := h.Deployments.Get(r.Context(), name)
	if err != nil || d == nil || d.TargetKind != stacks.TargetCluster {
		return false
	}
	full := name + "_" + service
	if !agents.ValidStackName(name) || !agents.ValidObjectName(full) {
		writeError(w, http.StatusBadRequest, "invalid service name")
		return true
	}
	var req scaleRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body")
		return true
	}
	if req.Replicas < 0 || req.Replicas > agents.MaxSwarmReplicas {
		writeError(w, http.StatusBadRequest, "replicas must be between 0 and 100")
		return true
	}
	targetRelease, err := h.StackLocks.TryLock(stacklock.TargetKey(name), stacklock.OpScale)
	if err != nil {
		writeStackBusy(w, err)
		return true
	}
	defer targetRelease()
	if d, err = h.Deployments.Get(r.Context(), name); err != nil || d == nil || d.TargetKind != stacks.TargetCluster {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "stack " + name + " is not deployed to a Swarm cluster", "code": "stack_not_on_cluster"})
		return true
	}
	clusterID := d.TargetID
	res := h.stackTargetReq(stacks.Target{Kind: stacks.TargetCluster, ID: clusterID}, name)
	if !middleware.AllowedOn(r.Context(), rbac.PermStacksDeploy, res) {
		h.writeScopeDenied(w, r, rbac.PermStacksDeploy, res, "stack "+name)
		return true
	}
	release, err := h.StackLocks.TryLock(stacklock.ClusterKey(clusterID, name), stacklock.OpScale)
	if err != nil {
		writeStackBusy(w, err)
		return true
	}
	defer release()
	if h.refuseMigrating(w, r, name) {
		return true
	}
	snap, ok := h.checkStack(w, r, name)
	if !ok {
		return true
	}
	b, err := h.buildSwarm(r.Context(), name, snap, "")
	if err != nil {
		writeSwarmBuildError(w, err)
		return true
	}
	var file *agents.SwarmServiceWrite
	for i := range b.req.Services {
		if b.req.Services[i].Spec.Name == full {
			file = &b.req.Services[i]
			break
		}
	}
	reader, err := h.Clusters.ReadHost(r.Context(), clusterID)
	if err != nil {
		writeClusterReadError(w, err)
		return true
	}
	list, err := h.swarm().SwarmServices(r.Context(), reader)
	if err != nil {
		writeClusterReadError(w, err)
		return true
	}
	var svc *agents.SwarmService
	for i := range list.Services {
		if s := list.Services[i]; s.Name == full && s.Stack == name && s.SpecHash != "" {
			svc = &list.Services[i]
			break
		}
	}
	if svc == nil {
		writeError(w, http.StatusNotFound, "no service "+service+" of stack "+name+" on the cluster")
		return true
	}
	if file == nil {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "service " + service + " is not in the stack's file; deploy the stack first", "code": "swarm_service_not_in_file",
		})
		return true
	}
	switch svc.UpdateState {
	case "updating", "paused", "rollback_started", "rollback_paused":
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "the update of service " + service + " is not finished; resume, roll back or deploy first", "code": "swarm_update_unfinished",
		})
		return true
	}
	if svc.Mode != "replicated" {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "service " + service + " runs in " + svc.Mode + " mode; only replicated services are scaled", "code": "swarm_not_replicated",
		})
		return true
	}
	if req.Replicas > 1 && !req.Force {
		if img, stateful := compose.StatefulImage(file.Spec.TaskTemplate.ContainerSpec.Image); stateful && len(file.Spec.TaskTemplate.ContainerSpec.Mounts) > 0 {
			writeJSON(w, http.StatusConflict, map[string]any{
				"warning":      "stateful_service",
				"message":      "Service " + service + " looks like a database (" + img + ") with mounted volumes. Scaling may cause data corruption.",
				"force_needed": true,
			})
			return true
		}
	}
	managers, err := h.Clusters.WriteHosts(r.Context(), clusterID)
	if err != nil {
		writeSwarmWriteError(w, err)
		return true
	}
	out := swarmScaleResponse{ClusterID: clusterID}
	if file.Spec.Mode.Replicated != nil {
		out.FileReplicas = file.Spec.Mode.Replicated.Replicas
	}
	prev := 0
	if svc.Replicas != nil && *svc.Replicas <= math.MaxInt32 {
		prev = int(*svc.Replicas)
	}
	wctx := context.WithoutCancel(r.Context())
	target := &scaling.ClusterTarget{ClusterID: clusterID, Stack: name, Service: service,
		Apply: func(ctx context.Context, replicas int) (int, error) {
			n := uint64(max(replicas, 0))
			areq := agents.SwarmServiceActionReq{Stack: name, Service: full, ID: svc.ID, Action: agents.SwarmServiceScale, Replicas: &n}
			var err error
			for _, m := range managers {
				hostRelease, lerr := h.StackLocks.TryLock(stacklock.HostKey(m, name), stacklock.OpScale)
				if lerr != nil {
					err = lerr
					continue
				}
				out.Manager = m
				var res agents.SwarmWriteResult
				res, err = h.swarmService().SwarmServiceAction(ctx, m, areq)
				hostRelease()
				if err == nil && res.Action != agents.SwarmActionUpdated {
					err = errors.New(res.Error)
				}
				if !writeNotSent(err) {
					break
				}
			}
			return prev, err
		}}
	result, err := target.Scale(wctx, "", "", req.Replicas)
	if err != nil {
		h.auditDetached(r, audit.ActionStackDeploy, name, map[string]any{
			"action": "scale", "service": service, "replicas": req.Replicas, "cluster": clusterID, "manager": out.Manager, "error": err.Error(),
		})
		if writeStackBusy(w, err) {
			return true
		}
		if errors.Is(err, host.ErrSwarmWriteUnknown) {
			writeError(w, http.StatusBadGateway, err.Error())
			return true
		}
		writeSwarmWriteError(w, err)
		return true
	}
	out.ScaleResult = *result
	h.auditDetached(r, audit.ActionStackDeploy, name, map[string]any{
		"action": "scale", "service": service, "previous": result.Previous, "current": result.Current,
		"cluster": clusterID, "manager": out.Manager,
	})
	writeJSON(w, http.StatusOK, out)
	return true
}
