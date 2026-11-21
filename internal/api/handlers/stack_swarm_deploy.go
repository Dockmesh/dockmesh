package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"

	composetypes "github.com/compose-spec/compose-go/v2/types"
	"github.com/go-chi/chi/v5"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"

	"github.com/dockmesh/dockmesh/internal/agents"
	"github.com/dockmesh/dockmesh/internal/audit"
	"github.com/dockmesh/dockmesh/internal/clusters"
	"github.com/dockmesh/dockmesh/internal/compose"
	"github.com/dockmesh/dockmesh/internal/host"
	"github.com/dockmesh/dockmesh/internal/rbac"
	"github.com/dockmesh/dockmesh/internal/stacklock"
	"github.com/dockmesh/dockmesh/internal/stacks"
	"github.com/dockmesh/dockmesh/internal/swarmconvert"
)

// swarmOps is the part of the host registry the Swarm executor uses.
type swarmOps interface {
	SwarmDeploy(ctx context.Context, hostID string, req agents.SwarmDeployReq) (agents.SwarmDeployResp, error)
	SwarmServices(ctx context.Context, hostID string) (agents.SwarmServicesResp, error)
	RegistryAuthFor(ctx context.Context, hostID string, images []string) (map[string]string, error)
}

func (h *Handlers) swarm() swarmOps {
	if h.SwarmOps != nil {
		return h.SwarmOps
	}
	return h.Hosts
}

var errSwarmEnvNotActive = errors.New("a Swarm deploy uses the stack's active environment; make it the active one first")

// swarmBuild is a stack converted for one cluster from the snapshot bytes.
type swarmBuild struct {
	env  string
	plan *swarmconvert.Plan
	req  agents.SwarmDeployReq
	hash string
}

// buildSwarm converts snap the way a deploy to a cluster sends it: the
// snapshot's files only, the global env merged in, every service labelled
// with its spec hash and every network it creates with the stack. The
// preview and the deploy share it, so a confirmed plan hash names exactly
// what gets written.
func (h *Handlers) buildSwarm(ctx context.Context, name string, snap *stacks.Snapshot, env string) (*swarmBuild, error) {
	if env == "" {
		env = snap.ActiveOverride
	}
	if env != snap.ActiveOverride {
		return nil, errSwarmEnvNotActive
	}
	if env != "" && snap.OverrideFile == nil {
		return nil, fmt.Errorf("%w: compose.%s.yaml", compose.ErrEnvironmentNotFound, env)
	}
	envContent, err := h.mergeGlobalEnv(ctx, snap.Detail.Env)
	if err != nil {
		return nil, err
	}
	staged, cleanup, err := compose.NewStagingDir("", "dockmesh-swarm-"+name+"-")
	if err != nil {
		return nil, err
	}
	defer cleanup()
	root, err := os.OpenRoot(staged)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.WriteFile("compose.yaml", []byte(snap.Detail.Compose), 0o600); err != nil {
		return nil, err
	}
	if env != "" {
		if err := root.WriteFile("compose."+env+".yaml", snap.OverrideFile, 0o600); err != nil {
			return nil, err
		}
	}
	project, _, err := compose.MergeEnvironment(ctx, staged, name, envContent, env, false)
	if err != nil {
		return nil, err
	}
	unresolved, _, err := compose.MergeEnvironment(ctx, staged, name, envContent, env, true)
	if err != nil {
		return nil, err
	}
	b := &swarmBuild{env: env, plan: swarmconvert.NewPlan(project, unresolved, name)}
	b.plan.Refusals = append(b.plan.Refusals, reservedSwarmLabels(unresolved)...)
	if b.plan.Result == nil || len(b.plan.Refusals) > 0 {
		return b, nil
	}
	b.req = agents.SwarmDeployReq{Stack: name, ExternalNetworks: b.plan.Result.ExternalNetworks}
	for _, n := range b.plan.Result.Networks {
		if !strings.HasPrefix(n.Name, name+"_") {
			b.plan.Refusals = append(b.plan.Refusals, swarmconvert.Finding{
				Key:     "networks " + n.Name,
				Message: "A network a Swarm stack creates is named " + name + "_<network>. Use external: true to attach to an existing network.",
			})
			continue
		}
		n.Labels = maps.Clone(n.Labels)
		n.Labels[agents.LabelDockmeshStack] = name
		b.req.Networks = append(b.req.Networks, n)
	}
	for _, spec := range b.plan.Result.Services {
		for _, m := range spec.TaskTemplate.ContainerSpec.Mounts {
			if m.Type == mount.TypeBind && (m.Source == staged || strings.HasPrefix(m.Source, staged+string(filepath.Separator))) {
				b.plan.Refusals = append(b.plan.Refusals, swarmconvert.Finding{
					Service: strings.TrimPrefix(spec.Name, name+"_"), Key: "volumes " + m.Target,
					Message: "Relative bind mounts have no fixed place in a Swarm. Use a named volume, or an absolute path with a placement constraint.",
				})
			}
		}
		b.req.Services = append(b.req.Services, agents.SwarmServiceWrite{Spec: swarmconvert.WithSpecHash(spec)})
	}
	if len(b.plan.Refusals) > 0 {
		return b, nil
	}
	if err := b.req.Validate(); err != nil {
		b.plan.Refusals = append(b.plan.Refusals, swarmconvert.Finding{Key: "spec", Message: err.Error()})
		return b, nil
	}
	b.hash = planHash(b.req)
	return b, nil
}

