package handlers

import (
	"context"
	"errors"
	"fmt"
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
	"github.com/dockmesh/dockmesh/internal/swarmread"
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

// clusterAccessView is what the caller may see of a cluster's access:
// every count, and the names only where the caller could list them
// elsewhere (users.view on everything; other users' tokens only with
// tokens.manage_others).
type clusterAccessView struct {
	clusters.Access
	Counts   map[string]int `json:"counts"`
	Complete bool           `json:"complete"`
}

func (h *Handlers) viewClusterAccess(r *http.Request, a clusters.Access) clusterAccessView {
	acc := middleware.AccessOf(r.Context())
	v := clusterAccessView{Access: a, Counts: a.Counts(), Complete: true}
	if !acc.HoldsOn(rbac.PermUsersView, rbac.Global, h.hostTags) {
		v.Assignments, v.Invites, v.Mappings = []clusters.ScopedAssignment{}, []clusters.ScopedInvite{}, []clusters.ScopedMapping{}
		v.Complete = len(a.Assignments)+len(a.Invites)+len(a.Mappings) == 0
	}
	if !acc.HoldsOn(rbac.PermTokensManageOthers, rbac.Global, h.hostTags) {
		own := []clusters.ScopedToken{}
		for _, t := range a.Tokens {
			if ownsClusterToken(r, t) {
				own = append(own, t)
			}
		}
		v.Complete = v.Complete && len(own) == len(a.Tokens)
		v.Tokens = own
	}
	return v
}

// ownsClusterToken applies RevokeAPIToken's self rule: a user owns the
// tokens they created, an API token only itself, and a caller without a
// known owner owns none.
func ownsClusterToken(r *http.Request, t clusters.ScopedToken) bool {
	me := callerTokenOwner(r)
	if me == "" || t.OwnerID != me {
		return false
	}
	self := middleware.APITokenID(r.Context())
	return self == 0 || self == t.ID
}

// errClusterAccessAboveOwn refuses a cluster delete that would remove
// access the caller could not have granted.
var errClusterAccessAboveOwn = errors.New("cluster access above own")

// checkClusterAccess applies the rule of checkGrant to deleting the
// cluster's access: every assignment and group mapping role on it must be
// one the caller may manage there, and other users' tokens need
// tokens.manage_others.
func (h *Handlers) checkClusterAccess(r *http.Request, id string, a clusters.Access) error {
	scope := rbac.Scope{Type: rbac.ScopeCluster, Host: id}
	check := func(what, role string) error {
		if _, known := h.Roles.Role(role); !known {
			return nil
		}
		if err := h.checkGrant(r, role, scope); err != nil {
			return fmt.Errorf("%w: %s has the role %s, which you can't manage (%s)", errClusterAccessAboveOwn, what, role, strings.TrimPrefix(err.Error(), errGrantExceedsCaller.Error()+": "))
		}
		return nil
	}
	for _, x := range a.Assignments {
		if err := check("a role assignment", x.Role); err != nil {
			return err
		}
	}
	for _, x := range a.Mappings {
		if err := check("a "+strings.ToUpper(x.Kind)+" group mapping", x.Role); err != nil {
			return err
		}
	}
	manageOthers := middleware.AccessOf(r.Context()).HoldsOn(rbac.PermTokensManageOthers, rbac.Global, h.hostTags)
	for _, t := range a.Tokens {
		if !manageOthers && !ownsClusterToken(r, t) {
			return fmt.Errorf("%w: an API token of another user is restricted to the cluster, and revoking it needs tokens.manage_others", errClusterAccessAboveOwn)
		}
	}
	return nil
}

// ClusterAccess lists the role assignments, invites, IdP group mappings
// and API tokens that deleting the cluster removes or revokes.
//
//	GET /api/v1/clusters/{id}/access
func (h *Handlers) ClusterAccess(w http.ResponseWriter, r *http.Request) {
	if h.Clusters == nil {
		writeError(w, http.StatusServiceUnavailable, "clusters are not available")
		return
	}
	a, err := h.Clusters.AccessOf(r.Context(), chi.URLParam(r, "id"))
	if errors.Is(err, clusters.ErrNotFound) {
		writeError(w, http.StatusNotFound, err.Error())
		return
	} else if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, h.viewClusterAccess(r, a))
}

