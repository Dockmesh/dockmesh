package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/api/middleware"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/clusters"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/swarmconvert"
)

// swarmAdoptOps is the part of the host registry adopting a stack uses.
type swarmAdoptOps interface {
	SwarmExport(ctx context.Context, hostID string, req agents.SwarmExportReq) (agents.SwarmExportResp, error)
	SwarmAdopt(ctx context.Context, hostID string, req agents.SwarmAdoptReq) (agents.SwarmAdoptResp, error)
}

func (h *Handlers) swarmAdopt() swarmAdoptOps {
	if a, ok := h.swarm().(swarmAdoptOps); ok {
		return a
	}
	return h.Hosts
}

type adoptServiceView struct {
	Name string `json:"name"`
	ID   string `json:"id"`
	// Changes are the fields in which a deploy from the adopted file
	// would change the running service.
	Changes []string `json:"changes"`
}

type adoptPreview struct {
	Stack     string `json:"stack"`
	ClusterID string `json:"cluster_id"`
	Manager   string `json:"manager"`
	Compose   string `json:"compose"`
	// EnvKeys are the variables the stack's environment file gets; the
	// values are never returned.
	EnvKeys  []string           `json:"env_keys"`
	Services []adoptServiceView `json:"services"`
	// NotCarried lists what the running services have that the file
	// can't hold; Refusals what a deploy from the file would refuse.
	NotCarried   []swarmconvert.Finding `json:"not_carried"`
	Refusals     []swarmconvert.Finding `json:"refusals"`
	Warnings     []swarmconvert.Finding `json:"warnings"`
	KeptNetworks []string               `json:"kept_networks"`
	PlanHash     string                 `json:"plan_hash"`
}

type adoptBuild struct {
	view    adoptPreview
	adopted *swarmconvert.Adopted
	labels  agents.SwarmAdoptReq
}

var errAdoptNameTaken = errors.New("a Dockmesh stack with this name exists")

// buildAdopt reads stack through a verified manager and writes it down as
// a compose file, then converts that file back the way a deploy would and
// names every difference to what runs.
func (h *Handlers) buildAdopt(ctx context.Context, clusterID, stack string) (*adoptBuild, error) {
	managers, err := h.Clusters.WriteHosts(ctx, clusterID)
	if err != nil {
		return nil, err
	}
	var (
		exp     agents.SwarmExportResp
		manager string
	)
	for _, m := range managers {
		manager = m
		exp, err = h.swarmAdopt().SwarmExport(ctx, m, agents.SwarmExportReq{Stack: stack})
		if !writeNotSent(err) {
			break
		}
	}
	if err != nil {
		return nil, err
	}
	a, err := swarmconvert.AdoptStack(stack, exp.Services, exp.Networks)
	if err != nil {
		return nil, err
	}
	snap := &stacks.Snapshot{Name: stack, Detail: &stacks.Detail{Name: stack, Compose: a.Compose, Env: a.Env}}
	b, err := h.buildSwarm(ctx, stack, snap, "")
	if err != nil {
		return nil, err
	}
	netNames := map[string]string{}
	for _, n := range exp.Networks {
		netNames[n.ID] = n.Name
	}
	built := map[string]agents.SwarmServiceWrite{}
	for _, s := range b.req.Services {
		built[s.Spec.Name] = s
	}
	out := &adoptBuild{adopted: a, labels: agents.SwarmAdoptReq{Stack: stack}}
	out.view = adoptPreview{
		Stack: stack, ClusterID: clusterID, Manager: manager, Compose: a.Compose,
		EnvKeys: nonNil(a.EnvKeys), NotCarried: nonNil(a.Findings), Refusals: nonNil(b.plan.Refusals),
		Warnings: nonNil(b.plan.Warnings), KeptNetworks: nonNil(a.KeptNetworks),
		Services: []adoptServiceView{},
	}
	type versioned struct {
		ID      string `json:"id"`
		Version uint64 `json:"version"`
	}
	var seen []versioned
	for _, s := range exp.Services {
		v := adoptServiceView{Name: s.Spec.Name, ID: s.ID, Changes: []string{}}
		seen = append(seen, versioned{s.ID, s.Version.Index})
		if w, ok := built[s.Spec.Name]; ok {
			v.Changes = nonNil(swarmconvert.Diff(s.Spec, w.Spec, netNames))
			out.labels.Services = append(out.labels.Services, agents.SwarmAdoptService{
				ID: s.ID, Name: s.Spec.Name, Version: s.Version.Index, SpecHash: w.Spec.Labels[agents.LabelSpecHash],
			})
		}
		out.view.Services = append(out.view.Services, v)
	}
	raw, err := json.Marshal(struct {
		Compose  string      `json:"compose"`
		Env      string      `json:"env"`
		Services []versioned `json:"services"`
	}{a.Compose, a.Env, seen})
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	out.view.PlanHash = hex.EncodeToString(sum[:])
	return out, nil
}