// reservedSwarmLabels refuses labels only Dockmesh and compose set. On a
// task container they would make it count as one of Dockmesh's own or as
// part of a host stack on every node it runs on.
func reservedSwarmLabels(p *composetypes.Project) []swarmconvert.Finding {
	var out []swarmconvert.Finding
	check := func(service, key string, labels map[string]string) {
		for _, k := range slices.Sorted(maps.Keys(labels)) {
			if strings.HasPrefix(k, "dockmesh.") || strings.HasPrefix(k, "com.docker.compose.") {
				out = append(out, swarmconvert.Finding{Service: service, Key: key + " " + k, Message: "This label is reserved for Dockmesh; remove it from the compose file."})
			}
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.Services)) {
		svc := p.Services[name]
		check(name, "labels", svc.Labels)
		if svc.Deploy != nil {
			check(name, "deploy.labels", svc.Deploy.Labels)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.Networks)) {
		if n := p.Networks[name]; !bool(n.External) {
			check("", "networks "+name+" labels", n.Labels)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.Volumes)) {
		if v := p.Volumes[name]; !bool(v.External) {
			check("", "volumes "+name+" labels", v.Labels)
		}
	}
	return out
}

// planHash names what a deploy writes; the deploy only runs when the
// caller confirms the hash its preview showed.
func planHash(req agents.SwarmDeployReq) string {
	b, err := json.Marshal(struct {
		Networks []network.CreateRequest    `json:"networks"`
		External []string                   `json:"external"`
		Services []agents.SwarmServiceWrite `json:"services"`
	}{req.Networks, req.ExternalNetworks, req.Services})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func writeSwarmBuildError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, compose.ErrEnvironmentNotFound):
		writeError(w, http.StatusNotFound, "environment not found")
	case errors.Is(err, errSwarmEnvNotActive):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "swarm_environment_not_active"})
	default:
		writeError(w, http.StatusUnprocessableEntity, err.Error())
	}
}

type swarmDeployRequest struct {
	PlanHash string `json:"plan_hash"`
}

type swarmServiceOutcome struct {
	agents.SwarmWriteResult
	// Applied is set when the outcome had to be read back from the cluster.
	Applied      *bool  `json:"applied,omitempty"`
	RunningTasks uint64 `json:"running_tasks"`
	DesiredTasks uint64 `json:"desired_tasks"`
	UpdateState  string `json:"update_state,omitempty"`
}

type swarmDeployResponse struct {
	Stack     string `json:"stack"`
	ClusterID string `json:"cluster_id"`
	Manager   string `json:"manager"`
	// Outcome: applied, partial (a write failed, the ones before it
	// stay), unknown (the manager did not answer and the cluster could not
	// confirm every service).
	Outcome    string                    `json:"outcome"`
	CheckedVia string                    `json:"checked_via,omitempty"`
	Error      string                    `json:"error,omitempty"`
	Networks   []agents.SwarmWriteResult `json:"networks"`
	Services   []swarmServiceOutcome     `json:"services"`
	Warnings   []swarmconvert.Finding    `json:"warnings"`
}

const (
	swarmOutcomeApplied = "applied"
	swarmOutcomePartial = "partial"
	swarmOutcomeUnknown = "unknown"
)