// DeleteCluster removes a cluster, its memberships and the access on its
// scope; tokens restricted to it are revoked. The Swarm itself is not
// touched.
//
//	DELETE /api/v1/clusters/{id}
func (h *Handlers) DeleteCluster(w http.ResponseWriter, r *http.Request) {
	if h.Clusters == nil {
		writeError(w, http.StatusServiceUnavailable, "clusters are not available")
		return
	}
	if h.Roles == nil {
		writeError(w, http.StatusServiceUnavailable, "roles store unavailable")
		return
	}
	id := chi.URLParam(r, "id")
	c, removed, err := h.Clusters.Delete(r.Context(), id, func(a clusters.Access) error {
		return h.checkClusterAccess(r, id, a)
	})
	switch {
	case err == nil:
	case errors.Is(err, clusters.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
		return
	case errors.Is(err, errClusterAccessAboveOwn):
		h.AuditPermDenied(r, routePerm(r))
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "you can't delete this cluster: " + strings.TrimPrefix(err.Error(), errClusterAccessAboveOwn.Error()+": "),
			"code":  "cluster_access_above_own",
		})
		return
	default:
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	hosts := make([]string, 0, len(c.Members))
	for _, m := range c.Members {
		hosts = append(hosts, m.HostID)
	}
	tokens := make([]int64, 0, len(removed.Tokens))
	for _, t := range removed.Tokens {
		tokens = append(tokens, t.ID)
	}
	h.audit(r, audit.ActionClusterDelete, id, map[string]any{
		"name": c.Name, "origin": c.OriginHost, "members": hosts,
		"removed": removed.Counts(), "revoked_tokens": tokens,
	})
	if err := h.reloadRoles(r.Context()); err != nil {
		slog.Error("reload roles after cluster delete", "cluster", id, "err", err)
		writeError(w, http.StatusInternalServerError, "the cluster and its access were deleted, but the permission cache could not be reloaded; restart Dockmesh so the removed roles stop applying")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// reloadRoles reloads the role cache, once more after a failure.
func (h *Handlers) reloadRoles(ctx context.Context) error {
	if err := h.Roles.Load(ctx); err == nil {
		return nil
	}
	return h.Roles.Load(ctx)
}

// ClusterScope records the cluster of /clusters/{id}/... read routes as
// the request's resource, so RequirePerm checks the route's permission on
// that cluster's scope.
func (h *Handlers) ClusterScope(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		res := rbac.Resource{ClusterID: chi.URLParam(r, "id")}
		next.ServeHTTP(w, r.WithContext(middleware.WithResource(r.Context(), res)))
	})
}

// clusterReadHost resolves the host a cluster is read through and writes
// the error response when there is none.
func (h *Handlers) clusterReadHost(w http.ResponseWriter, r *http.Request) (string, bool) {
	if h.Clusters == nil || h.Hosts == nil {
		writeError(w, http.StatusServiceUnavailable, "clusters are not available")
		return "", false
	}
	hostID, err := h.Clusters.ReadHost(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeClusterReadError(w, err)
		return "", false
	}
	return hostID, true
}

func writeClusterReadError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, clusters.ErrNotFound):
		writeError(w, http.StatusNotFound, err.Error())
	case errors.Is(err, agents.ErrSwarmServiceNotFound):
		writeError(w, http.StatusNotFound, "service not found")
	case errors.Is(err, clusters.ErrUnreadable):
		slog.Info("cluster read", "err", err)
		writeError(w, http.StatusServiceUnavailable, clusters.ErrUnreadable.Error())
	case errors.Is(err, agents.ErrNotSwarmManager),
		errors.Is(err, host.ErrAgentOffline), errors.Is(err, host.ErrNoDocker),
		errors.Is(err, host.ErrAgentNoSwarmNodes), errors.Is(err, host.ErrAgentNoSwarmRead),
		errors.Is(err, host.ErrAgentNoSwarmLogs):
		writeError(w, http.StatusServiceUnavailable, err.Error())
	default:
		slog.Warn("cluster read", "err", err)
		writeError(w, http.StatusBadGateway, "cannot read the cluster's Swarm state")
	}
}