// adoptAllowed answers the checks preview and adopt share: a valid,
// free stack name, stacks.adopt on the stack on the cluster, the cluster.
func (h *Handlers) adoptAllowed(w http.ResponseWriter, r *http.Request) (clusterID, stack string, ok bool) {
	if h.Clusters == nil || h.Stacks == nil || h.Deployments == nil || h.Hosts == nil && h.SwarmOps == nil {
		writeError(w, http.StatusServiceUnavailable, "clusters are not available")
		return "", "", false
	}
	clusterID, stack = chi.URLParam(r, "id"), chi.URLParam(r, "stack")
	if err := stacks.ValidateName(stack); err != nil || !agents.ValidStackName(stack) {
		writeError(w, http.StatusBadRequest, "invalid stack name")
		return "", "", false
	}
	res := h.stackTargetReq(stacks.Target{Kind: stacks.TargetCluster, ID: clusterID}, stack)
	if !middleware.AllowedOn(r.Context(), rbac.PermStacksAdopt, res) {
		h.writeScopeDenied(w, r, rbac.PermStacksAdopt, res, "stack "+stack)
		return "", "", false
	}
	if _, err := h.Clusters.Get(r.Context(), clusterID); errors.Is(err, clusters.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return "", "", false
	} else if err != nil {
		slog.Error("adopt swarm stack: load cluster", "cluster", clusterID, "err", err)
		writeError(w, http.StatusInternalServerError, "load cluster failed")
		return "", "", false
	}
	if taken, err := h.stackNameTaken(r.Context(), stack); err != nil {
		writeError(w, http.StatusInternalServerError, "check stack name failed")
		return "", "", false
	} else if taken {
		writeJSON(w, http.StatusConflict, map[string]string{"error": errAdoptNameTaken.Error() + ": " + stack, "code": "stack_exists"})
		return "", "", false
	}
	if h.Roles != nil && !middleware.AccessOf(r.Context()).AllowsByStack(rbac.PermStacksAdopt, res) {
		for _, a := range h.Roles.AllAssignments() {
			if a.Scope.Type == rbac.ScopeStack && a.Scope.Value == stack {
				writeError(w, http.StatusConflict, "the name "+stack+" is reserved by a stack assignment; ask an admin")
				return "", "", false
			}
		}
	}
	return clusterID, stack, true
}

func writeAdoptBuildError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agents.ErrSwarmWriteRefused):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "swarm_adopt_refused"})
	default:
		writeSwarmWriteError(w, err)
	}
}

// SwarmAdoptPreview shows how an External stack would be written down as a
// Dockmesh stack: the compose file, the environment variables (names
// only), what the file can't carry and, per service, what a deploy from
// the file would change. Nothing is written.
//
//	GET /api/v1/clusters/{id}/stacks/{stack}/adopt
func (h *Handlers) SwarmAdoptPreview(w http.ResponseWriter, r *http.Request) {
	clusterID, stack, ok := h.adoptAllowed(w, r)
	if !ok {
		return
	}
	b, err := h.buildAdopt(r.Context(), clusterID, stack)
	if err != nil {
		writeAdoptBuildError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, b.view)
}

type swarmAdoptRequest struct {
	PlanHash string `json:"plan_hash"`
}

type swarmAdoptResponse struct {
	Stack     string                    `json:"stack"`
	ClusterID string                    `json:"cluster_id"`
	Manager   string                    `json:"manager"`
	Outcome   string                    `json:"outcome"`
	Error     string                    `json:"error,omitempty"`
	Services  []agents.SwarmWriteResult `json:"services"`
}