// SwarmDeploy deploys a stack to a Swarm cluster through one of its
// verified managers. The caller confirms the plan its preview showed by
// its hash; anything that changed since answers 409. A write that got no
// answer is never sent again to another manager: the outcome is read back
// from the cluster and reported as it is.
//
//	POST /api/v1/stacks/{name}/swarm-deploy?cluster=<id>[&environment=<env>][&confirm_external=1]
func (h *Handlers) SwarmDeploy(w http.ResponseWriter, r *http.Request) {
	if h.Clusters == nil || h.Deployments == nil || h.Hosts == nil && h.SwarmOps == nil {
		writeError(w, http.StatusServiceUnavailable, "clusters are not available")
		return
	}
	name := chi.URLParam(r, "name")
	id := r.URL.Query().Get("cluster")
	if id == "" {
		writeError(w, http.StatusBadRequest, "cluster is required")
		return
	}
	var body swarmDeployRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil || body.PlanHash == "" {
		writeError(w, http.StatusBadRequest, "plan_hash from the preview is required")
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
		slog.Error("swarm deploy: load cluster", "cluster", id, "err", err)
		writeError(w, http.StatusInternalServerError, "load cluster failed")
		return
	}
	if !h.Stacks.Has(name) {
		writeStackError(w, stacks.ErrNotFound)
		return
	}
	targetRelease, err := h.StackLocks.TryLock(stacklock.TargetKey(name), stacklock.OpDeploy)
	if err != nil {
		writeStackBusy(w, err)
		return
	}
	defer targetRelease()
	release, err := h.StackLocks.TryLock(stacklock.ClusterKey(id, name), stacklock.OpDeploy)
	if err != nil {
		writeStackBusy(w, err)
		return
	}
	defer release()
	if h.refuseOtherTarget(w, r, name, id) {
		return
	}
	if h.refuseMigrating(w, r, name) {
		return
	}
	snap, ok := h.checkStack(w, r, name)
	if !ok {
		return
	}
	if h.refuseDependencyTargets(w, r, name, id) {
		return
	}
	b, err := h.buildSwarm(r.Context(), name, snap, r.URL.Query().Get("environment"))
	if err != nil {
		writeSwarmBuildError(w, err)
		return
	}
	if len(b.plan.Refusals) > 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]any{
			"error": "the stack can't be deployed to a Swarm", "code": "swarm_refused", "refusals": b.plan.Refusals,
		})
		return
	}
	if b.hash != body.PlanHash {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error": "the stack or its environment changed since the preview; preview it again", "code": "swarm_plan_changed",
		})
		return
	}
	managers, err := h.Clusters.WriteHosts(r.Context(), id)
	if err != nil {
		writeSwarmWriteError(w, err)
		return
	}

	// The write must not stop half-way because the browser went away.
	wctx := context.WithoutCancel(r.Context())
	var (
		resp      agents.SwarmDeployResp
		manager   string
		confirmed bool
	)
	for _, m := range managers {
		hostRelease, lerr := h.StackLocks.TryLock(stacklock.HostKey(m, name), stacklock.OpDeploy)
		if lerr != nil {
			err = lerr
			continue
		}
		req, aerr := h.withRegistryAuth(wctx, m, b.req)
		if aerr != nil {
			hostRelease()
			slog.Error("swarm deploy: registry auth", "stack", name, "err", aerr)
			writeError(w, http.StatusInternalServerError, "resolving registry credentials failed")
			return
		}
		if !confirmed {
			if !h.confirmStack(w, r, snap) {
				hostRelease()
				return
			}
			h.clearGitPending(wctx, name)
			confirmed = true
		}
		manager = m
		resp, err = h.swarm().SwarmDeploy(wctx, m, req)
		hostRelease()
		if !writeNotSent(err) {
			break
		}
		slog.Info("swarm deploy: manager did not take the write, trying the next", "stack", name, "manager", m, "err", err)
	}
	if err != nil && !errors.Is(err, host.ErrSwarmWriteUnknown) {
		if confirmed {
			h.auditDetached(r, audit.ActionStackDeploy, name, map[string]any{
				"cluster": id, "manager": manager, "outcome": "not_written", "error": err.Error(),
			})
		}
		if writeStackBusy(w, err) {
			return
		}
		writeSwarmWriteError(w, err)
		return
	}

	out := swarmDeployResponse{
		Stack: name, ClusterID: id, Manager: manager,
		Networks: nonNil(resp.Networks), Warnings: nonNil(b.plan.Warnings),
	}
	for _, s := range resp.Services {
		out.Services = append(out.Services, swarmServiceOutcome{SwarmWriteResult: s})
	}
	switch {
	case err != nil:
		out.Outcome, out.Error = swarmOutcomeUnknown, err.Error()
	case resp.Error != "":
		out.Outcome, out.Error = swarmOutcomePartial, resp.Error
	default:
		out.Outcome = swarmOutcomeApplied
	}
	h.readBack(wctx, id, b.req, &out)

	if err := h.Deployments.SetCluster(wctx, name, id, "deployed"); err != nil {
		slog.Warn("set stack deployment", "stack", name, "cluster", id, "err", err)
	}
	if err := h.Stacks.RecordOverride(name, b.env); err != nil {
		slog.Warn("record deployed environment override", "stack", name, "err", err)
	}
	nets := out.Networks
	if out.Outcome == swarmOutcomeUnknown && len(nets) == 0 {
		for _, n := range b.req.Networks {
			nets = append(nets, agents.SwarmWriteResult{Name: n.Name, Action: agents.SwarmActionUnknown})
		}
	}
	h.auditSwarmWrites(r, name, id, manager, nets, "network")
	for _, s := range out.Services {
		h.auditSwarmService(r, name, id, manager, s)
	}
	h.auditDetached(r, audit.ActionStackDeploy, name, map[string]any{
		"services": len(out.Services), "cluster": id, "manager": manager, "outcome": out.Outcome,
	})
	status := http.StatusOK
	if out.Outcome != swarmOutcomeApplied {
		status = http.StatusBadGateway
	}
	out.Services = nonNil(out.Services)
	writeJSON(w, status, out)
}

