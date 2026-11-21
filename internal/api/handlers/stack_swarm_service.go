package handlers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
)

type swarmServiceOps interface {
	SwarmServiceAction(ctx context.Context, hostID string, req agents.SwarmServiceActionReq) (agents.SwarmWriteResult, error)
}

func (h *Handlers) swarmService() swarmServiceOps {
	if a, ok := h.swarm().(swarmServiceOps); ok {
		return a
	}
	return h.Hosts
}

// While Swarm rolls out an update, the stack's cluster lock stays held, so
// no deploy, rollback or resume starts on top of it. A paused update frees
// it, otherwise the actions that resolve it would be blocked.
var (
	swarmUpdatePoll = 5 * time.Second
	swarmUpdateMax  = 30 * time.Minute
)

func rollingOut(state string) bool {
	return state == "updating" || state == "rollback_started"
}

// stackRollingOut reports whether a service of stack is mid-update.
func (h *Handlers) stackRollingOut(ctx context.Context, clusterID, stack string) (bool, error) {
	reader, err := h.Clusters.ReadHost(ctx, clusterID)
	if err != nil {
		return false, err
	}
	list, err := h.swarm().SwarmServices(ctx, reader)
	if err != nil {
		return false, err
	}
	for _, s := range list.Services {
		if s.Stack == stack && rollingOut(s.UpdateState) {
			return true, nil
		}
	}
	return false, nil
}

// swarmUpdateGrace is how long a write's lock is kept while Swarm has not
// reported the update it starts yet.
var swarmUpdateGrace = 3 * time.Second

// holdWhileRollingOut takes over release, the lock of a request that wrote
// to the stack's services, and keeps the stack locked for as long as
// Swarm updates one of them, at most swarmUpdateMax. A rollback cancels
// the hold.
func (h *Handlers) holdWhileRollingOut(ctx context.Context, clusterID, stack string, release func()) {
	poll, limit, grace := swarmUpdatePoll, swarmUpdateMax, swarmUpdateGrace
	if h.swarmNoGrace {
		grace = 0
	}
	key := stacklock.ClusterKey(clusterID, stack)
	hold, cancel := context.WithTimeout(context.WithoutCancel(ctx), limit)
	check := func() bool {
		c, done := context.WithTimeout(hold, 5*time.Second)
		defer done()
		busy, err := h.stackRollingOut(c, clusterID, stack)
		return err == nil && busy
	}
	busy := check()
	if !busy && grace <= 0 {
		cancel()
		release()
		return
	}
	entry := &swarmHold{cancel: cancel}
	h.swarmHolds.Store(key, entry)
	go func() {
		defer cancel()
		defer h.swarmHolds.CompareAndDelete(key, entry)
		for start := time.Now(); !busy; busy = check() {
			if time.Since(start) >= grace || hold.Err() != nil {
				release()
				return
			}
			time.Sleep(time.Second)
		}
		release()
		if hold.Err() != nil {
			return
		}
		held, err := h.StackLocks.TryLock(key, stacklock.OpSwarmUpdate)
		if err != nil {
			return
		}
		defer held()
		for {
			select {
			case <-hold.Done():
				if errors.Is(hold.Err(), context.DeadlineExceeded) {
					slog.Warn("swarm update still rolling out; freeing the stack's lock", "stack", stack, "cluster", clusterID, "after", limit)
				}
				return
			case <-time.After(poll):
			}
			if !check() {
				return
			}
		}
	}()
}

type swarmHold struct{ cancel context.CancelFunc }