// SwarmAdopt makes an External stack a Dockmesh stack: its services get
// the spec hash label (a label-only update, no task is replaced), then
// the previewed compose and environment files are written and the stack
// is recorded on the cluster. Nothing is deployed.
//
//	POST /api/v1/clusters/{id}/stacks/{stack}/adopt
func (h *Handlers) SwarmAdopt(w http.ResponseWriter, r *http.Request) {
	var body swarmAdoptRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.PlanHash == "" {
		writeError(w, http.StatusBadRequest, "plan_hash from the preview is required")
		return
	}
	clusterID, stack, ok := h.adoptAllowed(w, r)
	if !ok {
		return
	}
	targetRelease, err := h.StackLocks.TryLock(stacklock.TargetKey(stack), stacklock.OpDeploy)
	if err != nil {
		writeStackBusy(w, err)
		return
	}
	defer targetRelease()
	release, err := h.StackLocks.TryLock(stacklock.ClusterKey(clusterID, stack), stacklock.OpDeploy)
	if err != nil {
		writeStackBusy(w, err)
		return
	}
	defer release()
	if taken, err := h.stackNameTaken(r.Context(), stack); err != nil {
		writeError(w, http.StatusInternalServerError, "check stack name failed")
		return
	} else if taken {
		writeJSON(w, http.StatusConflict, map[string]string{"error": errAdoptNameTaken.Error() + ": " + stack, "code": "stack_exists"})
		return
	}
	b, err := h.buildAdopt(r.Context(), clusterID, stack)
	if err != nil {
		writeAdoptBuildError(w, err)
		return
	}
	if b.view.PlanHash != body.PlanHash {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "the stack changed on the cluster since the preview; preview it again", "code": "swarm_plan_changed",
		})
		return
	}
	if len(b.view.Refusals) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "a deploy from the adopted file would be refused", "code": "swarm_refused", "refusals": b.view.Refusals,
		})
		return
	}
	if len(b.labels.Services) == 0 {
		writeError(w, http.StatusUnprocessableEntity, "no service of the stack can be adopted")
		return
	}
	managers, err := h.Clusters.WriteHosts(r.Context(), clusterID)
	if err != nil {
		writeSwarmWriteError(w, err)
		return
	}
	wctx := context.WithoutCancel(r.Context())
	var (
		resp    agents.SwarmAdoptResp
		manager string
	)
	for _, m := range managers {
		hostRelease, lerr := h.StackLocks.TryLock(stacklock.HostKey(m, stack), stacklock.OpDeploy)
		if lerr != nil {
			err = lerr
			continue
		}
		manager = m
		resp, err = h.swarmAdopt().SwarmAdopt(wctx, m, b.labels)
		hostRelease()
		if !writeNotSent(err) {
			break
		}
	}
	out := swarmAdoptResponse{Stack: stack, ClusterID: clusterID, Manager: manager, Services: nonNil(resp.Services)}
	for _, s := range resp.Services {
		if s.Action == agents.SwarmActionUpdated || s.Action == agents.SwarmActionUnknown {
			h.auditDetached(r, audit.ActionSwarmServiceUpdate, s.Name, map[string]any{
				"stack": stack, "cluster": clusterID, "manager": manager, "id": s.ID, "result": s.Action, "change": "adopt label",
			})
		}
	}
	labelled := err == nil && len(resp.Services) == len(b.labels.Services)
	for _, s := range resp.Services {
		labelled = labelled && (s.Action == agents.SwarmActionUpdated || s.Action == agents.SwarmActionExists)
	}
	if !labelled {
		if err != nil && !errors.Is(err, host.ErrSwarmWriteUnknown) {
			if writeStackBusy(w, err) {
				return
			}
			writeAdoptBuildError(w, err)
			return
		}
		out.Outcome = swarmOutcomePartial
		if err != nil {
			out.Outcome, out.Error = swarmOutcomeUnknown, err.Error()
		} else if len(resp.Services) > 0 {
			last := resp.Services[len(resp.Services)-1]
			out.Error = last.Name + ": " + last.Error
		}
		h.auditDetached(r, audit.ActionStackAdopt, stack, map[string]any{"cluster": clusterID, "manager": manager, "outcome": out.Outcome, "error": out.Error})
		writeJSON(w, http.StatusBadGateway, out)
		return
	}
	if _, err := h.Stacks.Create(stack, b.adopted.Compose, b.adopted.Env); err != nil {
		slog.Error("adopt swarm stack: write files", "stack", stack, "err", err)
		writeError(w, http.StatusInternalServerError, "the services were marked as Dockmesh's but writing the stack files failed; adopt it again")
		return
	}
	if err := h.Deployments.SetCluster(wctx, stack, clusterID, "deployed"); err != nil {
		slog.Error("adopt swarm stack: record target", "stack", stack, "err", err)
		if derr := h.Stacks.Delete(wctx, stack); derr != nil {
			slog.Warn("adopt swarm stack: drop files", "stack", stack, "err", derr)
		}
		writeError(w, http.StatusInternalServerError, "recording the stack's cluster failed; adopt it again")
		return
	}
	out.Outcome = swarmOutcomeApplied
	h.auditDetached(r, audit.ActionStackAdopt, stack, map[string]any{
		"cluster": clusterID, "manager": manager, "outcome": out.Outcome, "services": len(resp.Services),
		"not_carried": len(b.view.NotCarried),
	})
	writeJSON(w, http.StatusCreated, out)
}