// writeNotSent reports whether err proves the manager never got the write:
// it is offline, or its agent answered without writing.
func writeNotSent(err error) bool {
	var busy *stacklock.BusyError
	return errors.Is(err, host.ErrAgentOffline) || errors.Is(err, agents.ErrNotSwarmManager) ||
		errors.Is(err, host.ErrAgentNoSwarmWrite) || errors.As(err, &busy)
}

func writeSwarmWriteError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, agents.ErrSwarmWriteRefused):
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error(), "code": "swarm_write_refused"})
	case errors.Is(err, clusters.ErrNoWriter), errors.Is(err, host.ErrAgentOffline),
		errors.Is(err, agents.ErrNotSwarmManager), errors.Is(err, host.ErrAgentNoSwarmWrite), errors.Is(err, host.ErrNoDocker):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		slog.Warn("swarm write", "err", err)
		writeError(w, http.StatusBadGateway, err.Error())
	}
}

func (h *Handlers) withRegistryAuth(ctx context.Context, manager string, req agents.SwarmDeployReq) (agents.SwarmDeployReq, error) {
	images := make([]string, 0, len(req.Services))
	for _, s := range req.Services {
		images = append(images, s.Spec.TaskTemplate.ContainerSpec.Image)
	}
	auth, err := h.swarm().RegistryAuthFor(ctx, manager, images)
	if err != nil {
		return req, err
	}
	out := req
	out.Services = make([]agents.SwarmServiceWrite, len(req.Services))
	for i, s := range req.Services {
		s.RegistryAuth = auth[s.Spec.TaskTemplate.ContainerSpec.Image]
		out.Services[i] = s
	}
	return out, nil
}

// readBack fills in the task counts of the stack's services, read through
// the cluster's current reader. With an unknown outcome it also decides,
// per service, whether the cluster runs the spec this deploy wrote.
func (h *Handlers) readBack(ctx context.Context, clusterID string, req agents.SwarmDeployReq, out *swarmDeployResponse) {
	reader, err := h.Clusters.ReadHost(ctx, clusterID)
	if err != nil {
		slog.Info("swarm deploy: read back", "cluster", clusterID, "err", err)
		return
	}
	list, err := h.swarm().SwarmServices(ctx, reader)
	if err != nil {
		slog.Info("swarm deploy: read back", "cluster", clusterID, "err", err)
		return
	}
	live := map[string]agents.SwarmService{}
	for _, s := range list.Services {
		if s.Stack == req.Stack {
			live[s.Name] = s
		}
	}
	if out.Outcome == swarmOutcomeUnknown {
		out.CheckedVia = reader
		written := map[string]int{}
		for i, s := range out.Services {
			written[s.Name] = i
		}
		all := !list.Truncated
		for _, w := range req.Services {
			s, ok := live[w.Spec.Name]
			applied := ok && s.SpecHash == w.Spec.Labels[agents.LabelSpecHash]
			all = all && applied
			i, seen := written[w.Spec.Name]
			if !seen {
				out.Services = append(out.Services, swarmServiceOutcome{SwarmWriteResult: agents.SwarmWriteResult{Name: w.Spec.Name, Action: agents.SwarmActionUnknown}})
				i = len(out.Services) - 1
			}
			out.Services[i].Applied = &applied
		}
		if all {
			out.Outcome = swarmOutcomeApplied
		}
	}
	for i := range out.Services {
		if s, ok := live[out.Services[i].Name]; ok {
			out.Services[i].RunningTasks = s.RunningTasks
			out.Services[i].DesiredTasks = s.DesiredTasks
			out.Services[i].UpdateState = s.UpdateState
		}
	}
}