// endSwarmHold stops a hold on the stack's cluster lock and waits up to a
// few seconds for the lock to come free.
func (h *Handlers) endSwarmHold(clusterID, stack, op string) (func(), bool, error) {
	key := stacklock.ClusterKey(clusterID, stack)
	v, ended := h.swarmHolds.Load(key)
	if ended {
		v.(*swarmHold).cancel()
	}
	var err error
	for range 20 {
		var release func()
		if release, err = h.StackLocks.TryLock(key, op); err == nil {
			return release, ended, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return nil, ended, err
}

// SwarmServiceRollback rolls a service of a stack on a cluster back to its
// previous spec, Swarm's own rollback.
//
//	POST /api/v1/stacks/{name}/swarm-services/{service}/rollback
func (h *Handlers) SwarmServiceRollback(w http.ResponseWriter, r *http.Request) {
	h.swarmServiceAction(w, r, agents.SwarmServiceRollback, rbac.PermStacksOperate, audit.ActionSwarmServiceRollback)
}

// SwarmServiceResume continues a paused update of a service of a stack on
// a cluster.
//
//	POST /api/v1/stacks/{name}/swarm-services/{service}/resume
func (h *Handlers) SwarmServiceResume(w http.ResponseWriter, r *http.Request) {
	h.swarmServiceAction(w, r, agents.SwarmServiceResume, rbac.PermStacksDeploy, audit.ActionSwarmServiceResume)
}

type swarmServiceActionResponse struct {
	Stack     string `json:"stack"`
	Service   string `json:"service"`
	ClusterID string `json:"cluster_id"`
	Manager   string `json:"manager"`
	agents.SwarmWriteResult
}

func (h *Handlers) swarmServiceAction(w http.ResponseWriter, r *http.Request, action string, perm rbac.Perm, auditAction string) {
	if h.Clusters == nil || h.Deployments == nil || h.Hosts == nil && h.SwarmOps == nil {
		writeError(w, http.StatusServiceUnavailable, "clusters are not available")
		return
	}
	name, service := chi.URLParam(r, "name"), chi.URLParam(r, "service")
	full := name + "_" + service
	if !agents.ValidStackName(name) || !agents.ValidObjectName(full) {
		writeError(w, http.StatusBadRequest, "invalid service name")
		return
	}
	targetRelease, err := h.StackLocks.TryLock(stacklock.TargetKey(name), stacklock.OpDeploy)
	if err != nil {
		writeStackBusy(w, err)
		return
	}
	defer targetRelease()
	d, err := h.Deployments.Get(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load stack deployment failed")
		return
	}
	if d == nil || d.TargetKind != stacks.TargetCluster {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "stack " + name + " is not deployed to a Swarm cluster", "code": "stack_not_on_cluster"})
		return
	}
	clusterID := d.TargetID
	res := h.stackTargetReq(stacks.Target{Kind: stacks.TargetCluster, ID: clusterID}, name)
	if !middleware.AllowedOn(r.Context(), perm, res) {
		h.writeScopeDenied(w, r, perm, res, "stack "+name)
		return
	}
	// A rollback is what resolves a bad rollout, so it ends the hold an
	// update in progress keeps on the stack; resume and deploy wait.
	var (
		release func()
		ended   bool
	)
	if action == agents.SwarmServiceRollback {
		release, ended, err = h.endSwarmHold(clusterID, name, stacklock.OpDeploy)
	} else {
		release, err = h.StackLocks.TryLock(stacklock.ClusterKey(clusterID, name), stacklock.OpDeploy)
	}
	if err != nil {
		writeStackBusy(w, err)
		return
	}
	wrote := false
	// A rollback that ended a hold and then wrote nothing hands the lock
	// back, so the rollout stays held.
	defer func(ctx context.Context) {
		if wrote || ended {
			h.holdWhileRollingOut(ctx, clusterID, name, release)
		} else {
			release()
		}
	}(r.Context())
	if h.refuseMigrating(w, r, name) {
		return
	}
	reader, err := h.Clusters.ReadHost(r.Context(), clusterID)
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	list, err := h.swarm().SwarmServices(r.Context(), reader)
	if err != nil {
		writeClusterReadError(w, err)
		return
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
		return
	}
	managers, err := h.Clusters.WriteHosts(r.Context(), clusterID)
	if err != nil {
		writeSwarmWriteError(w, err)
		return
	}
	req := agents.SwarmServiceActionReq{Stack: name, Service: full, ID: svc.ID, Action: action}
	wctx := context.WithoutCancel(r.Context())
	var (
		out     agents.SwarmWriteResult
		manager string
	)
	for _, m := range managers {
		hostRelease, lerr := h.StackLocks.TryLock(stacklock.HostKey(m, name), stacklock.OpDeploy)
		if lerr != nil {
			err = lerr
			continue
		}
		manager = m
		out, err = h.swarmService().SwarmServiceAction(wctx, m, req)
		hostRelease()
		if !writeNotSent(err) {
			break
		}
	}
	unknown := errors.Is(err, host.ErrSwarmWriteUnknown)
	if errors.Is(err, agents.ErrSwarmWriteRefused) {
		h.auditDetached(r, auditAction, full, map[string]any{
			"stack": name, "cluster": clusterID, "manager": manager, "id": svc.ID, "result": "refused", "error": err.Error(),
		})
	}
	if err != nil && !unknown {
		if writeStackBusy(w, err) {
			return
		}
		writeSwarmWriteError(w, err)
		return
	}
	if unknown {
		out = agents.SwarmWriteResult{Name: full, ID: svc.ID, Action: agents.SwarmActionUnknown, Error: err.Error()}
	}
	wrote = true
	h.auditDetached(r, auditAction, full, map[string]any{
		"stack": name, "cluster": clusterID, "manager": manager, "id": svc.ID, "result": out.Action, "error": out.Error,
		"update_state": svc.UpdateState,
	})
	status := http.StatusOK
	if out.Action != agents.SwarmActionUpdated {
		status = http.StatusBadGateway
	}
	writeJSON(w, status, swarmServiceActionResponse{Stack: name, Service: service, ClusterID: clusterID, Manager: manager, SwarmWriteResult: out})
}