type clusterNodesResponse struct {
	ReadHost string             `json:"read_host"`
	Nodes    []agents.SwarmNode `json:"nodes"`
}

// ClusterNodes lists every node of the cluster's Swarm, including nodes
// without an agent, read through the cluster's manager.
//
//	GET /api/v1/clusters/{id}/nodes
func (h *Handlers) ClusterNodes(w http.ResponseWriter, r *http.Request) {
	hostID, ok := h.clusterReadHost(w, r)
	if !ok {
		return
	}
	nodes, err := h.Hosts.SwarmNodes(r.Context(), hostID)
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, clusterNodesResponse{ReadHost: hostID, Nodes: nodes})
}

type clusterServicesResponse struct {
	ReadHost string `json:"read_host"`
	agents.SwarmServicesResp
}

// ClusterServices lists the cluster's Swarm services with task counts.
//
//	GET /api/v1/clusters/{id}/services
func (h *Handlers) ClusterServices(w http.ResponseWriter, r *http.Request) {
	hostID, ok := h.clusterReadHost(w, r)
	if !ok {
		return
	}
	res, err := h.Hosts.SwarmServices(r.Context(), hostID)
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, clusterServicesResponse{ReadHost: hostID, SwarmServicesResp: res})
}

type clusterServiceResponse struct {
	ReadHost string `json:"read_host"`
	agents.SwarmServiceDetail
}

// ClusterService inspects one service. Env comes as variable names only,
// secrets and configs as references; no value is ever returned.
//
//	GET /api/v1/clusters/{id}/services/{service}
func (h *Handlers) ClusterService(w http.ResponseWriter, r *http.Request) {
	service := chi.URLParam(r, "service")
	if !swarmread.ValidRef(service) {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	hostID, ok := h.clusterReadHost(w, r)
	if !ok {
		return
	}
	d, err := h.Hosts.SwarmService(r.Context(), hostID, service)
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, clusterServiceResponse{ReadHost: hostID, SwarmServiceDetail: d})
}

type clusterTasksResponse struct {
	ReadHost string `json:"read_host"`
	agents.SwarmTasksResp
}

// ClusterTasks lists the cluster's tasks, newest first, only those of the
// service query parameter when it is set.
//
//	GET /api/v1/clusters/{id}/tasks
func (h *Handlers) ClusterTasks(w http.ResponseWriter, r *http.Request) {
	service := r.URL.Query().Get("service")
	if service != "" && !swarmread.ValidRef(service) {
		writeError(w, http.StatusBadRequest, "invalid service id")
		return
	}
	hostID, ok := h.clusterReadHost(w, r)
	if !ok {
		return
	}
	res, err := h.Hosts.SwarmTasks(r.Context(), hostID, service)
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, clusterTasksResponse{ReadHost: hostID, SwarmTasksResp: res})
}

type clusterNetworksResponse struct {
	ReadHost string `json:"read_host"`
	agents.SwarmNetworksResp
}

// ClusterNetworks lists the networks with Swarm scope (overlay, ingress).
//
//	GET /api/v1/clusters/{id}/networks
func (h *Handlers) ClusterNetworks(w http.ResponseWriter, r *http.Request) {
	hostID, ok := h.clusterReadHost(w, r)
	if !ok {
		return
	}
	res, err := h.Hosts.SwarmNetworks(r.Context(), hostID)
	if err != nil {
		writeClusterReadError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, clusterNetworksResponse{ReadHost: hostID, SwarmNetworksResp: res})
}