func (h *Handlers) auditSwarmWrites(r *http.Request, stack, clusterID, manager string, results []agents.SwarmWriteResult, kind string) {
	for _, res := range results {
		d := map[string]any{"stack": stack, "cluster": clusterID, "manager": manager, "id": res.ID, "result": res.Action}
		switch {
		case kind == "network" && (res.Action == agents.SwarmActionCreated || res.Action == agents.SwarmActionUnknown):
			h.auditDetached(r, audit.ActionSwarmNetworkCreate, res.Name, d)
		case kind == "network" && res.Action == agents.SwarmActionRemoved:
			h.auditDetached(r, audit.ActionSwarmNetworkRemove, res.Name, d)
		case kind == "service" && res.Action == agents.SwarmActionCreated:
			h.auditDetached(r, audit.ActionSwarmServiceCreate, res.Name, d)
		case kind == "service" && (res.Action == agents.SwarmActionUpdated || res.Action == agents.SwarmActionUnknown):
			h.auditDetached(r, audit.ActionSwarmServiceUpdate, res.Name, d)
		case kind == "service" && res.Action == agents.SwarmActionRemoved:
			h.auditDetached(r, audit.ActionSwarmServiceRemove, res.Name, d)
		}
	}
}

func (h *Handlers) auditSwarmService(r *http.Request, stack, clusterID, manager string, s swarmServiceOutcome) {
	if s.Applied == nil {
		h.auditSwarmWrites(r, stack, clusterID, manager, []agents.SwarmWriteResult{s.SwarmWriteResult}, "service")
		return
	}
	if s.Action != agents.SwarmActionCreated && s.Action != agents.SwarmActionUpdated && s.Action != agents.SwarmActionUnknown {
		return
	}
	h.auditDetached(r, audit.ActionSwarmServiceUpdate, s.Name, map[string]any{
		"stack": stack, "cluster": clusterID, "manager": manager, "id": s.ID, "result": s.Action, "applied": *s.Applied,
	})
}

// refuseOtherTarget answers 409 when the stack is deployed anywhere but
// cluster id. Moving a stack from a host to a cluster is not offered.
func (h *Handlers) refuseOtherTarget(w http.ResponseWriter, r *http.Request, name, clusterID string) bool {
	d, err := h.Deployments.Get(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "load stack deployment failed")
		return true
	}
	if d == nil || d.TargetKind == stacks.TargetCluster && d.TargetID == clusterID {
		return false
	}
	msg := "stack " + name + " runs on a host; moving a stack from a host to a Swarm is not supported"
	if d.TargetKind == stacks.TargetHost && h.hostVisible(r, d.TargetID) {
		msg = "stack " + name + " runs on host " + d.TargetID + "; moving a stack from a host to a Swarm is not supported"
	}
	if d.TargetKind == stacks.TargetCluster {
		msg = "stack " + name + " runs on another Swarm cluster"
	}
	writeJSON(w, http.StatusConflict, map[string]string{"error": msg, "code": "stack_other_target"})
	return true
}

// refuseDependencyTargets answers 409 unless every stack name depends on
// is already deployed to the same cluster.
func (h *Handlers) refuseDependencyTargets(w http.ResponseWriter, r *http.Request, name, clusterID string) bool {
	if h.Dependencies == nil {
		return false
	}
	order, err := h.Dependencies.TopoOrder(r.Context(), name)
	if err != nil {
		if errors.Is(err, stacks.ErrDependencyCycle) {
			writeError(w, http.StatusUnprocessableEntity, err.Error())
			return true
		}
		writeError(w, http.StatusInternalServerError, "resolve dependencies failed")
		return true
	}
	for _, dep := range order[:len(order)-1] {
		d, err := h.Deployments.Get(r.Context(), dep)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "load stack deployment failed")
			return true
		}
		if d != nil && d.TargetKind == stacks.TargetCluster && d.TargetID == clusterID {
			continue
		}
		msg := "Dependencies must deploy to the same target. " + dep + " is not deployed to this cluster; deploy it there first."
		if d != nil && d.TargetKind == stacks.TargetHost {
			msg = "Dependencies must deploy to the same target. " + dep + " runs on a host."
			if h.hostVisible(r, d.TargetID) {
				msg = "Dependencies must deploy to the same target. " + dep + " runs on host " + d.TargetID + "."
			}
		}
		writeJSON(w, http.StatusConflict, map[string]string{"error": msg, "code": "dependency_other_target"})
		return true
	}
	return false
}
